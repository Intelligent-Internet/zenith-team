package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/telemetry"
)

type crashReportServiceStub struct {
	submit func(context.Context, telemetry.Submission) error
	calls  int
}

func (s *crashReportServiceStub) Submit(ctx context.Context, submission telemetry.Submission) error {
	s.calls++
	if s.submit == nil {
		return errors.New("unexpected Submit call")
	}
	return s.submit(ctx, submission)
}

func crashReportHTTPHandler(userID uuid.UUID, service CrashReportService) http.Handler {
	return New(Handler{
		AuthService:        authenticatedAuthStub(userID),
		CrashReportService: service,
	})
}

func crashReportBodyFixture(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"kind":         "crash",
		"platform":     "darwin",
		"architecture": "arm64",
		"appVersion":   "0.1.0",
		"reason":       "TypeError: boom",
	})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return body
}

func TestCrashReportRouteAcceptsAuthenticatedSubmission(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	service := &crashReportServiceStub{submit: func(_ context.Context, submission telemetry.Submission) error {
		want := telemetry.Submission{
			Type:         telemetry.ReportTypeCrash,
			Platform:     "darwin",
			Architecture: "arm64",
			AppVersion:   "0.1.0",
			Reason:       "TypeError: boom",
		}
		if submission.Type != want.Type || submission.Platform != want.Platform ||
			submission.Architecture != want.Architecture || submission.AppVersion != want.AppVersion ||
			submission.Reason != want.Reason {
			t.Fatalf("Submit input = %+v", submission)
		}
		return nil
	}}
	response := serveBearerJSON(crashReportHTTPHandler(userID, service),
		http.MethodPost, "/api/v1/crash-reports", crashReportBodyFixture(t))
	if response.Code != http.StatusAccepted || response.Body.Len() != 0 {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if service.calls != 1 {
		t.Fatalf("Submit calls = %d", service.calls)
	}
}

func TestCrashReportRouteRequiresAuthentication(t *testing.T) {
	t.Parallel()
	service := &crashReportServiceStub{}
	handler := New(Handler{
		AuthService: &authServiceStub{
			authenticate: func(context.Context, string, auth.TokenKind) (auth.UserPrincipal, error) {
				return auth.UserPrincipal{}, auth.ErrInvalidSession
			},
		},
		CrashReportService: service,
	})
	response := serveBearerJSON(handler, http.MethodPost, "/api/v1/crash-reports", crashReportBodyFixture(t))
	assertErrorResponse(t, response, http.StatusUnauthorized, "session_invalid")
	if service.calls != 0 {
		t.Fatalf("unauthenticated submission reached the service (%d calls)", service.calls)
	}
}

func TestCrashReportRouteRejectsMalformedBodiesBeforeService(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	service := &crashReportServiceStub{}
	handler := crashReportHTTPHandler(userID, service)

	for name, body := range map[string]string{
		"empty":         "",
		"not an object": `["crash"]`,
		"unknown field": `{"kind":"crash","platform":"darwin","architecture":"arm64","appVersion":"0.1.0","reason":"x","email":"a@b.co"}`,
		"wrong shape":   `{"kind":1,"platform":"darwin","architecture":"arm64","appVersion":"0.1.0","reason":"x"}`,
		"retired clock": `{"kind":"crash","platform":"darwin","architecture":"arm64","appVersion":"0.1.0","reason":"x","createdAt":"2026-07-14T11:59:00Z"}`,
	} {
		response := serveBearerJSON(handler, http.MethodPost, "/api/v1/crash-reports", []byte(body))
		assertErrorResponse(t, response, http.StatusBadRequest, "invalid_request")
		if service.calls != 0 {
			t.Fatalf("%s body reached the service", name)
		}
	}
}

func TestCrashReportRouteBoundsBodySize(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	service := &crashReportServiceStub{}
	oversize := []byte(
		`{"kind":"crash","platform":"darwin","architecture":"arm64","appVersion":"0.1.0","reason":"` +
			strings.Repeat(
				"a",
				maxCrashReportBodyBytes,
			) + `"}`,
	)
	response := serveBearerJSON(crashReportHTTPHandler(userID, service),
		http.MethodPost, "/api/v1/crash-reports", oversize)
	assertErrorResponse(t, response, http.StatusRequestEntityTooLarge, "request_too_large")
	if service.calls != 0 {
		t.Fatal("oversize body reached the service")
	}
}

func TestCrashReportRouteMapsServiceErrorsWithoutDisclosure(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"invalid input": {telemetry.ErrInvalidInput, http.StatusUnprocessableEntity, "validation_failed"},
		"store failure": {telemetry.ErrStore, http.StatusInternalServerError, "internal_error"},
	} {
		service := &crashReportServiceStub{submit: func(context.Context, telemetry.Submission) error {
			return tc.err
		}}
		response := serveBearerJSON(crashReportHTTPHandler(userID, service),
			http.MethodPost, "/api/v1/crash-reports", crashReportBodyFixture(t))
		assertErrorResponse(t, response, tc.status, tc.code)
		if strings.Contains(response.Body.String(), "crash report") {
			t.Fatalf("%s response disclosed internals: %s", name, response.Body.String())
		}
	}
}

func TestCrashReportRouteRequiresJSONMediaType(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	service := &crashReportServiceStub{}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/crash-reports",
		bytes.NewReader(crashReportBodyFixture(t)))
	request.Header.Set("Authorization", "Bearer access-token")
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	crashReportHTTPHandler(userID, service).ServeHTTP(response, request)
	assertErrorResponse(t, response, http.StatusUnsupportedMediaType, "unsupported_media_type")
	if service.calls != 0 {
		t.Fatal("non-JSON body reached the service")
	}
}
