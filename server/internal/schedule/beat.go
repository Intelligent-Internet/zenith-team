package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

const (
	QueueName    = "schedules"
	ScanTaskType = "schedule:scan"
	FireTaskType = "schedule:fire"
)

// Enqueuer is the queue boundary used by the beat. PostgreSQL remains the
// authority; neither a task ID nor an Asynq Unique lock is an execution receipt.
type Enqueuer interface {
	EnqueueContext(context.Context, *asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error)
}

type Beat struct {
	ScanTasks      func(context.Context) error
	ExtraHandler   asynq.Handler
	Queries        *generated.Queries
	Worker         *FiringWorker
	Client         Enqueuer
	ObserveTask    func(string, string)
	ObserveEnqueue func(string, string)
}

type firePayload struct {
	ScheduleID uuid.UUID `json:"scheduleId"`
	CompanyID  uuid.UUID `json:"companyId"`
}

func ScanOptions() []asynq.Option {
	return []asynq.Option{
		asynq.Queue(QueueName),
		asynq.Unique(30 * time.Second),
		asynq.Timeout(15 * time.Second),
		asynq.MaxRetry(0),
	}
}

// RetryDelay keeps fire retries inside the repair sweep's grace period.
func RetryDelay(n int, _ error, _ *asynq.Task) time.Duration {
	switch n {
	case 0:
		return time.Second
	case 1:
		return 3 * time.Second
	default:
		return 8 * time.Second
	}
}

func (b *Beat) ProcessTask(ctx context.Context, task *asynq.Task) error {
	switch task.Type() {
	case ScanTaskType:
		err := b.scan(ctx)
		if err == nil && b.ScanTasks != nil {
			err = b.ScanTasks(ctx)
		}
		if err == nil {
			observeBeat(b.ObserveTask, "scan", "ok")
		}
		return err
	case FireTaskType:
		var payload firePayload
		if err := json.Unmarshal(
			task.Payload(),
			&payload,
		); err != nil || payload.ScheduleID == uuid.Nil ||
			payload.CompanyID == uuid.Nil {
			return fmt.Errorf("bad_payload: %w", asynq.SkipRetry)
		}
		// Company identity is checked against the authoritative row, never trusted
		// as authorization from Redis. No prompt or credentials enter this queue.
		target, err := b.Queries.GetScheduleLockTarget(ctx, payload.ScheduleID)
		if errors.Is(err, pgx.ErrNoRows) {
			observeBeat(b.ObserveTask, "fire", "noop")
			return nil
		}
		if err != nil {
			b.Worker.observeIssue(
				FiringIssue{
					Path:       "beat",
					ScheduleID: payload.ScheduleID,
					CompanyID:  payload.CompanyID,
					Code:       firingErrorCode(err),
					Err:        err,
				},
			)
			return errors.New("schedule lookup failed")
		}
		if target.CompanyID != payload.CompanyID {
			return fmt.Errorf("bad_payload: %w", asynq.SkipRetry)
		}
		fired, err := b.Worker.FireSchedule(ctx, payload.ScheduleID, "beat")
		if err != nil {
			// Asynq persists returned errors. Full diagnostics stay in server logs.
			return errors.New("schedule firing failed")
		}
		result := "noop"
		if fired {
			result = "ok"
		}
		observeBeat(b.ObserveTask, "fire", result)
		return nil
	default:
		if b.ExtraHandler != nil {
			return b.ExtraHandler.ProcessTask(ctx, task)
		}
		return fmt.Errorf("bad_payload: %w", asynq.SkipRetry)
	}
}

func (b *Beat) scan(ctx context.Context) error {
	cursor, cursorID := pgtype.Timestamptz{}, uuid.Nil
	for scanned := 0; scanned < 5000; {
		rows, err := b.Queries.ListDueScheduleLockTargets(ctx, generated.ListDueScheduleLockTargetsParams{
			PageSize: 500, AfterTime: cursor, AfterID: cursorID,
		})
		if err != nil {
			b.Worker.observeIssue(FiringIssue{Path: "beat", Code: "scan_failed", Err: err})
			return errors.New("schedule scan failed")
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			scanned++
			cursor, cursorID = row.NextOccurrenceAt, row.ScheduleID
			payload, err := json.Marshal(firePayload{ScheduleID: row.ScheduleID, CompanyID: row.CompanyID})
			if err != nil {
				return err
			}
			_, err = b.Client.EnqueueContext(
				ctx,
				asynq.NewTask(FireTaskType, payload),
				asynq.Queue(
					QueueName,
				),
				asynq.TaskID(
					fmt.Sprintf(
						"schedule:fire:%s:%d",
						row.ScheduleID,
						row.NextOccurrenceAt.Time.Unix(),
					),
				),
				asynq.MaxRetry(3),
				asynq.Timeout(30*time.Second),
			)
			result := "ok"
			if errors.Is(err, asynq.ErrTaskIDConflict) {
				result = "duplicate"
			} else if err != nil {
				result = "error"
			}
			observeBeat(b.ObserveEnqueue, "fire", result)
			if result == "error" {
				return errors.New("schedule enqueue failed")
			}
		}
	}
	return nil
}

func observeBeat(hook func(string, string), task, result string) {
	if hook == nil {
		return
	}
	defer func() { _ = recover() }()
	hook(task, result)
}
