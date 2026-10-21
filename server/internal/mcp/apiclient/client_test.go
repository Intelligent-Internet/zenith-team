package apiclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/authfile"
)

func TestPostRunJSONSendsRunProofAndReturnsTypedAPIError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/companies/company-id/company-plan/apply" {
			http.NotFound(response, request)
			return
		}
		if got := request.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := request.Header.Get("X-Zenith-Team-Run-Token"); got != "brt_run-proof" {
			t.Fatalf("X-Zenith-Team-Run-Token = %q", got)
		}
		if got := request.Header.Get("Idempotency-Key"); got != "request-key" {
			t.Fatalf("Idempotency-Key = %q", got)
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusConflict)
		_, _ = response.Write(
			[]byte(
				`{"detail":"A Company owner must approve this plan.","code":"approval_required","requestId":"request-id"}`,
			),
		)
	}))
	defer server.Close()

	authPath := filepath.Join(t.TempDir(), "auth.json")
	writeCredential(t, authPath, authfile.Credential{
		APIBaseURL:       server.URL + "/api/v1",
		AccessToken:      "access",
		AccessExpiresAt:  timePointer(t, "2026-07-21T11:00:00Z"),
		RefreshToken:     "refresh",
		RefreshExpiresAt: timePointer(t, "2026-07-28T10:00:00Z"),
	})
	client := newClient(authPath, "brt_run-proof", server.Client(), func() time.Time {
		return time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	})

	err := client.PostRunJSON(
		t.Context(),
		"/companies/company-id/company-plan/apply",
		"request-key",
		map[string]any{"artifactVersion": 1},
		nil,
	)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("PostRunJSON() error = %T %v, want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusConflict || apiErr.Code != "approval_required" ||
		apiErr.Detail != "A Company owner must approve this plan." || apiErr.RequestID != "request-id" {
		t.Fatalf("PostRunJSON() API error = %+v", apiErr)
	}
}

// The daemon's output presenter renews the session it reads from auth.json
// exactly as before; it is the one Run caller that holds a credential.
func TestPostRunJSONRefreshesExpiredAccessTokenAndPersistsRotation(t *testing.T) {
	t.Parallel()
	refreshCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/auth/refresh":
			refreshCalls++
			var body struct {
				Client       string `json:"client"`
				RefreshToken string `json:"refreshToken"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode refresh request: %v", err)
			}
			if body.Client != "desktop" || body.RefreshToken != "old-refresh" {
				t.Fatalf("unexpected refresh request: %+v", body)
			}
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{
  "user": {"id":"user","email":"user@example.com","displayName":"User","avatarUrl":null,"revision":1,"createdAt":"2026-07-21T09:00:00Z"},
  "session": {"transport":"bearer","accessToken":"new-access","refreshToken":"new-refresh","accessExpiresAt":"2026-07-21T11:00:00Z","refreshExpiresAt":"2026-07-28T10:00:00Z"}
}`))
		case "/api/v1/runs/run-id/output":
			if authorization := request.Header.Get("Authorization"); authorization != "Bearer new-access" {
				t.Fatalf("Authorization = %q", authorization)
			}
			if proof := request.Header.Get("X-Zenith-Team-Run-Token"); proof != "brt_run-proof" {
				t.Fatalf("X-Zenith-Team-Run-Token = %q", proof)
			}
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"outputId":"output-id"}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	authPath := filepath.Join(t.TempDir(), "auth.json")
	writeCredential(t, authPath, authfile.Credential{
		APIBaseURL:       server.URL + "/api/v1",
		AccessToken:      "old-access",
		AccessExpiresAt:  timePointer(t, "2026-07-21T09:00:00Z"),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: timePointer(t, "2026-07-28T10:00:00Z"),
	})
	client := newClient(authPath, "brt_run-proof", server.Client(), func() time.Time {
		return time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	})

	var output struct {
		OutputID string `json:"outputId"`
	}
	if err := client.PostRunJSON(
		t.Context(), "/runs/run-id/output", "request-key", map[string]any{}, &output,
	); err != nil {
		t.Fatalf("PostRunJSON() error = %v", err)
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls)
	}
	stored, err := authfile.Load(authPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if stored.AccessToken != "new-access" || stored.RefreshToken != "new-refresh" {
		t.Fatalf("stored rotation = access %q refresh %q", stored.AccessToken, stored.RefreshToken)
	}
}

