package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/schedule"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// CalendarService owns scheduled Task assignments. Connector Schedules
// retain their private Conversation and execution lane; these are ordinary
// Project Tasks and use the existing Task execution kernel.
type CalendarService interface {
	DecideAssignment(context.Context, auth.UserPrincipal, uuid.UUID, int64, bool) (MutationResult, error)
	GetSchedule(context.Context, auth.UserPrincipal, uuid.UUID) (CalendarSchedule, error)
	UpdateSchedule(context.Context, auth.UserPrincipal, uuid.UUID, CalendarScheduleChange) (MutationResult, error)
	ListOccurrences(context.Context, auth.UserPrincipal, time.Time, time.Time) (CalendarPage, error)
	UpdateOccurrence(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		CalendarOccurrenceChange,
	) (MutationResult, error)
	ProcessDue(context.Context, time.Time, int) error
}

var _ CalendarService = (*DefaultCalendarService)(nil)

type DefaultCalendarService struct{ tasks *DefaultTaskService }

func NewCalendarService(tasks *DefaultTaskService) *DefaultCalendarService {
	return &DefaultCalendarService{tasks: tasks}
}

type CalendarSchedule struct {
	ID             uuid.UUID        `json:"id"`
	TemplateTaskID uuid.UUID        `json:"templateTaskId"`
	Trigger        schedule.Trigger `json:"trigger"`
	Paused         bool             `json:"paused"`
	Revision       int64            `json:"revision"`
}

type CalendarScheduleChange struct {
	ExpectedRevision int64             `json:"expectedRevision"`
	Trigger          *schedule.Trigger `json:"trigger"`
	Paused           bool              `json:"paused"`
}

type CalendarOccurrenceChange struct {
	ExpectedRevision int64      `json:"expectedRevision"`
	OccurrenceAt     time.Time  `json:"occurrenceAt"`
	RunAt            *time.Time `json:"runAt"`
	Cancel           bool       `json:"cancel"`
	Title            *string    `json:"title"`
	Description      *string    `json:"description"`
}

type CalendarOccurrence struct {
	Task         Response         `json:"task"`
	Schedule     CalendarSchedule `json:"schedule"`
	OccurrenceAt time.Time        `json:"occurrenceAt"`
	RunAt        time.Time        `json:"runAt"`
	Materialized bool             `json:"materialized"`
}

type CalendarPage struct {
	Occurrences []CalendarOccurrence `json:"occurrences"`
	Truncated   bool                 `json:"truncated"`
}

func calendarTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	at := value.Time.UTC()
	return &at
}

