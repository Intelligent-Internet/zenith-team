package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"boardly.dev/boardly/server/pkg/protocol"
)

func TestRuntimeModelDiscoveryDispatchesFromHTTPAndWSHeartbeatAndRetriesReport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	for _, transport := range []string{"http", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			dir := t.TempDir()
			argsPath := filepath.Join(dir, "args")
			executablePath := filepath.Join(dir, "exact-codex")
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsPath + "\"\n" +
				"printf '%s' '{\"models\":[{\"slug\":\"gpt-visible\",\"display_name\":\"GPT Visible\",\"visibility\":\"list\",\"supported_reasoning_levels\":[{\"effort\":\"high\"}]}]}'\n"
			if err := os.WriteFile(executablePath, []byte(script), 0o700); err != nil {
				t.Fatalf("write fixture: %v", err)
			}

			const runtimeID = "019fa834-a515-79b2-b1bd-19e6bd4f275e"
			const requestID = "019fa834-a515-79b2-b1bd-19e6bd4f2761"
			var reports atomic.Int32
			reported := make(chan ModelDiscoveryResult, 1)
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					switch request.URL.Path {
					case "/api/v1/daemon/heartbeat":
						json.NewEncoder(w).Encode(protocol.DaemonHeartbeatAckPayload{
							RuntimeID: runtimeID,
							Status:    "ok",
							ServerCapabilities: []string{
								protocol.DaemonCapabilityModelDiscoveryV1,
							},
							PendingModelList: &protocol.DaemonHeartbeatPendingModelList{
								RequestID: requestID,
							},
						})
					case "/api/v1/daemon/runtimes/" + runtimeID + "/model-discoveries/" + requestID + "/result":
						var result ModelDiscoveryResult
						if err := json.NewDecoder(request.Body).Decode(&result); err != nil {
							t.Errorf("decode report: %v", err)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						if reports.Add(1) == 1 {
							w.WriteHeader(http.StatusServiceUnavailable)
							return
						}
						reported <- result
						json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				}),
			)
			defer server.Close()

			client := NewClient(server.URL)
			client.SetToken("daemon-token")
			daemon := &Daemon{
				client: client,
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				runtimeIndex: map[string]Runtime{
					runtimeID: {ID: runtimeID, Provider: "codex", Status: "online"},
				},
				wsHBLastAck: make(map[string]time.Time),
				rootCtx:     context.Background(),
			}
			agents := map[string]AgentEntry{"codex": {Path: executablePath, Command: "ignored"}}
			daemon.agentsAvailable.Store(&agents)

			if transport == "http" {
				if transient := daemon.runHeartbeatTick(context.Background(), runtimeID); transient {
					t.Fatal("heartbeat unexpectedly reported a transient failure")
				}
			} else {
				daemon.handleWSHeartbeatAckForConnection(&HeartbeatResponse{
					RuntimeID: runtimeID,
					Status:    "ok",
					ServerCapabilities: []string{
						protocol.DaemonCapabilityModelDiscoveryV1,
					},
					PendingModelList: &protocol.DaemonHeartbeatPendingModelList{
						RequestID: requestID,
					},
				}, 1)
			}

			select {
			case result := <-reported:
				if result.Status != "completed" || len(result.Models) != 1 ||
					result.Models[0].ID != "gpt-visible" {
					t.Fatalf("reported result = %+v", result)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("model discovery report was not delivered")
			}
			rawArgs, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatalf("read exact executable argv: %v", err)
			}
			if got := strings.Fields(string(rawArgs)); strings.Join(got, " ") != "debug models --bundled" {
				t.Fatalf("argv = %q", got)
			}
			if reports.Load() != 2 {
				t.Fatalf("report attempts = %d, want 2", reports.Load())
			}
		})
	}
}
