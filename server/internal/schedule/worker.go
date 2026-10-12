package schedule

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const (
	defaultFiringBatchSize    = 64
	maximumFiringBatchSize    = 1000
	defaultFiringPollInterval = 15 * time.Second
	minimumFiringPollInterval = 100 * time.Millisecond
	maximumFiringPollInterval = time.Hour
)

const (
	ReasonRunActive              = "run_active"
	ReasonMaterializationBlocked = "materialization_blocked"
)

// Materializer turns one occurrence into a queued Run inside the caller's
// firing transaction. When the Schedule's Conversation or Agent can no longer
// take a Run it returns an error wrapping ErrConflict, or
// execution.ErrRunAgentNotBound when the creator's binding is what is gone;
// any other error is a failure.
type Materializer interface {
	MaterializeScheduleRunInTx(
		context.Context,
		*generated.Queries,
		*[]events.Event,
		MaterializeInput,
	) (MaterializeResult, error)
}

type MaterializeInput struct {
	ScheduleID        uuid.UUID
	CompanyID         uuid.UUID
	ConversationID    uuid.UUID
	CreatorUserID     uuid.UUID
	AgentID           uuid.UUID
	ScheduleVersion   int64
	Provider          string
	Instructions      string
	OccurrenceAt      time.Time
	Trigger           FiringTrigger
	ForceFreshSession bool
}

type MaterializeResult struct {
	Disposition FiringDisposition
	Reason      string
	RunID       uuid.UUID
	MessageID   uuid.UUID
}

// QueuedRunSuperseder terminalizes a queued Run because a newer occurrence
// will replace it in the same firing transaction.
type QueuedRunSuperseder interface {
	SupersedeScheduleRunInTx(
		context.Context,
		*generated.Queries,
		*[]events.Event,
		SupersedeQueuedRunInput,
	) error
}

// SupersedeQueuedRunInput identifies the exact queued Run and firing being
// replaced by a newer occurrence.
type SupersedeQueuedRunInput struct {
	FiringID       uuid.UUID
	RunID          uuid.UUID
	ScheduleID     uuid.UUID
	CompanyID      uuid.UUID
	ConversationID uuid.UUID
}

// QueuedRunSupersederFunc adapts a function to QueuedRunSuperseder.
type QueuedRunSupersederFunc func(
	context.Context,
	*generated.Queries,
	*[]events.Event,
	SupersedeQueuedRunInput,
) error

func (f QueuedRunSupersederFunc) SupersedeScheduleRunInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	input SupersedeQueuedRunInput,
) error {
	return f(ctx, queries, pending, input)
}

type materializationFailure struct {
	Schedule     Response
	OccurrenceAt time.Time
	ServerTime   time.Time
}

type scheduleAuthorityTarget struct {
	companyID     uuid.UUID
	creatorUserID uuid.UUID
}

type QueuedRunInvalidator interface {
	InvalidateScheduleRunInTx(
		context.Context,
		*generated.Queries,
		*[]events.Event,
		InvalidateQueuedRunInput,
	) error
}

type InvalidateQueuedRunInput struct {
	FiringID       uuid.UUID
	RunID          uuid.UUID
	ScheduleID     uuid.UUID
	CompanyID      uuid.UUID
	ConversationID uuid.UUID
}

type QueuedRunInvalidatorFunc func(
	context.Context,
	*generated.Queries,
	*[]events.Event,
	InvalidateQueuedRunInput,
) error

func (f QueuedRunInvalidatorFunc) InvalidateScheduleRunInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	input InvalidateQueuedRunInput,
) error {
	return f(ctx, queries, pending, input)
}

type MaterializerFunc func(
	context.Context,
	*generated.Queries,
	*[]events.Event,
	MaterializeInput,
) (MaterializeResult, error)

func (f MaterializerFunc) MaterializeScheduleRunInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	input MaterializeInput,
) (MaterializeResult, error) {
	return f(ctx, queries, pending, input)
}