func calendarPG(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func calendarSchedule(row generated.TaskSchedule) (CalendarSchedule, error) {
	result := CalendarSchedule{
		ID:             row.ID,
		TemplateTaskID: row.TemplateTaskID,
		Paused:         row.Paused,
		Revision:       row.Version,
	}
	if err := json.Unmarshal(row.Trigger, &result.Trigger); err != nil {
		return result, fmt.Errorf("decode Task schedule: %w", err)
	}
	return result, nil
}

func setCalendarInTx(ctx context.Context, q *generated.Queries, value Response) (Response, error) {
	row, err := q.SetTaskCalendar(ctx, generated.SetTaskCalendarParams{
		TaskID:      value.ID,
		CompanyID:   value.CompanyID,
		ScheduledAt: calendarPG(value.ScheduledAt),
		TaskScheduleID: util.NullUUIDFromPtr(
			value.ScheduleID,
		),
		ScheduleOccurrenceAt:   calendarPG(value.ScheduleOccurrenceAt),
		TaskScheduleGeneration: value.ScheduleGeneration,
		ScheduleFiredAt:        calendarPG(value.ScheduleFiredAt),
		ScheduleDisposition:    value.ScheduleDisposition,
		AssignmentStatus:       value.AssignmentStatus,
		Status:                 string(value.Status),
	})
	if err != nil {
		return Response{}, mapTaskMutationError("write Task calendar", err)
	}
	return taskFromRow(row), nil
}

func updateTaskCalendarInTx(
	ctx context.Context,
	q *generated.Queries,
	value Response,
	pending *[]events.Event,
	actor events.Actor,
) (Response, error) {
	before, err := activity.ObserveInTx(ctx, q, Topic, value.ID)
	if err != nil {
		return Response{}, err
	}
	updated, err := setCalendarInTx(ctx, q, value)
	if err != nil {
		return Response{}, err
	}
	if err := activity.RecordInTx(ctx, q, pending, actor, EventUpdated, before, Topic, value.ID); err != nil {
		return Response{}, err
	}
	return updated, nil
}

func createTaskCalendarInTx(
	ctx context.Context,
	q *generated.Queries,
	value Response,
	trigger schedule.Trigger,
) (Response, error) {
	preview, err := schedule.PreviewTrigger(trigger, time.Now().UTC(), 2)
	if err != nil || len(preview.Occurrences) == 0 {
		return Response{}, ErrInvalidInput
	}
	encoded, err := json.Marshal(preview.Trigger)
	if err != nil {
		return Response{}, err
	}
	next := pgtype.Timestamptz{}
	if len(preview.Occurrences) > 1 {
		next = calendarPG(&preview.Occurrences[1])
	}
	row, err := q.InsertTaskSchedule(ctx, generated.InsertTaskScheduleParams{
		CompanyID:           value.CompanyID,
		ProjectID:           value.ProjectID,
		TemplateTaskID:      value.ID,
		Trigger:             encoded,
		NextOccurrenceAt:    next,
		TemplateTitle:       value.Title,
		TemplateDescription: value.Description,
	})
	if err != nil {
		return Response{}, fmt.Errorf("create Task schedule: %w", err)
	}
	value.ScheduleID = &row.ID
	value.ScheduleGeneration = row.Generation
	value.ScheduledAt, value.ScheduleOccurrenceAt = &preview.Occurrences[0], &preview.Occurrences[0]
	value.ScheduleDisposition = "scheduled"
	return setCalendarInTx(ctx, q, value)
}

func (s *DefaultCalendarService) DecideAssignment(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	revision int64,
	accept bool,
) (MutationResult, error) {
	var effects taskWriteEffects
	err := s.tasks.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		current, _, access, err := lockTaskMutationTarget(ctx, q, p, id, revision, false, "assignment decision")
		if err != nil {
			return err
		}
		if current.Assignee.ID != p.UserID {
			return ErrAssigneeRequired
		}
		if current.AssignmentStatus == "accepted" {
			return ErrConflict
		}
		if accept && !access.Can(auth.ProjectPermissionTasksExecute) {
			return auth.ErrProjectPermissionRequired
		}
		state := "declined"
		if accept {
			state = "accepted"
		}
		current.AssignmentStatus = state
		current, err = updateTaskCalendarInTx(
			ctx,
			q,
			current,
			&effects.pending,
			events.UserActor(p.UserID.String()),
		)
		if err != nil {
			return err
		}
		// Accepting future work does not start it. For immediate work, acceptance
		// is explicit execution consent and reuses the ordinary start action.
		if accept && current.ScheduledAt == nil && current.ExecutedBy.Kind == ActorAgent {
			started, err := s.tasks.startTaskExecutionInTx(
				ctx,
				q,
				p,
				taskExecutionRequest{taskID: id, revision: current.Revision},
			)
			started.pending = append(effects.pending, started.pending...)
			effects = started
			return err
		}
		effects.result.Task, err = taskProjectionForActiveMember(ctx, q, p, id)
		if err != nil {
			return err
		}
		return stageTaskEvent(
			ctx,
			q,
			&effects.pending,
			newTaskEvent(p.UserID, EventUpdated, effects.result.Task),
		)
	})
	if err != nil {
		return MutationResult{}, err
	}
	s.tasks.publishTaskWrite(ctx, effects)
	return effects.result, nil
}

func (s *DefaultCalendarService) GetSchedule(
	ctx context.Context,
	p auth.UserPrincipal,
	taskID uuid.UUID,
) (CalendarSchedule, error) {
	value, err := s.tasks.Get(ctx, p, taskID, nil)
	if err != nil {
		return CalendarSchedule{}, err
	}
	if value.ScheduleID == nil {
		return CalendarSchedule{}, ErrNotFound
	}
	row, err := s.tasks.store.Queries().
		GetTaskSchedule(ctx, generated.GetTaskScheduleParams{ID: *value.ScheduleID, CompanyID: p.CompanyID})
	if err != nil {
		return CalendarSchedule{}, err
	}
	return calendarSchedule(row)
}

