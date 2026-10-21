//go:build integration

package execution

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// seedMemberB adds a plain member to the fixture's CompanyA.
func seedMemberB(t *testing.T, h *runHarness) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := dbtest.NewUUID(t)
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Member B')`,
		id,
		id.String()+"@zenith-team.test",
	)
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		h.fixture.CompanyA,
		id,
	)
	// Member B works on the fixture Project (ADR 0017): they read, write and
	// run its Tasks.
	dbtest.GrantProject(t, ctx, h.pool, h.fixture.CompanyA, h.fixture.ProjectA, id)
	return id
}

// seedRuntime registers an online runtime of the given provider for a
// member of the fixture's CompanyA.
func seedRuntime(t *testing.T, h *runHarness, owner uuid.UUID, provider, daemonID string) uuid.UUID {
	t.Helper()
	id := dbtest.NewUUID(t)
	dbtest.MustExec(t, context.Background(), h.pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, $3, 'Machine', $4, 'online', $5, now())`,
		id, h.fixture.CompanyA, daemonID, provider, owner)
	return id
}

// bind sets a member's binding of an Agent to one of their runtimes, the
// way the Agent domain's Bind writes it.
func bind(t *testing.T, h *runHarness, agentID, userID, runtimeID uuid.UUID, provider string) {
	t.Helper()
	dbtest.MustExec(t, context.Background(), h.pool, `
		INSERT INTO agent_runtime_bindings (company_id, agent_id, user_id, runtime_id, provider)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (agent_id, user_id) DO UPDATE SET runtime_id = EXCLUDED.runtime_id`,
		h.fixture.CompanyA, agentID, userID, runtimeID, provider)
}

func countRuns(t *testing.T, h *runHarness) int {
	t.Helper()
	var count int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM runs WHERE company_id = $1`,
		h.fixture.CompanyA).Scan(&count); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	return count
}

func mustRun(t *testing.T, h *runHarness, id uuid.UUID) generated.Run {
	t.Helper()
	run, err := h.queries.GetRun(context.Background(), id)
	if err != nil {
		t.Fatalf("read run %s: %v", id, err)
	}
	return run
}

// The enqueue places a Run on the authorizer's own binding of the Agent
// (ADR 0014): no binding — a member who never set the Agent up, no
// authorizer at all, a revoked member — is refused before anything is
// written; a bound member's Run lands on THEIR machine, never the
// registrant's.
func TestEnqueueUsesTheAuthorizersBindingAndRefusesWithoutOne(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	memberB := seedMemberB(t, h)
	conversation := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, h.pool, `
		INSERT INTO conversations (id, company_id, kind, agent_id, created_by_user_id, title)
		VALUES ($1, $2, 'direct', $3, $4, 'B asks')`,
		conversation, h.fixture.CompanyA, h.fixture.AgentA, memberB)
	trigger := func(authorizer uuid.UUID) RunTrigger {
		return RunTrigger{
			CompanyID: h.fixture.CompanyA, ConversationID: conversation, AgentID: h.fixture.AgentA,
			TriggerPrompt: "hello", Source: RunSourceMessage,
			ActorUserID: authorizer, AuthorizedByUserID: authorizer,
		}
	}
	before := countRuns(t, h)

	var pending []events.Event
	if _, err := EnqueueRunInTx(ctx, h.queries, &pending, trigger(memberB)); !errors.Is(err, ErrRunAgentNotBound) {
		t.Fatalf("member B without a binding: error = %v, want ErrRunAgentNotBound", err)
	}
	if _, err := EnqueueRunInTx(ctx, h.queries, &pending, trigger(uuid.Nil)); !errors.Is(err, ErrRunAgentNotBound) {
		t.Fatalf("no authorizer: error = %v, want ErrRunAgentNotBound", err)
	}
	if err := ProbeRunEnqueue(
		ctx,
		h.queries,
		h.fixture.CompanyA,
		h.fixture.AgentA,
		memberB,
	); !errors.Is(
		err,
		ErrRunAgentNotBound,
	) {
		t.Fatalf("probe for member B: error = %v, want ErrRunAgentNotBound", err)
	}
	if got := countRuns(t, h); got != before || len(pending) != 0 {
		t.Fatalf("refused enqueues wrote %d runs and staged %d events", got-before, len(pending))
	}

	// The registrant's own request runs on the machine their binding names.
	run, err := EnqueueRunInTx(ctx, h.queries, &pending, trigger(h.fixture.UserA))
	if err != nil {
		t.Fatalf("registrant enqueue: %v", err)
	}
	if !run.ExecutionAuthorizedByUserID.Valid || run.ExecutionAuthorizedByUserID.UUID != h.fixture.UserA ||
		run.RuntimeID != h.fixture.RuntimeA {
		t.Fatalf(
			"run = authorizer %+v on %s, want the registrant on their runtime",
			run.ExecutionAuthorizedByUserID,
			run.RuntimeID,
		)
	}

	// Member B sets the Agent up on their own machine: their Run lands
	// there, and the registrant's machine never hears of it.
	machineB := seedRuntime(t, h, memberB, "codex", "daemon-b")
	bind(t, h, h.fixture.AgentA, memberB, machineB, "codex")
	if err := ProbeRunEnqueue(ctx, h.queries, h.fixture.CompanyA, h.fixture.AgentA, memberB); err != nil {
		t.Fatalf("probe for a bound member B: %v", err)
	}
	bound, err := EnqueueRunInTx(ctx, h.queries, &pending, trigger(memberB))
	if err != nil {
		t.Fatalf("member B on their own binding: %v", err)
	}
	if !bound.ExecutionAuthorizedByUserID.Valid || bound.ExecutionAuthorizedByUserID.UUID != memberB ||
		bound.RuntimeID != machineB {
		t.Fatalf(
			"bound run = authorizer %+v on %s, want member B on their machine",
			bound.ExecutionAuthorizedByUserID,
			bound.RuntimeID,
		)
	}

	// A member who lost their standing is refused, binding or not.
	dbtest.MustExec(t, ctx, h.pool,
		`UPDATE company_members SET status = 'revoked' WHERE company_id = $1 AND user_id = $2`,
		h.fixture.CompanyA, memberB)
	if err := ProbeRunEnqueue(
		ctx,
		h.queries,
		h.fixture.CompanyA,
		h.fixture.AgentA,
		memberB,
	); !errors.Is(
		err,
		ErrRunAgentNotBound,
	) {
		t.Fatalf("probe for a revoked member: error = %v, want ErrRunAgentNotBound", err)
	}
}

