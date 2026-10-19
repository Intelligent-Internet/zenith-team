//go:build integration

package wiki_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/wiki"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const lockTestTimeout = 5 * time.Second

// holdAutosaveLocks takes what an autosave in flight holds: the Company and the
// acting member FOR SHARE, then the page and its draft FOR UPDATE.
func holdAutosaveLocks(t *testing.T, h wikiHarness, pageID uuid.UUID) pgx.Tx {
	t.Helper()
	ctx := t.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`SELECT id FROM companies WHERE id = $1 FOR SHARE`, []any{h.owner.CompanyID}},
		{
			`SELECT user_id FROM company_members WHERE company_id = $1 AND user_id = $2 FOR SHARE`,
			[]any{h.owner.CompanyID, h.owner.UserID},
		},
		{`SELECT id FROM wiki_pages WHERE id = $1 FOR UPDATE`, []any{pageID}},
		{`SELECT page_id FROM wiki_canvas_drafts WHERE page_id = $1 FOR UPDATE`, []any{pageID}},
	} {
		if _, err := tx.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("hold autosave lock %q: %v", statement.sql, err)
		}
	}
	return tx
}

// waitForBlockedQuery returns once a backend is waiting on a lock while running
// a statement whose text contains queryName (the sqlc `-- name:` comment).
func waitForBlockedQuery(t *testing.T, ctx context.Context, pool *pgxpool.Pool, queryName string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(lockTestTimeout)
	defer deadline.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE datname = current_database() AND pid <> pg_backend_pid()
				  AND wait_event_type = 'Lock' AND query LIKE '%' || $1 || '%')`, queryName).Scan(&waiting); err != nil {
			t.Fatalf("observe %s lock wait: %v", queryName, err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for %s lock: %v", queryName, ctx.Err())
		case <-deadline.C:
			t.Fatalf("no backend waited on a lock in %s", queryName)
		case <-ticker.C:
		}
	}
}

// receive returns the goroutine's result or fails when it does not arrive.
func receive(t *testing.T, name string, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(lockTestTimeout):
		t.Fatalf("%s did not finish", name)
		return nil
	}
}

// Page, collection and archive writes need the Company and the actor's
// membership only to fence revocation, so they share those locks with an
// autosave instead of queueing behind it.
func TestWikiMutationsDoNotWaitForAnAutosaveInFlight(t *testing.T) {
	h := startWiki(t)
	s := h.service.DefaultWikiService
	ctx := t.Context()
	busy, err := s.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Busy", Body: "autosaved"})
	if err != nil {
		t.Fatal(err)
	}
	idle, err := s.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Idle", Body: "quiet"})
	if err != nil {
		t.Fatal(err)
	}
	idleDraft, err := s.ReadDocument(ctx, h.owner, idle.ID, wiki.DocumentReadInput{Since: -1})
	if err != nil {
		t.Fatal(err)
	}
	renamable, err := s.CreateCollection(ctx, h.owner, "Rename me")
	if err != nil {
		t.Fatal(err)
	}
	doomed, err := s.CreateCollection(ctx, h.owner, "Delete me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Filed", CollectionID: &doomed.ID}); err != nil {
		t.Fatal(err)
	}

	holdAutosaveLocks(t, h, busy.ID)

	opCtx, cancel := context.WithTimeout(ctx, lockTestTimeout)
	defer cancel()
	for _, step := range []struct {
		name string
		run  func() error
	}{
		{"CreatePage", func() error {
			_, err := s.CreatePage(opCtx, h.owner, wiki.CreatePageInput{Title: "New", CollectionID: &renamable.ID})
			return err
		}},
		{"PublishPage", func() error {
			_, err := s.PublishPage(opCtx, h.owner, idle.ID, wiki.PublishPageInput{
				ExpectedRevision: idle.Revision, ExpectedSequence: idleDraft.Sequence,
			})
			return err
		}},
		{"ArchivePage and RestorePage", func() error {
			current, err := s.GetPage(opCtx, h.owner, idle.ID)
			if err != nil {
				return err
			}
			archived, err := s.ArchivePage(opCtx, h.owner, idle.ID, current.Revision)
			if err != nil {
				return err
			}
			_, err = s.RestorePage(opCtx, h.owner, idle.ID, archived.Revision)
			return err
		}},
		{"RenameCollection", func() error {
			_, err := s.RenameCollection(opCtx, h.owner, renamable.ID, renamable.Revision, "Renamed")
			return err
		}},
		{"CreateCollection", func() error {
			_, err := s.CreateCollection(opCtx, h.owner, "Fresh")
			return err
		}},
		{"DeleteCollection", func() error {
			return s.DeleteCollection(opCtx, h.owner, doomed.ID, doomed.Revision)
		}},
	} {
		if err := step.run(); err != nil {
			t.Fatalf("%s behind an autosave = %v, want it to complete", step.name, err)
		}
	}
}

// The founder's research batch locks only its own pages: an autosave of any
// other page neither delays it nor is delayed by it.
func TestAgentWriteDoesNotWaitForAnUnrelatedAutosave(t *testing.T) {
	h := startAgentWrite(t)
	ctx := t.Context()
	busy, err := h.service.DefaultWikiService.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Busy", Body: "autosaved"})
	if err != nil {
		t.Fatal(err)
	}
	holdAutosaveLocks(t, h.wikiHarness, busy.ID)

	opCtx, cancel := context.WithTimeout(ctx, lockTestTimeout)
	defer cancel()
	if _, err := h.writes.Write(opCtx, h.owner, h.research, wiki.AgentWriteInput{
		Pages: []wiki.AgentWritePageInput{page("one", "One", "first"), page("two", "Two", "see [[one]]")},
	}); err != nil {
		t.Fatalf("research write behind an unrelated autosave = %v, want it to complete", err)
	}
}

func TestResearchBatchDoesNotBlockEditors(t *testing.T) {
	h := startAgentWrite(t)
	s := h.service.DefaultWikiService
	ctx := t.Context()
	editing, err := s.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Editing", Body: "text"})
	if err != nil {
		t.Fatal(err)
	}

	// A research write in flight holds the Company's research-write lock.
	holder, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
	if err := generated.New(holder).LockWikiResearchWrites(ctx, h.owner.CompanyID); err != nil {
		t.Fatal(err)
	}
	written := make(chan error, 1)
	go func() {
		_, err := h.writes.Write(ctx, h.owner, h.research, wiki.AgentWriteInput{
			Pages: []wiki.AgentWritePageInput{page("queued", "Queued", "text")},
		})
		written <- err
	}()
	waitForBlockedQuery(t, ctx, h.pool, "LockWikiResearchWrites")

	opCtx, cancel := context.WithTimeout(ctx, lockTestTimeout)
	defer cancel()
	if _, err := s.ReserveBlockIDs(opCtx, h.owner, editing.ID, 1); err != nil {
		t.Fatalf("autosave path behind a research write = %v", err)
	}
	if _, err := s.CreatePage(opCtx, h.owner, wiki.CreatePageInput{Title: "Meanwhile"}); err != nil {
		t.Fatalf("CreatePage behind a research write = %v", err)
	}
	if _, err := s.CreateCollection(opCtx, h.owner, "Meanwhile"); err != nil {
		t.Fatalf("CreateCollection behind a research write = %v", err)
	}

	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, "research write", written); err != nil {
		t.Fatalf("queued research write = %v", err)
	}
}

// A writer that already passed the middleware must still see a revocation or
// demotion that commits while it waits for the Company: lifecycle writers take
// the Company FOR UPDATE, and the Wiki's FOR SHARE queues behind it.
func TestWikiMutationsRecheckMembershipAfterConcurrentLifecycleChange(t *testing.T) {
	h := startAgentWrite(t)
	s := h.service.DefaultWikiService
	ctx := t.Context()

	type operation struct {
		name    string
		prepare func(t *testing.T, tag string) func(context.Context) error
	}
	operations := []operation{
		{"CreatePage", func(*testing.T, string) func(context.Context) error {
			return func(ctx context.Context) error {
				_, err := s.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Raced"})
				return err
			}
		}},
		{"CreateCollection", func(_ *testing.T, tag string) func(context.Context) error {
			return func(ctx context.Context) error {
				_, err := s.CreateCollection(ctx, h.owner, "Raced "+tag)
				return err
			}
		}},
		{"RenameCollection", func(t *testing.T, tag string) func(context.Context) error {
			collection, err := s.CreateCollection(ctx, h.owner, "Rename raced "+tag)
			if err != nil {
				t.Fatal(err)
			}
			return func(ctx context.Context) error {
				_, err := s.RenameCollection(ctx, h.owner, collection.ID, collection.Revision, "Renamed")
				return err
			}
		}},
		{"DeleteCollection", func(t *testing.T, tag string) func(context.Context) error {
			collection, err := s.CreateCollection(ctx, h.owner, "Delete raced "+tag)
			if err != nil {
				t.Fatal(err)
			}
			return func(ctx context.Context) error {
				return s.DeleteCollection(ctx, h.owner, collection.ID, collection.Revision)
			}
		}},
		{"ArchivePage", func(t *testing.T, _ string) func(context.Context) error {
			created, err := s.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Archive raced"})
			if err != nil {
				t.Fatal(err)
			}
			return func(ctx context.Context) error {
				_, err := s.ArchivePage(ctx, h.owner, created.ID, created.Revision)
				return err
			}
		}},
		{"PublishPage", func(t *testing.T, _ string) func(context.Context) error {
			created, err := s.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Publish raced", Body: "text"})
			if err != nil {
				t.Fatal(err)
			}
			draft, err := s.ReadDocument(ctx, h.owner, created.ID, wiki.DocumentReadInput{Since: -1})
			if err != nil {
				t.Fatal(err)
			}
			return func(ctx context.Context) error {
				_, err := s.PublishPage(ctx, h.owner, created.ID, wiki.PublishPageInput{
					ExpectedRevision: created.Revision, ExpectedSequence: draft.Sequence,
				})
				return err
			}
		}},
		{"AgentWrite", func(*testing.T, string) func(context.Context) error {
			return func(ctx context.Context) error {
				_, err := h.writes.Write(ctx, h.owner, h.research, wiki.AgentWriteInput{
					Pages: []wiki.AgentWritePageInput{page("raced", "Raced", "text")},
				})
				return err
			}
		}},
	}
	changes := []struct {
		name, role, status string
		want               error
	}{
		{"revoked", "owner", "revoked", wiki.ErrPageNotFound},
		{"demoted", "member", "active", wiki.ErrPageForbidden},
	}
	for _, op := range operations {
		for _, change := range changes {
			t.Run(op.name+"/"+change.name, func(t *testing.T) {
				run := op.prepare(t, change.name)
				// The lifecycle writer: Company FOR UPDATE, then the change.
				holder, err := h.pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
				if _, err := holder.Exec(ctx, `SELECT id FROM companies WHERE id = $1 FOR UPDATE`, h.owner.CompanyID); err != nil {
					t.Fatal(err)
				}
				result := make(chan error, 1)
				go func() { result <- run(ctx) }()
				waitForBlockedQuery(t, ctx, h.pool, "LockCompanyForMembershipRead")

				if _, err := holder.Exec(ctx, `
					UPDATE company_members SET role = $3, status = $4,
						version = version + 1, updated_at = clock_timestamp()
					WHERE company_id = $1 AND user_id = $2`,
					h.owner.CompanyID, h.owner.UserID, change.role, change.status); err != nil {
					t.Fatal(err)
				}
				if err := holder.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_, _ = h.pool.Exec(context.Background(), `
						UPDATE company_members SET role = 'owner', status = 'active'
						WHERE company_id = $1 AND user_id = $2`, h.owner.CompanyID, h.owner.UserID)
				})
				if err := receive(t, op.name, result); !errors.Is(err, change.want) {
					t.Fatalf("%s after the member was %s = %v, want %v", op.name, change.name, err, change.want)
				}
			})
		}
	}
}

// The (company, lower(name)) unique index is the collection-name invariant.
func TestConcurrentCollectionCreatesWithOneNameYieldOneCollection(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	const callers = 8
	results := make(chan error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := "Handbook"
			if i%2 == 1 {
				name = "HANDBOOK"
			}
			<-start
			_, err := h.service.CreateCollection(ctx, h.owner, name)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	created, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			created++
		case errors.Is(err, wiki.ErrCollectionConflict):
			conflicts++
		default:
			t.Fatalf("concurrent CreateCollection = %v, want nil or ErrCollectionConflict", err)
		}
	}
	if created != 1 || conflicts != callers-1 {
		t.Fatalf("created %d, conflicts %d, want 1 and %d", created, conflicts, callers-1)
	}
}

// Publishing locks page -> collection while deleting walks collection -> pages.
// The shared and exclusive collections lock, taken first on both sides, keeps
// them from deadlocking: the delete queues behind the publish and then unfiles
// the page it just filed.
func TestDeleteCollectionQueuesBehindAFilingPublishWithoutDeadlock(t *testing.T) {
	h := startWiki(t)
	s := h.service.DefaultWikiService
	ctx := t.Context()
	collection, err := s.CreateCollection(ctx, h.owner, "Doomed")
	if err != nil {
		t.Fatal(err)
	}
	filed, err := s.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Filed", Body: "text", CollectionID: &collection.ID})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := s.ReadDocument(ctx, h.owner, filed.ID, wiki.DocumentReadInput{Since: -1})
	if err != nil {
		t.Fatal(err)
	}

	// An autosave of the page in flight keeps the publish waiting on the page lock.
	autosave, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = autosave.Rollback(context.Background()) })
	if _, err := autosave.Exec(ctx, `SELECT id FROM wiki_pages WHERE id = $1 FOR UPDATE`, filed.ID); err != nil {
		t.Fatal(err)
	}

	published := make(chan error, 1)
	go func() {
		_, err := s.PublishPage(ctx, h.owner, filed.ID, wiki.PublishPageInput{
			ExpectedRevision: filed.Revision, ExpectedSequence: draft.Sequence,
		})
		published <- err
	}()
	waitForBlockedQuery(t, ctx, h.pool, "GetWikiPageForUpdate")
	deleted := make(chan error, 1)
	go func() { deleted <- s.DeleteCollection(ctx, h.owner, collection.ID, collection.Revision) }()
	waitForBlockedQuery(t, ctx, h.pool, "LockWikiCollectionsExclusive")

	if err := autosave.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, "PublishPage", published); err != nil {
		t.Fatalf("publish filing into the collection = %v", err)
	}
	if err := receive(t, "DeleteCollection", deleted); err != nil {
		t.Fatalf("delete behind the publish = %v", err)
	}
	got, err := s.GetPage(ctx, h.owner, filed.ID)
	if err != nil || got.CollectionID != nil || got.CurrentRevisionID == nil {
		t.Fatalf("page after delete = %+v, %v; want published and Unfiled", got, err)
	}
	if collections, err := s.ListCollections(ctx, h.owner); err != nil || len(collections) != 0 {
		t.Fatalf("collections after delete = %v, %v", collections, err)
	}
}

// A page filed concurrently with a collection delete ends in one of two
// consistent states and never in a lock error: it is filed before the delete
// and then unfiled (a publish that read the pre-delete draft is a stale-fence
// conflict), or it is refused as filed into a missing collection.
func TestConcurrentCollectionDeleteAndFilingNeverFail(t *testing.T) {
	h := startWiki(t)
	s := h.service.DefaultWikiService
	ctx := t.Context()
	for round := range 12 {
		collection, err := s.CreateCollection(ctx, h.owner, fmt.Sprintf("Round %d", round))
		if err != nil {
			t.Fatal(err)
		}
		filed, err := s.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Filed", Body: "text", CollectionID: &collection.ID})
		if err != nil {
			t.Fatal(err)
		}
		other, err := s.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Other", Body: "text"})
		if err != nil {
			t.Fatal(err)
		}
		filedDraft, err := s.ReadDocument(ctx, h.owner, filed.ID, wiki.DocumentReadInput{Since: -1})
		if err != nil {
			t.Fatal(err)
		}
		otherDraft, err := s.ReadDocument(ctx, h.owner, other.ID, wiki.DocumentReadInput{Since: -1})
		if err != nil {
			t.Fatal(err)
		}
		operations := []func() error{
			func() error { return s.DeleteCollection(ctx, h.owner, collection.ID, collection.Revision) },
			func() error {
				_, err := s.PublishPage(ctx, h.owner, filed.ID, wiki.PublishPageInput{
					ExpectedRevision: filed.Revision, ExpectedSequence: filedDraft.Sequence,
				})
				return err
			},
			func() error {
				_, err := s.UpdateDraftMetadata(ctx, h.owner, other.ID, wiki.DraftMetadataInput{
					ExpectedSequence: otherDraft.Sequence, Title: "Other", CollectionID: &collection.ID,
				})
				return err
			},
			func() error {
				_, err := s.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Fresh", CollectionID: &collection.ID})
				return err
			},
		}
		results := make(chan error, len(operations))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, run := range operations {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				results <- run()
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil && !errors.Is(err, wiki.ErrCollectionNotFound) && !errors.Is(err, wiki.ErrPageConflict) {
				t.Fatalf("round %d: concurrent delete and filing = %v, want nil, ErrCollectionNotFound or ErrPageConflict", round, err)
			}
		}
		var dangling int
		if err := h.pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM wiki_pages p WHERE p.company_id = $1 AND p.collection_id = $2)
			     + (SELECT count(*) FROM wiki_canvas_drafts d WHERE d.company_id = $1 AND d.collection_id = $2)`,
			h.owner.CompanyID, collection.ID).Scan(&dangling); err != nil {
			t.Fatal(err)
		}
		if dangling != 0 {
			t.Fatalf("round %d: %d rows still name the deleted collection", round, dangling)
		}
	}
}

