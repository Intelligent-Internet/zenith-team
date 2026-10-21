//go:build integration

package execution

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// The task-lane capacity gate: agents.max_concurrent_runs caps how many
// TASK-lane runs (runs.lane = 'task') an agent may have in flight, counted
// under the agent-row lock inside the claim transaction. Direct-conversation
// runs bypass the gate in both directions.

// seedTask inserts one task plus its task-kind conversation in CompanyA,
// assigned to AgentA. The two rows are circular (conversations.task_id and
// tasks.conversation_id), so both land in one statement and let the deferred
// FK settle at its commit.
func (h *runHarness) seedTask(t *testing.T, number int64) (taskID, conversationID uuid.UUID) {
	t.Helper()
	taskID = newPGUUID(t)
	conversationID = newPGUUID(t)
	dbtest.MustExec(t, context.Background(), h.pool, `
		WITH conversation_row AS (
			INSERT INTO conversations (id, company_id, kind, task_id, title, project_id)
			VALUES ($1, $2, 'task', $3, 'Capacity Task', (SELECT id FROM projects WHERE company_id=$2 AND key='work'))
		)
				INSERT INTO tasks
			(project_id, id, company_id, conversation_id,
			 milestone_type, number, key, title,
			 executed_by_agent_id, assignee_user_id, creator_user_id)
		VALUES ((SELECT id FROM projects WHERE company_id=$2 AND key='work'), $3, $2, $1, 'product', $4, $5,
		        'Capacity Task', $6, $7, $7)`,
		conversationID, h.fixture.CompanyA, taskID, number,
		fmt.Sprintf("cap_task_%d", number),
		h.fixture.AgentA, h.fixture.UserA)
	return taskID, conversationID
}

// seedTaskRun inserts an AgentA run on RuntimeA bound to taskID with the
// given status and priority. The conversation it names is what puts the run in
// the task lane -- the gate counts runs whose conversation is kind 'task' --
// so a seed pointing at a direct conversation would build a row the gate does
// not count and these tests would measure nothing.
func (h *runHarness) seedTaskRun(
	t *testing.T,
	taskID, conversationID uuid.UUID,
	status string,
	priority int32,
) uuid.UUID {
	t.Helper()
	var runID uuid.UUID
	if err := h.pool.QueryRow(
		context.Background(), `
		INSERT INTO runs
			(company_id, agent_id, task_id, conversation_id, runtime_id, status, priority, run_type,
			 execution_authorized_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ), $8)
		RETURNING id`,
		h.fixture.CompanyA, h.fixture.AgentA, taskID, conversationID,
		h.fixture.RuntimeA, status, priority, h.fixture.UserA,
	).Scan(&runID); err != nil {
		t.Fatalf("seed task run: %v", err)
	}
	return runID
}

func (h *runHarness) setAgentCap(t *testing.T, agentID uuid.UUID, maxConcurrentRuns int32) {
	t.Helper()
	dbtest.MustExec(t, context.Background(), h.pool,
		`UPDATE agents SET max_concurrent_runs = $2 WHERE id = $1`, agentID, maxConcurrentRuns)
}

