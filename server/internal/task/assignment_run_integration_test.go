//go:build integration

package task

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// seedTaskMember adds an active member of CompanyA, a colleague a Task can
// be assigned to.
func seedTaskMember(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	email string,
) uuid.UUID {
	t.Helper()
	id := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`, id, email)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		fixture.CompanyA, id)
	// A member works on the fixture Project's Tasks: they read, write and
	// run them there (ADR 0017).
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, id)
	return id
}

// seedTaskRuntime registers one online Codex machine for owner.
func seedTaskRuntime(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	daemonID string,
	owner uuid.UUID,
) uuid.UUID {
	t.Helper()
	id := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes
			(id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, $3, $3, 'codex', 'online', $4, now())`,
		id, fixture.CompanyA, daemonID, owner)
	return id
}

// assertTaskRunPrompt checks that a started Task Run posted the Task's title
// and description in its chat as the member's message, carrying the Run,
// and froze the same words, their author and the start's source in the
// Run's context — whichever way the Run was started.
func assertTaskRunPrompt(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	value Response,
	runID, author uuid.UUID,
	source execution.RunEnqueueSource,
) {
	t.Helper()
	want := value.Title + "\n---\n" + value.Description
	var content string
	var itemRun, itemAuthor uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT content, run_id, author_id FROM conversation_items
		WHERE conversation_id = $1 AND type = 'user_message'
		ORDER BY seq DESC LIMIT 1`, value.ConversationID,
	).Scan(&content, &itemRun, &itemAuthor); err != nil {
		t.Fatalf("read the Task prompt message: %v", err)
	}
	if content != want || itemRun != runID || itemAuthor != author {
		t.Fatalf("Task prompt message = %q by %s for Run %s; want %q by %s for Run %s",
			content, itemAuthor, itemRun, want, author, runID)
	}
	var prompt, triggerAuthor, frozenSource string
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(context->>'prompt', ''),
		       COALESCE(context->'trigger'->'author'->>'id', ''),
		       COALESCE(context->'executionProfile'->>'source', '')
		FROM runs WHERE id = $1`, runID,
	).Scan(&prompt, &triggerAuthor, &frozenSource); err != nil {
		t.Fatalf("read the Run context: %v", err)
	}
	if prompt != want || triggerAuthor != author.String() || frozenSource != string(source) {
		t.Fatalf("Run context prompt %q, trigger author %q, source %q; want %q, %s, %s",
			prompt, triggerAuthor, frozenSource, want, author, source)
	}
}

