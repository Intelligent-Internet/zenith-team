package telemetry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/mailbox"
)

func TestHTTPMetricsExposeOnlyRouteTemplates(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	registry.ObserveHTTP(http.MethodGet, "/api/v1/tasks/:taskId", http.StatusOK, 12*time.Millisecond)

	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if !strings.Contains(
		body,
		`boardly_http_requests_total{method="GET",route="/api/v1/tasks/:taskId",status="200"} 1`,
	) {
		t.Fatalf("metrics missing route-template counter:\n%s", body)
	}
	if strings.Contains(body, "0197f590-10b8-7cc0-a6c6-160ed7a18b2c") {
		t.Fatal("metrics contain a resource identifier")
	}
}

func TestMailboxMetricsExposeOnlyClosedNonPIILabels(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	registry.ObserveMailboxWebhook("delivered")
	registry.ObserveMailboxWebhook("customer@example.com")
	registry.ObserveMailboxProvider("rate_limited")
	registry.ObserveMailboxProvider("provider-email-sensitive-id")
	registry.ObserveMailboxCache("hit")
	registry.ObserveMailboxCache("sensitive-subject")
	registry.ObserveMailboxRetention(mailbox.PurgeResult{Attachments: 3, Messages: 2, Receipts: 4})

	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{
		`boardly_mailbox_webhooks_total{outcome="delivered"} 1`,
		`boardly_mailbox_webhooks_total{outcome="invalid"} 1`,
		`boardly_mailbox_provider_requests_total{outcome="rate_limited"} 1`,
		`boardly_mailbox_provider_requests_total{outcome="invalid"} 1`,
		`boardly_mailbox_cache_total{outcome="hit"} 1`,
		`boardly_mailbox_cache_total{outcome="invalid"} 1`,
		`boardly_mailbox_retention_deleted_total{kind="attachments"} 3`,
		`boardly_mailbox_retention_deleted_total{kind="messages"} 2`,
		`boardly_mailbox_retention_deleted_total{kind="receipts"} 4`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics missing %q:\n%s", expected, body)
		}
	}
	for _, forbidden := range []string{
		"customer@example.com", "provider-email-sensitive-id", "sensitive-subject",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Mailbox metrics exposed PII label %q", forbidden)
		}
	}
}

func TestRunMetricsExposeOnlyClosedBoundedLabels(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	registry.ObserveRunUsageDelta(RunUsageDeltaObservation{
		Provider: "codex", InputTokens: 11, OutputTokens: 7,
		ReasoningTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 2,
	})
	registry.ObserveRunTerminal(RunTerminalObservation{
		Provider: "claude", Outcome: "message",
	})
	registry.ObserveRunTerminal(RunTerminalObservation{
		Provider:    "future-provider-with-user-controlled-suffix",
		Outcome:     "failed",
		FailureKind: "infrastructure",
	})
	registry.ObserveRunTerminal(RunTerminalObservation{
		Provider: "hostile-provider", Outcome: "hostile-outcome", FailureKind: "hostile-failure-kind",
	})

	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, expected := range []string{
		`boardly_run_tokens_total{kind="input",provider="codex"} 11`,
		`boardly_run_tokens_total{kind="output",provider="codex"} 7`,
		`boardly_run_tokens_total{kind="reasoning",provider="codex"} 5`,
		`boardly_run_tokens_total{kind="cache_read",provider="codex"} 3`,
		`boardly_run_tokens_total{kind="cache_write",provider="codex"} 2`,
		`boardly_run_terminal_total{failure_kind="none",outcome="message",provider="claude"} 1`,
		`boardly_run_terminal_total{failure_kind="infrastructure",outcome="failed",provider="other"} 1`,
		`boardly_run_terminal_total{failure_kind="invalid",outcome="invalid",provider="other"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics missing %q:\n%s", expected, body)
		}
	}
	for _, forbidden := range []string{
		"future-provider-with-user-controlled-suffix",
		"hostile-provider",
		"hostile-outcome",
		"hostile-failure-kind",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("metrics exposed unbounded label %q", forbidden)
		}
	}
}