// A Task Run is the assignee's alone (ADR 0014): the kernel's Task brief
// answers no row for any other authorizer, even one who has the Agent set
// up, so nothing is written.
func TestEnqueueRefusesATaskRunFromAnyoneButTheAssignee(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	memberB := seedMemberB(t, h)
	bind(t, h, h.fixture.AgentA, memberB, seedRuntime(t, h, memberB, "codex", "daemon-b"), "codex")
	before := countRuns(t, h)

	var pending []events.Event
	trigger := RunTrigger{
		CompanyID: h.fixture.CompanyA, ConversationID: h.fixture.ConversationA, AgentID: h.fixture.AgentA,
		TaskID: uuid.NullUUID{UUID: h.fixture.TaskA, Valid: true}, Source: RunSourceExecution,
		TriggerPrompt: "start", ActorUserID: memberB, AuthorizedByUserID: memberB,
	}
	if _, err := EnqueueRunInTx(ctx, h.queries, &pending, trigger); !errors.Is(err, ErrRunTaskAssigneeRequired) {
		t.Fatalf("member B starting the assignee's Task: error = %v, want ErrRunTaskAssigneeRequired", err)
	}
	if got := countRuns(t, h); got != before {
		t.Fatalf("refused enqueue wrote %d runs", got-before)
	}
	trigger.ActorUserID, trigger.AuthorizedByUserID = h.fixture.UserA, h.fixture.UserA
	run, err := EnqueueRunInTx(ctx, h.queries, &pending, trigger)
	if err != nil {
		t.Fatalf("assignee starting their Task: %v", err)
	}
	if run.RuntimeID != h.fixture.RuntimeA {
		t.Fatalf("assignee's run on %s, want their binding %s", run.RuntimeID, h.fixture.RuntimeA)
	}
}