// startTaskHarness migrates a fresh database, seeds the fixture and returns
// the Task service with the founder's principal.
func startTaskHarness(t *testing.T) (
	context.Context, *pgxpool.Pool, dbtest.Fixture, *DefaultTaskService, auth.UserPrincipal,
) {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool,
		`UPDATE companies SET next_task_number = 2 WHERE id = $1`, fixture.CompanyA)
	service, err := NewTaskService(store, newTestBus(t), nil)
	if err != nil {
		t.Fatalf("construct Task service: %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	return ctx, pool, fixture, service, principal
}

// An assignment Run is a fresh root Run doing the Task's work, so the Task
// it belongs to is in progress from the moment the Run exists — the same
// transition a Task-Conversation message makes when its Run materializes.
// It starts only for the assignee, on their own binding of the executing
// Agent (ADR 0014).
func TestAssignmentRunsMoveTheTaskIntoProgress(t *testing.T) {
	ctx, pool, fixture, service, principal := startTaskHarness(t)
	agent := ActorRef{Kind: ActorAgent, ID: fixture.AgentA}
	countRuns := func(taskID uuid.UUID) int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM runs WHERE task_id = $1`, taskID,
		).Scan(&count); err != nil {
			t.Fatalf("count Task Runs: %v", err)
		}
		return count
	}

	t.Run("creating a Task the caller runs with an Agent starts its work", func(t *testing.T) {
		result, err := service.Create(ctx, principal, CreateInput{
			ProjectID: fixture.ProjectA,
			Key:       "assign_on_create", Title: "Assign on create", Status: StatusTodo,
			MilestoneType: MilestoneTypeProduct,
			ExecutedBy:    &agent,
		})
		if err != nil {
			t.Fatalf("Create(executedBy agent) error = %v", err)
		}
		created := result.Task
		if created.Status != StatusInProgress || result.RunID == nil {
			t.Fatalf("Create(executedBy agent) = status %q run %v, want in_progress with a Run", created.Status, result.RunID)
		}
		// The assignee defaults to the caller, and the Run is placed on
		// their binding.
		if created.Assignee.ID != principal.UserID || created.ExecutedBy.ID != agent.ID ||
			!created.Execution.AssigneeReady {
			t.Fatalf("Create(executedBy agent) = %+v, want the caller assigned and ready", created)
		}
		var runtimeID, authorizer uuid.UUID
		if err := pool.QueryRow(ctx,
			`SELECT runtime_id, execution_authorized_by_user_id FROM runs WHERE task_id = $1`, created.ID,
		).Scan(&runtimeID, &authorizer); err != nil {
			t.Fatalf("read the assignment Run: %v", err)
		}
		if runtimeID != fixture.RuntimeA || authorizer != principal.UserID {
			t.Fatalf("assignment Run = %s / %s, want the assignee's binding", runtimeID, authorizer)
		}
		// The chat opens with what the Agent was asked, as an explicit
		// start writes it; the Run keeps its own source.
		assertTaskRunPrompt(t, ctx, pool, created, *result.RunID, principal.UserID, execution.RunSourceAssign)
	})

	t.Run("assigning another member starts nothing and tells them", func(t *testing.T) {
		member := seedTaskMember(t, ctx, pool, fixture, "member@example.com")
		result, err := service.Create(ctx, principal, CreateInput{
			ProjectID: fixture.ProjectA,
			Key:       "assign_to_member", Title: "Assign to a member", Status: StatusTodo,
			MilestoneType: MilestoneTypeProduct,
			Assignee:      ActorRef{Kind: ActorUser, ID: member},
			ExecutedBy:    &agent,
		})
		if err != nil {
			t.Fatalf("Create(another assignee) error = %v", err)
		}
		if result.RunID != nil || result.Task.Status != StatusTodo || countRuns(result.Task.ID) != 0 {
			t.Fatalf("Create(another assignee) = run %v status %q, want nothing started",
				result.RunID, result.Task.Status)
		}
		// They have not set the Agent up, so the board says so rather than
		// naming anyone's machine.
		if result.Task.Execution.AssigneeReady || result.Task.Execution.UnreadyReason == nil ||
			*result.Task.Execution.UnreadyReason != "not_bound" {
			t.Fatalf("Create(another assignee) execution = %+v, want not_bound", result.Task.Execution)
		}
		var inboxKind, inboxBody string
		if err := pool.QueryRow(ctx,
			`SELECT kind, body FROM inbox_items WHERE recipient_user_id = $1 AND entity_id = $2`,
			member, result.Task.ID.String(),
		).Scan(&inboxKind, &inboxBody); err != nil || inboxKind != "task_assigned" {
			t.Fatalf("the new assignee's inbox = %q, %v; want task_assigned", inboxKind, err)
		}
		// It says who assigned it and, since the Agent is not set up on
		// their computer yet, that setting it up comes first.
		if want := "Schema A assigned you “Assign to a member”. Set Agent A up on your computer to run it."; inboxBody != want {
			t.Fatalf("task_assigned body = %q, want %q", inboxBody, want)
		}
		// And only they can start it.
		memberPrincipal := auth.UserPrincipal{
			UserID: member, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember,
		}
		if _, err := service.StartExecution(ctx, principal, result.Task.ID, StartExecutionInput{
			ExpectedRevision: result.Task.Revision,
		}); !errors.Is(err, ErrAssigneeRequired) {
			t.Fatalf("StartExecution(by the creator) error = %v, want ErrAssigneeRequired", err)
		}
		// Even the assignee cannot start it without a binding of their own.
		if _, err := service.StartExecution(ctx, memberPrincipal, result.Task.ID, StartExecutionInput{
			ExpectedRevision: result.Task.Revision,
		}); !errors.Is(err, execution.ErrRunAgentNotBound) {
			t.Fatalf("StartExecution(unbound assignee) error = %v, want ErrRunAgentNotBound", err)
		}
		// Once they set the Agent up on their own machine, it starts there.
		theirs := seedTaskRuntime(t, ctx, pool, fixture, "daemon-member", member)
		dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, member, theirs, "codex")
		started, err := service.StartExecution(ctx, memberPrincipal, result.Task.ID, StartExecutionInput{
			ExpectedRevision: result.Task.Revision,
		})
		if err != nil || started.RunID == nil {
			t.Fatalf("StartExecution(bound assignee) = %+v, %v", started, err)
		}
		var runtimeID uuid.UUID
		if err := pool.QueryRow(ctx,
			`SELECT runtime_id FROM runs WHERE id = $1`, *started.RunID,
		).Scan(&runtimeID); err != nil || runtimeID != theirs {
			t.Fatalf("the assignee's Run ran on %s, %v; want their own machine %s", runtimeID, err, theirs)
		}
	})

	t.Run("a Task created already closed gets no Run and keeps its status", func(t *testing.T) {
		result, err := service.Create(ctx, principal, CreateInput{
			ProjectID: fixture.ProjectA,
			Key:       "assign_on_create_done", Title: "Assign on create, done", Status: StatusDone,
			MilestoneType: MilestoneTypeProduct,
			ExecutedBy:    &agent,
		})
		if err != nil {
			t.Fatalf("Create(done, executedBy agent) error = %v", err)
		}
		created := result.Task
		if created.Status != StatusDone || countRuns(created.ID) != 0 {
			t.Fatalf("Create(done) = status %q with %d Runs, want done and none", created.Status, countRuns(created.ID))
		}
		// A Task born finished carries its outcome time from the start, the
		// way one that reaches the state through an update does.
		if created.CompletedAt == nil || !created.CompletedAt.Equal(created.CreatedAt) {
			t.Fatalf("Create(done) completedAt = %v, want createdAt %v",
				created.CompletedAt, created.CreatedAt)
		}
		cancelled, err := service.Create(ctx, principal, CreateInput{
			ProjectID: fixture.ProjectA,
			Key:       "assign_on_create_cancelled", Title: "Assign on create, cancelled",
			Status:        StatusCancelled,
			MilestoneType: MilestoneTypeProduct,
		})
		if err != nil {
			t.Fatalf("Create(cancelled) error = %v", err)
		}
		if cancelled.Task.CompletedAt == nil || !cancelled.Task.CompletedAt.Equal(cancelled.Task.CreatedAt) {
			t.Fatalf("Create(cancelled) completedAt = %v, want createdAt %v",
				cancelled.Task.CompletedAt, cancelled.Task.CreatedAt)
		}
	})

	t.Run("handing a human Task to an Agent starts its work", func(t *testing.T) {
		result, err := service.Create(ctx, principal, CreateInput{
			ProjectID: fixture.ProjectA,
			Key:       "assign_on_update", Title: "Assign on update", Status: StatusTodo,
			MilestoneType: MilestoneTypeProduct,
		})
		if err != nil {
			t.Fatalf("Create(human work) error = %v", err)
		}
		created := result.Task
		if created.Status != StatusTodo || created.ExecutedBy != created.Assignee || countRuns(created.ID) != 0 {
			t.Fatalf("Create(human work) = status %q executedBy %+v with %d Runs",
				created.Status, created.ExecutedBy, countRuns(created.ID))
		}
		handed, err := service.Update(ctx, principal, created.ID, UpdateInput{
			ExpectedRevision: created.Revision,
			ExecutedBy:       &agent,
		})
		if err != nil {
			t.Fatalf("Update(executedBy agent) error = %v", err)
		}
		if handed.RunID == nil || handed.Task.Status != StatusInProgress || countRuns(created.ID) != 1 {
			t.Fatalf("Update(executedBy agent) = run %v status %q %d Runs; want a Run, in_progress, 1",
				handed.RunID, handed.Task.Status, countRuns(created.ID))
		}
		assertTaskRunPrompt(t, ctx, pool, handed.Task, *handed.RunID, principal.UserID, execution.RunSourceAssign)

		// Re-selecting the same Agent is not a handoff: no second Run, and
		// the Task stays where its live Run left it.
		priority := PriorityHigh
		again, err := service.Update(ctx, principal, created.ID, UpdateInput{
			ExpectedRevision: handed.Task.Revision, ExecutedBy: &agent, Priority: &priority,
		})
		if err != nil {
			t.Fatalf("Update(same executedBy) error = %v", err)
		}
		if again.RunID != nil || again.Task.Status != StatusInProgress || countRuns(created.ID) != 1 {
			t.Fatalf("Update(same executedBy) = run %v, status %q, %d Runs; want no Run, in_progress, 1",
				again.RunID, again.Task.Status, countRuns(created.ID))
		}
	})

	// A second Agent in the company, reporting to the root, for handoffs.
	otherAgent := ActorRef{Kind: ActorAgent, ID: dbtest.NewUUID(t)}
	dbtest.SeedBoundAgent(t, ctx, pool, otherAgent.ID, fixture.CompanyA, "Second", "codex",
		&fixture.AgentA, fixture.UserA, fixture.RuntimeA)

	t.Run("admission uses the status the handoff lands on", func(t *testing.T) {
		result, err := service.Create(ctx, principal, CreateInput{
			ProjectID: fixture.ProjectA,
			Key:       "handoff_with_status", Title: "Handoff with status", Status: StatusTodo,
			MilestoneType: MilestoneTypeProduct,
		})
		if err != nil {
			t.Fatalf("Create(human work) error = %v", err)
		}
		created := result.Task
		// Closing the Task while naming an Agent records who would do the
		// work and starts nothing.
		done := StatusDone
		closed, err := service.Update(ctx, principal, created.ID, UpdateInput{
			ExpectedRevision: created.Revision,
			ExecutedBy:       &agent, Status: &done,
		})
		if err != nil {
			t.Fatalf("Update(executedBy agent, done) error = %v", err)
		}
		if closed.RunID != nil || closed.Task.Status != StatusDone ||
			closed.Task.ExecutedBy.ID != agent.ID || countRuns(created.ID) != 0 {
			t.Fatalf("Update(executedBy agent, done) = run %v, status %q, executedBy %+v, %d Runs",
				closed.RunID, closed.Task.Status, closed.Task.ExecutedBy, countRuns(created.ID))
		}
		// Reopening with an explicit admitted status and another Agent lands
		// in progress with the handoff Run, whatever status the body sent.
		todo := StatusTodo
		reopened, err := service.Update(ctx, principal, created.ID, UpdateInput{
			ExpectedRevision: closed.Task.Revision, ExecutedBy: &otherAgent, Status: &todo,
		})
		if err != nil {
			t.Fatalf("Update(other agent, todo) error = %v", err)
		}
		if reopened.RunID == nil || reopened.Task.Status != StatusInProgress ||
			reopened.Task.ExecutedBy.ID != otherAgent.ID || countRuns(created.ID) != 1 {
			t.Fatalf("Update(other agent, todo) = run %v, status %q, %d Runs; want a Run, in_progress, 1",
				reopened.RunID, reopened.Task.Status, countRuns(created.ID))
		}
	})

	t.Run("a handoff waits on prerequisites like any fresh Run", func(t *testing.T) {
		prerequisite, err := service.Create(ctx, principal, CreateInput{
			ProjectID: fixture.ProjectA,
			Key:       "handoff_prerequisite", Title: "Prerequisite", Status: StatusTodo,
			MilestoneType: MilestoneTypeProduct,
		})
		if err != nil {
			t.Fatalf("Create(prerequisite) error = %v", err)
		}
		blocked, err := service.Create(ctx, principal, CreateInput{
			ProjectID: fixture.ProjectA,
			Key:       "handoff_blocked", Title: "Blocked by a prerequisite", Status: StatusTodo,
			MilestoneType: MilestoneTypeProduct,
		})
		if err != nil {
			t.Fatalf("Create(blocked) error = %v", err)
		}
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO task_dependencies
				(project_id, company_id, dependent_task_id, prerequisite_task_id, created_by_user_id)
			VALUES ($5, $1, $2, $3, $4)`,
			fixture.CompanyA, blocked.Task.ID, prerequisite.Task.ID, principal.UserID, fixture.ProjectA)
		if _, err := service.Update(ctx, principal, blocked.Task.ID, UpdateInput{
			ExpectedRevision: blocked.Task.Revision,
			ExecutedBy:       &agent,
		}); !errors.Is(err, ErrDependencyBlocked) {
			t.Fatalf("Update(handoff while blocked) error = %v, want %v", err, ErrDependencyBlocked)
		}
		if got := countRuns(blocked.Task.ID); got != 0 {
			t.Fatalf("Update(handoff while blocked) enqueued %d Runs, want none", got)
		}
	})

	t.Run("a failed Task handed to another Agent meets the reopen fence", func(t *testing.T) {
		dbtest.MustExec(t, ctx, pool, `
			UPDATE tasks
			SET status='failed', completed_at=clock_timestamp(),
			    execution_generation=7, updated_at=clock_timestamp()
			WHERE id=$1`, fixture.TaskA)
		var runID uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO runs (
				company_id, agent_id, task_id, conversation_id, runtime_id,
				execution_authorized_by_user_id, status, task_execution_generation, run_type
			) VALUES ($1,$2,$3,$4,$5,$6,'running',7,'task')
			RETURNING id`,
			fixture.CompanyA, fixture.AgentA, fixture.TaskA,
			fixture.ConversationA, fixture.RuntimeA, fixture.UserA,
		).Scan(&runID); err != nil {
			t.Fatalf("seed running failure Run: %v", err)
		}
		current, err := service.Get(ctx, principal, fixture.TaskA, nil)
		if err != nil {
			t.Fatalf("Get(failed Task) error = %v", err)
		}
		runsBefore := countRuns(fixture.TaskA)
		// The Task has run, so the handoff needs confirmation first.
		if _, err := service.Update(ctx, principal, fixture.TaskA, UpdateInput{
			ExpectedRevision: current.Revision, ExecutedBy: &otherAgent,
		}); !errors.Is(err, ErrHandoffConfirmationRequired) {
			t.Fatalf("Update(unconfirmed handoff) error = %v, want ErrHandoffConfirmationRequired", err)
		}
		if _, err := service.Update(ctx, principal, fixture.TaskA, UpdateInput{
			ExpectedRevision: current.Revision, ExecutedBy: &otherAgent, ConfirmHandoff: true,
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("Update(handoff while the failure Run is active) error = %v, want %v", err, ErrConflict)
		}
		if got := countRuns(fixture.TaskA); got != runsBefore {
			t.Fatalf("refused handoff enqueued a Run: %d, want %d", got, runsBefore)
		}
		dbtest.MustExec(t, ctx, pool, `
			UPDATE runs SET status='completed', updated_at=clock_timestamp() WHERE id=$1`, runID)
		reopened, err := service.Update(ctx, principal, fixture.TaskA, UpdateInput{
			ExpectedRevision: current.Revision, ExecutedBy: &otherAgent, ConfirmHandoff: true,
		})
		if err != nil {
			t.Fatalf("Update(handoff after the failure Run ended) error = %v", err)
		}
		if reopened.RunID == nil || reopened.Task.Status != StatusInProgress ||
			reopened.Task.ExecutedBy.ID != otherAgent.ID || countRuns(fixture.TaskA) != runsBefore+1 {
			t.Fatalf("Update(handoff after failure) = run %v, status %q, %d Runs; want a Run, in_progress, %d",
				reopened.RunID, reopened.Task.Status, countRuns(fixture.TaskA), runsBefore+1)
		}
	})

}

// Taking a Task that has run away from the member running it is a confirmed
// handoff (ADR 0014): it needs tasks.assign or the current assignee, it
// cancels their live Runs, it advances both generations so their machine's
// work can no longer touch the Task, and it tells them their unmerged files
// stay where they are. The Task lands on the new assignee's own machine.
func TestTaskHandoffTakesTheWorkOffThePreviousAssigneesMachine(t *testing.T) {
	ctx, pool, fixture, service, principal := startTaskHarness(t)
	agent := ActorRef{Kind: ActorAgent, ID: fixture.AgentA}
	member := seedTaskMember(t, ctx, pool, fixture, "handoff-member@example.com")
	memberPrincipal := auth.UserPrincipal{
		UserID: member, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember,
	}
	theirs := seedTaskRuntime(t, ctx, pool, fixture, "daemon-handoff", member)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, member, theirs, "codex")

	// The owner assigns it to the member, who starts it on their own machine.
	created, err := service.Create(ctx, principal, CreateInput{
		ProjectID: fixture.ProjectA,
		Key:       "handoff", Title: "Handoff", Status: StatusTodo,
		MilestoneType: MilestoneTypeProduct,
		Assignee:      ActorRef{Kind: ActorUser, ID: member}, ExecutedBy: &agent,
	})
	if err != nil || created.RunID != nil {
		t.Fatalf("Create(assigned to a colleague) = %+v, %v; want no Run", created, err)
	}
	started, err := service.StartExecution(ctx, memberPrincipal, created.Task.ID, StartExecutionInput{
		ExpectedRevision: created.Task.Revision,
	})
	if err != nil || started.RunID == nil {
		t.Fatalf("StartExecution(assignee) = %+v, %v", started, err)
	}
	assertTaskRunPrompt(t, ctx, pool, started.Task, *started.RunID, member, execution.RunSourceExecution)
	generationsOf := func() (int64, int64) {
		t.Helper()
		var executionGeneration, workspaceGeneration int64
		if err := pool.QueryRow(ctx,
			`SELECT execution_generation, workspace_generation FROM tasks WHERE id = $1`, created.Task.ID,
		).Scan(&executionGeneration, &workspaceGeneration); err != nil {
			t.Fatalf("read Task generations: %v", err)
		}
		return executionGeneration, workspaceGeneration
	}
	beforeExecution, beforeWorkspace := generationsOf()

	// A bystander cannot take it: they hold no tasks.assign and are not the
	// assignee.
	current, err := service.Get(ctx, principal, created.Task.ID, nil)
	if err != nil {
		t.Fatalf("Get(running Task) error = %v", err)
	}
	mine := ActorRef{Kind: ActorUser, ID: principal.UserID}
	bystander := seedTaskMember(t, ctx, pool, fixture, "bystander@example.com")
	bystanderPrincipal := auth.UserPrincipal{
		UserID: bystander, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember,
	}
	if _, err := service.Update(ctx, bystanderPrincipal, created.Task.ID, UpdateInput{
		ExpectedRevision: current.Revision,
		Assignee:         &ActorRef{Kind: ActorUser, ID: bystander}, ConfirmHandoff: true,
	}); !errors.Is(err, ErrHandoffPermissionRequired) {
		t.Fatalf("Update(bystander handoff) error = %v, want ErrHandoffPermissionRequired", err)
	}
	// Nor can they file a new Task for somebody else; for themself they can.
	if _, err := service.Create(ctx, bystanderPrincipal, CreateInput{
		ProjectID: fixture.ProjectA, Key: "bystander_for_member", Title: "For a colleague",
		Status: StatusTodo, MilestoneType: MilestoneTypeProduct,
		Assignee: ActorRef{Kind: ActorUser, ID: member},
	}); !errors.Is(err, ErrAssignPermissionRequired) {
		t.Fatalf("Create(bystander assigns a colleague) error = %v, want ErrAssignPermissionRequired", err)
	}
	// The owner holds tasks.assign, but the Task has run: confirmation first,
	// and the refusal says the member's Run is live, so confirming cancels it.
	var live *HandoffConfirmationError
	if _, err := service.Update(ctx, principal, created.Task.ID, UpdateInput{
		ExpectedRevision: current.Revision, Assignee: &mine,
	}); !errors.As(err, &live) || !live.RunLive {
		t.Fatalf("Update(unconfirmed handoff, Run live) error = %v, want HandoffConfirmationError{RunLive: true}", err)
	}

	handed, err := service.Update(ctx, principal, created.Task.ID, UpdateInput{
		ExpectedRevision: current.Revision, Assignee: &mine, ConfirmHandoff: true,
	})
	if err != nil {
		t.Fatalf("Update(confirmed handoff) error = %v", err)
	}
	if handed.Task.Assignee.ID != principal.UserID || handed.Task.ExecutedBy.ID != agent.ID ||
		!handed.Task.Execution.AssigneeReady || handed.Task.Status != StatusInProgress {
		t.Fatalf("handed Task = %+v, want the new assignee, the same Agent, ready, in progress", handed.Task)
	}
	// The Task lands on the new assignee's own machine, under their own
	// authority, because they took it themselves.
	if handed.RunID == nil {
		t.Fatalf("Update(handoff to myself) started no Run")
	}
	var runtimeID, authorizer uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT runtime_id, execution_authorized_by_user_id FROM runs WHERE id = $1`, *handed.RunID,
	).Scan(&runtimeID, &authorizer); err != nil {
		t.Fatalf("read the handoff Run: %v", err)
	}
	if runtimeID != fixture.RuntimeA || authorizer != principal.UserID {
		t.Fatalf("handoff Run = %s / %s, want the new assignee's binding %s / %s",
			runtimeID, authorizer, fixture.RuntimeA, principal.UserID)
	}
	// A handoff to oneself starts the Run the way an explicit start does.
	assertTaskRunPrompt(t, ctx, pool, handed.Task, *handed.RunID, principal.UserID, execution.RunSourceAssign)
	afterExecution, afterWorkspace := generationsOf()
	if afterExecution != beforeExecution+2 || afterWorkspace != beforeWorkspace+1 {
		t.Fatalf("generations = %d/%d, want %d/%d: the handoff advances both and its Run advances execution again",
			afterExecution, afterWorkspace, beforeExecution+2, beforeWorkspace+1)
	}

	// The previous assignee's Run is cancelled, so their daemon drops the
	// work in flight.
	var status, reason string
	if err := pool.QueryRow(ctx,
		`SELECT status, COALESCE(error, '') FROM runs WHERE id = $1`, *started.RunID,
	).Scan(&status, &reason); err != nil {
		t.Fatalf("read the previous assignee's Run: %v", err)
	}
	if status != "cancelled" || reason != execution.RunReasonTaskReassigned {
		t.Fatalf("the previous Run = %s / %q, want cancelled with task_reassigned", status, reason)
	}
	// And they are told where their unmerged files are. Nobody tells the
	// owner they gave themselves a Task.
	items := map[string]bool{}
	rows, err := pool.Query(ctx,
		`SELECT recipient_user_id, kind FROM inbox_items WHERE entity_id = $1`, created.Task.ID.String())
	if err != nil {
		t.Fatalf("read the handoff Inbox items: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var recipient uuid.UUID
		var kind string
		if err := rows.Scan(&recipient, &kind); err != nil {
			t.Fatalf("scan Inbox item: %v", err)
		}
		items[recipient.String()+":"+kind] = true
	}
	want := map[string]bool{
		member.String() + ":task_assigned": true,
		member.String() + ":task_handoff":  true,
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("handoff Inbox = %v, want %v: the assignment and the handoff, both to the colleague", items, want)
	}
	colleagueNotice := func(kind string) string {
		t.Helper()
		var body string
		if err := pool.QueryRow(ctx, `
			SELECT body FROM inbox_items
			WHERE recipient_user_id = $1 AND entity_id = $2 AND kind = $3
			ORDER BY created_at DESC, id DESC LIMIT 1`,
			member, created.Task.ID.String(), kind,
		).Scan(&body); err != nil {
			t.Fatalf("read the colleague's %s item: %v", kind, err)
		}
		return body
	}
	// The handoff stopped the colleague's run, and the notice says so.
	if got, want := colleagueNotice("task_handoff"), "“Handoff” was handed to Schema A. "+
		"Its run on your computer was stopped. Files that were not merged stay on your computer."; got != want {
		t.Fatalf("task_handoff body = %q, want %q", got, want)
	}
	// The first assignment named who gave it and what to press: the Task was
	// waiting to start and the colleague had the Agent set up.
	if got, want := colleagueNotice("task_assigned"), "Schema A assigned you “Handoff”. Start it on your computer: Agent A runs there."; got != want {
		t.Fatalf("first task_assigned body = %q, want %q", got, want)
	}

	// The owner's Run finishes and the Task waits in review, with its "Ready
	// to review" notice. Handing it back still needs confirmation, but now
	// nothing is live, and the refusal says so instead of promising to
	// cancel work.
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runs SET status = 'completed', updated_at = clock_timestamp() WHERE id = $1`, *handed.RunID)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks SET status = 'in_review', version = version + 1, updated_at = clock_timestamp()
		WHERE id = $1`, created.Task.ID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO inbox_items (
			company_id, recipient_user_id, conversation_id, kind, entity_type, entity_id,
			title, body, dedupe_key
		) VALUES ($1, $2, $3, 'run_completed', 'run', $4, 'Ready to review: Handoff',
			'Agent A finished its task run. Open the task to review the result.', 'run:' || $4 || ':completed')`,
		fixture.CompanyA, principal.UserID, created.Task.ConversationID, handed.RunID.String())
	reviewed, err := service.Get(ctx, principal, created.Task.ID, nil)
	if err != nil {
		t.Fatalf("Get(reviewed Task) error = %v", err)
	}
	colleagueRef := ActorRef{Kind: ActorUser, ID: member}
	var finished *HandoffConfirmationError
	if _, err := service.Update(ctx, principal, created.Task.ID, UpdateInput{
		ExpectedRevision: reviewed.Revision, Assignee: &colleagueRef,
	}); !errors.As(err, &finished) || finished.RunLive {
		t.Fatalf("Update(unconfirmed handoff, nothing live) error = %v, want HandoffConfirmationError{RunLive: false}", err)
	}
	back, err := service.Update(ctx, principal, created.Task.ID, UpdateInput{
		ExpectedRevision: reviewed.Revision, Assignee: &colleagueRef, ConfirmHandoff: true,
	})
	// Nobody can review or merge the owner's Run any more, so the Task does
	// not wait in review: it goes back to todo, and the owner's "Ready to
	// review" notice closes.
	if err != nil || back.RunID != nil || back.Task.Status != StatusTodo {
		t.Fatalf("Update(hand back) = %+v, %v; want the Task back in todo and no Run", back, err)
	}
	var reviewRead, reviewArchived *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT read_at, archived_at FROM inbox_items
		WHERE recipient_user_id = $1 AND entity_id = $2 AND kind = 'run_completed'`,
		principal.UserID, handed.RunID.String(),
	).Scan(&reviewRead, &reviewArchived); err != nil {
		t.Fatalf("read the owner's review notice: %v", err)
	}
	if reviewRead == nil || reviewArchived == nil {
		t.Fatalf("owner's review notice read %v archived %v, want both: nobody reviews that Run now",
			reviewRead, reviewArchived)
	}
	// The colleague is told to start it on their computer, and can.
	if got, want := colleagueNotice("task_assigned"), "Schema A assigned you “Handoff”. Start it on your computer: Agent A runs there."; got != want {
		t.Fatalf("hand-back task_assigned body = %q, want %q", got, want)
	}
	restarted, err := service.StartExecution(ctx, memberPrincipal, created.Task.ID, StartExecutionInput{
		ExpectedRevision: back.Task.Revision,
	})
	if err != nil || restarted.RunID == nil || restarted.Task.Status != StatusInProgress {
		t.Fatalf("StartExecution(after the hand back) = %+v, %v; want a Run, in progress", restarted, err)
	}
	// Their Run ends without moving the Task, which stays in progress with
	// nothing running. Handing it on again stops nothing, and the notice
	// does not claim it did; the Task still goes back to todo, since the
	// work in progress was the colleague's.
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runs SET status = 'completed', updated_at = clock_timestamp() WHERE id = $1`, *restarted.RunID)
	onward, err := service.Update(ctx, principal, created.Task.ID, UpdateInput{
		ExpectedRevision: restarted.Task.Revision,
		Assignee:         &ActorRef{Kind: ActorUser, ID: bystander}, ConfirmHandoff: true,
	})
	if err != nil || onward.RunID != nil || onward.Task.Status != StatusTodo {
		t.Fatalf("Update(hand on, nothing live) = %+v, %v; want the Task in todo and no Run", onward, err)
	}
	if got, want := colleagueNotice("task_handoff"), "“Handoff” was handed to bystander@example.com. "+
		"Files that were not merged stay on your computer."; got != want {
		t.Fatalf("task_handoff body, nothing live = %q, want %q", got, want)
	}
}

// Only the assignee starts a Task (ADR 0014), so when it moves to another
// member the "Task assigned" notice that asked the previous assignee to start
// it closes in the same write: whoever moved it, and whether or not it had
// run. The new assignee's notice stays open.
func TestReassigningATaskClosesThePreviousAssigneesAssignedNotice(t *testing.T) {
	ctx, pool, fixture, service, principal := startTaskHarness(t)
	agent := ActorRef{Kind: ActorAgent, ID: fixture.AgentA}
	bob := seedTaskMember(t, ctx, pool, fixture, "reassign-bob@example.com")
	eli := seedTaskMember(t, ctx, pool, fixture, "reassign-eli@example.com")
	bobRef := ActorRef{Kind: ActorUser, ID: bob}
	eliRef := ActorRef{Kind: ActorUser, ID: eli}
	bobRuntime := seedTaskRuntime(t, ctx, pool, fixture, "daemon-reassign-bob", bob)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, bob, bobRuntime, "codex")

	var updatedFrames []inbox.ItemWithCounts
	if err := service.bus.Subscribe(inbox.Topic, func(_ context.Context, event events.Event) error {
		if event.Name != inbox.EventItemUpdated {
			return nil
		}
		item, err := events.PayloadOf[inbox.ItemWithCounts](event)
		if err != nil {
			return err
		}
		updatedFrames = append(updatedFrames, item)
		return nil
	}); err != nil {
		t.Fatalf("subscribe to the inbox topic: %v", err)
	}

	created, err := service.Create(ctx, principal, CreateInput{
		ProjectID: fixture.ProjectA,
		Key:       "reassign", Title: "Reassign", Status: StatusTodo,
		MilestoneType: MilestoneTypeProduct,
		Assignee:      bobRef, ExecutedBy: &agent,
	})
	if err != nil {
		t.Fatalf("Create(assigned to Bob) error = %v", err)
	}
	// assignedNotices counts one member's task_assigned notices about the
	// Task: those still open, and those read and archived.
	assignedNotices := func(member uuid.UUID) (open, closed int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT
				count(*) FILTER (WHERE read_at IS NULL OR archived_at IS NULL)::integer,
				count(*) FILTER (WHERE read_at IS NOT NULL AND archived_at IS NOT NULL)::integer
			FROM inbox_items
			WHERE recipient_user_id = $1 AND entity_type = 'task' AND entity_id = $2
			  AND kind = 'task_assigned'`,
			member, created.Task.ID.String(),
		).Scan(&open, &closed); err != nil {
			t.Fatalf("count task_assigned notices: %v", err)
		}
		return open, closed
	}
	if open, closed := assignedNotices(bob); open != 1 || closed != 0 {
		t.Fatalf("Bob's task_assigned after create = %d open, %d closed; want 1, 0", open, closed)
	}

	// The Task has never run, so the owner gives it to Eli without a
	// confirmation. Bob's notice closes, and the close reaches his bell.
	toEli, err := service.Update(ctx, principal, created.Task.ID, UpdateInput{
		ExpectedRevision: created.Task.Revision, Assignee: &eliRef,
	})
	if err != nil {
		t.Fatalf("Update(reassign to Eli) error = %v", err)
	}
	if open, closed := assignedNotices(bob); open != 0 || closed != 1 {
		t.Fatalf("Bob's task_assigned after the reassignment = %d open, %d closed; want 0, 1", open, closed)
	}
	if open, closed := assignedNotices(eli); open != 1 || closed != 0 {
		t.Fatalf("Eli's task_assigned after the reassignment = %d open, %d closed; want 1, 0", open, closed)
	}
	var framed bool
	for _, frame := range updatedFrames {
		if frame.RecipientUserID == bob && frame.Type == inbox.TypeTaskAssigned &&
			!frame.Unread && frame.ArchivedAt != nil && frame.Counts.UnreadCount == 0 {
			framed = true
		}
	}
	if !framed {
		t.Fatalf("inbox.updated frames = %+v, want Bob's closed task_assigned with his count at 0",
			updatedFrames)
	}

	// Eli hands it back to Bob. Nobody tells Eli about a move they made,
	// but the notice that asked them to start it closes all the same.
	toBob, err := service.Update(ctx, auth.UserPrincipal{
		UserID: eli, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember,
	}, created.Task.ID, UpdateInput{
		ExpectedRevision: toEli.Task.Revision, Assignee: &bobRef,
	})
	if err != nil {
		t.Fatalf("Update(Eli hands it back to Bob) error = %v", err)
	}
	if open, closed := assignedNotices(eli); open != 0 || closed != 1 {
		t.Fatalf("Eli's task_assigned after handing it back = %d open, %d closed; want 0, 1", open, closed)
	}
	if open, closed := assignedNotices(bob); open != 1 || closed != 1 {
		t.Fatalf("Bob's task_assigned after the hand back = %d open, %d closed; want 1, 1", open, closed)
	}

	// Bob starts it on his computer, then the owner's confirmed handoff
	// gives it to Eli: Bob's run stops and his new notice closes too.
	started, err := service.StartExecution(ctx, auth.UserPrincipal{
		UserID: bob, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember,
	}, created.Task.ID, StartExecutionInput{ExpectedRevision: toBob.Task.Revision})
	if err != nil || started.RunID == nil {
		t.Fatalf("StartExecution(Bob) = %+v, %v", started, err)
	}
	if _, err := service.Update(ctx, principal, created.Task.ID, UpdateInput{
		ExpectedRevision: started.Task.Revision, Assignee: &eliRef, ConfirmHandoff: true,
	}); err != nil {
		t.Fatalf("Update(confirmed handoff to Eli) error = %v", err)
	}
	if open, closed := assignedNotices(bob); open != 0 || closed != 2 {
		t.Fatalf("Bob's task_assigned after the handoff = %d open, %d closed; want 0, 2", open, closed)
	}
	if open, closed := assignedNotices(eli); open != 1 || closed != 1 {
		t.Fatalf("Eli's task_assigned after the handoff = %d open, %d closed; want 1, 1", open, closed)
	}
}

