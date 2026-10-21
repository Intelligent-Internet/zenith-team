package schedule

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

const maximumFiringPassLimit = 5000

const (
	ReasonRunActive              = "run_active"
	ReasonMaterializationBlocked = "materialization_blocked"
	ReasonTriggerInvalid         = "trigger_invalid"
	ReasonFiringFailed           = "firing_failed"
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
	MissedCount int32
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
	Reason       string
	Schedule     Response
	OccurrenceAt time.Time
	ServerTime   time.Time
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

// FiringIssue carries row-level diagnostics to the composition root. IDs belong
// in logs, never in metric labels or queue error messages.
type FiringIssue struct {
	Path       string
	ScheduleID uuid.UUID
	CompanyID  uuid.UUID
	Code       string
	Err        error
	Stack      []byte
}

type FiringWorkerOptions struct {
	PassLimit     int
	SweepInterval time.Duration
	OnIssue       func(FiringIssue)
	OnFired       func(string, string, string, time.Duration)
	OnPass        func(string)
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
	failureMu        sync.Mutex
	failures         map[uuid.UUID]firingFailureCount
	nextFailurePrune time.Time
	store            *db.Store
	bus              *events.Bus
	materializer     Materializer
	superseder       QueuedRunSuperseder
	invalidator      QueuedRunInvalidator
	options          FiringWorkerOptions
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
	if options.PassLimit == 0 {
		options.PassLimit = maximumFiringPassLimit
	}
	if options.SweepInterval == 0 {
		options.SweepInterval = 30 * time.Second
	}
	return options.PassLimit >= 1 && options.PassLimit <= maximumFiringPassLimit &&
		options.SweepInterval >= 10*time.Second &&
		options.SweepInterval <= time.Minute
}

func (w *FiringWorker) Run(ctx context.Context) error {
	if w == nil || w.store == nil || w.bus == nil || nilInterface(w.materializer) ||
		nilInterface(w.superseder) || nilInterface(w.invalidator) || ctx == nil {
		return ErrWorkerConfiguration
	}
	ticker := time.NewTicker(w.options.SweepInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := w.SweepOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.observeIssue(FiringIssue{Path: "sweep", Code: "scan_failed", Err: err})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// SweepOnce repairs overdue work without depending on Redis. Each pass is
// bounded by the sweep interval; a slow company cannot block unrelated ones.
func (w *FiringWorker) SweepOnce(ctx context.Context) (count int, err error) {
	ctx, cancel := context.WithTimeout(ctx, w.options.SweepInterval)
	defer cancel()
	defer func() {
		if w.options.OnPass != nil {
			func() {
				defer func() { _ = recover() }()
				result := "ok"
				if err != nil {
					result = "error"
				}
				if errors.Is(err, context.DeadlineExceeded) {
					result = "deadline"
				}
				w.options.OnPass(result)
			}()
		}
	}()
	count, fireErr := w.firePass(ctx, 30)
	driftCount, driftErr := w.ReconcileOnce(ctx)
	// A previous process may have committed and died before publishing. Wake
	// each online runtime once, also invalidating the distributed empty cache.
	targets, wakeErr := w.store.Queries().ListScheduleWakeupTargets(ctx)
	if wakeErr == nil {
		for _, target := range targets {
			execution.PublishRuntimeUnblocked(
				ctx,
				w.bus,
				generated.Run{CompanyID: target.CompanyID, RuntimeID: target.RuntimeID},
			)
		}
	}
	return count + driftCount, errors.Join(fireErr, driftErr, wakeErr)
}

func (w *FiringWorker) ReconcileOnce(ctx context.Context) (int, error) {
	processed := 0
	cursor := uuid.Nil
	skippedCompanies := make(map[uuid.UUID]bool)
	for processed < w.options.PassLimit {
		rows, err := w.store.Queries().
			ListDriftedScheduleLockTargets(ctx, generated.ListDriftedScheduleLockTargetsParams{
				ActorRoles: auth.RolesWith(auth.PermissionSchedulesManage),
				AfterID:    cursor,
				PageSize:   int32(min(500, w.options.PassLimit-processed)),
			})
		if err != nil {
			return processed, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			cursor = row.ScheduleID
			processed++
			if skippedCompanies[row.CompanyID] {
				continue
			}
			_, err := w.processSchedule(ctx, row.ScheduleID, true, "sweep")
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
				skippedCompanies[row.CompanyID] = true
			}
		}
	}
	return processed, ctx.Err()
}

func (w *FiringWorker) FireOnce(ctx context.Context) (int, error) {
	return w.firePass(ctx, 0)
}

func (w *FiringWorker) firePass(ctx context.Context, grace float64) (int, error) {
	processed, visited := 0, 0
	cursor := pgtype.Timestamptz{}
	cursorID := uuid.Nil
	var failed []generated.ListDueScheduleLockTargetsRow
	skippedCompanies := make(map[uuid.UUID]bool)
	for visited < w.options.PassLimit {
		rows, err := w.store.Queries().
			ListDueScheduleLockTargets(ctx, generated.ListDueScheduleLockTargetsParams{
				GraceSeconds: grace,
				AfterTime:    cursor,
				AfterID:      cursorID,
				PageSize:     int32(min(500, w.options.PassLimit-visited)),
			})
		if err != nil {
			return processed, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			visited++
			cursor, cursorID = row.NextOccurrenceAt, row.ScheduleID
			if skippedCompanies[row.CompanyID] {
				continue
			}
			fired, err := w.FireSchedule(ctx, row.ScheduleID, "sweep")
			if err != nil {
				if escalationEligible(err) && w.countFailure(row) >= 5 &&
					row.ServerTime.Time.Sub(row.NextOccurrenceAt.Time) > 10*time.Minute {
					failed = append(failed, row)
				}
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
					skippedCompanies[row.CompanyID] = true
				}
				continue
			}
			if fired {
				processed++
			}
		}
	}
	if processed > 0 {
		for _, row := range failed {
			if err := w.escalateFailure(ctx, row); err != nil {
				w.observeIssue(
					FiringIssue{
						Path:       "sweep",
						ScheduleID: row.ScheduleID,
						CompanyID:  row.CompanyID,
						Code:       "firing_failed",
						Err:        err,
					},
				)
			}
		}
	}
	return processed, ctx.Err()
}

// FireSchedule rechecks one candidate using the database clock. Queue delivery
// is merely a hint: this transaction is also the repair sweep's only firing core.
func (w *FiringWorker) FireSchedule(ctx context.Context, id uuid.UUID, path string) (bool, error) {
	return w.processSchedule(ctx, id, false, path)
}

func (w *FiringWorker) processSchedule(
	ctx context.Context,
	id uuid.UUID,
	drift bool,
	path string,
) (found bool, err error) {
	ctx, cancelAttempt := context.WithTimeout(ctx, 30*time.Second)
	defer cancelAttempt()
	observation := firingObservation{outcome: "noop"}
	defer func() {
		var stack []byte
		if value := recover(); value != nil {
			err = fmt.Errorf("Schedule firing panic: %v", value)
			stack = debug.Stack()
		}
		if err != nil {
			w.observeIssue(
				FiringIssue{
					Path:       path,
					ScheduleID: id,
					CompanyID:  observation.companyID,
					Code:       firingErrorCode(err),
					Err:        err,
					Stack:      stack,
				},
			)
		} else if observation.reason != "" || observation.outcome == "noop" {
			code := observation.reason
			if code == "" {
				code = "noop"
			}
			w.observeIssue(
				FiringIssue{Path: path, ScheduleID: id, CompanyID: observation.companyID, Code: code},
			)
		}
	}()
	pending := []events.Event{}
	found, runID, failure, err := w.fireNext(ctx, id, drift, &pending, &observation)
	if err != nil {
		if failure != nil && errors.Is(err, ErrMaterialization) &&
			(errors.Is(err, ErrConflict) || errors.Is(err, execution.ErrRunAgentNotBound)) {
			err := w.recordMaterializationFailure(ctx, *failure)
			observation.reason = ReasonMaterializationBlocked
			if err == nil {
				w.observeFiring(
					path,
					firingObservation{
						outcome: "needs_attention",
						reason:  ReasonMaterializationBlocked,
					},
				)
			}
			return true, err
		}
		if errors.Is(err, db.ErrCommitUnknown) {
			repairCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			w.repairWakeups(repairCtx, id)
		}
		if failure == nil && (errors.Is(err, ErrConflict) || errors.Is(err, pgx.ErrNoRows)) {
			return false, nil
		}
		return false, err
	}
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	publishPending(postCtx, w.bus, pending)
	if runID != nil {
		w.notifyRunEnqueued(postCtx, *runID)
	}
	// A worker dying after commit must not strand a queued Run or its cached empty claim.
	if !found {
		w.repairWakeups(postCtx, id)
	}
	w.observeFiring(path, observation)
	return found, nil
}

func (w *FiringWorker) repairWakeups(ctx context.Context, id uuid.UUID) {
	runs, err := w.store.Queries().ListWaitingScheduleRuns(ctx, id)
	if err != nil {
		return
	}
	for _, run := range runs {
		execution.PublishRuntimeWork(ctx, w.bus, run)
	}
}

func (w *FiringWorker) fireNext(
	ctx context.Context,
	id uuid.UUID,
	drift bool,
	pending *[]events.Event,
	observation *firingObservation,
) (bool, *uuid.UUID, *materializationFailure, error) {
	found := false
	var queuedRunID *uuid.UUID
	var failure *materializationFailure
	target, err := w.store.Queries().GetScheduleLockTarget(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, nil, nil
	}
	if err != nil {
		return false, nil, nil, err
	}
	observation.companyID = target.CompanyID
	err = w.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		if err := tx.SetScheduleTransactionTimeouts(ctx, "200ms"); err != nil {
			return err
		}
		if _, err := lockScheduleCreatorAuthority(ctx, tx, target.CompanyID, target.CreatorUserID); err != nil {
			return err
		}
		if err := tx.SetScheduleTransactionTimeouts(ctx, "5s"); err != nil {
			return err
		}
		due, err := tx.LockNextDueSchedule(ctx, generated.LockNextDueScheduleParams{
			ActorRoles: auth.RolesWith(
				auth.PermissionSchedulesManage,
			),
			ScheduleID:    id,
			IncludeNotDue: drift,
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
			observation.outcome, observation.reason = "paused", reason
			return w.pauseInvalid(ctx, tx, pending, current, reason)
		}
		if drift {
			found = false
			*pending = nil
			return nil
		}
		occurrenceAt, err := latestDueOccurrence(
			current.Trigger,
			due.Schedule.NextOccurrenceAt.Time,
			due.ServerTime.Time,
		)
		if err != nil {
			observation.outcome, observation.reason = "paused", ReasonTriggerInvalid
			return w.pauseInvalid(ctx, tx, pending, current, ReasonTriggerInvalid)
		}
		observation.lag = due.ServerTime.Time.Sub(due.Schedule.NextOccurrenceAt.Time)
		missed := missedOccurrences(current.Trigger, due.Schedule.NextOccurrenceAt.Time, occurrenceAt)
		overlap, overlapFound, err := lockActiveScheduleRun(ctx, tx, current)
		if err != nil {
			return err
		}
		if overlapFound && !waitingRun(overlap.Status) {
			observation.outcome = "skipped"
			skipped := MaterializeResult{
				Disposition: FiringDispositionSkipped,
				Reason:      ReasonRunActive,
				MissedCount: missed,
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
		observation.outcome, observation.reason = string(materialized.Disposition), materialized.Reason
		materialized.MissedCount = missed
		firing, err := insertFiring(
			ctx, tx, current, occurrenceAt, FiringTriggerScheduled, uuid.Nil, materialized,
		)
		if err != nil {
			return err
		}
		if err := linkMaterializedRun(ctx, tx, current, firing, materialized); err != nil {
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

func waitingRun(status string) bool { return status == "queued" || status == "deferred" }

func (w *FiringWorker) pauseInvalid(
	ctx context.Context,
	tx *generated.Queries,
	pending *[]events.Event,
	current Response,
	reason string,
) error {
	if err := w.invalidateActiveOccurrence(ctx, tx, pending, current, reason); err != nil {
		return err
	}
	_, err := tx.PauseInvalidDueSchedule(ctx, generated.PauseInvalidDueScheduleParams{
		Reason: reason, ScheduleID: current.ID, CompanyID: current.CompanyID, ExpectedVersion: current.Revision,
	})
	if err != nil {
		return err
	}
	return notifyAttentionInTx(ctx, tx, pending, current, reason)
}

func (w *FiringWorker) invalidateActiveOccurrence(
	ctx context.Context,
	tx *generated.Queries,
	pending *[]events.Event,
	current Response,
	reason string,
) error {
	overlap, found, err := lockActiveScheduleRun(ctx, tx, current)
	if err != nil {
		return err
	}
	if found {
		if waitingRun(overlap.Status) {
			if err := w.invalidator.InvalidateScheduleRunInTx(ctx, tx, pending, InvalidateQueuedRunInput{
				FiringID:       overlap.FiringID,
				RunID:          overlap.RunID,
				ScheduleID:     current.ID,
				CompanyID:      current.CompanyID,
				ConversationID: current.ConversationID,
			}); err != nil {
				return err
			}
		}
		if err := markNeedsAttentionFiring(ctx, tx, overlap, reason); err != nil {
			return err
		}
	}
	return nil
}

func (w *FiringWorker) recordMaterializationFailure(
	ctx context.Context,
	failure materializationFailure,
) error {
	if failure.Reason == "" {
		failure.Reason = ReasonMaterializationBlocked
	}
	var pending []events.Event
	err := w.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		if err := tx.SetScheduleTransactionTimeouts(ctx, "200ms"); err != nil {
			return err
		}
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
		if err := tx.SetScheduleTransactionTimeouts(ctx, "5s"); err != nil {
			return err
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
		if err := w.invalidateActiveOccurrence(ctx, tx, &pending, current, failure.Reason); err != nil {
			return err
		}
		result := MaterializeResult{
			Disposition: FiringDispositionNeedsAttention,
			Reason:      failure.Reason,
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
		if err := notifyAttentionInTx(ctx, tx, &pending, current, result.Reason); err != nil {
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
		postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		publishPending(postCtx, w.bus, pending)
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
	if err := tx.LockScheduleConversation(ctx, generated.LockScheduleConversationParams{
		ScheduleID: current.ID, CompanyID: current.CompanyID, ConversationID: current.ConversationID,
	}); err != nil {
		return generated.LockActiveScheduleRunRow{}, false, err
	}
	// Separate READ COMMITTED statement sees a retry that committed while the
	// Conversation lock was being acquired.
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
	switch {
	case materialized.Disposition == FiringDispositionNeedsAttention:
		status = StatusPaused
		pauseReason = pgtype.Text{String: materialized.Reason, Valid: true}
		next = cloneTime(current.NextOccurrenceAt)
	case current.Trigger.Type == TriggerTypeOnce:
		status = StatusCompleted
	default:
		value, err := nextOccurrence(current.Trigger, serverTime)
		if err != nil {
			return err
		}
		next = &value
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
		Disposition: string(materialized.Disposition), Reason: reason, MissedCount: materialized.MissedCount,
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
	case FiringDispositionMaterialized, FiringDispositionSuperseded, FiringDispositionCancelled,
		FiringDispositionSkipped, FiringDispositionNeedsAttention:
		return true
	default:
		return false
	}
}

func firingErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	if db.IsTransient(err) {
		return "transient"
	}
	if errors.Is(err, ErrMaterialization) {
		return "materialize_failed"
	}
	if errors.Is(err, ErrInvalidStoredState) {
		return "invalid_stored_state"
	}
	return "firing_failed"
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

type firingObservation struct {
	companyID       uuid.UUID
	outcome, reason string
	lag             time.Duration
}

func (w *FiringWorker) observeFiring(path string, observation firingObservation) {
	if w.options.OnFired == nil {
		return
	}
	defer func() { _ = recover() }()
	w.options.OnFired(path, observation.outcome, observation.reason, observation.lag)
}
