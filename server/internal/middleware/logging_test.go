package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/util"
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
	router.Use(RequestID(), ClientMetadata(), HTTPMetrics(metrics), RequestLogger(logger), Recovery(logger))
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
