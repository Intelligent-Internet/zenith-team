//go:build integration

package db

import (
	"testing"

	"github.com/golang-migrate/migrate/v4"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestChatModelSettingsMigrationPreservesHistory(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := runMigration(
		ctx,
		pool,
		"previous main",
		func(m *migrate.Migrate) error { return m.Migrate(90) },
	); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE agents SET model='new-default', thinking_level='high' WHERE id=$1`,
		f.AgentA,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO runs(company_id,conversation_id,agent_id,runtime_id,run_type,status,context)
VALUES($1,$2,$3,$4,'direct_chat','completed','{"agent":{"model":"old-model","thinkingLevel":"low"}}')`,
		f.CompanyA,
		f.DirectConversationA,
		f.AgentA,
		f.RuntimeA,
	)
	for _, version := range []uint{91, 90, 91} {
		if err := runMigration(
			ctx,
			pool,
			"model settings",
			func(m *migrate.Migrate) error { return m.Migrate(version) },
		); err != nil {
			t.Fatal(err)
		}
		if version == 90 {
			continue
		}
		var model, thinking string
		if err := pool.QueryRow(ctx, `SELECT model,thinking_level FROM conversation_model_settings WHERE conversation_id=$1`, f.DirectConversationA).
			Scan(&model, &thinking); err != nil {
			t.Fatal(err)
		}
		if model != "old-model" || thinking != "low" {
			t.Fatalf("backfill = %s/%s", model, thinking)
		}
		if err := pool.QueryRow(ctx, `SELECT model,thinking_level FROM conversation_model_settings WHERE conversation_id=$1`, f.ConversationA).
			Scan(&model, &thinking); err != nil {
			t.Fatal(err)
		}
		if model != "new-default" || thinking != "high" {
			t.Fatalf("unused chat defaults = %s/%s", model, thinking)
		}
	}
}
