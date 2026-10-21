package main

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/Intelligent-Internet/zenith-team/server/internal/schedule"
	"github.com/Intelligent-Internet/zenith-team/server/internal/telemetry"
)

func TestScheduleQueueRepairsCatalogAfterRedisFlush(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	defer client.Close()
	producer := asynq.NewClientFromRedisClient(client)
	enqueue := func() {
		t.Helper()
		if _, err := producer.Enqueue(
			asynq.NewTask(schedule.ScanTaskType, nil),
			schedule.ScanOptions()...); err != nil {
			t.Fatal(err)
		}
	}
	enqueue()
	redisServer.FlushAll()
	enqueue() // The existing producer's internal queue cache is now stale.
	queue := &scheduleQueue{
		redis:     client,
		registry:  telemetry.NewRegistry(),
		inspector: asynq.NewInspectorFromRedisClient(client),
	}
	if err := queue.probeQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	names, err := queue.inspector.Queues()
	if err != nil || len(names) != 1 || names[0] != schedule.QueueName {
		t.Fatalf("queue catalog not restored: %v %v", names, err)
	}
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatal("shared Redis was closed", err)
	}
}
