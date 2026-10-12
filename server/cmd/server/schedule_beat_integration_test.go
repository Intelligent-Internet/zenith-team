//go:build integration

package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"boardly.dev/boardly/server/internal/schedule"
	"boardly.dev/boardly/server/internal/telemetry"
)

func TestScheduleAsynqRealRedisCompatibility(t *testing.T) {
	ctx := t.Context()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "redis:7.4-alpine",
			ExposedPorts: []string{"6379/tcp"},
			Cmd: []string{
				"redis-server",
				"--appendonly",
				"yes",
				"--maxmemory-policy",
				"noeviction",
			},
			WaitingFor: wait.ForListeningPort("6379/tcp"),
		},
		Started: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	address, err := container.Endpoint(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Addr: address, ContextTimeoutEnabled: true, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	producer := asynq.NewClientFromRedisClient(client)
	inspector := asynq.NewInspectorFromRedisClient(client)
	var attempts atomic.Int32
	completed := make(chan struct{}, 1)
	server := asynq.NewServerFromRedisClient(
		client,
		asynq.Config{
			Concurrency:              2,
			Queues:                   map[string]int{schedule.QueueName: 1},
			DelayedTaskCheckInterval: 100 * time.Millisecond,
			TaskCheckInterval:        100 * time.Millisecond,
			RetryDelayFunc:           schedule.RetryDelay,
			ShutdownTimeout:          time.Second,
		},
	)
	if err := server.Start(asynq.HandlerFunc(func(context.Context, *asynq.Task) error {
		if attempts.Add(1) < 2 {
			return errors.New("transient test failure")
		}
		completed <- struct{}{}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	defer server.Shutdown()
	if _, err := producer.EnqueueContext(
		ctx,
		asynq.NewTask(schedule.FireTaskType, []byte(`{"scheduleId":"test","companyId":"test"}`)),
		asynq.Queue(schedule.QueueName),
		asynq.MaxRetry(3),
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
	case <-time.After(10 * time.Second):
		t.Fatal("real Redis task retry did not complete")
	}
	server.Stop()
	// Flush only this disposable test Redis. Existing producer caches must not
	// make the inspector report an empty queue after new tasks have been added.
	if err := client.FlushAll(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	_, err = producer.EnqueueContext(ctx, asynq.NewTask(schedule.ScanTaskType, nil), schedule.ScanOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	queue := &scheduleQueue{redis: client, inspector: inspector, registry: telemetry.NewRegistry()}
	if err := queue.probeQueue(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := inspector.GetQueueInfo(schedule.QueueName)
	if err != nil || info.Pending != 1 {
		t.Fatalf("recovered queue = %+v, %v", info, err)
	}
	server.Shutdown()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal("Asynq closed the shared Redis client", err)
	}
}

func TestScheduleQueueShutdownWithRedisUnavailable(t *testing.T) {
	ctx := t.Context()
	container, err := testcontainers.GenericContainer(
		ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        "redis:7.4-alpine",
				ExposedPorts: []string{"6379/tcp"},
				WaitingFor:   wait.ForListeningPort("6379/tcp"),
			},
			Started: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	address, err := container.Endpoint(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Addr: address, MaxRetries: -1, ContextTimeoutEnabled: true})
	t.Cleanup(func() { _ = client.Close() })
	queue, err := newScheduleQueue(client, &schedule.Beat{}, telemetry.NewRegistry(), nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	next := queue.handler
	queue.handler = asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
		select {
		case started <- struct{}{}:
		default:
		}
		return next.ProcessTask(ctx, task)
	})
	producer := asynq.NewClientFromRedisClient(client)
	if _, err := producer.EnqueueContext(
		ctx,
		asynq.NewTask("schedule:fire2", nil),
		asynq.Queue(schedule.QueueName),
	); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- queue.Run(runCtx) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("queue did not start")
	}
	if err := container.Stop(ctx, nil); err != nil {
		t.Fatal(err)
	}
	shutdownAt := time.Now()
	cancel()
	select {
	case <-done:
		t.Logf("shutdown with Redis stopped: %s", time.Since(shutdownAt))
	case <-time.After(9 * time.Second):
		t.Fatal("queue exceeded 8-second shutdown cap")
	}
}
