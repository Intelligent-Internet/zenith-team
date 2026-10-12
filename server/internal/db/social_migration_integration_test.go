//go:build integration

package db

import (
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestSocialMigrationPreservesMainData(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := runMigration(ctx, pool, "prepare existing Company", func(m *migrate.Migrate) error {
		return m.Migrate(31)
	}); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	dbtest.SeedDiscussionChannel(t, ctx, pool, f.CompanyA, f.UserA, []uuid.UUID{f.AgentA})
	dbtest.MustExec(t, ctx, pool, `WITH personal AS (
		INSERT INTO calendars(type,user_id) VALUES('personal',$1) RETURNING id
	) INSERT INTO calendar_events(user_id,calendar_id,type,title,color,start_local,end_local,time_zone)
		SELECT $1,id,'event','Existing event','sage','2026-09-22 09:00','2026-09-22 10:00','Asia/Ho_Chi_Minh'
		FROM personal`, f.UserA)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO inbox_items
		(company_id,recipient_user_id,kind,entity_type,entity_id,title,body,dedupe_key)
		VALUES($1,$2,'task_assigned','task',$3,'Existing assignment','Keep this notice','before-social')`,
		f.CompanyA, f.UserA, f.TaskA.String())
	toMain := func(m *migrate.Migrate) error { return m.Migrate(33) }
	if err := runMigration(ctx, pool, "upgrade to main", toMain); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables
		WHERE schemaname='public' AND tablename <> 'schema_migrations' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	// Compare full rows, including timestamps and revisions, for every table
	// present immediately before Social (v33). Social must not rewrite even unrelated empty tables.
	snapshot := func() map[string]string {
		t.Helper()
		result := make(map[string]string, len(tables))
		for _, table := range tables {
			var content string
			query := `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text), '[]'::jsonb)::text FROM ` +
				pgx.Identifier{
					"public",
					table,
				}.Sanitize() + ` t`
			if err := pool.QueryRow(ctx, query).Scan(&content); err != nil {
				t.Fatalf("snapshot %s: %v", table, err)
			}
			result[table] = content
		}
		return result
	}
	want := snapshot()
	toSocial := func(m *migrate.Migrate) error { return m.Migrate(34) }
	for _, step := range []struct {
		name       string
		direction  func(*migrate.Migrate) error
		wantSocial bool
	}{
		{"upgrade Social", toSocial, true},
		{"rollback Social", toMain, false},
		{"reapply Social", toSocial, true},
	} {
		if err := runMigration(ctx, pool, step.name, step.direction); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		for table, content := range snapshot() {
			if content != want[table] {
				t.Errorf("%s changed existing rows in %s", step.name, table)
			}
		}
		for _, table := range []string{"social_connections", "social_targets", "social_oauth_flows", "social_posts"} {
			var exists bool
			if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+table).
				Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if exists != step.wantSocial {
				t.Errorf("%s: %s exists = %v, want %v", step.name, table, exists, step.wantSocial)
			}
		}
		if step.wantSocial {
			dbtest.MustExec(t, ctx, pool, `WITH connection AS (
				INSERT INTO social_connections(user_id,platform,external_id,display_name,credentials)
				VALUES($1,'x','existing-account','Account',decode('00','hex')) RETURNING id
			) INSERT INTO social_posts
				(user_id,company_id,conversation_id,run_id,connection_id,request_key,payload_hash,platform,target_name,text,status)
				SELECT $1,$2,$3,$4,id,gen_random_uuid(),decode('00','hex'),'x','Account','Posted content','published'
				FROM connection`, f.UserA, f.CompanyA, f.DirectConversationA, f.RunA)
		}
	}
}