// The claim rechecks the placement against the rows as they are now: a
// queued Run whose authorizer no longer owns the runtime, or whose binding
// is gone, is not a candidate on either claim path, and becomes one again
// when the facts return. A retry child resolves the authorizer's CURRENT
// binding rather than copying the parent's runtime, and there is no retry
// without one.
func TestClaimRefusesARunWhoseAuthorizerDoesNotOwnTheRuntime(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	memberB := seedMemberB(t, h)
	machineB := seedRuntime(t, h, memberB, "codex", "daemon-b")
	bind(t, h, h.fixture.AgentA, memberB, machineB, "codex")
	conversation := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, h.pool, `
		INSERT INTO conversations (id, company_id, kind, agent_id, created_by_user_id, title)
		VALUES ($1, $2, 'direct', $3, $4, 'B asks')`,
		conversation, h.fixture.CompanyA, h.fixture.AgentA, memberB)
	var pending []events.Event
	queued, err := EnqueueRunInTx(ctx, h.queries, &pending, RunTrigger{
		CompanyID: h.fixture.CompanyA, ConversationID: conversation, AgentID: h.fixture.AgentA,
		TriggerPrompt: "hello", Source: RunSourceMessage, ActorUserID: memberB, AuthorizedByUserID: memberB,
	})
	if err != nil {
		t.Fatalf("enqueue on member B's machine: %v", err)
	}

	expectClaim := func(want bool, why string) {
		t.Helper()
		// The empty-claim candidate cache and the claim itself must agree
		// (TestClaimCandidateCacheAgreesWithFence in spirit): both read one
		// predicate.
		candidates, err := h.queries.ListQueuedClaimCandidatesByRuntimes(ctx, claimCandidateParams(machineB))
		if err != nil {
			t.Fatalf("list candidates (%s): %v", why, err)
		}
		if (len(candidates) > 0) != want {
			t.Fatalf("%s: candidates = %d, want present=%t", why, len(candidates), want)
		}
		claimed, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, machineB)
		if err != nil {
			t.Fatalf("claim (%s): %v", why, err)
		}
		if (claimed != nil) != want {
			t.Fatalf("%s: claimed = %v, want claimed=%t", why, claimed != nil, want)
		}
	}

	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`UPDATE runtimes SET owner_user_id = $1 WHERE id = $2`,
		h.fixture.UserA,
		machineB,
	)
	expectClaim(false, "the runtime is another member's")
	dbtest.MustExec(t, ctx, h.pool, `UPDATE runtimes SET owner_user_id = $1 WHERE id = $2`, memberB, machineB)
	dbtest.MustExec(t, ctx, h.pool, `UPDATE runtimes SET status = 'offline' WHERE id = $1`, machineB)
	expectClaim(false, "the runtime is offline")
	dbtest.MustExec(t, ctx, h.pool, `UPDATE runtimes SET status = 'online' WHERE id = $1`, machineB)
	dbtest.MustExec(t, ctx, h.pool, `DELETE FROM agent_runtime_bindings WHERE agent_id = $1 AND user_id = $2`,
		h.fixture.AgentA, memberB)
	expectClaim(false, "the binding is gone")
	bind(t, h, h.fixture.AgentA, memberB, machineB, "codex")
	expectClaim(true, "owned, bound and online again")

	var status string
	if err := h.pool.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, queued.ID).
		Scan(&status); err != nil ||
		status != "dispatched" {
		t.Fatalf("run after claim = %s, %v; want dispatched", status, err)
	}

	// The retry child is placed by the authorizer's binding at retry time:
	// a binding moved to another of their machines takes the retry with it,
	// and a missing binding means no retry at all.
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`UPDATE runs SET status = 'failed', failure_reason = 'runtime_offline' WHERE id = $1`,
		queued.ID,
	)
	machineB2 := seedRuntime(t, h, memberB, "codex", "daemon-b2")
	bind(t, h, h.fixture.AgentA, memberB, machineB2, "codex")
	child, err := createRetryRunInTx(ctx, h.queries, mustRun(t, h, queued.ID))
	if err != nil || child == nil {
		t.Fatalf("createRetryRunInTx() = %v, %v", child, err)
	}
	if !child.ExecutionAuthorizedByUserID.Valid || child.ExecutionAuthorizedByUserID.UUID != memberB ||
		child.RuntimeID != machineB2 {
		t.Fatalf("retry = authorizer %+v on %s, want member B on their current machine %s",
			child.ExecutionAuthorizedByUserID, child.RuntimeID, machineB2)
	}
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`UPDATE runs SET status = 'failed', failure_reason = 'runtime_offline' WHERE id = $1`,
		child.ID,
	)
	dbtest.MustExec(t, ctx, h.pool, `DELETE FROM agent_runtime_bindings WHERE agent_id = $1 AND user_id = $2`,
		h.fixture.AgentA, memberB)
	if orphan, err := createRetryRunInTx(ctx, h.queries, mustRun(t, h, child.ID)); err != nil || orphan != nil {
		t.Fatalf("createRetryRunInTx(no binding) = %v, %v; want no retry", orphan, err)
	}
}

