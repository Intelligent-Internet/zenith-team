//go:build integration

package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestGeneralChannelUpgradePreservesPrivateChannels(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := runMigration(
		ctx,
		pool,
		"migrate before General",
		func(m *migrate.Migrate) error { return m.Migrate(31) },
	); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	private := dbtest.SeedDiscussionChannel(t, ctx, pool, f.CompanyA, f.UserA, []uuid.UUID{f.AgentA})
	dbtest.MustExec(t, ctx, pool, `UPDATE conversations SET title='general' WHERE id=$1`, private)
	dbtest.MustExec(t, ctx, pool, `WITH people AS (
 INSERT INTO users(email,display_name) SELECT 'general-'||n||'@example.test','Member '||n FROM generate_series(1,60) n RETURNING id
 ) INSERT INTO company_members(company_id,user_id,role) SELECT $1,id,'member' FROM people`, f.CompanyA)
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	var count, agents int
	if err := pool.QueryRow(ctx, `SELECT id FROM conversations WHERE company_id=$1 AND is_general`, f.CompanyA).
		Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id == private {
		t.Fatal("migration promoted an existing private channel")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM conversation_members WHERE conversation_id=$1 AND status='active'`, id).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 61 {
		t.Fatalf("General members = %d, want 61", count)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM conversation_agents WHERE conversation_id=$1`, id).
		Scan(&agents); err != nil {
		t.Fatal(err)
	}
	if agents != 0 {
		t.Fatalf("General agents = %d, want 0", agents)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM conversation_members WHERE conversation_id=$1`, private).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("private members = %d, want 1", count)
	}
	dbtest.AssertStatementRejected(
		t,
		ctx,
		pool,
		"23514",
		`UPDATE conversations SET title='renamed' WHERE id=$1`,
		id,
	)
	dbtest.AssertStatementRejected(
		t,
		ctx,
		pool,
		"23514",
		`UPDATE conversations SET archived_at=now() WHERE id=$1`,
		id,
	)
	dbtest.AssertStatementRejected(
		t,
		ctx,
		pool,
		"23514",
		`UPDATE conversations SET is_general=false WHERE id=$1`,
		id,
	)
}

func TestGeneralChannelDowngradePreservesRetainedChannel(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := runMigration(
		ctx,
		pool,
		"pre-General",
		func(m *migrate.Migrate) error { return m.Migrate(31) },
	); err != nil {
		t.Fatal(err)
	}
	dbtest.SeedFixture(t, ctx, pool)
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	err := runMigration(ctx, pool, "rollback General", func(m *migrate.Migrate) error { return m.Migrate(31) })
	if err == nil || !strings.Contains(err.Error(), "cannot roll back retained General") {
		t.Fatalf("downgrade=%v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM conversations WHERE is_general`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("retained General channels=%d", count)
	}
}
