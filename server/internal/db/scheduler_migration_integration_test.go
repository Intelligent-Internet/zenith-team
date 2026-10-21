//go:build integration

package db

import (
	"context"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestSchedulerUpgradePreservesVersion108History(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := runMigration(
		ctx,
		pool,
		"version 108",
		func(m *migrate.Migrate) error { return m.Migrate(108) },
	); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	scheduleID, conversationID, firingID, messageID := dbtest.NewUUID(
		t,
	), dbtest.NewUUID(
		t,
	), dbtest.NewUUID(
		t,
	), dbtest.NewUUID(
		t,
	)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	execute := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	execute(
		`INSERT INTO conversations(id,company_id,kind,schedule_id,agent_id,created_by_user_id,title) VALUES($1,$2,'schedule',$3,$4,$5,'Old schedule')`,
		conversationID,
		f.CompanyA,
		scheduleID,
		f.AgentA,
		f.UserA,
	)
	execute(
		`INSERT INTO schedules(id,company_id,conversation_id,creator_user_id,request_key,agent_id,reviewed_provider,name,instructions,trigger_type,cron_expression,timezone,next_occurrence_at,status,preset_key) VALUES($1,$2,$3,$4,gen_random_uuid(),$5,'codex','Old schedule','Preserve my instructions','cron','0 9 * * *','UTC',now()+interval '1 day','active','daily_brief_v1')`,
		scheduleID,
		f.CompanyA,
		conversationID,
		f.UserA,
		f.AgentA,
	)
	var parent, child, grandchild uuid.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO runs(company_id,conversation_id,agent_id,runtime_id,execution_authorized_by_user_id,run_type,status,connector_selection_mode,context) VALUES($1,$2,$3,$4,$5,'schedule','failed','exact','{"saved":"old payload"}') RETURNING id`, f.CompanyA, conversationID, f.AgentA, f.RuntimeA, f.UserA).
		Scan(&parent); err != nil {
		t.Fatal(err)
	}
	for index, destination := range []*uuid.UUID{&child, &grandchild} {
		previous := parent
		if index == 1 {
			previous = child
		}
		if err := tx.QueryRow(ctx, `INSERT INTO runs(company_id,conversation_id,agent_id,runtime_id,execution_authorized_by_user_id,run_type,status,connector_selection_mode,attempt,parent_run_id,context) VALUES($1,$2,$3,$4,$5,'schedule','failed','exact',$6,$7,'{"saved":"old retry payload"}') RETURNING id`, f.CompanyA, conversationID, f.AgentA, f.RuntimeA, f.UserA, index+2, previous).
			Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	execute(
		`INSERT INTO conversation_items(id,company_id,conversation_id,run_id,seq,type,author_kind,author_id,content) VALUES($1,$2,$3,$4,1,'user_message','user',$5,'Preserve my prompt')`,
		messageID,
		f.CompanyA,
		conversationID,
		parent,
		f.UserA,
	)
	execute(
		`INSERT INTO schedule_firings(id,company_id,schedule_id,schedule_version,conversation_id,message_id,run_id,occurrence_at,trigger,disposition) VALUES($1,$2,$3,1,$4,$5,$6,now()-interval '1 day','scheduled','materialized')`,
		firingID,
		f.CompanyA,
		scheduleID,
		conversationID,
		messageID,
		parent,
	)
	execute(`UPDATE runs SET schedule_firing_id=$1 WHERE id=$2`, firingID, parent)
	execute(`WITH imported AS (
        INSERT INTO skill_imports(company_id, agent_id, created_by_user_id, source_type, edited)
        VALUES($1, $2, $3, 'markdown', true) RETURNING id
    ), version AS (
        INSERT INTO skill_versions(company_id, import_id, name, description, source_path, content_hash, size_bytes, file_count, manifest)
        SELECT $1, id, 'preserved-skill', 'Preserve this skill', 'SKILL.md', repeat('a', 64), 5, 1, '[]'::jsonb
        FROM imported RETURNING id
    ), attached AS (
        INSERT INTO agent_skills(company_id, agent_id, version_id, name)
        SELECT $1, $2, id, 'preserved-skill' FROM version
    ) INSERT INTO run_skills(company_id, run_id, version_id)
      SELECT $1, $4, id FROM version`, f.CompanyA, f.AgentA, f.UserA, parent)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
   'schedules',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM schedules s),
   'firings',(SELECT jsonb_agg(to_jsonb(f)-'missed_count' ORDER BY id) FROM schedule_firings f),
   'runs',(SELECT jsonb_agg(to_jsonb(r)-'schedule_firing_id'-'task_scheduled' ORDER BY id) FROM runs r),
   'items',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM conversation_items i),
   'skillImports',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM skill_imports i),
   'skillVersions',(SELECT jsonb_agg(to_jsonb(v) ORDER BY id) FROM skill_versions v),
   'agentSkills',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM agent_skills s),
   'runSkills',(SELECT jsonb_agg(to_jsonb(s) ORDER BY run_id,version_id) FROM run_skills s))::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	if err := runMigration(
		ctx,
		pool,
		"scheduler upgrade",
		func(m *migrate.Migrate) error { return m.Migrate(109) },
	); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(); got != before {
		t.Fatal("upgrade altered existing history beyond recovering retry links")
	}
	var linked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE schedule_firing_id=$1`, firingID).
		Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked != 3 {
		t.Fatalf("linked attempts = %d, want 3", linked)
	}
	err = runMigration(
		ctx,
		pool,
		"refuse lossy downgrade",
		func(m *migrate.Migrate) error { return m.Migrate(108) },
	)
	if err == nil || !strings.Contains(err.Error(), "discard history") {
		t.Fatalf("downgrade did not protect linked retries: %v", err)
	}
	if got := snapshot(); got != before {
		t.Fatal("rejected downgrade altered history")
	}
}
