//go:build integration

package task

import (
	"context"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"boardly.dev/boardly/server/internal/schedule"
)

func TestCalendarAsynqReplicasAndBrokerLoss(t *testing.T) {
	ctx, pool, f, tasks, principal := startTaskHarness(t)
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
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	producer := asynq.NewClientFromRedisClient(client)
	beat := &CalendarBeat{Service: NewCalendarService(tasks), Client: producer}
	at := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
	created, err := tasks.Create(
		ctx,
		principal,
		CreateInput{
			ProjectID:     f.ProjectA,
			MilestoneType: MilestoneTypeProduct,
			Title:         "Redis calendar test",
			Status:        StatusTodo,
			ExecutedBy:    &ActorRef{Kind: ActorAgent, ID: f.AgentA},
			Schedule:      &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &at},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE tasks SET scheduled_at=date_trunc('minute',now()) WHERE id=$1`,
		created.Task.ID,
	); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := beat.Scan(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Exhausted retries leave an archived job. That Redis record must not
	// suppress a later wakeup for work PostgreSQL still considers due.
	inspector := asynq.NewInspectorFromRedisClient(client)
	pending, err := inspector.ListPendingTasks(schedule.QueueName)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending jobs = %v, error = %v; want one deduplicated job", pending, err)
	}
	if err := inspector.ArchiveTask(schedule.QueueName, pending[0].ID); err != nil {
		t.Fatal(err)
	}
	recoveryDeadline := time.NewTimer(40 * time.Second)
	defer recoveryDeadline.Stop()
	recoveryPoll := time.NewTicker(100 * time.Millisecond)
	defer recoveryPoll.Stop()
	for {
		if err := beat.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		pending, err = inspector.ListPendingTasks(schedule.QueueName)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 1 {
			break
		}
		select {
		case <-recoveryPoll.C:
		case <-recoveryDeadline.C:
			t.Fatal("archived job permanently suppressed the due Task")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	for range 2 {
		worker := asynq.NewServerFromRedisClient(client, asynq.Config{
			Concurrency: 2, Queues: map[string]int{schedule.QueueName: 1},
			ShutdownTimeout: time.Second, LogLevel: asynq.ErrorLevel,
		})
		if err := worker.Start(beat); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(worker.Shutdown)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()
	for {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE task_id=$1`, created.Task.ID).
			Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		if count > 1 {
			t.Fatalf("duplicate Runs: %d", count)
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatal("Asynq did not dispatch Calendar Task")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	// This Redis belongs solely to this test. Product receipts survive losing it.
	if err := client.FlushAll(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	if err := beat.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"id":"` + created.Task.ID.String() + `","companyId":"` + f.CompanyA.String() + `"}`)
	if err := beat.ProcessTask(ctx, asynq.NewTask("task-calendar:fire", payload)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE task_id=$1`, created.Task.ID).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("broker recovery duplicated work: %d Runs", count)
	}
}
