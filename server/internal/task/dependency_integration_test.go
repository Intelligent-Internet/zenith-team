//go:build integration

package task

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// A prerequisite crossing `done` flips every dependent's public
// dependenciesResolved projection. Each dependent gets its own task.updated
// frame with an advanced revision, so a client keeps the blocker chip and the
// Start eligibility live from frames alone. A change that does not cross
// `done` says nothing to the dependent.
func TestPrerequisiteCrossingDonePublishesDependentTaskUpdated(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus := newTestBus(t)
	var announced []events.Event
	if err := bus.SubscribeAll(func(_ context.Context, event events.Event) error {
		announced = append(announced, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	dependentID := seedDependentTask(t, ctx, pool, fixture, "dependent_task", fixture.TaskA)
	dependentUpdates := func() []Response {
		var updates []Response
		for _, event := range announced {
			if event.Name == EventUpdated && event.TaskID == dependentID {
				updates = append(updates, event.Payload.(Response))
			}
		}
		return updates
	}

	title := "Renamed prerequisite"
	if _, err := service.Update(ctx, principal, fixture.TaskA, UpdateInput{
		ExpectedRevision: 1, Title: &title,
	}); err != nil {
		t.Fatalf("Update(title) error = %v", err)
	}
	if got := dependentUpdates(); len(got) != 0 {
		t.Fatalf("a title change announced %d dependent updates, want none", len(got))
	}

	done := StatusDone
	if _, err := service.Update(ctx, principal, fixture.TaskA, UpdateInput{
		ExpectedRevision: 2, Status: &done,
	}); err != nil {
		t.Fatalf("Update(done) error = %v", err)
	}
	got := dependentUpdates()
	if len(got) != 1 || !got[0].DependenciesResolved || got[0].BlockedByCount != 1 || got[0].Revision != 2 {
		t.Fatalf("dependent updates after done = %+v, want one resolved at revision 2", got)
	}

	todo := StatusTodo
	if _, err := service.Update(ctx, principal, fixture.TaskA, UpdateInput{
		ExpectedRevision: 3, Status: &todo,
	}); err != nil {
		t.Fatalf("Update(reopen) error = %v", err)
	}
	got = dependentUpdates()
	if len(got) != 2 || got[1].DependenciesResolved || got[1].Revision != 3 {
		t.Fatalf("dependent updates after reopen = %+v, want a second, blocked, at revision 3", got)
	}
	var version int64
	if err := pool.QueryRow(ctx, `SELECT version FROM tasks WHERE id=$1`, dependentID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("dependent version = %d, want 3", version)
	}
}

// seedDependentTask inserts an agent-assigned Task with one prerequisite edge
// and returns its id.
func seedDependentTask(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	key string,
	prerequisiteID uuid.UUID,
) uuid.UUID {
	t.Helper()
	taskID := dbtest.NewUUID(t)
	conversationID := dbtest.NewUUID(t)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO conversations (id, company_id, kind, task_id, title, project_id)
			VALUES ($1, $2, 'task', $3, $4, (SELECT id FROM projects WHERE company_id=$2 AND key='work'))`,
			conversationID, fixture.CompanyA, taskID, key)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO tasks
				(project_id, id, company_id, conversation_id,
				 milestone_type, number, key, title,
				 executed_by_agent_id, assignee_user_id, creator_user_id)
			VALUES ((SELECT project_id FROM tasks WHERE company_id=$2 AND id=$6),
				$1, $2, $3, 'product',
				(SELECT max(number) + 1 FROM tasks WHERE company_id=$2), $4, $4, $5, $7, $7)`,
			taskID, fixture.CompanyA, conversationID, key, fixture.AgentA,
			prerequisiteID, fixture.UserA)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO task_dependencies
				(project_id, company_id, dependent_task_id, prerequisite_task_id, created_by_user_id)
			VALUES ((SELECT project_id FROM tasks WHERE company_id=$1 AND id=$2), $1, $2, $3, $4)`,
			fixture.CompanyA, taskID, prerequisiteID, fixture.UserA)
	})
	return taskID
}

// The dependency graph's identity rules are enforced by the database, not by
// Go: a duplicate edge, a self-edge, and an edge to a task that does not exist
// must all be rejected. These live here rather than beside the migration
// because they are task-domain invariants — the graph is what makes
// prerequisite ordering meaningful.
func TestTaskDependencyEdgesRejectDuplicatesSelfEdgesAndMissingTasks(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	dependentTaskID := dbtest.NewUUID(t)
	dependentConversationID := dbtest.NewUUID(t)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO conversations (id, company_id, kind, task_id, title, project_id)
			VALUES ($1, $2, 'task', $3, 'Dependent Task', (SELECT id FROM projects WHERE company_id=$2 AND key='work'))`,
			dependentConversationID, fixture.CompanyA, dependentTaskID)
		dbtest.MustExecTx(t, ctx, tx, `
								INSERT INTO tasks
					(project_id, id, company_id, conversation_id,
					 milestone_type, number, key, title,
					 executed_by_agent_id, assignee_user_id, creator_user_id)
				VALUES ((SELECT id FROM projects WHERE company_id=$2 AND key='work'),
					$1, $2, $3, 'product',
					2, 'dependent_task', 'Dependent Task', $4, $5, $5
				)`,
			dependentTaskID, fixture.CompanyA, dependentConversationID,
			fixture.AgentA, fixture.UserA)
	})

	t.Run("an edge is unique", func(t *testing.T) {
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO task_dependencies
				(project_id, company_id, dependent_task_id, prerequisite_task_id, created_by_user_id)
			VALUES ((SELECT project_id FROM tasks WHERE company_id=$1 AND id=$2), $1, $2, $3, $4)`,
			fixture.CompanyA, dependentTaskID, fixture.TaskA, fixture.UserA)
		dbtest.AssertStatementRejected(t, ctx, pool, "23505", `
			INSERT INTO task_dependencies
				(project_id, company_id, dependent_task_id, prerequisite_task_id, created_by_user_id)
			VALUES ((SELECT project_id FROM tasks WHERE company_id=$1 AND id=$2), $1, $2, $3, $4)`,
			fixture.CompanyA, dependentTaskID, fixture.TaskA, fixture.UserA)
	})

	t.Run("self edges are rejected", func(t *testing.T) {
		dbtest.AssertStatementRejected(t, ctx, pool, "23514", `
			INSERT INTO task_dependencies
				(project_id, company_id, dependent_task_id, prerequisite_task_id, created_by_user_id)
			VALUES ((SELECT project_id FROM tasks WHERE company_id=$1 AND id=$2), $1, $2, $2, $3)`,
			fixture.CompanyA, dependentTaskID, fixture.UserA)
	})

	t.Run("edges to missing tasks are FK-rejected", func(t *testing.T) {
		dbtest.AssertStatementRejected(t, ctx, pool, "23503", `
			INSERT INTO task_dependencies
				(project_id, company_id, dependent_task_id, prerequisite_task_id, created_by_user_id)
			VALUES ((SELECT project_id FROM tasks WHERE company_id=$1 AND id=$2), $1, $2, $3, $4)`,
			fixture.CompanyA, dependentTaskID, dbtest.NewUUID(t), fixture.UserA)
	})
}
