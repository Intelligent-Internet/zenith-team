//go:build integration

package wiki_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

type agentWriteHarness struct {
	wikiHarness
	writes   *wiki.DefaultAgentWriteService
	reads    *wiki.DefaultAgentReadService
	research uuid.UUID
	admin    auth.UserPrincipal // an admin of Company A who is not the Run's authorizer
	member   auth.UserPrincipal // a plain member of Company A
}

func startAgentWrite(t *testing.T) agentWriteHarness {
	t.Helper()
	h := startWiki(t)
	ctx := t.Context()
	store, err := db.NewStore(h.pool)
	if err != nil {
		t.Fatal(err)
	}
	writes, err := wiki.NewAgentWriteService(store, h.bus)
	if err != nil {
		t.Fatal(err)
	}
	reads, err := wiki.NewAgentReadService(store, h.bus)
	if err != nil {
		t.Fatal(err)
	}
	addMember := func(email, role string) auth.UserPrincipal {
		id := dbtest.NewUUID(t)
		dbtest.MustExec(
			t,
			ctx,
			h.pool,
			`INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`,
			id,
			email,
		)
		dbtest.MustExec(
			t,
			ctx,
			h.pool,
			`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, $3)`,
			h.fixture.CompanyA,
			id,
			role,
		)
		return auth.UserPrincipal{UserID: id, CompanyID: h.fixture.CompanyA, Role: auth.CompanyRole(role)}
	}
	harness := agentWriteHarness{
		wikiHarness: h, writes: writes, reads: reads,
		admin:  addMember("wiki-admin@example.com", "admin"),
		member: addMember("wiki-plain@example.com", "member"),
	}
	harness.research = harness.newRun(t, "company_research", "running", h.fixture.UserA)
	return harness
}

