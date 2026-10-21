//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestActivitySchemaVersionMigrationCopiesTheEnvelopeVersion(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	runner, err := newMigrationRunner(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Migrate(47); err != nil {
		t.Fatal(err)
	}
	if sourceErr, databaseErr := runner.Close(); sourceErr != nil || databaseErr != nil {
		t.Fatalf("close old migration: %v, %v", sourceErr, databaseErr)
	}
	companyID := dbtest.NewUUID(t)
	// Only the envelope's own key counts; a legacy payload that happens to
	// carry schemaVersion 1 is still evidence.
	details := map[string]string{
		"snapshot": `{"schemaVersion":1,"before":null,"after":{},"context":{}}`,
		"evidence": `{"schemaVersion":0,"payload":{"schemaVersion":1}}`,
		"bare":     `{}`,
	}
	ids := make(map[string]uuid.UUID, len(details))
	for name, document := range details {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO activity_events (company_id, action, entity_type, entity_id, details)
			VALUES ($1, 'task.updated', 'task', $2, $3::jsonb)
			RETURNING id`, companyID, dbtest.NewUUID(t).String(), document).Scan(&id); err != nil {
			t.Fatalf("insert %s row: %v", name, err)
		}
		ids[name] = id
	}
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int32{"snapshot": 1, "evidence": 0, "bare": 0} {
		var got int32
		if err := pool.QueryRow(ctx,
			`SELECT schema_version FROM activity_events WHERE id = $1`, ids[name]).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s schema_version = %d, want %d", name, got, want)
		}
	}
	assertStatementRejected(t, ctx, pool, "23514", `
		INSERT INTO activity_events (company_id, action, entity_type, entity_id, schema_version)
		VALUES ($1, 'task.updated', 'task', $2, 2)`, companyID, dbtest.NewUUID(t).String())

	// Rolling back drops only the copy; every envelope keeps its version.
	runner, err = newMigrationRunner(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Migrate(47); err != nil {
		t.Fatal(err)
	}
	if sourceErr, databaseErr := runner.Close(); sourceErr != nil || databaseErr != nil {
		t.Fatalf("close rollback migration: %v, %v", sourceErr, databaseErr)
	}
	var columns, snapshots int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'activity_events' AND column_name = 'schema_version'
	`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM activity_events WHERE id = $1 AND details->>'schemaVersion' = '1'`,
		ids["snapshot"]).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if columns != 0 || snapshots != 1 {
		t.Fatalf(
			"after rollback: schema_version columns = %d, snapshot envelopes = %d; want 0 and 1",
			columns,
			snapshots,
		)
	}
}

// 000097: Activity rows written before the migration read on with no
// person and no Run; afterwards only an Agent acts for a person, and a Run is
// named only with that person. A Task may name its person and the Agent that
// created it, and its chat Agent counts the creating Agent only when no
// person created it. Rolling back keeps every row and every Task: a Run's row
// keeps its Agent, and a Task with both creators keeps its person alone.
func TestActivityOnBehalfOfMigrationKeepsOlderRowsAndRollsBack(t *testing.T) {
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
	migrateTo(96)
	fixture := dbtest.SeedFixture(t, ctx, pool)
	creator := dbtest.NewUUID(t)
	dbtest.SeedAgent(t, ctx, pool, creator, fixture.CompanyA, "Eli", "codex", &fixture.AgentA)
	var older uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO activity_events (company_id, actor_type, actor_id, action, entity_type, entity_id)
		VALUES ($1, 'agent', $2, 'question.asked', 'question', $3) RETURNING id`,
		fixture.CompanyA, fixture.AgentA.String(), dbtest.NewUUID(t).String()).Scan(&older); err != nil {
		t.Fatalf("write an Agent row before 000097: %v", err)
	}

	migrateTo(97)
	var onBehalfOf, run uuid.NullUUID
	if err := pool.QueryRow(ctx, `SELECT on_behalf_of_user_id, run_id FROM activity_events WHERE id = $1`, older).
		Scan(&onBehalfOf, &run); err != nil || onBehalfOf.Valid || run.Valid {
		t.Fatalf("older row reads person %v and Run %v (%v); want neither", onBehalfOf, run, err)
	}
	insert := `INSERT INTO activity_events
		(company_id, actor_type, actor_id, action, entity_type, entity_id, on_behalf_of_user_id, run_id)
		VALUES ($1, $2, $3, 'question.asked', 'question', $4, $5, $6) RETURNING id`
	assertStatementRejected(t, ctx, pool, "23514", insert,
		fixture.CompanyA, "user", fixture.UserA.String(), dbtest.NewUUID(t).String(), fixture.UserA, nil)
	assertStatementRejected(t, ctx, pool, "23514", insert,
		fixture.CompanyA, "agent", fixture.AgentA.String(), dbtest.NewUUID(t).String(), nil, fixture.RunA)
	var byRun uuid.UUID
	if err := pool.QueryRow(ctx, insert, fixture.CompanyA, "agent", fixture.AgentA.String(),
		dbtest.NewUUID(t).String(), fixture.UserA, fixture.RunA).Scan(&byRun); err != nil {
		t.Fatalf("write a Run's row: %v", err)
	}

	// task_a becomes the owner's own work that Eli's Run created.
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks SET executed_by_agent_id = NULL, creator_agent_id = $2 WHERE id = $1`, fixture.TaskA, creator)
	chatAgent := func() uuid.UUID {
		t.Helper()
		var agentID uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT agent_id FROM task_run_agents WHERE task_id = $1`, fixture.TaskA).
			Scan(&agentID); err != nil {
			t.Fatal(err)
		}
		return agentID
	}
	if got := chatAgent(); got != fixture.AgentA {
		t.Fatalf(
			"a person's Task created by Eli's Run chats with %s; want the root Agent %s",
			got,
			fixture.AgentA,
		)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET creator_user_id = NULL WHERE id = $1`, fixture.TaskA)
	if got := chatAgent(); got != creator {
		t.Fatalf("a Task only Eli created chats with %s; want Eli %s", got, creator)
	}
	assertStatementRejected(t, ctx, pool, "23514",
		`UPDATE tasks SET creator_agent_id = NULL WHERE id = $1`, fixture.TaskA)
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET creator_user_id = $2 WHERE id = $1`,
		fixture.TaskA, fixture.UserA)

	migrateTo(96)
	var columns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'activity_events'
		  AND column_name IN ('on_behalf_of_user_id', 'run_id')`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("after rollback: %d attribution columns (%v); want none", columns, err)
	}
	var actorType, actorID string
	if err := pool.QueryRow(ctx, `SELECT actor_type, actor_id FROM activity_events WHERE id = $1`, byRun).
		Scan(&actorType, &actorID); err != nil || actorType != "agent" || actorID != fixture.AgentA.String() {
		t.Fatalf("after rollback the Run's row reads %s %s (%v); want its Agent", actorType, actorID, err)
	}
	var creatorUser, creatorAgent uuid.NullUUID
	if err := pool.QueryRow(
		ctx, `SELECT creator_user_id, creator_agent_id FROM tasks WHERE id = $1`, fixture.TaskA,
	).Scan(&creatorUser, &creatorAgent); err != nil ||
		creatorUser != (uuid.NullUUID{UUID: fixture.UserA, Valid: true}) || creatorAgent.Valid {
		t.Fatalf(
			"after rollback task_a creators are %v and %v (%v); want the owner alone",
			creatorUser,
			creatorAgent,
			err,
		)
	}
	assertStatementRejected(t, ctx, pool, "23514",
		`UPDATE tasks SET creator_agent_id = $2 WHERE id = $1`, fixture.TaskA, creator)
	if got := chatAgent(); got != fixture.AgentA {
		t.Fatalf("after rollback task_a chats with %s; want the root Agent %s", got, fixture.AgentA)
	}
}
