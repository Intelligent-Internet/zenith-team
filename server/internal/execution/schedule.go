package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

const (
	CancellationScheduleSuperseded  = "schedule_superseded"
	CancellationScheduleInvalidated = "schedule_invalidated"
	CancellationSchedulePaused      = "schedule_paused"
	CancellationScheduleCancelled   = "schedule_cancelled"
	CancellationScheduleEdited      = "schedule_edited"
)

var (
	ErrScheduleRunInvalid  = errors.New("Schedule Run cancellation input is invalid")
	ErrScheduleRunConflict = errors.New("Schedule Run changed before cancellation")
)

// ScheduleRunCancellationInput identifies one exact queued Schedule Run for a
// scheduler-owned terminal transition.
type ScheduleRunCancellationInput struct {
	Reason         string
	Actor          events.Actor
	RunID          uuid.UUID
	FiringID       uuid.UUID
	ScheduleID     uuid.UUID
	CompanyID      uuid.UUID
	ConversationID uuid.UUID
}

type SupersedeScheduleRunInput = ScheduleRunCancellationInput

// TaskScheduleCancellation identifies unclaimed work invalidated by a Calendar
// change. The Task domain owns the occurrence and definition updates.
type TaskScheduleCancellation struct {
	CompanyID uuid.UUID
	TaskID    uuid.UUID
	Reason    string
}

// CancelPendingTaskScheduleRunsInTx cancels only scheduled first attempts still
// waiting for a daemon. The caller holds the Company and Task mutation fences.
// Conversation locks precede Run locks; the final query rechecks waiting status
// under the Run lock so a concurrent claim may finish without being cancelled.
func CancelPendingTaskScheduleRunsInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	input TaskScheduleCancellation,
) ([]generated.Run, error) {
	if q == nil || pending == nil || input.CompanyID == uuid.Nil || input.TaskID == uuid.Nil {
		return nil, ErrScheduleRunInvalid
	}
	switch input.Reason {
	case CancellationSchedulePaused, CancellationScheduleCancelled, CancellationScheduleEdited:
	default:
		return nil, ErrScheduleRunInvalid
	}
	candidates, err := q.ListActiveRunsForTaskHandoff(ctx, generated.ListActiveRunsForTaskHandoffParams{
		CompanyID: input.CompanyID, TaskID: input.TaskID,
	})
	if err != nil {
		return nil, fmt.Errorf("list scheduled Task Runs for cancellation: %w", err)
	}
	var conversationID uuid.UUID
	for _, run := range candidates {
		if run.ConversationID != conversationID {
			if err := lockTerminalRunConversation(ctx, q, run); err != nil {
				return nil, err
			}
			conversationID = run.ConversationID
		}
	}
	candidates, err = q.LockPendingTaskScheduleRuns(ctx, generated.LockPendingTaskScheduleRunsParams{
		CompanyID: input.CompanyID, TaskID: input.TaskID,
	})
	if err != nil {
		return nil, fmt.Errorf("lock pending scheduled Task Runs: %w", err)
	}
	return cancelRunCandidatesInTx(ctx, q, pending, candidates, input.Reason)
}

// SupersedeScheduleRunInTx terminalizes one queued Schedule Run in the
// caller-owned firing transaction.
func SupersedeScheduleRunInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	input SupersedeScheduleRunInput,
) error {
	return cancelQueuedScheduleRunInTx(ctx, queries, pending, input, CancellationScheduleSuperseded)
}

// InvalidateScheduleRunInTx cancels a queued Run when the Schedule's reviewed
// agent, provider, or connector authority drifted before the next firing.
func InvalidateScheduleRunInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	input ScheduleRunCancellationInput,
) error {
	return cancelQueuedScheduleRunInTx(ctx, queries, pending, input, CancellationScheduleInvalidated)
}

// CancelScheduleRunInTx drops a waiting occurrence after an explicit definition
// mutation. The caller holds its Schedule and Conversation locks.
func CancelScheduleRunInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	input ScheduleRunCancellationInput,
) error {
	return cancelQueuedScheduleRunInTx(ctx, queries, pending, input, input.Reason)
}

func cancelQueuedScheduleRunInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	input ScheduleRunCancellationInput,
	reason string,
) error {
	if queries == nil || pending == nil || !validScheduleRunCancellationInput(input) {
		return ErrScheduleRunInvalid
	}
	switch reason {
	case CancellationScheduleSuperseded,
		CancellationScheduleInvalidated,
		CancellationSchedulePaused,
		CancellationScheduleCancelled,
		CancellationScheduleEdited:
	default:
		return ErrScheduleRunInvalid
	}
	actor := input.Actor
	if actor.Type == "" {
		actor = events.SystemActor()
	}

	beforeHistory, err := ObserveRunsInTx(ctx, queries, []uuid.UUID{input.RunID})
	if err != nil {
		return err
	}
	cancelled, err := queries.CancelQueuedScheduleRun(ctx, generated.CancelQueuedScheduleRunParams{
		RunID: input.RunID, CompanyID: input.CompanyID,
		ConversationID: input.ConversationID, FiringID: input.FiringID,
		ScheduleID: input.ScheduleID, Reason: reason,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrScheduleRunConflict
	}
	if err != nil {
		return fmt.Errorf("cancel queued Schedule Run: %w", err)
	}

	if err := activity.RecordInTx(
		ctx,
		queries,
		pending,
		actor,
		EventRunCancelled,
		beforeHistory[input.RunID],
		Topic,
		input.RunID,
	); err != nil {
		return err
	}
	view, err := loadRunView(ctx, queries, cancelled)
	if err != nil {
		return fmt.Errorf("load cancelled Schedule Run projection: %w", err)
	}
	*pending = append(*pending, events.Event{
		EntityType:     Topic,
		Name:           EventRunCancelled,
		CompanyID:      view.CompanyID,
		CreatedAt:      time.Now().UTC(),
		RunID:          view.ID,
		TaskID:         util.UUIDToValue(view.TaskID),
		ConversationID: view.ConversationID,
		Actor:          actor,
		Payload:        view.RunSnapshot,
	})
	return nil
}

func validScheduleRunCancellationInput(input ScheduleRunCancellationInput) bool {
	if input.RunID == uuid.Nil {
		return false
	}
	ids := []uuid.UUID{input.FiringID, input.ScheduleID, input.CompanyID, input.ConversationID}
	seen := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		if !util.ValidUUID(id) {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}
