package task

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// ListDependencies reads both dependency directions of one Task. Inside a
// Run the Task itself must be the Run's own; its prerequisites then come
// with it as read-only context, the same summaries a Task Run's prompt
// already carries, and of its dependents only the Run's own.
func (s *DefaultTaskService) ListDependencies(
	ctx context.Context,
	principal auth.UserPrincipal,
	taskID uuid.UUID,
	run *RunScope,
) (DependencySet, error) {
	queries := s.store.Queries()
	value, err := getTask(ctx, queries, principal, taskID)
	if err == nil {
		err = run.admitRead(ctx, queries, value)
	}
	if err != nil {
		return DependencySet{}, fmt.Errorf("authorize Task dependency list: %w", err)
	}
	result, err := listDependencies(ctx, queries, principal, taskID)
	if err != nil {
		return DependencySet{}, fmt.Errorf("list Task dependencies: %w", err)
	}
	return run.dependencies(result), nil
}

func (s *DefaultTaskService) ListDependencyGraph(
	ctx context.Context,
	principal auth.UserPrincipal,
	options DependencyGraphOptions,
) (DependencyGraph, error) {
	queries := s.store.Queries()
	if _, err := queries.GetActiveCompanyMembership(ctx, generated.GetActiveCompanyMembershipParams{
		CompanyID: principal.CompanyID, UserID: principal.UserID,
	}); errors.Is(err, pgx.ErrNoRows) {
		return DependencyGraph{}, ErrNotFound
	} else if err != nil {
		return DependencyGraph{}, fmt.Errorf("authorize Task dependency graph: %w", err)
	}
	result, err := listDependencyGraph(ctx, queries, principal, options)
	if err != nil {
		return DependencyGraph{}, fmt.Errorf("list Task dependency graph: %w", err)
	}
	return result, nil
}

// AdvanceDependencyProjectionInTx advances the Task revision after a caller
// mutates dependencies inside its own transaction. Dependency counts are part
// of the canonical Task projection, so the Task's ordering token must advance
// even though the changed rows live in task_dependencies.
func AdvanceDependencyProjectionInTx(
	ctx context.Context,
	q *generated.Queries,
	companyID, taskID uuid.UUID,
	expectedRevision int64,
) error {
	_, err := q.AdvanceTaskRevisionAfterDependencyChange(
		ctx,
		generated.AdvanceTaskRevisionAfterDependencyChangeParams{
			TaskID: taskID, CompanyID: companyID, ExpectedVersion: expectedRevision,
		},
	)
	if err == nil {
		return nil
	}
	return mapTaskMutationError("advance Task dependency projection", err)
}

// DependenciesResolvedFlips reports whether a prerequisite moving from one
// status to another changes its dependents' dependenciesResolved projection.
// Only `done` counts as resolved, so the flip is a move onto or off it. It is
// exported because a Company Plan apply restates statuses in its own
// transaction and owes its dependents the same task.updated frames.
func DependenciesResolvedFlips(previous, next Status) bool {
	return (previous == StatusDone) != (next == StatusDone)
}

// stageDependentProjectionEventsInTx announces the dependents whose
// dependenciesResolved projection a prerequisite status change flipped. Each
// dependent's revision advances first, so a delayed task.updated frame from
// before the flip cannot overwrite the announced one, and the frame carries
// the dependent's full projection like every other task.updated. The
// dependents' own columns do not change, so no Activity is recorded for them.
func stageDependentProjectionEventsInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	actor events.Actor,
	companyID, prerequisiteID uuid.UUID,
	previousStatus, newStatus Status,
) error {
	if !DependenciesResolvedFlips(previousStatus, newStatus) {
		return nil
	}
	dependents, err := q.LockDependentTasksForProjectionChange(
		ctx,
		generated.LockDependentTasksForProjectionChangeParams{
			CompanyID: companyID, PrerequisiteTaskID: prerequisiteID,
		},
	)
	if err != nil {
		return fmt.Errorf("lock dependents of Task %s: %w", prerequisiteID, err)
	}
	for _, dependent := range dependents {
		if err := AdvanceDependencyProjectionInTx(
			ctx, q, companyID, dependent.ID, dependent.Version,
		); err != nil {
			return err
		}
		projection, err := q.GetTaskProjectionForSystem(ctx, systemProjectionParams(companyID, dependent.ID))
		if err != nil {
			return fmt.Errorf("project dependent Task %s: %w", dependent.ID, err)
		}
		if err := stageTaskEvent(
			ctx, q, pending,
			newTaskEventForActor(actor, EventUpdated, responseFromSystemProjectionRow(projection)),
		); err != nil {
			return err
		}
	}
	return nil
}
