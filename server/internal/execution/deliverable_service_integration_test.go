//go:build integration

package execution

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	dbgen "boardly.dev/boardly/server/pkg/db/generated"
)

func TestWebAppPresentationOmitsLocalURLWithoutPublication(t *testing.T) {
	for _, test := range []struct {
		name string
		kind string
		role string
	}{
		{name: "task", kind: "task", role: "owner"},
		{name: "direct", kind: "direct", role: "owner"},
		{name: "schedule", kind: "schedule", role: "owner"},
		{name: "member", kind: "task", role: "member"},
	} {
		t.Run(test.name, func(t *testing.T) {
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
			service, err := NewDeliverableService(
				DeliverableServiceOptions{Store: store, Bus: bus},
			)
			if err != nil {
				t.Fatal(err)
			}
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`UPDATE company_members SET role = $3 WHERE company_id = $1 AND user_id = $2`,
				fixture.CompanyA,
				fixture.UserA,
				test.role,
			)
			dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'running' WHERE id = $1`, fixture.RunA)
			if test.kind == "task" {
				dbtest.MustExec(
					t,
					ctx,
					pool,
					`UPDATE tasks SET execution_generation = 1 WHERE id = $1`,
					fixture.TaskA,
				)
				dbtest.MustExec(
					t,
					ctx,
					pool,
					`UPDATE runs SET task_execution_generation = 1 WHERE id = $1`,
					fixture.RunA,
				)
			}
			if test.kind != "task" {
				dbtest.MustExec(
					t,
					ctx,
					pool,
					`UPDATE runs SET task_id = NULL WHERE id = $1`,
					fixture.RunA,
				)
				if test.kind == "schedule" {
					dbtest.MustExec(t, ctx, pool, `WITH schedule AS (
                        INSERT INTO schedules (company_id, conversation_id, creator_user_id, request_key,
                            agent_id, reviewed_provider, name, instructions, trigger_type, run_at)
                        VALUES ($1, $2, $3, gen_random_uuid(), $4, 'codex', 'Publish app', 'Publish app', 'once', now())
                        RETURNING id
                    ) UPDATE conversations SET kind = 'schedule', task_id = NULL, schedule_id = schedule.id,
                        agent_id = $4, created_by_user_id = $3 FROM schedule WHERE conversations.id = $2`,
						fixture.CompanyA, fixture.ConversationA, fixture.UserA, fixture.AgentA)
				} else {
					dbtest.MustExec(
						t,
						ctx,
						pool,
						`UPDATE conversations SET kind = 'direct', task_id = NULL, created_by_user_id = $2 WHERE id = $1`,
						fixture.ConversationA,
						fixture.UserA,
					)
				}
			}
			principal := auth.UserPrincipal{
				UserID:    fixture.UserA,
				CompanyID: fixture.CompanyA,
				Role:      auth.CompanyRoleOwner,
			}
			origin := RunOrigin{
				RunID:          fixture.RunA,
				CompanyID:      fixture.CompanyA,
				AgentID:        fixture.AgentA,
				ConversationID: fixture.ConversationA,
			}
			request := DeliverablePresentRequest{
				Type: ArtifactTypeApp,
				App: &DeliverablePresentAppRequest{Commands: []AppPreviewCommand{{
					Command: "npm", Args: []string{"run", "dev"}, Port: 5173,
				}}},
				Title: "Web app", Summary: "Ready for local preview.",
			}
			result, err := service.Present(ctx, principal, origin, request)
			if err != nil || !result.Created {
				t.Fatalf("Present() = %+v, %v", result, err)
			}
			var payload []byte
			if err := pool.QueryRow(
				ctx,
				`SELECT payload FROM outputs WHERE id = $1`,
				result.OutputID,
			).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			var stored DeliverablePresentAppRequest
			if err := json.Unmarshal(payload, &stored); err != nil || len(stored.Commands) != 1 ||
				stored.Commands[0].Command != "npm" || stored.Commands[0].Port != 5173 {
				t.Fatalf("stored app payload = %s, %v", payload, err)
			}
			publicationQuery := dbgen.GetOutputPublicationByOutputParams{
				CompanyID: fixture.CompanyA,
				OutputID:  result.OutputID,
			}
			if _, err := store.Queries().GetOutputPublicationByOutput(ctx, publicationQuery); !errors.Is(
				err,
				pgx.ErrNoRows,
			) {
				t.Fatalf("automatic publication error = %v, want no publication", err)
			}
			replay, err := service.Present(ctx, principal, origin, request)
			if err != nil || replay.Created || replay.OutputID != result.OutputID {
				t.Fatalf("Present(replay) = %+v, %v", replay, err)
			}
			view, err := service.FetchRunResult(ctx, principal, fixture.RunA)
			if err != nil || view.RuntimeID == nil || *view.RuntimeID != fixture.RuntimeA || view.Output == nil ||
				view.Output.Type != ArtifactTypeApp ||
				view.Output.URL != nil || len(view.Output.Items) != 0 ||
				view.Deliverable != nil {
				t.Fatalf("FetchRunResult() = %+v, %v", view, err)
			}
			if got, want := view.Output.CanvasURL != nil, test.kind == "task"; got != want {
				t.Fatalf("canvas destination present = %v, want %v", got, want)
			}
			artifacts, err := service.ListConversationArtifacts(
				ctx,
				principal,
				fixture.ConversationA,
				ArtifactListOptions{Limit: 10},
			)
			if err != nil || len(artifacts.Items) != 1 || artifacts.Items[0].Artifact.URL != nil ||
				len(artifacts.Items[0].Artifact.Items) != 0 {
				t.Fatalf("ListConversationArtifacts() = %+v, %v", artifacts, err)
			}
			if test.kind == "task" && test.role == "owner" {
				dbtest.MustExec(
					t,
					ctx,
					pool,
					`UPDATE runs SET status = 'completed', updated_at = statement_timestamp() WHERE id = $1`,
					fixture.RunA,
				)
			}
		})
	}
}

func TestDeliverablePresentsAudioMedia(t *testing.T) {
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
	service, err := NewDeliverableService(DeliverableServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	attachmentID := insertReadyDeliverableAttachment(t, ctx, pool, fixture.CompanyA, fixture.UserA, "music.mp3")
	dbtest.MustExec(t, ctx, pool, `UPDATE attachments SET content_type='audio/mpeg' WHERE id=$1`, attachmentID)
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='running' WHERE id=$1`, fixture.RunA)
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	presented, err := service.Present(ctx, principal, RunOrigin{
		RunID: fixture.RunA, CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
		TaskID: &fixture.TaskA, ConversationID: fixture.ConversationA,
	}, DeliverablePresentRequest{
		Type: ArtifactTypeMedia, Title: "Music", Summary: "Generated music.",
		Items: []DeliverablePresentItemRequest{{
			Role: DeliverableItemRolePrimary, Label: "Music", AttachmentID: &attachmentID,
		}},
	})
	if err != nil || !presented.Created {
		t.Fatalf("Present() = %+v, %v, want created audio Artifact", presented, err)
	}
	result, err := service.FetchRunResult(ctx, principal, fixture.RunA)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output == nil || result.Output.Type != ArtifactTypeMedia || len(result.Output.Items) != 1 {
		t.Fatalf("Output = %+v, want media Artifact with one item", result.Output)
	}
	item := result.Output.Items[0]
	if item.ContentType != "audio/mpeg" || item.DisplayName != "music.mp3" ||
		item.Role != DeliverableItemRolePrimary || item.AssetVersionID == uuid.Nil {
		t.Fatalf("item = %+v, want immutable primary MP3", item)
	}
}

func TestDeliverablePresentationAndReplay(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	var notifications []canvas.Changed
	if err := bus.Subscribe(canvas.Topic, func(ctx context.Context, event events.Event) error {
		change, ok := event.Payload.(canvas.Changed)
		if !ok {
			t.Errorf("canvas payload = %T", event.Payload)
			return nil
		}
		var committed bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_canvas_operations WHERE project_id=$1 AND seq=$2)`, change.ProjectID, change.Sequence).
			Scan(&committed)
		if err != nil || !committed {
			t.Errorf("notification before committed receipt: %v", err)
		}
		notifications = append(notifications, change)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewDeliverableService(DeliverableServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatalf("NewDeliverableService() error = %v", err)
	}
	attachmentID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO attachments (
			id, company_id, uploaded_by_user_id, request_key,
			filename, content_type, size_bytes, status
		) VALUES ($1, $2, $3, $4, 'launch-report.pdf', 'application/pdf', 4, 'ready')`,
		attachmentID,
		fixture.CompanyA,
		fixture.UserA,
		dbtest.NewUUID(t),
	)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks SET execution_generation = 1 WHERE id = $1`, fixture.TaskA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runs
		SET status = 'running', task_execution_generation = 1,
			execution_authorized_by_user_id = $2, updated_at = statement_timestamp()
		WHERE id = $1`, fixture.RunA, fixture.UserA)

	principal := auth.UserPrincipal{
		UserID: fixture.UserA, SessionID: dbtest.NewUUID(t), AuthKind: auth.TokenKindBearer,
		CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	origin := RunOrigin{
		RunID: fixture.RunA, CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
		TaskID: &fixture.TaskA, ConversationID: fixture.ConversationA,
		ConversationKind: "task", OriginUserID: &fixture.UserA,
	}
	request := DeliverablePresentRequest{
		Title: "Launch report", Summary: "Final launch analysis.",
		Items: []DeliverablePresentItemRequest{{
			Role: DeliverableItemRolePrimary, Label: "Report", AttachmentID: &attachmentID,
		}, {
			Role: DeliverableItemRoleFinal, Label: "Alternate report", AttachmentID: &attachmentID,
		}, {
			Role: DeliverableItemRoleFinal, Label: "Third report", AttachmentID: &attachmentID,
		}},
	}
	presented, err := service.Present(ctx, principal, origin, request)
	if err != nil || !presented.Created || presented.OutputID == uuid.Nil {
		t.Fatalf("Present() = %+v, %v", presented, err)
	}
	replay, err := service.Present(ctx, principal, origin, request)
	if err != nil || replay.Created || replay.OutputID != presented.OutputID {
		t.Fatalf("Present(replay) = %+v, %v", replay, err)
	}
	var registered, placed, sequence int64
	if err := pool.QueryRow(ctx, `
        SELECT (SELECT count(*) FROM project_artifact_versions WHERE output_id=$1),
               (SELECT count(*) FROM canvas_items WHERE project_id=$2),
               (SELECT head_seq FROM project_canvases WHERE project_id=$2)`,
		presented.OutputID, fixture.ProjectA,
	).Scan(&registered, &placed, &sequence); err != nil {
		t.Fatal(err)
	}
	if registered != 1 || placed != 1 || sequence != 1 {
		t.Fatalf("automatic registration: versions=%d placements=%d sequence=%d", registered, placed, sequence)
	}
	if len(notifications) != 1 || notifications[0].Sequence != 1 {
		t.Fatalf("registration/replay notifications = %+v", notifications)
	}
	// Automatic placement inherits the Output ACL, including later grant changes.
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		fixture.CompanyA, fixture.UserB)
	dbtest.MustExec(t, ctx, pool, `UPDATE outputs SET visibility='private' WHERE id=$1`, presented.OutputID)
	canvasService := canvas.NewCanvasService(store, bus, nil)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, fixture.UserB, "project.read")
	reader := auth.UserPrincipal{CompanyID: fixture.CompanyA, UserID: fixture.UserB, Role: auth.CompanyRoleMember}
	for _, tc := range []struct {
		name      string
		principal auth.UserPrincipal
		want      int
	}{
		{"owner", principal, 1},
		{"other member", reader, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := canvasService.Snapshot(ctx, tc.principal, fixture.ProjectA, 0)
			if err != nil || len(got.Items) != tc.want || len(got.Artifacts) != tc.want {
				t.Fatalf("automatic canvas visibility = %+v, %v; want %d cards and artifacts", got, err, tc.want)
			}
		})
	}
	dbtest.MustExec(t, ctx, pool, `INSERT INTO output_grants(company_id,output_id,user_id,permission,granted_by_user_id)
		VALUES($1,$2,$3,'view',$4)`, fixture.CompanyA, presented.OutputID, fixture.UserB, fixture.UserA)
	shared, err := canvasService.Snapshot(ctx, reader, fixture.ProjectA, 0)
	if err != nil || len(shared.Items) != 1 || len(shared.Artifacts) != 1 {
		t.Fatalf("shared automatic canvas = %+v, %v; want 1 card and artifact", shared, err)
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM output_grants WHERE output_id=$1`, presented.OutputID)
	revoked, err := canvasService.Snapshot(ctx, reader, fixture.ProjectA, 0)
	if err != nil || len(revoked.Items) != 0 || len(revoked.Artifacts) != 0 {
		t.Fatalf("revoked automatic canvas = %+v, %v; want no cards or artifacts", revoked, err)
	}
	replacementAttachmentID := insertReadyDeliverableAttachment(
		t, ctx, pool, fixture.CompanyA, fixture.UserA, "launch-preview.pdf",
	)
	replacement := DeliverablePresentRequest{
		Title: "Launch preview", Summary: "Final launch preview.",
		Items: []DeliverablePresentItemRequest{{
			Role: DeliverableItemRolePrimary, Label: "Preview",
			AttachmentID: &replacementAttachmentID,
		}},
	}
	replaced, err := service.Present(ctx, principal, origin, replacement)
	if err != nil || !replaced.Created || replaced.OutputID == uuid.Nil ||
		replaced.OutputID == presented.OutputID {
		t.Fatalf("Present(replacement) = %+v, %v", replaced, err)
	}
	replacementReplay, err := service.Present(ctx, principal, origin, replacement)
	if err != nil || replacementReplay.Created || replacementReplay.OutputID != replaced.OutputID {
		t.Fatalf("Present(replacement replay) = %+v, %v", replacementReplay, err)
	}
	firstReplay, err := service.Present(ctx, principal, origin, request)
	if err != nil || firstReplay.Created || firstReplay.OutputID != presented.OutputID {
		t.Fatalf("Present(first replay after replacement) = %+v, %v", firstReplay, err)
	}
	artifacts, err := service.ListConversationArtifacts(
		ctx,
		principal,
		fixture.ConversationA,
		ArtifactListOptions{Limit: 10},
	)
	if err != nil || len(artifacts.Items) != 2 ||
		artifacts.Items[0].Artifact.ID != replaced.OutputID ||
		artifacts.Items[1].Artifact.ID != presented.OutputID {
		t.Fatalf("ListConversationArtifacts() = %+v, %v", artifacts, err)
	}
	for _, item := range artifacts.Items {
		if item.Artifact.CanvasURL == nil {
			t.Fatalf("Output %s has no canvas destination", item.Artifact.ID)
		}
	}

	authority, err := store.Queries().GetPublicationAuthority(
		ctx,
		dbgen.GetPublicationAuthorityParams{
			UserID: principal.UserID, RunID: fixture.RunA, CompanyID: fixture.CompanyA,
		},
	)
	if err != nil || authority.OutputID != replaced.OutputID {
		t.Fatalf("GetPublicationAuthority() = %+v, %v", authority, err)
	}

	dbtest.MustExec(t, ctx, pool, `
		UPDATE runs SET status = 'completed', updated_at = statement_timestamp()
		WHERE id = $1`, fixture.RunA)
	terminalReplay, err := service.Present(ctx, principal, origin, request)
	if err != nil || terminalReplay.Created || terminalReplay.OutputID != presented.OutputID {
		t.Fatalf("Present(terminal replay) = %+v, %v", terminalReplay, err)
	}
	terminalPresentation := replacement
	terminalPresentation.Summary = "Changed after completion."
	if _, err := service.Present(
		ctx, principal, origin, terminalPresentation,
	); !errors.Is(err, ErrDeliverableConflict) {
		t.Fatalf("Present(after completion) error = %v, want ErrDeliverableConflict", err)
	}
	result, err := service.FetchRunArtifactResult(ctx, principal, fixture.RunA, presented.OutputID)
	if err != nil || result.Output == nil || result.Deliverable != nil {
		t.Fatalf("saved Output = %+v, %v; want no artifact approval", result, err)
	}
	firstResult, err := service.FetchRunArtifactResult(
		ctx, principal, fixture.RunA, presented.OutputID,
	)
	if err != nil || firstResult.Deliverable != nil || firstResult.Output == nil ||
		firstResult.Output.ID != presented.OutputID ||
		len(firstResult.Output.Items) != 3 ||
		firstResult.Output.Items[1].Role != DeliverableItemRoleFinal ||
		firstResult.Output.Items[2].Role != DeliverableItemRoleFinal ||
		firstResult.Output.Items[0].DisplayName != "launch-report.pdf" {
		t.Fatalf("FetchRunArtifactResult(first) = %+v, %v", firstResult, err)
	}
	if _, err := service.FetchRunArtifactResult(
		ctx, principal, fixture.RunA, dbtest.NewUUID(t),
	); !errors.Is(err, ErrDeliverableNotFound) {
		t.Fatalf("FetchRunArtifactResult(unknown) error = %v, want ErrDeliverableNotFound", err)
	}
	results, err := service.ListTaskResults(
		ctx,
		principal,
		fixture.TaskA,
		TaskResultListOptions{Limit: 10, SkipSources: true},
	)
	if err != nil || len(results.Items) != 2 || results.Items[0].Output.ID != replaced.OutputID ||
		results.Items[1].Output.ID != presented.OutputID ||
		results.Items[0].Deliverable != nil || results.Items[1].Deliverable != nil {
		t.Fatalf("ListTaskResults() = %+v, %v", results, err)
	}
	resolvedAttachmentID, err := service.ResolveArtifactFileAttachment(
		ctx, principal, fixture.RunA, replaced.OutputID, 0,
	)
	if err != nil || resolvedAttachmentID != replacementAttachmentID {
		t.Fatalf("ResolveArtifactFileAttachment(latest) = %s, %v", resolvedAttachmentID, err)
	}
	firstAttachmentID, err := service.ResolveArtifactFileAttachment(
		ctx, principal, fixture.RunA, presented.OutputID, 0,
	)
	if err != nil || firstAttachmentID != attachmentID {
		t.Fatalf("ResolveArtifactFileAttachment(first) = %s, %v", firstAttachmentID, err)
	}
	viewerID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Artifact Viewer')`,
		viewerID, viewerID.String()+"@deliverables.boardly.test")
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role, status)
		VALUES ($1, $2, 'member', 'active')`, fixture.CompanyA, viewerID)
	viewer := auth.UserPrincipal{
		UserID: viewerID, CompanyID: fixture.CompanyA,
		Role: auth.CompanyRoleMember,
	}
	viewPermission := ArtifactPermissionView
	if _, err := service.ChangeArtifactGrant(ctx, principal, presented.OutputID,
		viewerID, &viewPermission, 1); err != nil {
		t.Fatalf("grant Artifact view: %v", err)
	}
	sharedArtifact, err := service.GetArtifact(ctx, viewer, presented.OutputID)
	if err != nil {
		t.Fatalf("GetArtifact(shared) = %v", err)
	}
	if sharedArtifact.Access.Permission != ArtifactPermissionView || sharedArtifact.Result.Output == nil {
		t.Fatalf("view granted Artifact = %+v", sharedArtifact)
	}
	if sharedArtifact.Source != nil {
		t.Fatalf("shared Artifact source = %+v, want no private source", sharedArtifact.Source)
	}

}

func TestTaskRunAuthorizerCanViewTheArtifactItPresented(t *testing.T) {
	for _, test := range []struct {
		name           string
		byColleague    bool
		wantGrants     int
		wantVisibility string
	}{
		{name: "colleague runs the creator's task", byColleague: true, wantGrants: 1, wantVisibility: "shared"},
		{name: "creator runs their own task", byColleague: false, wantGrants: 0, wantVisibility: "private"},
	} {
		t.Run(test.name, func(t *testing.T) {
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
			service, err := NewDeliverableService(DeliverableServiceOptions{Store: store, Bus: bus})
			if err != nil {
				t.Fatal(err)
			}
			creator := auth.UserPrincipal{
				UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
			}
			runner := creator
			if test.byColleague {
				// UserA created TaskA and hands it to UserB, a member, who
				// set AgentA up on a computer of their own and runs it there.
				runner = auth.UserPrincipal{
					UserID:    fixture.UserB,
					CompanyID: fixture.CompanyA,
					Role:      auth.CompanyRoleMember,
				}
				runtimeID := dbtest.NewUUID(t)
				dbtest.MustExec(t, ctx, pool, `
					INSERT INTO company_members (company_id, user_id, role)
					VALUES ($1, $2, 'member')`, fixture.CompanyA, fixture.UserB)
				// The colleague works on the Task's Project (ADR 0017).
				dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, fixture.UserB)
				dbtest.MustExec(t, ctx, pool, `
					INSERT INTO runtimes
						(id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
					VALUES ($1, $2, 'daemon-colleague', 'Colleague laptop', 'codex', 'online', $3, now())`,
					runtimeID, fixture.CompanyA, fixture.UserB)
				dbtest.MustExec(t, ctx, pool, `
					INSERT INTO agent_runtime_bindings (company_id, agent_id, user_id, runtime_id, provider)
					VALUES ($1, $2, $3, $4, 'codex')`,
					fixture.CompanyA, fixture.AgentA, fixture.UserB, runtimeID)
				dbtest.MustExec(t, ctx, pool, `
					UPDATE tasks SET assignee_user_id = $2 WHERE id = $1`, fixture.TaskA, fixture.UserB)
				dbtest.MustExec(t, ctx, pool, `
					UPDATE runs SET runtime_id = $2 WHERE id = $1`, fixture.RunA, runtimeID)
			}
			dbtest.MustExec(t, ctx, pool, `
				UPDATE tasks SET execution_generation = 1 WHERE id = $1`, fixture.TaskA)
			dbtest.MustExec(t, ctx, pool, `
				UPDATE runs
				SET status = 'running', task_execution_generation = 1,
					execution_authorized_by_user_id = $2, updated_at = statement_timestamp()
				WHERE id = $1`, fixture.RunA, runner.UserID)
			attachmentID := insertReadyDeliverableAttachment(
				t, ctx, pool, fixture.CompanyA, runner.UserID, "sourdough.pdf",
			)
			presented, err := service.Present(ctx, runner, RunOrigin{
				RunID: fixture.RunA, CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
				TaskID: &fixture.TaskA, ConversationID: fixture.ConversationA,
			}, DeliverablePresentRequest{
				Title: "Sourdough sentence", Summary: "One sentence about sourdough.",
				Items: []DeliverablePresentItemRequest{
					{
						Role:         DeliverableItemRolePrimary,
						Label:        "Sentence",
						AttachmentID: &attachmentID,
					},
				},
			})
			if err != nil || !presented.Created {
				t.Fatalf("Present() = %+v, %v", presented, err)
			}

			// The member who ran the Task sees the Artifact wherever the Task
			// shows it, and can download its file.
			artifacts, err := service.ListConversationArtifacts(
				ctx, runner, fixture.ConversationA, ArtifactListOptions{Limit: 10},
			)
			if err != nil || len(artifacts.Items) != 1 ||
				artifacts.Items[0].Artifact.ID != presented.OutputID {
				t.Fatalf("ListConversationArtifacts(runner) = %+v, %v", artifacts, err)
			}
			results, err := service.ListTaskResults(
				ctx, runner, fixture.TaskA, TaskResultListOptions{Limit: 10, SkipSources: true},
			)
			if err != nil || len(results.Items) != 1 {
				t.Fatalf("ListTaskResults(runner) = %+v, %v", results, err)
			}
			if _, err := service.ResolveArtifactFileAttachment(
				ctx, runner, fixture.RunA, presented.OutputID, 0,
			); err != nil {
				t.Fatalf("ResolveArtifactFileAttachment(runner) error = %v", err)
			}
			seen, err := service.GetArtifact(ctx, runner, presented.OutputID)
			wantPermission := ArtifactPermissionManage
			if test.byColleague {
				wantPermission = ArtifactPermissionView
			}
			if err != nil || seen.Access.Permission != wantPermission {
				t.Fatalf("GetArtifact(runner) = %+v, %v", seen.Access, err)
			}

			if seen.Source == nil || seen.Source.Type != "task" || seen.Source.TaskID == nil || *seen.Source.TaskID != fixture.TaskA {
				t.Fatalf("Artifact source = %+v, want TaskA", seen.Source)
			}

			// The creator still owns it: they manage its sharing and it is
			// their review the finished Run asks for.
			owned, err := service.GetArtifact(ctx, creator, presented.OutputID)
			if err != nil || owned.Access.Permission != ArtifactPermissionManage ||
				owned.Access.OwnerUserID == nil || *owned.Access.OwnerUserID != fixture.UserA ||
				owned.Access.Visibility != test.wantVisibility || len(owned.Access.Grants) != test.wantGrants {
				t.Fatalf("GetArtifact(creator) = %+v, %v", owned.Access, err)
			}
			if test.byColleague && (owned.Access.Grants[0].UserID != fixture.UserB ||
				owned.Access.Grants[0].Permission != ArtifactPermissionView) {
				t.Fatalf(
					"creator's Artifact grants = %+v, want a view grant for the runner",
					owned.Access.Grants,
				)
			}
			dbtest.MustExec(t, ctx, pool, `
				UPDATE runs SET status = 'completed', updated_at = statement_timestamp()
				WHERE id = $1`, fixture.RunA)
		})
	}
}

// A tagged Run works for the person who tagged the Agent (D31), so what it
// presents is theirs (Access TECH B11). The channel Agent's chat belongs to
// the Agent's creator, yet the Output is owned by the tagger and starts
// private: the Agent's creator can neither open it, nor its file, nor share
// it. The tagger shares it into the channel with the existing controls, and
// a member of the channel then opens it. A Task Run's and a direct chat
// Run's Outputs keep today's owner and grant: the Task's creator, with a
// view grant for the colleague who ran it, and the chat's creator.
func TestATaggedRunsOutputBelongsToItsAuthorizer(t *testing.T) {
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
	service, err := NewDeliverableService(DeliverableServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	// UserA created AgentA. UserB, a member, set it up on a laptop of their
	// own (ADR 0014) and tags it; the reader is in the channel and never
	// tags it.
	creator := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	tagger := auth.UserPrincipal{UserID: fixture.UserB, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember}
	reader := auth.UserPrincipal{
		UserID:    dbtest.NewUUID(t),
		CompanyID: fixture.CompanyA,
		Role:      auth.CompanyRoleMember,
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO users (id, email, display_name) VALUES ($1, 'reader@example.test', 'Reader')`, reader.UserID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member'), ($1, $3, 'member')`, fixture.CompanyA, tagger.UserID, reader.UserID)
	taggerRuntime := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-tagger', 'Tagger laptop', 'codex', 'online', $3, now())`,
		taggerRuntime, fixture.CompanyA, tagger.UserID)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, tagger.UserID, taggerRuntime, "codex")

	present := func(principal auth.UserPrincipal, origin RunOrigin, filename string) uuid.UUID {
		t.Helper()
		attachmentID := insertReadyDeliverableAttachment(
			t,
			ctx,
			pool,
			fixture.CompanyA,
			principal.UserID,
			filename,
		)
		presented, err := service.Present(ctx, principal, origin, DeliverablePresentRequest{
			Title: filename, Summary: "What the Run made.",
			Items: []DeliverablePresentItemRequest{{
				Role: DeliverableItemRolePrimary, Label: "File", AttachmentID: &attachmentID,
			}},
		})
		if err != nil || !presented.Created {
			t.Fatalf("Present(%s) = %+v, %v", filename, presented, err)
		}
		return presented.OutputID
	}
	share := func(principal auth.UserPrincipal, outputID, chatID uuid.UUID) error {
		return store.WithinTx(ctx, pgx.TxOptions{}, func(q *dbgen.Queries) error {
			return ShareArtifactInTx(ctx, q, principal, outputID, chatID)
		})
	}
	wantOwner := func(name string, owner auth.UserPrincipal, outputID uuid.UUID, visibility string, grants int) {
		t.Helper()
		got, err := service.GetArtifact(ctx, owner, outputID)
		if err != nil || got.Access.Permission != ArtifactPermissionManage ||
			got.Access.OwnerUserID == nil || *got.Access.OwnerUserID != owner.UserID ||
			got.Access.Visibility != visibility || len(got.Access.Grants) != grants {
			t.Fatalf("%s Output access = %+v, %v, want owned by %s, %s, %d grants",
				name, got.Access, err, owner.UserID, visibility, grants)
		}
	}

	// The tagged Run, in a channel the creator and the reader are in too.
	channelID := dbtest.SeedDiscussionChannel(
		t,
		ctx,
		pool,
		fixture.CompanyA,
		tagger.UserID,
		[]uuid.UUID{fixture.AgentA},
	)
	dbtest.AddDiscussionMember(t, ctx, pool, fixture.CompanyA, channelID, creator.UserID)
	dbtest.AddDiscussionMember(t, ctx, pool, fixture.CompanyA, channelID, reader.UserID)
	taggedRun := dbtest.SeedTaggedRun(
		t,
		ctx,
		pool,
		fixture.CompanyA,
		channelID,
		fixture.AgentA,
		taggerRuntime,
		tagger.UserID,
	)
	// EnsureChannelAgentConversation makes the Agent's creator the creator
	// of the channel Agent's chat; the seed names the caller, so set it here.
	var agentChatID uuid.UUID
	if err := pool.QueryRow(ctx, `
		UPDATE conversations SET created_by_user_id = $2
		FROM runs WHERE runs.id = $1 AND conversations.id = runs.conversation_id
		RETURNING conversations.id`, taggedRun, creator.UserID).Scan(&agentChatID); err != nil {
		t.Fatal(err)
	}
	tagged := present(tagger, RunOrigin{
		RunID: taggedRun, CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
		ConversationID: agentChatID, ConversationKind: "channel_agent",
		AuthorizedByUserID: &tagger.UserID, InvocationChannelID: &channelID,
	}, "tagged.pdf")
	wantOwner("tagged Run", tagger, tagged, "private", 0)
	for name, viewer := range map[string]auth.UserPrincipal{"the Agent's creator": creator, "the reader": reader} {
		if _, err := service.GetArtifact(ctx, viewer, tagged); !errors.Is(err, ErrDeliverableNotFound) {
			t.Fatalf("%s opens the unshared Output: %v", name, err)
		}
		if _, err := service.ResolveArtifactFileAttachment(ctx, viewer, taggedRun, tagged, 0); !errors.Is(
			err, ErrDeliverableNotFound,
		) {
			t.Fatalf("%s downloads the unshared Output's file: %v", name, err)
		}
	}
	if err := share(creator, tagged, channelID); !errors.Is(err, ErrDeliverableNotFound) {
		t.Fatalf("the Agent's creator shares the tagger's Output: %v", err)
	}
	if err := share(tagger, tagged, channelID); err != nil {
		t.Fatalf("the tagger shares their Output into the channel: %v", err)
	}
	opened, err := service.GetArtifact(ctx, reader, tagged)
	if err != nil || opened.Access.Permission != ArtifactPermissionView {
		t.Fatalf("the reader opens the shared Output = %+v, %v", opened.Access, err)
	}
	if _, err := service.ResolveArtifactFileAttachment(ctx, reader, taggedRun, tagged, 0); err != nil {
		t.Fatalf("the reader downloads the shared Output's file: %v", err)
	}

	// A Task Run the tagger ran for the creator's Task keeps the Task's
	// creator as owner, with a view grant for the tagger.
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, tagger.UserID)
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET execution_generation = 1, assignee_user_id = $2 WHERE id = $1`,
		fixture.TaskA, tagger.UserID)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runs SET status = 'running', task_execution_generation = 1, runtime_id = $2,
			execution_authorized_by_user_id = $3, updated_at = statement_timestamp()
		WHERE id = $1`, fixture.RunA, taggerRuntime, tagger.UserID)
	task := present(tagger, RunOrigin{
		RunID: fixture.RunA, CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
		TaskID: &fixture.TaskA, ConversationID: fixture.ConversationA,
	}, "task.pdf")
	wantOwner("Task Run", creator, task, "shared", 1)

	// A direct chat's Run keeps the chat's creator as owner.
	directRun := dbtest.SeedRun(t, ctx, pool, dbtest.RunSeed{
		CompanyID: fixture.CompanyA, AgentID: fixture.AgentA, RuntimeID: fixture.RuntimeA,
		ConversationID: fixture.DirectConversationA, AuthorizerID: creator.UserID,
	})
	direct := present(creator, RunOrigin{
		RunID: directRun, CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
		ConversationID: fixture.DirectConversationA, ConversationKind: "direct",
	}, "direct.pdf")
	wantOwner("direct chat Run", creator, direct, "private", 0)
}

