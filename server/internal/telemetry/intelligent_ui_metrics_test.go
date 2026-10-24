package telemetry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPagePreviewMetricsUseBoundedLabels(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	registry.ObservePagePreview("patch", "accepted", 250*time.Millisecond, len("日本語"))
	registry.ObservePagePreview("patch", "replayed", 250*time.Millisecond, 0)
	registry.ObservePagePreview("private-generation-id", "private-source-text", -time.Second, -1)
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{
		`zenith_team_intelligent_ui_preview_operations_total{operation="patch",outcome="accepted"} 1`,
		`zenith_team_intelligent_ui_preview_operations_total{operation="patch",outcome="replayed"} 1`,
		`zenith_team_intelligent_ui_preview_operations_total{operation="other",outcome="other"} 1`,
		`zenith_team_intelligent_ui_preview_operation_duration_seconds_sum{operation="patch"} 0.5`,
		`zenith_team_intelligent_ui_preview_operation_duration_seconds_count{operation="patch"} 2`,
		`zenith_team_intelligent_ui_preview_operation_duration_seconds_sum{operation="other"} 0`,
		`zenith_team_intelligent_ui_preview_source_bytes_sum{operation="patch"} 9`,
		`zenith_team_intelligent_ui_preview_source_bytes_sum{operation="other"} 0`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics missing %q", expected)
		}
	}
	for _, forbidden := range []string{"private-generation-id", "private-source-text", "日本語"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("metrics contain private value %q", forbidden)
		}
	}
}

func TestCaptureMetricsUseBoundedLabels(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	registry.ObservePresentationCapture("save", "mismatch", 250*time.Millisecond)
	registry.ObservePresentationCapture("save", "replayed", 250*time.Millisecond)
	registry.ObservePresentationCapture("private-page-id", "private-input-value", -time.Second)
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{
		`zenith_team_intelligent_ui_capture_operations_total{operation="save",outcome="mismatch"} 1`,
		`zenith_team_intelligent_ui_capture_operations_total{operation="save",outcome="replayed"} 1`,
		`zenith_team_intelligent_ui_capture_operations_total{operation="other",outcome="other"} 1`,
		`zenith_team_intelligent_ui_capture_operation_duration_seconds_sum{operation="save"} 0.5`,
		`zenith_team_intelligent_ui_capture_operation_duration_seconds_sum{operation="other"} 0`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics missing %q", expected)
		}
	}
	for _, forbidden := range []string{"private-page-id", "private-input-value"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("metrics contain private value %q", forbidden)
		}
	}
}

func TestPageSourceMetricsUseBoundedLabels(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	registry.ObservePageSource("check", "accepted", 250*time.Millisecond, len("日本語"))
	registry.ObservePageSource("check", "rejected", 250*time.Millisecond, 0)
	registry.ObservePageSource("private-page-id", "private-source-text", -time.Second, -1)
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, want := range []string{
		`zenith_team_intelligent_ui_source_submissions_total{operation="check",outcome="accepted"} 1`,
		`zenith_team_intelligent_ui_source_submissions_total{operation="check",outcome="rejected"} 1`,
		`zenith_team_intelligent_ui_source_submissions_total{operation="other",outcome="other"} 1`,
		`zenith_team_intelligent_ui_source_submission_duration_seconds_sum{operation="check"} 0.5`,
		`zenith_team_intelligent_ui_source_submission_duration_seconds_sum{operation="other"} 0`,
		`zenith_team_intelligent_ui_source_submission_bytes_sum{operation="check"} 9`,
		`zenith_team_intelligent_ui_source_submission_bytes_sum{operation="other"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
	for _, forbidden := range []string{"private-page-id", "private-source-text", "日本語"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("metrics contain private value %q", forbidden)
		}
	}
}