// mustClaim asserts one ClaimRun outcome: wantRun == uuid.Nil means "no run".
func (h *runHarness) mustClaim(t *testing.T, label string, wantRun uuid.UUID) {
	t.Helper()
	claimed, err := h.svc.ClaimRun(context.Background(), h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil {
		t.Fatalf("%s: claim: %v", label, err)
	}
	if wantRun == uuid.Nil {
		if claimed != nil {
			t.Fatalf("%s: claim = run %d, want none", label, claimed.ID)
		}
		return
	}
	if claimed == nil || claimed.ID != wantRun {
		t.Fatalf("%s: claim = %+v, want run %d", label, claimed, wantRun)
	}
}

func TestClaimRunTaskLaneCapacityGate(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	h.setAgentCap(t, h.fixture.AgentA, 1)
	// Two queued task-lane runs on distinct tasks: the per-(task, agent)
	// serialization arm and runs_one_pending_per_task_agent_idx both forbid a
	// second pending run for one task, so the cap needs two tasks to bite.
	taskOne, conversationOne := h.seedTask(t, 101)
	taskTwo, conversationTwo := h.seedTask(t, 102)
	first := h.seedTaskRun(t, taskOne, conversationOne, "queued", 0)
	second := h.seedTaskRun(t, taskTwo, conversationTwo, "queued", 0)

	// Cap 1: the first claim dispatches, the second finds the lane closed and
	// leaves the candidate queued untouched.
	h.mustClaim(t, "first slot", first)
	h.mustClaim(t, "at capacity", uuid.Nil)

	var status string
	if err := h.pool.QueryRow(ctx,
		`SELECT status FROM runs WHERE id = $1`, second).Scan(&status); err != nil {
		t.Fatalf("read blocked run: %v", err)
	}
	if status != "queued" {
		t.Fatalf("blocked run status = %q, want queued untouched", status)
	}

	// A terminal transition frees the slot; the gate reads live status only.
	dbtest.MustExec(t, ctx, h.pool,
		`UPDATE runs SET status = 'completed', updated_at = now() WHERE id = $1`, first)
	h.mustClaim(t, "freed slot", second)
}

func TestClaimRunDirectLaneBypassesCapacity(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	h.setAgentCap(t, h.fixture.AgentA, 1)
	// The slot is held by an in-flight task-lane run.
	activeTask, activeConversation := h.seedTask(t, 111)
	occupying := h.seedTaskRun(t, activeTask, activeConversation, "running", 0)
	// A queued task candidate at HIGHER priority than the direct candidate:
	// the closed lane must skip it rather than block the claim on it.
	queuedTask, queuedTaskConversation := h.seedTask(t, 112)
	blockedRun := h.seedTaskRun(t, queuedTask, queuedTaskConversation, "queued", 5)
	directConversation := h.seedDirectConversation(t)
	directRun := h.seedQueuedRun(t, directConversation)

	// At capacity the claim still runs and sees only the direct lane, past
	// the higher-priority task candidate.
	h.mustClaim(t, "direct bypasses closed lane", directRun)
	// The dispatched direct run does not occupy the slot either, but the
	// running task run still does: the task candidate stays unclaimable.
	h.mustClaim(t, "task lane still closed", uuid.Nil)

	// Direct runs do not count toward the cap: with the task run terminal,
	// the lane is open even though the direct run is still dispatched.
	dbtest.MustExec(t, ctx, h.pool,
		`UPDATE runs SET status = 'completed', updated_at = now() WHERE id = $1`, occupying)
	h.mustClaim(t, "direct active run does not count", blockedRun)
}

func TestClaimRunCapacityArmKeepsOrderingWithinOpenLane(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)

	// Default cap (3), lane open: the boolean arm passes both lanes and the
	// existing priority DESC, created_at ASC order decides — the older direct
	// run must not leapfrog the higher-priority task run.
	directConversation := h.seedDirectConversation(t)
	directRun := h.seedQueuedRun(t, directConversation)
	task, taskConversation := h.seedTask(t, 121)
	taskRun := h.seedTaskRun(t, task, taskConversation, "queued", 3)

	h.mustClaim(t, "priority first", taskRun)
	h.mustClaim(t, "then FIFO", directRun)
}