type FiringIssue string

const (
	FiringIssueScanFailed         FiringIssue = "scan_failed"
	FiringIssueMaterializeFailed  FiringIssue = "materialize_failed"
	FiringIssueInvalidStoredState FiringIssue = "invalid_stored_state"
)

type FiringWorkerOptions struct {
	BatchSize    int
	PollInterval time.Duration
	OnIssue      func(FiringIssue)
}

// FiringWorkerDependencies are the transactional adapters used by one firing
// worker. Keeping them together leaves the constructor interface small while
// preserving distinct supersede and invalidation seams.
type FiringWorkerDependencies struct {
	Store        *db.Store
	Bus          *events.Bus
	Materializer Materializer
	Superseder   QueuedRunSuperseder
	Invalidator  QueuedRunInvalidator
}

type FiringWorker struct {
	store        *db.Store
	bus          *events.Bus
	materializer Materializer
	superseder   QueuedRunSuperseder
	invalidator  QueuedRunInvalidator
	options      FiringWorkerOptions
}

func NewFiringWorker(
	dependencies FiringWorkerDependencies,
	options FiringWorkerOptions,
) (*FiringWorker, error) {
	if dependencies.Store == nil || dependencies.Bus == nil ||
		nilInterface(dependencies.Materializer) || nilInterface(dependencies.Superseder) ||
		nilInterface(dependencies.Invalidator) ||
		!normalizeFiringWorkerOptions(&options) {
		return nil, ErrWorkerConfiguration
	}
	return &FiringWorker{
		store: dependencies.Store, bus: dependencies.Bus, materializer: dependencies.Materializer,
		superseder: dependencies.Superseder, invalidator: dependencies.Invalidator,
		options: options,
	}, nil
}

func normalizeFiringWorkerOptions(options *FiringWorkerOptions) bool {
	if options == nil {
		return false
	}
	if options.BatchSize == 0 {
		options.BatchSize = defaultFiringBatchSize
	}
	if options.PollInterval == 0 {
		options.PollInterval = defaultFiringPollInterval
	}
	return options.BatchSize >= 1 && options.BatchSize <= maximumFiringBatchSize &&
		options.PollInterval >= minimumFiringPollInterval &&
		options.PollInterval <= maximumFiringPollInterval
}

func (w *FiringWorker) Run(ctx context.Context) error {
	if w == nil || w.store == nil || w.bus == nil || nilInterface(w.materializer) ||
		nilInterface(w.superseder) || nilInterface(w.invalidator) || ctx == nil {
		return ErrWorkerConfiguration
	}
	ticker := time.NewTicker(w.options.PollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := w.ReconcileOnce(ctx); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			w.observeIssue(firingIssue(err))
		}
		if _, err := w.FireOnce(ctx); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			w.observeIssue(firingIssue(err))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *FiringWorker) ReconcileOnce(ctx context.Context) (int, error) {
	if w == nil || w.store == nil || w.bus == nil || nilInterface(w.materializer) ||
		nilInterface(w.superseder) || nilInterface(w.invalidator) || ctx == nil {
		return 0, ErrWorkerConfiguration
	}
	processed := 0
	for range w.options.BatchSize {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		found := false
		pending := []events.Event{}
		err := w.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
			drifted, err := tx.LockNextDriftedSchedule(ctx, auth.RolesWith(auth.PermissionSchedulesManage))
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("lock drifted Schedule: %w", err)
			}
			found = true
			reason := invalidScheduleReason(
				drifted.CompanyValid,
				drifted.CreatorValid,
				drifted.AgentValid,
				drifted.ProviderMatches,
				drifted.SourcesMatch,
			)
			current, err := responseWithSources(ctx, tx, drifted.Schedule)
			if err != nil {
				return err
			}
			pending = append(pending, changedEvent(current))
			overlap, overlapFound, err := lockActiveScheduleRun(ctx, tx, current)
			if err != nil {
				return err
			}
			if overlapFound && overlap.Status == "queued" {
				if err := w.invalidator.InvalidateScheduleRunInTx(
					ctx,
					tx,
					&pending,
					InvalidateQueuedRunInput{
						FiringID: overlap.FiringID, RunID: overlap.RunID,
						ScheduleID: current.ID, CompanyID: current.CompanyID,
						ConversationID: current.ConversationID,
					},
				); err != nil {
					return fmt.Errorf("invalidate queued drifted Schedule Run: %w", err)
				}
				if err := markNeedsAttentionFiring(ctx, tx, overlap, reason); err != nil {
					return err
				}
			}
			if _, err := tx.PauseInvalidDueSchedule(ctx, generated.PauseInvalidDueScheduleParams{
				Reason: reason, ScheduleID: drifted.Schedule.ID, CompanyID: drifted.Schedule.CompanyID,
				ExpectedVersion: drifted.Schedule.Version,
			}); err != nil {
				return fmt.Errorf("pause drifted Schedule: %w", err)
			}
			return nil
		})
		if err != nil {
			return processed, err
		}
		if !found {
			return processed, nil
		}
		publishPending(ctx, w.bus, pending)
		processed++
	}
	return processed, nil
}

