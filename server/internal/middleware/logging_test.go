package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

// statusLog is an HTTPObserver that keeps the statuses it was handed.
type statusLog []int

func (s *statusLog) ObserveHTTP(_, _ string, status int, _ time.Duration) {
	*s = append(*s, status)
}

// newObservedRouter mounts handle behind the global chain handler.New installs
// (RequestID through Recovery), once on a plain route and once behind
// RequestTimeout the way the discussion, huddle and studio groups mount it.
func newObservedRouter(logs *bytes.Buffer, metrics *statusLog, handle gin.HandlerFunc) *gin.Engine {
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	router := gin.New()
	router.Use(RequestID(), ClientMetadata(), HTTPMetrics(metrics), RequestLogger(logger, nil), Recovery(logger))
	router.GET("/plain", handle)
	router.Group("/bounded", RequestTimeout(time.Minute)).GET("", handle)
	return router
}

func logLines(t *testing.T, logs *bytes.Buffer) map[string]map[string]any {
	t.Helper()
	lines := map[string]map[string]any{}
	for _, raw := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var line map[string]any
		if err := json.Unmarshal(raw, &line); err != nil {
			t.Fatalf("log line %q is not JSON: %v", raw, err)
		}
		lines[line["msg"].(string)] = line
	}
	return lines
}

func writeInternalError(c *gin.Context) {
	util.WriteError(c, http.StatusInternalServerError, "internal server error", "internal_error")
}

func TestRequestLogAndMetricsRecordClientClosedFailuresAs499(t *testing.T) {
	t.Parallel()

	// A client that goes away cancels the request context; pgx notices it and
	// fails the query with context.Canceled, which the handler maps to 500.
	clientGoneThen := func(write func(*gin.Context)) func(*gin.Context, context.CancelFunc) {
		return func(c *gin.Context, cancelRoot context.CancelFunc) {
			cancelRoot()
			<-c.Request.Context().Done()
			write(c)
		}
	}
	liveClient := func(c *gin.Context, _ context.CancelFunc) { writeInternalError(c) }

	tests := []struct {
		name string
		path string
		// expiredRoot gives the request context a deadline already in the
		// past instead of a cancel the handler can pull.
		expiredRoot bool
		handle      func(c *gin.Context, cancelRoot context.CancelFunc)
		wantLevel   string
		wantStatus  int
		wantClosed  bool
		wantPanic   bool
	}{
		{
			name:       "client gone before a 500",
			path:       "/plain",
			handle:     clientGoneThen(writeInternalError),
			wantLevel:  "WARN",
			wantStatus: 499,
			wantClosed: true,
		},
		{
			name:       "client gone before a 500 on a timeout route",
			path:       "/bounded",
			handle:     clientGoneThen(writeInternalError),
			wantLevel:  "WARN",
			wantStatus: 499,
			wantClosed: true,
		},
		{
			name:       "client gone before a 4xx keeps its status",
			path:       "/plain",
			handle:     clientGoneThen(func(c *gin.Context) { c.Status(http.StatusNotFound) }),
			wantLevel:  "WARN",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "live client gets a 500",
			path:       "/plain",
			handle:     liveClient,
			wantLevel:  "ERROR",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "live client gets a 500 on a timeout route",
			path:       "/bounded",
			handle:     liveClient,
			wantLevel:  "ERROR",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "panic on a timeout route",
			path:       "/bounded",
			handle:     func(*gin.Context, context.CancelFunc) { panic("boom") },
			wantLevel:  "ERROR",
			wantStatus: http.StatusInternalServerError,
			wantPanic:  true,
		},
		{
			name:        "request deadline passed is not a client close",
			path:        "/plain",
			expiredRoot: true,
			handle:      liveClient,
			wantLevel:   "ERROR",
			wantStatus:  http.StatusInternalServerError,
		},
		{
			name:        "request deadline passed on a timeout route",
			path:        "/bounded",
			expiredRoot: true,
			handle:      liveClient,
			wantLevel:   "ERROR",
			wantStatus:  http.StatusInternalServerError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var root context.Context
			var cancelRoot context.CancelFunc
			if tt.expiredRoot {
				root, cancelRoot = context.WithTimeout(context.Background(), -time.Second)
			} else {
				root, cancelRoot = context.WithCancel(context.Background())
			}
			t.Cleanup(cancelRoot)

			var logs bytes.Buffer
			var metrics statusLog
			router := newObservedRouter(&logs, &metrics, func(c *gin.Context) {
				tt.handle(c, cancelRoot)
			})
			request := httptest.NewRequestWithContext(root, http.MethodGet, tt.path, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if tt.wantClosed && response.Code != http.StatusInternalServerError {
				t.Fatalf("response status = %d, want the handler's 500 untouched", response.Code)
			}
			if !slices.Equal(metrics, statusLog{tt.wantStatus}) {
				t.Fatalf("metric statuses = %v, want [%d]", metrics, tt.wantStatus)
			}
			lines := logLines(t, &logs)
			line, ok := lines["HTTP request"]
			if !ok {
				t.Fatalf("no request log line in %v", lines)
			}
			if line["level"] != tt.wantLevel {
				t.Fatalf("level = %v, want %s", line["level"], tt.wantLevel)
			}
			if line["status"] != float64(tt.wantStatus) {
				t.Fatalf("status = %v, want %d", line["status"], tt.wantStatus)
			}
			if closed, has := line["client_closed"]; tt.wantClosed != has || (has && closed != true) {
				t.Fatalf("client_closed = %v (present %t), want present %t", closed, has, tt.wantClosed)
			}
			if panicLine, has := lines["recovered HTTP panic"]; has != tt.wantPanic ||
				(has && panicLine["level"] != "ERROR") {
				t.Fatalf("recovered panic line = %v, want present %t at ERROR", panicLine, tt.wantPanic)
			}
		})
	}
}