// newRun inserts a direct-Conversation Run of the Agent with a frozen
// execution profile source, authorized by the given member.
func (h agentWriteHarness) newRun(t *testing.T, source, status string, authorizer uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := h.pool.QueryRow(t.Context(), `
		INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id, status, run_type, revision,
			context, execution_authorized_by_user_id)
		VALUES ($1, $2, $3, $4, $5, 'direct_chat', 1, jsonb_build_object('executionProfile',
			jsonb_build_object('source', $6::text)), $7) RETURNING id`,
		h.fixture.CompanyA, h.fixture.AgentA, h.fixture.DirectConversationA, h.fixture.RuntimeA,
		status, source, authorizer,
	).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func page(key, title, markdown string) wiki.AgentWritePageInput {
	return wiki.AgentWritePageInput{Key: key, Title: title, Summary: title + " summary", Markdown: markdown}
}

func (h agentWriteHarness) write(
	t *testing.T,
	run uuid.UUID,
	pages ...wiki.AgentWritePageInput,
) wiki.AgentWriteResponse {
	t.Helper()
	result, err := h.writes.Write(t.Context(), h.owner, run, wiki.AgentWriteInput{Pages: pages})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (h agentWriteHarness) outgoing(t *testing.T, id uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := h.pool.Query(
		t.Context(),
		`SELECT to_page_id FROM wiki_links WHERE from_page_id = $1 ORDER BY to_page_id`,
		id,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var to uuid.UUID
		if err := rows.Scan(&to); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, to)
	}
	return ids
}

func (h agentWriteHarness) pageCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := h.pool.QueryRow(t.Context(), `SELECT count(*) FROM wiki_pages WHERE company_id = $1`, h.fixture.CompanyA).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func byKey(result wiki.AgentWriteResponse) map[string]wiki.AgentWriteResult {
	items := map[string]wiki.AgentWriteResult{}
	for _, item := range result.Pages {
		items[item.Key] = item
	}
	return items
}

func TestAgentWritePublishesLinkedPagesAndRecordsEdgesToSameBatchPages(t *testing.T) {
	h := startAgentWrite(t)
	ctx := t.Context()
	h.eventNames()

	// The hub is listed first and links to pages that come later in the batch.
	result := h.write(
		t,
		h.research,
		page(
			"company-overview",
			"Company overview",
			"# Overview\n\nSee [[products]] and [[customers|our customers]].\n\n## Sources\n- site",
		),
		page("products", "Products", "Back to [[company-overview]]. Pricing is TBD."),
		page("customers", "Customers", "Back to [[company-overview]] and [[products]]. Also [[nowhere]]."),
	)
	items := byKey(result)
	if len(result.Pages) != 3 || result.TotalPages != 3 {
		t.Fatalf("result = %+v", result)
	}
	for key, item := range items {
		if item.Status != wiki.AgentWriteCreated || item.Revision != 1 || item.PageID == uuid.Nil {
			t.Fatalf("%s = %+v, want a created page at revision 1", key, item)
		}
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "nowhere") {
		t.Fatalf("warnings = %v, want one about [[nowhere]]", result.Warnings)
	}
	hub, products, customers := items["company-overview"].PageID, items["products"].PageID, items["customers"].PageID
	names := h.eventNames() // before the reads below publish evidence events

	// Edges exist in both directions, including the forward ones to pages that
	// were not yet published when the hub was.
	if got := h.outgoing(t, hub); len(got) != 2 {
		t.Fatalf("hub links = %v, want products and customers", got)
	}
	if got := h.outgoing(t, products); len(got) != 1 || got[0] != hub {
		t.Fatalf("products links = %v, want the hub", got)
	}
	if got := h.outgoing(t, customers); len(got) != 2 {
		t.Fatalf("customers links = %v, want hub and products", got)
	}

	// The pages are published, authored by the founder, and findable by search.
	read, err := h.reads.Read(ctx, h.owner, h.research, hub, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read.Content, products.String()) {
		t.Fatalf("hub text = %q, want a link to the products page", read.Content)
	}
	if strings.Contains(read.Content, "[[products]]") {
		t.Fatalf("placeholder survived: %q", read.Content)
	}
	page, err := h.service.GetPage(ctx, h.owner, hub)
	if err != nil || page.CreatedByUserID != h.fixture.UserA || page.LastAuthorUserID != h.fixture.UserA ||
		page.Status != "active" {
		t.Fatalf("hub = %+v, %v", page, err)
	}
	if found, err := h.reads.Search(
		ctx,
		h.owner,
		h.research,
		"pricing",
		5,
	); err != nil || found.Total != 1 ||
		found.Items[0].PageID != products {
		t.Fatalf("search = %+v, %v", found, err)
	}
	// The Company collection, then one page event per page, after the commit.
	if len(names) != 4 || names[0] != wiki.EventCollectionCreated {
		t.Fatalf("events = %v, want wiki.collection_created and three wiki.page_created", names)
	}
	for _, name := range names[1:] {
		if name != wiki.EventPageCreated {
			t.Fatalf("events = %v", names)
		}
	}
	var count int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM run_wiki_writes WHERE run_id = $1`, h.research).
		Scan(&count); err != nil ||
		count != 3 {
		t.Fatalf("run_wiki_writes = %d, %v", count, err)
	}
}

func TestAgentWriteResolvesLinksToPagesOfEarlierCalls(t *testing.T) {
	h := startAgentWrite(t)
	first := h.write(
		t,
		h.research,
		page("company-overview", "Company overview", "Hub. See [[products]] and [[open-questions]]."),
	)
	hub := byKey(first)["company-overview"]
	if len(first.Warnings) != 2 || h.outgoing(t, hub.PageID) != nil {
		t.Fatalf(
			"first call = %+v, links %v; want two warnings and no edges yet",
			first,
			h.outgoing(t, hub.PageID),
		)
	}

	// A later call writes the pages the hub named; the hub is relinked.
	h.eventNames()
	second := h.write(t, h.research,
		page("products", "Products", "Back to [[company-overview]]."),
		page("open-questions", "Open questions", "Back to [[company-overview]]."),
	)
	items := byKey(second)
	if len(second.Pages) != 3 || items["company-overview"].Status != wiki.AgentWriteUpdated ||
		items["company-overview"].Revision != 2 {
		t.Fatalf("second call = %+v, want the hub updated to revision 2", second)
	}
	if len(second.Warnings) != 0 || second.TotalPages != 3 {
		t.Fatalf("second call warnings %v total %d", second.Warnings, second.TotalPages)
	}
	if got := h.outgoing(t, hub.PageID); len(got) != 2 {
		t.Fatalf("hub links = %v, want products and open-questions", got)
	}
	if names := h.eventNames(); len(names) != 3 {
		t.Fatalf("events = %v, want two created and one updated", names)
	}
	read, err := h.reads.Read(t.Context(), h.owner, h.research, hub.PageID, 0)
	if err != nil || !strings.Contains(read.Content, items["products"].PageID.String()) {
		t.Fatalf("hub = %+v, %v", read, err)
	}
}

func TestAgentWriteUpsertsByKeyAndReplayIsIdempotent(t *testing.T) {
	h := startAgentWrite(t)
	ctx := t.Context()
	batch := []wiki.AgentWritePageInput{
		page("company-overview", "Company overview", "Hub. See [[products]]."),
		page("products", "Products", "Back to [[company-overview]]."),
	}
	created := h.write(t, h.research, batch...)
	pages := h.pageCount(t)
	h.eventNames()

	replay := h.write(t, h.research, batch...)
	for _, item := range replay.Pages {
		if item.Status != wiki.AgentWriteUnchanged || item.Revision != 1 ||
			item.PageID != byKey(created)[item.Key].PageID {
			t.Fatalf("replay item = %+v, want unchanged revision 1", item)
		}
	}
	if h.pageCount(t) != pages || len(h.eventNames()) != 0 {
		t.Fatal("a replayed write created pages or events")
	}

	// A changed page is a new revision of the same page.
	batch[1].Markdown = "Back to [[company-overview]]. New pricing details."
	updated := h.write(t, h.research, batch[1])
	item := updated.Pages[0]
	if item.Status != wiki.AgentWriteUpdated || item.Revision != 2 ||
		item.PageID != byKey(created)["products"].PageID ||
		updated.TotalPages != 2 {
		t.Fatalf("update = %+v", updated)
	}
	if got := h.outgoing(t, item.PageID); len(got) != 1 {
		t.Fatalf("updated page links = %v", got)
	}
	if names := h.eventNames(); len(names) != 1 || names[0] != wiki.EventPageUpdated {
		t.Fatalf("events = %v", names)
	}
	if found, err := h.reads.Search(
		ctx,
		h.owner,
		h.research,
		"pricing",
		5,
	); err != nil || found.Total != 1 ||
		found.Items[0].Revision != 2 {
		t.Fatalf("search after update = %+v, %v", found, err)
	}
}

// The research files every new page in the Company collection, created once.
func TestAgentWriteFilesNewPagesInTheCompanyCollection(t *testing.T) {
	h := startAgentWrite(t)
	h.eventNames()
	h.write(t, h.research, page("company-overview", "Company overview", "Hub."))
	if names := h.eventNames(); len(names) != 2 || names[0] != wiki.EventCollectionCreated {
		t.Fatalf("events = %v, want the collection then the page", names)
	}
	var name string
	if err := h.pool.QueryRow(t.Context(), `SELECT c.name FROM wiki_pages p JOIN wiki_collections c ON c.id = p.collection_id
		WHERE p.company_id = $1`, h.fixture.CompanyA).
		Scan(&name); err != nil ||
		name != "Company" {
		t.Fatalf("overview collection = %q, %v; want Company", name, err)
	}

	// A rewrite of a known page alone creates no collection.
	h.write(t, h.research, page("company-overview", "Company overview", "Hub, again."))
	h.write(t, h.research, page("products", "Products", "Back to [[company-overview]]."))
	var collections, filed int
	if err := h.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM wiki_collections WHERE company_id = $1),
		(SELECT count(*) FROM wiki_pages WHERE company_id = $1 AND collection_id IS NOT NULL)`, h.fixture.CompanyA).Scan(&collections, &filed); err != nil {
		t.Fatal(err)
	}
	if collections != 1 || filed != 2 {
		t.Fatalf("collections = %d, filed pages = %d, want 1 and 2", collections, filed)
	}
}