// Capacity is per binding (ADR 0014): the same Agent run by two members on
// their own machines gets a full task lane on each, so one member's live
// work never blocks the other's.
func TestCapacityIsPerBinding(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	memberB := seedMemberB(t, h)
	machineB := seedRuntime(t, h, memberB, "codex", "daemon-b")
	bind(t, h, h.fixture.AgentA, memberB, machineB, "codex")
	dbtest.MustExec(t, ctx, h.pool, `UPDATE agents SET max_concurrent_runs = 1 WHERE id = $1`, h.fixture.AgentA)
	// Member B's own Task on their machine, alongside the fixture's Task A
	// that the registrant runs.
	taskB, conversationB := dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, h.pool, `
		INSERT INTO conversations (id, company_id, kind, title, project_id) VALUES ($1, $2, 'task', 'Task of B', $3)`,
		conversationB, h.fixture.CompanyA, h.fixture.ProjectA)
	dbtest.MustExec(t, ctx, h.pool, `
		INSERT INTO tasks (id, company_id, conversation_id, project_id, milestone_type, number, key, title,
			assignee_user_id, executed_by_agent_id, creator_user_id, status)
		VALUES ($1, $2, $3, $4, 'product', 7, 'task_of_b', 'Task of B', $5, $6, $5, 'in_progress')`,
		taskB, h.fixture.CompanyA, conversationB, h.fixture.ProjectA, memberB, h.fixture.AgentA)
	dbtest.MustExec(t, ctx, h.pool, `UPDATE conversations SET task_id = $1 WHERE id = $2`, taskB, conversationB)
	dbtest.MustExec(t, ctx, h.pool, `UPDATE tasks SET status = 'in_progress' WHERE id = $1`, h.fixture.TaskA)

	var pending []events.Event
	enqueue := func(conversationID, taskID, authorizer uuid.UUID) generated.Run {
		t.Helper()
		run, err := EnqueueRunInTx(ctx, h.queries, &pending, RunTrigger{
			CompanyID: h.fixture.CompanyA, ConversationID: conversationID, AgentID: h.fixture.AgentA,
			TaskID: uuid.NullUUID{UUID: taskID, Valid: true}, Source: RunSourceExecution,
			TriggerPrompt: "go", ActorUserID: authorizer, AuthorizedByUserID: authorizer,
		})
		if err != nil {
			t.Fatalf("enqueue for %s: %v", authorizer, err)
		}
		return run
	}
	runA := enqueue(h.fixture.ConversationA, h.fixture.TaskA, h.fixture.UserA)
	runB := enqueue(conversationB, taskB, memberB)

	claimedA, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil || claimedA == nil || claimedA.ID != runA.ID {
		t.Fatalf("claim on the registrant's machine = %v, %v; want run A", claimedA, err)
	}
	// The registrant's lane is full at one; member B's lane on their own
	// machine is untouched by it.
	claimedB, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, machineB)
	if err != nil || claimedB == nil || claimedB.ID != runB.ID {
		t.Fatalf(
			"claim on member B's machine = %v, %v; want run B despite the registrant's live run",
			claimedB,
			err,
		)
	}
}