func (s *DefaultCalendarService) UpdateSchedule(
	ctx context.Context,
	p auth.UserPrincipal,
	taskID uuid.UUID,
	change CalendarScheduleChange,
) (MutationResult, error) {
	var effects taskWriteEffects
	err := s.tasks.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		current, _, access, err := lockTaskMutationTarget(
			ctx,
			q,
			p,
			taskID,
			change.ExpectedRevision,
			true,
			"schedule update",
		)
		if err != nil {
			return err
		}
		if err := requireProjectPermission(access, auth.ProjectPermissionTasksWrite); err != nil {
			return err
		}
		before, err := activity.ObserveInTx(ctx, q, Topic, current.ID)
		if err != nil {
			return err
		}
		if current.ScheduleID == nil {
			if change.Trigger == nil || change.Paused {
				return ErrInvalidInput
			}
			if current.ExecutionGeneration > 0 {
				return ErrConflict
			}
			if current.Assignee.ID != p.UserID {
				current.AssignmentStatus = "pending"
			}
			current, err = createTaskCalendarInTx(ctx, q, current, *change.Trigger)
			if err != nil {
				return err
			}
		} else {
			row, err := q.LockTaskSchedule(
				ctx,
				generated.LockTaskScheduleParams{ID: *current.ScheduleID, CompanyID: p.CompanyID},
			)
			if err != nil {
				return err
			}
			if row.TemplateTaskID != current.ID {
				return ErrConflict
			}
			trigger, next := row.Trigger, row.NextOccurrenceAt
			if change.Trigger != nil || change.Paused {
				reason := execution.CancellationScheduleEdited
				if change.Paused && change.Trigger == nil {
					reason = execution.CancellationSchedulePaused
				}
				current, _, err = cancelPendingCalendarRunInTx(
					ctx,
					q,
					&effects.pending,
					current,
					reason,
				)
				if err != nil {
					return err
				}
				if err := cancelTaskCalendarChildrenInTx(
					ctx,
					q,
					&effects.pending,
					row,
					current.ID,
					events.UserActor(p.UserID.String()),
					reason,
				); err != nil {
					return err
				}
			}
			if change.Trigger != nil {
				preview, err := schedule.PreviewTrigger(*change.Trigger, time.Now().UTC(), 2)
				if err != nil || len(preview.Occurrences) == 0 {
					return ErrInvalidInput
				}
				trigger, err = json.Marshal(preview.Trigger)
				if err != nil {
					return err
				}
				row, err = q.AdvanceTaskScheduleGeneration(
					ctx,
					generated.AdvanceTaskScheduleGenerationParams{
						ID:        row.ID,
						CompanyID: row.CompanyID,
					},
				)
				if err != nil {
					return err
				}
				// Reuse an unstarted template for the first revised occurrence;
				// historical work stays intact once a Run has started.
				next = calendarPG(&preview.Occurrences[0])
				if current.ScheduleFiredAt == nil {
					current.ScheduleGeneration = row.Generation
					current.ScheduledAt, current.ScheduleOccurrenceAt = &preview.Occurrences[0], &preview.Occurrences[0]
					current.ScheduleDisposition = "scheduled"
					next = pgtype.Timestamptz{}
					if len(preview.Occurrences) > 1 {
						next = calendarPG(&preview.Occurrences[1])
					}
				}
			}
			if _, err := q.UpdateTaskSchedule(
				ctx,
				generated.UpdateTaskScheduleParams{
					ID:               row.ID,
					CompanyID:        p.CompanyID,
					Trigger:          trigger,
					NextOccurrenceAt: next,
					Paused:           change.Paused,
				},
			); err != nil {
				return err
			}
			if current.Assignee.ID != p.UserID && (!change.Paused || change.Trigger != nil) {
				current.AssignmentStatus = "pending"
			}
			current, err = setCalendarInTx(ctx, q, current)
			if err != nil {
				return err
			}
		}
		if err := activity.RecordInTx(
			ctx,
			q,
			&effects.pending,
			events.UserActor(p.UserID.String()),
			EventUpdated,
			before,
			Topic,
			current.ID,
		); err != nil {
			return err
		}
		effects.result.Task, err = taskProjectionForActiveMember(ctx, q, p, current.ID)
		if err != nil {
			return err
		}
		return stageTaskEvent(
			ctx,
			q,
			&effects.pending,
			newTaskEvent(p.UserID, EventUpdated, effects.result.Task),
		)
	})
	if err != nil {
		return MutationResult{}, err
	}
	s.tasks.publishTaskWrite(ctx, effects)
	return effects.result, nil
}

