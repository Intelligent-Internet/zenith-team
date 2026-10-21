package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/schedule"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// CalendarBeat distributes due definitions and occurrences across the existing
// Asynq workers. Queue payloads contain identities only; PostgreSQL owns their
// consent, time, version and receipt. There is no queue record per future date.
type CalendarBeat struct {
	Service *DefaultCalendarService
	Client  schedule.Enqueuer
	OnError func(context.Context, error)
}

type calendarBeatPayload struct {
	ID        uuid.UUID `json:"id"`
	CompanyID uuid.UUID `json:"companyId"`
}

func (b *CalendarBeat) Scan(ctx context.Context) error {
	q := b.Service.tasks.store.Queries()
	clock, err := q.GetScheduleClock(ctx)
	if err != nil {
		return b.failure(ctx, err)
	}
	now := clock.Time
	definitionsPage := generated.ListDueTaskSchedulesParams{NowAt: calendarPG(&now), PageSize: 500}
	for {
		definitions, err := q.ListDueTaskSchedules(ctx, definitionsPage)
		if err != nil {
			return b.failure(ctx, err)
		}
		for _, row := range definitions {
			if err := b.enqueue(
				ctx,
				"task-calendar:materialize",
				row.ID,
				row.CompanyID,
			); err != nil {
				return b.failure(ctx, err)
			}
			definitionsPage.HasAfter, definitionsPage.AfterAt, definitionsPage.AfterID = true, row.NextOccurrenceAt, row.ID
		}
		if len(definitions) < 500 {
			break
		}
	}
	tasksPage := generated.ListDueCalendarTasksParams{NowAt: calendarPG(&now), PageSize: 500}
	for {
		rows, err := q.ListDueCalendarTasks(ctx, tasksPage)
		if err != nil {
			return b.failure(ctx, err)
		}
		for _, row := range rows {
			if err := b.enqueue(ctx, "task-calendar:fire", row.ID, row.CompanyID); err != nil {
				return b.failure(ctx, err)
			}
			tasksPage.HasAfter, tasksPage.AfterAt, tasksPage.AfterID = true, row.ScheduledAt, row.ID
		}
		if len(rows) < 500 {
			break
		}
	}
	return nil
}

func (b *CalendarBeat) enqueue(ctx context.Context, name string, id, companyID uuid.UUID) error {
	payload, err := json.Marshal(calendarBeatPayload{ID: id, CompanyID: companyID})
	if err != nil {
		return err
	}
	_, err = b.Client.EnqueueContext(
		ctx,
		asynq.NewTask(name, payload),
		asynq.Queue(schedule.QueueName),
		// Bound queue deduplication so an archived job cannot permanently
		// suppress the next wakeup. Database receipts prevent duplicate Runs.
		asynq.Unique(30*time.Second),
		asynq.MaxRetry(3),
		asynq.Timeout(30*time.Second),
	)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func (b *CalendarBeat) ProcessTask(ctx context.Context, job *asynq.Task) error {
	if job.Type() != "task-calendar:materialize" && job.Type() != "task-calendar:fire" {
		return fmt.Errorf("bad_payload: %w", asynq.SkipRetry)
	}
	var payload calendarBeatPayload
	if err := json.Unmarshal(
		job.Payload(),
		&payload,
	); err != nil || payload.ID == uuid.Nil ||
		payload.CompanyID == uuid.Nil {
		return fmt.Errorf("bad_payload: %w", asynq.SkipRetry)
	}
	q := b.Service.tasks.store.Queries()
	var err error
	switch job.Type() {
	case "task-calendar:materialize":
		var row generated.TaskSchedule
		row, err = q.GetTaskSchedule(
			ctx,
			generated.GetTaskScheduleParams{ID: payload.ID, CompanyID: payload.CompanyID},
		)
		if err == nil {
			err = b.Service.materializeDue(ctx, row, time.Time{})
		}
	case "task-calendar:fire":
		var row generated.Task
		row, err = q.GetCalendarTaskTarget(
			ctx,
			generated.GetCalendarTaskTargetParams{ID: payload.ID, CompanyID: payload.CompanyID},
		)
		if err == nil {
			err = b.Service.fireDue(ctx, row, time.Time{})
		}
	default:
		return fmt.Errorf("bad_payload: %w", asynq.SkipRetry)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return b.failure(ctx, err)
	}
	return nil
}

func (b *CalendarBeat) failure(ctx context.Context, err error) error {
	if b.OnError != nil {
		b.OnError(ctx, err)
	}
	// Asynq retains errors in Redis; keep database and product details in the
	// server's diagnostics only, following the existing Schedule worker.
	return errors.New("task calendar job failed")
}
