//go:build integration

package wiki_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

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
