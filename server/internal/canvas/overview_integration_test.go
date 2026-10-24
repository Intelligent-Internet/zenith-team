//go:build integration

package canvas

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/reearth/ygo/crdt"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestProjectOverviewCaptureFlow(t *testing.T) {
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
	first, err := createOverviewPage(s, ctx, p, f.ProjectA, OverviewInput{RequestKey: uuid.New(), Action: "create"})
	if err != nil || first.PageID == uuid.Nil {
		t.Fatalf("create = %+v, %v", first, err)
	}
	again, err := s.Overview(ctx, p, f.ProjectA)
	if err != nil || again.PageID == nil || *again.PageID != first.PageID {
		t.Fatalf("read = %+v, %v", again, err)
	}
	preview, err := s.PreviewPresentation(ctx, p, f.ProjectA, first.PageID, first.Sequence)
	if err != nil || preview.Fingerprint == "" {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	input := PresentationInput{
		PageID:      preview.PageID,
		RequestKey:  uuid.New(),
		Sequence:    preview.Sequence,
		Fingerprint: preview.Fingerprint,
	}
	saved, err := s.CapturePresentation(ctx, p, f.ProjectA, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CapturePresentation(ctx, p, f.ProjectA, input)
	if err != nil || replay.ID != saved.ID {
		t.Fatalf("replay = %+v, %v; want %s", replay, err, saved.ID)
	}
	if _, err = s.Presentation(ctx, p, f.ProjectB, saved.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign project = %v", err)
	}
	if _, err = s.CapturePresentation(
		ctx,
		p,
		f.ProjectA,
		PresentationInput{
			PageID:      preview.PageID,
			RequestKey:  uuid.New(),
			Sequence:    preview.Sequence,
			Fingerprint: strings.Repeat("a", 64),
		},
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("wrong preview = %v", err)
	}
	var body map[string]any
	if err = json.Unmarshal(saved.Content, &body); err != nil || body["type"] != "doc" {
		t.Fatalf("captured body = %#v, %v", body, err)
	}
	read, err := s.Presentation(ctx, p, f.ProjectA, saved.ID)
	if err != nil || string(read.Content) != string(saved.Content) || !read.CanExport {
		t.Fatalf("saved read = %+v, %v", read, err)
	}
}

func TestPresentationRetainsSourcesAndRechecksAccess(t *testing.T) {
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
	published, err := s.PublishPresentation(
		ctx,
		p,
		f.ProjectA,
		saved.ID,
		PublicationInput{RequestKey: uuid.New(), ConfirmPublicExposure: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	publicHTML, err := s.PublicPresentationHTML(ctx, *published.PublicToken)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM output_grants WHERE output_id=$1 AND user_id=$2`, private, f.UserA)
	retainedPublicHTML, err := s.PublicPresentationHTML(ctx, *published.PublicToken)
	if err != nil || retainedPublicHTML != publicHTML {
		t.Fatalf("public capture resolved revoked sources: %v", err)
	}
	if err := s.RevokePublication(ctx, p, f.ProjectA, saved.ID, *published.ID); err != nil {
		t.Fatalf("source loss prevented revocation: %v", err)
	}
	if _, err := s.PublishPresentation(
		ctx,
		p,
		f.ProjectA,
		saved.ID,
		PublicationInput{RequestKey: uuid.New(), ConfirmPublicExposure: true},
	); !errors.Is(
		err,
		ErrForbidden,
	) {
		t.Fatalf("publication after source revocation: %v", err)
	}

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
	var content []byte
	if err = pool.QueryRow(ctx, `SELECT content FROM project_presentations WHERE id=$1`, saved.ID).
		Scan(&content); err != nil ||
		!strings.Contains(string(content), "Restricted evidence") {
		t.Fatalf("capture was rewritten: %s, %v", content, err)
	}
	// New evidence marks declared prose stale without rewriting its words.
	board, err := s.Snapshot(ctx, p, f.ProjectA, 0)
	if err != nil {
		t.Fatal(err)
	}
	var artifact Artifact
	for _, row := range board.Artifacts {
		if row.LatestVersionID == page.ID {
			artifact = row
		}
	}
	if _, err = s.Mutate(
		ctx,
		p,
		f.ProjectA,
		Command{
			RequestKey:       uuid.New(),
			Type:             "checkpoint",
			ArtifactID:       &artifact.ID,
			VersionID:        &page.ID,
			WikiPageID:       artifact.LatestVersion.WikiPageID,
			ExpectedRevision: artifact.Revision,
		},
	); err != nil {
		t.Fatal(err)
	}
	living, err := s.Page(ctx, p, f.ProjectA, overview.PageID)
	if err != nil || len(living.Sources) != 2 || living.Sources[0].Status != "unavailable" ||
		living.Sources[1].Status != "needs_refresh" {
		t.Fatalf("source freshness = %+v, %v", living.Sources, err)
	}
	current, err := s.Document(ctx, p, f.ProjectA, overview.VersionID, -1)
	if err != nil || !strings.Contains(current.Markdown, "The finding is supported") {
		t.Fatalf("narrative was rewritten = %s, %v", current.Markdown, err)
	}
	// Canonical exports stay pinned even before they become submitted Artifacts.
	var file uuid.UUID
	if err = pool.QueryRow(ctx, `INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status) VALUES($1,$2,gen_random_uuid(),'presentation.md','text/markdown',4,'ready') RETURNING id`, f.CompanyA, f.UserA).
		Scan(&file); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE project_presentations SET export_attachment_id=$2 WHERE id=$1`,
		saved.ID,
		file,
	)
	denied, err := store.Queries().
		HasInaccessibleOutputAttachment(ctx, generated.HasInaccessibleOutputAttachmentParams{CompanyID: f.CompanyA, AttachmentID: file, ActorUserID: f.UserA})
	if err != nil || !denied {
		t.Fatalf("direct export access = %v, %v", denied, err)
	}
	cleaned, err := store.Queries().
		TransitionClaimedAbandonedReadyAttachments(ctx, generated.TransitionClaimedAbandonedReadyAttachmentsParams{AttachmentIds: []uuid.UUID{file}, AbandonedGraceMilliseconds: 0})
	if err != nil || len(cleaned) != 0 {
		t.Fatalf("retained export cleanup = %v, %v", cleaned, err)
	}
	runDownload := generated.AuthorizeRunAttachmentDownloadParams{
		CompanyID: f.CompanyA, AttachmentID: file,
		RunID: f.RunA, AuthorizerUserID: f.UserA,
	}
	if _, err = store.Queries().AuthorizeRunAttachmentDownload(ctx, runDownload); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("Run download with revoked evidence = %v, want no rows", err)
	}
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO output_grants(company_id,output_id,user_id,permission,granted_by_user_id)
		 VALUES($1,$2,$3,'view',$4)`, f.CompanyA, private, f.UserA, f.UserB)
	if _, err = store.Queries().AuthorizeRunAttachmentDownload(ctx, runDownload); err != nil {
		t.Fatalf("Run download with readable evidence = %v, want allowed", err)
	}
}

func TestOverviewAgentAuthorityAndHumanEdits(t *testing.T) {
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
	if _, err = readOverviewForRun(s, ctx, p, f.RunA); err != nil {
		t.Fatalf("same-project live task read = %v", err)
	}
	read, err := readOverviewForRun(s, ctx, p, f.RunA)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := s.Document(ctx, p, f.ProjectA, overview.VersionID, -1)
	if err != nil {
		t.Fatal(err)
	}
	peer := crdt.New()
	defer peer.Destroy()
	if err = crdt.ApplyUpdateV1(peer, draft.State, nil); err != nil {
		t.Fatal(err)
	}
	fragment := peer.GetXmlFragment("content")
	heading := fragment.Children()[0].(*crdt.YXmlElement)
	text := heading.Children()[0].(*crdt.YXmlText)
	peer.Transact(func(tx *crdt.Transaction) { text.Insert(tx, text.Len(), " chosen by a person", nil) })
	insert := wiki.BlocksFromMarkdown("Agent result").Content[0]
	in := PageEditInput{
		RequestKey: uuid.New(),
		Sequence:   read.Sequence,
		Summary:    "Added the result",
		Operations: []wiki.BlockOperation{{Type: "insert", Content: &insert}},
	}
	change, err := editAndAcceptOverviewForRun(s, ctx, p, f.RunA, in)
	if err != nil || change.Status != "applied" {
		t.Fatalf("insert = %+v, %v", change, err)
	}
	replay, err := editOverviewForRun(s, ctx, p, f.RunA, in)
	if err != nil || replay.ID != change.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	human, err := s.SyncDocument(
		ctx,
		p,
		f.ProjectA,
		overview.VersionID,
		DocumentSyncInput{Update: peer.EncodeStateAsUpdate(), BaseSequence: draft.Sequence},
	)
	if err != nil || !strings.Contains(human.Markdown, "chosen by a person") ||
		!strings.Contains(human.Markdown, "Agent result") {
		t.Fatalf("concurrent human edit = %s, %v", human.Markdown, err)
	}
	read, err = readOverviewForRun(s, ctx, p, f.RunA)
	if err != nil {
		t.Fatal(err)
	}
	var root wiki.BlockNode
	if err = json.Unmarshal(read.Content, &root); err != nil {
		t.Fatal(err)
	}
	humanTarget := root.Content[0]
	humanTarget.Content = []wiki.BlockNode{{Type: "text", Text: "Agent replacement"}}
	targetID := humanTarget.Attrs["blockId"].(string)
	proposal, err := editOverviewForRun(s,
		ctx,
		p,
		f.RunA,
		PageEditInput{
			RequestKey: uuid.New(),
			Sequence:   read.Sequence,
			Summary:    "Proposed a purpose revision",
			Operations: []wiki.BlockOperation{
				{
					Type:         "update",
					BlockID:      targetID,
					ExpectedHash: read.BlockHashes[targetID],
					Content:      &humanTarget,
				},
			},
		},
	)
	if err != nil || proposal.Status != "proposed" {
		t.Fatalf("human target = %+v, %v", proposal, err)
	}
	contributions, err := readOverviewForRun(s, ctx, p, f.RunA)
	if err != nil || len(contributions.Changes) == 0 || contributions.Changes[0].ID != proposal.ID ||
		contributions.Changes[0].Status != "proposed" {
		t.Fatalf("proposal missing from Agent read: %+v, %v", contributions.Changes, err)
	}
	unchanged, err := s.Document(ctx, p, f.ProjectA, overview.VersionID, -1)
	if err != nil || strings.Contains(unchanged.Markdown, "Agent replacement") {
		t.Fatalf("proposal changed human content: %s, %v", unchanged.Markdown, err)
	}
	if _, err = s.ReviewPageChange(ctx, p, f.ProjectA, overview.PageID, proposal.ID, "apply"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReviewPageChange(ctx, p, f.ProjectA, overview.PageID, proposal.ID, "undo"); err != nil {
		t.Fatal(err)
	}
	read, err = readOverviewForRun(s, ctx, p, f.RunA)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err = editOverviewForRun(s, ctx, p, f.RunA, PageEditInput{
		RequestKey: uuid.New(),
		Sequence:   read.Sequence,
		Summary:    "A different revision for review",
		Operations: []wiki.BlockOperation{
			{
				Type:         "update",
				BlockID:      targetID,
				ExpectedHash: read.BlockHashes[targetID],
				Content:      &humanTarget,
			},
		},
	})
	if err != nil || proposal.Status != "proposed" {
		t.Fatalf("second proposal: %+v, %v", proposal, err)
	}
	if _, err = s.ReviewPageChange(ctx, p, f.ProjectA, overview.PageID, proposal.ID, "reject"); err != nil {
		t.Fatal(err)
	}
	read, err = readOverviewForRun(s, ctx, p, f.RunA)
	if err != nil || len(read.Changes) == 0 || read.Changes[0].ID != proposal.ID ||
		read.Changes[0].Status != "rejected" {
		t.Fatalf("rejection missing from Agent read: %+v, %v", read.Changes, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE tasks SET execution_generation=execution_generation+1 WHERE id=$1`,
		f.TaskA,
	)
	if _, err = readOverviewForRun(s, ctx, p, f.RunA); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale generation read = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, f.RunA)
	if _, err = editAndAcceptOverviewForRun(s, ctx, p, f.RunA, in); !errors.Is(err, ErrForbidden) {
		t.Fatalf("terminal replay = %v", err)
	}
}

