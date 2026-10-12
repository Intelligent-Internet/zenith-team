package execution

// Runtime ownership at the kernel (ADR 0014). A runtime executes only its
// registrant's own work, and an Agent is a template: the machine a Run
// executes on is the authorizing member's own binding of that Agent. The
// enqueue resolves the binding below and stores the runtime on the Run; the
// claim rechecks the same facts in SQL (SelectClaimableRun) against the
// row's current state — the authorizer still owns the runtime, is still an
// enabled, active member, and the binding still exists — so a binding
// removed between the two is honored at dispatch. Nothing here grants
// anyone else's machine to anyone: there is no share to revoke.

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/events"
	db "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// ErrRunAgentNotBound signals that the enqueue refused to queue work because
// the authorizing member has not bound the Agent to one of their own live
// runtimes — or the authorizer is unknown, disabled or no longer an active
// member. Productizable — surface as "set this Agent up on one of your
// computers" rather than retrying; nothing was written.
var ErrRunAgentNotBound = errors.New("run enqueue: agent not bound for the authorizing member")

// ErrRunTaskAssigneeRequired signals that a Task Run was asked for by someone
// other than the Task's assignee. Only the assignee starts a Task's Runs,
// because the Run executes on the assignee's own computer (ADR 0014).
// Productizable — surface as "only the assignee can start this task";
// nothing was written.
var ErrRunTaskAssigneeRequired = errors.New("run enqueue: task run authorized by someone other than the assignee")

// Cancellation reasons the binding and handoff passes write onto runs.error.
// Exported for the callers in internal/agent, which name the reason for the
// pass they run.
const (
	// RunReasonTaskReassigned is the reason a confirmed handoff cancels the
	// Task's live work with. It is protocol vocabulary: the daemon reads it
	// back from the run status to supersede the Run's worktree.
	RunReasonTaskReassigned = protocol.RunCancelReasonTaskReassigned
	// RunReasonAgentUnbound is the reason an unbind cancels the member's
	// queued work on that Agent with.
	RunReasonAgentUnbound = "agent_unbound"
	// RunReasonRuntimeRemoved is the reason a runtime removal without a target
	// cancels the registrant's queued work with.
	RunReasonRuntimeRemoved = "runtime_removed"
	// RunReasonTaskDeleted is the reason a Company Plan apply that deletes a
	// Task by omission cancels its live work with.
	RunReasonTaskDeleted = "task_deleted"
)

// runBinding is the resolved execution placement of one enqueue: the
// runtime the authorizer's binding names.
type runBinding struct {
	ID        uuid.UUID
	RuntimeID uuid.UUID
	Provider  string
}