// Two research calls racing for the last pages of the cap: the research-write
// lock makes the second read the first one's total, so exactly one fits.
func TestConcurrentAgentWritesHoldThePerRunPageCap(t *testing.T) {
	h := startAgentWrite(t)
	ctx := t.Context()
	for batch := range 3 {
		var pages []wiki.AgentWritePageInput
		for i := range wiki.MaxAgentWritePagesPerCall {
			n := batch*wiki.MaxAgentWritePagesPerCall + i
			pages = append(pages, page(fmt.Sprintf("seed-%d", n), fmt.Sprintf("Seed %d", n), "text"))
		}
		h.write(t, h.research, pages...)
	}
	seeded := 3 * wiki.MaxAgentWritePagesPerCall
	room := wiki.MaxAgentWritePagesPerRun - seeded // 4: two calls of 3 do not both fit
	if room < 4 {
		t.Fatalf("test needs room for two 3-page calls, have %d", room)
	}
	before := h.pageCount(t)

	results := make(chan error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, prefix := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pages := make([]wiki.AgentWritePageInput, 3)
			for i := range pages {
				pages[i] = page(fmt.Sprintf("%s-%d", prefix, i), fmt.Sprintf("%s page %d", strings.ToUpper(prefix), i), "text")
			}
			<-start
			_, err := h.writes.Write(ctx, h.owner, h.research, wiki.AgentWriteInput{Pages: pages})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	accepted, refused := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, wiki.ErrAgentWriteLimit):
			refused++
		default:
			t.Fatalf("concurrent research write = %v, want nil or ErrAgentWriteLimit", err)
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatalf("accepted %d, refused %d, want 1 and 1", accepted, refused)
	}
	if got := h.pageCount(t); got != before+3 {
		t.Fatalf("pages = %d, want %d (the refused call wrote nothing)", got, before+3)
	}
}

