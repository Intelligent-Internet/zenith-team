//go:build integration

package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	internaldb "boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	db "boardly.dev/boardly/server/pkg/db/generated"
)

func startProjectAccessRunFixture(t *testing.T) (context.Context, *pgxpool.Pool, dbtest.Fixture) {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	if err := internaldb.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return ctx, pool, dbtest.SeedFixture(t, ctx, pool)
}

// revokeExecute takes project.tasks.execute from the person on the Project,
// leaving their read and write rows.
func revokeExecute(t *testing.T, ctx context.Context, pool *pgxpool.Pool, companyID, projectID, userID uuid.UUID) {
	t.Helper()
	dbtest.MustExec(t, ctx, pool, `
		DELETE FROM project_grants
		WHERE company_id=$1 AND project_id=$2 AND user_id=$3 AND permission='project.tasks.execute'`,
		companyID, projectID, userID)
}

// enqueueInRolledBackTx runs the kernel in a transaction it then rolls back,
// so one fixture serves several attempts.
func enqueueInRolledBackTx(t *testing.T, ctx context.Context, pool *pgxpool.Pool, in RunTrigger) error {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var pending []events.Event
	_, err = EnqueueRunInTx(ctx, db.New(tx), &pending, in)
	return err
}

// seedLegacyTaskChat inserts a Task chat whose Task was deleted before
// migration 000086 recorded Projects: kind task, no Task, no Project. The
// CHECK admits it only with a creation time before that migration.
func seedLegacyTaskChat(t *testing.T, ctx context.Context, pool *pgxpool.Pool, companyID uuid.UUID) uuid.UUID {
	t.Helper()
	id := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversations (id, company_id, kind, title, created_at, updated_at)
		VALUES ($1, $2, 'task', 'Detached', '2000-01-01', '2000-01-01')`, id, companyID)
	return id
}

// Every Run passes the enqueue kernel, so it is the backstop for the Task
// service's and the Task chat's own execute checks (ADR 0017): a Run in a Task
// chat needs its authorizer's project.tasks.execute on the chat's Project.
// The refusal names the Agent, never the Project, and comes before anything
// is written. A Company-visible Project gives every member the permission; a
// Task chat with no Project admits no Run; any other chat is not a Project's.
func TestEnqueueKernelRefusesATaskRunWithoutExecute(t *testing.T) {
	t.Parallel()
	ctx, pool, fixture := startProjectAccessRunFixture(t)
	taskRun := RunTrigger{
		CompanyID: fixture.CompanyA, ConversationID: fixture.ConversationA, AgentID: fixture.AgentA,
		TaskID: uuid.NullUUID{UUID: fixture.TaskA, Valid: true}, Source: RunSourceExecution,
		ActorUserID: fixture.UserA, AuthorizedByUserID: fixture.UserA,
	}
	if err := enqueueInRolledBackTx(t, ctx, pool, taskRun); err != nil {
		t.Fatalf("enqueue with execute = %v", err)
	}

	revokeExecute(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, fixture.UserA)
	err := enqueueInRolledBackTx(t, ctx, pool, taskRun)
	var refusal *ExecutePermissionError
	if !errors.As(err, &refusal) || !errors.Is(err, auth.ErrProjectPermissionRequired) {
		t.Fatalf("enqueue without execute = %v, want *ExecutePermissionError", err)
	}
	if refusal.AssigneeID != fixture.UserA || refusal.AgentName != "Agent A" {
		t.Fatalf("refusal = %+v, want the authorizer and the Agent", refusal)
	}

	// A direct chat is not a Project's: the same person's Run there is queued.
	direct := RunTrigger{
		CompanyID: fixture.CompanyA, ConversationID: fixture.DirectConversationA, AgentID: fixture.AgentA,
		Source: RunSourceMessage, TriggerPrompt: "hello", ActorUserID: fixture.UserA,
		AuthorizedByUserID: fixture.UserA,
	}
	if err := enqueueInRolledBackTx(t, ctx, pool, direct); err != nil {
		t.Fatalf("direct chat enqueue = %v", err)
	}

	// On a Company-visible Project every active member holds execute.
	dbtest.MustExec(t, ctx, pool, `UPDATE projects SET visibility='company' WHERE id=$1`, fixture.ProjectA)
	if err := enqueueInRolledBackTx(t, ctx, pool, taskRun); err != nil {
		t.Fatalf("enqueue on a Company-visible Project = %v", err)
	}

	// A Task chat with no Project runs nothing, whoever authorizes it.
	legacy := seedLegacyTaskChat(t, ctx, pool, fixture.CompanyA)
	err = enqueueInRolledBackTx(t, ctx, pool, RunTrigger{
		CompanyID: fixture.CompanyA, ConversationID: legacy, AgentID: fixture.AgentA,
		Source: RunSourceMessage, TriggerPrompt: "hello", ActorUserID: fixture.UserA,
		AuthorizedByUserID: fixture.UserA,
	})
	if !errors.As(err, &refusal) {
		t.Fatalf("enqueue in a Task chat with no Project = %v, want *ExecutePermissionError", err)
	}
}

// The claim fence repeats the check beside the binding fence (ADR 0017): a
// queued Run in a Task chat is claimable only while its authorizer holds
// project.tasks.execute on the chat's Project, and the empty-claim cache's
// candidate listing agrees. A queued Run in a Task chat with no Project is
// never claimable.
func TestClaimSkipsTaskRunsWhoseAuthorizerCannotExecute(t *testing.T) {
	t.Parallel()
	ctx, pool, fixture := startProjectAccessRunFixture(t)
	queries := db.New(pool)
	queue := func(conversationID uuid.UUID, taskID uuid.NullUUID) db.Run {
		t.Helper()
		run, err := queries.CreateRun(ctx, db.CreateRunParams{
			CompanyID: fixture.CompanyA, AgentID: fixture.AgentA, TaskID: taskID,
			ConversationID: conversationID, RuntimeID: fixture.RuntimeA,
			ExecutionAuthorizedByUserID: fixture.UserA,
			Status:                      "queued", Attempt: 1, Context: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	fence := claimCandidateParams()
	claimable := func() (uuid.UUID, bool) {
		t.Helper()
		candidates, err := queries.ListQueuedClaimCandidatesByRuntimes(ctx, claimCandidateParams(fixture.RuntimeA))
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := queries.SelectClaimableRun(ctx, db.SelectClaimableRunParams{
			AgentID: fixture.AgentA, RuntimeID: fixture.RuntimeA, TaskLaneOpen: true,
			ExecutePermission:         fence.ExecutePermission,
			ReadPermission:            fence.ReadPermission,
			CompanyDefaultPermissions: fence.CompanyDefaultPermissions,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			if len(candidates) != 0 {
				t.Fatalf("the candidate listing names %+v the claim does not admit", candidates)
			}
			return uuid.Nil, false
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(candidates) != 1 {
			t.Fatalf("the claim admits %s but the candidate listing names %+v", candidate.ID, candidates)
		}
		return candidate.ID, true
	}
	finish := func(run db.Run) {
		t.Helper()
		dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='cancelled' WHERE id=$1`, run.ID)
	}

	taskRun := queue(fixture.ConversationA, uuid.NullUUID{UUID: fixture.TaskA, Valid: true})
	if id, ok := claimable(); !ok || id != taskRun.ID {
		t.Fatalf("claimable with execute = %s %t, want %s", id, ok, taskRun.ID)
	}
	revokeExecute(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, fixture.UserA)
	if id, ok := claimable(); ok {
		t.Fatalf("claimable without execute = %s", id)
	}
	// Keyed on the chat: the Task's delete does not make the Run claimable.
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET task_id=NULL WHERE id=$1`, taskRun.ID)
	if id, ok := claimable(); ok {
		t.Fatalf("claimable once the Run lost its Task = %s", id)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE projects SET visibility='company' WHERE id=$1`, fixture.ProjectA)
	if id, ok := claimable(); !ok || id != taskRun.ID {
		t.Fatalf("claimable on a Company-visible Project = %s %t, want %s", id, ok, taskRun.ID)
	}
	finish(taskRun)

	// CreateRun writes no Run into a Task chat without its Task; this one was
	// queued before its Task was deleted, before 000086.
	var legacyRun db.Run
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id,
			execution_authorized_by_user_id, status, run_type)
		VALUES ($1, $2, $3, $4, $5, 'queued', 'task') RETURNING id`,
		fixture.CompanyA, fixture.AgentA, seedLegacyTaskChat(t, ctx, pool, fixture.CompanyA),
		fixture.RuntimeA, fixture.UserA,
	).Scan(&legacyRun.ID); err != nil {
		t.Fatal(err)
	}
	if id, ok := claimable(); ok {
		t.Fatalf("a queued Run in a Task chat with no Project is claimable: %s", id)
	}
	finish(legacyRun)

	directRun := queue(fixture.DirectConversationA, uuid.NullUUID{})
	if id, ok := claimable(); !ok || id != directRun.ID {
		t.Fatalf("direct chat Run claimable = %s %t, want %s", id, ok, directRun.ID)
	}
}