func TestAgentWriteReusesAPersonsCompanyCollection(t *testing.T) {
	h := startAgentWrite(t)
	mine, err := h.service.CreateCollection(t.Context(), h.owner, "COMPANY")
	if err != nil {
		t.Fatal(err)
	}
	hub := byKey(h.write(t, h.research, page("company-overview", "Company overview", "Hub.")))["company-overview"]
	got, err := h.service.GetPage(t.Context(), h.owner, hub.PageID)
	if err != nil || got.CollectionID == nil || *got.CollectionID != mine.ID {
		t.Fatalf("overview collection = %v, %v; want the person's %s", got.CollectionID, err, mine.ID)
	}
}

func TestAgentWriteNeverTouchesPagesItDidNotCreate(t *testing.T) {
	h := startAgentWrite(t)
	ctx := t.Context()
	human, err := h.service.CreatePage(
		ctx,
		h.owner,
		wiki.CreatePageInput{Title: "company-overview", Body: "Human text", Summary: "s"},
	)
	if err != nil {
		t.Fatal(err)
	}
	// The same key and title on the Run's side makes a new page, not an edit.
	result := h.write(t, h.research, page("company-overview", "company-overview", "Agent text"))
	if result.Pages[0].PageID == human.ID || result.Pages[0].Status != wiki.AgentWriteCreated {
		t.Fatalf("write reused a person's page: %+v", result)
	}
	if got, err := h.service.GetPage(
		ctx,
		h.owner,
		human.ID,
	); err != nil || got.Body != "Human text" ||
		got.Revision != human.Revision {
		t.Fatalf("person's page changed: %+v, %v", got, err)
	}

	// A page a person archived is reported and left alone.
	mine := result.Pages[0]
	archived, err := h.service.GetPage(ctx, h.owner, mine.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.ArchivePage(ctx, h.owner, mine.PageID, archived.Revision); err != nil {
		t.Fatal(err)
	}
	skipped := h.write(t, h.research, page("company-overview", "company-overview", "Agent edit"))
	if len(skipped.Pages) != 0 || len(skipped.Warnings) != 1 || !strings.Contains(skipped.Warnings[0], "archived") {
		t.Fatalf("write to an archived page = %+v", skipped)
	}
}

func TestAgentWriteRefusesAnyRunThatIsNotTheLiveResearchRunOfTheCaller(t *testing.T) {
	h := startAgentWrite(t)
	ctx := t.Context()
	input := wiki.AgentWriteInput{Pages: []wiki.AgentWritePageInput{page("a", "A", "text")}}
	before := h.pageCount(t)

	cases := map[string]struct {
		principal auth.UserPrincipal
		run       uuid.UUID
		want      error
	}{
		"task run": {h.owner, h.fixture.RunA, wiki.ErrAgentWriteForbidden},
		"direct chat run": {
			h.owner,
			h.newRun(t, "human_input", "running", h.fixture.UserA),
			wiki.ErrAgentWriteForbidden,
		},
		"finished research": {
			h.owner,
			h.newRun(t, "company_research", "completed", h.fixture.UserA),
			wiki.ErrAgentWriteForbidden,
		},
		"queued research": {
			h.owner,
			h.newRun(t, "company_research", "queued", h.fixture.UserA),
			wiki.ErrAgentWriteForbidden,
		},
		"another authorizer": {h.admin, h.research, wiki.ErrAgentWriteForbidden},
		"run without founder": {
			h.owner,
			h.newRun(t, "company_research", "running", h.admin.UserID),
			wiki.ErrAgentWriteForbidden,
		},
		"unknown run":       {h.owner, uuid.New(), wiki.ErrRunNotFound},
		"run of another co": {h.owner, h.fixture.RunB, wiki.ErrRunNotFound},
		"nil run":           {h.owner, uuid.Nil, wiki.ErrRunNotFound},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := h.writes.Write(ctx, test.principal, test.run, input); !errors.Is(err, test.want) {
				t.Fatalf("Write() = %v, want %v", err, test.want)
			}
		})
	}

	// A member without wiki.publish cannot write even for a Run of their own.
	memberRun := h.newRun(t, "company_research", "running", h.member.UserID)
	if _, err := h.writes.Write(ctx, h.member, memberRun, input); !errors.Is(err, wiki.ErrPageForbidden) {
		t.Fatalf("member Write() = %v, want ErrPageForbidden", err)
	}
	// An outsider named against the Company is not a member at all.
	outsider := auth.UserPrincipal{
		UserID:    h.fixture.UserB,
		CompanyID: h.fixture.CompanyA,
		Role:      auth.CompanyRoleOwner,
	}
	if _, err := h.writes.Write(ctx, outsider, h.research, input); !errors.Is(err, wiki.ErrPageNotFound) {
		t.Fatalf("outsider Write() = %v, want ErrPageNotFound", err)
	}
	if h.pageCount(t) != before {
		t.Fatal("a refused write left pages behind")
	}

	// The Agent's Wiki switch is enforced by the server.
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`UPDATE agents SET settings = jsonb_build_object('capabilities', jsonb_build_object('disabledBuiltIns', jsonb_build_array('wiki'))) WHERE id = $1`,
		h.fixture.AgentA,
	)
	if _, err := h.writes.Write(ctx, h.owner, h.research, input); !errors.Is(err, wiki.ErrAgentAccessDisabled) {
		t.Fatalf("switched-off Write() = %v, want ErrAgentAccessDisabled", err)
	}
}

