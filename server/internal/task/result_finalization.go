package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// FinalizeResultsInput selects evidence; it grants no suggestion or Task approval.
type FinalizeResultsInput struct {
	RequestKey       uuid.UUID
	ExpectedRevision int64
	OutputIDs        []uuid.UUID
}

func taskResultRunRequestHash(in UpdateInput, runID uuid.UUID) string {
	value := sha256.Sum256([]byte(runID.String() + taskResultRequestHash(in)))
	return hex.EncodeToString(value[:])
}

// FinalizeResults keeps the Task running until the terminal Run transaction.
// Only the exact live execution may select its own deliveries.
func (s *DefaultTaskService) FinalizeResults(
	ctx context.Context,
	p auth.UserPrincipal,
	origin execution.RunOrigin,
	in FinalizeResultsInput,
) (Response, error) {
	if !validTaskRunOrigin(p, origin) {
		return Response{}, ErrConflict
	}
	if in.RequestKey == uuid.Nil || in.ExpectedRevision < 1 || len(in.OutputIDs) == 0 || len(in.OutputIDs) > 32 {
		return Response{}, ErrInvalidInput
	}
	status := StatusInProgress
	input := UpdateInput{
		ExpectedRevision: in.ExpectedRevision,
		Status:           &status,
		ResultSelection:  &ResultSelectionInput{RequestKey: in.RequestKey, OutputIDs: in.OutputIDs},
	}
	var result Response
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = nil
		if _, err := authorizeTaskMutation(ctx, q, p); err != nil {
			return err
		}
		if err := lockTaskRunConversation(ctx, q, origin); err != nil {
			return err
		}
		run, err := q.LockRunForTerminalTranscript(ctx, origin.RunID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if !runMatchesTaskOrigin(run, origin) || run.Status != "running" ||
			!run.ExecutionAuthorizedByUserID.Valid ||
			run.ExecutionAuthorizedByUserID.UUID != p.UserID {
			return ErrConflict
		}
		row, err := q.GetTaskForCompanyByIDForUpdate(
			ctx,
			generated.GetTaskForCompanyByIDForUpdateParams{CompanyID: p.CompanyID, TaskID: *origin.TaskID},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if !taskMatchesRunGeneration(row, run) || row.AssigneeUserID != p.UserID ||
			row.Status != string(StatusInProgress) {
			return ErrConflict
		}
		if _, err = auth.RequireTaskPermission(
			ctx,
			q,
			p,
			row.ID,
			auth.ProjectPermissionTasksExecute,
		); err != nil {
			return err
		}
		agent, err := q.GetTaskRunAgent(
			ctx,
			generated.GetTaskRunAgentParams{CompanyID: p.CompanyID, TaskID: row.ID},
		)
		if err != nil {
			return err
		}
		if agent.ID != run.AgentID {
			return ErrConflict
		}
		receipt, err := q.GetTaskResultReceipt(
			ctx,
			generated.GetTaskResultReceiptParams{
				CompanyID:   p.CompanyID,
				ActorUserID: p.UserID,
				RequestKey:  in.RequestKey,
			},
		)
		if err == nil {
			if receipt.TaskID != row.ID || receipt.RequestHash != taskResultRunRequestHash(input, run.ID) {
				return ErrConflict
			}
			for _, id := range in.OutputIDs {
				level, e := auth.OutputAccessInTx(ctx, q, p, id)
				if e != nil {
					return e
				}
				if level == 0 {
					return ErrNotFound
				}
			}
			result, err = taskProjectionForActiveMember(ctx, q, p, row.ID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if row.Version != in.ExpectedRevision {
			return ErrConflict
		}
		current := taskFromRow(row)
		target := current
		if err = changeTaskResultsInTx(
			ctx,
			q,
			p,
			taskResultMutation{
				RunID:   run.ID,
				Current: current,
				Target:  &target,
				Input:   input,
				Pending: &pending,
			},
		); err != nil {
			return err
		}
		if _, err = updateTask(
			ctx,
			q,
			target,
			current.Revision,
			&pending,
			events.RunActor(run.AgentID.String(), run.ID.String(), p.UserID.String()),
			false,
			personWriteFence(p.UserID, auth.ProjectPermissionTasksExecute),
		); err != nil {
			return err
		}
		result, err = taskProjectionForActiveMember(ctx, q, p, row.ID)
		if err != nil {
			return err
		}
		return stageTaskEvent(
			ctx,
			q,
			&pending,
			newAgentTaskUpdatedEvent(
				run.ID,
				events.RunActor(run.AgentID.String(), run.ID.String(), p.UserID.String()),
				result,
			),
		)
	})
	if err != nil {
		return Response{}, fmt.Errorf("finalize Task results: %w", err)
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}