func (s *DefaultCalendarService) ListOccurrences(
	ctx context.Context,
	p auth.UserPrincipal,
	from, to time.Time,
) (CalendarPage, error) {
	result := CalendarPage{Occurrences: []CalendarOccurrence{}}
	if from.IsZero() || !to.After(from) || to.Sub(from) > 93*24*time.Hour {
		return result, ErrInvalidInput
	}
	err := s.tasks.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			var err error
			result, err = listCalendarOccurrences(ctx, q, p, from, to)
			return err
		},
	)
	return result, err
}

// The bounded range, series expansion and canonical projections share a read
// snapshot, so a concurrent reschedule cannot pair an old time with a new
// revision and accidentally authorize an overwrite from that Calendar card.
func listCalendarOccurrences(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	from, to time.Time,
) (CalendarPage, error) {
	result := CalendarPage{Occurrences: []CalendarOccurrence{}}
	rows, err := q.ListCalendarTaskRows(
		ctx,
		generated.ListCalendarTaskRowsParams{
			CompanyID:   p.CompanyID,
			ActorUserID: p.UserID,
			FromAt:      calendarPG(&from),
			ToAt:        calendarPG(&to),
		},
	)
	if err != nil {
		return result, err
	}
	for _, row := range rows {
		definition := CalendarSchedule{
			ID:             row.Task.TaskScheduleID.UUID,
			TemplateTaskID: row.TemplateTaskID,
			Paused:         row.Paused,
			Revision:       row.ScheduleVersion,
		}
		if err := json.Unmarshal(row.Trigger, &definition.Trigger); err != nil {
			return result, err
		}
		result.Occurrences = append(
			result.Occurrences,
			CalendarOccurrence{
				Task:         taskFromRow(row.Task),
				Schedule:     definition,
				OccurrenceAt: row.Task.ScheduleOccurrenceAt.Time,
				RunAt:        row.Task.ScheduledAt.Time,
				Materialized: true,
			},
		)
	}
	series, err := q.ListCalendarTaskSeries(
		ctx,
		generated.ListCalendarTaskSeriesParams{
			CompanyID:   p.CompanyID,
			ActorUserID: p.UserID,
			ToAt:        calendarPG(&to),
		},
	)
	if err != nil {
		return result, err
	}
	result.Truncated = len(series) > 200
	if len(series) > 200 {
		series = series[:200]
	}
	ids := make([]uuid.UUID, 0, len(series))
	for _, row := range series {
		ids = append(ids, row.TaskSchedule.ID)
	}
	materialized, err := q.ListMaterializedTaskOccurrences(
		ctx,
		generated.ListMaterializedTaskOccurrencesParams{
			CompanyID:   p.CompanyID,
			ScheduleIds: ids,
			FromAt:      calendarPG(&from),
			ToAt:        calendarPG(&to),
		},
	)
	if err != nil {
		return result, err
	}
	// If future exceptions alone exceed the page budget, omit virtual cards
	// rather than inventing a duplicate of an exception outside this window.
	exceptionsTruncated := len(materialized) > 2000
	result.Truncated = result.Truncated || exceptionsTruncated
	seen := make(map[string]bool, len(materialized))
	for _, row := range materialized {
		seen[occurrenceKey(row.TaskScheduleID.UUID, row.ScheduleOccurrenceAt.Time)] = true
	}
	for _, row := range series {
		if exceptionsTruncated || len(result.Occurrences) > 2000 {
			break
		}
		definition, err := calendarSchedule(row.TaskSchedule)
		if err != nil {
			return result, err
		}
		start := from
		if row.TaskSchedule.NextOccurrenceAt.Time.After(start) {
			start = row.TaskSchedule.NextOccurrenceAt.Time
		}
		times, err := schedule.StoredOccurrences(definition.Trigger, start, to, 2001-len(result.Occurrences))
		if err != nil {
			return result, err
		}
		for _, at := range times {
			if seen[occurrenceKey(row.TaskSchedule.ID, at)] {
				continue
			}
			value := taskFromRow(row.Task)
			value.Title, value.Description = row.TaskSchedule.TemplateTitle, row.TaskSchedule.TemplateDescription
			value.ScheduledAt, value.ScheduleOccurrenceAt, value.ScheduleFiredAt = &at, &at, nil
			value.Status, value.ScheduleDisposition = StatusTodo, "scheduled"
			result.Occurrences = append(
				result.Occurrences,
				CalendarOccurrence{Task: value, Schedule: definition, OccurrenceAt: at, RunAt: at},
			)
		}
	}
	result.Truncated = result.Truncated || len(result.Occurrences) > 2000
	if len(result.Occurrences) > 2000 {
		result.Occurrences = result.Occurrences[:2000]
	}
	ids = ids[:0]
	unique := make(map[uuid.UUID]bool)
	for _, item := range result.Occurrences {
		if !unique[item.Task.ID] {
			ids = append(ids, item.Task.ID)
			unique[item.Task.ID] = true
		}
	}
	if len(ids) > 0 {
		params := taskProjectionParams(p, ListOptions{}, nil)
		params.HasTaskIds, params.TaskIds = true, ids
		projections, err := q.ListTaskProjectionsForActiveMember(ctx, params)
		if err != nil {
			return result, err
		}
		byID := make(map[uuid.UUID]Response, len(projections))
		for _, row := range projections {
			byID[row.Task.ID] = responseFromProjectionRow(row)
		}
		visible := result.Occurrences[:0]
		for _, item := range result.Occurrences {
			value, ok := byID[item.Task.ID]
			if !ok {
				continue
			}
			if !item.Materialized {
				value.Title, value.Description = item.Task.Title, item.Task.Description
				value.ScheduledAt, value.ScheduleOccurrenceAt, value.ScheduleFiredAt = &item.RunAt, &item.OccurrenceAt, nil
				value.Status, value.ScheduleDisposition = StatusTodo, "scheduled"
			}
			item.Task = value
			visible = append(visible, item)
		}
		result.Occurrences = visible
	}
	return result, nil
}