func TestPresentationFreezesWorkflowAndTaskState(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	definition := json.RawMessage(
		`{"key":"research","name":"Research release one","description":"Review evidence","highlights":["A sourced recommendation"],"pages":[]}`,
	)
	s := newCanvasServices(store, bus, nil, func(string) (json.RawMessage, error) { return definition, nil })
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	if _, err := createOverviewPage(s,
		ctx,
		p,
		f.ProjectA,
		OverviewInput{RequestKey: uuid.New(), Action: "create"},
	); err != nil {
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
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE tasks SET title='Original research Task',labels=ARRAY['workflow:research'] WHERE id=$1`,
		f.TaskA,
	)
	read, err := readOverviewForRun(s, ctx, p, f.RunA)
	if err != nil {
		t.Fatal(err)
	}
	workflow := wiki.BlockNode{
		Type:  "workflowReference",
		Attrs: map[string]any{"workflowKey": "research", "display": "expanded"},
	}
	if _, err = editAndAcceptOverviewForRun(s,
		ctx,
		p,
		f.RunA,
		PageEditInput{
			RequestKey: uuid.New(),
			Sequence:   read.Sequence,
			Summary:    "Include the research Workflow",
			Operations: []wiki.BlockOperation{{Type: "insert", Content: &workflow}},
		},
	); err != nil {
		t.Fatal(err)
	}
	preview, err := previewOverview(s, ctx, p, f.ProjectA)
	if err != nil {
		t.Fatal(err)
	}
	again, err := previewOverview(s, ctx, p, f.ProjectA)
	if err != nil || again.Fingerprint != preview.Fingerprint {
		t.Fatalf("unchanged preview fingerprint = %s, %v; want %s", again.Fingerprint, err, preview.Fingerprint)
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
	definition = json.RawMessage(
		`{"key":"research","name":"Research release two","description":"A newer process","highlights":[],"pages":[]}`,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE tasks SET title='Changed research Task',status='done',version=version+1 WHERE id=$1`,
		f.TaskA,
	)
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
		t.Fatalf("changed Workflow/work accepted an old preview = %v", err)
	}
	retained, err := s.Presentation(ctx, p, f.ProjectA, saved.ID)
	if err != nil || string(retained.Content) != string(saved.Content) ||
		!strings.Contains(string(retained.Content), "Research release one") ||
		!strings.Contains(string(retained.Content), "Original research Task") ||
		strings.Contains(string(retained.Content), "Changed research Task") {
		t.Fatalf("saved Workflow/work changed = %s, %v", retained.Content, err)
	}
}
