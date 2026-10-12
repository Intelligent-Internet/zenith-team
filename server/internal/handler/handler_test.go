package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/telemetry"
)

var _ http.Handler = (*Handler)(nil)

func TestHealthzReportsProcessLiveness(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	New(Handler{}).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status body = %#v", body)
	}
}

func TestNewBuildsAnApplicationHandler(t *testing.T) {
	t.Parallel()

	input := fullyWiredHandler()
	allowedOrigin := input.AllowedOrigins[0]
	handler := New(input)
	input.AllowedOrigins[0] = "https://mutated.boardly.test"

	if handler.router == nil {
		t.Fatal("New returned a Handler without a Gin router")
	}
	if handler.AuthService != input.AuthService ||
		handler.CompanyService != input.CompanyService ||
		handler.CompanyResourceService != input.CompanyResourceService {
		t.Fatal("Handler did not retain the injected transport dependencies")
	}
	if handler.AllowedOrigins[0] != allowedOrigin {
		t.Fatal("Handler retained the caller's mutable AllowedOrigins slice")
	}
}

// An unmatched route is still an error response, so it carries the one
// {detail, code, requestId} envelope like every other failure — never gin's
// plain-text "404 page not found". Gin routes a method mismatch on a known
// path through the same handler, so both answer route_not_found.
func TestUnmatchedRouteAnswersTheErrorEnvelope(t *testing.T) {
	t.Parallel()

	handler := New(Handler{})
	for _, test := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "unknown API path", method: http.MethodGet, path: "/api/v1/nope"},
		{name: "unknown root path", method: http.MethodGet, path: "/nope"},
		{name: "method mismatch on a known path", method: http.MethodPost, path: "/healthz"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))

			assertErrorResponse(t, response, http.StatusNotFound, "route_not_found")
			if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Errorf("Content-Type = %q", got)
			}
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if requestID := response.Header().Get("X-Request-ID"); body["requestId"] != requestID {
				t.Errorf("requestId = %q, header = %q", body["requestId"], requestID)
			}
		})
	}
}

