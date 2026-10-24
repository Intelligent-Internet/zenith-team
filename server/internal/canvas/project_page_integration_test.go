//go:build integration

package canvas

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestTaskResultPageAndLiveOverviewCapture(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	s := newCanvasServices(store, bus, nil)
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	overview, err := createOverviewPage(
		s,
		ctx,
		p,
		f.ProjectA,
		OverviewInput{RequestKey: uuid.New(), Action: "create"},
	)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET status='running',execution_authorized_by_user_id=$2,task_execution_generation=(SELECT execution_generation FROM tasks WHERE id=$3) WHERE id=$1`,
		f.RunA,
		f.UserA,
		f.TaskA,
	)
	// A member with Project work access contributes without Company Wiki publication authority.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET assignee_user_id=$2 WHERE id=$1`, f.TaskA, f.UserB)
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET execution_authorized_by_user_id=$2 WHERE id=$1`, f.RunA, f.UserB)
	dbtest.GrantProject(t, ctx, pool, f.CompanyA, f.ProjectA, f.UserB)
	member := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleMember}
	in := PageCreateInput{
		RequestKey: uuid.New(),
		Title:      "Launch campaign",
		Content: wiki.BlocksFromMarkdown(
			"## Research\n\nA sourced audience finding.\n\n## Publishing\n\nDraft ready for review.",
		),
	}
	in.Content.Content = append(
		in.Content.Content,
		wiki.BlockNode{Type: "taskCollection", Attrs: map[string]any{"filter": "all"}},
	)
	page, err := createAndAcceptPageForRun(s, ctx, member, f.RunA, in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := createAndAcceptPageForRun(s, ctx, member, f.RunA, in)
	if err != nil || replay != page {
		t.Fatalf("create replay = %+v, %v; want %+v", replay, err, page)
	}
	in.Title = "Changed request"
	if _, err = createAndAcceptPageForRun(s, ctx, member, f.RunA, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay = %v", err)
	}
	var sourceTask uuid.UUID
	if err = pool.QueryRow(ctx, `SELECT task_id FROM artifacts WHERE id=$1`, page.ArtifactID).
		Scan(&sourceTask); err != nil ||
		sourceTask != f.TaskA {
		t.Fatalf("source task = %s, %v", sourceTask, err)
	}
	library, err := s.Snapshot(ctx, member, f.ProjectA, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, artifact := range library.Artifacts {
		if artifact.ID == page.ArtifactID {
			found = artifact.LatestVersion.RunID != nil && *artifact.LatestVersion.RunID == f.RunA
		}
	}
	if !found {
		t.Fatal("task result Page missing from library or missing Run attribution")
	}
	read, err := s.ReadPageForRun(ctx, member, f.RunA, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	var root wiki.BlockNode
	if err = json.Unmarshal(read.Content, &root); err != nil {
		t.Fatal(err)
	}
	doc, err := s.Document(ctx, p, f.ProjectA, page.VersionID, -1)
	if err != nil {
		t.Fatal(err)
	}
	_, decoded, _, err := wiki.MergeBlocks(doc.State, nil)
	if err != nil {
		t.Fatal(err)
	}
	if wiki.BlockHash(root.Content[1]) != wiki.BlockHash(decoded.Content[1]) {
		t.Fatalf("projection differs from CRDT: stored=%+v decoded=%+v", root.Content[1], decoded.Content[1])
	}
	target := root.Content[1]
	target.Content = []wiki.BlockNode{{Type: "text", Text: "Updated research finding."}}
	id := target.Attrs["blockId"].(string)
	edit := PageEditInput{
		RequestKey: uuid.New(),
		Sequence:   read.Sequence,
		Summary:    "Updated campaign research",
		Operations: []wiki.BlockOperation{
			{Type: "update", BlockID: id, ExpectedHash: read.BlockHashes[id], Content: &target},
		},
	}
	applied, err := editAndAcceptPageForRun(s, ctx, member, f.RunA, page.PageID, edit)
	if err != nil || applied.Status != "applied" {
		t.Fatalf("page update = %+v, %v", applied, err)
	}
	// Contributions remain private to the member who requested the work.
	project, err := s.Overview(ctx, member, f.ProjectA)
	if err != nil || len(project.Changes) != 2 || project.Changes[0].PageID != page.PageID ||
		project.Changes[0].PageTitle != page.Title {
		t.Fatalf("page change feed = %+v, %v", project.Changes, err)
	}
	overviewRead, err := readOverviewForRun(s, ctx, member, f.RunA)
	if err != nil {
		t.Fatal(err)
	}
	live := wiki.BlockNode{
		Type:  "artifactReference",
		Attrs: map[string]any{"versionId": page.VersionID.String(), "format": "live", "display": "expanded"},
	}
	pinned := wiki.BlockNode{
		Type: "artifactReference",
		Attrs: map[string]any{
			"versionId": page.VersionID.String(),
			"format":    "pinned",
			"label":     "Initial research",
		},
	}
	if _, err = editAndAcceptOverviewForRun(
		s,
		ctx,
		member,
		f.RunA,
		PageEditInput{
			RequestKey: uuid.New(),
			Sequence:   overviewRead.Sequence,
			Summary:    "Added campaign and original evidence",
			Operations: []wiki.BlockOperation{
				{Type: "insert", Content: &live},
				{Type: "insert", Content: &pinned},
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	// Initial native versions and explicit checkpoints pin a live source's
	// current content, while the editor continues following its saved draft.
	container := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{live}}
	agentPage, err := createAndAcceptPageForRun(s,
		ctx,
		member,
		f.RunA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Campaign plan", Content: container},
	)
	if err != nil {
		t.Fatal(err)
	}
	native, _ := json.Marshal(container)
	humanPage, err := s.Mutate(
		ctx,
		p,
		f.ProjectA,
		Command{RequestKey: uuid.New(), Type: "create_document", Title: "Campaign brief", Content: native},
	)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Snapshot(ctx, p, f.ProjectA, 0)
	if err != nil {
		t.Fatal(err)
	}
	var revision int64
	for _, artifact := range current.Artifacts {
		if artifact.ID == overview.ArtifactID {
			revision = artifact.Revision
		}
	}
	checkpoint, err := s.Mutate(
		ctx,
		p,
		f.ProjectA,
		Command{
			RequestKey:       uuid.New(),
			Type:             "checkpoint",
			ArtifactID:       &overview.ArtifactID,
			VersionID:        &overview.VersionID,
			WikiPageID:       &overview.PageID,
			ExpectedRevision: revision,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Mutate(ctx, p, f.ProjectA, Command{
		RequestKey: uuid.New(), Type: "checkpoint", ArtifactID: &overview.ArtifactID,
		VersionID: &checkpoint.ID, WikiPageID: &overview.PageID, ExpectedRevision: revision,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("checkpoint accepted a stale Artifact revision: %v", err)
	}
	for _, version := range []uuid.UUID{agentPage.VersionID, humanPage.ID, checkpoint.ID} {
		content, err := s.VersionBlocks(ctx, p, f.ProjectA, version)
		if err != nil {
			t.Fatal(err)
		}
		var frozen wiki.BlockNode
		if err = json.Unmarshal(content, &frozen); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, node := range frozen.Content {
			if node.Type != "artifactReference" || node.Attrs["label"] != page.Title {
				continue
			}
			found = true
			if node.Attrs["format"] != "pinned" {
				t.Fatal("immutable version retained live reference")
			}
			source, err := uuid.Parse(node.Attrs["versionId"].(string))
			if err != nil {
				t.Fatal(err)
			}
			sourceContent, err := s.VersionBlocks(ctx, p, f.ProjectA, source)
			if err != nil || !strings.Contains(string(sourceContent), "Updated research") {
				t.Fatalf("pinned current source = %s, %v", sourceContent, err)
			}
		}
		if !found {
			t.Fatal("snapshot lost the current Page reference or its title")
		}
	}
	livePage, err := s.ReadPageForRun(ctx, member, f.RunA, agentPage.PageID)
	var liveRoot wiki.BlockNode
	if decodeErr := json.Unmarshal(livePage.Content, &liveRoot); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if err != nil || liveRoot.Content[0].Attrs["format"] != "live" {
		t.Fatalf("draft stopped following live Page: %s, %v", livePage.Content, err)
	}
	preview, err := previewOverview(s, ctx, p, f.ProjectA)
	if err != nil {
		t.Fatal(err)
	}
	if got := wiki.BlocksMarkdown(
		preview.Documents[page.VersionID.String()+":live"],
	); !strings.Contains(
		got,
		"Updated research",
	) {
		t.Fatalf("live preview = %s", got)
	}
	if got := wiki.BlocksMarkdown(
		preview.Documents[page.VersionID.String()],
	); !strings.Contains(
		got,
		"sourced audience",
	) {
		t.Fatalf("pinned preview = %s", got)
	}
	capture := PresentationInput{
		PageID:      preview.PageID,
		RequestKey:  uuid.New(),
		Sequence:    preview.Sequence,
		Fingerprint: preview.Fingerprint,
	}
	saved, err := s.CapturePresentation(ctx, p, f.ProjectA, capture)
	if err != nil {
		t.Fatal(err)
	}
	// The ordinary saved overview version is independently immutable, including
	// the work state inside an embedded Page, without Presentation rendering context.
	savedRow, err := store.Queries().
		GetProjectPresentation(ctx, generated.GetProjectPresentationParams{CompanyID: f.CompanyA, ProjectID: f.ProjectA, ID: saved.ID})
	if err != nil {
		t.Fatal(err)
	}
	savedContent, err := s.VersionBlocks(ctx, p, f.ProjectA, savedRow.PageVersionID)
	var savedRoot wiki.BlockNode
	if err != nil || json.Unmarshal(savedContent, &savedRoot) != nil {
		t.Fatalf("saved overview version: %s, %v", savedContent, err)
	}
	found = false
	for _, node := range savedRoot.Content {
		if node.Type != "artifactReference" || node.Attrs["label"] != page.Title {
			continue
		}
		source, err := uuid.Parse(node.Attrs["versionId"].(string))
		if err != nil {
			t.Fatal(err)
		}
		content, err := s.VersionBlocks(ctx, p, f.ProjectA, source)
		if err != nil || !strings.Contains(string(content), `"capturedAt"`) ||
			!strings.Contains(string(content), `"capturedTasks"`) {
			t.Fatalf("saved embedded Page did not retain work state: %s, %v", content, err)
		}
		found = true
	}
	if !found {
		t.Fatal("saved overview version lost embedded Page")
	}
	latest, err := s.ReadPageForRun(ctx, member, f.RunA, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	target.Content = []wiki.BlockNode{{Type: "text", Text: "A later finding."}}
	edit.RequestKey = uuid.New()
	edit.Sequence = latest.Sequence
	edit.Operations[0].ExpectedHash = latest.BlockHashes[id]
	edit.Operations[0].Content = &target
	if _, err = editAndAcceptPageForRun(s, ctx, member, f.RunA, page.PageID, edit); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CapturePresentation(
		ctx,
		p,
		f.ProjectA,
		PresentationInput{
			PageID:      preview.PageID,
			RequestKey:  uuid.New(),
			Sequence:    preview.Sequence,
			Fingerprint: preview.Fingerprint,
		},
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale live preview capture = %v", err)
	}
	frozen, err := s.Presentation(ctx, p, f.ProjectA, saved.ID)
	if err != nil ||
		!strings.Contains(
			wiki.BlocksMarkdown(frozen.Documents[page.VersionID.String()+":live"]),
			"Updated research",
		) {
		t.Fatalf("frozen live source = %+v, %v", frozen, err)
	}
	// Review is scoped to the Page recorded on the contribution, not the current overview pointer.
	if _, err = s.ReviewPageChange(
		ctx,
		member,
		f.ProjectA,
		page.PageID,
		applied.ID,
		"undo",
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("undo over subsequent change = %v", err)
	}
	if _, err = s.ReadPageForRun(ctx, member, f.RunB, overview.PageID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign run = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, f.RunA)
	if _, err = createAndAcceptPageForRun(s, ctx, member, f.RunA, in); !errors.Is(err, ErrForbidden) {
		t.Fatalf("terminal creation = %v", err)
	}
}

func TestTaskPageDiscoveryAndNestedBlockInserts(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	s := newCanvasServices(store, bus, nil)
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET status='running',execution_authorized_by_user_id=$2,task_execution_generation=(SELECT execution_generation FROM tasks WHERE id=$3) WHERE id=$1`,
		f.RunA,
		f.UserA,
		f.TaskA,
	)
	page, err := createAndAcceptPageForRun(s, ctx, p, f.RunA, PageCreateInput{
		RequestKey: uuid.New(),
		Title:      "Campaign checklist",
		Content: wiki.BlocksFromMarkdown(
			"- First action\n\n| Item | State |\n| --- | --- |\n| Draft | Ready |",
		),
	})
	if err != nil {
		t.Fatalf("create list/table Page: %v", err)
	}
	for i := range 64 {
		_, err := createAndAcceptPageForRun(s,
			ctx,
			p,
			f.RunA,
			PageCreateInput{
				RequestKey: uuid.New(),
				Title:      fmt.Sprintf("Newer result %d", i),
				Content:    wiki.BlocksFromMarkdown("A later result."),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	read, err := s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	projectRead, err := s.ReadProjectForRun(ctx, p, f.RunA)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, artifact := range projectRead.Pages {
		found = found || artifact.ID == page.ArtifactID
	}
	if !found || len(projectRead.Pages) != 65 {
		t.Errorf("old Page missing from complete discovery: found=%v, count=%d", found, len(projectRead.Pages))
	}
	var root wiki.BlockNode
	if err := json.Unmarshal(read.Content, &root); err != nil {
		t.Fatal(err)
	}
	listID := root.Content[0].Attrs["blockId"].(string)
	tableID := root.Content[1].Attrs["blockId"].(string)
	paragraphID := root.Content[0].Content[0].Content[0].Attrs["blockId"].(string)
	item := wiki.BlocksFromMarkdown("- Next action").Content[0].Content[0]
	row := wiki.BlocksFromMarkdown("| Item | State |\n| --- | --- |\n| Review | Pending |").Content[0].Content[1]
	result, err := editAndAcceptPageForRun(s, ctx, p, f.RunA, page.PageID, PageEditInput{
		RequestKey: uuid.New(),
		Sequence:   read.Sequence,
		Summary:    "Add the next action and review state",
		Operations: []wiki.BlockOperation{
			{Type: "insert", ParentID: listID, Content: &item},
			{Type: "insert", ParentID: tableID, Content: &row},
		},
	})
	if err != nil || result.Status != "applied" {
		t.Fatalf("nested inserts: %+v, %v", result, err)
	}
	read, err = s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil || json.Unmarshal(read.Content, &root) != nil {
		t.Fatalf("read nested content: %s, %v", read.Content, err)
	}
	if len(root.Content[0].Content) != 2 || len(root.Content[1].Content) != 3 {
		t.Fatalf("nested insert did not preserve siblings: %s", read.Content)
	}
	if len(read.Changes) != 2 || read.Changes[0].ID != result.ID || read.Changes[0].Status != "applied" {
		t.Fatalf("applied contribution missing from Agent read: %+v", read.Changes)
	}
	if _, err = s.EditPageForRun(ctx, p, f.RunA, page.PageID, PageEditInput{
		RequestKey: uuid.New(), Sequence: read.Sequence, Summary: "Invalid list destination",
		Operations: []wiki.BlockOperation{{Type: "insert", ParentID: paragraphID, Content: &item}},
	}); !errors.Is(err, ErrPageInvalidInsert) {
		t.Fatalf("invalid list destination error: got %v, want ErrPageInvalidInsert", err)
	}
}
