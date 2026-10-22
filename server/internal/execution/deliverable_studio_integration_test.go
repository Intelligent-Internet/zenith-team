//go:build integration

package execution

import (
	"context"
	"errors"
	"reflect"
	"testing"

	artifactdomain "github.com/Intelligent-Internet/zenith-team/server/internal/artifact"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/studiocanvas"
)

func TestAttachStudioCanvasPreservesOutputAndScopesAccess(t *testing.T) {
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
	var changed int
	if err := bus.Subscribe(
		artifactdomain.EventChanged,
		func(_ context.Context, _ events.Event) error { changed++; return nil },
	); err != nil {
		t.Fatal(err)
	}
	service, err := NewDeliverableService(DeliverableServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	attachmentID := insertReadyDeliverableAttachment(t, ctx, pool, fixture.CompanyA, fixture.UserA, "milan.jpg")
	dbtest.MustExec(t, ctx, pool, `UPDATE attachments SET content_type='image/jpeg' WHERE id=$1`, attachmentID)
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='running' WHERE id=$1`, fixture.RunA)
	_, err = service.Present(
		ctx,
		principal,
		RunOrigin{
			RunID:          fixture.RunA,
			CompanyID:      fixture.CompanyA,
			AgentID:        fixture.AgentA,
			TaskID:         &fixture.TaskA,
			ConversationID: fixture.ConversationA,
		},
		DeliverablePresentRequest{
			Title:   "Milan",
			Summary: "Saved image",
			Items: []DeliverablePresentItemRequest{
				{Role: DeliverableItemRolePrimary, Label: "Image", AttachmentID: &attachmentID},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, fixture.RunA)
	canvas := studiocanvas.Canvas{
		Version:   1,
		ProjectID: "project",
		Nodes: []studiocanvas.Node{
			{
				ID:            "image",
				Type:          "image",
				Label:         "Milan",
				Position:      studiocanvas.Position{X: 160, Y: 120},
				OutputOrdinal: new(0),
			},
		},
		Edges: []studiocanvas.Edge{},
	}
	before, err := service.FetchRunResult(ctx, principal, fixture.RunA)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := service.AttachStudioCanvas(ctx, principal, fixture.RunA, canvas)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.AttachStudioCanvas(ctx, principal, fixture.RunA, canvas)
	if err != nil || !replay.AddedAt.Equal(saved.AddedAt) {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if changed != 1 {
		t.Fatalf("Studio source attachment artifact hints = %d, want one", changed)
	}
	after, err := service.FetchRunResult(ctx, principal, fixture.RunA)
	if err != nil || after.StudioCanvas == nil || after.StudioCanvas.Canvas.Nodes[0].Position.X != 160 ||
		!reflect.DeepEqual(after.Output, before.Output) {
		t.Fatalf("result = %+v, %v", after, err)
	}
	canvas.Nodes[0].Position.X = 200
	if _, err := service.AttachStudioCanvas(
		ctx,
		principal,
		fixture.RunA,
		canvas,
	); !errors.Is(
		err,
		ErrDeliverableConflict,
	) {
		t.Fatalf("replace = %v", err)
	}
	canvas.Nodes[0].OutputOrdinal = new(4)
	if _, err := service.AttachStudioCanvas(
		ctx,
		principal,
		fixture.RunA,
		canvas,
	); !errors.Is(
		err,
		ErrDeliverableInvalidInput,
	) {
		t.Fatalf("bad ordinal = %v", err)
	}
	canvas.Nodes[0].OutputOrdinal = nil
	other := principal
	other.CompanyID = fixture.CompanyB
	if _, err := service.AttachStudioCanvas(ctx, other, fixture.RunA, canvas); err == nil {
		t.Fatal("cross-company accepted")
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET task_id=NULL WHERE id=$1`, fixture.RunA)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE conversations SET kind='direct',task_id=NULL,created_by_user_id=$2 WHERE id=$1`,
		fixture.ConversationA,
		fixture.UserB,
	)
	if _, err := service.AttachStudioCanvas(
		ctx,
		principal,
		fixture.RunA,
		canvas,
	); !errors.Is(
		err,
		ErrDeliverableConflict,
	) {
		t.Fatalf("private conversation = %v", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		fixture.CompanyA,
		fixture.UserA,
	)
	if _, err := service.AttachStudioCanvas(
		ctx,
		principal,
		fixture.RunA,
		canvas,
	); !errors.Is(
		err,
		ErrDeliverableForbidden,
	) {
		t.Fatalf("revoked = %v", err)
	}
}