func TestNewUsesSizeAppropriateAttachmentUploadBudget(t *testing.T) {
	t.Parallel()

	client := New("auth.json", "brt_run-proof")
	if client.httpClient.Timeout != 20*time.Second {
		t.Fatalf("JSON client timeout = %v, want 20s", client.httpClient.Timeout)
	}
	if client.attachmentClient == nil || client.attachmentClient == client.httpClient {
		t.Fatal("attachment client must be distinct from the JSON client")
	}
	if client.attachmentClient.Timeout != 0 {
		t.Fatalf(
			"attachment HTTP client timeout = %v, want request-context budget",
			client.attachmentClient.Timeout,
		)
	}
	if got := attachmentUploadTimeout(100 << 20); got < 15*time.Minute {
		t.Fatalf("100 MiB attachment upload timeout = %v, want at least 15m", got)
	}
}

// The presenter's upload start and completion both carry the Run proof, so
// the server checks the session is the Run's authorizer and records the
// upload as the Run's.
func TestUploadAttachmentUsesFreshSizeAppropriateCompletionBudget(t *testing.T) {
	t.Parallel()

	var uploadDeadline, completionDeadline time.Time
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		deadline, ok := request.Context().Deadline()
		if !ok {
			t.Fatalf("%s has no request deadline", request.URL.Path)
		}
		if proof := request.Header.Get("X-Zenith-Team-Run-Token"); proof != "brt_run-proof" {
			t.Fatalf("%s X-Zenith-Team-Run-Token = %q, want the Run proof", request.URL.Path, proof)
		}
		if _, err := io.Copy(io.Discard, request.Body); err != nil {
			t.Fatalf("read %s body: %v", request.URL.Path, err)
		}
		body := `{"id":"attachment-id"}`
		switch request.URL.Path {
		case "/api/v1/attachments/initiate":
			uploadDeadline = deadline
		case "/api/v1/attachments/attachment-id/complete":
			completionDeadline = deadline
		default:
			t.Fatalf("unexpected request path %q", request.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(body)),
			Request:    request,
		}, nil
	})
	authPath := filepath.Join(t.TempDir(), "auth.json")
	writeCredential(t, authPath, authfile.Credential{
		APIBaseURL:       "http://localhost/api/v1",
		AccessToken:      "access",
		AccessExpiresAt:  timePointer(t, "2026-07-21T11:00:00Z"),
		RefreshToken:     "refresh",
		RefreshExpiresAt: timePointer(t, "2026-07-28T10:00:00Z"),
	})
	client := newClient(
		authPath,
		"brt_run-proof",
		&http.Client{Transport: transport, Timeout: 20 * time.Second},
		func() time.Time { return time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC) },
	)

	attachmentID, err := client.UploadAttachment(t.Context(), AttachmentUpload{
		RequestKey: "request-key", CompanyID: "company-id", Filename: "result.zip",
		ContentType: "application/zip", SizeBytes: 4,
	}, bytes.NewReader([]byte("data")))
	if err != nil {
		t.Fatalf("UploadAttachment() error = %v", err)
	}
	if attachmentID != "attachment-id" {
		t.Fatalf("UploadAttachment() = %q, want attachment-id", attachmentID)
	}
	if uploadDeadline.IsZero() || completionDeadline.IsZero() ||
		!completionDeadline.After(uploadDeadline) {
		t.Fatalf(
			"request deadlines = upload %v completion %v, want a fresh completion budget",
			uploadDeadline,
			completionDeadline,
		)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

var _ http.RoundTripper = roundTripFunc(nil)

func writeCredential(t *testing.T, path string, credential authfile.Credential) {
	t.Helper()
	if err := authfile.Save(path, credential); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
}

func timePointer(t *testing.T, value string) *time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("time.Parse() error = %v", err)
	}
	return &parsed
}

func TestUploadAttachmentRefusesWithoutTheRunProof(t *testing.T) {
	t.Parallel()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("an upload without the Run proof reached %s", request.URL.Path)
		return nil, errors.New("unreachable")
	})
	client := newClient("auth.json", "", &http.Client{Transport: transport}, time.Now)
	if _, err := client.UploadAttachment(t.Context(), AttachmentUpload{
		RequestKey: "request-key", CompanyID: "company-id", Filename: "result.zip",
		ContentType: "application/zip", SizeBytes: 4,
	}, bytes.NewReader([]byte("data"))); err == nil {
		t.Fatal("UploadAttachment() without the Run proof succeeded")
	}
}
