package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type ConversationUpdater interface {
	UpdateScheduleConversationInTx(
		context.Context,
		*generated.Queries,
		*[]events.Event,
		UpdateConversationInput,
	) error
}

type UpdateConversationInput struct {
	ID            uuid.UUID
	ScheduleID    uuid.UUID
	CompanyID     uuid.UUID
	CreatorUserID uuid.UUID
	AgentID       uuid.UUID
	Title         string
}

type ConversationUpdaterFunc func(
	context.Context,
	*generated.Queries,
	*[]events.Event,
	UpdateConversationInput,
) error

func (f ConversationUpdaterFunc) UpdateScheduleConversationInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	input UpdateConversationInput,
) error {
	return f(ctx, queries, pending, input)
}

// ScheduleService owns Schedule definitions, lifecycle changes, manual
// firings, and firing history.
type ScheduleService interface { //nolint:revive // AGENTS.md mandates <Domain>Service; bare Service is forbidden.
	Preview(context.Context, auth.UserPrincipal, PreviewInput) (PreviewResult, error)
	Create(context.Context, auth.UserPrincipal, CreateInput) (CreateResult, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (Response, error)
	List(context.Context, auth.UserPrincipal, ListOptions) (Page, error)
	Update(context.Context, auth.UserPrincipal, uuid.UUID, UpdateInput) (MutationResult, error)
	Pause(context.Context, auth.UserPrincipal, uuid.UUID, RevisionInput) (MutationResult, error)
	Activate(context.Context, auth.UserPrincipal, uuid.UUID, ReviewRevisionInput) (MutationResult, error)
	Resume(context.Context, auth.UserPrincipal, uuid.UUID, ReviewRevisionInput) (MutationResult, error)
	Cancel(context.Context, auth.UserPrincipal, uuid.UUID, RevisionInput) (MutationResult, error)
	ListFirings(context.Context, auth.UserPrincipal, uuid.UUID, FiringListOptions) (FiringPage, error)
	RunNow(context.Context, auth.UserPrincipal, uuid.UUID, RunNowInput) (RunNowResult, error)
}

var _ ScheduleService = (*DefaultScheduleService)(nil)

type ServiceOptions struct {
	Store               *db.Store
	Bus                 *events.Bus
	ConversationUpdater ConversationUpdater
	Materializer        Materializer
}

type DefaultScheduleService struct {
	store               *db.Store
	bus                 *events.Bus
	conversationUpdater ConversationUpdater
	materializer        Materializer
}

func NewScheduleService(options ServiceOptions) (*DefaultScheduleService, error) {
	if options.Store == nil || options.Bus == nil ||
		nilInterface(options.ConversationUpdater) || nilInterface(options.Materializer) {
		return nil, errors.New(
			"Schedule store, Conversation updater, and Run materializer are required",
		)
	}
	return &DefaultScheduleService{
		store: options.Store, bus: options.Bus,
		conversationUpdater: options.ConversationUpdater, materializer: options.Materializer,
	}, nil
}

func (s *DefaultScheduleService) Preview(
	ctx context.Context,
	principal auth.UserPrincipal,
	input PreviewInput,
) (PreviewResult, error) {
	if !validPrincipal(principal) {
		return PreviewResult{}, ErrUnauthenticated
	}
	normalized, err := normalizePreviewInput(input)
	if err != nil {
		return PreviewResult{}, err
	}
	var result PreviewResult
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		clock, err := tx.GetScheduleClock(ctx)
		if err != nil {
			return fmt.Errorf("read Schedule preview clock: %w", err)
		}
		result, err = resolvePreview(
			ctx, tx, principal.CompanyID, principal.UserID, normalized, clock, false,
		)
		return err
	})
	if err != nil {
		return PreviewResult{}, err
	}
	return result, nil
}

