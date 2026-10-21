package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCORSAllowsZenithTeamRequestHeaders(t *testing.T) {
	t.Parallel()

	router := gin.New()
	router.Use(CORS(CORSOptions{AllowedOrigins: []string{"https://app.zenith-team.test"}}))
	router.OPTIONS("/probe", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodOptions, "/probe", nil)
	request.Header.Set("Origin", "https://app.zenith-team.test")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set(
		"Access-Control-Request-Headers",
		"x-zenith-team-client, x-zenith-team-run-token",
	)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	got := response.Header().Get("Access-Control-Allow-Headers")
	if !strings.Contains(got, "X-Zenith-Team-Run-Token") {
		t.Fatalf("Access-Control-Allow-Headers = %q, want X-Zenith-Team-Run-Token", got)
	}
	if !strings.Contains(got, "X-Zenith-Team-Client") {
		t.Fatalf("Access-Control-Allow-Headers = %q, want X-Zenith-Team-Client", got)
	}
}

func TestCORSPreflightCache(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		origin     string
		method     string
		path       string
		wantStatus int
		wantMaxAge string
	}{
		{
			name: "web preflight", origin: "https://app.zenith-team.test",
			method: http.MethodOptions, path: "/probe",
			wantStatus: http.StatusNoContent, wantMaxAge: "600",
		},
		{
			name: "desktop preflight", origin: "zenith-team-app://renderer",
			method: http.MethodOptions, path: "/probe",
			wantStatus: http.StatusNoContent, wantMaxAge: "600",
		},
		{
			name: "unknown origin", origin: "https://unknown.test",
			method: http.MethodOptions, path: "/probe", wantStatus: http.StatusForbidden,
		},
		{
			name: "null origin", origin: "null",
			method: http.MethodOptions, path: "/probe", wantStatus: http.StatusForbidden,
		},
		{
			name: "no origin", method: http.MethodOptions,
			path: "/probe", wantStatus: http.StatusNoContent,
		},
		{
			name: "bypassed path", origin: "zenith-team-app://renderer",
			method: http.MethodOptions, path: "/ws", wantStatus: http.StatusNoContent,
		},
		{
			name: "actual request", origin: "zenith-team-app://renderer",
			method: http.MethodPost, path: "/probe", wantStatus: http.StatusNoContent,
		},
		{
			name: "actual request still checks origin", origin: "https://unknown.test",
			method: http.MethodPost, path: "/probe", wantStatus: http.StatusForbidden,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			router := gin.New()
			router.Use(CORS(CORSOptions{
				AllowedOrigins: []string{"https://app.zenith-team.test", "zenith-team-app://renderer"},
				BypassPaths:    []string{"/ws"},
			}))
			router.Any(test.path, func(c *gin.Context) { c.Status(http.StatusNoContent) })
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Access-Control-Request-Method", http.MethodPost)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if got := response.Header().Get("Access-Control-Max-Age"); got != test.wantMaxAge {
				t.Errorf("Access-Control-Max-Age = %q, want %q", got, test.wantMaxAge)
			}
		})
	}
}
