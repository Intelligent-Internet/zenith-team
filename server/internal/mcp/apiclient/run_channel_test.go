package apiclient

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/runchannel"
)

const testChannelCapability = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

// fakeDaemonChannel stands in for the daemon's Run channel: it records every
// request document and answers with the server verdict respond returns.
func fakeDaemonChannel(
	t *testing.T,
	respond func(runchannel.Request) (int, string),
) (runchannel.RunControlConfig, *[]runchannel.Request) {
	t.Helper()
	var seen []runchannel.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != runchannel.Path || r.Method != http.MethodPost {
			t.Errorf("channel request = %s %s", r.Method, r.URL.Path)
		}
		want := runchannel.AuthorizationScheme + " " + testChannelCapability
		if got := r.Header.Get("Authorization"); got != want {
			t.Errorf("Authorization = %q, want the Run capability and never a bearer", got)
		}
		if r.Header.Get("X-Boardly-Run-Token") != "" {
			t.Error("boardly-mcp sent a Run token; only the daemon's forward carries the proof")
		}
		var request runchannel.Request
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			t.Errorf("decode channel request: %v", err)
		}
		seen = append(seen, request)
		if request.Path == "/refused" {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(runchannel.ErrorBody{
				SchemaVersion: 1, Code: "run_scope_denied",
				Reason: "This Run's scope does not admit it.",
			})
			return
		}
		status, body := respond(request)
		_ = json.NewEncoder(w).Encode(runchannel.Response{
			SchemaVersion: 1, Status: status, Body: json.RawMessage(body),
		})
	}))
	t.Cleanup(server.Close)
	return runchannel.RunControlConfig{
		SchemaVersion: 1,
		Endpoint:      server.URL,
		Capability:    testChannelCapability,
	}, &seen
}

func TestRunChannelCarriesEveryCallWithoutABearer(t *testing.T) {
	t.Parallel()
	control, seen := fakeDaemonChannel(t, func(request runchannel.Request) (int, string) {
		switch request.Path {
		case "/companies/c1/questions":
			return http.StatusCreated, `{"id":"q1","status":"pending"}`
		case "/companies/c1/agents":
			return http.StatusOK, `{"items":[{"id":"a1"}]}`
		default:
			return http.StatusConflict,
				`{"detail":"The Run has ended.","code":"run_not_active","requestId":"request-id"}`
		}
	})
	channel := NewRunChannel(control)

	var question struct {
		ID string `json:"id"`
	}
	if err := channel.PostRunJSON(
		t.Context(), "/companies/c1/questions", "0198aa00-0000-7000-8000-000000000001",
		map[string]string{"title": "Pick a name"}, &question,
	); err != nil || question.ID != "q1" {
		t.Fatalf("PostRunJSON() = %+v, %v", question, err)
	}
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := channel.FetchRunJSON(t.Context(), "/companies/c1/agents", &page); err != nil ||
		len(page.Items) != 1 {
		t.Fatalf("FetchRunJSON() = %+v, %v", page, err)
	}

	// The server's verdict keeps its envelope.
	err := channel.FetchRunJSON(t.Context(), "/companies/c1/runs/r1", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict ||
		apiErr.Code != "run_not_active" || apiErr.RequestID != "request-id" {
		t.Fatalf("server verdict = %v, want the typed API error", err)
	}
	// The daemon's own refusal is the channel's, never mistaken for the server's.
	err = channel.FetchRunJSON(t.Context(), "/refused", nil)
	var refusal *runchannel.Refusal
	if !errors.As(err, &refusal) || refusal.Code != "run_scope_denied" || errors.As(err, &apiErr) {
		t.Fatalf("channel refusal = %v, want *runchannel.Refusal", err)
	}
	// A mutation without a request key never leaves boardly-mcp.
	if err := channel.PostRunJSON(t.Context(), "/companies/c1/questions", "", nil, nil); err == nil {
		t.Fatal("PostRunJSON() without a request key succeeded")
	}

	requests := *seen
	if len(requests) != 4 {
		t.Fatalf("channel saw %d requests, want 4", len(requests))
	}
	post := requests[0]
	if post.SchemaVersion != 1 || post.Method != http.MethodPost ||
		post.RequestKey != "0198aa00-0000-7000-8000-000000000001" ||
		!strings.Contains(string(post.Body), `"title":"Pick a name"`) {
		t.Fatalf("post request = %+v", post)
	}
	get := requests[1]
	if get.Method != http.MethodGet || get.RequestKey != "" || len(get.Body) != 0 {
		t.Fatalf("get request = %+v", get)
	}
}
