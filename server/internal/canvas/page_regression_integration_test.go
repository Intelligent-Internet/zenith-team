//go:build integration

package canvas

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"

	"github.com/google/uuid"
)

func TestSavedPageVersionRechecksSourceAccess(t *testing.T) {
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
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
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
	_ = overview
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
	var private uuid.UUID
	if err = pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id,visibility, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Restricted evidence','Original finding','overview-retention',$3,'private', origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.CompanyA, f.RunA, f.UserB).
		Scan(&private); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO output_grants(company_id,output_id,user_id,permission,granted_by_user_id) VALUES($1,$2,$3,'view',$4)`,
		f.CompanyA,
		private,
		f.UserA,
		f.UserB,
	)
	version, err := s.Mutate(
		ctx,
		p,
		f.ProjectA,
		Command{RequestKey: uuid.New(), Type: "add_artifact", OutputID: &private, Title: "Restricted evidence"},
	)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.Mutate(
		ctx,
		p,
		f.ProjectA,
		Command{
			RequestKey: uuid.New(),
			Type:       "create_document",
			Title:      "Research",
			Body:       "# Finding\n\nThe exact first revision.",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	read, err := readOverviewForRun(s, ctx, p, f.RunA)
	if err != nil {
		t.Fatal(err)
	}
	ref := wiki.BlockNode{
		Type: "artifactReference",
		Attrs: map[string]any{
			"versionId": version.ID.String(),
			"format":    "pinned",
			"label":     "Restricted evidence",
		},
	}
	summary := wiki.BlockNode{
		Type:    "paragraph",
		Attrs:   map[string]any{"sourceVersionIds": []string{page.ID.String()}},
		Content: []wiki.BlockNode{{Type: "text", Text: "The finding is supported by the Research Page."}},
	}
	if _, err = editAndAcceptOverviewForRun(s,
		ctx,
		p,
		f.RunA,
		PageEditInput{
			RequestKey: uuid.New(),
			Sequence:   read.Sequence,
			Summary:    "Included exact evidence",
			Operations: []wiki.BlockOperation{
				{Type: "insert", Content: &ref},
				{Type: "insert", Content: &summary},
				{
					Type: "insert",
					Content: &wiki.BlockNode{
						Type:  "outputCollection",
						Attrs: map[string]any{"filter": "all"},
					},
				},
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	if source, err := s.ReadPageSource(
		ctx,
		p,
		f.RunA,
		page.ID,
		"",
	); err != nil ||
		!strings.Contains(source.Markdown, "exact first revision") {
		t.Fatalf("source = %+v, %v", source, err)
	}
	linked, err := readOverviewForRun(s, ctx, p, f.RunA)
	if err != nil {
		t.Fatal(err)
	}
	var linkedRoot wiki.BlockNode
	if err = json.Unmarshal(linked.Content, &linkedRoot); err != nil {
		t.Fatal(err)
	}
	for _, node := range linkedRoot.Content {
		if node.Type == "artifactReference" && node.Attrs["artifactId"] == nil {
			t.Fatal("a version-only reference did not resolve its stable content identity")
		}
	}
	preview, err := previewOverview(s, ctx, p, f.ProjectA)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.CapturePresentation(
		ctx,
		p,
		f.ProjectA,
		PresentationInput{
			PageID:      preview.PageID,
			RequestKey:  uuid.New(),
			Sequence:    preview.Sequence,
			Fingerprint: preview.Fingerprint,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM outputs WHERE id=$1`, private); err == nil {
		t.Fatal("retained evidence was deleted")
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM output_grants WHERE output_id=$1 AND user_id=$2`, private, f.UserA)
	redacted, err := s.Presentation(ctx, p, f.ProjectA, saved.ID)
	if err != nil || redacted.CanExport || strings.Contains(string(redacted.Content), "Restricted evidence") {
		t.Fatalf("redacted presentation = %+v, %v", redacted, err)
	}
	if _, err = s.PresentationExport(ctx, p, f.ProjectA, saved.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked export = %v", err)
	}
	if _, err = s.ReadPageSource(ctx, p, f.RunA, version.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked source read = %v", err)
	}
	var snapshot uuid.UUID
	if err = pool.QueryRow(ctx, `SELECT page_version_id FROM project_presentations WHERE id=$1`, saved.ID).
		Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	raw, err := s.VersionBlocks(ctx, p, f.ProjectA, snapshot)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked VersionBlocks=%s,%v", raw, err)
	}
	if _, err = s.VersionBody(ctx, p, f.ProjectA, snapshot); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked VersionBody=%v", err)
	}
	if _, err = s.ReadPageSource(ctx, p, f.RunA, snapshot, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked snapshot source=%v", err)
	}
	history, err := s.History(ctx, p, f.ProjectA, overview.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range history.Versions {
		if strings.Contains(v.Excerpt, "Restricted evidence") {
			t.Fatal("restricted source leaked through history excerpt")
		}
	}
}

func TestAcceptedChildAndParentSuggestions(t *testing.T) {
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
	page, err := createAndAcceptPageForRun(
		s,
		ctx,
		p,
		f.RunA,
		PageCreateInput{
			RequestKey: uuid.New(),
			Title:      "Research",
			Content:    wiki.BlocksFromMarkdown("> Original finding."),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	var root wiki.BlockNode
	if err = json.Unmarshal(read.Content, &root); err != nil {
		t.Fatal(err)
	}
	parent := root.Content[0]
	if parent.Type != "blockquote" {
		t.Fatalf("type=%s", parent.Type)
	}
	child := parent.Content[0]
	child.Content = []wiki.BlockNode{{Type: "text", Text: "New finding."}}
	id := child.Attrs["blockId"].(string)
	result, err := editAndAcceptPageForRun(
		s,
		ctx,
		p,
		f.RunA,
		page.PageID,
		PageEditInput{
			RequestKey: uuid.New(),
			Sequence:   read.Sequence,
			Summary:    "Update finding",
			Operations: []wiki.BlockOperation{
				{Type: "update", BlockID: id, ExpectedHash: read.BlockHashes[id], Content: &child},
			},
		},
	)
	if err != nil || result.Status != "applied" {
		t.Fatalf("first edit=%+v,%v", result, err)
	}
	read, err = s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(read.Content, &root); err != nil {
		t.Fatal(err)
	}
	parent = root.Content[0]
	parent.Content[0].Content = []wiki.BlockNode{{Type: "text", Text: "Final finding."}}
	id = parent.Attrs["blockId"].(string)
	result, err = editAndAcceptPageForRun(
		s,
		ctx,
		p,
		f.RunA,
		page.PageID,
		PageEditInput{
			RequestKey: uuid.New(),
			Sequence:   read.Sequence,
			Summary:    "Refine quotation",
			Operations: []wiki.BlockOperation{
				{Type: "update", BlockID: id, ExpectedHash: read.BlockHashes[id], Content: &parent},
			},
		},
	)
	if err != nil || result.Status != "applied" {
		t.Fatalf("agent-only parent edit=%+v,%v; want applied", result, err)
	}
}

func TestPendingPageContributionStaysDiscoverable(t *testing.T) {
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
		`INSERT INTO project_page_changes(company_id,project_id,page_id,actor_user_id,request_key,request_hash,operations,before_content,result,summary,status,created_at)
 SELECT $1,$2,$3,$4,gen_random_uuid(),'test','[]','{}','{}','Review probe',CASE WHEN n=0 THEN 'proposed' ELSE 'applied' END,now()+n*interval '1 second' FROM generate_series(0,50) n`,
		f.CompanyA,
		f.ProjectA,
		overview.PageID,
		f.UserA,
	)
	got, err := s.Overview(ctx, p, f.ProjectA)
	if err != nil {
		t.Fatal(err)
	}
	pending := false
	for _, change := range got.Changes {
		pending = pending || change.Status == "proposed"
	}
	if !pending {
		t.Fatal("pending contribution disappears after 50 newer applied changes")
	}
	feed, err := s.PageChanges(ctx, p, f.ProjectA, overview.PageID, "")
	if err != nil || len(feed.Pending) != 1 || len(feed.History) != 50 {
		t.Fatalf("first change feed = %+v, %v", feed, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO project_page_changes(company_id,project_id,page_id,actor_user_id,request_key,request_hash,operations,before_content,result,summary,status,created_at) VALUES($1,$2,$3,$4,gen_random_uuid(),'test','[]','{}','{}','Older change','applied',now()-interval '1 minute')`,
		f.CompanyA,
		f.ProjectA,
		overview.PageID,
		f.UserA,
	)
	feed, err = s.PageChanges(ctx, p, f.ProjectA, overview.PageID, "")
	if err != nil || feed.NextCursor == nil {
		t.Fatalf("missing history cursor = %+v, %v", feed, err)
	}
	older, err := s.PageChanges(ctx, p, f.ProjectA, overview.PageID, *feed.NextCursor)
	if err != nil || len(older.History) != 1 || len(older.Pending) != 1 || older.NextCursor != nil {
		t.Fatalf("older change feed = %+v, %v", older, err)
	}
}

