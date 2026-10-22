//go:build integration

package execution

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestLiveWhiteboardLibraryHasNoRunDelivery(t *testing.T) {
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
	owner := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	member := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleMember}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	var ids []uuid.UUID
	for _, title := range []string{"First board", "Second board"} {
		if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			id, err := canvas.CreateWhiteboardInTx(ctx, q, owner, f.DirectConversationA, title)
			ids = append(ids, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	var outputCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outputs WHERE id=ANY($1)`, ids).
		Scan(&outputCount); err != nil ||
		outputCount != 0 {
		t.Fatalf("Whiteboard Output rows = %d, %v; want zero", outputCount, err)
	}
	page, err := service.ListArtifacts(
		ctx,
		owner,
		CompanyArtifactListOptions{ArtifactListOptions: ArtifactListOptions{Limit: 1}},
	)
	if err != nil || len(page.Items) != 1 || page.Next == nil || page.Items[0].Artifact.ID != ids[1] {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	last, err := service.ListArtifacts(
		ctx,
		owner,
		CompanyArtifactListOptions{ArtifactListOptions: ArtifactListOptions{Limit: 1, Before: page.Next}},
	)
	if err != nil || len(last.Items) != 1 || last.Next != nil || last.Items[0].Artifact.ID != ids[0] {
		t.Fatalf("last page = %+v, %v", last, err)
	}
	result := last.Items[0]
	if result.Artifact.ActorType != "user" || result.Artifact.Freshness != "not_applicable" ||
		result.RunID != nil ||
		result.Deliverable != nil ||
		result.Publication != nil ||
		result.Library == nil ||
		!result.Library.CanShare {
		t.Fatalf("live work reported as a delivery: %+v", result)
	}
	hidden, err := service.ResolveArtifacts(ctx, member, ids)
	if err != nil || len(hidden) != 0 {
		t.Fatalf("private boards = %+v, %v", hidden, err)
	}
	if _, err := store.Queries().UpsertArtifactGrant(ctx, generated.UpsertArtifactGrantParams{
		CompanyID: f.CompanyA, ArtifactID: ids[0], UserID: f.UserB, ActorUserID: f.UserA, Permission: "view",
	}); err != nil {
		t.Fatal(err)
	}
	readable, err := service.ResolveArtifacts(ctx, member, ids)
	if err != nil || len(readable) != 1 || readable[0].Artifact.ID != ids[0] || readable[0].Library.CanShare ||
		readable[0].Library.Source.Type != "restricted" {
		t.Fatalf("granted board = %+v, %v", readable, err)
	}
	chat, err := service.ListConversationArtifacts(
		ctx,
		owner,
		f.DirectConversationA,
		ArtifactListOptions{Limit: 10},
	)
	if err != nil || len(chat.Items) != 2 {
		t.Fatalf("chat boards = %+v, %v", chat, err)
	}
	for _, item := range chat.Items {
		if item.RunID != nil || item.Artifact.Type != "whiteboard" {
			t.Fatalf("chat live work = %+v", item)
		}
	}
}
