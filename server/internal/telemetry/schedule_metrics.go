package telemetry

import (
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type scheduleMetrics struct {
	lag, due, overdue, pending, retry, archived, latency, queueAvailable prometheus.Gauge
	probe, sweep, pauses                                                 *prometheus.CounterVec
	firings, tasks, enqueue                                              *prometheus.CounterVec
	firingDelay                                                          *prometheus.HistogramVec
}

func newScheduleMetrics(registry *prometheus.Registry) scheduleMetrics {
	gauge := func(name, help string) prometheus.Gauge {
		value := prometheus.NewGauge(
			prometheus.GaugeOpts{Namespace: "boardly", Subsystem: "schedule", Name: name, Help: help},
		)
		registry.MustRegister(value)
		return value
	}
	counter := func(name, help string, labels ...string) *prometheus.CounterVec {
		value := prometheus.NewCounterVec(
			prometheus.CounterOpts{Namespace: "boardly", Subsystem: "schedule", Name: name, Help: help},
			labels,
		)
		registry.MustRegister(value)
		return value
	}
	delay := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "boardly",
			Subsystem: "schedule",
			Name:      "firing_delay_seconds",
			Help:      "Committed occurrence lateness measured using the database clock.",
			Buckets:   []float64{1, 5, 10, 15, 30, 60, 75, 120, 300, 600},
		},
		[]string{"path"},
	)
	registry.MustRegister(delay)
	return scheduleMetrics{
		lag:            gauge("firing_lag_seconds", "Oldest active due Schedule lateness."),
		due:            gauge("due", "Active due Schedules, capped at 10000."),
		overdue:        gauge("overdue", "Schedules more than 60 seconds late, capped at 10000."),
		pending:        gauge("queue_pending", "Pending Asynq Schedule tasks."),
		retry:          gauge("queue_retry", "Retrying Asynq Schedule tasks."),
		archived:       gauge("queue_archived", "Archived Asynq Schedule tasks."),
		latency:        gauge("queue_latency_seconds", "Oldest pending Schedule task latency."),
		queueAvailable: gauge("queue_available", "Whether the last Asynq queue probe succeeded."),
		probe:          counter("probe_runs_total", "Schedule PostgreSQL probes.", "result"),
		sweep:          counter("sweep_passes_total", "Schedule repair sweep passes.", "result"),
		pauses:         counter("pauses_total", "Schedules paused by bounded reason.", "reason"),
		firings:        counter("firings_total", "Schedule transaction outcomes.", "path", "outcome"),
		tasks:          counter("tasks_total", "Asynq Schedule task outcomes.", "task", "result"),
		enqueue:        counter("enqueue_total", "Asynq Schedule enqueues.", "task", "result"),
		firingDelay:    delay,
	}
}

func (r *Registry) ObserveScheduleProbe(lag float64, due, overdue int64, err error) {
	if err != nil {
		r.schedule.probe.WithLabelValues("error").Inc()
		return
	}
	r.schedule.lag.Set(lag)
	r.schedule.due.Set(float64(due))
	r.schedule.overdue.Set(float64(overdue))
	r.schedule.probe.WithLabelValues("ok").Inc()
}

func (r *Registry) ObserveScheduleQueue(pending, retry, archived int, latency time.Duration, available bool) {
	if !available {
		r.schedule.queueAvailable.Set(0)
		return
	}
	r.schedule.queueAvailable.Set(1)
	r.schedule.pending.Set(float64(pending))
	r.schedule.retry.Set(float64(retry))
	r.schedule.archived.Set(float64(archived))
	r.schedule.latency.Set(latency.Seconds())
}

func (r *Registry) ObserveScheduleFiring(path, outcome, reason string, delay time.Duration) {
	if !slices.Contains([]string{"beat", "sweep", "run_now"}, path) ||
		!slices.Contains([]string{"materialized", "skipped", "needs_attention", "paused", "noop"}, outcome) {
		return
	}
	r.schedule.firings.WithLabelValues(path, outcome).Inc()
	if outcome == "materialized" || outcome == "skipped" {
		r.schedule.firingDelay.WithLabelValues(path).Observe(max(0, delay.Seconds()))
	}
	if reason != "" && (outcome == "paused" || outcome == "needs_attention") {
		if !slices.Contains(
			[]string{
				"trigger_invalid",
				"firing_failed",
				"materialization_blocked",
				"company_changed",
				"creator_changed",
				"agent_unavailable",
				"provider_changed",
				"source_changed",
				"user_paused",
				"definition_updated",
			},
			reason,
		) {
			reason = "firing_failed"
		}
		r.schedule.pauses.WithLabelValues(reason).Inc()
	}
}

func (r *Registry) ObserveScheduleSweep(result string) {
	if !slices.Contains([]string{"ok", "error", "deadline"}, result) {
		return
	}
	r.schedule.sweep.WithLabelValues(result).Inc()
}

func (r *Registry) ObserveScheduleTask(task, result string) {
	if !slices.Contains([]string{"scan", "fire"}, task) ||
		!slices.Contains([]string{"ok", "noop", "retry", "failed", "bad_payload"}, result) {
		return
	}
	r.schedule.tasks.WithLabelValues(task, result).Inc()
}

func (r *Registry) ObserveScheduleEnqueue(task, result string) {
	if !slices.Contains([]string{"scan", "fire"}, task) ||
		!slices.Contains([]string{"ok", "duplicate", "error"}, result) {
		return
	}
	r.schedule.enqueue.WithLabelValues(task, result).Inc()
}
