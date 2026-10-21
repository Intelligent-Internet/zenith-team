//go:build integration

package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestTaskPlanSchemaUsesDirectTaskRouting(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}

	for _, table := range []string{"goals", "missions"} {
		assertTableDoesNotExist(t, ctx, pool, table)
	}

	for _, column := range []string{"goal_id", "department_agent_id", "execution_type"} {
		var exists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = 'tasks' AND column_name = $1
			)`, column).Scan(&exists); err != nil {
			t.Fatalf("read retired Task column %s: %v", column, err)
		}
		if exists {
			t.Errorf("tasks.%s exists, want absent", column)
		}
	}

	for _, column := range []string{"milestone_type"} {
		var nullable string
		if err := pool.QueryRow(ctx, `
			SELECT is_nullable
			FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'tasks' AND column_name = $1`, column).Scan(&nullable); err != nil {
			t.Fatalf("read tasks.%s nullability: %v", column, err)
		}
		if nullable != "NO" {
			t.Errorf("tasks.%s is_nullable = %q, want NO", column, nullable)
		}
	}
}

func TestTaskPlanBaselineSchemaShapeAndIndexes(t *testing.T) {
	upSQL := normalizedBaselineSQL(t, "000002_tenancy.up.sql", "000003_agents.up.sql", "000004_work.up.sql")
	for _, forbidden := range []string{"create table missions", "mission_id", "tasks_mission_idx"} {
		if strings.Contains(upSQL, forbidden) {
			t.Errorf("baseline migration still contains %q", forbidden)
		}
	}
	for _, fragment := range []string{
		"milestone_type text not null",
		"on tasks using btree (company_id, milestone_type, number desc)",
	} {
		if !strings.Contains(upSQL, fragment) {
			t.Errorf("baseline migration is missing %q", fragment)
		}
	}
}

func TestGoalRemovalMigrationPreservesTaskGraphAndHistory(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := runMigration(ctx, pool, "migrate to version 15", func(runner *migrate.Migrate) error {
		return runner.Migrate(15)
	}); err != nil {
		t.Fatalf("migrate to version 15: %v", err)
	}

	companyID, agentID, goalID := dbtest.NewUUID(t), dbtest.NewUUID(t), dbtest.NewUUID(t)
	runtimeID := dbtest.NewUUID(t)
	taskIDs := []uuid.UUID{dbtest.NewUUID(t), dbtest.NewUUID(t)}
	conversationIDs := []uuid.UUID{dbtest.NewUUID(t), dbtest.NewUUID(t)}
	activityID := dbtest.NewUUID(t)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO companies (id, name, slug, task_prefix)
			VALUES ($1, 'Existing Company', 'retired-plan-parent', 'EXT')`, companyID)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO runtimes (id, company_id, daemon_id, name, provider)
			VALUES ($1, $2, 'existing-daemon', 'Existing Runtime', 'codex')`, runtimeID, companyID)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO agents (id, company_id, runtime_id, name, title, department_key)
			VALUES ($1, $2, $3, 'Engineering', 'Engineering', 'engineering')`, agentID, companyID, runtimeID)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO goals (id, company_id, key, title, creator_agent_id)
			VALUES ($1, $2, 'existing_parent', 'Existing Parent', $3)`, goalID, companyID, agentID)
		for i, taskID := range taskIDs {
			dbtest.MustExecTx(t, ctx, tx, `
				INSERT INTO conversations (id, company_id, kind, task_id, title)
				VALUES ($1, $2, 'task', $3, 'Existing Task')`, conversationIDs[i], companyID, taskID)
			dbtest.MustExecTx(t, ctx, tx, `
				INSERT INTO tasks (
					id, company_id, conversation_id, goal_id, department_agent_id,
					milestone_type, number, key, title, description,
					assignee_agent_id, creator_agent_id
				) VALUES ($1, $2, $3, $4, $5, 'product', $6, $7, 'Existing Task',
					'Preserve authored work', $5, $5)`,
				taskID, companyID, conversationIDs[i], goalID, agentID, i+1, taskID.String())
		}
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO task_dependencies (company_id, dependent_task_id, prerequisite_task_id, created_by_agent_id)
			VALUES ($1, $2, $3, $4)`, companyID, taskIDs[1], taskIDs[0], agentID)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO activity_events (id, company_id, action, entity_type, entity_id, details, visibility)
			VALUES ($1, $2, 'goal.created', 'goal', $3,
				jsonb_build_object('schemaVersion', 0, 'payload', jsonb_build_object('goalId', $3::text)), 'company')`,
			activityID, companyID, goalID.String())
	})

	if err := runMigration(
		ctx,
		pool,
		"migrate to version 17",
		func(runner *migrate.Migrate) error { return runner.Migrate(17) },
	); err != nil {
		t.Fatalf("upgrade from version 15: %v", err)
	}
	assertTableDoesNotExist(t, ctx, pool, "goals")
	for i, taskID := range taskIDs {
		var conversationID, assigneeID uuid.UUID
		var description string
		if err := pool.QueryRow(ctx, `
			SELECT tasks.conversation_id, tasks.assignee_agent_id, tasks.description
			FROM tasks JOIN conversations ON conversations.id = tasks.conversation_id AND conversations.task_id = tasks.id
			WHERE tasks.id = $1`, taskID).Scan(&conversationID, &assigneeID, &description); err != nil {
			t.Fatalf("read preserved Task: %v", err)
		}
		if conversationID != conversationIDs[i] || assigneeID != agentID ||
			description != "Preserve authored work" {
			t.Errorf("Task changed during upgrade: conversation = %s, assignee = %s, description = %q",
				conversationID, assigneeID, description)
		}
		var stateHasGoal bool
		if err := pool.QueryRow(ctx, `
			SELECT state ? 'goalId' FROM activity_subject_states
			WHERE entity_type = 'task' AND entity_id = $1`, taskID).Scan(&stateHasGoal); err != nil {
			t.Fatalf("read current Task activity state: %v", err)
		}
		if stateHasGoal {
			t.Error("current Task activity state includes goalId")
		}
	}
	var dependencies int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM task_dependencies
		WHERE company_id = $1 AND dependent_task_id = $2 AND prerequisite_task_id = $3`,
		companyID, taskIDs[1], taskIDs[0]).Scan(&dependencies); err != nil {
		t.Fatalf("read preserved dependency: %v", err)
	}
	if dependencies != 1 {
		t.Errorf("preserved dependencies = %d, want 1", dependencies)
	}
	var historicalGoalID string
	if err := pool.QueryRow(ctx, `
		SELECT details->'payload'->>'goalId' FROM activity_events WHERE id = $1`, activityID).Scan(&historicalGoalID); err != nil {
		t.Fatalf("read historical evidence: %v", err)
	}
	if historicalGoalID != goalID.String() {
		t.Errorf("historical attribution = %q, want %q", historicalGoalID, goalID.String())
	}
}