func (w *FiringWorker) FireOnce(ctx context.Context) (int, error) {
	if w == nil || w.store == nil || w.bus == nil || nilInterface(w.materializer) ||
		nilInterface(w.superseder) || nilInterface(w.invalidator) || ctx == nil {
		return 0, ErrWorkerConfiguration
	}
	processed := 0
	for range w.options.BatchSize {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		pending := []events.Event{}
		found, runID, failure, err := w.fireNext(ctx, &pending)
		if err != nil {
			if failure != nil && errors.Is(err, ErrMaterialization) {
				if recordErr := w.recordMaterializationFailure(ctx, *failure); recordErr != nil {
					return processed, errors.Join(err, recordErr)
				}
				w.observeIssue(FiringIssueMaterializeFailed)
				processed++
				continue
			}
			return processed, err
		}
		if !found {
			return processed, nil
		}
		publishPending(ctx, w.bus, pending)
		if runID != nil {
			w.notifyRunEnqueued(ctx, *runID)
		}
		processed++
	}
	return processed, nil
}

func (w *FiringWorker) fireNext(
	ctx context.Context,
	pending *[]events.Event,
) (bool, *uuid.UUID, *materializationFailure, error) {
	found := false
	var queuedRunID *uuid.UUID
	var failure *materializationFailure
	err := w.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		candidateIDs, err := lockDueScheduleProducerFences(ctx, tx)
		if err != nil {
			return err
		}
		if len(candidateIDs) == 0 {
			return nil
		}
		due, err := tx.LockNextDueSchedule(ctx, generated.LockNextDueScheduleParams{
			ActorRoles:  auth.RolesWith(auth.PermissionSchedulesManage),
			ScheduleIds: candidateIDs,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock next due Schedule: %w", err)
		}
		found = true
		current, err := responseWithSources(ctx, tx, due.Schedule)
		if err != nil {
			return err
		}
		*pending = append(*pending, changedEvent(current))
		if reason := invalidDueReason(due); reason != "" {
			if _, err := tx.PauseInvalidDueSchedule(ctx, generated.PauseInvalidDueScheduleParams{
				Reason: reason, ScheduleID: current.ID, CompanyID: current.CompanyID,
				ExpectedVersion: current.Revision,
			}); err != nil {
				return fmt.Errorf("pause invalid due Schedule: %w", err)
			}
			return nil
		}
		occurrenceAt, err := latestDueOccurrence(
			current.Trigger,
			due.Schedule.NextOccurrenceAt.Time,
			due.ServerTime.Time,
		)
		if err != nil {
			return err
		}
		overlap, overlapFound, err := lockActiveScheduleRun(ctx, tx, current)
		if err != nil {
			return err
		}
		if overlapFound && overlap.Status != "queued" {
			skipped := MaterializeResult{
				Disposition: FiringDispositionSkipped,
				Reason:      ReasonRunActive,
			}
			if _, err := insertFiring(
				ctx, tx, current, occurrenceAt, FiringTriggerScheduled, uuid.Nil, skipped,
			); err != nil {
				return err
			}
			return advanceAfterFiring(ctx, tx, current, occurrenceAt, due.ServerTime.Time, skipped)
		}
		if overlapFound {
			if err := w.superseder.SupersedeScheduleRunInTx(
				ctx,
				tx,
				pending,
				SupersedeQueuedRunInput{
					FiringID: overlap.FiringID, RunID: overlap.RunID,
					ScheduleID: current.ID, CompanyID: current.CompanyID,
					ConversationID: current.ConversationID,
				},
			); err != nil {
				return fmt.Errorf("supersede queued Schedule Run: %w", err)
			}
		}
		materializeRequest, err := materializeInput(
			current, occurrenceAt, FiringTriggerScheduled,
		)
		if err != nil {
			return err
		}
		materialized, err := w.materializer.MaterializeScheduleRunInTx(
			ctx,
			tx,
			pending,
			materializeRequest,
		)
		if err != nil {
			failure = &materializationFailure{
				Schedule: current, OccurrenceAt: occurrenceAt, ServerTime: due.ServerTime.Time,
			}
			return errors.Join(ErrMaterialization, err)
		}
		firing, err := insertFiring(
			ctx, tx, current, occurrenceAt, FiringTriggerScheduled, uuid.Nil, materialized,
		)
		if err != nil {
			return err
		}
		if err := linkMaterializedRun(ctx, tx, current, firing, materializeRequest, materialized); err != nil {
			return err
		}
		if overlapFound {
			if err := markSupersededFiring(ctx, tx, overlap); err != nil {
				return err
			}
		}
		if materialized.Disposition == FiringDispositionMaterialized {
			value := materialized.RunID
			queuedRunID = &value
		}
		return advanceAfterFiring(ctx, tx, current, occurrenceAt, due.ServerTime.Time, materialized)
	})
	if err != nil {
		return found, nil, failure, err
	}
	return found, queuedRunID, nil, nil
}