// Two calls of one Run writing the same new key must not collide on the key or
// the page: the second finds the first's page and updates it.
func TestConcurrentAgentWritesOfOneKeyCreateOnePage(t *testing.T) {
	h := startAgentWrite(t)
	ctx := t.Context()
	const callers = 4
	results := make(chan error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := h.writes.Write(ctx, h.owner, h.research, wiki.AgentWriteInput{
				Pages: []wiki.AgentWritePageInput{page("same", "Same", "text")},
			})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent research write of one key = %v", err)
		}
	}
	var pages, collections int
	if err := h.pool.QueryRow(t.Context(), `
		SELECT (SELECT count(*) FROM run_wiki_writes WHERE company_id = $1 AND key = 'same'),
		       (SELECT count(*) FROM wiki_collections WHERE company_id = $1 AND lower(name) = 'company')`,
		h.owner.CompanyID).Scan(&pages, &collections); err != nil {
		t.Fatal(err)
	}
	if pages != 1 || collections != 1 {
		t.Fatalf("pages %d, Company collections %d, want 1 and 1", pages, collections)
	}
}

// A publisher holds its own page row while it inserts wiki_links, whose
// foreign key takes KEY SHARE on every page it links to. The page lock must not
// conflict with that, or two pages that link to each other and publish at once
// deadlock (40P01).
func TestPublishDoesNotWaitForALockedLinkTarget(t *testing.T) {
	h := startWiki(t)
	s := h.service.DefaultWikiService
	ctx := t.Context()
	target, err := s.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Target", Body: "text"})
	if err != nil {
		t.Fatal(err)
	}
	targetDraft, err := s.ReadDocument(ctx, h.owner, target.ID, wiki.DocumentReadInput{Since: -1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishPage(ctx, h.owner, target.ID, wiki.PublishPageInput{
		ExpectedRevision: target.Revision, ExpectedSequence: targetDraft.Sequence,
	}); err != nil {
		t.Fatal(err)
	}
	for _, hold := range []struct {
		name string
		lock func(q *generated.Queries) error
	}{
		{"GetWikiPageForUpdate", func(q *generated.Queries) error {
			_, err := q.GetWikiPageForUpdate(ctx, generated.GetWikiPageForUpdateParams{
				CompanyID: h.owner.CompanyID, PageID: target.ID,
			})
			return err
		}},
		{"LockCanvasWikiPage", func(q *generated.Queries) error {
			_, err := q.LockCanvasWikiPage(ctx, generated.LockCanvasWikiPageParams{
				CompanyID: h.owner.CompanyID, ID: target.ID,
			})
			return err
		}},
	} {
		t.Run(hold.name, func(t *testing.T) {
			linking, err := s.CreatePage(ctx, h.owner, wiki.CreatePageInput{
				Title: "Linking", Body: fmt.Sprintf("see [[%s|Target]]", target.ID),
			})
			if err != nil {
				t.Fatal(err)
			}
			draft, err := s.ReadDocument(ctx, h.owner, linking.ID, wiki.DocumentReadInput{Since: -1})
			if err != nil {
				t.Fatal(err)
			}
			tx, err := h.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if err := hold.lock(generated.New(tx)); err != nil {
				t.Fatal(err)
			}
			opCtx, cancel := context.WithTimeout(ctx, lockTestTimeout)
			defer cancel()
			if _, err := s.PublishPage(opCtx, h.owner, linking.ID, wiki.PublishPageInput{
				ExpectedRevision: linking.Revision, ExpectedSequence: draft.Sequence,
			}); err != nil {
				t.Fatalf("publish linking to a page held by %s = %v, want it to complete", hold.name, err)
			}
			var links int
			if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM wiki_links WHERE from_page_id = $1 AND to_page_id = $2`,
				linking.ID, target.ID).Scan(&links); err != nil {
				t.Fatal(err)
			}
			if links != 1 {
				t.Fatalf("links from the published page = %d, want 1", links)
			}
		})
	}
}
