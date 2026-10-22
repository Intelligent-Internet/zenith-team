package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
)

type publicCaptureService struct {
	canvas.PresentationService
	token uuid.UUID
	html  string
}

func (s publicCaptureService) PublicPresentationHTML(_ context.Context, token uuid.UUID) (string, error) {
	if token != s.token {
		return "", canvas.ErrNotFound
	}
	return s.html, nil
}

func TestPublicCaptureHeadersAndRevocation(t *testing.T) {
	t.Parallel()
	token := uuid.New()
	h := &Handler{
		PresentationService: publicCaptureService{token: token, html: "<!doctype html><p>Retained value</p>"},
	}
	router := gin.New()
	router.GET("/public/captures/:token", h.publicPresentation)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/public/captures/"+token.String(), nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Retained value") {
		t.Fatalf("public response: %d %s", recorder.Code, recorder.Body.String())
	}
	for name, want := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff", "X-Robots-Tag": "noindex, nofollow, noarchive", "Content-Type": "text/html; charset=utf-8"} {
		if got := recorder.Header().Get(name); got != want {
			t.Errorf("%s=%q want %q", name, got, want)
		}
	}
	if csp := recorder.Header().
		Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") ||
		!strings.Contains(csp, "sandbox") {
		t.Fatalf("unsafe CSP: %s", csp)
	}
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/public/captures/"+uuid.NewString(), nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unavailable token: %d", recorder.Code)
	}
}