// Moving a Task's work to another Agent is a handoff too (ADR 0014). The
// assignee keeps the Task, so when an owner's move stops their run, they are
// the one told: who moved it, that the run stopped and how to go on. The
// "Ready to review" notice of a Run from before the handoff closes with it.
func TestTaskAgentHandoffTellsTheAssigneeTheirRunStopped(t *testing.T) {
	ctx, pool, fixture, service, principal := startTaskHarness(t)
	agent := ActorRef{Kind: ActorAgent, ID: fixture.AgentA}
	member := seedTaskMember(t, ctx, pool, fixture, "agent-handoff@example.com")
	memberPrincipal := auth.UserPrincipal{
		UserID: member, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember,
	}
	theirs := seedTaskRuntime(t, ctx, pool, fixture, "daemon-agent-handoff", member)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, member, theirs, "codex")
	second := ActorRef{Kind: ActorAgent, ID: dbtest.NewUUID(t)}
	dbtest.SeedAgent(t, ctx, pool, second.ID, fixture.CompanyA, "Second", "codex", &fixture.AgentA)

	created, err := service.Create(ctx, principal, CreateInput{
		ProjectID: fixture.ProjectA,
		Key:       "agent_handoff", Title: "Agent handoff", Status: StatusTodo,
		MilestoneType: MilestoneTypeProduct,
		Assignee:      ActorRef{Kind: ActorUser, ID: member}, ExecutedBy: &agent,
	})
	if err != nil {
		t.Fatalf("Create(assigned to a colleague) error = %v", err)
	}
	started, err := service.StartExecution(ctx, memberPrincipal, created.Task.ID, StartExecutionInput{
		ExpectedRevision: created.Task.Revision,
	})
	if err != nil || started.RunID == nil {
		t.Fatalf("StartExecution(assignee) = %+v, %v", started, err)
	}
	// An earlier Run of the Task finished and left its review notice.
	var earlier uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			execution_authorized_by_user_id, status, task_execution_generation, run_type
		) VALUES ($1, $2, $3, $4, $5, $6, 'completed', 1, 'task')
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA, created.Task.ID, created.Task.ConversationID, theirs, member,
	).Scan(&earlier); err != nil {
		t.Fatalf("seed the earlier Run: %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO inbox_items (
			company_id, recipient_user_id, conversation_id, kind, entity_type, entity_id,
			title, body, dedupe_key
		) VALUES ($1, $2, $3, 'run_completed', 'run', $4, 'Ready to review: Agent handoff',
			'Agent A finished its task run. Open the task to review the result.', 'run:' || $4 || ':completed')`,
		fixture.CompanyA, member, created.Task.ConversationID, earlier.String())

	current, err := service.Get(ctx, principal, created.Task.ID, nil)
	if err != nil {
		t.Fatalf("Get(running Task) error = %v", err)
	}
	moved, err := service.Update(ctx, principal, created.Task.ID, UpdateInput{
		ExpectedRevision: current.Revision, ExecutedBy: &second, ConfirmHandoff: true,
	})
	if err != nil {
		t.Fatalf("Update(confirmed Agent handoff) error = %v", err)
	}
	// Its run was stopped, so the Task does not stay in progress with
	// nothing running: it goes back to todo for the member to start.
	if moved.RunID != nil || moved.Task.Assignee.ID != member || moved.Task.ExecutedBy.ID != second.ID ||
		moved.Task.Status != StatusTodo {
		t.Fatalf("moved Task = %+v, run %v; want the member's, done by Second, todo, no Run", moved.Task, moved.RunID)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, *started.RunID).Scan(&status); err != nil {
		t.Fatalf("read the member's Run: %v", err)
	}
	if status != "cancelled" {
		t.Fatalf("the member's Run = %s, want cancelled", status)
	}

	var title, body string
	if err := pool.QueryRow(ctx, `
		SELECT title, body FROM inbox_items
		WHERE recipient_user_id = $1 AND entity_id = $2 AND kind = 'task_handoff'`,
		member, created.Task.ID.String(),
	).Scan(&title, &body); err != nil {
		t.Fatalf("read the member's task_handoff item: %v", err)
	}
	if want := "Schema A moved “Agent handoff” to Second. Its run on your computer was stopped. " +
		"Files that were not merged stay on your computer. Set Second up on your computer to run it."; title != "Task run stopped" || body != want {
		t.Fatalf("task_handoff = %q / %q, want %q / %q", title, body, "Task run stopped", want)
	}
	var readAt, archivedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT read_at, archived_at FROM inbox_items
		WHERE recipient_user_id = $1 AND entity_id = $2 AND kind = 'run_completed'`,
		member, earlier.String(),
	).Scan(&readAt, &archivedAt); err != nil {
		t.Fatalf("read the earlier review notice: %v", err)
	}
	if readAt == nil || archivedAt == nil {
		t.Fatalf("earlier review notice read %v archived %v, want both: nobody reviews that Run now", readAt, archivedAt)
	}

	// The member moving their own Task is told nothing.
	current, err = service.Get(ctx, principal, created.Task.ID, nil)
	if err != nil {
		t.Fatalf("Get(moved Task) error = %v", err)
	}
	if _, err := service.Update(ctx, memberPrincipal, created.Task.ID, UpdateInput{
		ExpectedRevision: current.Revision, ExecutedBy: &agent, ConfirmHandoff: true,
	}); err != nil {
		t.Fatalf("Update(the member moves it back) error = %v", err)
	}
	var notices int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM inbox_items
		WHERE recipient_user_id = $1 AND entity_id = $2 AND kind = 'task_handoff'`,
		member, created.Task.ID.String(),
	).Scan(&notices); err != nil || notices != 1 {
		t.Fatalf("task_handoff items = %d, %v; want the one the owner's move raised", notices, err)
	}
}

// Moving a Task out of review decides the work its assignee's computer did,
// so it is the assignee's or a tasks.assign holder's. A colleague who reads
// the Task cannot mark it done without review; other moves stay open.
func TestTaskReviewIsTheAssigneesOrATasksAssignHolders(t *testing.T) {
	ctx, pool, fixture, service, principal := startTaskHarness(t)
	member := seedTaskMember(t, ctx, pool, fixture, "reviewer@example.com")
	colleague := seedTaskMember(t, ctx, pool, fixture, "review-colleague@example.com")
	principalOf := func(id uuid.UUID) auth.UserPrincipal {
		return auth.UserPrincipal{UserID: id, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember}
	}
	inReview := func(key string) Response {
		t.Helper()
		created, err := service.Create(ctx, principal, CreateInput{
			ProjectID: fixture.ProjectA, Key: key, Title: key, Status: StatusTodo,
			MilestoneType: MilestoneTypeProduct, Assignee: ActorRef{Kind: ActorUser, ID: member},
		})
		if err != nil {
			t.Fatalf("Create(%s) error = %v", key, err)
		}
		dbtest.MustExec(t, ctx, pool, `
			UPDATE tasks SET status = 'in_review', version = version + 1, updated_at = clock_timestamp()
			WHERE id = $1`, created.Task.ID)
		current, err := service.Get(ctx, principal, created.Task.ID, nil)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", key, err)
		}
		return current
	}
	done := StatusDone

	reviewed := inReview("review_member")
	if _, err := service.Update(ctx, principalOf(colleague), reviewed.ID, UpdateInput{
		ExpectedRevision: reviewed.Revision, Status: &done,
	}); !errors.Is(err, ErrReviewPermissionRequired) {
		t.Fatalf("Update(colleague marks done without review) error = %v, want ErrReviewPermissionRequired", err)
	}
	if finished, err := service.Update(ctx, principalOf(member), reviewed.ID, UpdateInput{
		ExpectedRevision: reviewed.Revision, Status: &done,
	}); err != nil || finished.Task.Status != StatusDone {
		t.Fatalf("Update(assignee marks done) = %+v, %v; want done", finished.Task, err)
	}
	reviewed = inReview("review_owner")
	if finished, err := service.Update(ctx, principal, reviewed.ID, UpdateInput{
		ExpectedRevision: reviewed.Revision, Status: &done,
	}); err != nil || finished.Task.Status != StatusDone {
		t.Fatalf("Update(owner marks done) = %+v, %v; want done", finished.Task, err)
	}

	// A Task that is not in review is not a review decision.
	open, err := service.Create(ctx, principal, CreateInput{
		ProjectID: fixture.ProjectA, Key: "review_open", Title: "Open", Status: StatusTodo,
		MilestoneType: MilestoneTypeProduct, Assignee: ActorRef{Kind: ActorUser, ID: member},
	})
	if err != nil {
		t.Fatalf("Create(open) error = %v", err)
	}
	if _, err := service.Update(ctx, principalOf(colleague), open.Task.ID, UpdateInput{
		ExpectedRevision: open.Task.Revision, Status: &done,
	}); err != nil {
		t.Fatalf("Update(colleague closes an open Task) error = %v", err)
	}
}

// A create without a key gets {taskPrefix}-{number} from the same allocation
// that numbered it, so the key a person quotes on the CLI and the label on
// the board are one Task. A key the client chose is kept as sent.
func TestCreateMintsTheDisplayedKeyWhenNoneIsGiven(t *testing.T) {
	ctx, pool, fixture, service, principal := startTaskHarness(t)
	dbtest.MustExec(t, ctx, pool,
		`UPDATE companies SET next_task_number = 7, task_prefix = 'NWR' WHERE id = $1`, fixture.CompanyA)
	founder := CreateInput{
		ProjectID: fixture.ProjectA,
		Title:     "Unkeyed", Status: StatusTodo,
		MilestoneType: MilestoneTypeProduct,
	}

	minted, err := service.Create(ctx, principal, founder)
	if err != nil {
		t.Fatalf("Create(no key) error = %v", err)
	}
	if minted.Task.Number != 7 || minted.Task.Key != "NWR-7" {
		t.Fatalf("Create(no key) = number %d key %q, want 7 and NWR-7", minted.Task.Number, minted.Task.Key)
	}

	chosen := founder
	chosen.Key = "ship_release"
	kept, err := service.Create(ctx, principal, chosen)
	if err != nil {
		t.Fatalf("Create(chosen key) error = %v", err)
	}
	if kept.Task.Number != 8 || kept.Task.Key != "ship_release" {
		t.Fatalf("Create(chosen key) = number %d key %q, want 8 and ship_release", kept.Task.Number, kept.Task.Key)
	}

	// The label namespace belongs to the server: a chosen key shaped like a
	// label would either lie about a number or take the label of a later
	// Task, wedging every keyless create once the counter reaches it.
	nextNumber := func() int64 {
		t.Helper()
		var next int64
		if err := pool.QueryRow(ctx,
			`SELECT next_task_number FROM companies WHERE id = $1`, fixture.CompanyA,
		).Scan(&next); err != nil {
			t.Fatalf("read next Task number: %v", err)
		}
		return next
	}
	for _, planted := range []string{"NWR-12", "nwr-12", "NWR-9"} {
		shaped := founder
		shaped.Key = planted
		if _, err := service.Create(ctx, principal, shaped); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Create(key %q) error = %v, want %v", planted, err, ErrInvalidInput)
		}
		if got := nextNumber(); got != 9 {
			t.Fatalf("refused key %q consumed a number: next is %d, want 9", planted, got)
		}
	}

	// A label-shaped key that reached the table anyway (older data) must
	// not wedge keyless creates: the mint skips the taken label.
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET key = 'NWR-9' WHERE id = $1`, fixture.TaskA)
	skipped, err := service.Create(ctx, principal, founder)
	if err != nil {
		t.Fatalf("Create(no key, label taken) error = %v", err)
	}
	if skipped.Task.Number != 10 || skipped.Task.Key != "NWR-10" {
		t.Fatalf("Create(no key, label taken) = number %d key %q, want 10 and NWR-10",
			skipped.Task.Number, skipped.Task.Key)
	}
	again, err := service.Create(ctx, principal, founder)
	if err != nil || again.Task.Number != 11 || again.Task.Key != "NWR-11" {
		t.Fatalf("Create(no key, after skip) = %d %q, %v; want 11 NWR-11", again.Task.Number, again.Task.Key, err)
	}
}

