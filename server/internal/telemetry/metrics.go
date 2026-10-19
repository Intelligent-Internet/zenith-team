package telemetry

import (
	"net/http"
	"strconv"
	"time"

	"boardly.dev/boardly/server/internal/mailbox"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// RunUsageDeltaObservation is one persisted provider-token delta for a Run.
// The observation structs live here so the metrics boundary survives the run
// domain rebuild; producers hand them in from their callback paths.
type RunUsageDeltaObservation struct {
	Provider         string
	InputTokens      int64
	OutputTokens     int64
	ReasoningTokens  int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

// RunTerminalObservation is one newly persisted terminal Run outcome.
type RunTerminalObservation struct {
	Provider    string
	Outcome     string
	FailureKind string
}

type Registry struct {
	schedule     scheduleMetrics
	registry     *prometheus.Registry
	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec

	runTokens   *prometheus.CounterVec
	runTerminal *prometheus.CounterVec

	mailboxWebhooks         *prometheus.CounterVec
	mailboxProviderRequests *prometheus.CounterVec
	mailboxCache            *prometheus.CounterVec
	mailboxRetentionDeleted *prometheus.CounterVec
}

func NewRegistry() *Registry {
	registry := prometheus.NewRegistry()
	httpRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "boardly",
		Subsystem: "http",
		Name:      "requests_total",
		Help:      "Total HTTP requests by method, route template, and status.",
	}, []string{"method", "route", "status"})
	httpDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "boardly",
		Subsystem: "http",
		Name:      "request_duration_seconds",
		Help:      "HTTP request duration by method and route template.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "route"})
	runTokens := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "boardly",
		Subsystem: "run",
		Name:      "tokens_total",
		Help:      "Total provider tokens persisted for Runs by bounded provider and token kind.",
	}, []string{"provider", "kind"})
	runTerminal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "boardly",
		Subsystem: "run",
		Name:      "terminal_total",
		Help:      "Total newly persisted terminal Run outcomes by bounded provider and result class.",
	}, []string{"provider", "outcome", "failure_kind"})
	mailboxWebhooks := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "boardly", Subsystem: "mailbox", Name: "webhooks_total",
		Help: "Total verified Mailbox webhook outcomes by closed result class.",
	}, []string{"outcome"})
	mailboxProviderRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "boardly", Subsystem: "mailbox", Name: "provider_requests_total",
		Help: "Total on-demand Mailbox provider requests by closed result class.",
	}, []string{"outcome"})
	mailboxCache := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "boardly", Subsystem: "mailbox", Name: "cache_total",
		Help: "Total sanitized Mailbox detail cache outcomes.",
	}, []string{"outcome"})
	mailboxRetentionDeleted := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "boardly", Subsystem: "mailbox", Name: "retention_deleted_total",
		Help: "Total Mailbox metadata rows deleted by retention.",
	}, []string{"kind"})
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		httpRequests,
		httpDuration,
		runTokens,
		runTerminal,
		mailboxWebhooks,
		mailboxProviderRequests,
		mailboxCache,
		mailboxRetentionDeleted,
	)
	return &Registry{
		schedule:     newScheduleMetrics(registry),
		registry:     registry,
		httpRequests: httpRequests,
		httpDuration: httpDuration,

		runTokens:   runTokens,
		runTerminal: runTerminal,

		mailboxWebhooks:         mailboxWebhooks,
		mailboxProviderRequests: mailboxProviderRequests,
		mailboxCache:            mailboxCache,
		mailboxRetentionDeleted: mailboxRetentionDeleted,
	}
}

func (r *Registry) ObserveMailboxWebhook(outcome string) {
	if r != nil && r.mailboxWebhooks != nil {
		r.mailboxWebhooks.WithLabelValues(boundedMailboxWebhookOutcome(outcome)).Inc()
	}
}

func (r *Registry) ObserveMailboxProvider(outcome string) {
	if r != nil && r.mailboxProviderRequests != nil {
		r.mailboxProviderRequests.WithLabelValues(boundedMailboxProviderOutcome(outcome)).Inc()
	}
}

func (r *Registry) ObserveMailboxCache(outcome string) {
	if r != nil && r.mailboxCache != nil {
		r.mailboxCache.WithLabelValues(boundedMailboxCacheOutcome(outcome)).Inc()
	}
}

func (r *Registry) ObserveMailboxRetention(result mailbox.PurgeResult) {
	if r == nil || r.mailboxRetentionDeleted == nil {
		return
	}
	for _, value := range []struct {
		kind  string
		count int64
	}{
		{kind: "attachments", count: result.Attachments},
		{kind: "messages", count: result.Messages},
		{kind: "receipts", count: result.Receipts},
	} {
		if value.count > 0 {
			r.mailboxRetentionDeleted.WithLabelValues(value.kind).Add(float64(value.count))
		}
	}
}

func boundedMailboxWebhookOutcome(value string) string {
	switch value {
	case "delivered", "ignored", "replay", "invalid", "failed":
		return value
	default:
		return "invalid"
	}
}

func boundedMailboxProviderOutcome(value string) string {
	switch value {
	case "success", "not_found", "rate_limited", "unavailable", "invalid":
		return value
	default:
		return "invalid"
	}
}

func boundedMailboxCacheOutcome(value string) string {
	switch value {
	case "hit", "miss", "error", "invalid":
		return value
	default:
		return "invalid"
	}
}

func (r *Registry) ObserveHTTP(method, route string, status int, duration time.Duration) {
	r.httpRequests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	r.httpDuration.WithLabelValues(method, route).Observe(duration.Seconds())
}

func (r *Registry) ObserveRunUsageDelta(value RunUsageDeltaObservation) {
	if r == nil || r.runTokens == nil {
		return
	}
	provider := boundedRunProvider(value.Provider)
	for _, token := range []struct {
		kind  string
		value int64
	}{
		{kind: "input", value: value.InputTokens},
		{kind: "output", value: value.OutputTokens},
		{kind: "reasoning", value: value.ReasoningTokens},
		{kind: "cache_read", value: value.CacheReadTokens},
		{kind: "cache_write", value: value.CacheWriteTokens},
	} {
		if token.value > 0 {
			r.runTokens.WithLabelValues(provider, token.kind).Add(float64(token.value))
		}
	}
}

func (r *Registry) ObserveRunTerminal(value RunTerminalObservation) {
	if r == nil || r.runTerminal == nil {
		return
	}
	outcome := boundedRunOutcome(value.Outcome)
	r.runTerminal.WithLabelValues(
		boundedRunProvider(value.Provider),
		outcome,
		boundedRunFailureKind(outcome, value.FailureKind),
	).Inc()
}

func boundedRunProvider(value string) string {
	switch value {
	case "codex", "claude":
		return value
	default:
		return "other"
	}
}

func boundedRunOutcome(value string) string {
	switch value {
	case "message", "no_response", "failed":
		return value
	default:
		return "invalid"
	}
}

func boundedRunFailureKind(outcome, value string) string {
	if outcome == "message" || outcome == "no_response" {
		if value == "" {
			return "none"
		}
		return "invalid"
	}
	if outcome == "failed" {
		switch value {
		case "provider", "infrastructure":
			return value
		default:
			return "invalid"
		}
	}
	return "invalid"
}

func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}