func TestAgentWriteEnforcesThePerRunPageCap(t *testing.T) {
	h := startAgentWrite(t)
	ctx := t.Context()
	for batch := 0; batch*wiki.MaxAgentWritePagesPerCall < wiki.MaxAgentWritePagesPerRun; batch++ {
		var pages []wiki.AgentWritePageInput
		for i := 0; i < wiki.MaxAgentWritePagesPerCall &&
			batch*wiki.MaxAgentWritePagesPerCall+i < wiki.MaxAgentWritePagesPerRun; i++ {
			n := batch*wiki.MaxAgentWritePagesPerCall + i
			pages = append(pages, page(fmt.Sprintf("p-%d", n), fmt.Sprintf("Page %d", n), "text"))
		}
		if result := h.write(
			t,
			h.research,
			pages...); result.TotalPages != batch*wiki.MaxAgentWritePagesPerCall+len(
			pages,
		) {
			t.Fatalf("total = %d", result.TotalPages)
		}
	}
	before := h.pageCount(t)
	_, err := h.writes.Write(
		ctx,
		h.owner,
		h.research,
		wiki.AgentWriteInput{Pages: []wiki.AgentWritePageInput{page("one-more", "One more", "x")}},
	)
	if !errors.Is(err, wiki.ErrAgentWriteLimit) || h.pageCount(t) != before {
		t.Fatalf(
			"41st page = %v (pages %d -> %d), want ErrAgentWriteLimit and no page",
			err,
			before,
			h.pageCount(t),
		)
	}
	// Updating a page it already owns is still allowed at the cap.
	if _, err := h.writes.Write(
		ctx,
		h.owner,
		h.research,
		wiki.AgentWriteInput{Pages: []wiki.AgentWritePageInput{page("p-0", "Page 0", "changed")}},
	); err != nil {
		t.Fatalf("update at the cap = %v", err)
	}
}

