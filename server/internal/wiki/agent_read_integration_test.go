//go:build integration

package wiki_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

// agentReadHarness adds to the Wiki harness a second member of Company A, a
// direct-Conversation Run and a Schedule-Conversation Run of the owner, and
// the Agent-facing read service.
type agentReadHarness struct {
	wikiHarness
	reads       *wiki.DefaultAgentReadService
	member      auth.UserPrincipal
	taskRun     uuid.UUID
	directRun   uuid.UUID
	scheduleRun uuid.UUID
	otherRunB   uuid.UUID
	outsiderIn  auth.UserPrincipal // a member of Company B named against Company A
}

func startAgentRead(t *testing.T) agentReadHarness {
	t.Helper()
	h := startWiki(t)
	ctx := t.Context()
	store, err := db.NewStore(h.pool)
	if err != nil {
		t.Fatal(err)
	}
	reads, err := wiki.NewAgentReadService(store, h.bus)
	if err != nil {
		t.Fatal(err)
	}
	memberID := dbtest.NewUUID(t)
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'wiki-member@example.com', 'Wiki Member')`,
		memberID,
	)
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		h.fixture.CompanyA,
		memberID,
	)
	scheduleID, scheduleConversation := dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.WithinSeedTx(t, ctx, h.pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO schedules (id, company_id, conversation_id, creator_user_id, request_key, agent_id,
				reviewed_provider, name, instructions, trigger_type, run_at)
			VALUES ($1, $2, $3, $4, gen_random_uuid(), $5, 'codex', 'Digest', 'Digest', 'once', now())`,
			scheduleID, h.fixture.CompanyA, scheduleConversation, h.fixture.UserA, h.fixture.AgentA)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO conversations (id, company_id, kind, schedule_id, agent_id, created_by_user_id, title)
			VALUES ($1, $2, 'schedule', $3, $4, $5, 'Digest')`,
			scheduleConversation, h.fixture.CompanyA, scheduleID, h.fixture.AgentA, h.fixture.UserA)
	})
	newRun := func(conversationID uuid.UUID, runType string) uuid.UUID {
		t.Helper()
		var run uuid.UUID
		if err := h.pool.QueryRow(ctx, `
			INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id, status, run_type, revision)
			VALUES ($1, $2, $3, $4, 'running', $5, 1) RETURNING id`,
			h.fixture.CompanyA, h.fixture.AgentA, conversationID, h.fixture.RuntimeA, runType,
		).Scan(&run); err != nil {
			t.Fatal(err)
		}
		return run
	}
	return agentReadHarness{
		wikiHarness: h, reads: reads, taskRun: h.fixture.RunA, otherRunB: h.fixture.RunB,
		directRun:   newRun(h.fixture.DirectConversationA, "direct_chat"),
		scheduleRun: newRun(scheduleConversation, "schedule"),
		member: auth.UserPrincipal{
			UserID:    memberID,
			CompanyID: h.fixture.CompanyA,
			Role:      auth.CompanyRoleMember,
		},
		outsiderIn: auth.UserPrincipal{
			UserID:    h.fixture.UserB,
			CompanyID: h.fixture.CompanyA,
			Role:      auth.CompanyRoleOwner,
		},
	}
}

func TestWikiEvidencePublishesCommittedRunScopedInvalidations(t *testing.T) {
	h := startAgentRead(t)
	for _, id := range []uuid.UUID{h.taskRun, h.directRun, h.scheduleRun} {
		h.eventNames()
		if _, err := h.reads.Search(t.Context(), h.owner, id, "private search", 5); err != nil {
			t.Fatal(err)
		}
		if len(*h.events) != 1 || (*h.events)[0].Name != wiki.EventRunReadsChanged {
			t.Fatalf("missing Wiki evidence event: %+v", *h.events)
		}
		payload, err := events.PayloadOf[wiki.RunReadsChanged]((*h.events)[0])
		if err != nil || payload.RunID != id {
			t.Fatalf("Wiki evidence hint = %+v, %v", payload, err)
		}
		var count int
		if err := h.pool.QueryRow(t.Context(), `SELECT count(*) FROM run_wiki_reads WHERE run_id=$1`, id).
			Scan(&count); err != nil ||
			count != 1 {
			t.Fatalf("event did not follow evidence commit: %d, %v", count, err)
		}
	}
}

func (h agentReadHarness) publish(t *testing.T, principal auth.UserPrincipal, title, body string) wiki.PageResponse {
	t.Helper()
	page, err := h.service.CreatePage(
		t.Context(),
		principal,
		wiki.CreatePageInput{Title: title, Body: body, Summary: title + " summary"},
	)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func (h agentReadHarness) evidenceCount(t *testing.T, where string, args ...any) int {
	t.Helper()
	var count int
	if err := h.pool.QueryRow(t.Context(), `SELECT count(*) FROM run_wiki_reads WHERE `+where, args...).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestAgentSearchRanksActivePagesOfTheCompanyAndRecordsEvidence(t *testing.T) {
	h := startAgentRead(t)
	ctx := t.Context()

	collection, err := h.service.CreateCollection(ctx, h.owner, "Handbook")
	if err != nil {
		t.Fatal(err)
	}
	strong, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{
		Title:        "Refund policy",
		Body:         "Refund requests: a refund is issued within 14 days. Every refund needs approval.",
		Summary:      "Refund policy summary",
		CollectionID: &collection.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	weak := h.publish(t, h.owner, "Shipping", "Orders ship in two days. Ask about a refund if delayed.")
	h.publish(t, h.owner, "Holidays", "The office is closed in December.")
	archived := h.publish(t, h.owner, "Old refunds", "Refund refund refund refund.")
	if _, err := h.service.ArchivePage(ctx, h.owner, archived.ID, archived.Revision); err != nil {
		t.Fatal(err)
	}
	h.publish(t, h.foreign, "Foreign refunds", "Refund refund refund refund.")

	result, err := h.reads.Search(ctx, h.owner, h.taskRun, "refund", 5)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || len(result.Items) != 2 || result.Items[0].PageID != strong.ID ||
		result.Items[1].PageID != weak.ID {
		t.Fatalf(
			"search = %+v, want [%s %s] ranked by relevance, archived and foreign pages excluded",
			result,
			strong.ID,
			weak.ID,
		)
	}
	first := result.Items[0]
	if first.Title != "Refund policy" || first.Summary != "Refund policy summary" || first.Revision != 1 ||
		first.CollectionName == nil || *first.CollectionName != "Handbook" || !strings.Contains(strings.ToLower(first.Snippet), "refund") {
		t.Fatalf("first item = %+v", first)
	}
	if result.Items[1].CollectionName != nil {
		t.Fatalf("uncollected page reports collection %q", *result.Items[1].CollectionName)
	}
	if read, err := h.reads.Read(
		ctx,
		h.owner,
		h.taskRun,
		strong.ID,
		0,
	); err != nil || read.CollectionName == nil ||
		*read.CollectionName != "Handbook" {
		t.Fatalf("read of a collected page = %+v, %v", read, err)
	}

	// The limit trims the list, not the total, and every search leaves a row.
	limited, err := h.reads.Search(ctx, h.owner, h.taskRun, "refund", 1)
	if err != nil || len(limited.Items) != 1 || limited.Total != 2 || limited.Items[0].PageID != strong.ID {
		t.Fatalf("limited search = %+v, %v", limited, err)
	}
	// A question of many words finds pages matching any of them; a query with
	// no words at all finds nothing and is still recorded.
	natural, err := h.reads.Search(ctx, h.owner, h.taskRun, "how do we handle a refund for december?", 10)
	if err != nil || natural.Total != 3 {
		t.Fatalf("natural-language search = %+v, %v; want the two refund pages and Holidays", natural, err)
	}
	empty, err := h.reads.Search(ctx, h.owner, h.taskRun, "!!!", 5)
	if err != nil || empty.Total != 0 || len(empty.Items) != 0 {
		t.Fatalf("wordless search = %+v, %v", empty, err)
	}
	if got := h.evidenceCount(t, `run_id = $1 AND kind = 'search'`, h.taskRun); got != 4 {
		t.Fatalf("search evidence rows = %d, want 4", got)
	}
	var query string
	var count int32
	if err := h.pool.QueryRow(ctx, `SELECT query, result_count FROM run_wiki_reads WHERE run_id = $1 AND kind = 'search' ORDER BY created_at, id LIMIT 1`, h.taskRun).
		Scan(&query, &count); err != nil {
		t.Fatal(err)
	}
	if query != "refund" || count != 2 {
		t.Fatalf("first evidence = %q/%d, want refund/2", query, count)
	}

	// Credentials in a query never reach the trail.
	if _, err := h.reads.Search(
		ctx,
		h.owner,
		h.taskRun,
		"key sk-proj-AbCdEf0123456789AbCdEf0123456789 rotation",
		5,
	); err != nil {
		t.Fatal(err)
	}
	if got := h.evidenceCount(t, `query LIKE '%AbCdEf0123456789%'`); got != 0 {
		t.Fatalf("%d evidence rows kept a credential", got)
	}

	// Refused searches record nothing.
	before := h.evidenceCount(t, `kind = 'search'`)
	for name, call := range map[string]func() error{
		"blank": func() error { _, err := h.reads.Search(ctx, h.owner, h.taskRun, "  ", 5); return err },
		"too long": func() error {
			_, err := h.reads.Search(ctx, h.owner, h.taskRun, strings.Repeat("a", 201), 5)
			return err
		},
		"limit 0":   func() error { _, err := h.reads.Search(ctx, h.owner, h.taskRun, "refund", 0); return err },
		"limit 11":  func() error { _, err := h.reads.Search(ctx, h.owner, h.taskRun, "refund", 11); return err },
		"nil bytes": func() error { _, err := h.reads.Search(ctx, h.owner, h.taskRun, "a\x00b", 5); return err },
	} {
		if err := call(); !errors.Is(err, wiki.ErrPageInvalidInput) {
			t.Fatalf("%s search = %v, want ErrPageInvalidInput", name, err)
		}
	}
	if after := h.evidenceCount(t, `kind = 'search'`); after != before {
		t.Fatalf("refused searches recorded %d rows", after-before)
	}
}

func TestAgentReadNamesTheAgentThatWroteTheRevision(t *testing.T) {
	h := startAgentWrite(t)
	ctx := t.Context()
	var agentName string
	if err := h.pool.QueryRow(ctx, `SELECT name FROM agents WHERE id = $1`, h.fixture.AgentA).
		Scan(&agentName); err != nil {
		t.Fatal(err)
	}

	written := byKey(h.write(t, h.research, page("company-overview", "Company overview", "Written by research.")))
	researched, err := h.reads.Read(ctx, h.owner, h.research, written["company-overview"].PageID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if researched.UpdatedByAgentName == nil || *researched.UpdatedByAgentName != agentName {
		t.Fatalf("updatedByAgentName = %v, want %q", researched.UpdatedByAgentName, agentName)
	}
	if researched.UpdatedByName == nil || *researched.UpdatedByName != "Schema A" {
		t.Fatalf("updatedByName = %v, want the founder the Agent wrote for", researched.UpdatedByName)
	}

	human, err := h.service.CreatePage(
		ctx,
		h.owner,
		wiki.CreatePageInput{Title: "Human page", Body: "Written by a person."},
	)
	if err != nil {
		t.Fatal(err)
	}
	read, err := h.reads.Read(ctx, h.owner, h.research, human.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if read.UpdatedByAgentName != nil {
		t.Fatalf("updatedByAgentName = %q for a human-written page, want none", *read.UpdatedByAgentName)
	}
}

func TestAgentReadPinsTheRevisionItReturnedAndPagesLongBodies(t *testing.T) {
	h := startAgentRead(t)
	ctx := t.Context()

	body := strings.Repeat("é", 30000) // multi-byte characters: offsets count characters
	page := h.publish(t, h.owner, "Long handbook", body)

	first, err := h.reads.Read(ctx, h.owner, h.taskRun, page.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.ReturnedChars != wiki.MaxAgentReadRunes || first.TotalChars != 30000 || !first.Truncated ||
		first.NextOffset == nil || *first.NextOffset != 24000 || first.Revision != 1 ||
		first.RevisionID != *page.CurrentRevisionID || first.Notice != wiki.AgentUntrustedNotice ||
		first.Content != strings.Repeat("é", 24000) {
		t.Fatalf("first slice = %+v", first)
	}
	if first.UpdatedByName == nil || *first.UpdatedByName != "Schema A" {
		t.Fatalf("updatedByName = %v, want the author's display name", first.UpdatedByName)
	}
	second, err := h.reads.Read(ctx, h.owner, h.taskRun, page.ID, 24000)
	if err != nil {
		t.Fatal(err)
	}
	if second.ReturnedChars != 6000 || second.Truncated || second.NextOffset != nil || second.Offset != 24000 ||
		second.Content != strings.Repeat("é", 6000) {
		t.Fatalf("second slice = %+v", second)
	}
	if _, err := h.reads.Read(ctx, h.owner, h.taskRun, page.ID, 30001); !errors.Is(err, wiki.ErrPageInvalidInput) {
		t.Fatalf("offset past the end = %v, want ErrPageInvalidInput", err)
	}
	if end, err := h.reads.Read(
		ctx,
		h.owner,
		h.taskRun,
		page.ID,
		30000,
	); err != nil || end.Content != "" ||
		end.Truncated {
		t.Fatalf("read at the end = %+v, %v", end, err)
	}

	// The page moves on; the trail keeps the revision each read returned.
	updated, err := h.service.UpdatePage(ctx, h.owner, page.ID, updatePageInput{
		ExpectedRevision: page.Revision, Title: "Handbook v2", Body: "Short new text.", Summary: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := h.reads.Read(ctx, h.owner, h.taskRun, page.ID, 0)
	if err != nil || latest.Revision != 2 || latest.RevisionID != *updated.CurrentRevisionID ||
		latest.Content != "Short new text." {
		t.Fatalf("read after update = %+v, %v", latest, err)
	}
	trail, err := h.reads.ListRunReads(ctx, h.owner, h.taskRun)
	if err != nil || len(trail.Items) != 4 {
		t.Fatalf("trail = %+v, %v; want 4 rows", trail, err)
	}
	for i, want := range []struct {
		title     string
		revision  int32
		truncated bool
	}{{"Long handbook", 1, true}, {"Long handbook", 1, false}, {"Long handbook", 1, false}, {"Handbook v2", 2, false}} {
		item := trail.Items[i]
		if item.Kind != "read" || item.Title != want.title || item.RevisionNumber == nil ||
			*item.RevisionNumber != want.revision ||
			item.Truncated != want.truncated ||
			item.PageID == nil ||
			*item.PageID != page.ID ||
			item.ResultCount != nil {
			t.Fatalf("trail[%d] = %+v, want %+v", i, item, want)
		}
		if item.Page == nil || item.Page.Status != "active" || item.Page.CurrentRevision != 2 {
			t.Fatalf("trail[%d] page = %+v, want active at revision 2", i, item.Page)
		}
	}
	if trail.Items[0].RevisionID == nil || *trail.Items[0].RevisionID != *page.CurrentRevisionID ||
		trail.Items[3].RevisionID == nil || *trail.Items[3].RevisionID != *updated.CurrentRevisionID {
		t.Fatalf("trail did not pin the revisions returned: %+v", trail.Items)
	}

	// An archived page is gone for Agents; the trail reports it archived.
	if _, err := h.service.ArchivePage(ctx, h.owner, page.ID, updated.Revision); err != nil {
		t.Fatal(err)
	}
	before := h.evidenceCount(t, `kind = 'read'`)
	if _, err := h.reads.Read(ctx, h.owner, h.taskRun, page.ID, 0); !errors.Is(err, wiki.ErrPageNotFound) {
		t.Fatalf("read of archived page = %v, want ErrPageNotFound", err)
	}
	if _, err := h.reads.Read(ctx, h.owner, h.taskRun, uuid.New(), 0); !errors.Is(err, wiki.ErrPageNotFound) {
		t.Fatalf("read of unknown page = %v, want ErrPageNotFound", err)
	}
	foreign := h.publish(t, h.foreign, "Foreign", "secret")
	if _, err := h.reads.Read(ctx, h.owner, h.taskRun, foreign.ID, 0); !errors.Is(err, wiki.ErrPageNotFound) {
		t.Fatalf("read of another Company's page = %v, want ErrPageNotFound", err)
	}
	if after := h.evidenceCount(t, `kind = 'read'`); after != before {
		t.Fatalf("refused reads recorded %d rows", after-before)
	}
	trail, err = h.reads.ListRunReads(ctx, h.owner, h.taskRun)
	if err != nil || trail.Items[0].Page == nil || trail.Items[0].Page.Status != "archived" {
		t.Fatalf("trail after archive = %+v, %v", trail, err)
	}
}

func TestAgentReadsHonourTheAgentSwitchTheRunAndTheMembershipFence(t *testing.T) {
	h := startAgentRead(t)
	ctx := t.Context()
	page := h.publish(t, h.owner, "Handbook", "Company handbook text.")

	setDisabled := func(disabled bool) {
		t.Helper()
		settings := `{"capabilities":{"disabledBuiltIns":[]}}`
		if disabled {
			settings = `{"capabilities":{"disabledBuiltIns":["planning","wiki"]}}`
		}
		dbtest.MustExec(
			t,
			ctx,
			h.pool,
			`UPDATE agents SET settings = $2::jsonb WHERE id = $1`,
			h.fixture.AgentA,
			settings,
		)
	}
	setDisabled(true)
	if _, err := h.reads.Search(
		ctx,
		h.owner,
		h.taskRun,
		"handbook",
		5,
	); !errors.Is(
		err,
		wiki.ErrAgentAccessDisabled,
	) {
		t.Fatalf("search with wiki disabled = %v, want ErrAgentAccessDisabled", err)
	}
	if _, err := h.reads.Read(ctx, h.owner, h.taskRun, page.ID, 0); !errors.Is(err, wiki.ErrAgentAccessDisabled) {
		t.Fatalf("read with wiki disabled = %v, want ErrAgentAccessDisabled", err)
	}
	if got := h.evidenceCount(t, `true`); got != 0 {
		t.Fatalf("disabled Agent left %d evidence rows", got)
	}
	setDisabled(false)
	if _, err := h.reads.Search(ctx, h.owner, h.taskRun, "handbook", 5); err != nil {
		t.Fatalf("search after re-enabling: %v", err)
	}

	// The Run must belong to the caller's Company, and the caller must be an
	// active member of it.
	if _, err := h.reads.Search(ctx, h.owner, h.otherRunB, "handbook", 5); !errors.Is(err, wiki.ErrRunNotFound) {
		t.Fatalf("search on another Company's Run = %v, want ErrRunNotFound", err)
	}
	if _, err := h.reads.Read(ctx, h.owner, uuid.New(), page.ID, 0); !errors.Is(err, wiki.ErrRunNotFound) {
		t.Fatalf("read on an unknown Run = %v, want ErrRunNotFound", err)
	}
	if _, err := h.reads.Search(ctx, h.outsiderIn, h.taskRun, "handbook", 5); !errors.Is(err, wiki.ErrRunNotFound) {
		t.Fatalf("search by a non-member = %v, want ErrRunNotFound", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`UPDATE company_members SET status = 'revoked' WHERE company_id = $1 AND user_id = $2`,
		h.fixture.CompanyA,
		h.member.UserID,
	)
	if _, err := h.reads.Read(ctx, h.member, h.taskRun, page.ID, 0); !errors.Is(err, wiki.ErrRunNotFound) {
		t.Fatalf("read by a removed member = %v, want ErrRunNotFound", err)
	}
	if got := h.evidenceCount(t, `true`); got != 1 {
		t.Fatalf("evidence rows = %d, want only the one accepted search", got)
	}
}

func TestRunWikiReadsFollowTheTranscriptVisibilityRule(t *testing.T) {
	h := startAgentRead(t)
	ctx := t.Context()
	page := h.publish(t, h.owner, "Handbook", "Company handbook text.")
	for _, run := range []uuid.UUID{h.taskRun, h.directRun, h.scheduleRun} {
		if _, err := h.reads.Search(ctx, h.owner, run, "handbook", 5); err != nil {
			t.Fatal(err)
		}
		if _, err := h.reads.Read(ctx, h.owner, run, page.ID, 0); err != nil {
			t.Fatal(err)
		}
	}

	// A Task Run's trail follows its Task chat (ADR 0017): a member who does
	// not read the restricted Project gets the same answer as for no Run.
	if _, err := h.reads.ListRunReads(ctx, h.member, h.taskRun); !errors.Is(err, wiki.ErrRunNotFound) {
		t.Fatalf("non-reader's view of the Task Run = %v, want ErrRunNotFound", err)
	}
	// A reader of the Project sees it, search row first.
	dbtest.GrantProject(t, ctx, h.pool, h.fixture.CompanyA, h.fixture.ProjectA, h.member.UserID, "project.read")
	trail, err := h.reads.ListRunReads(ctx, h.member, h.taskRun)
	if err != nil || len(trail.Items) != 2 {
		t.Fatalf("member's view of the Task Run = %+v, %v", trail, err)
	}
	search := trail.Items[0]
	if search.Kind != "search" || search.Query != "handbook" || search.ResultCount == nil ||
		*search.ResultCount != 1 ||
		search.PageID != nil ||
		search.RevisionID != nil ||
		search.RevisionNumber != nil ||
		search.Page != nil {
		t.Fatalf("search row = %+v", search)
	}
	// A direct or Schedule Conversation's Run is its opener's alone.
	for kind, run := range map[string]uuid.UUID{"direct": h.directRun, "schedule": h.scheduleRun} {
		if _, err := h.reads.ListRunReads(ctx, h.member, run); !errors.Is(err, wiki.ErrRunNotFound) {
			t.Fatalf("member's view of the owner's %s Run = %v, want ErrRunNotFound", kind, err)
		}
		if own, err := h.reads.ListRunReads(ctx, h.owner, run); err != nil || len(own.Items) != 2 {
			t.Fatalf("owner's view of their %s Run = %+v, %v", kind, own, err)
		}
	}
	for name, call := range map[string]func() error{
		"unknown Run":       func() error { _, err := h.reads.ListRunReads(ctx, h.owner, uuid.New()); return err },
		"another Company":   func() error { _, err := h.reads.ListRunReads(ctx, h.owner, h.otherRunB); return err },
		"non-member":        func() error { _, err := h.reads.ListRunReads(ctx, h.outsiderIn, h.taskRun); return err },
		"foreign principal": func() error { _, err := h.reads.ListRunReads(ctx, h.foreign, h.taskRun); return err },
	} {
		if err := call(); !errors.Is(err, wiki.ErrRunNotFound) {
			t.Fatalf("%s = %v, want ErrRunNotFound", name, err)
		}
	}
}

func TestPageAgentReadsGroupRunsAndHideInvisibleOnes(t *testing.T) {
	h := startAgentRead(t)
	ctx := t.Context()
	page := h.publish(t, h.owner, "Handbook", "Company handbook text.")
	other := h.publish(t, h.owner, "Unread", "Nobody reads this.")

	mustRead := func(run uuid.UUID) {
		t.Helper()
		if _, err := h.reads.Read(ctx, h.owner, run, page.ID, 0); err != nil {
			t.Fatal(err)
		}
	}
	mustRead(h.taskRun)
	mustRead(h.taskRun)
	if _, err := h.service.UpdatePage(ctx, h.owner, page.ID, updatePageInput{
		ExpectedRevision: page.Revision, Title: "Handbook", Body: "Second edition.", Summary: "s",
	}); err != nil {
		t.Fatal(err)
	}
	mustRead(h.directRun)
	if _, err := h.reads.Search(
		ctx,
		h.owner,
		h.directRun,
		"handbook",
		5,
	); err != nil { // searches are not page reads
		t.Fatal(err)
	}
	mustRead(h.taskRun) // the Task Run's reads collapse into this latest one

	owner, err := h.reads.ListPageAgentReads(ctx, h.owner, page.ID, 10)
	if err != nil || owner.Total != 2 || len(owner.Items) != 2 {
		t.Fatalf("owner's view = %+v, %v", owner, err)
	}
	taskEntry, directEntry := owner.Items[0], owner.Items[1]
	if taskEntry.RunID != h.taskRun || taskEntry.RevisionNumber != 2 || taskEntry.TaskID == nil ||
		*taskEntry.TaskID != h.fixture.TaskA ||
		!taskEntry.LastReadAt.After(directEntry.LastReadAt) {
		t.Fatalf("newest entry = %+v, want the Task Run at its latest read, revision 2", taskEntry)
	}
	if directEntry.RunID != h.directRun || directEntry.RevisionNumber != 2 || directEntry.TaskID != nil ||
		directEntry.AgentID != h.fixture.AgentA || directEntry.AgentName != "Agent A" {
		t.Fatalf("older entry = %+v, want the direct Run at revision 2", directEntry)
	}

	if limited, err := h.reads.ListPageAgentReads(
		ctx,
		h.owner,
		page.ID,
		1,
	); err != nil || limited.Total != 2 ||
		len(limited.Items) != 1 {
		t.Fatalf("limited view = %+v, %v", limited, err)
	}
	// The other member does not see the owner's direct Run, nor count it,
	// and sees the Task Run only while reading its Project (ADR 0017).
	if member, err := h.reads.ListPageAgentReads(ctx, h.member, page.ID, 10); err != nil ||
		member.Total != 0 || len(member.Items) != 0 {
		t.Fatalf("non-reader member's view = %+v, %v", member, err)
	}
	dbtest.GrantProject(t, ctx, h.pool, h.fixture.CompanyA, h.fixture.ProjectA, h.member.UserID, "project.read")
	member, err := h.reads.ListPageAgentReads(ctx, h.member, page.ID, 10)
	if err != nil || member.Total != 1 || len(member.Items) != 1 || member.Items[0].RunID != h.taskRun {
		t.Fatalf("member's view = %+v, %v", member, err)
	}

	// A direct Run that came from a Task names it only to a reader of the
	// Task's Project; the owner who loses the Project keeps their own direct
	// Run but no longer sees the Task Run or the Task's id.
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`UPDATE runs SET source_task_id = $2 WHERE id = $1`,
		h.directRun,
		h.fixture.TaskA,
	)
	if sourced, err := h.reads.ListPageAgentReads(
		ctx,
		h.owner,
		page.ID,
		10,
	); err != nil || len(sourced.Items) != 2 ||
		sourced.Items[1].TaskID == nil ||
		*sourced.Items[1].TaskID != h.fixture.TaskA {
		t.Fatalf("owner's view with a sourced direct Run = %+v, %v", sourced, err)
	}
	dbtest.MustExec(t, ctx, h.pool, `DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2`,
		h.fixture.ProjectA, h.fixture.UserA)
	lost, err := h.reads.ListPageAgentReads(ctx, h.owner, page.ID, 10)
	if err != nil || lost.Total != 1 || len(lost.Items) != 1 || lost.Items[0].RunID != h.directRun ||
		lost.Items[0].TaskID != nil {
		t.Fatalf("owner's view after losing the Project = %+v, %v", lost, err)
	}
	if none, err := h.reads.ListPageAgentReads(
		ctx,
		h.owner,
		other.ID,
		10,
	); err != nil || none.Total != 0 ||
		len(none.Items) != 0 {
		t.Fatalf("unread page = %+v, %v", none, err)
	}
	for name, call := range map[string]func() error{
		"unknown page": func() error { _, err := h.reads.ListPageAgentReads(ctx, h.owner, uuid.New(), 10); return err },
		"non-member":   func() error { _, err := h.reads.ListPageAgentReads(ctx, h.outsiderIn, page.ID, 10); return err },
	} {
		if err := call(); !errors.Is(err, wiki.ErrPageNotFound) {
			t.Fatalf("%s = %v, want ErrPageNotFound", name, err)
		}
	}
	for _, limit := range []int{0, 51} {
		if _, err := h.reads.ListPageAgentReads(
			ctx,
			h.owner,
			page.ID,
			limit,
		); !errors.Is(
			err,
			wiki.ErrPageInvalidInput,
		) {
			t.Fatalf("limit %d = %v, want ErrPageInvalidInput", limit, err)
		}
	}
}

func searchIDs(items []wiki.AgentSearchItem) []uuid.UUID {
	ids := make([]uuid.UUID, len(items))
	for i, item := range items {
		ids[i] = item.PageID
	}
	return ids
}

// A title hit outranks a summary hit, which outranks a body-only hit, however
// often the body repeats the word and however recently it was edited.
func TestAgentSearchRanksTitleThenSummaryThenBody(t *testing.T) {
	h := startAgentRead(t)
	ctx := t.Context()
	create := func(title, summary, body string) wiki.PageResponse {
		t.Helper()
		page, err := h.service.CreatePage(
			ctx,
			h.owner,
			wiki.CreatePageInput{Title: title, Summary: summary, Body: body},
		)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	title := create("Onboarding", "How new hires start", "Welcome aboard.")
	summary := create("Handbook", "How onboarding works", "Nothing about it here.")
	body := create("Policies", "Company rules", strings.Repeat("Onboarding onboarding onboarding. ", 6))

	result, err := h.reads.Search(ctx, h.owner, h.taskRun, "onboarding", 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []uuid.UUID{title.ID, summary.ID, body.ID}
	if got := searchIDs(result.Items); result.Total != 3 || !slices.Equal(got, want) {
		t.Fatalf(
			"search = %v (total %d), want title, summary, body hits in that order %v",
			got,
			result.Total,
			want,
		)
	}
}

// A question whose words all appear on one page finds that page and only the
// pages holding every word; when no page holds them all the words are OR-ed.
func TestAgentSearchPrefersPagesHoldingEveryWordAndFallsBackToAny(t *testing.T) {
	h := startAgentRead(t)
	ctx := t.Context()
	both := h.publish(t, h.owner, "Refund policy", "Customers receive a refund within 14 days.")
	h.publish(t, h.owner, "Refund process", "Requests are reviewed weekly.")
	h.publish(t, h.owner, "Travel policy", "Employees book flights early.")
	holidays := h.publish(t, h.owner, "Holidays", "The office is closed in December.")

	all, err := h.reads.Search(ctx, h.owner, h.taskRun, "refund policy", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(all.Items); all.Total != 1 || !slices.Equal(got, []uuid.UUID{both.ID}) {
		t.Fatalf("search = %v (total %d), want only the page with both words", got, all.Total)
	}
	var recorded int32
	if err := h.pool.QueryRow(ctx, `SELECT result_count FROM run_wiki_reads WHERE run_id = $1 AND kind = 'search' AND query = 'refund policy'`, h.taskRun).
		Scan(&recorded); err != nil ||
		recorded != 1 {
		t.Fatalf("recorded result_count = %d, %v; want the AND total 1", recorded, err)
	}

	// No page holds both words: pages with either one answer.
	either, err := h.reads.Search(ctx, h.owner, h.taskRun, "refund holidays", 10)
	if err != nil {
		t.Fatal(err)
	}
	if either.Total != 3 || len(either.Items) != 3 {
		t.Fatalf("fallback search = %+v, want the two refund pages and Holidays", either)
	}
	found := false
	for _, item := range either.Items {
		found = found || item.PageID == holidays.ID
	}
	if !found {
		t.Fatalf("fallback search %+v misses the page holding the other word", either)
	}

	// A natural-language question is no longer a match on every page.
	question, err := h.reads.Search(ctx, h.owner, h.taskRun, "what is the refund policy", 10)
	if err != nil || question.Total == 0 {
		t.Fatalf("question = %+v, %v", question, err)
	}
	if question.Items[0].PageID != both.ID {
		t.Fatalf("question ranks %v first, want the Refund policy page %s", searchIDs(question.Items), both.ID)
	}
}

// A page link keeps its label in the index and loses its id, which is not
// text anyone searches for and would match unrelated words and numbers.
func TestAgentSearchIndexesLinkLabelsNotLinkTargets(t *testing.T) {
	h := startAgentRead(t)
	ctx := t.Context()
	target := h.publish(t, h.owner, "Pricing sheet", "Prices.")
	index := h.publish(t, h.owner, "Index", "See [["+target.ID.String()+"|Rate card]] for details.")

	byLabel, err := h.reads.Search(ctx, h.owner, h.taskRun, "rate card", 5)
	if err != nil || len(byLabel.Items) != 1 || byLabel.Items[0].PageID != index.ID {
		t.Fatalf("search by link label = %+v, %v", byLabel, err)
	}
	byID, err := h.reads.Search(ctx, h.owner, h.taskRun, target.ID.String(), 5)
	if err != nil || byID.Total != 0 {
		t.Fatalf("search by link target id = %+v, %v; want none", byID, err)
	}
}

func TestAgentListBrowsesActivePagesByTitleAndRecordsEvidence(t *testing.T) {
	h := startAgentRead(t)
	ctx := t.Context()
	handbook, err := h.service.CreateCollection(ctx, h.owner, "Handbook")
	if err != nil {
		t.Fatal(err)
	}
	policies, err := h.service.CreateCollection(ctx, h.owner, "Policies")
	if err != nil {
		t.Fatal(err)
	}
	create := func(title string, collection *uuid.UUID) wiki.PageResponse {
		t.Helper()
		page, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{
			Title: title, Summary: title + " summary", Body: title + " body", CollectionID: collection,
		})
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	beta := create("Beta", &handbook.ID)
	alpha := create("Alpha", &handbook.ID)
	gamma := create("Gamma", &policies.ID)
	delta := create("Delta", nil)
	archived := create("Zeta", &handbook.ID)
	if _, err := h.service.ArchivePage(ctx, h.owner, archived.ID, archived.Revision); err != nil {
		t.Fatal(err)
	}
	foreignCollection, err := h.service.CreateCollection(ctx, h.foreign, "Foreign")
	if err != nil {
		t.Fatal(err)
	}
	h.publish(t, h.foreign, "Foreign page", "secret")

	itemIDs := func(list wiki.AgentListResponse) []uuid.UUID {
		ids := make([]uuid.UUID, len(list.Items))
		for i, item := range list.Items {
			ids[i] = item.PageID
		}
		return ids
	}

	// The first page carries the collections and the next offset; pages
	// come ordered by title; archived and foreign pages are absent.
	first, err := h.reads.List(ctx, h.owner, h.taskRun, nil, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemIDs(first); first.Total != 4 || !slices.Equal(got, []uuid.UUID{alpha.ID, beta.ID, delta.ID}) ||
		first.NextOffset == nil || *first.NextOffset != 3 {
		t.Fatalf("first page = %+v, want Alpha Beta Delta of 4 and nextOffset 3", first)
	}
	if len(first.Collections) != 2 || first.Collections[0].Name != "Handbook" ||
		first.Collections[0].PageCount != 2 ||
		first.Collections[1].Name != "Policies" ||
		first.Collections[1].PageCount != 1 {
		t.Fatalf(
			"collections = %+v, want Handbook 2 and Policies 1 (archived page not counted)",
			first.Collections,
		)
	}
	if a := first.Items[0]; a.Title != "Alpha" || a.Summary != "Alpha summary" || a.Revision != 1 ||
		a.CollectionName == nil || *a.CollectionName != "Handbook" || a.UpdatedAt.IsZero() {
		t.Fatalf("first item = %+v", a)
	}
	if first.Items[2].CollectionName != nil {
		t.Fatalf("uncollected page reports collection %q", *first.Items[2].CollectionName)
	}

	// The last page has no next offset and no collections.
	last, err := h.reads.List(ctx, h.owner, h.taskRun, nil, 3, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemIDs(
		last,
	); last.Total != 4 || !slices.Equal(got, []uuid.UUID{gamma.ID}) || last.NextOffset != nil ||
		last.Collections == nil ||
		len(last.Collections) != 0 {
		t.Fatalf("last page = %+v, want Gamma alone, no collections, no next offset", last)
	}
	past, err := h.reads.List(ctx, h.owner, h.taskRun, nil, 40, 3)
	if err != nil || len(past.Items) != 0 || past.Total != 4 || past.NextOffset != nil {
		t.Fatalf("page past the end = %+v, %v; want no items and the real total", past, err)
	}

	// A collection narrows the pages and the total, not the overview.
	filtered, err := h.reads.List(ctx, h.owner, h.taskRun, &handbook.ID, 0, 25)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemIDs(filtered); filtered.Total != 2 || !slices.Equal(got, []uuid.UUID{alpha.ID, beta.ID}) ||
		filtered.NextOffset != nil || len(filtered.Collections) != 2 {
		t.Fatalf("filtered list = %+v, want Alpha and Beta of 2", filtered)
	}

	// Every call left a list row; the browse's collection and total are on it.
	rows, err := h.pool.Query(
		ctx,
		`SELECT query, result_count FROM run_wiki_reads WHERE run_id = $1 AND kind = 'list' ORDER BY created_at, id`,
		h.taskRun,
	)
	if err != nil {
		t.Fatal(err)
	}
	type listRow struct {
		query string
		count int32
	}
	var got []listRow
	for rows.Next() {
		var row listRow
		if err := rows.Scan(&row.query, &row.count); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	rows.Close()
	if want := []listRow{{"", 4}, {"", 4}, {"", 4}, {"Handbook", 2}}; !slices.Equal(got, want) {
		t.Fatalf("list evidence = %+v, want %+v", got, want)
	}
	trail, err := h.reads.ListRunReads(ctx, h.owner, h.taskRun)
	if err != nil || len(trail.Items) != 4 {
		t.Fatalf("trail = %+v, %v", trail, err)
	}
	if item := trail.Items[3]; item.Kind != "list" || item.Query != "Handbook" || item.ResultCount == nil ||
		*item.ResultCount != 2 ||
		item.PageID != nil ||
		item.RevisionID != nil ||
		item.RevisionNumber != nil ||
		item.Page != nil {
		t.Fatalf("list trail row = %+v", item)
	}

	// Refused browses record nothing.
	before := h.evidenceCount(t, `kind = 'list'`)
	unknown := uuid.New()
	nilID := uuid.Nil
	for name, call := range map[string]struct {
		run        uuid.UUID
		principal  auth.UserPrincipal
		collection *uuid.UUID
		offset     int
		limit      int
		want       error
	}{
		"limit 0":                 {h.taskRun, h.owner, nil, 0, 0, wiki.ErrPageInvalidInput},
		"limit 51":                {h.taskRun, h.owner, nil, 0, 51, wiki.ErrPageInvalidInput},
		"negative offset":         {h.taskRun, h.owner, nil, -1, 5, wiki.ErrPageInvalidInput},
		"offset too deep":         {h.taskRun, h.owner, nil, wiki.MaxAgentListOffset + 1, 5, wiki.ErrPageInvalidInput},
		"nil collection id":       {h.taskRun, h.owner, &nilID, 0, 5, wiki.ErrPageInvalidInput},
		"nil Run":                 {uuid.Nil, h.owner, nil, 0, 5, wiki.ErrPageInvalidInput},
		"unknown collection":      {h.taskRun, h.owner, &unknown, 0, 5, wiki.ErrCollectionNotFound},
		"another Company's":       {h.taskRun, h.owner, &foreignCollection.ID, 0, 5, wiki.ErrCollectionNotFound},
		"another Company's Run":   {h.otherRunB, h.owner, nil, 0, 5, wiki.ErrRunNotFound},
		"non-member of the Run's": {h.taskRun, h.outsiderIn, nil, 0, 5, wiki.ErrRunNotFound},
	} {
		if _, err := h.reads.List(
			ctx,
			call.principal,
			call.run,
			call.collection,
			call.offset,
			call.limit,
		); !errors.Is(
			err,
			call.want,
		) {
			t.Fatalf("%s list = %v, want %v", name, err, call.want)
		}
	}
	if after := h.evidenceCount(t, `kind = 'list'`); after != before {
		t.Fatalf("refused browses recorded %d rows", after-before)
	}
}

func TestAgentListHonoursTheAgentSwitch(t *testing.T) {
	h := startAgentRead(t)
	ctx := t.Context()
	h.publish(t, h.owner, "Handbook", "Company handbook text.")
	dbtest.MustExec(t, ctx, h.pool, `UPDATE agents SET settings = $2::jsonb WHERE id = $1`,
		h.fixture.AgentA, `{"capabilities":{"disabledBuiltIns":["wiki"]}}`)
	if _, err := h.reads.List(ctx, h.owner, h.taskRun, nil, 0, 5); !errors.Is(err, wiki.ErrAgentAccessDisabled) {
		t.Fatalf("list with wiki disabled = %v, want ErrAgentAccessDisabled", err)
	}
	if got := h.evidenceCount(t, `true`); got != 0 {
		t.Fatalf("disabled Agent left %d evidence rows", got)
	}
	dbtest.MustExec(t, ctx, h.pool, `UPDATE agents SET settings = $2::jsonb WHERE id = $1`,
		h.fixture.AgentA, `{"capabilities":{"disabledBuiltIns":[]}}`)
	if list, err := h.reads.List(ctx, h.owner, h.taskRun, nil, 0, 5); err != nil || list.Total != 1 {
		t.Fatalf("list after re-enabling = %+v, %v", list, err)
	}
}
