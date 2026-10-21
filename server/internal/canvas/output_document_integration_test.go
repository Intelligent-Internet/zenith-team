//go:build integration

package canvas

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reearth/ygo/crdt"
	"golang.org/x/sync/errgroup"

	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type documentAttachments struct {
	attachment.AttachmentService
	body string
}

func (a documentAttachments) DownloadResolved(
	context.Context,
	attachment.ResolvedDownloadInput,
) (attachment.DownloadResult, error) {
	return attachment.DownloadResult{Body: io.NopCloser(strings.NewReader(a.body))}, nil
}

func TestCanvasOutputDocument(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'admin')`,
		f.CompanyA,
		f.UserB,
	)
	var output, file, asset, assetVersion, item uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id,visibility, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Brief','Original summary','editable-output',$3,'company', origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.CompanyA, f.RunA, f.UserA).
		Scan(&output); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status)
	VALUES($1,$2,gen_random_uuid(),'brief.md','text/markdown',80,'ready') RETURNING id`, f.CompanyA, f.UserA).
		Scan(&file); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO assets(company_id,title,media_class,created_by_user_id) VALUES($1,'Brief','text',$2) RETURNING id`, f.CompanyA, f.UserA).
		Scan(&asset); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO asset_versions(company_id,asset_id,version_number,attachment_id,source_run_id,filename,content_type,size_bytes,integrity_status)
	VALUES($1,$2,1,$3,$4,'brief.md','text/markdown',80,'verified') RETURNING id`, f.CompanyA, asset, file, f.RunA).
		Scan(&assetVersion); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO output_items(company_id,output_id,ordinal,role,label,asset_version_id)
	VALUES($1,$2,0,'primary','Brief',$3) RETURNING id`, f.CompanyA, output, assetVersion).Scan(&item); err != nil {
		t.Fatal(err)
	}
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	body := "# Brief\n\nA **shared** draft.\n\n- [ ] Review\n\n```mermaid\nflowchart LR\n A --> B\n```\n"
	service := newCanvasServices(store, bus, documentAttachments{body: body})
	owner := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	dbtest.GrantProject(t, ctx, pool, f.CompanyA, f.ProjectA, f.UserB)
	member := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleAdmin}
	original, err := service.Mutate(
		ctx,
		owner,
		f.ProjectA,
		Command{RequestKey: uuid.New(), Type: "add_artifact", OutputID: &output, Title: "Brief"},
	)
	if err != nil {
		t.Fatal(err)
	}
	board, err := service.Snapshot(ctx, owner, f.ProjectA, 0)
	if err != nil {
		t.Fatal(err)
	}
	a := board.Artifacts[0]
	commands := []Command{
		{
			RequestKey:   uuid.New(),
			Type:         "import_output_document",
			ArtifactID:   &a.ID,
			VersionID:    &original.ID,
			OutputItemID: &item,
			Body:         "Untrusted replacement",
		},
		{
			RequestKey:   uuid.New(),
			Type:         "import_output_document",
			ArtifactID:   &a.ID,
			VersionID:    &original.ID,
			OutputItemID: &item,
		},
	}
	results := make([]Result, 2)
	group, ctx := errgroup.WithContext(ctx)
	for i := range commands {
		group.Go(func() error {
			var err error
			results[i], err = service.Mutate(ctx, owner, f.ProjectA, commands[i])
			return err
		})
	}
	if err := group.Wait(); err != nil {
		t.Fatal(err)
	}
	ctx = t.Context()
	if results[0].ID != results[1].ID {
		t.Fatalf("simultaneous opens created different documents: %+v", results)
	}
	draft, err := service.Document(ctx, member, f.ProjectA, results[0].ID, -1)
	if err != nil || !draft.CanEdit || !strings.Contains(string(draft.Content), `"mermaid"`) ||
		!strings.Contains(draft.Markdown, "shared") ||
		strings.Contains(draft.Markdown, "Untrusted") {
		t.Fatalf(
			"imported collaborative draft: canEdit=%v, content=%s, error=%v",
			draft.CanEdit,
			draft.Content,
			err,
		)
	}
	history, err := service.History(ctx, owner, f.ProjectA, a.ID)
	if err != nil || len(history.Versions) != 2 || history.Versions[0].BaseVersionID == nil ||
		*history.Versions[0].BaseVersionID != original.ID {
		t.Fatalf("original and imported history = %+v, %v", history, err)
	}
	// Derived drafts must never become a Company Wiki publication or outlive access.
	if _, err := store.Queries().
		GetWikiPageForMember(ctx, generated.GetWikiPageForMemberParams{CompanyID: f.CompanyA, PageID: draft.PageID, ActorUserID: f.UserB}); err == nil {
		t.Fatal("Output document leaked into Company Wiki")
	}
	// Agent Wiki tools have Company-wide reads, not source Output authority.
	queries := store.Queries()
	matches, err := queries.SearchWikiPagesForRun(ctx, generated.SearchWikiPagesForRunParams{
		CompanyID: f.CompanyA, ActorUserID: f.UserB, Search: "Brief", ResultLimit: 10,
	})
	if err != nil || len(matches) != 0 {
		t.Fatalf("Output document leaked into Agent Wiki search: %+v, %v", matches, err)
	}
	if _, err := queries.GetWikiPageSliceForRun(ctx, generated.GetWikiPageSliceForRunParams{
		CompanyID: f.CompanyA, ActorUserID: f.UserB, PageID: draft.PageID,
		CharOffset: 0, CharLimit: 100,
	}); err == nil {
		t.Fatal("Output document leaked into Agent Wiki reads")
	}
	present, err := queries.WikiPageExistsForMember(ctx, generated.WikiPageExistsForMemberParams{
		CompanyID: f.CompanyA, ActorUserID: f.UserB, PageID: draft.PageID,
	})
	if err != nil || present {
		t.Fatalf("Output document leaked into Agent Wiki page metadata: %v, %v", present, err)
	}
	board, err = service.Snapshot(ctx, member, f.ProjectA, 0)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := service.Mutate(ctx, member, f.ProjectA, Command{
		RequestKey: uuid.New(), Type: "checkpoint", ArtifactID: &a.ID, VersionID: &results[0].ID,
		WikiPageID: &draft.PageID, ExpectedRevision: board.Artifacts[0].Revision,
	})
	if err != nil {
		t.Fatalf("checkpoint imported document: %v", err)
	}
	savedBody, err := service.VersionBody(ctx, member, f.ProjectA, checkpoint.ID)
	if err != nil || strings.TrimSpace(savedBody) != strings.TrimSpace(draft.Markdown) {
		t.Fatalf("import checkpoint body = %q, %v", savedBody, err)
	}
	commands[0].RequestKey = uuid.New()
	reopen, err := service.Mutate(ctx, owner, f.ProjectA, commands[0])
	if err != nil || reopen.ID != checkpoint.ID {
		t.Fatalf("reopen should use latest checkpoint on the same draft: %+v, %v", reopen, err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE outputs SET visibility='private' WHERE id=$1`, output)
	if _, err := service.Document(ctx, member, f.ProjectA, results[0].ID, -1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked document read = %v", err)
	}
	hidden, err := service.Snapshot(ctx, member, f.ProjectA, 0)
	if err != nil || len(hidden.Artifacts) != 0 || len(hidden.ArtifactIDs) != 0 || len(hidden.Items) != 0 {
		t.Fatalf("revoked draft leaked in snapshot: %+v, %v", hidden, err)
	}
	if _, err := service.VersionBody(ctx, member, f.ProjectA, results[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked checkpoint read = %v", err)
	}
	if _, err := service.Mutate(ctx, member, f.ProjectA, commands[1]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked import = %v", err)
	}
}

