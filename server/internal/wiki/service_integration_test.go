//go:build integration

package wiki_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"boardly.dev/boardly/server/internal/wiki"
)

// wikiHarness is one migrated database with a Wiki service, the bus it
// publishes on, and every event that reached a Wiki topic subscriber.
type wikiHarness struct {
	bus     *events.Bus
	pool    *pgxpool.Pool
	fixture dbtest.Fixture
	service *publishedWikiFixture
	owner   auth.UserPrincipal
	foreign auth.UserPrincipal
	events  *[]events.Event
}

func startWiki(t *testing.T) wikiHarness {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	received := []events.Event{}
	if err := bus.Subscribe(wiki.TopicWiki, func(_ context.Context, event events.Event) error {
		received = append(received, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service, err := wiki.NewWikiService(store, bus)
	if err != nil {
		t.Fatal(err)
	}
	return wikiHarness{
		pool:    pool,
		fixture: fixture,
		service: &publishedWikiFixture{service},
		events:  &received,
		bus:     bus,
		owner: auth.UserPrincipal{
			UserID:    fixture.UserA,
			CompanyID: fixture.CompanyA,
			Role:      auth.CompanyRoleOwner,
		},
		foreign: auth.UserPrincipal{
			UserID:    fixture.UserB,
			CompanyID: fixture.CompanyB,
			Role:      auth.CompanyRoleOwner,
		},
	}
}

// eventNames lists the names delivered to Wiki subscribers since the last call.
func (h wikiHarness) eventNames() []string {
	names := make([]string, len(*h.events))
	for i, event := range *h.events {
		names[i] = event.Name
	}
	*h.events = (*h.events)[:0]
	return names
}

func TestDeleteCollectionPreservesPagesHistoryAndLinks(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	collection, err := h.service.CreateCollection(ctx, h.owner, "Temporary")
	if err != nil {
		t.Fatal(err)
	}
	target, err := h.service.CreatePage(
		ctx,
		h.owner,
		wiki.CreatePageInput{Title: "Target", CollectionID: &collection.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	page, err := h.service.CreatePage(
		ctx,
		h.owner,
		wiki.CreatePageInput{
			Title:        "Source",
			Body:         fmt.Sprintf("Read [[%s|Target]].", target.ID),
			CollectionID: &collection.ID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := h.service.ArchivePage(ctx, h.owner, target.ID, target.Revision)
	if err != nil {
		t.Fatal(err)
	}
	// Neighbours the delete must leave alone: a sibling collection with a page,
	// an Unfiled page, and another Company's collection with a page.
	keeper, err := h.service.CreateCollection(ctx, h.owner, "Keeper")
	if err != nil {
		t.Fatal(err)
	}
	kept, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Kept", CollectionID: &keeper.ID})
	if err != nil {
		t.Fatal(err)
	}
	unfiled, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Unfiled"})
	if err != nil {
		t.Fatal(err)
	}
	foreignCollection, err := h.service.CreateCollection(ctx, h.foreign, collection.Name)
	if err != nil {
		t.Fatal(err)
	}
	foreignPage, err := h.service.CreatePage(
		ctx,
		h.foreign,
		wiki.CreatePageInput{Title: "Foreign", CollectionID: &foreignCollection.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	h.eventNames()
	for _, tc := range []struct {
		name      string
		principal auth.UserPrincipal
		id        uuid.UUID
		revision  int64
		want      error
	}{
		{"foreign company", h.foreign, collection.ID, collection.Revision, wiki.ErrCollectionNotFound},
		{"missing", h.owner, uuid.New(), collection.Revision, wiki.ErrCollectionNotFound},
		{"stale", h.owner, collection.ID, collection.Revision + 1, wiki.ErrCollectionConflict},
		{"invalid revision", h.owner, collection.ID, 0, wiki.ErrPageInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := h.service.DeleteCollection(
				ctx,
				tc.principal,
				tc.id,
				tc.revision,
			); !errors.Is(
				err,
				tc.want,
			) {
				t.Fatalf("delete = %v, want %v", err, tc.want)
			}
		})
	}
	if names := h.eventNames(); len(names) != 0 {
		t.Fatalf("refused deletes published %v", names)
	}
	if err := h.service.DeleteCollection(ctx, h.owner, collection.ID, collection.Revision); err != nil {
		t.Fatal(err)
	}
	if names := h.eventNames(); !slices.Equal(names, []string{wiki.EventCollectionDeleted}) {
		t.Fatalf("events = %v", names)
	}
	collections, err := h.service.ListCollections(ctx, h.owner)
	if err != nil || len(collections) != 1 || collections[0].ID != keeper.ID {
		t.Fatalf("collections = %v, %v; want only %s", collections, err, keeper.ID)
	}
	for _, want := range []struct {
		principal  auth.UserPrincipal
		page       wiki.PageResponse
		collection *uuid.UUID
	}{
		{h.owner, kept, &keeper.ID},
		{h.owner, unfiled, nil},
		{h.foreign, foreignPage, &foreignCollection.ID},
	} {
		after, err := h.service.GetPage(ctx, want.principal, want.page.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !equalCollectionID(after.CollectionID, want.collection) || after.Revision != want.page.Revision {
			t.Fatalf("neighbour page %q changed: before=%+v after=%+v", want.page.Title, want.page, after)
		}
	}
	foreignCollections, err := h.service.ListCollections(ctx, h.foreign)
	if err != nil || len(foreignCollections) != 1 || foreignCollections[0].ID != foreignCollection.ID ||
		foreignCollections[0].Revision != foreignCollection.Revision {
		t.Fatalf(
			"foreign collections = %+v, %v; want %s at revision %d",
			foreignCollections,
			err,
			foreignCollection.ID,
			foreignCollection.Revision,
		)
	}
	for _, before := range []wiki.PageResponse{page, archived} {
		after, err := h.service.GetPage(ctx, h.owner, before.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.CollectionID != nil || after.Revision != before.Revision+1 || *after.CurrentRevisionID != *before.CurrentRevisionID || after.Body != before.Body || after.Status != before.Status {
			t.Fatalf("page changed unexpectedly: before=%+v after=%+v", before, after)
		}
		history, err := h.service.ListRevisions(ctx, h.owner, before.ID, 0, 50)
		if err != nil || len(history.Items) != 1 || history.Items[0].Body != before.Body {
			t.Fatalf("history = %+v, %v", history, err)
		}
	}
	if _, err := h.service.UpdatePage(
		ctx,
		h.owner,
		page.ID,
		updatePageInput{Title: page.Title, Body: page.Body, ExpectedRevision: page.Revision},
	); !errors.Is(
		err,
		wiki.ErrPageConflict,
	) {
		t.Fatalf("stale page save = %v", err)
	}
	var linkCount int
	if err := h.pool.QueryRow(ctx, "SELECT count(*) FROM wiki_links WHERE company_id=$1 AND from_page_id=$2 AND to_page_id=$3", h.owner.CompanyID, page.ID, target.ID).Scan(&linkCount); err != nil || linkCount != 1 {
		t.Fatalf("links = %d, %v", linkCount, err)
	}
	if _, err := h.service.CreateCollection(ctx, h.owner, collection.Name); err != nil {
		t.Fatalf("reuse deleted name: %v", err)
	}
}

func equalCollectionID(a, b *uuid.UUID) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func TestDeleteCollectionRechecksMembershipAndAllowsEmptyCollection(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	collection, err := h.service.CreateCollection(ctx, h.owner, "Empty")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		role, status string
		want         error
	}{
		{"member", "active", wiki.ErrPageForbidden},
		{"admin", "revoked", wiki.ErrPageNotFound},
	} {
		if _, err := h.pool.Exec(
			ctx,
			"UPDATE company_members SET role=$3, status=$4 WHERE company_id=$1 AND user_id=$2",
			h.owner.CompanyID,
			h.owner.UserID,
			tc.role,
			tc.status,
		); err != nil {
			t.Fatal(err)
		}
		if err := h.service.DeleteCollection(
			ctx,
			h.owner,
			collection.ID,
			collection.Revision,
		); !errors.Is(
			err,
			tc.want,
		) {
			t.Fatalf("delete with %s/%s = %v, want %v", tc.role, tc.status, err, tc.want)
		}
	}
	if _, err := h.pool.Exec(
		ctx,
		"UPDATE company_members SET role='admin', status='active' WHERE company_id=$1 AND user_id=$2",
		h.owner.CompanyID,
		h.owner.UserID,
	); err != nil {
		t.Fatal(err)
	}
	if err := h.service.DeleteCollection(ctx, h.owner, collection.ID, collection.Revision); err != nil {
		t.Fatalf("admin delete empty collection: %v", err)
	}
	if collections, err := h.service.ListCollections(ctx, h.owner); err != nil || len(collections) != 0 {
		t.Fatalf("collections after delete = %v, %v", collections, err)
	}
}

func TestPagesKeepCompanyScopeHistoryAndOptimisticWrites(t *testing.T) {
	h := startWiki(t)
	ctx, pool, service, owner, foreign := t.Context(), h.pool, h.service, h.owner, h.foreign

	collection, err := service.CreateCollection(ctx, owner, "Handbook")
	if err != nil {
		t.Fatal(err)
	}
	if names := h.eventNames(); !slices.Equal(names, []string{wiki.EventCollectionCreated}) {
		t.Fatalf("create collection published %v, want [%s]", names, wiki.EventCollectionCreated)
	}
	collection, err = service.RenameCollection(ctx, owner, collection.ID, collection.Revision, "Handbook")
	if err != nil {
		t.Fatal(err)
	}
	if names := h.eventNames(); !slices.Equal(names, []string{wiki.EventCollectionUpdated}) {
		t.Fatalf("rename collection published %v, want [%s]", names, wiki.EventCollectionUpdated)
	}
	page, err := service.CreatePage(ctx, owner, wiki.CreatePageInput{
		Title: "Welcome", Body: "A shared starting point.", CollectionID: &collection.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Revision != 1 || page.ContentRevision != 1 || page.Body != "A shared starting point." || page.CollectionID == nil || *page.CollectionID != collection.ID {
		t.Fatalf("created = %+v", page)
	}
	if names := h.eventNames(); !slices.Contains(names, wiki.EventPageCreated) {
		t.Fatalf("create published %v, want %s delivered to a subscriber", names, wiki.EventPageCreated)
	}
	if _, err := service.GetPage(ctx, foreign, page.ID); !errors.Is(err, wiki.ErrPageNotFound) {
		t.Fatalf("foreign page = %v", err)
	}

	page, err = service.UpdatePage(ctx, owner, page.ID, updatePageInput{
		ExpectedRevision: 1, Title: "Welcome", Body: "Updated handbook.", CollectionID: &collection.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Revision != 2 || page.ContentRevision != 2 || page.Body != "Updated handbook." {
		t.Fatalf("updated = %+v", page)
	}
	if names := h.eventNames(); !slices.Equal(names, []string{wiki.EventPageUpdated}) {
		t.Fatalf("update published %v, want [%s]", names, wiki.EventPageUpdated)
	}
	if _, err := service.UpdatePage(
		ctx,
		owner,
		page.ID,
		updatePageInput{ExpectedRevision: 1, Title: "Stale", Body: "Old"},
	); !errors.Is(
		err,
		wiki.ErrPageConflict,
	) {
		t.Fatalf("stale save = %v", err)
	}
	versions, err := service.ListRevisions(ctx, owner, page.ID, 0, 50)
	if err != nil || len(versions.Items) != 2 || versions.Items[0].Body != "Updated handbook." || versions.Items[1].Body != "A shared starting point." {
		t.Fatalf("versions = %+v, %v", versions, err)
	}
	linked, err := service.CreatePage(ctx, owner, wiki.CreatePageInput{Title: "How we work", Body: "Team process."})
	if err != nil {
		t.Fatal(err)
	}
	page, err = service.UpdatePage(ctx, owner, page.ID, updatePageInput{
		ExpectedRevision: page.Revision, Title: page.Title,
		Body: fmt.Sprintf("Read [[%s|How we work]] next.", linked.ID), CollectionID: &collection.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	outgoing, err := service.GetPageLinks(ctx, owner, page.ID)
	if err != nil || len(outgoing.Outgoing) != 1 || outgoing.Outgoing[0].ID != linked.ID {
		t.Fatalf("outgoing links = %+v, %v", outgoing, err)
	}
	incoming, err := service.GetPageLinks(ctx, owner, linked.ID)
	if err != nil || len(incoming.Incoming) != 1 || incoming.Incoming[0].ID != page.ID {
		t.Fatalf("incoming links = %+v, %v", incoming, err)
	}
	graph, err := service.GetGraph(ctx, owner)
	if err != nil || len(graph.Pages) != 2 || len(graph.Links) != 1 {
		t.Fatalf("graph = %+v, %v", graph, err)
	}

	if _, err := pool.Exec(
		ctx,
		`UPDATE company_members SET role='member' WHERE company_id=$1 AND user_id=$2`,
		owner.CompanyID,
		owner.UserID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdatePage(
		ctx,
		owner,
		page.ID,
		updatePageInput{ExpectedRevision: page.Revision, Title: "Denied", Body: "Denied"},
	); !errors.Is(
		err,
		wiki.ErrPageForbidden,
	) {
		t.Fatalf("revoked publisher = %v", err)
	}
	current, err := service.GetPage(ctx, owner, page.ID)
	if err != nil || current.Body != page.Body {
		t.Fatalf("member read = %+v, %v", current, err)
	}
}

// WK-FR5: credentials never become Company knowledge, while contact details
// and headings that merely name a secret stay verbatim in the revision.
func TestPagesKeepProseAndRedactCredentials(t *testing.T) {
	h := startWiki(t)
	page, err := h.service.CreatePage(t.Context(), h.owner, wiki.CreatePageInput{
		Title:   "Access for hr@acme.com",
		Summary: "Password: rotate every 90 days.",
		Body:    "Contact hr@acme.com.\n\nPassword: rotate every 90 days.\n\nOld key AKIAIOSFODNN7EXAMPLE.",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "Contact hr@acme.com.\n\nPassword: rotate every 90 days.\n\nOld key [redacted:aws-key]."
	if page.Title != "Access for hr@acme.com" || page.Summary != "Password: rotate every 90 days." || page.Body != want {
		t.Fatalf("stored title=%q summary=%q body=%q", page.Title, page.Summary, page.Body)
	}
	collection, err := h.service.CreateCollection(t.Context(), h.owner, "Contact ops@acme.com")
	if err != nil || collection.Name != "Contact ops@acme.com" {
		t.Fatalf("collection = %+v, %v", collection, err)
	}
}

// Search reads the page's indexed current content: word prefixes of the title
// and body match, emails and hosts match as the parser indexes them (whole,
// or as a prefix while typed), and a word removed by a later revision stops
// matching.
func TestSearchMatchesCurrentTitleAndBodyWordPrefixes(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	page, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{
		Title: "Quarterly planning", Body: "Onboarding checklist for new hires.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.CreatePage(
		ctx,
		h.owner,
		wiki.CreatePageInput{Title: "Expenses", Body: "Receipts."},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{
		Title: "Support", Body: "Mail alice@example.com or open docs.example.com/help.",
	}); err != nil {
		t.Fatal(err)
	}
	search := func(query string) []string {
		t.Helper()
		list, err := h.service.ListPages(ctx, h.owner, wiki.PageListInput{Search: query, Limit: 50})
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		if int(list.Total) != len(list.Items) {
			t.Fatalf("search %q total = %d, items = %d", query, list.Total, len(list.Items))
		}
		titles := make([]string, len(list.Items))
		for i, item := range list.Items {
			titles[i] = item.Title
		}
		return titles
	}
	for query, want := range map[string][]string{
		"quarter":             {"Quarterly planning"},
		"ONBOARD":             {"Quarterly planning"},
		"checklist quarterly": {"Quarterly planning"},
		"hires receipts":      {},
		"receipt":             {"Expenses"},
		"'&|!:*()":            {"Expenses", "Quarterly planning", "Support"},
		"alice@example.com":   {"Support"},
		"Alice@Exa":           {"Support"},
		"docs.example.com":    {"Support"},
		"alice@example.org":   {},
	} {
		got := search(query)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("search %q = %v, want %v", query, got, want)
		}
	}
	if _, err := h.service.UpdatePage(ctx, h.owner, page.ID, updatePageInput{
		ExpectedRevision: page.Revision, Title: page.Title, Body: "Offboarding only.",
	}); err != nil {
		t.Fatal(err)
	}
	if got := search("onboarding"); len(got) != 0 {
		t.Fatalf("search after edit = %v, want none", got)
	}
	if got := search("offboard"); !slices.Equal(got, []string{"Quarterly planning"}) {
		t.Fatalf("search new body = %v", got)
	}
}

// A collection name is unique per Company (case-insensitively); a rename
// names a collection that exists in the caller's Company and states the
// revision it read.
func TestCollectionNamesAreUniqueAndRenamesAreOptimistic(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	handbook, err := h.service.CreateCollection(ctx, h.owner, "Handbook")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.CreateCollection(ctx, h.owner, "handbook"); !errors.Is(err, wiki.ErrCollectionConflict) {
		t.Fatalf("duplicate create = %v", err)
	}
	if _, err := h.service.CreateCollection(ctx, h.foreign, "Handbook"); err != nil {
		t.Fatalf("same name in another Company = %v", err)
	}
	policies, err := h.service.CreateCollection(ctx, h.owner, "Policies")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.RenameCollection(
		ctx,
		h.owner,
		policies.ID,
		policies.Revision,
		"HANDBOOK",
	); !errors.Is(
		err,
		wiki.ErrCollectionConflict,
	) {
		t.Fatalf("rename onto a used name = %v", err)
	}
	if _, err := h.service.RenameCollection(
		ctx,
		h.owner,
		uuid.New(),
		1,
		"Missing",
	); !errors.Is(
		err,
		wiki.ErrCollectionNotFound,
	) {
		t.Fatalf("rename missing collection = %v", err)
	}
	if _, err := h.service.RenameCollection(
		ctx,
		h.foreign,
		handbook.ID,
		handbook.Revision,
		"Stolen",
	); !errors.Is(
		err,
		wiki.ErrCollectionNotFound,
	) {
		t.Fatalf("rename another Company's collection = %v", err)
	}
	renamed, err := h.service.RenameCollection(ctx, h.owner, handbook.ID, handbook.Revision, "Team handbook")
	if err != nil || renamed.Revision != handbook.Revision+1 || renamed.Name != "Team handbook" {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}
	if _, err := h.service.RenameCollection(
		ctx,
		h.owner,
		handbook.ID,
		handbook.Revision,
		"Stale",
	); !errors.Is(
		err,
		wiki.ErrCollectionConflict,
	) {
		t.Fatalf("stale rename = %v", err)
	}
}

// Limits count characters, as the OpenAPI maxLength does: multi-byte text is
// accepted up to the documented length and refused one character past it.
func TestLengthLimitsCountCharacters(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	page, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{
		Title: strings.Repeat("ệ", 240), Summary: strings.Repeat("ệ", 2048), Body: strings.Repeat("界", 262144),
	})
	if err != nil {
		t.Fatalf("page at every limit = %v", err)
	}
	if utf8.RuneCountInString(page.Body) != 262144 {
		t.Fatalf("stored body runes = %d", utf8.RuneCountInString(page.Body))
	}
	if _, err := h.service.CreatePage(
		ctx,
		h.owner,
		wiki.CreatePageInput{Title: "Summary", Summary: strings.Repeat("ệ", 2049)},
	); !errors.Is(
		err,
		wiki.ErrPageInvalidInput,
	) {
		t.Fatalf("summary past limit = %v", err)
	}
	if _, err := h.service.CreatePage(
		ctx,
		h.owner,
		wiki.CreatePageInput{Title: "Body", Body: strings.Repeat("界", 262145)},
	); !errors.Is(
		err,
		wiki.ErrPageBodyTooLarge,
	) {
		t.Fatalf("body past limit = %v", err)
	}
	if _, err := h.service.ListPages(
		ctx,
		h.owner,
		wiki.PageListInput{Search: strings.Repeat("ệ", 200), Limit: 10},
	); err != nil {
		t.Fatalf("search at limit = %v", err)
	}
	if _, err := h.service.ListPages(
		ctx,
		h.owner,
		wiki.PageListInput{Search: strings.Repeat("ệ", 201), Limit: 10},
	); !errors.Is(
		err,
		wiki.ErrPageInvalidInput,
	) {
		t.Fatalf("search past limit = %v", err)
	}
}

// Backlinks and outgoing links are capped independently: a heavily linked
// page still lists its own outgoing links.
func TestPageLinksCapEachDirectionIndependently(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	outgoingTarget, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Glossary"})
	if err != nil {
		t.Fatal(err)
	}
	hub, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{
		Title: "Hub", Body: fmt.Sprintf("See [[%s|Glossary]].", outgoingTarget.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 201 {
		if _, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{
			Title: fmt.Sprintf("Linker %03d", i), Body: fmt.Sprintf("Back to [[%s]].", hub.ID),
		}); err != nil {
			t.Fatal(err)
		}
	}
	links, err := h.service.GetPageLinks(ctx, h.owner, hub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links.Incoming) != 200 || !links.IncomingTruncated || links.Incoming[0].Title != "Linker 000" {
		t.Fatalf("incoming = %d truncated=%v", len(links.Incoming), links.IncomingTruncated)
	}
	if len(links.Outgoing) != 1 || links.Outgoing[0].ID != outgoingTarget.ID || links.OutgoingTruncated {
		t.Fatalf("outgoing = %+v truncated=%v", links.Outgoing, links.OutgoingTruncated)
	}
}

// Favorites belong to the calling member; an archived page cannot become a
// favorite, and saying so is a 409 rather than a silent no-op.
func TestFavoritesArePerMemberAndRefuseArchivedPages(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	page, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Benefits"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.service.SetFavorite(ctx, h.owner, page.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := h.service.SetFavorite(ctx, h.owner, page.ID, true); err != nil {
		t.Fatalf("repeat favorite = %v", err)
	}
	favorites, err := h.service.ListPages(ctx, h.owner, wiki.PageListInput{FavoritesOnly: true, Limit: 10})
	if err != nil || len(favorites.Items) != 1 || !favorites.Items[0].Favorite {
		t.Fatalf("favorites = %+v, %v", favorites, err)
	}
	if err := h.service.SetFavorite(ctx, h.foreign, page.ID, true); !errors.Is(err, wiki.ErrPageNotFound) {
		t.Fatalf("foreign favorite = %v", err)
	}
	archived, err := h.service.ArchivePage(ctx, h.owner, page.ID, page.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.service.SetFavorite(ctx, h.owner, page.ID, false); err != nil {
		t.Fatalf("unfavorite archived = %v", err)
	}
	if err := h.service.SetFavorite(ctx, h.owner, page.ID, true); !errors.Is(err, wiki.ErrPageArchived) {
		t.Fatalf("favorite archived = %v", err)
	}
	current, err := h.service.GetPage(ctx, h.owner, archived.ID)
	if err != nil || current.Favorite {
		t.Fatalf("archived page favorite = %+v, %v", current, err)
	}
}

// Archive hides a page from the graph and from other pages' links without
// deleting it; an archived page cannot be edited, and each transition states
// the revision it read.
func TestArchiveAndRestoreHideAndReturnAPage(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	target, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Old policy", Body: "Retired."})
	if err != nil {
		t.Fatal(err)
	}
	source, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{
		Title: "Index", Body: fmt.Sprintf("See [[%s|Old policy]].", target.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.eventNames()

	if _, err := h.service.ArchivePage(
		ctx,
		h.owner,
		target.ID,
		target.Revision+1,
	); !errors.Is(
		err,
		wiki.ErrPageConflict,
	) {
		t.Fatalf("stale archive = %v", err)
	}
	if _, err := h.service.RestorePage(
		ctx,
		h.owner,
		target.ID,
		target.Revision,
	); !errors.Is(
		err,
		wiki.ErrPageConflict,
	) {
		t.Fatalf("restore an active page = %v", err)
	}
	archived, err := h.service.ArchivePage(ctx, h.owner, target.ID, target.Revision)
	if err != nil || archived.Status != "archived" || archived.ArchivedAt == nil || archived.Revision != target.Revision+1 ||
		archived.ContentRevision != target.ContentRevision || archived.Body != "Retired." {
		t.Fatalf("archived = %+v, %v", archived, err)
	}
	if names := h.eventNames(); !slices.Equal(names, []string{wiki.EventPageArchived}) {
		t.Fatalf("archive published %v", names)
	}
	if _, err := h.service.ArchivePage(
		ctx,
		h.owner,
		target.ID,
		archived.Revision,
	); !errors.Is(
		err,
		wiki.ErrPageConflict,
	) {
		t.Fatalf("archive an archived page = %v", err)
	}
	if _, err := h.service.UpdatePage(ctx, h.owner, target.ID, updatePageInput{
		ExpectedRevision: archived.Revision, Title: "Edited", Body: "Edited.",
	}); !errors.Is(err, wiki.ErrPageArchived) {
		t.Fatalf("edit archived = %v", err)
	}
	graph, err := h.service.GetGraph(ctx, h.owner)
	if err != nil || len(graph.Pages) != 1 || graph.Pages[0].ID != source.ID || len(graph.Links) != 0 {
		t.Fatalf("graph with archived page = %+v, %v", graph, err)
	}
	links, err := h.service.GetPageLinks(ctx, h.owner, source.ID)
	if err != nil || len(links.Outgoing) != 0 {
		t.Fatalf("links to archived page = %+v, %v", links, err)
	}
	active, err := h.service.ListPages(ctx, h.owner, wiki.PageListInput{Limit: 10})
	if err != nil || len(active.Items) != 1 || active.Items[0].ID != source.ID {
		t.Fatalf("active list = %+v, %v", active, err)
	}
	archivedList, err := h.service.ListPages(ctx, h.owner, wiki.PageListInput{Status: "archived", Limit: 10})
	if err != nil || len(archivedList.Items) != 1 || archivedList.Items[0].ID != target.ID {
		t.Fatalf("archived list = %+v, %v", archivedList, err)
	}

	restored, err := h.service.RestorePage(ctx, h.owner, target.ID, archived.Revision)
	if err != nil || restored.Status != "active" || restored.ArchivedAt != nil || restored.Revision != archived.Revision+1 {
		t.Fatalf("restored = %+v, %v", restored, err)
	}
	if names := h.eventNames(); !slices.Equal(names, []string{wiki.EventPageRestored}) {
		t.Fatalf("restore published %v", names)
	}
	links, err = h.service.GetPageLinks(ctx, h.owner, source.ID)
	if err != nil || len(links.Outgoing) != 1 || links.Outgoing[0].ID != target.ID {
		t.Fatalf("links after restore = %+v, %v", links, err)
	}
}

// Restoring history copies an old revision into a new one, so the intervening
// history stays; it names a revision of this page and the revision it read.
func TestRestoreRevisionPublishesANewRevision(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	page, err := h.service.CreatePage(
		ctx,
		h.owner,
		wiki.CreatePageInput{Title: "Leave", Body: "Ten days.", Summary: "v1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	page, err = h.service.UpdatePage(ctx, h.owner, page.ID, updatePageInput{
		ExpectedRevision: page.Revision, Title: "Leave policy", Body: "Fifteen days.", Summary: "v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	history, err := h.service.ListRevisions(ctx, h.owner, page.ID, 0, 50)
	if err != nil || len(history.Items) != 2 {
		t.Fatalf("history = %+v, %v", history, err)
	}
	first := history.Items[1]
	other, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	foreignPage, err := h.service.CreatePage(ctx, h.foreign, wiki.CreatePageInput{Title: "Foreign"})
	if err != nil {
		t.Fatal(err)
	}
	h.eventNames()

	if _, err := h.service.RestoreRevision(
		ctx,
		h.owner,
		page.ID,
		first.ID,
		page.Revision-1,
	); !errors.Is(
		err,
		wiki.ErrPageConflict,
	) {
		t.Fatalf("stale restore = %v", err)
	}
	for name, revisionID := range map[string]uuid.UUID{
		"another page's revision":    *other.CurrentRevisionID,
		"another Company's revision": *foreignPage.CurrentRevisionID,
		"unknown revision":           uuid.New(),
	} {
		if _, err := h.service.RestoreRevision(
			ctx,
			h.owner,
			page.ID,
			revisionID,
			page.Revision,
		); !errors.Is(
			err,
			wiki.ErrPageNotFound,
		) {
			t.Fatalf("restore %s = %v", name, err)
		}
	}
	if _, err := h.service.RestoreRevision(
		ctx,
		h.foreign,
		page.ID,
		first.ID,
		page.Revision,
	); !errors.Is(
		err,
		wiki.ErrPageNotFound,
	) {
		t.Fatalf("foreign restore = %v", err)
	}

	restored, err := h.service.RestoreRevision(ctx, h.owner, page.ID, first.ID, page.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ContentRevision != 3 || restored.Revision != page.Revision+1 || restored.Title != "Leave" ||
		restored.Body != "Ten days." || restored.Summary != "v1" || *restored.CurrentRevisionID == first.ID {
		t.Fatalf("restored = %+v", restored)
	}
	if names := h.eventNames(); !slices.Equal(names, []string{wiki.EventPageUpdated}) {
		t.Fatalf("restore revision published %v", names)
	}
	history, err = h.service.ListRevisions(ctx, h.owner, page.ID, 0, 50)
	if err != nil || history.Total != 3 || history.Items[0].Number != 3 || history.Items[1].Body != "Fifteen days." {
		t.Fatalf("history after restore = %+v, %v", history, err)
	}
}

// A collection id or link target from another Company never crosses the
// fence: the collection is not found, and the link is simply not recorded.
func TestCollectionsAndLinksStayInsideTheCompany(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	foreignCollection, err := h.service.CreateCollection(ctx, h.foreign, "Foreign")
	if err != nil {
		t.Fatal(err)
	}
	foreignPage, err := h.service.CreatePage(ctx, h.foreign, wiki.CreatePageInput{Title: "Foreign"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{
		Title: "Misfiled", CollectionID: &foreignCollection.ID,
	}); !errors.Is(err, wiki.ErrCollectionNotFound) {
		t.Fatalf("create in foreign collection = %v", err)
	}
	page, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{
		Title: "Local", Body: fmt.Sprintf("Peek at [[%s|Foreign]].", foreignPage.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	links, err := h.service.GetPageLinks(ctx, h.owner, page.ID)
	if err != nil || len(links.Outgoing) != 0 {
		t.Fatalf("link to foreign page = %+v, %v", links, err)
	}
	backlinks, err := h.service.GetPageLinks(ctx, h.foreign, foreignPage.ID)
	if err != nil || len(backlinks.Incoming) != 0 {
		t.Fatalf("foreign backlinks = %+v, %v", backlinks, err)
	}
	if _, err := h.service.UpdatePage(ctx, h.owner, page.ID, updatePageInput{
		ExpectedRevision: page.Revision, Title: page.Title, Body: page.Body, CollectionID: &foreignCollection.ID,
	}); !errors.Is(err, wiki.ErrCollectionNotFound) {
		t.Fatalf("move into foreign collection = %v", err)
	}
	current, err := h.service.GetPage(ctx, h.owner, page.ID)
	if err != nil || current.Revision != page.Revision || current.CollectionID != nil {
		t.Fatalf("page after refused move = %+v, %v", current, err)
	}
}

// Reads check current membership: a member whose membership is revoked stops
// seeing pages, history and lists at once.
func TestRevokedMemberCannotRead(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	page, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Roadmap", Body: "Private plans."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		h.owner.CompanyID, h.owner.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.GetPage(ctx, h.owner, page.ID); !errors.Is(err, wiki.ErrPageNotFound) {
		t.Fatalf("revoked read = %v", err)
	}
	if _, err := h.service.ListRevisions(ctx, h.owner, page.ID, 0, 50); !errors.Is(err, wiki.ErrPageNotFound) {
		t.Fatalf("revoked history = %v", err)
	}
	if _, err := h.service.GetPageLinks(ctx, h.owner, page.ID); !errors.Is(err, wiki.ErrPageNotFound) {
		t.Fatalf("revoked links = %v", err)
	}
	list, err := h.service.ListPages(ctx, h.owner, wiki.PageListInput{Limit: 10})
	if err != nil || len(list.Items) != 0 || list.Total != 0 {
		t.Fatalf("revoked list = %+v, %v", list, err)
	}
	graph, err := h.service.GetGraph(ctx, h.owner)
	if err != nil || len(graph.Pages) != 0 {
		t.Fatalf("revoked graph = %+v, %v", graph, err)
	}
	collections, err := h.service.ListCollections(ctx, h.owner)
	if err != nil || len(collections) != 0 {
		t.Fatalf("revoked collections = %+v, %v", collections, err)
	}
	if err := h.service.SetFavorite(ctx, h.owner, page.ID, true); !errors.Is(err, wiki.ErrPageNotFound) {
		t.Fatalf("revoked favorite = %v", err)
	}
	if _, err := h.service.CreatePage(
		ctx,
		h.owner,
		wiki.CreatePageInput{Title: "Denied"},
	); !errors.Is(
		err,
		wiki.ErrPageNotFound,
	) {
		t.Fatalf("revoked write = %v", err)
	}
}

// A search lists the best match first: a title hit, then a summary hit, then a
// body-only hit however often the body repeats the word and however recently
// it changed. Without a search the list stays newest first. An unpublished
// draft, which has no stored vector, is ranked on the same weights from its
// own text.
func TestSearchRanksTitleThenSummaryThenBodyAndBrowsingStaysNewestFirst(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	create := func(title, summary, body string) wiki.PageResponse {
		t.Helper()
		page, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: title, Summary: summary, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	create("Onboarding", "How new hires start", "Welcome aboard.")
	create("Handbook", "How onboarding works", "Nothing about it here.")
	draft, err := h.service.DefaultWikiService.CreatePage(ctx, h.owner, wiki.CreatePageInput{
		Title: "Onboarding plan", Body: "Draft notes.",
	})
	if err != nil {
		t.Fatal(err)
	}
	create("Policies", "Company rules", strings.Repeat("Onboarding onboarding onboarding. ", 6))

	titles := func(search string) []string {
		t.Helper()
		list, err := h.service.ListPages(ctx, h.owner, wiki.PageListInput{Search: search, Limit: 50})
		if err != nil {
			t.Fatalf("search %q: %v", search, err)
		}
		titles := make([]string, len(list.Items))
		for i, item := range list.Items {
			titles[i] = item.Title
		}
		if int(list.Total) != len(titles) {
			t.Fatalf("search %q total = %d, items = %d", search, list.Total, len(titles))
		}
		return titles
	}
	// The two title hits tie on rank and fall back to the newer one (the draft).
	if got, want := titles("onboarding"), []string{draft.Title, "Onboarding", "Handbook", "Policies"}; !slices.Equal(got, want) {
		t.Fatalf("search = %v, want %v", got, want)
	}
	if got, want := titles(""), []string{"Policies", draft.Title, "Handbook", "Onboarding"}; !slices.Equal(got, want) {
		t.Fatalf("browse = %v, want newest first %v", got, want)
	}
}