// resolveRunBinding is the enqueue-side placement: which of the authorizer's
// runtimes executes this Agent. It fails closed — no authorizer, no binding,
// a binding on a removed runtime or one the member no longer owns, a
// disabled account or a revoked membership all refuse. The read holds the
// binding row FOR SHARE, so an unbind or a runtime removal in flight waits
// for this enqueue to commit and then retargets or cancels the Run it wrote.
// The claim never calls it: it already holds a Run row lock, and its fence
// in SelectClaimableRun checks the binding without a lock.
func resolveRunBinding(
	ctx context.Context,
	q *db.Queries,
	companyID, agentID, authorizedByUserID uuid.UUID,
) (runBinding, error) {
	if authorizedByUserID == uuid.Nil {
		return runBinding{}, ErrRunAgentNotBound
	}
	row, err := q.GetAgentRuntimeBindingForRun(ctx, db.GetAgentRuntimeBindingForRunParams{
		CompanyID: companyID, AgentID: agentID, UserID: authorizedByUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return runBinding{}, ErrRunAgentNotBound
	}
	if err != nil {
		return runBinding{}, fmt.Errorf("resolve run binding: %w", err)
	}
	return runBinding{ID: row.ID, RuntimeID: row.RuntimeID, Provider: row.Provider}, nil
}

// TaskHandoff identifies the Task whose live work a confirmed handoff ends.
type TaskHandoff struct {
	CompanyID uuid.UUID
	TaskID    uuid.UUID
}

// CancelTaskRunsForHandoffInTx ends the Task's live work before its assignee
// or executing Agent changes (ADR 0014): every queued, deferred, claimed or
// executing Run of the Task is cancelled with reason task_reassigned, its Run
// tokens are deleted so the old machine's next authenticated call fails and
// it discards the result, and run.cancelled is staged for the daemon and the
// Task's viewers. The Task row itself is untouched: the caller advances the
// execution generation with its own write, and the terminal transition a
// cancelled Run would have applied is exactly what a handoff must not apply.
// The caller holds the Company, membership, Conversation and Task locks and
// owns the transaction.
func CancelTaskRunsForHandoffInTx(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	input TaskHandoff,
) ([]db.Run, error) {
	candidates, err := q.ListActiveRunsForTaskHandoff(ctx, db.ListActiveRunsForTaskHandoffParams{
		CompanyID: input.CompanyID, TaskID: input.TaskID,
	})
	if err != nil {
		return nil, fmt.Errorf("list Task Runs for handoff: %w", err)
	}
	return cancelRunCandidatesInTx(ctx, q, pending, input.CompanyID, candidates, RunReasonTaskReassigned)
}

// CancelDeletedTaskRunsInTx ends the live work of a Task about to be deleted:
// every queued, deferred, claimed or executing Run of it is cancelled with
// reason task_deleted, its tokens deleted and run.cancelled staged, exactly
// as a handoff does. Without it the Runs would outlive their Task: runs.task_id
// goes NULL on the delete while the Task chat keeps its Project, so a queued
// one would still be claimed and run for work nobody holds any more. The
// caller holds the Company, membership, Conversation and Task locks and owns
// the transaction, and deletes the Task after this returns.
func CancelDeletedTaskRunsInTx(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	companyID, taskID uuid.UUID,
) ([]db.Run, error) {
	candidates, err := q.ListActiveRunsForTaskHandoff(ctx, db.ListActiveRunsForTaskHandoffParams{
		CompanyID: companyID, TaskID: taskID,
	})
	if err != nil {
		return nil, fmt.Errorf("list Runs of a deleted Task: %w", err)
	}
	return cancelRunCandidatesInTx(ctx, q, pending, companyID, candidates, RunReasonTaskDeleted)
}

// BindingWork names a member's unclaimed work on some Agents of one runtime:
// what moves when a binding moves and what is cancelled when one goes.
type BindingWork struct {
	CompanyID       uuid.UUID
	SourceRuntimeID uuid.UUID
	UserID          uuid.UUID
	AgentIDs        []uuid.UUID
}

// RetargetPendingRunsForBindingsInTx moves the member's pending work on the
// named Agents from one of their runtimes to another inside the CALLER'S
// transaction — the participant a rebind and a runtime removal with a target
// call after they move the bindings. Only queued and deferred rows move:
// nobody has claimed them, so the target's daemon picks them up as if they
// had been enqueued there. Dispatched, running and waiting rows stay on the
// source, which is still the same member's machine; the offline sweepers
// own that terminal transition. Returns the moved rows so the caller can
// publish one runtime.work_available per run after commit
// (PublishRuntimeWork) — a wakeup hint, not a run.queued re-emission: the
// run's identity and routing ids are unchanged. An empty agent set moves
// nothing and runs no statement.
//
// This package is imported by internal/agent for this seam; it must never
// import internal/agent back.
func RetargetPendingRunsForBindingsInTx(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	actor events.Actor,
	work BindingWork,
	targetRuntimeID uuid.UUID,
) ([]db.Run, error) {
	if len(work.AgentIDs) == 0 {
		return nil, nil
	}
	ids, err := q.ListPendingRunsForBindingsForUpdate(ctx, db.ListPendingRunsForBindingsForUpdateParams{
		CompanyID: work.CompanyID, SourceRuntimeID: work.SourceRuntimeID,
		UserID: work.UserID, AgentIds: work.AgentIDs,
	})
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	beforeHistory, err := ObserveRunsInTx(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	moved, err := q.RetargetPendingRunsForBindings(ctx, db.RetargetPendingRunsForBindingsParams{
		TargetRuntimeID: targetRuntimeID,
		CompanyID:       work.CompanyID,
		SourceRuntimeID: work.SourceRuntimeID,
		UserID:          work.UserID,
		AgentIds:        work.AgentIDs,
		RunIds:          ids,
	})
	if err != nil {
		return nil, fmt.Errorf("retarget pending runs: %w", err)
	}
	for _, run := range moved {
		if err := activity.RecordInTx(ctx, q, pending, actor, "run.updated", beforeHistory[run.ID], Topic, run.ID); err != nil {
			return nil, err
		}
	}
	return moved, nil
}

// CancelPendingRunsForBindingsInTx cancels the member's pending work on the
// named Agents of one runtime: what an unbind (reason agent_unbound) or a
// runtime removal without a target (runtime_removed) leaves with no machine
// to run on. Only queued and deferred rows: a Run a daemon already claimed is
// executing on the member's own machine and finishes there. The caller
// holds the runtime and binding locks and owns the transaction.
func CancelPendingRunsForBindingsInTx(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	work BindingWork,
	reason string,
) ([]db.Run, error) {
	if len(work.AgentIDs) == 0 {
		return nil, nil
	}
	if _, err := q.ListPendingRunsForBindingsForUpdate(ctx, db.ListPendingRunsForBindingsForUpdateParams{
		CompanyID: work.CompanyID, SourceRuntimeID: work.SourceRuntimeID,
		UserID: work.UserID, AgentIds: work.AgentIDs,
	}); err != nil {
		return nil, err
	}
	candidates, err := q.ListPendingRunsForBindings(ctx, db.ListPendingRunsForBindingsParams{
		CompanyID: work.CompanyID, SourceRuntimeID: work.SourceRuntimeID,
		UserID: work.UserID, AgentIds: work.AgentIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("list pending runs for cancellation: %w", err)
	}
	return cancelRunCandidatesInTx(ctx, q, pending, work.CompanyID, candidates, reason)
}
