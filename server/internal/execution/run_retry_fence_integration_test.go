//go:build integration

package execution

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestCreateRetryRunFencesTaskExecutionOwnership(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)

	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks
		SET status = 'in_progress'
		WHERE id = $1`, fixture.TaskA)
	first := enqueueRetryFenceTaskRun(t, ctx, queries, fixture)
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'failed' WHERE id = $1`, first.ID)

	dbtest.MustExec(t, ctx, pool, `UPDATE projects SET title='Changed after enqueue',version=version+1 WHERE id=$1`, fixture.ProjectA)
	child, err := queries.CreateRetryRun(
		ctx,
		generated.CreateRetryRunParams{ParentRunID: first.ID},
	)
	if err != nil {
		t.Fatalf("create eligible Task retry: %v", err)
	}
	if !bytes.Equal(child.Context, first.Context) {
		t.Fatal("retry refreshed frozen Project context")
	}
	if child.TaskExecutionGeneration != first.TaskExecutionGeneration {
		t.Fatalf(
			"retry generation = %+v, want %+v",
			child.TaskExecutionGeneration,
			first.TaskExecutionGeneration,
		)
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM runs WHERE id = $1`, child.ID)

	second := enqueueRetryFenceTaskRun(t, ctx, queries, fixture)
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'failed' WHERE id = $1`, second.ID)

	assertRetrySuppressed(t, ctx, pool, queries, first.ID, "superseded generation")

	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks
		SET status = 'failed'
		WHERE id = $1`, fixture.TaskA)
	assertRetrySuppressed(t, ctx, pool, queries, second.ID, "terminal Task")

	// The assignee took the work into their own hands: no executing Agent,
	// no retry.
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks
		SET status = 'in_progress', executed_by_agent_id = NULL
		WHERE id = $1`, fixture.TaskA)
	assertRetrySuppressed(t, ctx, pool, queries, second.ID, "human handoff")
	// The Task moved to another member (ADR 0014): the parent's authorizer
	// is no longer the assignee, so the retry is fenced even with the
	// Agent restored.
	otherMember := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Other')`, otherMember, otherMember.String()+"@boardly.test")
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`, fixture.CompanyA, otherMember)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks
		SET status = 'in_progress', executed_by_agent_id = $2, assignee_user_id = $3
		WHERE id = $1`, fixture.TaskA, fixture.AgentA, otherMember)
	assertRetrySuppressed(t, ctx, pool, queries, second.ID, "assignee handoff")
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET assignee_user_id = $2 WHERE id = $1`, fixture.TaskA, fixture.UserA)

	prerequisiteID := dbtest.NewUUID(t)
	prerequisiteConversationID := dbtest.NewUUID(t)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO conversations (id, company_id, kind, task_id, title, project_id)
			VALUES ($1, $2, 'task', $3, 'Retry prerequisite', (SELECT id FROM projects WHERE company_id=$2 AND key='work'))`,
			prerequisiteConversationID, fixture.CompanyA, prerequisiteID)
		dbtest.MustExecTx(t, ctx, tx, `
						INSERT INTO tasks (project_id,
				id, company_id, conversation_id,
				milestone_type, number, key, title,
				executed_by_agent_id, assignee_user_id, creator_user_id
			)
			VALUES ((SELECT id FROM projects WHERE company_id=$2 AND key='work'),
				$1, $2, $3,
				'product', 99, 'retry_prerequisite', 'Retry prerequisite',
				$4, $5, $5
			)`, prerequisiteID, fixture.CompanyA, prerequisiteConversationID,
			fixture.AgentA, fixture.UserA)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO task_dependencies (project_id,
				company_id, dependent_task_id, prerequisite_task_id, created_by_user_id
			)
			VALUES ((SELECT project_id FROM tasks WHERE company_id=$1 AND id=$2), $1, $2, $3, $4)`,
			fixture.CompanyA, fixture.TaskA, prerequisiteID, fixture.UserA)
	})
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks
		SET status = 'in_progress', executed_by_agent_id = $2
		WHERE id = $1`, fixture.TaskA, fixture.AgentA)
	assertRetrySuppressed(t, ctx, pool, queries, second.ID, "unresolved dependency")

	var directParentID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs (
			company_id, agent_id, conversation_id, runtime_id, execution_authorized_by_user_id, status
		, run_type)
		VALUES ($1, $2, $3, $4, $5, 'failed', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`, fixture.CompanyA, fixture.AgentA,
		fixture.DirectConversationA, fixture.RuntimeA, fixture.UserA).Scan(&directParentID); err != nil {
		t.Fatalf("insert direct Run parent: %v", err)
	}
	if _, err := queries.CreateRetryRun(
		ctx,
		generated.CreateRetryRunParams{ParentRunID: directParentID},
	); err != nil {
		t.Fatalf("direct Run retry was fenced by Task policy: %v", err)
	}
}

func enqueueRetryFenceTaskRun(
	t *testing.T,
	ctx context.Context,
	queries *generated.Queries,
	fixture dbtest.Fixture,
) generated.Run {
	t.Helper()
	var pending []events.Event
	run, err := EnqueueRunInTx(ctx, queries, &pending, RunTrigger{
		CompanyID:          fixture.CompanyA,
		ConversationID:     fixture.ConversationA,
		AgentID:            fixture.AgentA,
		TaskID:             util.NullUUIDFromPtr(&fixture.TaskA),
		Source:             RunSourceMessage,
		AuthorizedByUserID: fixture.UserA,
	})
	if err != nil {
		t.Fatalf("enqueue Task Run: %v", err)
	}
	return run
}

func assertRetrySuppressed(
	t *testing.T,
	ctx context.Context,
	pool queryRower,
	queries *generated.Queries,
	parentID uuid.UUID,
	reason string,
) {
	t.Helper()
	type taskState struct {
		status        string
		generation    int64
		version       int64
		assigneeAgent string
		assigneeUser  string
	}
	readTaskState := func() taskState {
		t.Helper()
		var state taskState
		if err := pool.QueryRow(ctx, `
			SELECT status, execution_generation, version,
			       COALESCE(executed_by_agent_id::text, ''),
			       COALESCE(assignee_user_id::text, '')
			FROM tasks
			WHERE id = (
				SELECT task_id FROM runs WHERE id = $1
			)`, parentID).Scan(
			&state.status,
			&state.generation,
			&state.version,
			&state.assigneeAgent,
			&state.assigneeUser,
		); err != nil {
			t.Fatalf("read %s Task before retry: %v", reason, err)
		}
		return state
	}
	before := readTaskState()
	for replay := range 2 {
		if _, err := queries.CreateRetryRun(
			ctx,
			generated.CreateRetryRunParams{ParentRunID: parentID},
		); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("%s retry replay %d error = %v, want pgx.ErrNoRows", reason, replay, err)
		}
	}
	var childCount int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM runs WHERE parent_run_id = $1`,
		parentID,
	).Scan(&childCount); err != nil {
		t.Fatalf("count %s retry children: %v", reason, err)
	}
	if childCount != 0 {
		t.Fatalf("%s retry children = %d, want 0", reason, childCount)
	}
	if after := readTaskState(); after != before {
		t.Fatalf("%s retry changed Task from %+v to %+v", reason, before, after)
	}
}

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}