// lockDueScheduleProducerFences discovers a bounded candidate set without
// locking Schedules, then takes every Company lock before any membership lock.
// Both phases are UUID-sorted so concurrent workers and account deletion share
// a deterministic prefix before Schedule -> Conversation -> Run.
func lockDueScheduleProducerFences(
	ctx context.Context,
	queries *generated.Queries,
) ([]uuid.UUID, error) {
	rows, err := queries.ListDueScheduleLockTargets(ctx, defaultFiringBatchSize)
	if err != nil {
		return nil, fmt.Errorf("list due Schedule producer fences: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	candidateIDs := make([]uuid.UUID, 0, len(rows))
	companySet := make(map[uuid.UUID]struct{}, len(rows))
	targetSet := make(map[scheduleAuthorityTarget]struct{}, len(rows))
	for _, row := range rows {
		candidateIDs = append(candidateIDs, row.ScheduleID)
		companySet[row.CompanyID] = struct{}{}
		targetSet[scheduleAuthorityTarget{
			companyID: row.CompanyID, creatorUserID: row.CreatorUserID,
		}] = struct{}{}
	}

	companyIDs := make([]uuid.UUID, 0, len(companySet))
	for companyID := range companySet {
		companyIDs = append(companyIDs, companyID)
	}
	slices.SortFunc(companyIDs, func(left, right uuid.UUID) int {
		return bytes.Compare(left[:], right[:])
	})
	companyAvailable := make(map[uuid.UUID]bool, len(companyIDs))
	for _, companyID := range companyIDs {
		available, err := lockScheduleCompanyAuthority(ctx, queries, companyID)
		if err != nil {
			return nil, err
		}
		companyAvailable[companyID] = available
	}

	targets := make([]scheduleAuthorityTarget, 0, len(targetSet))
	for target := range targetSet {
		targets = append(targets, target)
	}
	slices.SortFunc(targets, func(left, right scheduleAuthorityTarget) int {
		if order := bytes.Compare(left.companyID[:], right.companyID[:]); order != 0 {
			return order
		}
		return bytes.Compare(left.creatorUserID[:], right.creatorUserID[:])
	})
	for _, target := range targets {
		if !companyAvailable[target.companyID] {
			continue
		}
		if _, err := lockScheduleCreatorMembership(
			ctx,
			queries,
			target.companyID,
			target.creatorUserID,
		); err != nil {
			return nil, err
		}
	}
	return candidateIDs, nil
}

func (w *FiringWorker) recordMaterializationFailure(
	ctx context.Context,
	failure materializationFailure,
) error {
	var pending []events.Event
	err := w.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		authorized, err := lockScheduleCreatorAuthority(
			ctx,
			tx,
			failure.Schedule.CompanyID,
			failure.Schedule.CreatorUserID,
		)
		if err != nil {
			return err
		}
		if !authorized {
			return nil
		}
		row, err := tx.GetScheduleForCreatorForUpdate(
			ctx,
			generated.GetScheduleForCreatorForUpdateParams{
				ActorRoles:  auth.RolesWith(auth.PermissionSchedulesManage),
				ActorUserID: failure.Schedule.CreatorUserID,
				ScheduleID:  failure.Schedule.ID,
				CompanyID:   failure.Schedule.CompanyID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock Schedule after materialization failure: %w", err)
		}
		if row.Version != failure.Schedule.Revision || row.Status != string(StatusActive) ||
			!row.NextOccurrenceAt.Valid || failure.Schedule.NextOccurrenceAt == nil ||
			!row.NextOccurrenceAt.Time.Equal(failure.Schedule.NextOccurrenceAt.UTC()) {
			return nil
		}
		current, err := responseWithSources(ctx, tx, row)
		if err != nil {
			return err
		}
		result := MaterializeResult{
			Disposition: FiringDispositionNeedsAttention,
			Reason:      ReasonMaterializationBlocked,
		}
		if _, err := insertFiring(
			ctx,
			tx,
			current,
			failure.OccurrenceAt,
			FiringTriggerScheduled,
			uuid.Nil,
			result,
		); err != nil {
			return err
		}
		pending = append(pending, changedEvent(current))
		return advanceAfterFiring(
			ctx,
			tx,
			current,
			failure.OccurrenceAt,
			failure.ServerTime,
			result,
		)
	})
	if err == nil {
		publishPending(ctx, w.bus, pending)
	}
	return err
}

func invalidDueReason(row generated.LockNextDueScheduleRow) string {
	return invalidScheduleReason(
		row.CompanyValid,
		row.CreatorValid,
		row.AgentValid,
		row.ProviderMatches,
		row.SourcesMatch,
	)
}

func invalidScheduleReason(
	companyValid, creatorValid, agentValid, providerMatches bool,
	sourcesMatch pgtype.Bool,
) string {
	switch {
	case !companyValid:
		return ReasonCompanyChanged
	case !creatorValid:
		return ReasonCreatorChanged
	case !agentValid:
		return ReasonAgentUnavailable
	case !providerMatches:
		return ReasonProviderChanged
	case !sourcesMatch.Valid || !sourcesMatch.Bool:
		return ReasonSourceChanged
	default:
		return ""
	}
}

func lockActiveScheduleRun(
	ctx context.Context,
	tx *generated.Queries,
	current Response,
) (generated.LockActiveScheduleRunRow, bool, error) {
	row, err := tx.LockActiveScheduleRun(ctx, generated.LockActiveScheduleRunParams{
		ScheduleID: current.ID, CompanyID: current.CompanyID, ConversationID: current.ConversationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.LockActiveScheduleRunRow{}, false, nil
	}
	if err != nil {
		return generated.LockActiveScheduleRunRow{}, false,
			fmt.Errorf("lock active Schedule Run: %w", err)
	}
	return row, true, nil
}

func markSupersededFiring(
	ctx context.Context,
	tx *generated.Queries,
	previous generated.LockActiveScheduleRunRow,
) error {
	_, err := tx.UpdateScheduleFiringDisposition(ctx, generated.UpdateScheduleFiringDispositionParams{
		Disposition: string(FiringDispositionSuperseded), Reason: "newer_occurrence",
		FiringID: previous.FiringID, CompanyID: previous.CompanyID,
	})
	if err != nil {
		return fmt.Errorf("mark previous Schedule firing superseded: %w", err)
	}
	return nil
}

func markNeedsAttentionFiring(
	ctx context.Context,
	tx *generated.Queries,
	previous generated.LockActiveScheduleRunRow,
	reason string,
) error {
	_, err := tx.UpdateScheduleFiringDisposition(ctx, generated.UpdateScheduleFiringDispositionParams{
		Disposition: string(FiringDispositionNeedsAttention), Reason: reason,
		FiringID: previous.FiringID, CompanyID: previous.CompanyID,
	})
	if err != nil {
		return fmt.Errorf("mark drifted Schedule firing needs attention: %w", err)
	}
	return nil
}

func advanceAfterFiring(
	ctx context.Context,
	tx *generated.Queries,
	current Response,
	occurrenceAt time.Time,
	serverTime time.Time,
	materialized MaterializeResult,
) error {
	status := StatusActive
	var next *time.Time
	var pauseReason pgtype.Text
	if current.Trigger.Type == TriggerTypeOnce {
		status = StatusCompleted
	} else {
		value, err := nextOccurrence(current.Trigger, serverTime)
		if err != nil {
			return err
		}
		next = &value
	}
	if materialized.Disposition == FiringDispositionNeedsAttention {
		status = StatusPaused
		pauseReason = pgtype.Text{String: materialized.Reason, Valid: true}
		if current.Trigger.Type == TriggerTypeOnce {
			next = cloneTime(current.Trigger.RunAt)
		}
	}
	if _, err := tx.AdvanceScheduleAfterFiring(ctx, generated.AdvanceScheduleAfterFiringParams{
		Status: string(status), NextOccurrenceAt: optionalTime(next), OccurrenceAt: requiredTime(occurrenceAt),
		PauseReason: pauseReason, ScheduleID: current.ID, CompanyID: current.CompanyID,
		ExpectedVersion: current.Revision,
	}); err != nil {
		return fmt.Errorf("advance Schedule after firing: %w", err)
	}
	return nil
}

func materializeInput(
	current Response,
	occurrenceAt time.Time,
	trigger FiringTrigger,
) (MaterializeInput, error) {
	input := MaterializeInput{
		ScheduleID: current.ID, CompanyID: current.CompanyID,
		ConversationID: current.ConversationID,
		CreatorUserID:  current.CreatorUserID, AgentID: current.AgentID,
		ScheduleVersion: current.Revision, Provider: current.ReviewedProvider,
		Instructions: current.Instructions,
		OccurrenceAt: occurrenceAt.UTC(), Trigger: trigger, ForceFreshSession: true,
	}
	if !validMaterializeInput(input) {
		return MaterializeInput{}, ErrInvalidStoredState
	}
	return input, nil
}

func insertFiring(
	ctx context.Context,
	tx *generated.Queries,
	current Response,
	occurrenceAt time.Time,
	trigger FiringTrigger,
	requestKey uuid.UUID,
	materialized MaterializeResult,
) (generated.ScheduleFiring, error) {
	if !validMaterializeResult(materialized) {
		return generated.ScheduleFiring{}, ErrInvalidStoredState
	}
	reason := pgtype.Text{}
	if materialized.Reason != "" {
		reason = pgtype.Text{String: materialized.Reason, Valid: true}
	}
	messageID := uuid.NullUUID{}
	runID := uuid.NullUUID{}
	if materialized.Disposition == FiringDispositionMaterialized {
		messageID = uuid.NullUUID{UUID: materialized.MessageID, Valid: true}
		runID = uuid.NullUUID{UUID: materialized.RunID, Valid: true}
	}
	row, err := tx.InsertScheduleFiring(ctx, generated.InsertScheduleFiringParams{
		CompanyID: current.CompanyID, ScheduleID: current.ID,
		ScheduleVersion: current.Revision, ConversationID: current.ConversationID,
		MessageID: messageID, RunID: runID, OccurrenceAt: pgtype.Timestamptz{
			Time: occurrenceAt.UTC(), Valid: true,
		},
		Trigger: string(trigger), RequestKey: uuid.NullUUID{UUID: requestKey, Valid: requestKey != uuid.Nil},
		Disposition: string(materialized.Disposition), Reason: reason,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.ScheduleFiring{}, ErrConflict
	}
	if err != nil {
		return generated.ScheduleFiring{}, mapMutationError("insert Schedule firing", err)
	}
	return row, nil
}

func linkMaterializedRun(
	ctx context.Context,
	tx *generated.Queries,
	current Response,
	firing generated.ScheduleFiring,
	input MaterializeInput,
	result MaterializeResult,
) error {
	if result.Disposition != FiringDispositionMaterialized {
		return nil
	}
	_, err := tx.LinkRunToScheduleFiring(ctx, generated.LinkRunToScheduleFiringParams{
		FiringID: firing.ID, ScheduleID: current.ID,
		RunID: result.RunID, CompanyID: current.CompanyID, ConversationID: current.ConversationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("link Run to Schedule firing: %w", err)
	}
	return nil
}

func validMaterializeResult(result MaterializeResult) bool {
	if !validDisposition(result.Disposition) {
		return false
	}
	if result.Disposition == FiringDispositionMaterialized {
		return result.Reason == "" && result.RunID != uuid.Nil && util.ValidUUID(result.MessageID)
	}
	return result.RunID == uuid.Nil && result.MessageID == uuid.Nil && safeCodePattern.MatchString(result.Reason)
}

func validDisposition(value FiringDisposition) bool {
	switch value {
	case FiringDispositionMaterialized, FiringDispositionSuperseded,
		FiringDispositionSkipped, FiringDispositionNeedsAttention:
		return true
	default:
		return false
	}
}

func firingIssue(err error) FiringIssue {
	switch {
	case errors.Is(err, ErrMaterialization):
		return FiringIssueMaterializeFailed
	case errors.Is(err, ErrInvalidStoredState):
		return FiringIssueInvalidStoredState
	default:
		return FiringIssueScanFailed
	}
}

func (w *FiringWorker) observeIssue(issue FiringIssue) {
	if w == nil || w.options.OnIssue == nil {
		return
	}
	func() {
		defer func() { _ = recover() }()
		w.options.OnIssue(issue)
	}()
}

func (w *FiringWorker) notifyRunEnqueued(ctx context.Context, runID uuid.UUID) {
	run, err := w.store.Queries().GetRun(ctx, runID)
	if err == nil {
		execution.PublishRuntimeWork(ctx, w.bus, run)
	}
}

func publishPending(ctx context.Context, bus *events.Bus, pending []events.Event) {
	for _, group := range events.GroupByTopic(pending) {
		bus.Publish(ctx, group.Topic, group.Events...)
	}
}

func validMaterializeInput(input MaterializeInput) bool {
	return util.ValidUUID(input.ScheduleID) &&
		util.ValidUUID(input.CompanyID) && util.ValidUUID(input.ConversationID) &&
		util.ValidUUID(input.CreatorUserID) && util.ValidUUID(input.AgentID) &&
		validVersion(input.ScheduleVersion) && !input.OccurrenceAt.IsZero() &&
		input.Provider != "" && input.Instructions != "" &&
		(input.Trigger == FiringTriggerScheduled || input.Trigger == FiringTriggerManual) &&
		input.ForceFreshSession
}
