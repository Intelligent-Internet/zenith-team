package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOutputChannelPresentsThroughBoundRun(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"file", "app"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			input := PresentOutputInput{Type: kind, Title: "Result", Summary: "Completed"}
			if kind == "app" {
				input.App = &PresentOutputApp{Commands: []PresentOutputCommand{{Command: "npm", Args: []string{"run", "dev"}, Port: 5173}}}
			} else {
				input.Items = []PresentOutputItem{{Role: "primary", Label: "Report", Target: PresentOutputTarget{Kind: "workspace_file", WorkspaceRelativePath: "reports/result.md"}}}
			}
			runID := "0198aa00-0000-7000-8000-00000000ff02"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/run/api" || r.Header.Get("Authorization") != "ZenithRun capability" {
					t.Errorf("unexpected channel request")
				}
				var request struct {
					Method, Path, RequestKey string
					Body                     PresentOutputInput
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Method != "POST" || request.Path != "/runs/"+runID+"/output" || request.RequestKey == "" || request.Body.Type != kind {
					t.Errorf("request = %+v", request)
				}
				if kind == "app" && request.Body.App.Commands[0].Port != 5173 {
					t.Error("app command lost")
				}
				if kind == "file" && request.Body.Items[0].Target.WorkspaceRelativePath != "reports/result.md" {
					t.Error("workspace path lost")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"schemaVersion":1,"status":201,"body":{"outputId":"0198aa00-0000-7000-8000-00000000ff01"}}`))
			}))
			defer server.Close()
			service, err := NewOutputChannelService(server.URL, "capability", runID)
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.PresentOutput(t.Context(), input)
			if err != nil || result.OutputID != "0198aa00-0000-7000-8000-00000000ff01" {
				t.Fatalf("result = %+v, %v", result, err)
			}
		})
	}
}

func TestOutputChannelRefusesUnavailableAndInvalidResponses(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"schemaVersion":1,"status":403,"body":{"code":"run_scope_denied","detail":"Run ended"}}`,
		`{"schemaVersion":2,"status":201,"body":{"outputId":"0198aa00-0000-7000-8000-00000000ff01"}}`,
		`{"schemaVersion":1,"status":201,"body":{"outputId":"invalid"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			service, err := NewOutputChannelService(server.URL, "capability", "0198aa00-0000-7000-8000-00000000ff02")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.PresentOutput(t.Context(), PresentOutputInput{}); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
	for _, endpoint := range []string{"https://example.com", "http://localhost:1234", "http://127.0.0.1:1234/path", "http://user:pass@127.0.0.1:1234"} {
		if _, err := NewOutputChannelService(endpoint, "capability", "0198aa00-0000-7000-8000-00000000ff02"); err == nil {
			t.Errorf("accepted endpoint %s", endpoint)
		}
	}
}
