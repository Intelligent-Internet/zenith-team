package schedule

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hibiken/asynq"
)

func TestBeatRejectsUnknownTaskAndMalformedIDs(t *testing.T) {
	beat := &Beat{}
	for _, task := range []*asynq.Task{
		asynq.NewTask("schedule:fire2", []byte(`{}`)),
		asynq.NewTask(FireTaskType, []byte(`{"scheduleId":"bad","companyId":"bad"}`)),
		asynq.NewTask(FireTaskType, []byte(`{`)),
	} {
		if err := beat.ProcessTask(context.Background(), task); !errors.Is(err, asynq.SkipRetry) {
			t.Errorf("task %s = %v", task.Type(), err)
		}
	}
}

func TestBeatRetryDelays(t *testing.T) {
	for n, want := range []time.Duration{time.Second, 3 * time.Second, 8 * time.Second, 8 * time.Second} {
		if got := RetryDelay(n, nil, nil); got != want {
			t.Errorf("retry %d = %s, want %s", n, got, want)
		}
	}
}