func (s *DefaultScheduleService) Create(
	ctx context.Context,
	principal auth.UserPrincipal,
	input CreateInput,
) (CreateResult, error) {
	if !validPrincipal(principal) {
		return CreateResult{}, ErrUnauthenticated
	}
	normalized, err := normalizeCreateInput(input)
	if err != nil {
		return CreateResult{}, err
	}
	var result CreateResult
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		clock, err := tx.GetScheduleClock(ctx)
		if err != nil {
			return fmt.Errorf("read Schedule create clock: %w", err)
		}
		replayed, replayedOK, err := createReplay(ctx, tx, principal, normalized)
		if err != nil {
			return err
		}
		if replayedOK {
			result = CreateResult{Schedule: replayed}
			return nil
		}
		preview, err := resolveReviewedPreview(ctx, tx, principal.CompanyID, principal.UserID, PreviewInput{
			AgentID:      normalized.AgentID,
			Name:         normalized.Name,
			Instructions: normalized.Instructions,
			PresetKey:    normalized.PresetKey,
			Trigger:      normalized.Trigger,
			ConnectorIDs: normalized.ConnectorIDs,
		}, clock)
		if err != nil {
			return err
		}
		if preview.ReviewDigest != normalized.ReviewDigest {
			return ErrReviewStale
		}
		var next *time.Time
		if normalized.Status == StatusActive {
			value := preview.Occurrences[0]
			next = &value
		}
		inserted, err := tx.InsertSchedule(ctx, insertScheduleParams(
			principal, normalized, preview, next,
		))
		if err != nil {
			return mapMutationError("insert Schedule", err)
		}
		row := scheduleFromInsertRow(inserted)
		if err := replaceSources(ctx, tx, row, preview.Sources); err != nil {
			return err
		}
		result = CreateResult{Schedule: responseFromRow(row, preview.Sources), Created: true}
		pending = append(pending, changedEvent(result.Schedule))
		return activity.RecordInTx(
			ctx,
			tx,
			&pending,
			events.UserActor(principal.UserID.String()),
			"conversation.created",
			nil,
			"conversation",
			row.ConversationID,
		)
	})
	if err != nil {
		return CreateResult{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

func (s *DefaultScheduleService) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
) (Response, error) {
	if !validPrincipal(principal) {
		return Response{}, ErrUnauthenticated
	}
	if !util.ValidUUID(scheduleID) {
		return Response{}, ErrNotFound
	}
	row, err := s.store.Queries().GetScheduleForCreator(
		ctx,
		generated.GetScheduleForCreatorParams{
			ActorRoles:  auth.RolesWith(auth.PermissionSchedulesManage),
			ActorUserID: principal.UserID, ScheduleID: scheduleID, CompanyID: principal.CompanyID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Response{}, ErrNotFound
	}
	if err != nil {
		return Response{}, fmt.Errorf("get Schedule: %w", err)
	}
	return responseWithSources(ctx, s.store.Queries(), row)
}

func (s *DefaultScheduleService) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	options ListOptions,
) (Page, error) {
	if !validPrincipal(principal) {
		return Page{}, ErrUnauthenticated
	}
	normalized, err := normalizeListOptions(options)
	if err != nil {
		return Page{}, err
	}
	params := generated.ListSchedulesForCreatorParams{
		ActorRoles:  auth.RolesWith(auth.PermissionSchedulesManage),
		ActorUserID: principal.UserID, CompanyID: principal.CompanyID,
		BeforeCreatedAt: requiredTime(time.Unix(0, 0).UTC()), PageSize: int32(normalized.Limit + 1),
	}
	if normalized.Status != nil {
		params.Status = string(*normalized.Status)
	}
	if normalized.PresetKey != nil {
		params.PresetKey = *normalized.PresetKey
	}
	if normalized.After != nil {
		params.HasCursor = true
		params.BeforeCreatedAt = requiredTime(normalized.After.CreatedAt)
		params.BeforeID = normalized.After.ID
	}
	rows, err := s.store.Queries().ListSchedulesForCreator(ctx, params)
	if err != nil {
		return Page{}, fmt.Errorf("list Schedules: %w", err)
	}
	hasNext := len(rows) > normalized.Limit
	if hasNext {
		rows = rows[:normalized.Limit]
	}
	page := Page{Schedules: make([]Response, 0, len(rows))}
	for _, row := range rows {
		response, err := responseWithSources(ctx, s.store.Queries(), row)
		if err != nil {
			return Page{}, err
		}
		page.Schedules = append(page.Schedules, response)
	}
	if hasNext {
		last := page.Schedules[len(page.Schedules)-1]
		page.NextCursor = &ListCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

func (s *DefaultScheduleService) Update(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
	input UpdateInput,
) (MutationResult, error) {
	if !validPrincipal(principal) {
		return MutationResult{}, ErrUnauthenticated
	}
	if !util.ValidUUID(scheduleID) {
		return MutationResult{}, ErrNotFound
	}
	normalized, err := normalizeUpdateInput(input)
	if err != nil {
		return MutationResult{}, err
	}
	var result MutationResult
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		current, err := lockResponse(ctx, tx, principal, scheduleID)
		if err != nil {
			return err
		}
		if current.Revision != normalized.ExpectedRevision {
			return ErrVersionConflict
		}
		if current.Status == StatusCompleted || current.Status == StatusCancelled {
			return ErrTerminal
		}
		previewInput := previewInputFromResponse(current)
		applyUpdate(&previewInput, normalized)
		clock, err := tx.GetScheduleClock(ctx)
		if err != nil {
			return fmt.Errorf("read Schedule update clock: %w", err)
		}
		preview, err := resolveReviewedPreview(
			ctx, tx, principal.CompanyID, principal.UserID, previewInput, clock,
		)
		if err != nil {
			return err
		}
		if preview.ReviewDigest != normalized.ReviewDigest {
			return ErrReviewStale
		}
		var next *time.Time
		if current.Status == StatusActive || current.Status == StatusPaused {
			value := preview.Occurrences[0]
			next = &value
		}
		row, err := tx.UpdateScheduleDefinition(ctx, generated.UpdateScheduleDefinitionParams{
			AgentID:          preview.AgentID,
			ReviewedProvider: preview.ReviewedProvider,
			Name:             preview.Name,
			Instructions:     preview.Instructions,
			PresetKey:        optionalText(preview.PresetKey),
			TriggerType:      string(preview.Trigger.Type),
			RunAt:            optionalTime(preview.Trigger.RunAt),
			CronExpression: optionalText(
				preview.Trigger.Expression,
			),
			Timezone:         optionalText(preview.Trigger.TimeZone),
			NextOccurrenceAt: optionalTime(next),
			ScheduleID:       scheduleID,
			CompanyID:        principal.CompanyID,
			ActorUserID:      principal.UserID,
			ExpectedVersion:  current.Revision,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionConflict
		}
		if err != nil {
			return mapMutationError("update Schedule", err)
		}
		if err := s.conversationUpdater.UpdateScheduleConversationInTx(
			ctx,
			tx,
			&pending,
			UpdateConversationInput{
				ID: current.ConversationID, ScheduleID: current.ID, CompanyID: current.CompanyID,
				CreatorUserID: current.CreatorUserID, AgentID: preview.AgentID, Title: preview.Name,
			},
		); err != nil {
			return fmt.Errorf("update Schedule Conversation: %w", err)
		}
		if err := replaceSources(ctx, tx, row, preview.Sources); err != nil {
			return err
		}
		result = MutationResult{Schedule: responseFromRow(row, preview.Sources)}
		pending = append(pending, changedEvent(result.Schedule))
		return nil
	})
	if err != nil {
		return MutationResult{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

func (s *DefaultScheduleService) Pause(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
	input RevisionInput,
) (MutationResult, error) {
	return s.mutateSimpleLifecycle(ctx, principal, scheduleID, input, StatusActive, func(
		ctx context.Context,
		tx *generated.Queries,
		current Response,
	) (generated.Schedule, error) {
		return tx.PauseSchedule(ctx, generated.PauseScheduleParams{
			Reason: ReasonUserPaused, ScheduleID: current.ID, CompanyID: current.CompanyID,
			ActorUserID: current.CreatorUserID, ExpectedVersion: current.Revision,
		})
	})
}

func (s *DefaultScheduleService) Activate(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
	input ReviewRevisionInput,
) (MutationResult, error) {
	return s.reviewLifecycle(ctx, principal, scheduleID, input, StatusDraft, func(
		ctx context.Context,
		tx *generated.Queries,
		current Response,
		preview PreviewResult,
	) (generated.Schedule, error) {
		return tx.ActivateSchedule(ctx, generated.ActivateScheduleParams{
			ReviewedProvider: preview.ReviewedProvider,
			NextOccurrenceAt: requiredTime(preview.Occurrences[0]),
			ScheduleID:       current.ID,
			CompanyID:        current.CompanyID,
			ActorUserID:      current.CreatorUserID,
			ExpectedVersion:  current.Revision,
		})
	})
}

func (s *DefaultScheduleService) Resume(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
	input ReviewRevisionInput,
) (MutationResult, error) {
	return s.reviewLifecycle(ctx, principal, scheduleID, input, StatusPaused, func(
		ctx context.Context,
		tx *generated.Queries,
		current Response,
		preview PreviewResult,
	) (generated.Schedule, error) {
		return tx.ResumeSchedule(ctx, generated.ResumeScheduleParams{
			ReviewedProvider: preview.ReviewedProvider,
			NextOccurrenceAt: requiredTime(preview.Occurrences[0]),
			ScheduleID:       current.ID,
			CompanyID:        current.CompanyID,
			ActorUserID:      current.CreatorUserID,
			ExpectedVersion:  current.Revision,
		})
	})
}

func (s *DefaultScheduleService) Cancel(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
	input RevisionInput,
) (MutationResult, error) {
	if !validPrincipal(principal) {
		return MutationResult{}, ErrUnauthenticated
	}
	if !util.ValidUUID(scheduleID) {
		return MutationResult{}, ErrNotFound
	}
	if !validVersion(input.ExpectedRevision) {
		return MutationResult{}, ErrInvalidInput
	}
	var result MutationResult
	var changed events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		current, err := lockResponse(ctx, tx, principal, scheduleID)
		if err != nil {
			return err
		}
		if current.Revision != input.ExpectedRevision {
			return ErrVersionConflict
		}
		if current.Status == StatusCompleted || current.Status == StatusCancelled {
			return ErrTerminal
		}
		row, err := tx.CancelSchedule(ctx, generated.CancelScheduleParams{
			ScheduleID: scheduleID, CompanyID: principal.CompanyID,
			ActorUserID: principal.UserID, ExpectedVersion: current.Revision,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionConflict
		}
		if err != nil {
			return mapMutationError("cancel Schedule", err)
		}
		result = MutationResult{Schedule: responseFromRow(row, current.Sources)}
		changed = changedEvent(result.Schedule)
		return nil
	})
	if err != nil {
		return MutationResult{}, err
	}
	s.bus.Publish(ctx, Topic, changed)
	return result, nil
}

func (s *DefaultScheduleService) ListFirings(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
	options FiringListOptions,
) (FiringPage, error) {
	if !validPrincipal(principal) {
		return FiringPage{}, ErrUnauthenticated
	}
	if !util.ValidUUID(scheduleID) {
		return FiringPage{}, ErrNotFound
	}
	normalized, err := normalizeFiringListOptions(options)
	if err != nil {
		return FiringPage{}, err
	}
	// The firing query is creator-scoped and would simply return no rows for
	// a Schedule that is missing or another creator's; resolve the Schedule
	// first so that case is ErrNotFound, as it is for Get and RunNow.
	if _, err := s.store.Queries().GetScheduleForCreator(ctx, generated.GetScheduleForCreatorParams{
		ActorRoles:  auth.RolesWith(auth.PermissionSchedulesManage),
		ActorUserID: principal.UserID, ScheduleID: scheduleID, CompanyID: principal.CompanyID,
	}); errors.Is(err, pgx.ErrNoRows) {
		return FiringPage{}, ErrNotFound
	} else if err != nil {
		return FiringPage{}, fmt.Errorf("get Schedule for firing history: %w", err)
	}
	params := generated.ListScheduleFiringsForCreatorParams{
		ActorRoles:  auth.RolesWith(auth.PermissionSchedulesManage),
		ActorUserID: principal.UserID, ScheduleID: scheduleID, CompanyID: principal.CompanyID,
		BeforeOccurrenceAt: requiredTime(time.Unix(0, 0).UTC()), PageSize: int32(normalized.Limit + 1),
	}
	if normalized.After != nil {
		params.HasCursor = true
		params.BeforeOccurrenceAt = requiredTime(normalized.After.OccurrenceAt)
		params.BeforeID = normalized.After.ID
	}
	rows, err := s.store.Queries().ListScheduleFiringsForCreator(ctx, params)
	if err != nil {
		return FiringPage{}, fmt.Errorf("list Schedule firings: %w", err)
	}
	hasNext := len(rows) > normalized.Limit
	if hasNext {
		rows = rows[:normalized.Limit]
	}
	page := FiringPage{Firings: make([]Firing, 0, len(rows))}
	for _, row := range rows {
		page.Firings = append(page.Firings, firingWithResultFromRow(row))
	}
	if hasNext {
		last := page.Firings[len(page.Firings)-1]
		page.NextCursor = &FiringCursor{OccurrenceAt: last.OccurrenceAt, ID: last.ID}
	}
	return page, nil
}

func (s *DefaultScheduleService) RunNow(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
	input RunNowInput,
) (RunNowResult, error) {
	if !validPrincipal(principal) {
		return RunNowResult{}, ErrUnauthenticated
	}
	if !util.ValidUUID(scheduleID) {
		return RunNowResult{}, ErrNotFound
	}
	normalized, err := normalizeRunNowInput(input)
	if err != nil {
		return RunNowResult{}, err
	}
	var result RunNowResult
	var pending []events.Event
	var queuedRunID uuid.UUID
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = pending[:0]
		queuedRunID = uuid.Nil
		current, err := lockResponse(ctx, tx, principal, scheduleID)
		if err != nil {
			return err
		}
		replay, err := tx.GetScheduleFiringReplay(ctx, generated.GetScheduleFiringReplayParams{
			CompanyID: principal.CompanyID, ScheduleID: scheduleID,
			Trigger:    string(FiringTriggerManual),
			RequestKey: uuid.NullUUID{UUID: normalized.RequestKey, Valid: true},
		})
		if err == nil {
			firing, err := firingWithResult(ctx, tx, replay)
			result = RunNowResult{Firing: firing}
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("look up manual Schedule firing replay: %w", err)
		}
		if current.Status != StatusActive {
			return ErrInvalidState
		}
		active, err := tx.HasActiveScheduleRun(ctx, generated.HasActiveScheduleRunParams{
			ScheduleID: scheduleID, CompanyID: principal.CompanyID,
		})
		if err != nil {
			return fmt.Errorf("check active Schedule Run: %w", err)
		}
		if active {
			return ErrRunActive
		}
		clock, err := tx.GetScheduleClock(ctx)
		if err != nil {
			return fmt.Errorf("read manual Schedule firing clock: %w", err)
		}
		materializeRequest, err := materializeInput(
			current, clock.Time, FiringTriggerManual,
		)
		if err != nil {
			return err
		}
		materialized, err := s.materializer.MaterializeScheduleRunInTx(
			ctx,
			tx,
			&pending,
			materializeRequest,
		)
		// The creator's binding, or the Agent itself, can go before the due
		// worker's drift pass pauses the Schedule. A missing binding is the
		// setup step a review names too; anything else the Schedule can no
		// longer run on is a conflict. Neither is a failure.
		if errors.Is(err, execution.ErrRunAgentNotBound) {
			return ErrAgentBindingRequired
		}
		if errors.Is(err, ErrConflict) {
			return ErrConflict
		}
		if err != nil {
			return errors.Join(ErrMaterialization, err)
		}
		if materialized.Disposition != FiringDispositionMaterialized {
			return ErrInvalidStoredState
		}
		row, err := insertFiring(
			ctx, tx, current, clock.Time, FiringTriggerManual, normalized.RequestKey, materialized,
		)
		if err != nil {
			return err
		}
		if err := linkMaterializedRun(ctx, tx, current, row, materializeRequest, materialized); err != nil {
			return err
		}
		if _, err := tx.RecordManualScheduleFiring(ctx, generated.RecordManualScheduleFiringParams{
			OccurrenceAt: clock, ScheduleID: current.ID, CompanyID: current.CompanyID,
			ExpectedVersion: current.Revision,
		}); err != nil {
			return fmt.Errorf("record manual Schedule firing: %w", err)
		}
		firing, err := firingWithResult(ctx, tx, row)
		if err != nil {
			return err
		}
		result = RunNowResult{Firing: firing, Created: true}
		pending = append(pending, changedEvent(current))
		queuedRunID = materialized.RunID
		return nil
	})
	if err != nil {
		return RunNowResult{}, err
	}
	publishPending(ctx, s.bus, pending)
	if queuedRunID != uuid.Nil {
		run, loadErr := s.store.Queries().GetRun(ctx, queuedRunID)
		if loadErr == nil {
			execution.PublishRuntimeWork(ctx, s.bus, run)
		}
	}
	return result, nil
}

type lifecycleMutation func(
	context.Context,
	*generated.Queries,
	Response,
) (generated.Schedule, error)

func (s *DefaultScheduleService) mutateSimpleLifecycle(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
	input RevisionInput,
	expected Status,
	mutate lifecycleMutation,
) (MutationResult, error) {
	if !validPrincipal(principal) {
		return MutationResult{}, ErrUnauthenticated
	}
	if !util.ValidUUID(scheduleID) {
		return MutationResult{}, ErrNotFound
	}
	if !validVersion(input.ExpectedRevision) {
		return MutationResult{}, ErrInvalidInput
	}
	var result MutationResult
	var changed events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		current, err := lockResponse(ctx, tx, principal, scheduleID)
		if err != nil {
			return err
		}
		if current.Revision != input.ExpectedRevision {
			return ErrVersionConflict
		}
		if current.Status != expected {
			return ErrInvalidState
		}
		row, err := mutate(ctx, tx, current)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionConflict
		}
		if err != nil {
			return mapMutationError("mutate Schedule lifecycle", err)
		}
		result = MutationResult{Schedule: responseFromRow(row, current.Sources)}
		changed = changedEvent(result.Schedule)
		return nil
	})
	if err != nil {
		return MutationResult{}, err
	}
	s.bus.Publish(ctx, Topic, changed)
	return result, nil
}