func TestRequestLoggerUsesRouteTemplateAndRequestID(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	router := gin.New()
	router.Use(middleware.RequestID(), middleware.RequestLogger(logger))
	router.GET("/tasks/:taskId", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := httptest.NewRequest(http.MethodGet, "/tasks/0197f590-10b8-7cc0-a6c6-160ed7a18b2c", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	logLine := output.String()
	if !strings.Contains(logLine, `"route":"/tasks/:taskId"`) {
		t.Fatalf("log missing route template: %s", logLine)
	}
	if !strings.Contains(logLine, `"request_id":"`+response.Header().Get("X-Request-ID")+`"`) {
		t.Fatalf("log missing request ID: %s", logLine)
	}
	if strings.Contains(logLine, "0197f590-10b8-7cc0-a6c6-160ed7a18b2c") {
		t.Fatalf("log contains concrete resource ID: %s", logLine)
	}
}

func TestRouterExposesLowCardinalityHTTPMetrics(t *testing.T) {
	t.Parallel()

	registry := telemetry.NewRegistry()
	handler := New(Handler{
		MetricsHandler: registry.Handler(),
		HTTPMetrics:    registry,
	})

	missingResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		missingResponse,
		httptest.NewRequest(http.MethodGet, "/private/resource/0197f590-10b8-7cc0-a6c6-160ed7a18b2c", nil),
	)

	healthResponse := httptest.NewRecorder()
	handler.ServeHTTP(healthResponse, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	metricsResponse := httptest.NewRecorder()
	handler.ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metricsResponse.Body.String()
	if metricsResponse.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d", metricsResponse.Code, http.StatusOK)
	}
	if !strings.Contains(body, `boardly_http_requests_total{method="GET",route="unmatched",status="404"} 1`) {
		t.Fatalf("metrics missing unmatched route counter:\n%s", body)
	}
	if strings.Contains(body, "0197f590-10b8-7cc0-a6c6-160ed7a18b2c") ||
		strings.Contains(body, `route="/healthz"`) {
		t.Fatal("metrics contain a resource ID or health-probe route")
	}
}

func TestCSRFMiddlewareProtectsUnsafeCookieRequests(t *testing.T) {
	t.Parallel()

	protector, err := auth.NewCSRF([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("NewCSRF() error = %v", err)
	}
	authCookie := "signed-access-cookie"
	csrfToken := protector.Token(authCookie)

	newRouter := func() http.Handler {
		router := gin.New()
		router.Use(middleware.RequestID(), middleware.RequireCSRF(protector))
		router.POST("/mutation", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		router.POST("/api/v1/auth/refresh", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		return router
	}

	t.Run("missing token is rejected", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/mutation", nil)
		request.AddCookie(&http.Cookie{Name: middleware.AuthCookieName, Value: authCookie})
		response := httptest.NewRecorder()
		newRouter().ServeHTTP(response, request)

		if response.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
		}
	})

	t.Run("authorization header does not bypass cookie CSRF", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/mutation", nil)
		request.Header.Set("Authorization", "Bearer invalid-or-unrelated-token")
		request.AddCookie(&http.Cookie{Name: middleware.AuthCookieName, Value: authCookie})
		response := httptest.NewRecorder()
		newRouter().ServeHTTP(response, request)

		if response.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
		}
	})

	t.Run("bearer-only request does not require CSRF", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/mutation", nil)
		request.Header.Set("Authorization", "Bearer desktop-access-token")
		response := httptest.NewRecorder()
		newRouter().ServeHTTP(response, request)

		if response.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
		}
	})

	t.Run("bound double-submit token is accepted", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/mutation", nil)
		request.AddCookie(&http.Cookie{Name: middleware.AuthCookieName, Value: authCookie})
		request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: csrfToken})
		request.Header.Set(middleware.CSRFHeaderName, csrfToken)
		response := httptest.NewRecorder()
		newRouter().ServeHTTP(response, request)

		if response.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
		}
	})

	t.Run("refresh cookie without access still requires CSRF", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
		request.AddCookie(&http.Cookie{Name: middleware.RefreshCookieName, Value: "rotating-refresh-cookie"})
		response := httptest.NewRecorder()
		newRouter().ServeHTTP(response, request)

		if response.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
		}
	})

	t.Run("refresh-bound double-submit token is accepted without access", func(t *testing.T) {
		refreshCookie := "rotating-refresh-cookie"
		refreshCSRF := protector.Token(refreshCookie)
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
		request.AddCookie(&http.Cookie{Name: middleware.RefreshCookieName, Value: refreshCookie})
		request.AddCookie(&http.Cookie{Name: middleware.RefreshCSRFCookieName, Value: refreshCSRF})
		request.Header.Set(middleware.CSRFHeaderName, refreshCSRF)
		response := httptest.NewRecorder()
		newRouter().ServeHTTP(response, request)

		if response.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
		}
	})
}

func TestRecoveryReturnsRedactedErrorEnvelope(t *testing.T) {
	t.Parallel()

	router := gin.New()
	router.Use(
		middleware.RequestID(),
		middleware.Recovery(slog.New(slog.NewJSONHandler(io.Discard, nil))),
	)
	router.GET("/panic", func(*gin.Context) {
		panic("database password must not reach the client")
	})

	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if strings.Contains(response.Body.String(), "database password") {
		t.Fatal("panic response leaked panic value")
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["detail"] != "internal server error" || body["code"] != "internal_error" {
		t.Errorf("error body = %#v", body)
	}
	if body["requestId"] != response.Header().Get("X-Request-ID") {
		t.Errorf("requestId = %q, header = %q", body["requestId"], response.Header().Get("X-Request-ID"))
	}
}

func TestCORSEnforcesCredentialedOriginAllowlist(t *testing.T) {
	t.Parallel()

	handler := New(Handler{AllowedOrigins: []string{
		"https://app.boardly.test",
		"boardly-app://renderer",
	}})

	t.Run("allowed preflight", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodOptions, "/api/v1/tasks", nil)
		request.Header.Set("Origin", "https://app.boardly.test")
		request.Header.Set("Access-Control-Request-Method", http.MethodPost)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
		}
		if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://app.boardly.test" {
			t.Errorf("Access-Control-Allow-Origin = %q", got)
		}
		if got := response.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("Access-Control-Allow-Credentials = %q", got)
		}
		if !strings.Contains(response.Header().Get("Vary"), "Origin") {
			t.Errorf("Vary = %q, want Origin", response.Header().Get("Vary"))
		}
	})

	t.Run("packaged desktop renderer preflight", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodOptions, "/api/v1/tasks", nil)
		request.Header.Set("Origin", "boardly-app://renderer")
		request.Header.Set("Access-Control-Request-Method", http.MethodPost)
		request.Header.Set(
			"Access-Control-Request-Headers",
			"authorization,content-type,idempotency-key,x-boardly-run-token",
		)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
		}
		if got := response.Header().Get("Access-Control-Allow-Origin"); got != "boardly-app://renderer" {
			t.Errorf("Access-Control-Allow-Origin = %q", got)
		}
		if got := response.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("Access-Control-Allow-Credentials = %q", got)
		}
		gotHeaders := response.Header().Get("Access-Control-Allow-Headers")
		for _, want := range []string{"Authorization", "Idempotency-Key", "X-Boardly-Run-Token"} {
			if !strings.Contains(gotHeaders, want) {
				t.Errorf("Access-Control-Allow-Headers = %q, want %s", gotHeaders, want)
			}
		}
	})

	t.Run("disallowed origin", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		request.Header.Set("Origin", "https://evil.example")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
		}
		if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("Access-Control-Allow-Origin = %q, want empty", got)
		}
	})
}