// refusingAuthenticator refuses every credential the way an ended session is
// refused.
type refusingAuthenticator struct{ auth.AuthService }

func (refusingAuthenticator) Authenticate(context.Context, string, auth.TokenKind) (auth.UserPrincipal, error) {
	return auth.UserPrincipal{}, auth.ErrInvalidSession
}

// A client stuck on 401s has to be traceable from the request log alone: who
// it is (address, user agent, self-reported client and daemon id) and why
// it was refused, with nothing of the credential it sent.
func TestRequestLogAttributesARefusedCallerWithoutItsCredential(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		authorization string
		wantFailure   string
	}{
		{name: "no credential", wantFailure: "access_token_missing"},
		{name: "not a bearer", authorization: "Basic c2VjcmV0LXRva2Vu", wantFailure: "access_token_malformed"},
		{name: "refused bearer", authorization: "Bearer secret-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			router := gin.New()
			router.Use(RequestID(), ClientMetadata(),
				RequestLogger(logger, []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}))
			router.GET(
				"/api/v1/daemon/ws",
				RequireDaemonAuth(refusingAuthenticator{}),
				func(c *gin.Context) {
					c.Status(http.StatusNoContent)
				},
			)

			request := httptest.NewRequest(http.MethodGet, "/api/v1/daemon/ws", nil)
			request.RemoteAddr = "10.1.4.2:51234"
			request.Header.Set("X-Forwarded-For", "198.51.100.7, 203.0.113.9")
			request.Header.Set("User-Agent", "Go-http-client/1.1")
			request.Header.Set(HeaderClientPlatform, "daemon")
			request.Header.Set(HeaderClientVersion, "0.2.47")
			request.Header.Set(HeaderClientOS, "macos")
			request.Header.Set(HeaderDaemonID, "6b0f3c55-6a3e-4f43-9a51-0c8f6d2f1b7e")
			if tt.authorization != "" {
				request.Header.Set("Authorization", tt.authorization)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", response.Code)
			}
			line, ok := logLines(t, &logs)["HTTP request"]
			if !ok {
				t.Fatalf("no request log line in %s", logs.String())
			}
			want := map[string]any{
				"client_ip":       "203.0.113.9",
				"user_agent":      "Go-http-client/1.1",
				"client_platform": "daemon",
				"client_version":  "0.2.47",
				"client_os":       "macos",
				"daemon_id":       "6b0f3c55-6a3e-4f43-9a51-0c8f6d2f1b7e",
			}
			for key, value := range want {
				if line[key] != value {
					t.Errorf("%s = %v, want %v", key, line[key], value)
				}
			}
			if failure, has := line["auth_failure"]; tt.wantFailure != "" && failure != tt.wantFailure ||
				tt.wantFailure == "" && has {
				t.Errorf("auth_failure = %v (present %t), want %q", failure, has, tt.wantFailure)
			}
			if strings.Contains(logs.String(), "secret-token") ||
				strings.Contains(logs.String(), "c2VjcmV0") {
				t.Fatalf("request log carries the credential: %s", logs.String())
			}
		})
	}
}

func TestClientIPTrustsForwardedForOnlyFromTrustedProxies(t *testing.T) {
	t.Parallel()

	trusted := []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16"), netip.MustParsePrefix("fd00::/8")}
	tests := []struct {
		name       string
		remoteAddr string
		forwarded  []string
		want       string
	}{
		{name: "direct caller", remoteAddr: "203.0.113.9:443", want: "203.0.113.9"},
		{
			name:       "untrusted peer cannot claim an address",
			remoteAddr: "203.0.113.9:443", forwarded: []string{"198.51.100.7"},
			want: "203.0.113.9",
		},
		{
			name:       "the hop the proxy appended wins over what the client sent",
			remoteAddr: "10.1.4.2:51234", forwarded: []string{"198.51.100.7, 203.0.113.9"},
			want: "203.0.113.9",
		},
		{
			name:       "chained proxies and repeated headers",
			remoteAddr: "10.1.4.2:51234", forwarded: []string{"198.51.100.7", "203.0.113.9, 10.1.9.9"},
			want: "203.0.113.9",
		},
		{
			name:       "a garbled hop stops the walk at the last vouched address",
			remoteAddr: "10.1.4.2:51234", forwarded: []string{"203.0.113.9, not-an-ip"},
			want: "10.1.4.2",
		},
		{name: "trusted proxy with no header", remoteAddr: "10.1.4.2:51234", want: "10.1.4.2"},
		{
			name:       "IPv6 proxy",
			remoteAddr: "[fd00::1]:443", forwarded: []string{"2001:db8::7"},
			want: "2001:db8::7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.RemoteAddr = tt.remoteAddr
			for _, value := range tt.forwarded {
				request.Header.Add("X-Forwarded-For", value)
			}
			if got := clientIP(request, trusted); got != tt.want {
				t.Fatalf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
