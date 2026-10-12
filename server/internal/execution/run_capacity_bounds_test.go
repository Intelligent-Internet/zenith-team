package execution

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// warnCounter counts WARN records whose message names the out-of-bounds cap,
// so the assertion is about that one line and not about whatever else the
// package logs while the test runs.
type warnCounter struct {
	slog.Handler
	count *int
}

func (h warnCounter) Handle(_ context.Context, record slog.Record) error {
	if record.Level == slog.LevelWarn && strings.Contains(record.Message, "max_concurrent_runs") {
		*h.count++
	}
	return nil
}

func (h warnCounter) Enabled(context.Context, slog.Level) bool { return true }

func captureCapWarnings(t *testing.T) *int {
	t.Helper()
	count := 0
	previous := slog.Default()
	slog.SetDefault(slog.New(warnCounter{Handler: slog.DiscardHandler, count: &count}))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &count
}

// A stored cap outside 1..10 can only come from a write that went around the
// request binder. The gate clamps it rather than acting on it, because a 0 or
// a negative would close the agent's task lane forever — every claim answering
// "nothing to do" with nothing anywhere saying why.
func TestAgentTaskLaneCapacityClampsOutOfBoundsValues(t *testing.T) {
	warnings := captureCapWarnings(t)
	service := &DefaultRunService{}

	for _, test := range []struct {
		name   string
		stored int32
		want   int32
	}{
		{"zero closes the lane forever", 0, MinTaskLaneCapacity},
		{"negative", -4, MinTaskLaneCapacity},
		{"above the ceiling", 99, MaxTaskLaneCapacity},
		{"the floor itself is fine", MinTaskLaneCapacity, MinTaskLaneCapacity},
		{"the ceiling itself is fine", MaxTaskLaneCapacity, MaxTaskLaneCapacity},
		{"the default", 3, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := service.agentTaskLaneCapacity(uuid.New(), test.stored)
			if got != test.want {
				t.Errorf("agentTaskLaneCapacity(%d) = %d, want %d", test.stored, got, test.want)
			}
		})
	}
	if *warnings != 3 {
		t.Errorf("warned %d times, want one per out-of-bounds agent (3)", *warnings)
	}
}

// The claim path runs on every daemon poll, so the warning is once per agent:
// a line per poll is a line nobody reads, and no line at all is the failure
// this is here to prevent.
func TestAgentTaskLaneCapacityWarnsOncePerAgent(t *testing.T) {
	warnings := captureCapWarnings(t)
	service := &DefaultRunService{}
	agent, other := uuid.New(), uuid.New()

	for range 5 {
		service.agentTaskLaneCapacity(agent, 0)
	}
	if *warnings != 1 {
		t.Fatalf("warned %d times for one agent, want 1", *warnings)
	}
	service.agentTaskLaneCapacity(other, 0)
	if *warnings != 2 {
		t.Errorf("warned %d times for two agents, want 2", *warnings)
	}
	// An in-bounds cap is the ordinary case and says nothing at all.
	service.agentTaskLaneCapacity(uuid.New(), 3)
	if *warnings != 2 {
		t.Errorf("an in-bounds cap warned; warnings = %d, want 2", *warnings)
	}
}