// The lane is what the gate counts, not task_id. runs.task_id is
// ON DELETE SET NULL and applying a company plan deletes exactly the
// non-terminal tasks, so a run that is executing right now can lose its
// task_id — and if the gate read task_id it would stop counting a run that is
// still holding a slot, letting the agent dispatch past its cap.
func TestClaimRunCapacitySurvivesTaskDeletion(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	h.setAgentCap(t, h.fixture.AgentA, 1)
	occupyingTask, occupyingConversation := h.seedTask(t, 141)
	occupying := h.seedTaskRun(t, occupyingTask, occupyingConversation, "running", 0)
	queuedTask, queuedConversation := h.seedTask(t, 142)
	blocked := h.seedTaskRun(t, queuedTask, queuedConversation, "queued", 0)

	// Delete the busy task the way a plan apply does. The FK detaches it from
	// the still-running run rather than removing the run.
	dbtest.MustExec(t, ctx, h.pool, `DELETE FROM tasks WHERE id = $1`, occupyingTask)
	var taskID *uuid.UUID
	var conversationKind string
	if err := h.pool.QueryRow(
		ctx, `
		SELECT runs.task_id, conversations.kind
		FROM runs
		JOIN conversations ON conversations.id = runs.conversation_id
		WHERE runs.id = $1`, occupying,
	).Scan(&taskID, &conversationKind); err != nil {
		t.Fatalf("read detached run: %v", err)
	}
	if taskID != nil {
		t.Fatalf("task_id = %v, want NULL after the task delete", taskID)
	}
	// The whole reason the lane is read from the conversation and not from
	// task_id: conversations.task_id is ON DELETE SET NULL, so the
	// conversation outlives the task and keeps saying this is task work.
	if conversationKind != "task" {
		t.Fatalf("conversation kind = %q, want the lane to survive the task delete", conversationKind)
	}

	// The slot is still held, so the queued task run stays unclaimable.
	h.mustClaim(t, "detached run still holds its slot", uuid.Nil)

	dbtest.MustExec(t, ctx, h.pool,
		`UPDATE runs SET status = 'completed', updated_at = now() WHERE id = $1`, occupying)
	h.mustClaim(t, "slot returns when it finishes", blocked)
}

func TestClaimRunsForRuntimesSkipsOnlyTheAtCapacityAgent(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	// AgentA is at capacity: cap 1, one running task-lane run, one queued.
	h.setAgentCap(t, h.fixture.AgentA, 1)
	activeTask, activeConversation := h.seedTask(t, 131)
	h.seedTaskRun(t, activeTask, activeConversation, "running", 0)
	queuedTask, queuedConversation := h.seedTask(t, 132)
	h.seedTaskRun(t, queuedTask, queuedConversation, "queued", 0)

	// A second agent on the same runtime with a claimable direct run.
	otherAgent := newPGUUID(t)
	dbtest.SeedBoundAgent(t, ctx, h.pool, otherAgent, h.fixture.CompanyA, "Worker", "codex",
		&h.fixture.AgentA, h.fixture.UserA, h.fixture.RuntimeA)
	otherConversation := newPGUUID(t)
	dbtest.MustExec(t, ctx, h.pool, `
		INSERT INTO conversations (id, company_id, kind, agent_id, created_by_user_id, title)
		VALUES ($1, $2, 'direct', $3, $4, 'Direct')`,
		otherConversation, h.fixture.CompanyA, otherAgent, h.fixture.UserA)
	var otherRun uuid.UUID
	if err := h.pool.QueryRow(
		ctx, `
		INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id, execution_authorized_by_user_id, status, run_type)
		VALUES ($1, $2, $3, $4, $5, 'queued', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`,
		h.fixture.CompanyA, otherAgent, otherConversation, h.fixture.RuntimeA, h.fixture.UserA,
	).Scan(&otherRun); err != nil {
		t.Fatalf("seed other agent run: %v", err)
	}

	// The batch loop visits both (runtime, agent) pairs: AgentA's claim
	// commits empty and releases its agent-row lock; the other agent's claim
	// proceeds in the same poll.
	claimed, err := h.svc.ClaimRunsForRuntimes(ctx, []uuid.UUID{h.fixture.RuntimeA}, 8)
	if err != nil {
		t.Fatalf("batch claim: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != otherRun {
		t.Fatalf("batch claim = %+v, want exactly run %d from the open agent", claimed, otherRun)
	}
}
