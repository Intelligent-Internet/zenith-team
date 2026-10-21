//go:build integration

package db

import (
	"testing"

	"github.com/golang-migrate/migrate/v4"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestHuddleNotesDefaultsPreserveExplicitPolicy(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := runMigration(
		ctx,
		pool,
		"before notes defaults",
		func(m *migrate.Migrate) error { return m.Migrate(120) },
	); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO huddle_notes_policies(company_id, revision) VALUES($1,1),($2,2)`,
		f.CompanyA,
		f.CompanyB,
	)
	for _, version := range []uint{121, 120, 121} {
		if err := runMigration(
			ctx,
			pool,
			"notes defaults",
			func(m *migrate.Migrate) error { return m.Migrate(version) },
		); err != nil {
			t.Fatal(err)
		}
		var enabled bool
		var seconds, revision int64
		if err := pool.QueryRow(ctx, `SELECT enabled,monthly_seconds,revision FROM huddle_notes_policies WHERE company_id=$1`, f.CompanyA).
			Scan(&enabled, &seconds, &revision); err != nil {
			t.Fatal(err)
		}
		if !enabled || seconds != 360000 || revision != 2 {
			t.Fatalf("untouched policy = %v, %d, %d; want true, 360000, 2", enabled, seconds, revision)
		}
		if err := pool.QueryRow(ctx, `SELECT enabled,monthly_seconds,revision FROM huddle_notes_policies WHERE company_id=$1`, f.CompanyB).
			Scan(&enabled, &seconds, &revision); err != nil {
			t.Fatal(err)
		}
		if enabled || seconds != 0 || revision != 2 {
			t.Fatalf("explicit policy = %v, %d, %d; want false, 0, 2", enabled, seconds, revision)
		}
	}
}