func TestAgentWriteRollsBackTheWholeBatchOnFailure(t *testing.T) {
	h := startAgentWrite(t)
	before := h.pageCount(t)
	// The second page cites an Artifact nobody can open, which publishing refuses.
	_, err := h.writes.Write(
		t.Context(),
		h.owner,
		h.research,
		wiki.AgentWriteInput{Pages: []wiki.AgentWritePageInput{
			page("good", "Good", "fine"),
			page("bad", "Bad", "cites [[artifact:"+uuid.NewString()+"]]"),
		}},
	)
	if !errors.Is(err, wiki.ErrArtifactRefUnavailable) {
		t.Fatalf("Write() = %v, want ErrArtifactRefUnavailable", err)
	}
	var owned int
	if err := h.pool.QueryRow(t.Context(), `SELECT count(*) FROM run_wiki_writes WHERE run_id = $1`, h.research).
		Scan(&owned); err != nil || owned != 0 ||
		h.pageCount(t) != before {
		t.Fatalf("failed batch left %d writes, pages %d -> %d (%v)", owned, before, h.pageCount(t), err)
	}
	if names := h.eventNames(); len(names) != 0 {
		t.Fatalf("failed batch published %v", names)
	}
}

// newRetryChild is an automatic-retry child of the parent Run.
func (h agentWriteHarness) newRetryChild(t *testing.T, parent uuid.UUID) uuid.UUID {
	t.Helper()
	child := h.newRun(t, "company_research", "running", h.fixture.UserA)
	dbtest.MustExec(t, t.Context(), h.pool, `UPDATE runs SET parent_run_id = $1 WHERE id = $2`, parent, child)
	dbtest.MustExec(t, t.Context(), h.pool, `UPDATE runs SET status = 'failed' WHERE id = $1`, parent)
	return child
}