// A Run a Task write enqueues is claimed the moment it exists, not on the
// daemon's next poll: every enqueue says runtime.work_available after
// commit, exactly as a Task message does (Runs: push wakeup on every
// claimable transition; the 30-second poll is only the backstop).
func TestTaskWritesWakeTheRuntimeForTheRunsTheyEnqueue(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool,
		`UPDATE companies SET next_task_number = 2 WHERE id = $1`, fixture.CompanyA)
	bus := newTestBus(t)
	var woken []string
	if err := bus.Subscribe(execution.EventRuntimeWorkAvailable, func(_ context.Context, event events.Event) error {
		work, err := events.PayloadOf[execution.RuntimeWork](event)
		if err != nil {
			return err
		}
		if work.RuntimeID == fixture.RuntimeA.String() {
			woken = append(woken, work.RunID)
		}
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	service, err := NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatalf("construct Task service: %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	agent := ActorRef{Kind: ActorAgent, ID: fixture.AgentA}
	lastRunID := func(taskID uuid.UUID) string {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx,
			`SELECT id FROM runs WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1`, taskID,
		).Scan(&id); err != nil {
			t.Fatalf("read the Task's latest Run: %v", err)
		}
		return id.String()
	}

	// Create with an Agent doing the work.
	created, err := service.Create(ctx, principal, CreateInput{
		ProjectID: fixture.ProjectA,
		Key:       "wake_on_create", Title: "Wake on create", Status: StatusTodo,
		MilestoneType: MilestoneTypeProduct, ExecutedBy: &agent,
	})
	if err != nil {
		t.Fatalf("Create(executedBy agent) error = %v", err)
	}
	if len(woken) != 1 || woken[0] != lastRunID(created.Task.ID) {
		t.Fatalf("Create(executedBy agent) woke %v, want the assignment Run %s", woken, lastRunID(created.Task.ID))
	}

	// A human Task handed to an Agent.
	founder, err := service.Create(ctx, principal, CreateInput{
		ProjectID: fixture.ProjectA,
		Key:       "wake_on_handoff", Title: "Wake on handoff", Status: StatusTodo,
		MilestoneType: MilestoneTypeProduct,
	})
	if err != nil {
		t.Fatalf("Create(human work) error = %v", err)
	}
	if len(woken) != 1 {
		t.Fatalf("Create(human work) woke a runtime: %v", woken)
	}
	handed, err := service.Update(ctx, principal, founder.Task.ID, UpdateInput{
		ExpectedRevision: founder.Task.Revision,
		ExecutedBy:       &agent,
	})
	if err != nil || handed.RunID == nil {
		t.Fatalf("Update(handoff) = %+v, %v", handed, err)
	}
	if len(woken) != 2 || woken[1] != handed.RunID.String() {
		t.Fatalf("Update(handoff) woke %v, want the handoff Run %s", woken, handed.RunID)
	}

	// An explicit start on a runnable Agent Task (the start advances the
	// Starter Journey, which needs its row).
	dbtest.MustExec(t, ctx, pool,
		`UPDATE tasks SET status = 'todo', updated_at = clock_timestamp() WHERE id = $1`, fixture.TaskA)
	current, err := service.Get(ctx, principal, fixture.TaskA, nil)
	if err != nil {
		t.Fatalf("Get(TaskA) error = %v", err)
	}
	started, err := service.StartExecution(ctx, principal, fixture.TaskA, StartExecutionInput{
		ExpectedRevision: current.Revision,
	})
	if err != nil || started.RunID == nil {
		t.Fatalf("StartExecution = %+v, %v", started, err)
	}
	if len(woken) != 3 || woken[2] != started.RunID.String() {
		t.Fatalf("StartExecution woke %v, want the execution Run %s", woken, started.RunID)
	}
}
