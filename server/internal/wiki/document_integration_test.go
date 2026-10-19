//go:build integration

package wiki_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"boardly.dev/boardly/server/internal/wiki"
	"github.com/reearth/ygo/crdt"
)

func TestWikiDraftPublicationBoundary(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	page, err := h.service.DefaultWikiService.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Working", Body: "Unpublished research"})
	if err != nil {
		t.Fatal(err)
	}
	if page.CurrentRevisionID != nil {
		t.Fatal("new page was published")
	}
	draft, err := h.service.DefaultWikiService.ReadDocument(ctx, h.owner, page.ID, wiki.DocumentReadInput{Since: -1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(draft.Markdown, "Unpublished research") {
		t.Fatal("import lost content")
	}
	published, err := h.service.DefaultWikiService.PublishPage(ctx, h.owner, page.ID, wiki.PublishPageInput{ExpectedRevision: page.Revision, ExpectedSequence: draft.Sequence})
	if err != nil {
		t.Fatal(err)
	}
	var root wiki.BlockNode
	if err := json.Unmarshal(draft.Content, &root); err != nil {
		t.Fatal(err)
	}
	old := root.Content[0]
	replacement := old
	replacement.Content = []wiki.BlockNode{{Type: "text", Text: "Draft changed AKIAIOSFODNN7EXAMPLE"}}
	update, _, err := wiki.ApplyBlockOperations(draft.State, []wiki.BlockOperation{{Type: "update", BlockID: old.Attrs["blockId"].(string), ExpectedHash: wiki.BlockHash(old), Content: &replacement}})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := h.service.DefaultWikiService.SyncDocument(ctx, h.owner, page.ID, wiki.SyncDocumentInput{Update: update, BaseSequence: draft.Sequence})
	if err != nil {
		t.Fatal(err)
	}
	read, err := h.service.DefaultWikiService.GetPage(ctx, h.owner, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.Body != published.Body || strings.Contains(read.Body, "Draft changed") {
		t.Fatal("draft leaked into published read")
	}
	if _, err = h.service.DefaultWikiService.PublishPage(ctx, h.owner, page.ID, wiki.PublishPageInput{ExpectedRevision: published.Revision, ExpectedSequence: draft.Sequence}); !errors.Is(err, wiki.ErrPageConflict) {
		t.Fatalf("stale draft publish = %v", err)
	}
	final, err := h.service.DefaultWikiService.PublishPage(ctx, h.owner, page.ID, wiki.PublishPageInput{ExpectedRevision: published.Revision, ExpectedSequence: saved.Sequence})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(final.Body, "AKIAIOSFODNN7EXAMPLE") || strings.Contains(string(final.Content), "AKIAIOSFODNN7EXAMPLE") {
		t.Fatal("published content bypassed credential redaction")
	}
	var publishedRoot wiki.BlockNode
	if err = json.Unmarshal(final.Content, &publishedRoot); err != nil {
		t.Fatal(err)
	}
	if strings.TrimRight(wiki.BlocksMarkdown(publishedRoot), "\n") != final.Body {
		t.Fatal("published projections diverged")
	}
	restored, err := h.service.DefaultWikiService.RestoreRevision(ctx, h.owner, page.ID, *published.CurrentRevisionID, wiki.RestoreRevisionInput{ExpectedRevision: final.Revision, ExpectedSequence: saved.Sequence})
	if err != nil {
		t.Fatal(err)
	}
	read, err = h.service.DefaultWikiService.GetPage(ctx, h.owner, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.Body != final.Body || !strings.Contains(restored.Markdown, "Unpublished research") {
		t.Fatal("restoring history published without review")
	}
}

func TestWikiDraftConcurrentEditingAndReaderIsolation(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	s := h.service.DefaultWikiService
	dbtest.MustExec(t, ctx, h.pool, `INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`, h.fixture.CompanyA, h.fixture.UserB)
	reader := auth.UserPrincipal{CompanyID: h.fixture.CompanyA, UserID: h.fixture.UserB, Role: auth.CompanyRoleMember}
	page, err := s.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Secret draft title", Body: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.eventNames()) != 0 {
		t.Fatal("draft creation published a Company event")
	}
	if _, err = s.GetPage(ctx, reader, page.ID); !errors.Is(err, wiki.ErrPageNotFound) {
		t.Fatalf("unpublished read=%v", err)
	}
	if err = s.SetFavorite(ctx, reader, page.ID, true); !errors.Is(err, wiki.ErrPageNotFound) {
		t.Fatalf("unpublished favorite=%v", err)
	}
	list, err := s.ListPages(ctx, reader, wiki.PageListInput{Limit: 20})
	if err != nil || list.Total != 0 {
		t.Fatalf("unpublished list=%+v %v", list, err)
	}
	draft, err := s.ReadDocument(ctx, h.owner, page.ID, wiki.DocumentReadInput{Since: -1})
	if err != nil {
		t.Fatal(err)
	}
	updates := make([][]byte, 2)
	for i, suffix := range []string{" Alice", " Bob"} {
		doc := crdt.New()
		if err = crdt.ApplyUpdateV1(doc, draft.State, nil); err != nil {
			t.Fatal(err)
		}
		text := doc.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement).Children()[0].(*crdt.YXmlText)
		doc.Transact(func(tx *crdt.Transaction) { text.Insert(tx, 5, suffix, nil) })
		updates[i] = doc.EncodeStateAsUpdate()
		doc.Destroy()
	}
	var saved wiki.Document
	for _, update := range updates {
		saved, err = s.SyncDocument(ctx, h.owner, page.ID, wiki.SyncDocumentInput{Update: update, BaseSequence: draft.Sequence})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(saved.Markdown, "Alice") || !strings.Contains(saved.Markdown, "Bob") {
		t.Fatalf("merged content=%q", saved.Markdown)
	}
	var root wiki.BlockNode
	if err = json.Unmarshal(draft.Content, &root); err != nil {
		t.Fatal(err)
	}
	ids, err := s.ReserveBlockIDs(ctx, h.owner, page.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	replacement := wiki.BlockNode{Type: "paragraph", Attrs: map[string]any{"blockId": ids[0].String()}, Content: []wiki.BlockNode{{Type: "text", Text: "Stale insertion"}}}
	structural, _, err := wiki.ApplyBlockOperations(draft.State, []wiki.BlockOperation{{Type: "insert", Content: &replacement}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SyncDocument(ctx, h.owner, page.ID, wiki.SyncDocumentInput{Update: structural, BaseSequence: draft.Sequence}); !errors.Is(err, wiki.ErrPageConflict) {
		t.Fatalf("stale structure=%v", err)
	}
	published, err := s.PublishPage(ctx, h.owner, page.ID, wiki.PublishPageInput{ExpectedRevision: page.Revision, ExpectedSequence: saved.Sequence})
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.GetPage(ctx, reader, page.ID)
	if err != nil || read.Body != published.Body {
		t.Fatalf("published reader=%+v %v", read, err)
	}
	if _, err = s.ReadDocument(ctx, reader, page.ID, wiki.DocumentReadInput{Since: -1}); !errors.Is(err, wiki.ErrPageForbidden) {
		t.Fatalf("reader draft access=%v", err)
	}
	if _, err = s.PublishPage(ctx, h.owner, page.ID, wiki.PublishPageInput{ExpectedRevision: page.Revision, ExpectedSequence: saved.Sequence}); !errors.Is(err, wiki.ErrPageConflict) {
		t.Fatalf("stale published revision=%v", err)
	}
}

func TestReadDocumentDoesNotWaitForPageLocksOfUpgradedDrafts(t *testing.T) {
	h := startWiki(t)
	s := h.service.DefaultWikiService
	page, err := s.CreatePage(t.Context(), h.owner, wiki.CreatePageInput{Title: "Busy", Body: "Autosaved"})
	if err != nil {
		t.Fatal(err)
	}
	// The first open upgrades or creates the draft under the page lock.
	first, err := s.ReadDocument(t.Context(), h.owner, page.ID, wiki.DocumentReadInput{Since: -1})
	if err != nil {
		t.Fatal(err)
	}

	// An autosave in flight holds the page and draft rows.
	tx, err := h.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err := tx.Exec(t.Context(), `SELECT id FROM wiki_pages WHERE id=$1 FOR UPDATE`, page.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `SELECT page_id FROM wiki_canvas_drafts WHERE page_id=$1 FOR UPDATE`, page.ID); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	got, err := s.ReadDocument(ctx, h.owner, page.ID, wiki.DocumentReadInput{Since: -1})
	if err != nil {
		t.Fatalf("read behind a page lock = %v, want it to return", err)
	}
	if got.Sequence != first.Sequence || got.SchemaVersion != 1 || !got.CanEdit || got.Markdown != first.Markdown ||
		got.Title != "Busy" || len(got.State) == 0 {
		t.Fatalf("read = %+v, want the same draft as %+v", got, first)
	}
	unchanged, err := s.ReadDocument(ctx, h.owner, page.ID, wiki.DocumentReadInput{Since: first.Sequence})
	if err != nil || unchanged.State != nil || unchanged.Content != nil || unchanged.Markdown != "" || unchanged.Sequence != first.Sequence {
		t.Fatalf("unchanged read = %+v, %v", unchanged, err)
	}
	projection, err := s.ReadDocument(ctx, h.owner, page.ID, wiki.DocumentReadInput{Since: -1, Projection: true})
	if err != nil || projection.State != nil || len(projection.Content) == 0 {
		t.Fatalf("projection read = %+v, %v", projection, err)
	}
}

func TestReadDocumentKeepsAccessErrorsOnTheLockFreePath(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	s := h.service.DefaultWikiService
	page, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Guarded", Body: "Text"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadDocument(ctx, h.owner, page.ID, wiki.DocumentReadInput{Since: -1}); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, h.pool, `INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`, h.fixture.CompanyA, h.fixture.UserB)
	plain := auth.UserPrincipal{CompanyID: h.fixture.CompanyA, UserID: h.fixture.UserB, Role: auth.CompanyRoleMember}

	if _, err := s.ReadDocument(ctx, plain, page.ID, wiki.DocumentReadInput{Since: -1}); !errors.Is(err, wiki.ErrPageForbidden) {
		t.Fatalf("non-publisher = %v, want ErrPageForbidden", err)
	}
	if _, err := s.ReadDocument(ctx, h.owner, uuid.New(), wiki.DocumentReadInput{Since: -1}); !errors.Is(err, wiki.ErrPageNotFound) {
		t.Fatalf("unknown page = %v, want ErrPageNotFound", err)
	}
	if _, err := s.ReadDocument(ctx, h.foreign, page.ID, wiki.DocumentReadInput{Since: -1}); !errors.Is(err, wiki.ErrPageNotFound) {
		t.Fatalf("other Company's member = %v, want ErrPageNotFound", err)
	}

	archived, err := s.ArchivePage(ctx, h.owner, page.ID, page.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadDocument(ctx, h.owner, page.ID, wiki.DocumentReadInput{Since: -1}); !errors.Is(err, wiki.ErrPageArchived) {
		t.Fatalf("archived page = %v, want ErrPageArchived", err)
	}
	if _, err := s.RestorePage(ctx, h.owner, page.ID, archived.Revision); err != nil {
		t.Fatal(err)
	}

	dbtest.MustExec(t, ctx, h.pool, `UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`, h.owner.CompanyID, h.owner.UserID)
	if _, err := s.ReadDocument(ctx, h.owner, page.ID, wiki.DocumentReadInput{Since: -1}); !errors.Is(err, wiki.ErrPageNotFound) {
		t.Fatalf("revoked member = %v, want ErrPageNotFound", err)
	}
}