func occurrenceKey(id uuid.UUID, at time.Time) string {
	return id.String() + ":" + at.UTC().Format(time.RFC3339Nano)
}

func (s *DefaultCalendarService) UpdateOccurrence(
	ctx context.Context,
	p auth.UserPrincipal,
	taskID uuid.UUID,
	change CalendarOccurrenceChange,
) (MutationResult, error) {
	var normalized UpdateInput
	var err error
	if change.Title != nil || change.Description != nil {
		normalized, err = normalizeUpdateInput(
			UpdateInput{
				ExpectedRevision: change.ExpectedRevision,
				Title:            change.Title,
				Description:      change.Description,
			},
		)
		if err != nil {
			return MutationResult{}, err
		}
	}
	if change.OccurrenceAt.IsZero() ||
		(!change.Cancel && (change.RunAt == nil || !change.RunAt.After(time.Now()) || change.RunAt.Second() != 0 || change.RunAt.Nanosecond() != 0)) {
		return MutationResult{}, ErrInvalidInput
	}
	var effects taskWriteEffects
	err = s.tasks.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		current, _, access, err := lockTaskMutationTarget(
			ctx,
			q,
			p,
			taskID,
			change.ExpectedRevision,
			true,
			"occurrence update",
		)
		if err != nil {
			return err
		}
		if err := requireProjectPermission(access, auth.ProjectPermissionTasksWrite); err != nil {
			return err
		}
		if current.ScheduleID == nil {
			return ErrNotFound
		}
		row, err := q.LockTaskSchedule(
			ctx,
			generated.LockTaskScheduleParams{ID: *current.ScheduleID, CompanyID: p.CompanyID},
		)
		if err != nil {
			return err
		}
		if current.ID != row.TemplateTaskID && current.ScheduleGeneration != row.Generation {
			return ErrConflict
		}
		occurrence, err := q.GetTaskOccurrence(
			ctx,
			generated.GetTaskOccurrenceParams{
				TaskScheduleID:         util.NullUUIDFromPtr(current.ScheduleID),
				TaskScheduleGeneration: row.Generation,
				ScheduleOccurrenceAt:   calendarPG(&change.OccurrenceAt),
			},
		)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			definition, err := calendarSchedule(row)
			if err != nil {
				return err
			}
			if !row.NextOccurrenceAt.Valid || change.OccurrenceAt.Before(row.NextOccurrenceAt.Time) {
				return ErrConflict
			}
			matches, err := schedule.StoredOccurrences(
				definition.Trigger,
				change.OccurrenceAt,
				change.OccurrenceAt.Add(time.Second),
				1,
			)
			if err != nil || len(matches) != 1 {
				return ErrInvalidInput
			}
			template, err := q.GetTaskForCompanyByIDForUpdate(
				ctx,
				generated.GetTaskForCompanyByIDForUpdateParams{
					CompanyID: p.CompanyID,
					TaskID:    row.TemplateTaskID,
				},
			)
			if err != nil {
				return err
			}
			current, err = s.materializeOccurrenceInTx(
				ctx,
				q,
				taskFromRow(template),
				row,
				change.OccurrenceAt,
				&effects.pending,
			)
			if err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if occurrence.ID != taskID {
				return ErrConflict
			}
			current = taskFromRow(occurrence)
		}
		if current.ScheduleFiredAt != nil || current.ScheduleDisposition != "scheduled" {
			return ErrConflict
		}
		if normalized.Title != nil || normalized.Description != nil {
			if normalized.Title != nil {
				current.Title = *normalized.Title
			}
			if normalized.Description != nil {
				current.Description = *normalized.Description
			}
			current, err = updateTask(
				ctx,
				q,
				current,
				current.Revision,
				&effects.pending,
				events.UserActor(p.UserID.String()),
				false,
				personWriteFence(p.UserID, auth.ProjectPermissionTasksWrite),
			)
			if err != nil {
				return err
			}
		}
		if change.Cancel {
			current.ScheduleDisposition = "cancelled"
		} else {
			current.ScheduledAt = change.RunAt
		}
		if !change.Cancel && current.Assignee.ID != p.UserID {
			current.AssignmentStatus = "pending"
		}
		current, err = updateTaskCalendarInTx(
			ctx,
			q,
			current,
			&effects.pending,
			events.UserActor(p.UserID.String()),
		)
		if err != nil {
			return err
		}
		effects.result.Task, err = taskProjectionForActiveMember(ctx, q, p, current.ID)
		if err != nil {
			return err
		}
		return stageTaskEvent(
			ctx,
			q,
			&effects.pending,
			newTaskEvent(p.UserID, EventUpdated, effects.result.Task),
		)
	})
	if err != nil {
		return MutationResult{}, err
	}
	s.tasks.publishTaskWrite(ctx, effects)
	return effects.result, nil
}