func TestOutputDocumentWithoutProject(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'admin')`,
		f.CompanyA,
		f.UserB,
	)
	var run uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status,run_type) VALUES($1,$2,$3,$4,'completed','direct_chat') RETURNING id`, f.CompanyA, f.AgentA, f.DirectConversationA, f.RuntimeA).
		Scan(&run); err != nil {
		t.Fatal(err)
	}
	var output, file, asset, assetVersion, item uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id,visibility, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Brief','Original summary','editable-output',$3,'company', origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.CompanyA, run, f.UserA).
		Scan(&output); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status)
	VALUES($1,$2,gen_random_uuid(),'brief.md','text/markdown',80,'ready') RETURNING id`, f.CompanyA, f.UserA).
		Scan(&file); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO assets(company_id,title,media_class,created_by_user_id) VALUES($1,'Brief','text',$2) RETURNING id`, f.CompanyA, f.UserA).
		Scan(&asset); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO asset_versions(company_id,asset_id,version_number,attachment_id,source_run_id,filename,content_type,size_bytes,integrity_status)
	VALUES($1,$2,1,$3,$4,'brief.md','text/markdown',80,'verified') RETURNING id`, f.CompanyA, asset, file, run).
		Scan(&assetVersion); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO output_items(company_id,output_id,ordinal,role,label,asset_version_id)
	VALUES($1,$2,0,'primary','Brief',$3) RETURNING id`, f.CompanyA, output, assetVersion).Scan(&item); err != nil {
		t.Fatal(err)
	}

	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	service := NewCanvasService(store, bus, documentAttachments{body: "# Original\n\nShared Markdown"})
	pages := NewPageService(store, bus)
	owner := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	reader := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleAdmin}
	if _, err := service.OpenOutputDocument(
		ctx,
		reader,
		OutputDocumentRef{OutputID: output, ItemID: item},
	); !errors.Is(
		err,
		ErrForbidden,
	) {
		t.Fatalf("reader import = %v", err)
	}
	first, err := service.OpenOutputDocument(ctx, owner, OutputDocumentRef{OutputID: output, ItemID: item})
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.OpenOutputDocument(ctx, owner, OutputDocumentRef{OutputID: output, ItemID: item})
	if err != nil || first != again {
		t.Fatalf("reopen = %v, %v; want %v", again, err, first)
	}
	// A Project placement must reuse, and never own, the standalone document.
	placed, err := service.Mutate(
		ctx,
		owner,
		f.ProjectA,
		Command{RequestKey: uuid.New(), Type: "add_artifact", OutputID: &output, Title: "Brief"},
	)
	if err != nil {
		t.Fatal(err)
	}
	board, err := service.Snapshot(ctx, owner, f.ProjectA, 0)
	if err != nil {
		t.Fatal(err)
	}
	artifact := board.Artifacts[0]
	imported, err := service.Mutate(
		ctx,
		owner,
		f.ProjectA,
		Command{
			RequestKey:   uuid.New(),
			Type:         "import_output_document",
			ArtifactID:   &artifact.ID,
			VersionID:    &placed.ID,
			OutputItemID: &item,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	throughProject, err := pages.ReadDocument(
		ctx,
		owner,
		f.ProjectA,
		DocumentReadInput{VersionID: imported.ID, Since: -1},
	)
	if err != nil || throughProject.PageID != first {
		t.Fatalf("placement document = %+v, %v; want %v", throughProject, err, first)
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM project_artifacts WHERE id=$1`, artifact.ID)
	var project uuid.NullUUID
	if err := pool.QueryRow(ctx, `SELECT project_id FROM wiki_pages WHERE id=$1`, first).
		Scan(&project); err != nil ||
		project.Valid {
		t.Fatalf("document placement owns page: %v, %v", project, err)
	}
	draft, err := pages.ReadOutputDocument(
		ctx,
		reader,
		OutputDocumentRef{OutputID: output, ItemID: item},
		DocumentReadInput{Since: -1},
	)
	if err != nil || draft.CanEdit || !strings.Contains(draft.Markdown, "Shared Markdown") {
		t.Fatalf("reader document = %+v, %v", draft, err)
	}
	if _, err = pages.ReserveOutputBlockIDs(
		ctx,
		reader,
		OutputDocumentRef{OutputID: output, ItemID: item},
		2,
	); !errors.Is(
		err,
		ErrForbidden,
	) {
		t.Fatalf("reader reserve = %v", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO output_grants(company_id,output_id,user_id,permission,granted_by_user_id) VALUES($1,$2,$3,'edit',$4)`,
		f.CompanyA,
		output,
		f.UserB,
		f.UserA,
	)
	draft, err = pages.ReadOutputDocument(
		ctx,
		reader,
		OutputDocumentRef{OutputID: output, ItemID: item},
		DocumentReadInput{Since: -1},
	)
	if err != nil || !draft.CanEdit {
		t.Fatalf("editor document = %+v, %v", draft, err)
	}
	if _, err = pages.ReserveOutputBlockIDs(
		ctx,
		reader,
		OutputDocumentRef{OutputID: output, ItemID: item},
		2,
	); err != nil {
		t.Fatal(err)
	}

	client := crdt.New()
	defer client.Destroy()
	if err = crdt.ApplyUpdateV1(client, draft.State, nil); err != nil {
		t.Fatal(err)
	}
	text := client.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement).Children()[0].(*crdt.YXmlText)
	client.Transact(func(tx *crdt.Transaction) { text.Insert(tx, 0, "Edited ", nil) })
	changed, err := pages.SyncOutputDocument(
		ctx,
		reader,
		OutputDocumentRef{OutputID: output, ItemID: item},
		DocumentSyncInput{Update: client.EncodeStateAsUpdate(), BaseSequence: draft.Sequence},
	)
	if err != nil || !strings.Contains(changed.Markdown, "Edited Original") {
		t.Fatalf("saved content = %q, %v", changed.Markdown, err)
	}
	historyRef := DocumentHistoryRef{OutputID: output, ItemID: item}
	history, err := pages.DocumentHistory(ctx, reader, historyRef, DocumentHistoryInput{})
	if err != nil || len(history.Items) < 2 || history.Items[0].ActorID == nil ||
		*history.Items[0].ActorID != reader.UserID {
		t.Fatalf("history attribution = %+v, %v", history, err)
	}
	point, err := pages.DocumentHistoryState(ctx, reader, historyRef, changed.Sequence)
	if err != nil || !strings.Contains(point.Markdown, "Edited Original") {
		t.Fatalf("history content = %+v, %v", point, err)
	}
	if _, err = pages.RestoreDocumentHistory(
		ctx,
		reader,
		historyRef,
		DocumentHistoryRestore{Sequence: draft.Sequence, ExpectedSequence: draft.Sequence},
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale restore = %v", err)
	}
	version, err := pages.CheckpointOutputDocument(
		ctx,
		reader,
		OutputDocumentRef{OutputID: output, ItemID: item},
		changed.Sequence,
	)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := pages.CheckpointOutputDocument(
		ctx,
		reader,
		OutputDocumentRef{OutputID: output, ItemID: item},
		changed.Sequence,
	)
	if err != nil || repeated != version {
		t.Fatalf("checkpoint replay = %v,%v; want %v", repeated, err, version)
	}
	exported, err := pages.OutputDocumentVersion(ctx, reader, output, version)
	if err != nil || !strings.Contains(exported.Body, "Edited Original") {
		t.Fatalf("export = %q,%v", exported.Body, err)
	}
	if _, err = pages.CheckpointOutputDocument(
		ctx,
		reader,
		OutputDocumentRef{OutputID: output, ItemID: item},
		draft.Sequence,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale checkpoint = %v", err)
	}
	thread, err := service.MutateOutputReview(
		ctx,
		reader,
		output,
		Command{
			RequestKey: uuid.New(),
			Type:       "comment",
			PageID:     &changed.PageID,
			Body:       "Working document comment",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	historical, err := service.MutateOutputReview(
		ctx,
		reader,
		output,
		Command{RequestKey: uuid.New(), Type: "comment", VersionID: &version, Body: "Saved version comment"},
	)
	if err != nil {
		t.Fatal(err)
	}
	review, err := service.OutputReview(ctx, reader, output)
	if err != nil {
		t.Fatal(err)
	}
	foundLive, foundVersion := false, false
	for _, t := range review.Items {
		if t.ID == thread.ID {
			foundLive = t.PageID != nil && *t.PageID == changed.PageID
		}
		if t.ID == historical.ID {
			foundVersion = t.PageID == nil && t.VersionID == version
		}
	}
	if !foundLive || !foundVersion {
		t.Fatalf("comment subjects = %+v", review.Items)
	}
	// Crossing a checkpoint still reconstructs exact earlier states, including deletions.
	last := changed
	for range 105 {
		client.Transact(func(tx *crdt.Transaction) { text.Insert(tx, 0, "x", nil) })
		last, err = pages.SyncOutputDocument(
			ctx,
			owner,
			OutputDocumentRef{OutputID: output, ItemID: item},
			DocumentSyncInput{Update: client.EncodeStateAsUpdate(), BaseSequence: last.Sequence},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	firstPage, err := pages.DocumentHistory(ctx, owner, historyRef, DocumentHistoryInput{})
	if err != nil || len(firstPage.Items) != 100 || firstPage.NextBeforeSequence == 0 {
		t.Fatalf("history page = %+v,%v", firstPage, err)
	}
	olderPage, err := pages.DocumentHistory(
		ctx,
		owner,
		historyRef,
		DocumentHistoryInput{BeforeSequence: firstPage.NextBeforeSequence},
	)
	if err != nil || len(olderPage.Items) == 0 || olderPage.Items[0].Sequence >= firstPage.Items[99].Sequence {
		t.Fatalf("history cursor overlap = %+v,%v", olderPage, err)
	}
	prior, err := pages.DocumentHistoryState(ctx, owner, historyRef, changed.Sequence)
	if err != nil || prior.Markdown != point.Markdown {
		t.Fatalf("checkpoint reconstruction = %+v,%v", prior, err)
	}
	at := history.Items[0].SavedAt
	end := at.Add(time.Minute)
	filtered, err := pages.DocumentHistory(
		ctx,
		owner,
		historyRef,
		DocumentHistoryInput{ActorID: &reader.UserID, After: &at, Before: &end},
	)
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].Sequence != changed.Sequence {
		t.Fatalf("filtered history = %+v,%v", filtered, err)
	}
	if err = pages.NameDocumentHistory(
		ctx,
		owner,
		historyRef,
		DocumentHistoryName{Sequence: changed.Sequence, Label: "Reviewed brief"},
	); err != nil {
		t.Fatal(err)
	}
	named, err := pages.DocumentHistory(ctx, owner, historyRef, DocumentHistoryInput{ActorID: &reader.UserID})
	if err != nil || len(named.Items) != 1 || named.Items[0].Label != "Reviewed brief" {
		t.Fatalf("named history = %+v,%v", named, err)
	}
	// Recovery changes shared content, never the Agent's immutable deliveries
	// or the Task's selected/approved results.
	evidence := func() string {
		t.Helper()
		var value string
		err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
            'outputs', (SELECT jsonb_agg(to_jsonb(o) ORDER BY id) FROM outputs o WHERE company_id=$1),
            'items', (SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM output_items i WHERE company_id=$1),
            'resultSets', (SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM task_result_sets r WHERE company_id=$1),
            'tasks', (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM tasks t WHERE company_id=$1)
        )::text`, f.CompanyA).Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	beforeRestore := evidence()
	restored, err := pages.RestoreDocumentHistory(
		ctx,
		owner,
		historyRef,
		DocumentHistoryRestore{Sequence: draft.Sequence, ExpectedSequence: last.Sequence},
	)
	if err != nil || strings.Contains(restored.Markdown, "Edited") || restored.Sequence <= last.Sequence {
		t.Fatalf("restored history = %+v,%v", restored, err)
	}
	if afterRestore := evidence(); afterRestore != beforeRestore {
		t.Fatal("history restoration changed immutable deliveries or Task results")
	}
	preserved, err := pages.DocumentHistoryState(ctx, owner, historyRef, last.Sequence)
	if err != nil || !strings.Contains(preserved.Markdown, "xxxx") {
		t.Fatalf("restore erased intervening history = %+v,%v", preserved, err)
	}
	replay, err := pages.SyncOutputDocument(
		ctx,
		owner,
		OutputDocumentRef{OutputID: output, ItemID: item},
		DocumentSyncInput{Update: client.EncodeStateAsUpdate(), BaseSequence: last.Sequence},
	)
	if err != nil || replay.Sequence != restored.Sequence || replay.Markdown != restored.Markdown {
		t.Fatalf("replayed save changed restoration = %+v,%v", replay, err)
	}
	client.Transact(func(tx *crdt.Transaction) { text.Insert(tx, 0, "offline edit", nil) })
	if _, err = pages.SyncOutputDocument(
		ctx,
		owner,
		OutputDocumentRef{OutputID: output, ItemID: item},
		DocumentSyncInput{Update: client.EncodeStateAsUpdate(), BaseSequence: last.Sequence},
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale edit after restore = %v", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE output_grants SET permission='view' WHERE output_id=$1 AND user_id=$2`,
		output,
		f.UserB,
	)
	if _, err = pages.SyncOutputDocument(
		ctx,
		reader,
		OutputDocumentRef{OutputID: output, ItemID: item},
		DocumentSyncInput{Update: client.EncodeStateAsUpdate(), BaseSequence: draft.Sequence},
	); !errors.Is(
		err,
		ErrForbidden,
	) {
		t.Fatalf("revoked editor save = %v", err)
	}
	if _, err = pages.RestoreDocumentHistory(
		ctx,
		reader,
		historyRef,
		DocumentHistoryRestore{Sequence: changed.Sequence, ExpectedSequence: restored.Sequence},
	); !errors.Is(
		err,
		ErrForbidden,
	) {
		t.Fatalf("viewer restore = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM output_grants WHERE output_id=$1`, output)
	dbtest.MustExec(t, ctx, pool, `UPDATE outputs SET visibility='private' WHERE id=$1`, output)
	if _, err = pages.ReadOutputDocument(
		ctx,
		reader,
		OutputDocumentRef{OutputID: output, ItemID: item},
		DocumentReadInput{Since: -1},
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("revoked read = %v", err)
	}
	if _, err = pages.DocumentHistory(
		ctx,
		reader,
		historyRef,
		DocumentHistoryInput{},
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("revoked history = %v", err)
	}
	cross := owner
	cross.CompanyID = f.CompanyB
	if _, err = pages.ReadOutputDocument(
		ctx,
		cross,
		OutputDocumentRef{OutputID: output, ItemID: item},
		DocumentReadInput{Since: -1},
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("cross-company read = %v", err)
	}
}