// A missing or `null` desktop Origin is accepted only when the upgrade
// carries no auth cookie — on the generic HTTP surface a literal
// "null" Origin is an opaque sandboxed browser context and must never be
// treated as an allowlisted credentialed origin, even if misconfigured.
func TestCORSRejectsLiteralNullOriginEvenWhenConfigured(t *testing.T) {
	t.Parallel()

	handler := New(Handler{AllowedOrigins: []string{"null", "https://app.boardly.test"}})
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("Origin", "null")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty", got)
	}
}

// Cookie-required Origin versus the no-cookie desktop token path: the
// Client WebSocket transport makes that credential-aware
// Origin decision itself, so the generic CORS middleware must delegate the
// upgrade request instead of rejecting a desktop client without an Origin or
// accepting a browser Origin it cannot judge.
func TestCORSDelegatesClientWebSocketOriginPolicyToTheSocketServer(t *testing.T) {
	t.Parallel()

	handler := New(Handler{
		AllowedOrigins: []string{"https://app.boardly.test"},
		ClientWebSocket: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusUpgradeRequired)
		}),
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/ws", nil)
	request.Header.Set("Origin", "https://evil.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUpgradeRequired {
		t.Fatalf(
			"status = %d, want %d (socket server owns the Origin decision)",
			response.Code,
			http.StatusUpgradeRequired,
		)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty on the socket path", got)
	}
}

func TestReadyzReflectsRequiredDependencyHealth(t *testing.T) {
	t.Parallel()

	t.Run("ready", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		response := httptest.NewRecorder()
		New(
			Handler{Readiness: readinessFunc(func(context.Context) error { return nil })},
		).ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
		}
	})

	t.Run("dependency failure", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		response := httptest.NewRecorder()
		New(Handler{Readiness: readinessFunc(func(context.Context) error {
			return errors.New("database password leaked if returned")
		})}).ServeHTTP(response, request)

		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
		}
		var body struct {
			Detail    string `json:"detail"`
			Code      string `json:"code"`
			RequestID string `json:"requestId"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.Detail != "service is not ready" || body.Code != "service_not_ready" {
			t.Errorf("error body = %#v", body)
		}
		if body.RequestID == "" || body.RequestID != response.Header().Get("X-Request-ID") {
			t.Errorf("requestId = %q, header = %q", body.RequestID, response.Header().Get("X-Request-ID"))
		}
		if strings.Contains(response.Body.String(), "database password") {
			t.Error("response leaked dependency error")
		}
	})
}

func TestRequestIDGeneratesUUIDv7(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	New(Handler{}).ServeHTTP(response, request)

	requestID, err := uuid.Parse(response.Header().Get("X-Request-ID"))
	if err != nil {
		t.Fatalf("X-Request-ID is not a UUID: %v", err)
	}
	if requestID.Version() != 7 {
		t.Errorf("request ID version = %d, want 7", requestID.Version())
	}
}

func TestRequestIDRetainsValidInboundUUID(t *testing.T) {
	t.Parallel()

	want := "0197f590-10b8-7cc0-a6c6-160ed7a18b2c"
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", want)
	response := httptest.NewRecorder()
	New(Handler{}).ServeHTTP(response, request)

	if got := response.Header().Get("X-Request-ID"); got != want {
		t.Errorf("X-Request-ID = %q, want %q", got, want)
	}
}

type readinessFunc func(context.Context) error

func (fn readinessFunc) Ping(ctx context.Context) error {
	return fn(ctx)
}
