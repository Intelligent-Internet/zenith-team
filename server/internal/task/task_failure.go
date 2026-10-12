package task

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// FailureInput is the replay fence and Agent-authored business reason for one
// live Task Run. Run, Agent, Task, and Conversation identity come from the
// supplemental Run proof instead of this input.
type FailureInput struct {
	RequestKey uuid.UUID
	Reason     string
}

// MarkFailed records a business outcome from the Agent executing the exact
// current Task generation. It deliberately leaves the Run running: the normal
// terminal callback remains the owner of transcript, usage, and session state.
func (s *DefaultTaskService) MarkFailed(
	ctx context.Context,
	principal auth.UserPrincipal,
	origin execution.RunOrigin,
	input FailureInput,
) (Response, error) {
	normalized, err := normalizeFailureInput(input)
	if err != nil {
		return Response{}, err
	}
	if !validTaskRunOrigin(principal, origin) {
		return Response{}, ErrConflict
	}

	var result Response
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite,
	}, func(tx *generated.Queries) error {
		pending = pending[:0]
		if _, err := authorizeTaskMutation(ctx, tx, principal); err != nil {
			return err
		}
		if err := lockTaskRunConversation(ctx, tx, origin); err != nil {
			return err
		}
		lockedRun, err := tx.LockRunForTerminalTranscript(ctx, origin.RunID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return fmt.Errorf("lock Task failure Run: %w", err)
		}
		if !runMatchesTaskOrigin(lockedRun, origin) || lockedRun.Status != "running" {
			return ErrConflict
		}

		lockedTask, err := tx.GetTaskForCompanyByIDForUpdate(
			ctx,
			generated.GetTaskForCompanyByIDForUpdateParams{
				TaskID: *origin.TaskID, CompanyID: principal.CompanyID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return fmt.Errorf("lock Task for business failure: %w", err)
		}
		taskAgent, err := tx.GetTaskRunAgent(
			ctx,
			generated.GetTaskRunAgentParams{CompanyID: lockedTask.CompanyID, TaskID: lockedTask.ID},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return fmt.Errorf("resolve Task failure Agent: %w", err)
		}
		if taskAgent.ID != lockedRun.AgentID || !taskMatchesRunGeneration(lockedTask, lockedRun) {
			return ErrConflict
		}

		replay, err := tx.GetTaskFailureReplayForUpdate(
			ctx,
			generated.GetTaskFailureReplayForUpdateParams{
				SourceRunID: origin.RunID, RequestKey: normalized.RequestKey,
			},
		)
		if err == nil {
			if !failureReplayMatches(replay, lockedTask, lockedRun, normalized) {
				return ErrConflict
			}
			result, err = taskProjectionForActiveMember(ctx, tx, principal, lockedTask.ID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read Task failure replay: %w", err)
		}
		if Status(lockedTask.Status) != StatusInProgress {
			return ErrConflict
		}

		previous := taskFromRow(lockedTask)
		target := previous
		target.Status = StatusFailed
		// The Run declares the failure: its Agent, for its authorizer, in
		// this Run (D31).
		actor := origin.Actor()
		updated, err := updateTask(
			ctx, tx, target, previous.Revision, &pending, actor, false, projectWriteFence{},
		)
		if err != nil {
			return err
		}
		if err := ReconcileOutcomeReportInTx(ctx, tx, &pending, OutcomeReportInput{
			Actor:          ActorRef{Kind: ActorAgent, ID: lockedRun.AgentID},
			RunActor:       actor,
			PreviousStatus: previous.Status,
			Task:           updated,
			ResultSummary:  normalized.Reason,
			SourceRunID:    lockedRun.ID,
			RequestKey:     normalized.RequestKey,
		}); err != nil {
			return fmt.Errorf("create failed Task outcome report: %w", err)
		}
		result, err = taskProjectionForActiveMember(ctx, tx, principal, updated.ID)
		if err != nil {
			return err
		}
		return stageTaskEvent(ctx, tx, &pending, newAgentTaskUpdatedEvent(lockedRun.ID, actor, result))
	})
	if err != nil {
		return Response{}, fmt.Errorf("mark Task failed: %w", err)
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

func normalizeFailureInput(input FailureInput) (FailureInput, error) {
	input.Reason = protocol.TrimContractWhitespace(input.Reason)
	if !util.ValidUUID(input.RequestKey) || input.Reason == "" ||
		!utf8.ValidString(input.Reason) || utf8.RuneCountInString(input.Reason) > 2000 {
		return FailureInput{}, ErrInvalidInput
	}
	return input, nil
}

func validTaskRunOrigin(principal auth.UserPrincipal, origin execution.RunOrigin) bool {
	return origin.RunID != uuid.Nil && origin.CompanyID == principal.CompanyID &&
		origin.TaskID != nil && util.ValidUUID(*origin.TaskID) &&
		util.ValidUUID(origin.AgentID) && util.ValidUUID(origin.ConversationID) &&
		origin.ConversationKind == "task"
}

func lockTaskRunConversation(
	ctx context.Context,
	tx *generated.Queries,
	origin execution.RunOrigin,
) error {
	_, err := tx.LockConversationForSystemRunQueueMutation(
		ctx,
		generated.LockConversationForSystemRunQueueMutationParams{
			ConversationID: origin.ConversationID, CompanyID: origin.CompanyID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("lock Task failure Conversation: %w", err)
	}
	return nil
}

func runMatchesTaskOrigin(run generated.Run, origin execution.RunOrigin) bool {
	return run.ID == origin.RunID && run.CompanyID == origin.CompanyID &&
		run.AgentID == origin.AgentID && run.TaskID.Valid && origin.TaskID != nil &&
		run.TaskID.UUID == *origin.TaskID && run.ConversationID == origin.ConversationID &&
		run.TaskExecutionGeneration.Valid
}

func taskMatchesRunGeneration(value generated.Task, run generated.Run) bool {
	return value.ID == run.TaskID.UUID && value.CompanyID == run.CompanyID &&
		value.ConversationID == run.ConversationID &&
		value.ExecutionGeneration == run.TaskExecutionGeneration.Int64
}

func failureReplayMatches(
	replay generated.TaskReportEvent,
	value generated.Task,
	run generated.Run,
	input FailureInput,
) bool {
	return replay.CompanyID == value.CompanyID && replay.TaskID == value.ID &&
		replay.TaskVersion == value.Version && replay.SourceAgentID == run.AgentID &&
		replay.CompletionSource == reportSourceAgentBusinessFailure &&
		replay.ResultSummary == input.Reason && replay.SourceRunID.Valid &&
		replay.SourceRunID.UUID == run.ID && replay.RequestKey.Valid &&
		replay.RequestKey.UUID == input.RequestKey && Status(value.Status) == StatusFailed
}

func newAgentTaskUpdatedEvent(runID uuid.UUID, actor events.Actor, value Response) events.Event {
	value = framedResponse(value)
	return events.Event{
		EntityType: Topic, Name: EventUpdated, CompanyID: value.CompanyID,
		CreatedAt: value.UpdatedAt, TaskID: value.ID, ConversationID: value.ConversationID,
		RunID: runID, Actor: actor, Payload: value,
	}
}