func TestDeliverablePresentFencesConcurrentMembershipRevocation(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewDeliverableService(DeliverableServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatalf("NewDeliverableService() error = %v", err)
	}
	attachmentID := insertReadyDeliverableAttachment(
		t, ctx, pool, fixture.CompanyA, fixture.UserA, "race-report.pdf",
	)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runs SET status = 'running', updated_at = statement_timestamp() WHERE id = $1`,
		fixture.RunA)

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Company lock holder: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := holder.Exec(
		ctx, `SELECT id FROM companies WHERE id = $1 FOR UPDATE`, fixture.CompanyA,
	); err != nil {
		t.Fatalf("hold Company lock: %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, SessionID: dbtest.NewUUID(t), AuthKind: auth.TokenKindBearer,
		CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	result := make(chan error, 1)
	go func() {
		_, presentErr := service.Present(ctx, principal, RunOrigin{
			RunID: fixture.RunA, CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
			ConversationID: fixture.ConversationA,
		}, DeliverablePresentRequest{
			Title: "Race report", Summary: "Must not survive a concurrent revocation.",
			Items: []DeliverablePresentItemRequest{{
				Role: DeliverableItemRolePrimary, Label: "Report", AttachmentID: &attachmentID,
			}},
		})
		result <- presentErr
	}()
	waitForBlockedOutputMutation(t, ctx, pool)
	if _, err := holder.Exec(ctx, `
		UPDATE company_members
		SET status = 'revoked', revoked_at = clock_timestamp(),
			updated_at = clock_timestamp(), version = version + 1
		WHERE company_id = $1 AND user_id = $2`, fixture.CompanyA, fixture.UserA); err != nil {
		t.Fatalf("revoke Output presenter: %v", err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("commit presenter revocation: %v", err)
	}
	select {
	case presentErr := <-result:
		if !errors.Is(presentErr, ErrDeliverableNotFound) {
			t.Fatalf("Present() error = %v, want ErrDeliverableNotFound", presentErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Present() did not resume after Company lock release")
	}
	var outputCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outputs WHERE run_id = $1`, fixture.RunA).
		Scan(&outputCount); err != nil {
		t.Fatalf("count Outputs: %v", err)
	}
	if outputCount != 0 {
		t.Fatalf("Output count after revocation race = %d, want 0", outputCount)
	}
}

func insertReadyDeliverableAttachment(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID, userID uuid.UUID,
	filename string,
) uuid.UUID {
	t.Helper()
	attachmentID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO attachments (
			id, company_id, uploaded_by_user_id, request_key,
			filename, content_type, size_bytes, status
		) VALUES ($1, $2, $3, $4, $5, 'application/pdf', 4, 'ready')`,
		attachmentID,
		companyID,
		userID,
		dbtest.NewUUID(t),
		filename,
	)
	return attachmentID
}

func containsDeliverableEvent(pending []events.Event, name string, id uuid.UUID) bool {
	for _, event := range pending {
		payload, ok := event.Payload.(DeliverableEvent)
		if ok && event.Name == name && payload.ID == id {
			return true
		}
	}
	return false
}
