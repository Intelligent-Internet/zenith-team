//go:build integration

package commerce

import (
	"context"
	"errors"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestResolveProvisioningIntentCommitDistinguishesAbsentWriteFromLostAuthority(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	sessionID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO auth_sessions (id, user_id, token_prefix, token_hash, expires_at)
		VALUES ($1, $2, 'commerce-recovery', $3, clock_timestamp() + interval '1 hour')`,
		sessionID, fixture.UserA, []byte("commerce-recovery-session"))
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("db.NewStore() error = %v", err)
	}
	service := &DefaultCommerceService{store: store}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, SessionID: sessionID, AuthKind: auth.TokenKindSession,
		CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	proposed := newAccountProvisioningRequest(
		dbtest.NewUUID(t),
		fixture.CompanyA,
		dbtest.NewUUID(t),
		EnvironmentSandbox,
		fixture.UserA,
	)

	if _, _, _, err := service.resolveProvisioningIntentCommit(
		ctx,
		principal,
		EnvironmentSandbox,
		proposed,
	); !errors.Is(err, ErrAccountRecovery) || errors.Is(err, ErrForbidden) {
		t.Fatalf("resolveProvisioningIntentCommit(absent write) error = %v, want recovery", err)
	}

	dbtest.MustExec(t, ctx, pool, `
		UPDATE company_members
		SET status='revoked', updated_at=clock_timestamp()
		WHERE company_id=$1 AND user_id=$2`, fixture.CompanyA, fixture.UserA)
	if _, _, _, err := service.resolveProvisioningIntentCommit(
		ctx,
		principal,
		EnvironmentSandbox,
		proposed,
	); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("resolveProvisioningIntentCommit(revoked authority) error = %v, want forbidden", err)
	}
}