// Calendar background events carry the same names/readiness as all Task reads.
func stageCalendarTaskUpdate(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	row generated.Task,
) error {
	projection, err := q.GetTaskProjectionForSystem(ctx, systemProjectionParams(row.CompanyID, row.ID))
	if err != nil {
		return err
	}
	return stageTaskEvent(
		ctx,
		q,
		pending,
		newTaskEventForActor(events.SystemActor(), EventUpdated, responseFromSystemProjectionRow(projection)),
	)
}

// A revised definition gets a new occurrence namespace. Cancelled exceptions
// remain historical Tasks without suppressing the replacement definition's slots.
func reviseTaskCalendarInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	value Response,
	actorID uuid.UUID,
	assignmentChanged bool,
) error {
	if value.ScheduleID == nil {
		return nil
	}
	definition, err := q.LockTaskSchedule(
		ctx,
		generated.LockTaskScheduleParams{ID: *value.ScheduleID, CompanyID: value.CompanyID},
	)
	if err != nil {
		return err
	}
	if definition.TemplateTaskID == value.ID && !assignmentChanged && definition.TemplateTitle == value.Title &&
		definition.TemplateDescription == value.Description {
		return nil
	}
	value, cancelled, err := cancelPendingCalendarRunInTx(
		ctx,
		q,
		pending,
		value,
		execution.CancellationScheduleEdited,
	)
	if err != nil {
		return err
	}
	if definition.TemplateTaskID == value.ID {
		if err := q.UpdateTaskScheduleBrief(
			ctx,
			generated.UpdateTaskScheduleBriefParams{
				CompanyID:   value.CompanyID,
				TaskID:      value.ID,
				Title:       value.Title,
				Description: value.Description,
			},
		); err != nil {
			return err
		}
		if err := cancelTaskCalendarChildrenInTx(
			ctx,
			q,
			pending,
			definition,
			value.ID,
			events.SystemActor(),
			execution.CancellationScheduleEdited,
		); err != nil {
			return err
		}
		definition, err = q.AdvanceTaskScheduleGeneration(
			ctx,
			generated.AdvanceTaskScheduleGenerationParams{
				ID:        definition.ID,
				CompanyID: definition.CompanyID,
			},
		)
		if err != nil {
			return err
		}
		if value.ScheduleFiredAt == nil {
			value.ScheduleGeneration = definition.Generation
		}
	} else if !cancelled {
		return nil
	}
	if value.Assignee.ID != actorID {
		value.AssignmentStatus = "pending"
	}
	_, err = updateTaskCalendarInTx(ctx, q, value, pending, events.SystemActor())
	return err
}