type reviewLifecycleMutation func(
	context.Context,
	*generated.Queries,
	Response,
	PreviewResult,
) (generated.Schedule, error)

func (s *DefaultScheduleService) reviewLifecycle(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
	input ReviewRevisionInput,
	expected Status,
	mutate reviewLifecycleMutation,
) (MutationResult, error) {
	if !validPrincipal(principal) {
		return MutationResult{}, ErrUnauthenticated
	}
	if !util.ValidUUID(scheduleID) {
		return MutationResult{}, ErrNotFound
	}
	if !validVersion(input.ExpectedRevision) || !digestPattern.MatchString(input.ReviewDigest) {
		return MutationResult{}, ErrInvalidInput
	}
	var result MutationResult
	var changed events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		current, err := lockResponse(ctx, tx, principal, scheduleID)
		if err != nil {
			return err
		}
		if current.Revision != input.ExpectedRevision {
			return ErrVersionConflict
		}
		if current.Status != expected {
			return ErrInvalidState
		}
		clock, err := tx.GetScheduleClock(ctx)
		if err != nil {
			return fmt.Errorf("read Schedule lifecycle clock: %w", err)
		}
		preview, err := resolveReviewedPreview(
			ctx, tx, principal.CompanyID, principal.UserID, previewInputFromResponse(current), clock,
		)
		if err != nil {
			return err
		}
		if preview.ReviewDigest != input.ReviewDigest {
			return ErrReviewStale
		}
		row, err := mutate(ctx, tx, current, preview)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionConflict
		}
		if err != nil {
			return mapMutationError("mutate reviewed Schedule lifecycle", err)
		}
		if err := replaceSources(ctx, tx, row, preview.Sources); err != nil {
			return err
		}
		result = MutationResult{Schedule: responseFromRow(row, preview.Sources)}
		changed = changedEvent(result.Schedule)
		return nil
	})
	if err != nil {
		return MutationResult{}, err
	}
	s.bus.Publish(ctx, Topic, changed)
	return result, nil
}

