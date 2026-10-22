package telemetry

import (
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type intelligentUIMetrics struct {
	sources         *prometheus.CounterVec
	sourceDuration  *prometheus.HistogramVec
	sourceBytes     *prometheus.HistogramVec
	captures        *prometheus.CounterVec
	captureDuration *prometheus.HistogramVec
	previews        *prometheus.CounterVec
	duration        *prometheus.HistogramVec
	bytes           *prometheus.HistogramVec
}

func newIntelligentUIMetrics(registry *prometheus.Registry) intelligentUIMetrics {
	metrics := intelligentUIMetrics{
		sources: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "zenith-team",
			Subsystem: "intelligent_ui",
			Name:      "source_submissions_total",
			Help:      "Page source create, proposal and member-check requests by bounded outcome; accepted includes successful retries.",
		}, []string{"operation", "outcome"}),
		sourceDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "zenith-team",
			Subsystem: "intelligent_ui",
			Name:      "source_submission_duration_seconds",
			Help:      "Complete Page source submission service duration, including admission and persistence where applicable.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"operation"}),
		sourceBytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "zenith-team",
			Subsystem: "intelligent_ui",
			Name:      "source_submission_bytes",
			Help:      "UTF-8 source bytes per request; proposal batches sum their source operations without retaining content.",
			Buckets:   []float64{0, 256, 1024, 4096, 16384, 65536, 131072, 262144, 1048576},
		}, []string{"operation"}),
		captures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "zenith-team",
			Subsystem: "intelligent_ui",
			Name:      "capture_operations_total",
			Help:      "Capture preparation and save outcomes, including exact retries and reviewed evidence mismatches.",
		}, []string{"operation", "outcome"}),
		captureDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "zenith-team",
			Subsystem: "intelligent_ui",
			Name:      "capture_operation_duration_seconds",
			Help:      "Complete capture preparation or save service duration, including authorization and persistence.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"operation"}),
		previews: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "zenith-team", Subsystem: "intelligent_ui", Name: "preview_operations_total",
			Help: "Preview operations by bounded operation and outcome; includes retries and rejections.",
		}, []string{"operation", "outcome"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "zenith-team",
			Subsystem: "intelligent_ui",
			Name:      "preview_operation_duration_seconds",
			Help:      "Preview compilation or complete patch request duration, including admission and persistence for patches.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"operation"}),
		bytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "zenith-team",
			Subsystem: "intelligent_ui",
			Name:      "preview_source_bytes",
			Help:      "UTF-8 bytes submitted in a patch or processed by read-time compilation; no source content is retained.",
			Buckets:   []float64{0, 256, 1024, 4096, 16384, 65536, 131072, 262144},
		}, []string{"operation"}),
	}
	registry.MustRegister(
		metrics.sources,
		metrics.sourceDuration,
		metrics.sourceBytes,
		metrics.previews,
		metrics.duration,
		metrics.bytes,
		metrics.captures,
		metrics.captureDuration,
	)
	return metrics
}

func (r *Registry) ObservePagePreview(operation, outcome string, duration time.Duration, bytes int) {
	if !slices.Contains([]string{"patch", "compile"}, operation) {
		operation = "other"
	}
	if !slices.Contains(
		[]string{
			"accepted",
			"replayed",
			"discarded",
			"conflict",
			"limit",
			"denied",
			"invalid",
			"error",
			"complete",
			"partial",
			"empty",
		},
		outcome,
	) {
		outcome = "other"
	}
	r.intelligentUI.previews.WithLabelValues(operation, outcome).Inc()
	r.intelligentUI.duration.WithLabelValues(operation).Observe(max(0, duration.Seconds()))
	r.intelligentUI.bytes.WithLabelValues(operation).Observe(float64(max(0, bytes)))
}

func (r *Registry) ObservePresentationCapture(operation, outcome string, duration time.Duration) {
	if !slices.Contains([]string{"prepare", "save"}, operation) {
		operation = "other"
	}
	if !slices.Contains(
		[]string{
			"prepared",
			"saved",
			"replayed",
			"mismatch",
			"conflict",
			"invalid",
			"denied",
			"error",
			"issues",
		},
		outcome,
	) {
		outcome = "other"
	}
	r.intelligentUI.captures.WithLabelValues(operation, outcome).Inc()
	r.intelligentUI.captureDuration.WithLabelValues(operation).Observe(max(0, duration.Seconds()))
}

func (r *Registry) ObservePageSource(operation, outcome string, duration time.Duration, bytes int) {
	if !slices.Contains([]string{"create", "proposal", "check"}, operation) {
		operation = "other"
	}
	if !slices.Contains([]string{"accepted", "rejected", "conflict", "denied", "disabled", "error"}, outcome) {
		outcome = "other"
	}
	r.intelligentUI.sources.WithLabelValues(operation, outcome).Inc()
	r.intelligentUI.sourceDuration.WithLabelValues(operation).Observe(max(0, duration.Seconds()))
	r.intelligentUI.sourceBytes.WithLabelValues(operation).Observe(float64(max(0, bytes)))
}
