//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// 000098: every session written before the migration is a person's;
// afterwards a session is a person's or a daemon's and nothing else. Rolling
// back ends every live daemon session first, because without the column it
// would read as its person's own, and leaves every other session as it was.
func TestDaemonSessionMigrationKeepsPersonSessionsAndEndsDaemonSessionsOnRollback(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	migrateTo := func(version uint) {
		t.Helper()
		runner, err := newMigrationRunner(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.Migrate(version); err != nil {
			t.Fatalf("migrate to %d: %v", version, err)
		}
		if sourceErr, databaseErr := runner.Close(); sourceErr != nil || databaseErr != nil {
			t.Fatalf("close migration to %d: %v, %v", version, sourceErr, databaseErr)
		}
	}
	migrateTo(97)
	fixture := dbtest.SeedFixture(t, ctx, pool)
	insertSession := func(prefix string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO auth_sessions (user_id, token_prefix, token_hash, expires_at)
			VALUES ($1, $2, gen_random_bytes(32), now() + interval '1 hour') RETURNING id`,
			fixture.UserA, prefix).Scan(&id); err != nil {
			t.Fatalf("insert session %s: %v", prefix, err)
		}
		return id
	}
	older := insertSession("older")

	migrateTo(98)
	client := func(id uuid.UUID) string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, `SELECT client FROM auth_sessions WHERE id = $1`, id).
			Scan(&value); err != nil {
			t.Fatalf("read client of %s: %v", id, err)
		}
		return value
	}
	if got := client(older); got != "person" {
		t.Fatalf("a session from before 000098 reads %q; want person", got)
	}
	live := insertSession("daemon-live")
	ended := insertSession("daemon-ended")
	person := insertSession("person")
	dbtest.MustExec(t, ctx, pool, `UPDATE auth_sessions SET client = 'daemon' WHERE id IN ($1, $2)`, live, ended)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE auth_sessions SET revoked_at = now(), revocation_reason = 'logout' WHERE id = $1`, ended)
	assertStatementRejected(
		t,
		ctx,
		pool,
		"23514",
		`UPDATE auth_sessions SET client = 'robot' WHERE id = $1`,
		person,
	)

	migrateTo(97)
	revocation := func(id uuid.UUID) (bool, string) {
		t.Helper()
		var revokedAt pgtype.Timestamptz
		var reason pgtype.Text
		if err := pool.QueryRow(ctx, `SELECT revoked_at, revocation_reason FROM auth_sessions WHERE id = $1`, id).
			Scan(&revokedAt, &reason); err != nil {
			t.Fatalf("read revocation of %s: %v", id, err)
		}
		return revokedAt.Valid, reason.String
	}
	if revoked, reason := revocation(live); !revoked || reason != "downgrade" {
		t.Fatalf(
			"after rollback the live daemon session reads revoked %v, %q; want revoked for downgrade",
			revoked,
			reason,
		)
	}
	if revoked, reason := revocation(ended); !revoked || reason != "logout" {
		t.Fatalf(
			"after rollback the ended daemon session reads revoked %v, %q; want its own logout",
			revoked,
			reason,
		)
	}
	for _, id := range []uuid.UUID{older, person} {
		if revoked, reason := revocation(id); revoked {
			t.Fatalf("after rollback person session %s reads revoked for %q; want live", id, reason)
		}
	}
	var columns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'auth_sessions' AND column_name = 'client'`).
		Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("after rollback: %d client columns (%v); want none", columns, err)
	}
}
