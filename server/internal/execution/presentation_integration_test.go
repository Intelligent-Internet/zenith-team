//go:build integration

package execution

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

type presentationObjects struct {
	attachment.ObjectStorage
	contents     map[string]string
	beforeUpload func(string)
}

func (s *presentationObjects) Upload(_ context.Context, key string, reader io.Reader, _ int64, _, _ string) error {
	body, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	if s.beforeUpload != nil {
		s.beforeUpload(key)
	}
	s.contents[key] = string(body)
	return nil
}

func (s *presentationObjects) GetReader(_ context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(s.contents[key])), nil
}

func TestNativePresentationSubmission(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	objects := &presentationObjects{contents: map[string]string{}}
	files, err := attachment.NewAttachmentService(
		attachment.ServiceOptions{Store: store, Bus: bus, Objects: objects},
	)
	if err != nil {
		t.Fatal(err)
	}
	pages := canvas.NewPageService(store, bus)
	presentations := canvas.NewPresentationService(store, bus, files)
	board := canvas.NewCanvasService(store, bus, files)
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
	working, err := pages.CreatePageForRun(
		ctx,
		p,
		f.RunA,
		canvas.PageCreateInput{
			RequestKey: uuid.New(),
			Title:      "Audience research",
			Content:    wiki.BlocksFromMarkdown("## Findings\n\nEvidence selected for review."),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	accept := func(changeID uuid.UUID) {
		t.Helper()
		detail, e := pages.PageChangeDetail(ctx, p, f.ProjectA, working.PageID, changeID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pages.ReviewPageChange(
			ctx,
			p,
			f.ProjectA,
			working.PageID,
			changeID,
			canvas.PageChangeReviewInput{
				RequestKey:  uuid.New(),
				Fingerprint: detail.Fingerprint,
				Action:      "apply",
			},
		); e != nil {
			t.Fatal(e)
		}
	}
	accept(*working.ChangeID)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	var sourceRun uuid.UUID
	if err = pool.QueryRow(ctx, `INSERT INTO runs(company_id,agent_id,task_id,conversation_id,runtime_id,status,run_type) VALUES($1,$2,$3,$4,$5,'completed','task') RETURNING id`, f.CompanyA, f.AgentA, f.TaskA, f.ConversationA, f.RuntimeA).
		Scan(&sourceRun); err != nil {
		t.Fatal(err)
	}
	var evidence uuid.UUID
	if err = pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Private evidence','Finding','native-source',$3, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.CompanyA, sourceRun, f.UserB).
		Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO output_grants(company_id,output_id,user_id,permission,granted_by_user_id) VALUES($1,$2,$3,'view',$4)`,
		f.CompanyA,
		evidence,
		f.UserA,
		f.UserB,
	)
	source, err := board.Mutate(
		ctx,
		p,
		f.ProjectA,
		canvas.Command{
			RequestKey: uuid.New(),
			Type:       "add_artifact",
			OutputID:   &evidence,
			Title:      "Private evidence",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	read, err := pages.ReadPageForRun(ctx, p, f.RunA, working.PageID)
	if err != nil {
		t.Fatal(err)
	}
	reference := wiki.BlockNode{
		Type:  "artifactReference",
		Attrs: map[string]any{"versionId": source.ID.String(), "format": "pinned"},
	}
	change, err := pages.EditPageForRun(
		ctx,
		p,
		f.RunA,
		working.PageID,
		canvas.PageEditInput{
			RequestKey: uuid.New(),
			Sequence:   read.Sequence,
			Summary:    "Included evidence",
			Operations: []wiki.BlockOperation{{Type: "insert", Content: &reference}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	accept(change.ID)
	// Project editing and result capture do not require Company Wiki publishing.
	// A member can save the chosen Page and its live Task Run can submit it.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET role='member' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		f.UserA,
	)
	p.Role = auth.CompanyRoleMember
	preview, err := presentations.PreviewPresentationForRun(ctx, p, f.RunA, working.PageID)
	if err != nil {
		t.Fatal(err)
	}
	input := canvas.PresentationInput{
		PageID:      working.PageID,
		RequestKey:  uuid.New(),
		Sequence:    preview.Sequence,
		Fingerprint: preview.Fingerprint,
	}
	saved, err := presentations.CapturePresentation(ctx, p, f.ProjectA, input)
	if err != nil {
		t.Fatalf("member Page capture = %v", err)
	}
	// Caller-derived request keys cannot address the server's reserved upload,
	// even when an ordinary upload has matching public metadata.
	body, err := presentations.PresentationExport(ctx, p, f.ProjectA, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.RequestKey = uuid.New()
	forged, err := files.Initiate(ctx, attachment.InitiateInput{
		CompanyID: p.CompanyID, UploadedByUserID: p.UserID,
		RequestKey: uuid.NewSHA1(input.RequestKey, []byte("project-presentation-export")),
		Filename:   "project-presentation.md", ContentType: "text/markdown", SizeBytes: int64(len(body)),
	}, strings.NewReader(strings.Repeat("x", len(body))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = files.Complete(
		ctx,
		attachment.CompleteInput{CompanyID: p.CompanyID, UploadedByUserID: p.UserID, ID: forged.Attachment.ID},
	); err != nil {
		t.Fatal(err)
	}
	uploadsBefore := len(objects.contents)
	// A pending canonical file must already inherit its capture's source access.
	// Otherwise cancellation during upload can strand a normal uploader-owned file.
	objects.beforeUpload = func(key string) {
		var bound bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_presentations p JOIN attachments a ON a.id=p.export_attachment_id WHERE a.object_key=$1 AND p.source_run_id=$2 AND p.page_id=$3)`, key, f.RunA, working.PageID).
			Scan(&bound)
		if err != nil || !bound {
			t.Errorf("canonical export access before upload = %v, %v", bound, err)
		}
	}
	prepared, err := presentations.PreparePresentationForRun(ctx, p, f.RunA, input)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.AttachmentID == forged.Attachment.ID {
		t.Fatal("canonical export reused an ordinary uploaded file")
	}
	replay, err := presentations.PreparePresentationForRun(ctx, p, f.RunA, input)
	if err != nil || replay != prepared || len(objects.contents) != uploadsBefore+1 {
		t.Fatalf("capture replay = %+v, %v", replay, err)
	}
	var fakeReviews int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM deliverables`).
		Scan(&fakeReviews); err != nil ||
		fakeReviews != 0 {
		t.Fatalf("capture created review records = %d, %v", fakeReviews, err)
	}
	service, err := NewDeliverableService(DeliverableServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	origin := RunOrigin{
		RunID:            f.RunA,
		CompanyID:        f.CompanyA,
		AgentID:          f.AgentA,
		TaskID:           &f.TaskA,
		ConversationID:   f.ConversationA,
		ConversationKind: "task",
		OriginUserID:     &f.UserA,
	}
	request := DeliverablePresentRequest{
		Type:    ArtifactTypeText,
		Title:   prepared.Title,
		Summary: "Selected results and exact evidence.",
		Items: []DeliverablePresentItemRequest{
			{
				Role:         DeliverableItemRolePrimary,
				Label:        "Project presentation",
				AttachmentID: &prepared.AttachmentID,
			},
		},
	}
	if _, err = service.Present(ctx, p, origin, request); !errors.Is(err, ErrDeliverableInvalidInput) {
		t.Fatalf("unbound canonical export = %v", err)
	}
	request.PresentationID = &prepared.PresentationID
	submitted, err := service.Present(ctx, p, origin, request)
	if err != nil || !submitted.Created {
		t.Fatalf("native submission = %+v, %v", submitted, err)
	}
	again, err := service.Present(ctx, p, origin, request)
	if err != nil || again.OutputID != submitted.OutputID || again.Created {
		t.Fatalf("submission replay = %+v, %v", again, err)
	}
	result, err := service.FetchRunArtifactResult(ctx, p, f.RunA, submitted.OutputID)
	if err != nil || result.Output == nil || result.Output.PresentationURL == nil ||
		!strings.Contains(*result.Output.PresentationURL, "pageId="+working.PageID.String()) ||
		!strings.Contains(*result.Output.PresentationURL, "presentationId="+prepared.PresentationID.String()) {
		t.Fatalf("native result = %+v, %v", result, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET role='owner' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		f.UserA,
	)
	p.Role = auth.CompanyRoleOwner
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, f.RunA)
	result, err = service.FetchRunArtifactResult(ctx, p, f.RunA, submitted.OutputID)
	if err != nil || result.Output == nil || result.Deliverable != nil {
		t.Fatalf("saved Output = %+v, %v; want no artifact approval", result, err)
	}
	if result.Output.Freshness != "current" || result.Output.LiveURL == nil {
		t.Fatalf("fresh delivery = %+v, want current with live destination", result.Output)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE wiki_canvas_drafts SET head_seq=head_seq+1 WHERE page_id=$1`,
		working.PageID,
	)
	result, err = service.FetchRunArtifactResult(ctx, p, f.RunA, submitted.OutputID)
	if err != nil || result.Output == nil || result.Output.Freshness != "current" {
		t.Fatalf("no-op save freshness = %+v, %v", result, err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE wiki_pages SET title='Updated title' WHERE id=$1`, working.PageID)
	result, err = service.FetchRunArtifactResult(ctx, p, f.RunA, submitted.OutputID)
	if err != nil || result.Output == nil || result.Output.Freshness != "stale" {
		t.Fatalf("changed title freshness = %+v, %v", result, err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE wiki_pages SET title=$2 WHERE id=$1`, working.PageID, prepared.Title)
	// A later shared edit makes every projection stale without altering its capture.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE wiki_canvas_drafts SET content=jsonb_set(content,'{content,0,content}','[{"type":"text","text":"Updated after delivery"}]'::jsonb),head_seq=head_seq+1 WHERE page_id=$1`,
		working.PageID,
	)
	result, err = service.FetchRunArtifactResult(ctx, p, f.RunA, submitted.OutputID)
	if err != nil || result.Output == nil || result.Output.Freshness != "stale" || result.Output.LiveURL == nil {
		t.Fatalf("stale Run result = %+v, %v", result, err)
	}
	projected, err := service.ResolveArtifacts(ctx, p, []uuid.UUID{submitted.OutputID})
	if err != nil || len(projected) != 1 || projected[0].Artifact.Freshness != "stale" {
		t.Fatalf("stale delivery projection = %+v, %v", projected, err)
	}
	taskResults, err := service.ListTaskResults(
		ctx,
		p,
		f.TaskA,
		TaskResultListOptions{Limit: 10, SkipSources: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	foundStale := false
	for _, item := range taskResults.Items {
		if item.Output.ID == submitted.OutputID {
			foundStale = item.Output.Freshness == "stale"
		}
	}
	if !foundStale {
		t.Fatalf("Task result missing stale delivery: %+v", taskResults)
	}
	access, err := service.getArtifactAccess(ctx, p, submitted.OutputID)
	if err != nil {
		t.Fatal(err)
	}
	access, err = service.SetArtifactVisibility(ctx, p, submitted.OutputID,
		ArtifactVisibilityInput{Visibility: "public", ExpectedRevision: access.Revision})
	if err != nil || access.PublicToken == nil {
		t.Fatalf("public aggregate = %+v, %v", access, err)
	}
	public, err := service.GetPublicArtifact(ctx, *access.PublicToken)
	if err != nil || len(public.Items) == 0 {
		t.Fatalf("public aggregate files = %+v, %v", public, err)
	}
	for _, item := range public.Items {
		companyID, attachmentID, err := service.ResolvePublicArtifactFile(
			ctx,
			*access.PublicToken,
			item.Ordinal,
		)
		if err != nil || companyID != f.CompanyA || attachmentID == uuid.Nil {
			t.Fatalf("public aggregate file = %s, %s, %v", companyID, attachmentID, err)
		}
	}
	citations, err := service.ResolveArtifacts(ctx, p, []uuid.UUID{submitted.OutputID})
	if err != nil || len(citations) != 1 || citations[0].Artifact.PresentationURL == nil {
		t.Fatalf("native Wiki citation = %+v, %v", citations, err)
	}
	// A person grant opens the saved result without granting its Project workspace.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO output_grants(company_id,output_id,user_id,permission,granted_by_user_id) VALUES($1,$2,$3,'view',$4)`,
		f.CompanyA,
		submitted.OutputID,
		f.UserB,
		f.UserA,
	)
	recipient := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleMember}
	shared, err := service.ResolveArtifacts(ctx, recipient, []uuid.UUID{submitted.OutputID})
	if err != nil || len(shared) != 1 || shared[0].Artifact.PresentationURL != nil ||
		shared[0].Artifact.CanvasURL != nil ||
		shared[0].Artifact.LiveURL != nil ||
		shared[0].Artifact.Freshness != "unavailable" {
		t.Fatalf("shared citation outside Project = %+v, %v", shared, err)
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM output_grants WHERE output_id=$1 AND user_id=$2`, evidence, f.UserA)
	citations, err = service.ResolveArtifacts(ctx, p, []uuid.UUID{submitted.OutputID})
	if err != nil || len(citations) != 0 {
		t.Fatalf("revoked Wiki citation = %+v, %v", citations, err)
	}

	if _, err = service.GetArtifact(ctx, p, submitted.OutputID); !errors.Is(err, ErrDeliverableNotFound) {
		t.Fatalf("revoked aggregate read = %v", err)
	}
	if _, err = service.ResolveArtifactFileAttachment(
		ctx,
		p,
		f.RunA,
		submitted.OutputID,
		0,
	); !errors.Is(
		err,
		ErrDeliverableNotFound,
	) {
		t.Fatalf("revoked file read = %v", err)
	}
}

func TestProposalDeliveryFreshnessTracksAcceptanceAndRevision(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	files, err := attachment.NewAttachmentService(
		attachment.ServiceOptions{
			Store:   store,
			Bus:     bus,
			Objects: &presentationObjects{contents: map[string]string{}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	pages := canvas.NewPageService(store, bus)
	presentations := canvas.NewPresentationService(store, bus, files)
	service, err := NewDeliverableService(DeliverableServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
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
	page, err := pages.CreatePage(
		ctx,
		p,
		f.ProjectA,
		canvas.PageCreateInput{RequestKey: uuid.New(), Title: "Findings", Markdown: "Shared introduction"},
	)
	if err != nil {
		t.Fatal(err)
	}
	read, err := pages.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	block := wiki.BlocksFromMarkdown("Proposed finding").Content[0]
	change, err := pages.EditPageForRun(
		ctx,
		p,
		f.RunA,
		page.PageID,
		canvas.PageEditInput{
			RequestKey: uuid.New(),
			Sequence:   read.Sequence,
			Summary:    "Add finding",
			Operations: []wiki.BlockOperation{{Type: "insert", Content: &block}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := presentations.PreviewProposalForRun(ctx, p, f.RunA, page.PageID, change.ID)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := presentations.PreparePresentationForRun(
		ctx,
		p,
		f.RunA,
		canvas.PresentationInput{
			PageID:      page.PageID,
			ChangeID:    &change.ID,
			RequestKey:  uuid.New(),
			Sequence:    preview.Sequence,
			Fingerprint: preview.Fingerprint,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	output, err := service.Present(
		ctx,
		p,
		RunOrigin{
			RunID:            f.RunA,
			CompanyID:        f.CompanyA,
			AgentID:          f.AgentA,
			TaskID:           &f.TaskA,
			ConversationID:   f.ConversationA,
			ConversationKind: "task",
			OriginUserID:     &f.UserA,
		},
		DeliverablePresentRequest{
			Type:           ArtifactTypeText,
			Title:          capture.Title,
			Summary:        "Proposed finding",
			PresentationID: &capture.PresentationID,
			Items: []DeliverablePresentItemRequest{
				{
					Role:         DeliverableItemRolePrimary,
					Label:        "Findings",
					AttachmentID: &capture.AttachmentID,
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertFreshness := func(want string) {
		t.Helper()
		result, e := service.FetchRunArtifactResult(ctx, p, f.RunA, output.OutputID)
		if e != nil || result.Output == nil || result.Output.Freshness != want {
			t.Fatalf("delivery freshness = %+v, %v, want %s", result.Output, e, want)
		}
	}
	assertFreshness("current")
	detail, err := pages.PageChangeDetail(ctx, p, f.ProjectA, page.PageID, change.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pages.ReviewPageChange(
		ctx,
		p,
		f.ProjectA,
		page.PageID,
		change.ID,
		canvas.PageChangeReviewInput{RequestKey: uuid.New(), Fingerprint: detail.Fingerprint, Action: "apply"},
	); err != nil {
		t.Fatal(err)
	}
	assertFreshness("current")
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE wiki_canvas_drafts SET content=jsonb_set(content,'{content,0,content}','[{"type":"text","text":"Changed since proposal"}]'::jsonb),head_seq=head_seq+1 WHERE page_id=$1`,
		page.PageID,
	)
	assertFreshness("stale")
}