// Enqueue is not execution: a first attempt still waiting for a daemon can be
// replaced. A claimed Run is preserved, together with its original occurrence.
func cancelPendingCalendarRunInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	value Response,
	reason string,
) (Response, bool, error) {
	if value.ScheduleFiredAt == nil {
		return value, false, nil
	}
	cancelled, err := execution.CancelPendingTaskScheduleRunsInTx(
		ctx,
		q,
		pending,
		execution.TaskScheduleCancellation{CompanyID: value.CompanyID, TaskID: value.ID, Reason: reason},
	)
	if err != nil {
		return value, false, err
	}
	if len(cancelled) == 0 {
		return value, false, nil
	}
	if reason == execution.CancellationScheduleEdited {
		value.ScheduleFiredAt = nil
		value.ScheduleDisposition = "scheduled"
	} else {
		value.ScheduleDisposition = "cancelled"
	}
	if value.Status == StatusInProgress {
		value.Status = StatusTodo
	}
	return value, true, nil
}

func cancelTaskCalendarChildrenInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	definition generated.TaskSchedule,
	templateID uuid.UUID,
	actor events.Actor,
	reason string,
) error {
	rows, err := q.ListUnstartedTaskOccurrences(ctx, generated.ListUnstartedTaskOccurrencesParams{
		ScheduleID: uuid.NullUUID{
			UUID:  definition.ID,
			Valid: true,
		},
		ExcludedTaskID:   templateID,
		Generation:       definition.Generation,
		IncludeScheduled: reason != execution.CancellationSchedulePaused,
	})
	if err != nil {
		return err
	}
	for _, row := range rows {
		value := taskFromRow(row)
		var cancelled bool
		value, cancelled, err = cancelPendingCalendarRunInTx(ctx, q, pending, value, reason)
		if err != nil {
			return err
		}
		if value.ScheduleFiredAt != nil && !cancelled {
			continue
		}
		// Pause keeps future exceptions in place; only already queued attempts
		// are cancelled. Definition edits invalidate every unstarted exception.
		if reason == execution.CancellationSchedulePaused && !cancelled {
			continue
		}
		value.ScheduleDisposition = "cancelled"
		if _, err := updateTaskCalendarInTx(ctx, q, value, pending, actor); err != nil {
			return err
		}
		if err := stageCalendarTaskUpdate(ctx, q, pending, row); err != nil {
			return err
		}
	}
	return nil
}

// DetachDeletedCalendarTemplatesInTx preserves Tasks retained by a Company Plan
// when their template is deleted. Unclaimed scheduled work is cancelled; claimed
// work and history survive without the definition. The caller holds the Company,
// Conversation and Task locks and records Task history around the whole Plan
// mutation, including the revisions written here, before publishing events.
func DetachDeletedCalendarTemplatesInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	companyID uuid.UUID,
	deletedTaskIDs []uuid.UUID,
) error {
	if len(deletedTaskIDs) == 0 {
		return nil
	}
	rows, err := q.ListRetainedTaskOccurrences(ctx, generated.ListRetainedTaskOccurrencesParams{
		CompanyID: companyID, DeletedTaskIds: deletedTaskIDs,
	})
	if err != nil {
		return fmt.Errorf("list retained Calendar occurrences: %w", err)
	}
	for _, row := range rows {
		value, _, err := cancelPendingCalendarRunInTx(
			ctx, q, pending, taskFromRow(row), execution.CancellationScheduleCancelled,
		)
		if err != nil {
			return err
		}
		value.ScheduleID = nil
		if value.ScheduleDisposition == "scheduled" {
			value.ScheduleDisposition = "cancelled"
		}
		// Company Plan owns the before/after snapshots so its metadata edits
		// and this detachment produce one final Task history revision.
		if _, err := setCalendarInTx(ctx, q, value); err != nil {
			return err
		}
		if err := stageCalendarTaskUpdate(ctx, q, pending, row); err != nil {
			return err
		}
	}
	return nil
}
