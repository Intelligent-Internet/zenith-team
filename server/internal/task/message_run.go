package task

import (
	"context"
	"errors"
	"fmt"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/google/uuid"
)

// CheckMessageRunInTx applies the Task lifecycle and dependency admission
// policy before Conversation either queues a prompt or materializes its Run.
// Exact send replays bypass this method at the Conversation boundary.
//
// Every Run a Task chat starts is authorized by the Task's assignee
// (ADR 0014), so the assignee must hold project.tasks.execute on the Task's
// Project (ADR 0017); otherwise the send is refused with an
// execution.ExecutePermissionError. That includes a colleague's message, which is
// saved only for a Run the assignee would start: it would never be read.
func CheckMessageRunInTx(
	ctx context.Context,
	queries *generated.Queries,
	row generated.Task,
) (bool, error) {
	allowed, _ := taskMessageRunStatusPolicy(Status(row.Status))
	if !allowed {
		return false, nil
	}
	if err := ensureTaskExecutionEligible(ctx, queries, row.CompanyID, row.ID); err != nil {
		if errors.Is(err, ErrDependencyBlocked) || errors.Is(err, ErrProjectArchived) {
			return false, nil
		}
		return false, err
	}
	if err := requireAssigneeExecuteInTx(ctx, queries, row); err != nil {
		return false, err
	}
	if (row.AssignmentStatus == "pending" || row.AssignmentStatus == "declined") ||
		row.ScheduleDisposition == "scheduled" ||
		row.ScheduleDisposition == "cancelled" ||
		row.ScheduleDisposition == "missed" ||
		row.ScheduleDisposition == "skipped" {
		return false, nil
	}
	return true, nil
}

// PrepareMessageRunInTx moves an admitted Task back into progress at the
// instant its Message materializes a fresh Run. Conversation owns the caller's
// transaction and publishes the staged task.updated event after commit.
func PrepareMessageRunInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	actorUserID uuid.UUID,
	row generated.Task,
) (bool, error) {
	allowed, err := CheckMessageRunInTx(ctx, queries, row)
	if err != nil || !allowed {
		return allowed, err
	}
	_, update := taskMessageRunStatusPolicy(Status(row.Status))
	if !update {
		return true, nil
	}
	// Conversation already resolved the target and locked the active
	// membership. Keep this cross-domain seam scoped to the facts Task needs
	// instead of manufacturing a company-scoped principal for a target-scoped
	// route.
	previous := taskFromRow(row)
	target := previous
	target.Status = StatusInProgress
	updated, err := updateTask(
		ctx, queries, target, row.Version, pending, events.UserActor(actorUserID.String()), false,
		personWriteFence(actorUserID, auth.ProjectPermissionTasksExecute),
	)
	if err != nil {
		return false, fmt.Errorf("move Task into progress for Message Run: %w", err)
	}
	projection, err := queries.GetTaskProjectionForSystem(
		ctx,
		systemProjectionParams(updated.CompanyID, updated.ID),
	)
	if err != nil {
		return false, fmt.Errorf("project Task after Message Run transition: %w", err)
	}
	if err := stageTaskEvent(
		ctx, queries, pending,
		newTaskEvent(actorUserID, EventUpdated, responseFromSystemProjectionRow(projection)),
	); err != nil {
		return false, err
	}
	if err := reconcileTaskReportEvent(
		ctx,
		queries,
		pending,
		actorUserID,
		previous,
		updated,
	); err != nil {
		return false, fmt.Errorf("reconcile Task outcome report for Message Run: %w", err)
	}
	return true, nil
}

// PrepareContinuationRunInTx applies the Task lifecycle and dependency policy
// to an Approval or Question continuation. A failed Task requires a fresh
// member Message instead, so stale human-input receipts cannot reopen it. The
// lifecycle worker is a system writer, so it projects and attributes the
// transition without inventing a member principal. The caller already owns
// the Conversation and Task locks, and publishes the staged task.updated event
// after commit.
//
// A status that admits no continuation (closed or failed) answers false. A
// Task waiting on a prerequisite or in an archived Project answers
// ErrDependencyBlocked or ErrProjectArchived, so the caller can tell its
// owner which one it is.
func PrepareContinuationRunInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	row generated.Task,
) (bool, error) {
	if (row.AssignmentStatus == "pending" || row.AssignmentStatus == "declined") ||
		row.ScheduleDisposition == "scheduled" {
		return false, nil
	}
	allowed, update := taskContinuationRunStatusPolicy(Status(row.Status))
	if !allowed {
		return false, nil
	}
	if err := ensureTaskExecutionEligible(ctx, queries, row.CompanyID, row.ID); err != nil {
		return false, err
	}
	if !update {
		return true, nil
	}
	target := taskFromRow(row)
	target.Status = StatusInProgress
	updated, err := updateTask(
		ctx,
		queries,
		target,
		row.Version,
		pending,
		events.SystemActor(),
		false,
		projectWriteFence{},
	)
	if err != nil {
		return false, fmt.Errorf("move Task into progress for continuation Run: %w", err)
	}
	projection, err := queries.GetTaskProjectionForSystem(
		ctx,
		systemProjectionParams(updated.CompanyID, updated.ID),
	)
	if err != nil {
		return false, fmt.Errorf("project Task after continuation Run transition: %w", err)
	}
	if err := stageTaskEvent(
		ctx, queries, pending,
		newTaskEventForActor(
			events.SystemActor(),
			EventUpdated,
			responseFromSystemProjectionRow(projection),
		),
	); err != nil {
		return false, err
	}
	return true, nil
}

// taskMessageRunStatusPolicy is the admission rule for a fresh root Run on
// a Task — a member message, or an assignment handoff from create/update:
// allowed says whether the status admits one, update whether it moves the
// Task into progress when the Run materializes. One table, so a Task reaches
// in_progress the same way whichever write started its work.
func taskMessageRunStatusPolicy(status Status) (allowed, update bool) {
	switch status {
	case StatusBacklog, StatusTodo, StatusInReview, StatusBlocked, StatusFailed, StatusError:
		return true, true
	case StatusInProgress:
		return true, false
	case StatusDone, StatusCancelled:
		return false, false
	default:
		return false, false
	}
}

func taskContinuationRunStatusPolicy(status Status) (allowed, update bool) {
	if status == StatusFailed {
		return false, false
	}
	return taskMessageRunStatusPolicy(status)
}
