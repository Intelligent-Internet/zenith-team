package task

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/schedule"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// ProcessDue is an Asynq wakeup, not a clock authority or execution receipt.
// Each bounded transaction rechecks the definition, assignee and Task. Repeated
// deliveries and concurrent API replicas serialize on the existing Company
// mutation fence, with a unique (schedule, generation, occurrence) database backstop.
func (s *DefaultCalendarService) ProcessDue(ctx context.Context, now time.Time, limit int) error {
	if limit < 1 || limit > 500 {
		return ErrInvalidInput
	}
	q := s.tasks.store.Queries()
	definitions, err := q.ListDueTaskSchedules(
		ctx,
		generated.ListDueTaskSchedulesParams{NowAt: calendarPG(&now), PageSize: int32(limit)},
	)
	if err != nil {
		return err
	}
	var failures []error
	for _, definition := range definitions {
		if err := s.materializeDue(ctx, definition, now); err != nil {
			failures = append(failures, err)
		}
	}
	rows, err := q.ListDueCalendarTasks(
		ctx,
		generated.ListDueCalendarTasksParams{NowAt: calendarPG(&now), PageSize: int32(limit)},
	)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := s.fireDue(ctx, row, now); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *DefaultCalendarService) materializeDue(
	ctx context.Context,
	target generated.TaskSchedule,
	now time.Time,
) error {
	var effects taskWriteEffects
	err := s.tasks.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := q.SetScheduleTransactionTimeouts(ctx, "200ms"); err != nil {
			return err
		}
		if _, err := q.LockCompanyForMembershipMutation(ctx, target.CompanyID); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		if err := q.SetScheduleTransactionTimeouts(ctx, "5s"); err != nil {
			return err
		}
		if err := q.LockTaskDependencyGraph(ctx, target.CompanyID); err != nil {
			return err
		}
		project, err := q.GetTaskProjectForShare(
			ctx,
			generated.GetTaskProjectForShareParams{
				CompanyID: target.CompanyID,
				TaskID:    target.TemplateTaskID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if project.ArchivedAt.Valid {
			return nil
		}
		template, err := q.GetTaskForCompanyByIDForUpdate(
			ctx,
			generated.GetTaskForCompanyByIDForUpdateParams{
				CompanyID: target.CompanyID,
				TaskID:    target.TemplateTaskID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if template.AssignmentStatus != "accepted" {
			return nil
		}
		if err := checkAssigneeReadsProject(
			ctx,
			q,
			template.CompanyID,
			template.ProjectID,
			template.AssigneeUserID,
		); errors.Is(
			err,
			ErrAssigneeOutsideProject,
		) {
			return nil
		} else if err != nil {
			return err
		}
		// The Company fence also serializes membership, handoff and Project
		// archival. Materialization itself grants no execution authority.
		definition, err := q.LockTaskSchedule(
			ctx,
			generated.LockTaskScheduleParams{ID: target.ID, CompanyID: target.CompanyID},
		)
		if err != nil {
			return err
		}
		// Production deliveries read time after acquiring the mutation locks.
		// ProcessDue supplies a deterministic time for reconciliation tests.
		if now.IsZero() {
			clock, err := q.GetScheduleClock(ctx)
			if err != nil {
				return err
			}
			now = clock.Time
		}
		if definition.Paused || !definition.NextOccurrenceAt.Valid ||
			definition.NextOccurrenceAt.Time.After(now) {
			return nil
		}
		value, err := calendarSchedule(definition)
		if err != nil {
			return err
		}
		at, next, _, err := schedule.AdvanceStoredTrigger(value.Trigger, definition.NextOccurrenceAt.Time, now)
		if err != nil {
			return err
		}
		if _, err := q.GetTaskOccurrence(
			ctx,
			generated.GetTaskOccurrenceParams{
				TaskScheduleID:         uuid.NullUUID{UUID: definition.ID, Valid: true},
				TaskScheduleGeneration: definition.Generation,
				ScheduleOccurrenceAt:   calendarPG(&at),
			},
		); errors.Is(
			err,
			pgx.ErrNoRows,
		) {
			task, err := s.materializeOccurrenceInTx(
				ctx,
				q,
				taskFromRow(template),
				definition,
				at,
				&effects.pending,
			)
			if err != nil {
				return err
			}
			effects.result.Task = task
		} else if err != nil {
			return err
		}
		nextAt := calendarPG(&next)
		if value.Trigger.Type == schedule.TriggerTypeOnce {
			nextAt = calendarPG(nil)
		}
		_, err = q.UpdateTaskSchedule(
			ctx,
			generated.UpdateTaskScheduleParams{
				ID:               definition.ID,
				CompanyID:        definition.CompanyID,
				Trigger:          definition.Trigger,
				NextOccurrenceAt: nextAt,
				Paused:           false,
			},
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("materialize Task occurrence: %w", err)
	}
	s.tasks.publishTaskWrite(ctx, effects)
	return nil
}

func (s *DefaultCalendarService) materializeOccurrenceInTx(
	ctx context.Context,
	q *generated.Queries,
	template Response,
	definition generated.TaskSchedule,
	at time.Time,
	pending *[]events.Event,
) (Response, error) {
	key, err := allocateTaskKey(ctx, q, template.CompanyID, "")
	if err != nil {
		return Response{}, err
	}
	sets := auth.ProjectPermissionHeldSets()
	// An accepted series is a durable product definition, so materializing its
	// next Task is a system write. Starting its Run still checks the assignee's
	// current membership, Project permission and private binding.
	inserted, err := q.InsertTaskWithConversation(ctx, generated.InsertTaskWithConversationParams{
		CompanyID:                 template.CompanyID,
		ProjectID:                 template.ProjectID,
		Key:                       key.key,
		Number:                    key.number,
		Title:                     definition.TemplateTitle,
		Description:               definition.TemplateDescription,
		MilestoneType:             string(template.MilestoneType),
		Status:                    string(StatusTodo),
		Priority:                  string(template.Priority),
		Labels:                    template.Labels,
		AssigneeUserID:            template.Assignee.ID,
		ExecutedByAgentID:         util.NullUUIDFromPtr(executedByAgentID(template.ExecutedBy)),
		CreatorUserID:             uuid.NullUUID{UUID: template.Assignee.ID, Valid: true},
		CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
		ManagementPermissions:     sets.ManagementPermissions,
		ManagementRoles:           sets.ManagementRoles,
	})
	if err != nil {
		return Response{}, err
	}
	value := taskFromRow(generated.Task(inserted))
	value.ScheduleID = template.ScheduleID
	value.ScheduleGeneration = definition.Generation
	value.ScheduledAt, value.ScheduleOccurrenceAt = &at, &at
	value.ScheduleDisposition, value.AssignmentStatus = "scheduled", template.AssignmentStatus
	value, err = setCalendarInTx(ctx, q, value)
	if err != nil {
		return Response{}, err
	}
	if err := q.CopyTaskOccurrenceResultPolicy(
		ctx,
		generated.CopyTaskOccurrenceResultPolicyParams{TaskID: value.ID, TemplateTaskID: template.ID},
	); err != nil {
		return Response{}, err
	}
	if err := q.CopyTaskOccurrenceDependencies(
		ctx,
		generated.CopyTaskOccurrenceDependenciesParams{TaskID: value.ID, TemplateTaskID: template.ID},
	); err != nil {
		return Response{}, err
	}
	if err := activity.RecordInTx(
		ctx,
		q,
		pending,
		events.SystemActor(),
		EventCreated,
		nil,
		Topic,
		value.ID,
	); err != nil {
		return Response{}, err
	}
	if err := activity.RecordInTx(
		ctx,
		q,
		pending,
		events.SystemActor(),
		conversation.EventConversationCreated,
		nil,
		conversation.Topic,
		value.ConversationID,
	); err != nil {
		return Response{}, err
	}
	chat := generated.Conversation{
		ID:        value.ConversationID,
		CompanyID: value.CompanyID,
		Kind:      "task",
		TaskID:    uuid.NullUUID{UUID: value.ID, Valid: true},
		Title:     util.StrToText(value.Title),
		CreatedAt: inserted.CreatedAt,
		UpdatedAt: inserted.UpdatedAt,
		Version:   1,
	}
	event := newTaskConversationCreatedEvent(value.Assignee.ID, value.ID, chat)
	event.Actor = events.SystemActor()
	if err := stageTaskEvent(ctx, q, pending, event); err != nil {
		return Response{}, err
	}
	projection, err := q.GetTaskProjectionForSystem(ctx, systemProjectionParams(value.CompanyID, value.ID))
	if err != nil {
		return Response{}, err
	}
	value = responseFromSystemProjectionRow(projection)
	if err := stageTaskEvent(
		ctx,
		q,
		pending,
		newTaskEventForActor(events.SystemActor(), EventCreated, value),
	); err != nil {
		return Response{}, err
	}
	return value, nil
}

func (s *DefaultCalendarService) fireDue(ctx context.Context, target generated.Task, now time.Time) error {
	var effects taskWriteEffects
	err := s.tasks.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := q.SetScheduleTransactionTimeouts(ctx, "200ms"); err != nil {
			return err
		}
		// Identity comes from the accepted persisted assignment, never a Redis
		// payload. The ordinary start path rechecks this member under locks.
		principal := auth.UserPrincipal{CompanyID: target.CompanyID, UserID: target.AssigneeUserID}
		current, _, _, err := lockTaskMutationTarget(
			ctx,
			q,
			principal,
			target.ID,
			target.Version,
			true,
			"scheduled execution",
		)
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := q.SetScheduleTransactionTimeouts(ctx, "5s"); err != nil {
			return err
		}
		if now.IsZero() {
			clock, err := q.GetScheduleClock(ctx)
			if err != nil {
				return err
			}
			now = clock.Time
		}
		if current.ScheduleID == nil || current.ScheduleDisposition != "scheduled" ||
			current.AssignmentStatus != "accepted" ||
			current.ScheduledAt.After(now) {
			return nil
		}
		definition, err := q.LockTaskSchedule(
			ctx,
			generated.LockTaskScheduleParams{ID: *current.ScheduleID, CompanyID: current.CompanyID},
		)
		if err != nil {
			return err
		}
		if definition.Paused || current.ScheduleGeneration != definition.Generation {
			return nil
		}
		template, err := q.GetTaskForCompanyByIDForUpdate(
			ctx,
			generated.GetTaskForCompanyByIDForUpdateParams{
				CompanyID: current.CompanyID,
				TaskID:    definition.TemplateTaskID,
			},
		)
		if err != nil {
			return err
		}
		if template.AssignmentStatus != "accepted" {
			return nil
		}
		state, err := q.GetTaskScheduleRunState(ctx, generated.GetTaskScheduleRunStateParams{
			ScheduleID:  util.NullUUIDFromPtr(current.ScheduleID),
			CompanyID:   current.CompanyID,
			Generation:  definition.Generation,
			ScheduledAt: calendarPG(current.ScheduledAt),
			NowAt:       calendarPG(&now),
		})
		if err != nil {
			return err
		}
		disposition := "started"
		switch {
		case state.ActiveRun || state.NewerDueOccurrence ||
			(definition.NextOccurrenceAt.Valid && definition.NextOccurrenceAt.Time.After(*current.ScheduleOccurrenceAt) &&
				definition.NextOccurrenceAt.Time.After(*current.ScheduledAt) &&
				!definition.NextOccurrenceAt.Time.After(now)):
			disposition = "skipped"
		case !runnableTaskStatus(current.Status):
			disposition = "cancelled"
		}
		if disposition == "started" {
			projection, err := taskProjectionForActiveMember(ctx, q, principal, current.ID)
			if err != nil {
				return err
			}
			if !projection.Execution.AssigneeReady || !projection.DependenciesResolved {
				return nil
			}
		}
		current.ScheduleDisposition = disposition
		if disposition == "started" {
			current.ScheduleFiredAt = &now
		}
		current, err = updateTaskCalendarInTx(ctx, q, current, &effects.pending, events.SystemActor())
		if err != nil {
			return err
		}
		if disposition == "started" {
			started, err := s.tasks.startTaskExecutionInTx(
				ctx,
				q,
				principal,
				taskExecutionRequest{taskID: current.ID, revision: current.Revision},
			)
			if err != nil {
				return err
			}
			started.pending = append(effects.pending, started.pending...)
			effects = started
			return execution.MarkTaskRunScheduledInTx(
				ctx,
				q,
				*effects.enqueued,
			)
		}
		effects.result.Task, err = taskProjectionForActiveMember(ctx, q, principal, current.ID)
		if err != nil {
			return err
		}
		return stageTaskEvent(
			ctx,
			q,
			&effects.pending,
			newTaskEventForActor(events.SystemActor(), EventUpdated, effects.result.Task),
		)
	})
	// Due work waits for its prerequisites and the assignee's computer.
	// A later recurrence supersedes unstarted older work instead of replaying a backlog.
	if errors.Is(err, ErrDependencyBlocked) || errors.Is(err, ErrProjectArchived) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fire Task occurrence: %w", err)
	}
	s.tasks.publishTaskWrite(ctx, effects)
	return nil
}
