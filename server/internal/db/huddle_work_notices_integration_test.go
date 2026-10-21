//go:build integration

package db

import (
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestHuddleWorkNoticeMigrationPreservesReceipts(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := runMigration(
		ctx,
		pool,
		"before work notices",
		func(m *migrate.Migrate) error { return m.Migrate(81) },
	); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	var huddleID, proposalID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO huddles(company_id,source_conversation_id,chat_conversation_id,started_by_user_id,request_key,deadline_at) VALUES($1,$2,$2,$3,gen_random_uuid(),now()+interval '1 hour') RETURNING id`, f.CompanyA, f.ConversationA, f.UserA).
		Scan(&huddleID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO huddle_work_proposals(company_id,huddle_id,invitation_revision,created_by_agent_id,agent_name,project_title,request_key,request_hash,title,brief,expires_at,state,submitted_task_id,submitted_run_id)
	VALUES($1,$2,1,$3,'Eli','Project','migration','test','Preserved proposal','Reviewed brief',now()+interval '1 hour','submitted',$4,$5) RETURNING id`, f.CompanyA, huddleID, f.AgentA, f.TaskA, f.RunA).
		Scan(&proposalID); err != nil {
		t.Fatal(err)
	}
	for _, version := range []uint{82, 81, 82} {
		if err := runMigration(
			ctx,
			pool,
			"work notices migration",
			func(m *migrate.Migrate) error { return m.Migrate(version) },
		); err != nil {
			t.Fatal(err)
		}
		var preserved bool
		if err := pool.QueryRow(ctx, `SELECT state='submitted' AND title='Preserved proposal' AND submitted_task_id=$2 AND submitted_run_id=$3 FROM huddle_work_proposals WHERE id=$1`, proposalID, f.TaskA, f.RunA).
			Scan(&preserved); err != nil ||
			!preserved {
			t.Fatalf("version %d receipt preserved = %v, %v", version, preserved, err)
		}
		if version == 82 {
			var count int
			if err := pool.QueryRow(ctx, `SELECT cardinality(voice_notice_keys) FROM huddle_work_proposals WHERE id=$1`, proposalID).
				Scan(&count); err != nil ||
				count != 0 {
				t.Fatalf("new checkpoints = %d, %v", count, err)
			}
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`UPDATE huddle_work_proposals SET voice_notice_keys=ARRAY['claimed'] WHERE id=$1`,
				proposalID,
			)
		}
	}
}
