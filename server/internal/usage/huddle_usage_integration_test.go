//go:build integration

package usage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestHuddleBudgetConfiguration(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seed := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewHuddleBudgetService(store)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.UserPrincipal{CompanyID: seed.CompanyA, UserID: seed.UserA, Role: auth.CompanyRoleOwner}
	got, err := service.Get(ctx, principal)
	if err != nil || got.MonthlyLimitUSDTicks != "0" || got.Revision != 0 {
		t.Fatalf("unconfigured budget = %+v, error = %v, want zero allowance", got, err)
	}
	got, err = service.Configure(
		ctx,
		principal,
		ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: 100000000000, ExpectedRevision: 0},
	)
	if err != nil || got.MonthlyLimitUSDTicks != "100000000000" || got.Revision != 1 {
		t.Fatalf("configured budget = %+v, error = %v", got, err)
	}
	if _, err := service.Configure(
		ctx,
		principal,
		ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: 1, ExpectedRevision: 0},
	); !errors.Is(
		err,
		ErrHuddleBudgetConflict,
	) {
		t.Fatalf("stale configure error = %v, want conflict", err)
	}
	got, err = service.Configure(
		ctx,
		principal,
		ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: 0, ExpectedRevision: 1},
	)
	if err != nil || got.MonthlyLimitUSDTicks != "0" || got.Revision != 2 {
		t.Fatalf("disabled budget = %+v, error = %v", got, err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE company_members SET role='member' WHERE company_id=$1 AND user_id=$2`,
		seed.CompanyA,
		seed.UserA,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Configure(
		ctx,
		principal,
		ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: 1, ExpectedRevision: 2},
	); !errors.Is(
		err,
		ErrHuddleBudgetForbidden,
	) {
		t.Fatalf("revoked role configure error = %v, want forbidden", err)
	}
}