func TestPageMetadataAndContributionBoundary(t *testing.T) {
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
		`UPDATE company_members SET role='member' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		f.UserA,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET status='running',execution_authorized_by_user_id=$2,task_execution_generation=(SELECT execution_generation FROM tasks WHERE id=$3) WHERE id=$1`,
		f.RunA,
		f.UserA,
		f.TaskA,
	)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	s := newCanvasServices(store, bus, nil)
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleMember}
	page, err := createAndAcceptPageForRun(
		s,
		ctx,
		p,
		f.RunA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Research", Markdown: "## Research\n\nDraft findings."},
	)
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.Page(ctx, p, f.ProjectA, page.PageID)
	if err != nil || !read.CanEdit {
		t.Fatalf("member Page = %+v, %v", read, err)
	}
	var wikiCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM company_wiki_pages WHERE id=$1`, page.PageID).
		Scan(&wikiCount); err != nil ||
		wikiCount != 0 {
		t.Fatalf("Project Page in company Wiki = %d, %v", wikiCount, err)
	}
	theme := "editorial"
	edited, err := s.UpdatePage(
		ctx,
		p,
		f.ProjectA,
		page.PageID,
		PageMetadataInput{Sequence: read.Sequence, Theme: &theme},
	)
	if err != nil || edited.Theme != theme {
		t.Fatalf("member theme = %+v, %v", edited, err)
	}
	if _, err = s.UpdatePage(
		ctx,
		p,
		f.ProjectA,
		page.PageID,
		PageMetadataInput{Sequence: read.Sequence, Theme: &theme},
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale metadata = %v", err)
	}
	theme = "compact"
	change, err := editAndAcceptPageForRun(
		s,
		ctx,
		p,
		f.RunA,
		page.PageID,
		PageEditInput{
			RequestKey: uuid.New(),
			Sequence:   edited.Sequence,
			Summary:    "Compact result",
			Theme:      &theme,
		},
	)
	if err != nil || change.Status != "applied" {
		t.Fatalf("agent theme = %+v, %v", change, err)
	}
	other, err := s.CreatePage(
		ctx,
		p,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Other", Markdown: "Other page."},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PageChangeDetail(ctx, p, f.ProjectA, other.PageID, change.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong Page detail = %v", err)
	}
	if _, err = s.ReviewPageChange(
		ctx,
		p,
		f.ProjectA,
		other.PageID,
		change.ID,
		"undo",
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("wrong Page review = %v", err)
	}
	if _, err = s.ReviewPageChange(ctx, p, f.ProjectA, page.PageID, change.ID, "undo"); err != nil {
		t.Fatal(err)
	}
	restored, err := s.Page(ctx, p, f.ProjectA, page.PageID)
	if err != nil || restored.Theme != "editorial" {
		t.Fatalf("theme undo = %+v, %v", restored, err)
	}
	preview, err := s.PreviewPresentation(ctx, p, f.ProjectA, page.PageID, restored.Sequence)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := s.CapturePresentation(
		ctx,
		p,
		f.ProjectA,
		PresentationInput{
			PageID:      page.PageID,
			RequestKey:  uuid.New(),
			Sequence:    preview.Sequence,
			Fingerprint: preview.Fingerprint,
		},
	)
	if err != nil || capture.Theme != "editorial" || capture.PageID != page.PageID {
		t.Fatalf("capture metadata = %+v, %v", capture, err)
	}
}

func TestProjectPageAccessFollowsProjectGrants(t *testing.T) {
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
	page, err := s.CreatePage(
		ctx,
		p,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Project notes", Markdown: "Private work"},
	)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`DELETE FROM project_grants WHERE project_id=$1 AND user_id=$2 AND permission <> 'project.read'`,
		f.ProjectA,
		f.UserA,
	)
	read, err := s.Page(ctx, p, f.ProjectA, page.PageID)
	if err != nil || read.CanEdit {
		t.Fatalf("reader page = %+v, %v", read, err)
	}
	if _, err = s.CreatePage(
		ctx,
		p,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Denied", Markdown: "Must not be saved"},
	); !errors.Is(
		err,
		auth.ErrProjectPermissionRequired,
	) {
		t.Fatalf("read-only write = %v", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`DELETE FROM project_grants WHERE project_id=$1 AND user_id=$2`,
		f.ProjectA,
		f.UserA,
	)
	if _, err = s.Page(ctx, p, f.ProjectA, page.PageID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("owner without project read = %v", err)
	}
}

func TestCanvasFileDownloadsFollowProjectAccess(t *testing.T) {
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
	var file uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status) VALUES($1,$2,gen_random_uuid(),'research.txt','text/plain',4,'ready') RETURNING id`, f.CompanyA, f.UserA).
		Scan(&file); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mutate(
		ctx,
		p,
		f.ProjectA,
		Command{RequestKey: uuid.New(), Type: "add_artifact", AttachmentID: &file, Title: "Research"},
	); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`DELETE FROM project_grants WHERE project_id=$1 AND user_id=$2`,
		f.ProjectA,
		f.UserA,
	)
	var allowed bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM attachment_access WHERE attachment_id=$1 AND user_id=$2)`, file, f.UserA).
		Scan(&allowed); err != nil ||
		allowed {
		t.Fatalf("revoked uploader download = %v, %v", allowed, err)
	}
	dbtest.GrantProject(t, ctx, pool, f.CompanyA, f.ProjectA, f.UserA, "project.read")
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM attachment_access WHERE attachment_id=$1 AND user_id=$2)`, file, f.UserA).
		Scan(&allowed); err != nil ||
		!allowed {
		t.Fatalf("Project reader download = %v, %v", allowed, err)
	}
}
