package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"boardly.dev/boardly/server/internal/schedule"
	"boardly.dev/boardly/server/internal/telemetry"
)

type scheduleQueue struct {
	redis          *redis.Client
	server         *asynq.Server
	scheduler      *asynq.Scheduler
	inspector      *asynq.Inspector
	handler        asynq.Handler
	registry       *telemetry.Registry
	logger         *scheduleQueueLogger
	cancelHandlers context.CancelFunc
	probeSlot      chan struct{}
}

func newScheduleQueue(
	client *redis.Client,
	beat *schedule.Beat,
	registry *telemetry.Registry,
	logger *slog.Logger,
	concurrency int,
) (*scheduleQueue, error) {
	if client == nil {
		return nil, errors.New("Schedule queue requires Redis")
	}
	if logger == nil {
		logger = slog.Default()
	}
	queueLog := &scheduleQueueLogger{logger: logger.With("component", "asynq")}
	base, cancel := context.WithCancel(context.Background())
	queue := &scheduleQueue{
		redis:          client,
		handler:        beat,
		registry:       registry,
		logger:         queueLog,
		cancelHandlers: cancel,
		probeSlot:      make(chan struct{}, 1),
	}
	queue.inspector = asynq.NewInspectorFromRedisClient(client)
	queue.server = asynq.NewServerFromRedisClient(client, asynq.Config{
		Concurrency: concurrency, Queues: map[string]int{schedule.QueueName: 1},
		BaseContext: func() context.Context { return base }, ShutdownTimeout: 5 * time.Second,
		DelayedTaskCheckInterval: time.Second, RetryDelayFunc: schedule.RetryDelay,
		Logger: queueLog, LogLevel: asynq.WarnLevel,
		ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, err error) {
			defer func() { _ = recover() }()
			name := "fire"
			if task.Type() == schedule.ScanTaskType {
				name = "scan"
			}
			retry, _ := asynq.GetRetryCount(ctx)
			maximum, _ := asynq.GetMaxRetry(ctx)
			result := "retry"
			if retry >= maximum {
				result = "failed"
			}
			if errors.Is(err, asynq.SkipRetry) {
				result = "bad_payload"
			}
			registry.ObserveScheduleTask(name, result)
		}),
	})
	queue.scheduler = asynq.NewSchedulerFromRedisClient(client, &asynq.SchedulerOpts{
		Logger: queueLog, LogLevel: asynq.WarnLevel,
		PostEnqueueFunc: func(_ *asynq.TaskInfo, err error) {
			defer func() { _ = recover() }()
			result := "ok"
			if errors.Is(err, asynq.ErrDuplicateTask) {
				result = "duplicate"
			} else if err != nil {
				result = "error"
			}
			registry.ObserveScheduleEnqueue("scan", result)
		},
	})
	if _, err := queue.scheduler.Register(
		"@every 10s",
		asynq.NewTask(schedule.ScanTaskType, nil),
		schedule.ScanOptions()...); err != nil {
		cancel()
		return nil, err
	}
	return queue, nil
}

func (q *scheduleQueue) Run(ctx context.Context) error {
	defer q.cancelHandlers()
	for delay := time.Second; ; delay = min(30*time.Second, delay*2) {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := q.redis.Ping(pingCtx).Err()
		cancel()
		if err == nil {
			break
		}
		q.logger.Warn("Schedule queue waiting for Redis")
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err := q.server.Start(q.handler); err != nil {
		return err
	}
	if err := q.scheduler.Start(); err != nil {
		q.server.Shutdown()
		return err
	}
	<-ctx.Done()
	// HTTP has already drained. Stop both components concurrently so a blocked
	// Scheduler cleanup cannot consume the Server's five-second handler drain.
	done := make(chan struct{})
	go func() {
		var group sync.WaitGroup
		group.Add(2)
		go func() { defer group.Done(); q.scheduler.Shutdown() }()
		go func() { defer group.Done(); q.server.Shutdown() }()
		group.Wait()
		close(done)
	}()
	cancelTimer := time.AfterFunc(5*time.Second, q.cancelHandlers)
	defer cancelTimer.Stop()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		q.logger.Warn("Schedule queue shutdown reached its deadline")
	}
	return ctx.Err()
}

func (q *scheduleQueue) probe(ctx context.Context) {
	// An Inspector has no context API. Keep at most one read in flight even
	// against a blackholed Redis, and never let it hold up PostgreSQL probing.
	select {
	case q.probeSlot <- struct{}{}:
	default:
		q.registry.ObserveScheduleQueue(0, 0, 0, 0, false)
		return
	}
	done := make(chan error, 1)
	go func() { defer func() { <-q.probeSlot }(); done <- q.probeQueue(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			q.registry.ObserveScheduleQueue(0, 0, 0, 0, false)
			q.logger.Warn("Schedule queue probe failed")
		}
	case <-ctx.Done():
		q.registry.ObserveScheduleQueue(0, 0, 0, 0, false)
	}
}

func (q *scheduleQueue) probeQueue(ctx context.Context) error {
	queues, err := q.inspector.Queues()
	if err != nil {
		return err
	}
	if !slices.Contains(queues, schedule.QueueName) {
		// Asynq caches queue registration per producer. A fresh producer repairs
		// discovery after FLUSHALL/failover without writing private Redis keys.
		fresh := asynq.NewClientFromRedisClient(q.redis)
		_, err = fresh.EnqueueContext(ctx, asynq.NewTask(schedule.ScanTaskType, nil), schedule.ScanOptions()...)
		if err != nil && !errors.Is(err, asynq.ErrDuplicateTask) {
			return err
		}
	}
	info, err := q.inspector.GetQueueInfo(schedule.QueueName)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	q.registry.ObserveScheduleQueue(info.Pending, info.Retry, info.Archived, info.Latency, true)
	return nil
}

// The queue library's Fatal must never terminate the API process. Repeated
// outage diagnostics are bounded; ordinary scan Unique collisions are debug.
type scheduleQueueLogger struct {
	logger *slog.Logger
	mu     sync.Mutex
	last   time.Time
}

func (l *scheduleQueueLogger) Debug(args ...any) { l.logger.Debug(fmt.Sprint(args...)) }
func (l *scheduleQueueLogger) Info(args ...any)  { l.logger.Debug(fmt.Sprint(args...)) }
func (l *scheduleQueueLogger) Warn(args ...any)  { l.write(slog.LevelWarn, args...) }
func (l *scheduleQueueLogger) Error(args ...any) { l.write(slog.LevelError, args...) }
func (l *scheduleQueueLogger) Fatal(args ...any) { l.write(slog.LevelError, args...) }
func (l *scheduleQueueLogger) write(level slog.Level, args ...any) {
	text := fmt.Sprint(args...)
	if strings.Contains(text, "connection is shared") {
		return
	}
	if strings.Contains(text, "duplicate task") {
		l.logger.Debug("Schedule scan already queued")
		return
	}
	l.mu.Lock()
	allowed := time.Since(l.last) >= 30*time.Second
	if allowed {
		l.last = time.Now()
	}
	l.mu.Unlock()
	if allowed {
		l.logger.Log(context.Background(), level, text)
	}
}