// resolvePreview reads the selection as the Schedule's creator sees it: the
// Agent resolves only while they have it set up on one of their own live
// runtimes, because that binding is where every firing will run (ADR 0014).
func resolvePreview(
	ctx context.Context,
	tx *generated.Queries,
	companyID, creatorUserID uuid.UUID,
	input PreviewInput,
	clock pgtype.Timestamptz,
	lockAgent bool,
) (PreviewResult, error) {
	var provider string
	var err error
	if lockAgent {
		row, queryErr := tx.LockActiveScheduleAgent(ctx, generated.LockActiveScheduleAgentParams{
			AgentID: input.AgentID, CompanyID: companyID, CreatorUserID: creatorUserID,
		})
		provider, err = row.Provider, queryErr
	} else {
		row, queryErr := tx.GetActiveScheduleAgent(ctx, generated.GetActiveScheduleAgentParams{
			AgentID: input.AgentID, CompanyID: companyID, CreatorUserID: creatorUserID,
		})
		provider, err = row.Provider, queryErr
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Name the one reason the creator can fix — an active Agent they
		// have not set up — apart from an Agent they cannot schedule at all.
		needsBinding, classifyErr := tx.ScheduleAgentNeedsCreatorBinding(
			ctx,
			generated.ScheduleAgentNeedsCreatorBindingParams{
				CreatorUserID: creatorUserID, AgentID: input.AgentID, CompanyID: companyID,
			},
		)
		if classifyErr != nil {
			return PreviewResult{}, fmt.Errorf("classify unavailable Schedule Agent: %w", classifyErr)
		}
		if needsBinding {
			return PreviewResult{}, ErrAgentBindingRequired
		}
		return PreviewResult{}, ErrSelectionUnavailable
	}
	if err != nil {
		return PreviewResult{}, fmt.Errorf("resolve Schedule Agent: %w", err)
	}
	preview, err := PreviewTrigger(input.Trigger, clock.Time, defaultPreviewLimit)
	if err != nil {
		return PreviewResult{}, err
	}
	sources, err := resolveSources(ctx, tx, companyID, provider, input.ConnectorIDs)
	if err != nil {
		return PreviewResult{}, err
	}
	result := PreviewResult{
		AgentID: input.AgentID, ReviewedProvider: provider, Name: input.Name,
		Instructions: input.Instructions, PresetKey: cloneString(input.PresetKey),
		Trigger: preview.Trigger, Sources: sources, Occurrences: preview.Occurrences,
	}
	result.ReviewDigest, err = computeReviewDigest(result)
	if err != nil {
		return PreviewResult{}, err
	}
	return result, nil
}

