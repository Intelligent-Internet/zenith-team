package task

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// ApplyRunTerminalInTx applies the Task-facing outcome of one terminal Run.
// The SQL write is fenced by the Run's Task execution generation and current
// Agent assignment, so a newer trigger or handoff wins atomically. The caller
// owns the transaction and publishes the staged task.updated event post-commit.
func ApplyRunTerminalInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	run generated.Run,
) error {
	status, valid := runTerminalTaskStatus(run.Status)
	if queries == nil || pending == nil || !valid {
		return errors.New("terminal Run Task effect is invalid")
	}
	if !run.TaskID.Valid {
		return nil
	}
	before, err := activity.ObserveInTx(ctx, queries, Topic, run.TaskID.UUID)
	if err != nil {
		return err
	}
	if run.Status == "completed" {
		status, err = completedRunResultStatus(ctx, queries, run)
		if err != nil {
			return err
		}
	}
	updated, err := queries.UpdateTaskForRunTerminal(
		ctx,
		generated.UpdateTaskForRunTerminalParams{Status: string(status), RunID: run.ID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("update terminal Run Task: %w", err)
	}
	if run.Status == "completed" && status == StatusInReview {
		if _, err := queries.InsertProactiveTaskMergeJobForRun(
			ctx,
			generated.InsertProactiveTaskMergeJobForRunParams{
				ActorRoles: auth.RolesWith(auth.PermissionCompanyProactiveModeManage),
				RunID:      run.ID,
			},
		); err != nil &&
			!errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("create proactive Task merge job: %w", err)
		}
	}
	projection, err := queries.GetTaskProjectionForSystem(
		ctx,
		systemProjectionParams(updated.CompanyID, updated.ID),
	)
	if err != nil {
		return fmt.Errorf("project terminal Run Task: %w", err)
	}
	if status == StatusDone {
		if err := stageDependentProjectionEventsInTx(
			ctx,
			queries,
			pending,
			events.SystemActor(),
			run.CompanyID,
			updated.ID,
			StatusInProgress,
			StatusDone,
		); err != nil {
			return err
		}
		if err := ReconcileOutcomeReportInTx(
			ctx,
			queries,
			pending,
			OutcomeReportInput{
				Actor: ActorRef{Kind: ActorAgent, ID: run.AgentID},
				RunActor: events.RunActor(
					run.AgentID.String(),
					run.ID.String(),
					run.ExecutionAuthorizedByUserID.UUID.String(),
				),
				PreviousStatus: StatusInProgress,
				Task:           taskFromRow(updated),
			},
		); err != nil {
			return err
		}
	}
	event := newAgentTaskUpdatedEvent(run.ID, events.SystemActor(), responseFromSystemProjectionRow(projection))
	if err := stageTaskEvent(ctx, queries, pending, event); err != nil {
		return err
	}
	return activity.RecordInTx(ctx, queries, pending, events.SystemActor(), EventUpdated, before, Topic, updated.ID)
}

func runTerminalTaskStatus(runStatus string) (Status, bool) {
	switch runStatus {
	case "completed":
		return StatusInReview, true
	case "failed":
		return StatusError, true
	case "cancelled":
		return StatusCancelled, true
	default:
		return "", false
	}
}

func completedRunResultStatus(ctx context.Context, q *generated.Queries, run generated.Run) (Status, error) {
	state, err := q.GetTaskResultState(
		ctx,
		generated.GetTaskResultStateParams{CompanyID: run.CompanyID, ID: run.TaskID.UUID},
	)
	if err != nil {
		return "", err
	}
	if state.ResultReviewRequired || !state.SourceRunID.Valid || state.SourceRunID.UUID != run.ID ||
		!run.ExecutionAuthorizedByUserID.Valid {
		return StatusInReview, nil
	}
	for _, id := range state.OutputIds {
		readable, err := q.TaskResultOutputEligible(
			ctx,
			generated.TaskResultOutputEligibleParams{
				CompanyID:   run.CompanyID,
				TaskID:      run.TaskID.UUID,
				OutputID:    id,
				ActorUserID: run.ExecutionAuthorizedByUserID.UUID,
			},
		)
		if err != nil {
			return "", err
		}
		if !readable {
			return StatusInReview, nil
		}
	}
	unapplied, err := q.TaskHasUnappliedResultSuggestions(
		ctx,
		generated.TaskHasUnappliedResultSuggestionsParams{CompanyID: run.CompanyID, ID: run.TaskID.UUID},
	)
	if err != nil {
		return "", err
	}
	if unapplied {
		return StatusInReview, nil
	}
	return StatusDone, nil
}
