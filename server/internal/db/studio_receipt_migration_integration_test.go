//go:build integration

package db

import (
	"testing"

	"github.com/golang-migrate/migrate/v4"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestStudioReceiptUpgradeFrom36PreservesLegacyBindings(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := runMigration(ctx, pool, "prepare version 36", func(runner *migrate.Migrate) error {
		return runner.Migrate(36)
	}); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO conversation_studio_projects
		(company_id, conversation_id, project_id, title, description)
		VALUES ($1,$2,gen_random_uuid(),'Existing bound project',repeat('b',3000)),
		       ($1,$3,NULL,'Legacy pending reservation',repeat('p',3000))`,
		f.CompanyA, f.ConversationA, f.DirectConversationA)
	snapshot := func() string {
		t.Helper()
		var rows string
		if err := pool.QueryRow(ctx, `SELECT jsonb_agg(
			jsonb_build_object('id',id,'companyId',company_id,'conversationId',conversation_id,
			'projectId',project_id,'title',title,'description',description,'createdAt',created_at)
			ORDER BY id)::text FROM conversation_studio_projects`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	want := snapshot()
	for _, step := range []struct {
		name    string
		version uint
	}{
		{"upgrade from 36", 58},
		{"rollback receipt schema only", 57},
		{"reapply receipt schema", 58},
	} {
		if err := runMigration(ctx, pool, step.name, func(runner *migrate.Migrate) error {
			return runner.Migrate(step.version)
		}); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got := snapshot(); got != want {
			t.Fatalf(
				"%s changed legacy binding identity, project, title, description or timestamp",
				step.name,
			)
		}
		if step.version == 58 {
			var legacy, pending int
			if err := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE project_id IS NULL)
				FROM conversation_studio_projects WHERE api_origin='https://api-agent.ii.inc'
				AND creation_protocol='legacy_marker' AND binding_revision=1
				AND creation_account_id IS NULL AND creation_namespace IS NULL
				AND creation_request_hash IS NULL AND created_by_user_id IS NULL`).Scan(&legacy, &pending); err != nil {
				t.Fatal(err)
			}
			if legacy != 2 || pending != 1 {
				t.Fatalf(
					"%s: legacy=%d pending=%d, want 2 and 1 with untouched recovery identity",
					step.name,
					legacy,
					pending,
				)
			}
		}
	}
	// A localhost binding must never be silently folded back into the legacy
	// production-only schema, even when no receipt has yet been written.
	dbtest.MustExec(t, ctx, pool, `UPDATE conversation_studio_projects
		SET api_origin='http://localhost:8000' WHERE project_id IS NULL`)
	if err := runMigration(ctx, pool, "refuse origin-discarding rollback", func(runner *migrate.Migrate) error {
		return runner.Migrate(57)
	}); err == nil {
		t.Fatal("rollback discarded a nondefault origin")
	}
	if got := snapshot(); got != want {
		t.Fatal("refused rollback changed legacy binding content")
	}
}
