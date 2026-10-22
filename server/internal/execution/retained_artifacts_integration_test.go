//go:build integration

package execution

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/artifact"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestRetainedArtifactSurvivesDeliveries(t *testing.T) {
	for _, kind := range []ArtifactType{ArtifactTypeFile, ArtifactTypeApp} {
		t.Run(string(kind), func(t *testing.T) {
			pool, _ := dbtest.StartPostgres(t)
			ctx := t.Context()
			if err := db.MigrateUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			f := dbtest.SeedFixture(t, ctx, pool)
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
			contentService := artifact.NewArtifactService(store, bus)
			owner := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
			reader := auth.UserPrincipal{
				CompanyID: f.CompanyA,
				UserID:    f.UserB,
				Role:      auth.CompanyRoleMember,
			}
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
				f.CompanyA,
				f.UserB,
			)
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`UPDATE runs SET status='running',conversation_id=$2,task_id=NULL WHERE id=$1`,
				f.RunA,
				f.DirectConversationA,
			)
			origin := RunOrigin{
				CompanyID:      f.CompanyA,
				RunID:          f.RunA,
				AgentID:        f.AgentA,
				ConversationID: f.DirectConversationA,
			}
			request := DeliverablePresentRequest{
				Type:    kind,
				Title:   "First title",
				Summary: "First saved result",
			}
			var attachmentID uuid.UUID
			if kind == ArtifactTypeApp {
				request.App = &DeliverablePresentAppRequest{
					Commands: []AppPreviewCommand{
						{Command: "npm", Args: []string{"run", "dev"}, Port: 5173},
					},
				}
			} else {
				attachmentID = insertReadyDeliverableAttachment(
					t,
					ctx,
					pool,
					f.CompanyA,
					f.UserA,
					"report.pdf",
				)
				request.Items = []DeliverablePresentItemRequest{
					{
						Role:         DeliverableItemRolePrimary,
						Label:        "Report",
						AttachmentID: &attachmentID,
					},
				}
			}
			first, err := service.Present(ctx, owner, origin, request)
			if err != nil || first.ArtifactID == nil || first.SnapshotID == nil {
				t.Fatalf("first = %+v, %v", first, err)
			}
			// Placement in a Project does not transfer standalone ownership or grants.
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`INSERT INTO project_canvases(company_id,project_id) VALUES($1,$2) ON CONFLICT(project_id) DO NOTHING`,
				f.CompanyA,
				f.ProjectA,
			)
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`INSERT INTO canvas_items(company_id,project_id,canvas_id,item_type,artifact_id,pinned_version_id,x,y,width,height,created_by_user_id)
    SELECT $1,$2,id,'artifact',$3,$4,40,40,320,280,$5 FROM project_canvases WHERE company_id=$1 AND project_id=$2`,
				f.CompanyA,
				f.ProjectA,
				*first.ArtifactID,
				*first.SnapshotID,
				f.UserA,
			)
			request.ArtifactID = first.ArtifactID
			request.Title = "Revised title"
			request.Summary = "Second saved result"
			second, err := service.Present(ctx, owner, origin, request)
			if err != nil || second.ArtifactID == nil || *second.ArtifactID != *first.ArtifactID ||
				second.SnapshotID == nil ||
				*second.SnapshotID == *first.SnapshotID ||
				second.OutputID == first.OutputID {
				t.Fatalf("second = %+v, %v; first = %+v", second, err, first)
			}
			replay, err := service.Present(ctx, owner, origin, request)
			if err != nil || replay.Created || replay.ArtifactID == nil ||
				*replay.ArtifactID != *first.ArtifactID ||
				replay.SnapshotID == nil ||
				*replay.SnapshotID != *second.SnapshotID {
				t.Fatalf("replay = %+v, %v", replay, err)
			}
			saved, err := contentService.Content(
				ctx,
				owner,
				artifact.ContentInput{ArtifactID: *first.ArtifactID, SnapshotID: first.SnapshotID},
			)
			if err != nil || saved.Title != "First title" || saved.Summary != "First saved result" ||
				saved.Number != 1 {
				t.Fatalf("first snapshot = %+v, %v", saved, err)
			}
			// A delivery grant is evidence access, not authority over ongoing work.
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`INSERT INTO output_grants(company_id,output_id,user_id,permission,granted_by_user_id) VALUES($1,$2,$3,'view',$4)`,
				f.CompanyA,
				first.OutputID,
				f.UserB,
				f.UserA,
			)
			if _, err = contentService.Content(
				ctx,
				reader,
				artifact.ContentInput{ArtifactID: *first.ArtifactID},
			); !errors.Is(
				err,
				artifact.ErrNotFound,
			) {
				t.Fatalf("delivery reader content = %v", err)
			}
			_, err = service.Present(ctx, reader, origin, request)
			if !errors.Is(err, ErrDeliverableNotFound) {
				t.Fatalf("delivery reader edit = %v", err)
			}
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`DELETE FROM outputs WHERE id=ANY($1)`,
				[]uuid.UUID{first.OutputID, second.OutputID},
			)
			current, err := contentService.Content(
				ctx,
				owner,
				artifact.ContentInput{ArtifactID: *first.ArtifactID},
			)
			if err != nil || current.SnapshotID != *second.SnapshotID || current.Title != "Revised title" ||
				current.Number != 2 {
				t.Fatalf("content after delivery deletion = %+v, %v", current, err)
			}
			old, err := contentService.Content(
				ctx,
				owner,
				artifact.ContentInput{ArtifactID: *first.ArtifactID, SnapshotID: first.SnapshotID},
			)
			if err != nil || old.Title != saved.Title || string(old.Payload) != string(saved.Payload) {
				t.Fatalf("old evidence changed = %+v, %v", old, err)
			}
			canvasService := canvas.NewCanvasService(store, bus, nil)
			scene, err := canvasService.Snapshot(ctx, owner, f.ProjectA, 0)
			if err != nil || len(scene.Items) != 1 || len(scene.Artifacts) != 1 {
				t.Fatalf("standalone placement = %+v, %v", scene, err)
			}
			history, err := canvasService.History(ctx, owner, f.ProjectA, *first.ArtifactID)
			if err != nil || len(history.Versions) != 2 {
				t.Fatalf("standalone placed history = %+v, %v", history, err)
			}
			if _, err = canvasService.Threads(ctx, owner, f.ProjectA, *first.SnapshotID); err != nil {
				t.Fatalf("standalone snapshot authority = %v", err)
			}
			dbtest.GrantProject(t, ctx, pool, f.CompanyA, f.ProjectA, f.UserB, "project.read")
			hidden, err := canvasService.Snapshot(ctx, reader, f.ProjectA, 0)
			if err != nil || len(hidden.Artifacts) != 0 {
				t.Fatalf("placement granted content = %+v, %v", hidden, err)
			}
			if kind == ArtifactTypeFile {
				if len(old.Items) != 1 {
					t.Fatalf("retained files = %+v", old.Items)
				}
				item := old.Items[0]
				download, err := contentService.ContentAttachment(
					ctx,
					owner,
					artifact.ContentAttachmentInput{
						ArtifactID: *first.ArtifactID,
						SnapshotID: *first.SnapshotID,
						ItemID:     item.ID,
					},
				)
				if err != nil || download != attachmentID {
					t.Fatalf("retained attachment = %s, %v", download, err)
				}
				if _, err = pool.Exec(
					ctx,
					`DELETE FROM asset_versions WHERE id=$1`,
					item.AssetVersionID,
				); err == nil {
					t.Fatal("deleted an asset version retained by immutable evidence")
				}
				_, err = contentService.ContentAttachment(
					ctx,
					owner,
					artifact.ContentAttachmentInput{
						ArtifactID: *first.ArtifactID,
						SnapshotID: *second.SnapshotID,
						ItemID:     item.ID,
					},
				)
				if !errors.Is(err, artifact.ErrNotFound) {
					t.Fatalf("cross-snapshot item read = %v", err)
				}
			}
			if _, err = contentService.Content(
				ctx,
				reader,
				artifact.ContentInput{ArtifactID: *first.ArtifactID},
			); !errors.Is(
				err,
				artifact.ErrNotFound,
			) {
				t.Fatalf("private retained content = %v", err)
			}
		})
	}
}
