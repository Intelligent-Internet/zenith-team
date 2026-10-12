//go:build integration

package account

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestDeleteAccountRevokesDiscussionAndRetainsPrivateHistory(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'owner')`,
		fixture.CompanyA,
		fixture.UserB,
	)
	id := dbtest.SeedDiscussionChannel(t, ctx, pool, fixture.CompanyA, fixture.UserA, []uuid.UUID{fixture.AgentA})
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var sessionID uuid.UUID
	if err = pool.QueryRow(ctx, `INSERT INTO auth_sessions(user_id,token_prefix,token_hash,expires_at,client_metadata) VALUES($1,'c1test',decode(repeat('ab',32),'hex'),now()+interval '1 hour','{}') RETURNING id`, fixture.UserA).
		Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	service := newAccountIntegrationService(t, store, bus)
	if _, err = service.Delete(
		ctx,
		auth.UserPrincipal{UserID: fixture.UserA, SessionID: sessionID, AuthKind: auth.TokenKindSession},
		Confirmation,
	); err != nil {
		t.Fatal(err)
	}
	var revoked, archived, deleted bool
	if err = pool.QueryRow(ctx, `SELECT m.status='revoked',c.archived_at IS NOT NULL,u.deleted_at IS NOT NULL FROM conversation_members m JOIN conversations c ON c.id=m.conversation_id JOIN users u ON u.id=m.user_id WHERE c.id=$1`, id).
		Scan(&revoked, &archived, &deleted); err != nil {
		t.Fatal(err)
	}
	if !revoked || !archived || !deleted {
		t.Fatalf("account/grant/archive=%v/%v/%v", deleted, revoked, archived)
	}
}
