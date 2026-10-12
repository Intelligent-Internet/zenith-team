package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const (
	CancellationScheduleSuperseded  = "schedule_superseded"
	CancellationScheduleInvalidated = "schedule_invalidated"
)

var (
	ErrScheduleRunInvalid  = errors.New("Schedule Run cancellation input is invalid")
	ErrScheduleRunConflict = errors.New("Schedule Run changed before cancellation")
)

// ScheduleRunCancellationInput identifies one exact queued Schedule Run for a
// scheduler-owned terminal transition. Runs use the database bigint identity;
// the surrounding Schedule aggregate keeps UUID identities.
type ScheduleRunCancellationInput struct {
	RunID          uuid.UUID
	FiringID       uuid.UUID
	ScheduleID     uuid.UUID
	CompanyID      uuid.UUID
	ConversationID uuid.UUID
}

type SupersedeScheduleRunInput = ScheduleRunCancellationInput

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
	if reason != CancellationScheduleSuperseded && reason != CancellationScheduleInvalidated {
		return ErrScheduleRunInvalid
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

	if err := activity.RecordInTx(ctx, queries, pending, events.SystemActor(), EventRunCancelled, beforeHistory[input.RunID], Topic, input.RunID); err != nil {
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
		Actor:          events.SystemActor(),
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
