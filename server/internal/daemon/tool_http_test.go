package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalToolOperationsRequireExactRuntimeAndLiveCloudAccess(t *testing.T) {
	t.Parallel()
	runtime := Runtime{
		ID:        "11111111-1111-4111-8111-111111111111",
		CompanyID: "22222222-2222-4222-8222-222222222222",
		Provider:  "codex",
	}
	for _, test := range []struct {
		name, token, company, provider, tool, daemon string
		status                                       int
		agent                                        string
	}{
		{"missing Agent", "private", runtime.CompanyID, "codex", "zenith-studio", "this-machine", 400, ""},
		{"unbound Agent", "private", runtime.CompanyID, "codex", "zenith-studio", "this-machine", 404, "55555555-5555-4555-8555-555555555555"},
		{"valid", "private", runtime.CompanyID, "codex", "zenith-studio", "this-machine", 200, testToolAgentID},
		{"local renderer has no sign-in", "private", runtime.CompanyID, "codex", "hyperframes", "this-machine", 400, testToolAgentID},
		{"missing capability", "", runtime.CompanyID, "codex", "zenith-studio", "this-machine", 401, testToolAgentID},
		{"other company", "private", "33333333-3333-4333-8333-333333333333", "codex", "zenith-studio", "this-machine", 404, testToolAgentID},
		{"other provider", "private", runtime.CompanyID, "claude", "zenith-studio", "this-machine", 404, testToolAgentID},
		{"unreviewed package", "private", runtime.CompanyID, "codex", "arbitrary-npm", "this-machine", 404, testToolAgentID},
		{"cloud binding changed", "private", runtime.CompanyID, "codex", "zenith-studio", "other-machine", 404, testToolAgentID},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			// The daemon's own session reads its runtime and bindings on the
			// daemon plane only (D31); a member route would refuse it.
			cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/daemon/runtimes/"+runtime.ID+"/agent-bindings" {
					t.Errorf("unexpected cloud path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"runtime": map[string]string{
						"id":        runtime.ID,
						"companyId": runtime.CompanyID,
						"provider":  runtime.Provider,
						"daemonId":  test.daemon,
					},
					"items": []any{
						map[string]any{
							"agentId": testToolAgentID,
							"runtime": map[string]string{"id": runtime.ID},
						},
					},
				})
			}))
			defer cloud.Close()
			service := newLocalToolService(t.TempDir())
			service.started = true
			d := &Daemon{
				cfg:               Config{DaemonID: "this-machine"},
				client:            NewClient(cloud.URL),
				localPreviewToken: "private",
				tools:             service,
				runtimeIndex:      map[string]Runtime{runtime.ID: runtime},
			}
			body, err := json.Marshal(
				localToolRequest{
					SchemaVersion: 1,
					CompanyID:     test.company,
					RuntimeID:     runtime.ID,
					AgentID:       test.agent,
					Provider:      test.provider,
					ToolKey:       test.tool,
					Action:        "connect",
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(
				http.MethodPost,
				"/v1/tools/operate",
				strings.NewReader(string(body)),
			)
			request.Header.Set("Authorization", "Bearer "+test.token)
			response := httptest.NewRecorder()
			d.localToolHandler().ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if (len(service.pending) == 1) != (test.status == 200) {
				t.Fatal("operation queued outside authorized scope")
			}
		})
	}
}
