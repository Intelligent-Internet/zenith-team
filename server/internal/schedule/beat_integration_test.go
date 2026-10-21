//go:build integration

package schedule

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestScheduleAsynqTwoReplicasAndRedisLoss(t *testing.T) {
	ctx := t.Context()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "redis:7.4-alpine",
			ExposedPorts: []string{"6379/tcp"},
			Cmd: []string{
				"redis-server",
				"--appendonly",
				"yes",
				"--appendfsync",
				"everysec",
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
	client := redis.NewClient(&redis.Options{Addr: address, MaxRetries: -1, ContextTimeoutEnabled: true})
	t.Cleanup(func() { _ = client.Close() })
	pool, store, f := startSchedulePostgres(t)
	svc := newScheduleTestService(t, store)
	principal := companyPrincipal(f.UserA, f.CompanyA)
	base := createDueSchedule(t, ctx, pool, svc, principal, f.AgentA)
	count := 2
	if os.Getenv("ZENITH_TEAM_SCHEDULE_LOAD_TEST") == "1" {
		count = 1000
	}
	seedScheduleBurst(t, ctx, pool, base.Schedule, count)
	committed := make(chan struct{}, count+10)
	started := time.Now()
	for range 2 {
		worker := reliabilityWorker(t, store, MaterializerFunc(materializedScheduleRun))
		worker.options.OnFired = func(_, outcome, _ string, _ time.Duration) {
			if outcome == "materialized" {
				committed <- struct{}{}
			}
		}
		beat := &Beat{Queries: store.Queries(), Worker: worker, Client: asynq.NewClientFromRedisClient(client)}
		server := asynq.NewServerFromRedisClient(
			client,
			asynq.Config{
				Concurrency:              8,
				Queues:                   map[string]int{QueueName: 1},
				ShutdownTimeout:          time.Second,
				DelayedTaskCheckInterval: time.Second,
				RetryDelayFunc:           RetryDelay,
				LogLevel:                 asynq.ErrorLevel,
			},
		)
		if err := server.Start(beat); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(server.Shutdown)
		timer := asynq.NewSchedulerFromRedisClient(client, &asynq.SchedulerOpts{LogLevel: asynq.ErrorLevel})
		if _, err := timer.Register(
			"@every 10s",
			asynq.NewTask(ScanTaskType, nil),
			ScanOptions()...); err != nil {
			t.Fatal(err)
		}
		if err := timer.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(timer.Shutdown)
	}
	deadline := time.NewTimer(time.Minute)
	defer deadline.Stop()
	for range count {
		select {
		case <-committed:
		case <-deadline.C:
			t.Fatal("beat burst exceeded 60 seconds")
		}
	}
	var firings, unique int
	if err := pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT schedule_id) FROM schedule_firings WHERE trigger='scheduled'`).
		Scan(&firings, &unique); err != nil {
		t.Fatal(err)
	}
	if firings != count || unique != count {
		t.Fatalf("%d firings/%d schedules, want %d", firings, unique, count)
	}
	t.Logf("Asynq two replicas x eight workers: %d firings in %s including timer wait", count, time.Since(started))
	// Remove the broker's entire state. Both existing timers/producers keep
	// running; PostgreSQL prevents completed work from being repeated.
	if err := client.FlushAll(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	afterFlush := createDueSchedule(t, ctx, pool, svc, principal, f.AgentA)
	select {
	case <-committed:
	case <-time.After(15 * time.Second):
		t.Fatal("beat did not recover after Redis FLUSHALL")
	}
	if countScheduleFirings(t, ctx, pool, afterFlush.Schedule.ID) != 1 {
		t.Fatal("flush lost or duplicated a firing")
	}
	// The sweep must have no Redis dependency, including when Redis is stopped.
	if err := container.Stop(ctx, nil); err != nil {
		t.Fatal(err)
	}
	offline := createDueSchedule(t, ctx, pool, svc, principal, f.AgentA)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE schedules SET next_occurrence_at=now()-interval '1 minute' WHERE id=$1`,
		offline.Schedule.ID,
	)
	worker := reliabilityWorker(t, store, MaterializerFunc(materializedScheduleRun))
	if _, err := worker.SweepOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if countScheduleFirings(t, ctx, pool, offline.Schedule.ID) != 1 {
		t.Fatal("Redis outage stopped repair")
	}
	if countScheduleFirings(t, ctx, pool, base.Schedule.ID) != 1 {
		t.Fatal("repair duplicated committed firing")
	}
}