// A confirmed handoff cancels every live Run of the Task — queued, deferred
// or already claimed — with the task_reassigned reason, and touches no
// other Task's work.
func TestHandoffCancelsEveryLiveRunOfTheTask(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	seed := func(conversationID, taskID uuid.UUID, status string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := h.pool.QueryRow(ctx, `
			INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id,
				execution_authorized_by_user_id, status, fire_at, run_type, task_execution_generation)
			VALUES ($1, $2, $3, $4, $5, $6, $7,
				CASE WHEN $7 = 'deferred' THEN now() + interval '1 hour' END, 'task', 1)
			RETURNING id`,
			h.fixture.CompanyA, h.fixture.AgentA, taskID, conversationID, h.fixture.RuntimeA,
			h.fixture.UserA, status,
		).Scan(&id); err != nil {
			t.Fatalf("seed %s run: %v", status, err)
		}
		return id
	}
	queued := seed(h.fixture.ConversationA, h.fixture.TaskA, "queued")
	running := seed(h.fixture.ConversationA, h.fixture.TaskA, "running")
	otherConversation, otherTask := dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, h.pool, `
		INSERT INTO conversations (id, company_id, kind, title, project_id) VALUES ($1, $2, 'task', 'Other', $3)`,
		otherConversation, h.fixture.CompanyA, h.fixture.ProjectA)
	dbtest.MustExec(t, ctx, h.pool, `
		INSERT INTO tasks (id, company_id, conversation_id, project_id, milestone_type, number, key, title,
			assignee_user_id, executed_by_agent_id, creator_user_id)
		VALUES ($1, $2, $3, $4, 'product', 8, 'other', 'Other', $5, $6, $5)`,
		otherTask, h.fixture.CompanyA, otherConversation, h.fixture.ProjectA, h.fixture.UserA, h.fixture.AgentA)
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`UPDATE conversations SET task_id = $1 WHERE id = $2`,
		otherTask,
		otherConversation,
	)
	untouched := seed(otherConversation, otherTask, "queued")
	logged := captureRunCancelledLines(t)

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var pending []events.Event
	cancelled, err := CancelTaskRunsForHandoffInTx(ctx, generated.New(tx), &pending, TaskHandoff{
		CompanyID: h.fixture.CompanyA, TaskID: h.fixture.TaskA,
	})
	if err != nil {
		t.Fatalf("CancelTaskRunsForHandoffInTx() error = %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(cancelled) != 2 {
		t.Fatalf("cancelled %d runs, want the queued and running ones", len(cancelled))
	}
	for _, id := range []uuid.UUID{queued, running} {
		run := mustRun(t, h, id)
		if run.Status != "cancelled" || !run.Error.Valid || run.Error.String != RunReasonTaskReassigned {
			t.Fatalf("run %s = %s / %+v, want cancelled with task_reassigned", id, run.Status, run.Error)
		}
	}
	if run := mustRun(t, h, untouched); run.Status != "queued" {
		t.Fatalf("the other Task's run = %s, want still queued", run.Status)
	}
	// The pass stages exactly one run.cancelled frame per cancelled Run.
	cancelledEvents := 0
	for _, event := range pending {
		if event.EntityType == Topic && event.Name == EventRunCancelled && event.RunID != uuid.Nil {
			cancelledEvents++
		}
	}
	if cancelledEvents != 2 {
		t.Fatalf("staged %d run.cancelled events, want one per cancelled Run", cancelledEvents)
	}
	// And the server log says so, the way a single cancel does: an operator
	// reading it sees why the old machine dropped its work.
	for _, id := range []uuid.UUID{queued, running} {
		if reason, ok := logged.reason(id); !ok || reason != RunReasonTaskReassigned {
			t.Fatalf(
				"run cancelled log for %s = %q (logged %v), want reason task_reassigned",
				id,
				reason,
				ok,
			)
		}
	}
	if _, ok := logged.reason(untouched); ok {
		t.Fatalf("the other Task's queued run was logged as cancelled")
	}
}

// runCancelledLines records the "run cancelled" INFO lines the process
// logs while a test runs, keyed by run id.
type runCancelledLines struct {
	mu      sync.Mutex
	reasons map[string]string
}

func (l *runCancelledLines) reason(runID uuid.UUID) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	reason, ok := l.reasons[runID.String()]
	return reason, ok
}

type runCancelledHandler struct {
	slog.Handler
	lines *runCancelledLines
}

func (h runCancelledHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h runCancelledHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Level != slog.LevelInfo || record.Message != "run cancelled" {
		return nil
	}
	var runID, reason string
	record.Attrs(func(attr slog.Attr) bool {
		switch attr.Key {
		case "run_id":
			runID = attr.Value.String()
		case "reason":
			reason = attr.Value.String()
		}
		return true
	})
	h.lines.mu.Lock()
	defer h.lines.mu.Unlock()
	h.lines.reasons[runID] = reason
	return nil
}

// captureRunCancelledLines swaps the default logger for the test. The test
// must not run in parallel with another that logs.
func captureRunCancelledLines(t *testing.T) *runCancelledLines {
	t.Helper()
	lines := &runCancelledLines{reasons: map[string]string{}}
	previous := slog.Default()
	slog.SetDefault(slog.New(runCancelledHandler{Handler: slog.DiscardHandler, lines: lines}))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return lines
}
