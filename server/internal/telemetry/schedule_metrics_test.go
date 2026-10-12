package telemetry

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestScheduleMetricsKeepClosedLabelsAndIndependentProbe(t *testing.T) {
	r := NewRegistry()
	r.ObserveScheduleTask("arbitrary-id", "ok")
	r.ObserveScheduleEnqueue("fire", "arbitrary-id")
	r.ObserveScheduleFiring("arbitrary-id", "materialized", "", time.Second)
	if got := (testutil.CollectAndCount(r.schedule.tasks) + testutil.CollectAndCount(r.schedule.enqueue) + testutil.CollectAndCount(r.schedule.firings)); got != 0 {
		t.Fatalf("unknown labels created %d families", got)
	}
	r.ObserveScheduleFiring("beat", "paused", "arbitrary-id", 0)
	if got := testutil.ToFloat64(r.schedule.pauses.WithLabelValues("firing_failed")); got != 1 {
		t.Fatalf("bounded fallback = %v", got)
	}
	r.ObserveScheduleProbe(75, 5, 3, nil)
	r.ObserveScheduleQueue(0, 0, 0, 0, false)
	if got := testutil.ToFloat64(r.schedule.lag); got != 75 {
		t.Fatalf("Redis failure changed PG lag to %v", got)
	}
	r.ObserveScheduleProbe(0, 0, 0, errors.New("unavailable"))
	if got := testutil.ToFloat64(r.schedule.overdue); got != 3 {
		t.Fatalf("failed probe cleared overdue to %v", got)
	}
	if got := testutil.ToFloat64(r.schedule.probe.WithLabelValues("error")); got != 1 {
		t.Fatalf("probe error count = %v", got)
	}
}
