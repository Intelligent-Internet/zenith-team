//go:build integration

package canvas

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/reearth/ygo/crdt"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"boardly.dev/boardly/server/internal/wiki"
)

func TestCanvasWikiReferencesReadPublicationOnly(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	s := newCanvasServices(store, bus, nil)
	w, err := wiki.NewWikiService(store, bus)
	if err != nil {
		t.Fatal(err)
	}
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	page, err := w.CreatePage(ctx, p, wiki.CreatePageInput{Title: "Company knowledge", Body: "Published guidance"})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := w.ReadDocument(ctx, p, page.ID, wiki.DocumentReadInput{Since: -1})
	if err != nil {
		t.Fatal(err)
	}
	page, err = w.PublishPage(ctx, p, page.ID, wiki.PublishPageInput{ExpectedRevision: page.Revision, ExpectedSequence: draft.Sequence})
	if err != nil {
		t.Fatal(err)
	}
	linked, err := s.Mutate(ctx, p, f.ProjectA, Command{RequestKey: uuid.New(), Type: "add_artifact", WikiPageID: &page.ID, Title: page.Title})
	if err != nil {
		t.Fatal(err)
	}
	client := crdt.New()
	defer client.Destroy()
	if err = crdt.ApplyUpdateV1(client, draft.State, nil); err != nil {
		t.Fatal(err)
	}
	text := client.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement).Children()[0].(*crdt.YXmlText)
	client.Transact(func(tx *crdt.Transaction) { text.Insert(tx, 0, "Unpublished secret. ", nil) })
	update := client.EncodeStateAsUpdate()
	if _, err = w.SyncDocument(ctx, p, page.ID, wiki.SyncDocumentInput{Update: update, BaseSequence: draft.Sequence}); err != nil {
		t.Fatal(err)
	}
	ref := DocumentHistoryRef{ProjectID: f.ProjectA, VersionID: linked.ID}
	if _, err := s.DocumentHistory(ctx, p, ref, DocumentHistoryInput{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("published Wiki history list = %v, want %v", err, ErrForbidden)
	}
	if _, err := s.DocumentHistoryState(ctx, p, ref, draft.Sequence); !errors.Is(err, ErrForbidden) {
		t.Errorf("published Wiki history state = %v, want %v", err, ErrForbidden)
	}
	if _, err := s.RestoreDocumentHistory(ctx, p, ref, DocumentHistoryRestore{
		Sequence: draft.Sequence, ExpectedSequence: draft.Sequence,
	}); !errors.Is(err, ErrForbidden) {
		t.Errorf("published Wiki history restore = %v, want %v", err, ErrForbidden)
	}
	for _, test := range []struct {
		term string
		want int
	}{{"secret", 0}, {"Published guidance", 1}, {page.Title, 1}} {
		hits, err := s.Search(ctx, p, f.ProjectA, test.term)
		if err != nil || len(hits) != test.want {
			t.Fatalf("search %q = %+v, %v, want %d hits", test.term, hits, err, test.want)
		}
		for _, hit := range hits {
			if strings.Contains(hit.Excerpt, "secret") {
				t.Fatalf("search exposed Wiki draft: %+v", hit)
			}
		}
	}
	document, err := s.Document(ctx, p, f.ProjectA, linked.ID, -1)
	if err != nil || document.CanEdit || len(document.State) != 0 || strings.Contains(document.Markdown, "secret") || !strings.Contains(document.Markdown, "Published guidance") {
		t.Fatalf("Canvas Wiki read = %+v, %v", document, err)
	}
	if _, err = s.SyncDocument(ctx, p, f.ProjectA, linked.ID, update, draft.Sequence); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Canvas Wiki edit = %v, want forbidden", err)
	}
	if _, err = s.ReserveBlockIDs(ctx, p, f.ProjectA, linked.ID, 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Canvas Wiki block allocation = %v, want forbidden", err)
	}
	snapshot, err := s.Snapshot(ctx, p, f.ProjectA, 0)
	if err != nil {
		t.Fatal(err)
	}
	artifact := snapshot.Artifacts[0]
	for _, command := range []string{"checkpoint", "restore_version"} {
		_, err = s.Mutate(ctx, p, f.ProjectA, Command{RequestKey: uuid.New(), Type: command, WikiPageID: &page.ID, ArtifactID: &artifact.ID, VersionID: &linked.ID, ExpectedRevision: artifact.Revision})
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("Canvas Wiki %s = %v, want forbidden", command, err)
		}
	}
	root := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{{Type: "artifactReference", Attrs: map[string]any{"versionId": linked.ID.String(), "artifactId": artifact.ID.String(), "format": "live", "display": "expanded"}}}}
	projectPage, err := s.CreatePage(ctx, p, f.ProjectA, PageCreateInput{RequestKey: uuid.New(), Title: "Project plan", Content: root})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewPresentation(ctx, p, f.ProjectA, projectPage.PageID, -1)
	if err != nil {
		t.Fatal(err)
	}
	read, _ := json.Marshal(preview.Documents)
	if strings.Contains(string(read), "secret") || !strings.Contains(string(read), "Published guidance") {
		t.Fatalf("Project capture exposed Wiki draft: %s", read)
	}
	if _, err = s.CapturePresentation(ctx, p, f.ProjectA, PresentationInput{PageID: projectPage.PageID, RequestKey: uuid.New(), Sequence: preview.Sequence, Fingerprint: preview.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='running',execution_authorized_by_user_id=$2,task_execution_generation=(SELECT execution_generation FROM tasks WHERE id=$3) WHERE id=$1`, f.RunA, f.UserA, f.TaskA)
	if agentPreview, err := s.PreviewPresentationForRun(ctx, p, f.RunA, projectPage.PageID); err != nil || len(agentPreview.Documents) != 0 || agentPreview.Fingerprint != preview.Fingerprint {
		t.Fatalf("Agent preview bypassed Wiki read policy = %+v, %v", agentPreview, err)
	}
	if _, err = s.ReadPageSource(ctx, p, f.RunA, linked.ID, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("source_read bypassed Wiki read policy = %v", err)
	}
	project, err := s.ReadProjectForRun(ctx, p, f.RunA)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range project.Results {
		if result.ID == artifact.ID && result.LatestVersion.Excerpt != "" {
			t.Fatalf("project_read bypassed Wiki read policy: %q", result.LatestVersion.Excerpt)
		}
	}
	var privateRevision uuid.UUID
	if err = pool.QueryRow(ctx, `INSERT INTO wiki_revisions(company_id,page_id,number,title,body,summary,author_user_id) VALUES($1,$2,99,'Private checkpoint','Never published','',$3) RETURNING id`, f.CompanyA, page.ID, f.UserA).Scan(&privateRevision); err != nil {
		t.Fatal(err)
	}
	_, err = s.Mutate(ctx, p, f.ProjectA, Command{RequestKey: uuid.New(), Type: "add_artifact", WikiPageID: &page.ID, WikiRevisionID: &privateRevision, Title: "Private checkpoint"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("unpublished Wiki checkpoint reference = %v", err)
	}
}