// resolveReviewedPreview re-reviews a definition inside a write that echoes
// a digest, locking the Agent row. The caller reviewed this selection once;
// an Agent or source that no longer resolves means that review is stale. An
// Agent the creator no longer has set up stays ErrAgentBindingRequired: a
// fresh review would not help, setting it up again would.
func resolveReviewedPreview(
	ctx context.Context,
	tx *generated.Queries,
	companyID, creatorUserID uuid.UUID,
	input PreviewInput,
	clock pgtype.Timestamptz,
) (PreviewResult, error) {
	result, err := resolvePreview(ctx, tx, companyID, creatorUserID, input, clock, true)
	if errors.Is(err, ErrSelectionUnavailable) {
		return PreviewResult{}, ErrReviewStale
	}
	return result, err
}

func resolveSources(
	ctx context.Context,
	tx *generated.Queries,
	companyID uuid.UUID,
	provider string,
	connectorIDs []uuid.UUID,
) ([]Source, error) {
	if len(connectorIDs) == 0 {
		return []Source{}, nil
	}
	rows, err := tx.ResolveScheduleSources(ctx, generated.ResolveScheduleSourcesParams{
		CompanyID: companyID, Provider: provider, ConnectorIds: connectorIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve Schedule sources: %w", err)
	}
	if len(rows) != len(connectorIDs) {
		return nil, ErrSelectionUnavailable
	}
	sources := make([]Source, len(rows))
	for index, row := range rows {
		sources[index] = Source{
			ConnectorID: row.ConnectorID, PolicyRevision: row.PolicyRevision,
			Provider: row.Provider, Source: row.Source, NativeID: row.NativeID,
			ReviewedContractDigest: row.ReviewedContractDigest.String, Position: row.Position,
		}
	}
	return sources, nil
}

func createReplay(
	ctx context.Context,
	tx *generated.Queries,
	principal auth.UserPrincipal,
	input CreateInput,
) (Response, bool, error) {
	row, err := tx.GetScheduleCreateReplay(ctx, generated.GetScheduleCreateReplayParams{
		ActorRoles:    auth.RolesWith(auth.PermissionSchedulesManage),
		CreatorUserID: principal.UserID, CompanyID: principal.CompanyID, RequestKey: input.RequestKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Response{}, false, nil
	}
	if err != nil {
		return Response{}, false, fmt.Errorf("look up Schedule create replay: %w", err)
	}
	response, err := responseWithSources(ctx, tx, row)
	if err != nil {
		return Response{}, false, err
	}
	trigger, err := parseStoredTrigger(input.Trigger)
	if err != nil {
		return Response{}, false, ErrInvalidInput
	}
	if response.AgentID != input.AgentID || response.Name != input.Name ||
		response.Instructions != input.Instructions ||
		!equalOptionalText(response.PresetKey, input.PresetKey) || response.Status != input.Status ||
		!equalTrigger(response.Trigger, trigger.trigger) ||
		!slices.Equal(sourceConnectorIDs(response.Sources), input.ConnectorIDs) {
		return Response{}, false, ErrConflict
	}
	digest, err := computeReviewDigest(PreviewResult{
		AgentID: response.AgentID, ReviewedProvider: response.ReviewedProvider,
		Name: response.Name, Instructions: response.Instructions, PresetKey: response.PresetKey,
		Trigger: response.Trigger, Sources: response.Sources,
	})
	if err != nil {
		return Response{}, false, err
	}
	if digest != input.ReviewDigest {
		return Response{}, false, ErrConflict
	}
	return response, true, nil
}

func lockResponse(
	ctx context.Context,
	tx *generated.Queries,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
) (Response, error) {
	authorized, err := lockScheduleCreatorAuthority(
		ctx,
		tx,
		principal.CompanyID,
		principal.UserID,
	)
	if err != nil {
		return Response{}, err
	}
	if !authorized {
		return Response{}, ErrNotFound
	}
	row, err := tx.GetScheduleForCreatorForUpdate(ctx, generated.GetScheduleForCreatorForUpdateParams{
		ActorRoles:  auth.RolesWith(auth.PermissionSchedulesManage),
		ActorUserID: principal.UserID, ScheduleID: scheduleID, CompanyID: principal.CompanyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Response{}, ErrNotFound
	}
	if err != nil {
		return Response{}, fmt.Errorf("lock Schedule: %w", err)
	}
	return responseWithSources(ctx, tx, row)
}

// lockScheduleCreatorAuthority fences producer transactions before they lock
// a Schedule or its Conversation. Account deletion takes the same Company row
// exclusively before revoking memberships, so one side completes as a unit.
func lockScheduleCreatorAuthority(
	ctx context.Context,
	queries *generated.Queries,
	companyID uuid.UUID,
	creatorUserID uuid.UUID,
) (bool, error) {
	companyAvailable, err := lockScheduleCompanyAuthority(ctx, queries, companyID)
	if err != nil || !companyAvailable {
		return false, err
	}
	return lockScheduleCreatorMembership(ctx, queries, companyID, creatorUserID)
}

func lockScheduleCompanyAuthority(
	ctx context.Context,
	queries *generated.Queries,
	companyID uuid.UUID,
) (bool, error) {
	if _, err := queries.LockCompanyForMembershipRead(ctx, companyID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("lock Schedule Company: %w", err)
	}
	return true, nil
}

func lockScheduleCreatorMembership(
	ctx context.Context,
	queries *generated.Queries,
	companyID uuid.UUID,
	creatorUserID uuid.UUID,
) (bool, error) {
	membership, err := queries.GetCompanyMemberForShare(
		ctx,
		generated.GetCompanyMemberForShareParams{
			CompanyID: companyID,
			UserID:    creatorUserID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock Schedule creator membership: %w", err)
	}
	return membership.Status == "active" &&
		auth.CompanyRole(membership.Role).Can(auth.PermissionSchedulesManage), nil
}

func responseWithSources(
	ctx context.Context,
	queries *generated.Queries,
	row generated.Schedule,
) (Response, error) {
	sourceRows, err := queries.ListScheduleSources(ctx, generated.ListScheduleSourcesParams{
		ScheduleID: row.ID, CompanyID: row.CompanyID,
	})
	if err != nil {
		return Response{}, fmt.Errorf("list Schedule sources: %w", err)
	}
	sources := make([]Source, len(sourceRows))
	for index, sourceRow := range sourceRows {
		sources[index] = sourceFromRow(sourceRow)
	}
	return responseFromRow(row, sources), nil
}

func responseFromRow(row generated.Schedule, sources []Source) Response {
	trigger := Trigger{Type: TriggerType(row.TriggerType)}
	if trigger.Type == TriggerTypeOnce {
		runAt := row.RunAt.Time.UTC()
		trigger.RunAt = &runAt
	} else {
		trigger.Expression = cloneString(&row.CronExpression.String)
		trigger.TimeZone = cloneString(&row.Timezone.String)
	}
	return Response{
		ID:               row.ID,
		CompanyID:        row.CompanyID,
		ConversationID:   row.ConversationID,
		CreatorUserID:    row.CreatorUserID,
		AgentID:          row.AgentID,
		ReviewedProvider: row.ReviewedProvider,
		Name:             row.Name,
		Instructions:     row.Instructions,
		PresetKey:        optionalString(row.PresetKey),
		Trigger:          trigger,
		Sources:          slices.Clone(sources),
		NextOccurrenceAt: nullableTime(
			row.NextOccurrenceAt,
		),
		LastOccurrenceAt: nullableTime(row.LastOccurrenceAt),
		Status:           Status(row.Status),
		PauseReason:      optionalString(row.PauseReason),
		CreatedAt:        row.CreatedAt.Time.UTC(),
		UpdatedAt:        row.UpdatedAt.Time.UTC(),
		Revision:         row.Version,
	}
}

func sourceFromRow(row generated.ScheduleSource) Source {
	return Source{
		ConnectorID: row.ConnectorID, PolicyRevision: row.PolicyRevision,
		Provider: row.Provider, Source: row.Source, NativeID: row.NativeID,
		ReviewedContractDigest: row.ReviewedContractDigest, Position: row.Position,
	}
}

func firingFromRow(row generated.ScheduleFiring) Firing {
	trigger := FiringTrigger(row.Trigger)
	disposition := FiringDisposition(row.Disposition)
	var messageID *uuid.UUID
	if row.MessageID.Valid {
		value := row.MessageID.UUID
		messageID = &value
	}
	var runID *uuid.UUID
	if row.RunID.Valid {
		value := row.RunID.UUID
		runID = &value
	}
	return Firing{
		ID: row.ID, CompanyID: row.CompanyID, ScheduleID: row.ScheduleID,
		ScheduleVersion: row.ScheduleVersion, ConversationID: row.ConversationID,
		MessageID: messageID, RunID: runID, OccurrenceAt: row.OccurrenceAt.Time.UTC(),
		Trigger: trigger, Disposition: disposition, Reason: optionalString(row.Reason),
		CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(), Revision: row.Version,
	}
}

// firingWithResult reads one firing back in the history's shape, so Run now
// answers with the prompt's seq and the Run's status as the list does. The
// two queries share one result column list: the row conversion stops
// compiling the moment either drifts.
func firingWithResult(
	ctx context.Context,
	tx *generated.Queries,
	row generated.ScheduleFiring,
) (Firing, error) {
	withResult, err := tx.GetScheduleFiringWithResult(ctx, generated.GetScheduleFiringWithResultParams{
		FiringID: row.ID, CompanyID: row.CompanyID,
	})
	if err != nil {
		return Firing{}, fmt.Errorf("read Schedule firing result: %w", err)
	}
	return firingWithResultFromRow(generated.ListScheduleFiringsForCreatorRow(withResult)), nil
}

func firingWithResultFromRow(row generated.ListScheduleFiringsForCreatorRow) Firing {
	firing := firingFromRow(row.ScheduleFiring)
	firing.RunStatus = optionalString(row.RunStatus)
	if row.HasOutput {
		firing.Output = cloneString(&row.Output)
	}
	if row.MessageSeq.Valid {
		messageSeq := row.MessageSeq.Int64
		firing.MessageSeq = &messageSeq
	}
	return firing
}

func replaceSources(
	ctx context.Context,
	tx *generated.Queries,
	row generated.Schedule,
	sources []Source,
) error {
	_, err := tx.DeleteScheduleSources(ctx, generated.DeleteScheduleSourcesParams{
		ScheduleID: row.ID, CompanyID: row.CompanyID,
	})
	if err != nil {
		return fmt.Errorf("delete Schedule sources: %w", err)
	}
	encoded, err := encodeSources(row.CompanyID, sources)
	if err != nil {
		return err
	}
	inserted, err := tx.InsertScheduleSources(ctx, generated.InsertScheduleSourcesParams{
		ScheduleID: row.ID, Sources: encoded,
	})
	if err != nil {
		return mapMutationError("insert Schedule sources", err)
	}
	if inserted != int64(len(sources)) {
		return ErrInvalidStoredState
	}
	return nil
}

type sourceInsert struct {
	ConnectorID            uuid.UUID `json:"connectorId"`
	CompanyID              uuid.UUID `json:"companyId"`
	Provider               string    `json:"provider"`
	Source                 string    `json:"source"`
	NativeID               string    `json:"nativeId"`
	ReviewedContractDigest string    `json:"reviewedContractDigest"`
	PolicyRevision         int64     `json:"policyRevision"`
	Position               int32     `json:"position"`
}

func encodeSources(companyID uuid.UUID, sources []Source) (json.RawMessage, error) {
	items := make([]sourceInsert, len(sources))
	for index, source := range sources {
		items[index] = sourceInsert{
			ConnectorID: source.ConnectorID, CompanyID: companyID, Provider: source.Provider,
			Source: source.Source, NativeID: source.NativeID,
			ReviewedContractDigest: source.ReviewedContractDigest,
			PolicyRevision:         source.PolicyRevision, Position: int32(index),
		}
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil, errors.New("encode Schedule sources")
	}
	return encoded, nil
}

func insertScheduleParams(
	principal auth.UserPrincipal,
	input CreateInput,
	preview PreviewResult,
	next *time.Time,
) generated.InsertScheduleParams {
	return generated.InsertScheduleParams{
		ActorRoles:       auth.RolesWith(auth.PermissionSchedulesManage),
		CompanyID:        principal.CompanyID,
		CreatorUserID:    principal.UserID,
		RequestKey:       input.RequestKey,
		AgentID:          input.AgentID,
		ReviewedProvider: preview.ReviewedProvider,
		Name:             preview.Name,
		Instructions:     preview.Instructions,
		PresetKey:        optionalText(preview.PresetKey),
		TriggerType:      string(preview.Trigger.Type),
		RunAt:            optionalTime(preview.Trigger.RunAt),
		CronExpression: optionalText(
			preview.Trigger.Expression,
		),
		Timezone:         optionalText(preview.Trigger.TimeZone),
		NextOccurrenceAt: optionalTime(next),
		Status:           string(input.Status),
	}
}

func scheduleFromInsertRow(row generated.InsertScheduleRow) generated.Schedule {
	return generated.Schedule(row)
}

func previewInputFromResponse(value Response) PreviewInput {
	return PreviewInput{
		AgentID: value.AgentID, Name: value.Name, Instructions: value.Instructions,
		PresetKey: cloneString(value.PresetKey), Trigger: cloneTrigger(value.Trigger),
		ConnectorIDs: sourceConnectorIDs(value.Sources),
	}
}

func applyUpdate(target *PreviewInput, input UpdateInput) {
	if input.AgentID != nil {
		target.AgentID = *input.AgentID
	}
	if input.Name != nil {
		target.Name = *input.Name
	}
	if input.Instructions != nil {
		target.Instructions = *input.Instructions
	}
	if input.PresetKey.Present {
		target.PresetKey = cloneString(input.PresetKey.Value)
	}
	if input.Trigger != nil {
		target.Trigger = cloneTrigger(*input.Trigger)
	}
	if input.Sources.Present {
		target.ConnectorIDs = slices.Clone(input.Sources.ConnectorIDs)
	}
}

func sourceConnectorIDs(sources []Source) []uuid.UUID {
	result := make([]uuid.UUID, len(sources))
	for index, source := range sources {
		result[index] = source.ConnectorID
	}
	return result
}

func optionalText(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func optionalString(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return cloneString(&value.String)
}

func nullableTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	utc := value.Time.UTC()
	return &utc
}

func optionalTime(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return requiredTime(*value)
}

func requiredTime(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTrigger(value Trigger) Trigger {
	result := Trigger{Type: value.Type, RunAt: cloneTime(value.RunAt)}
	result.Expression = cloneString(value.Expression)
	result.TimeZone = cloneString(value.TimeZone)
	return result
}

func equalOptionalText(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func equalTrigger(left, right Trigger) bool {
	return left.Type == right.Type && equalOptionalText(left.Expression, right.Expression) &&
		equalOptionalText(left.TimeZone, right.TimeZone) &&
		(left.RunAt == nil && right.RunAt == nil || left.RunAt != nil && right.RunAt != nil && left.RunAt.Equal(*right.RunAt))
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func mapMutationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch pgErr.Code {
		case "23505":
			return ErrConflict
		case "23514":
			return ErrInvalidInput
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}