// personDraft saves a new title into the page's draft as a person, without
// publishing it, and returns the page as it was and the draft sequence.
func (h agentWriteHarness) personDraft(t *testing.T, pageID uuid.UUID, title string) (wiki.PageResponse, int64) {
	t.Helper()
	s := h.service.DefaultWikiService
	current, err := s.GetPage(t.Context(), h.owner, pageID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := s.ReadDocument(t.Context(), h.owner, pageID, wiki.DocumentReadInput{Since: -1})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.UpdateDraftMetadata(t.Context(), h.owner, pageID, wiki.DraftMetadataInput{
		ExpectedSequence: draft.Sequence,
		Title:            title,
		Summary:          current.Summary,
		CollectionID:     current.CollectionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return current, saved.Sequence
}

// personEdit publishes a new revision of the page as a person.
func (h agentWriteHarness) personEdit(t *testing.T, pageID uuid.UUID, title string) {
	t.Helper()
	current, sequence := h.personDraft(t, pageID, title)
	if _, err := h.service.PublishPage(t.Context(), h.owner, pageID, wiki.PublishPageInput{
		ExpectedRevision: current.Revision, ExpectedSequence: sequence,
	}); err != nil {
		t.Fatal(err)
	}
}

// The founder's manual retry is a new Run with no parent: it still owns the
// pages the failed attempt published, so it updates them in place instead of
// writing a second "Company overview".
func TestAgentWriteManualRetryUpdatesTheEarlierAttemptsPages(t *testing.T) {
	h := startAgentWrite(t)
	first := h.write(t, h.research, page("company-overview", "Company overview", "Hub."))
	dbtest.MustExec(t, t.Context(), h.pool, `UPDATE runs SET status = 'failed' WHERE id = $1`, h.research)
	retry := h.newRun(t, "company_research", "running", h.fixture.UserA)
	pages := h.pageCount(t)

	second := h.write(t, retry, page("company-overview", "Company overview", "Hub, researched again."))
	got := byKey(second)["company-overview"]
	if got.PageID != byKey(first)["company-overview"].PageID || got.Status != wiki.AgentWriteUpdated ||
		h.pageCount(t) != pages {
		t.Fatalf("retry wrote a copy instead of updating: %+v, pages %d -> %d", second, pages, h.pageCount(t))
	}
}

// The editor saves drafts continuously, so a person editing a research page
// has unpublished work the Run must not replace.
func TestAgentWriteLeavesAPageWithAnUnpublishedPersonDraft(t *testing.T) {
	h := startAgentWrite(t)
	hub := byKey(h.write(t, h.research, page("company-overview", "Company overview", "Hub.")))["company-overview"]
	h.personDraft(t, hub.PageID, "Our company, being edited")

	rewrite := h.write(t, h.research, page("company-overview", "Company overview", "Agent rewrite."))
	if len(rewrite.Pages) != 0 || len(rewrite.Warnings) != 1 ||
		!strings.Contains(rewrite.Warnings[0], "edited by a person") {
		t.Fatalf("rewrite = %+v", rewrite)
	}
	draft, err := h.service.ReadDocument(t.Context(), h.owner, hub.PageID, wiki.DocumentReadInput{Since: -1})
	if err != nil || draft.Title != "Our company, being edited" ||
		strings.Contains(draft.Markdown, "Agent rewrite") {
		t.Fatalf("the person's draft was overwritten: %+v, %v", draft, err)
	}
}

// A research page names the Agent that wrote it for the founder, and a body
// that opens by repeating the title does not show the title twice.
func TestAgentWriteAttributesThePageToTheAgentAndDropsARepeatedTitle(t *testing.T) {
	h := startAgentWrite(t)
	hub := byKey(h.write(t, h.research,
		page("company-overview", "Company overview", "# Company overview\n\n## Summary\nHub.")))["company-overview"]
	got, err := h.service.GetPage(t.Context(), h.owner, hub.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastAuthorUserID != h.owner.UserID || got.LastAuthorAgentID == nil ||
		*got.LastAuthorAgentID != h.fixture.AgentA ||
		got.LastAuthorAgentName == nil ||
		*got.LastAuthorAgentName == "" {
		t.Fatalf(
			"author = user %s agent %v %v, want the Agent for the founder",
			got.LastAuthorUserID,
			got.LastAuthorAgentID,
			got.LastAuthorAgentName,
		)
	}
	if strings.Contains(got.Body, "# Company overview") || !strings.Contains(got.Body, "## Summary") {
		t.Fatalf("body = %q, want the repeated title heading dropped", got.Body)
	}
	revisions, err := h.service.ListRevisions(t.Context(), h.owner, hub.PageID, 0, 10)
	if err != nil || len(revisions.Items) == 0 || revisions.Items[0].AuthorAgentID == nil ||
		*revisions.Items[0].AuthorAgentID != h.fixture.AgentA {
		t.Fatalf("revisions = %+v, %v", revisions, err)
	}
}

func TestAgentWriteRetryChildOwnsTheLineagePagesAndSharesTheCap(t *testing.T) {
	h := startAgentWrite(t)
	first := h.write(t, h.research,
		page("company-overview", "Company overview", "Hub."),
		page("products", "Products", "Back to [[company-overview]]."),
	)
	child := h.newRetryChild(t, h.research)
	pages := h.pageCount(t)

	// The child updates the parent's pages in place and links to them.
	second := h.write(t, child,
		page("products", "Products", "Back to [[company-overview]]. More."),
		page("customers", "Customers", "See [[products]] and [[company-overview]]."),
	)
	items := byKey(second)
	if items["products"].PageID != byKey(first)["products"].PageID ||
		items["products"].Status != wiki.AgentWriteUpdated {
		t.Fatalf("child duplicated or missed the parent's page: %+v", second)
	}
	if items["customers"].Status != wiki.AgentWriteCreated || second.TotalPages != 3 || h.pageCount(t) != pages+1 {
		t.Fatalf("child result = %+v, pages %d -> %d", second, pages, h.pageCount(t))
	}
	if len(second.Warnings) != 0 {
		t.Fatalf("warnings = %v, want links to the parent's pages to resolve", second.Warnings)
	}
	if got := h.outgoing(t, items["customers"].PageID); len(got) != 2 {
		t.Fatalf("customers links = %v, want 2", got)
	}
}

func TestAgentWriteLeavesAPageAPersonEdited(t *testing.T) {
	h := startAgentWrite(t)
	result := h.write(t, h.research,
		page("company-overview", "Company overview", "Hub. See [[products]]."),
		page("products", "Products", "Back to [[company-overview]]."),
		page("later", "Later", "Links [[company-overview]] and [[faq]]."),
	)
	hub, later := byKey(result)["company-overview"], byKey(result)["later"]
	h.personEdit(t, hub.PageID, "Our company")
	h.personEdit(t, later.PageID, "Later, edited")

	// A rewrite of the edited page is skipped with a warning; the untouched
	// page is still updated.
	rewrite := h.write(t, h.research,
		page("company-overview", "Company overview", "Agent rewrite."),
		page("products", "Products", "Back to [[company-overview]]. New."),
	)
	if len(rewrite.Pages) != 1 || rewrite.Pages[0].Key != "products" || len(rewrite.Warnings) != 1 ||
		!strings.Contains(rewrite.Warnings[0], "edited by a person") {
		t.Fatalf("rewrite = %+v", rewrite)
	}
	got, err := h.service.GetPage(t.Context(), h.owner, hub.PageID)
	if err != nil || got.Title != "Our company" || strings.Contains(got.Body, "Agent rewrite") {
		t.Fatalf("person's edit was overwritten: %+v, %v", got, err)
	}

	// Creating the key an edited page waited for would relink it: skipped too.
	relink := h.write(t, h.research, page("faq", "FAQ", "Answers."))
	if len(relink.Pages) != 1 || relink.Pages[0].Key != "faq" || len(relink.Warnings) != 1 ||
		!strings.Contains(relink.Warnings[0], "later") {
		t.Fatalf("relink = %+v", relink)
	}
	if got, err = h.service.GetPage(
		t.Context(),
		h.owner,
		later.PageID,
	); err != nil || got.Title != "Later, edited" ||
		strings.Contains(got.Body, "FAQ") {
		t.Fatalf("relink overwrote a person's edit: %+v, %v", got, err)
	}
}
