package daemon

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// readHealthBody serves one /health request against the handler and returns
// the decoded body together with the raw JSON, so tests can assert both on
// the Go shape and on the wire keys the desktop supervisor reads.
func readHealthBody(t *testing.T, d *Daemon) (HealthResponse, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	d.healthHandler(time.Now())(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	var health HealthResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	return health, recorder.Body.String()
}

func newHealthTestDaemon(agents map[string]AgentEntry) *Daemon {
	d := &Daemon{
		cfg:           Config{CLIVersion: "0.9.2"},
		logger:        testLogger(),
		agentVersions: make(map[string]string),
		skippedAgents: make(map[string]string),
	}
	d.agentsAvailable.Store(&agents)
	return d
}

func TestHealthIdentifiesTheRunningBuildAfterBinaryReplacement(t *testing.T) {
	d, binary, _ := newSelfReloadTestDaemon(t, "0.1.8")
	agents := map[string]AgentEntry{}
	d.agentsAvailable.Store(&agents)
	want, err := executableDigest(binary)
	if err != nil {
		t.Fatal(err)
	}
	replaceBinary(t, binary)
	_, raw := readHealthBody(t, d)
	var health map[string]any
	if err := json.Unmarshal([]byte(raw), &health); err != nil {
		t.Fatal(err)
	}
	if health["cliDigest"] != want {
		t.Fatalf("health build digest = %v, want the startup digest %q", health["cliDigest"], want)
	}
	onDisk, err := executableDigest(binary)
	if err != nil {
		t.Fatal(err)
	}
	if health["cliDigest"] == onDisk {
		t.Fatal("health identifies the replacement instead of the running build")
	}
}

func TestHealthServesAppPreviewControlRoutes(t *testing.T) {
	t.Parallel()
	d := newHealthTestDaemon(nil)
	d.localPreviewToken = "preview-route-test"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop := d.startHealthServer(listener, time.Now())
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Errorf("stop health server: %v", err)
		}
	})
	for _, operation := range []string{"inspect", "attach", "ensure", "renew", "release", "restart", "stop"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			for _, authorized := range []bool{false, true} {
				request, err := http.NewRequestWithContext(
					t.Context(),
					http.MethodPost,
					"http://"+listener.Addr().
						String()+
						"/v2/app-previews/"+operation,
					strings.NewReader(`{}`),
				)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", "application/json")
				want := http.StatusUnauthorized
				if authorized {
					request.Header.Set("Authorization", "Bearer preview-route-test")
					want = http.StatusBadRequest
				}
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				if response.StatusCode != want {
					t.Fatalf(
						"authorized=%v: status = %d, want %d",
						authorized,
						response.StatusCode,
						want,
					)
				}
			}
		})
	}
}

// The desktop daemon panel lists every detected provider with its CLI
// version; /health is the only local source for that, so it carries
// `agentVersions` next to `agents`.
func TestHealthListsDetectedProviderVersions(t *testing.T) {
	d := newHealthTestDaemon(map[string]AgentEntry{
		"claude": {Path: "/bin/claude", Command: "claude"},
		"codex":  {Path: "/bin/codex", Command: "codex"},
	})
	d.setAgentVersion("claude", "2.1.0")
	d.setAgentVersion("codex", "0.150.0")

	health, raw := readHealthBody(t, d)
	if !strings.Contains(raw, `"agentVersions"`) {
		t.Fatalf("health body lacks the agentVersions key: %s", raw)
	}
	want := map[string]string{"claude": "2.1.0", "codex": "0.150.0"}
	if len(health.AgentVersions) != len(want) {
		t.Fatalf("agentVersions = %v, want %v", health.AgentVersions, want)
	}
	for provider, version := range want {
		if got := health.AgentVersions[provider]; got != version {
			t.Fatalf("agentVersions[%s] = %q, want %q", provider, got, version)
		}
	}
	if len(health.Agents) != 2 {
		t.Fatalf("agents = %v, want both providers", health.Agents)
	}
}

// A provider that was found but skipped (undetectable or too-old version)
// stays in skippedAgents with its reason and never gets a version entry; a
// daemon with no versions at all omits the key rather than sending `{}`.
func TestHealthOmitsVersionsForSkippedProviders(t *testing.T) {
	d := newHealthTestDaemon(map[string]AgentEntry{
		"claude": {Path: "/bin/claude", Command: "claude"},
		"codex":  {Path: "/bin/codex", Command: "codex"},
	})
	d.setAgentVersion("claude", "2.1.0")
	reason := "codex version 0.90.0 is below minimum required 0.100.0 — please upgrade"
	d.setSkippedAgents(map[string]string{"codex": reason}, nil)

	health, _ := readHealthBody(t, d)
	if len(health.AgentVersions) != 1 || health.AgentVersions["claude"] != "2.1.0" {
		t.Fatalf("agentVersions = %v, want only claude", health.AgentVersions)
	}
	if _, present := health.AgentVersions["codex"]; present {
		t.Fatal("a skipped provider must not carry a version")
	}
	if got := health.SkippedAgents["codex"]; got != reason {
		t.Fatalf("skippedAgents[codex] = %q, want %q", got, reason)
	}

	bare := newHealthTestDaemon(map[string]AgentEntry{
		"claude": {Path: "/bin/claude", Command: "claude"},
	})
	_, raw := readHealthBody(t, bare)
	if strings.Contains(raw, `"agentVersions"`) {
		t.Fatalf("agentVersions must be omitted when no version is known: %s", raw)
	}
}

// The health port is the daemon's single-instance lock and the only thing the
// CLI and the desktop supervisor can ask "are you still here?". SIGTERM does
// not end the daemon: pollLoop keeps draining in-flight runs for up to 30 s
// plus 150 s of terminal callbacks, and the process holds the port for all of
// it. So the health server must not close on the signal — it answers
// truthfully, with the runs it is draining, until the shutdown that follows
// the drain stops it.
func TestHealthKeepsAnsweringWhileTheDaemonDrains(t *testing.T) {
	d := newHealthTestDaemon(map[string]AgentEntry{"claude": {Path: "/bin/claude", Command: "claude"}})
	d.ready.Store(true)
	d.activeRuns.Store(2)
	rootCtx, signalDaemon := context.WithCancel(t.Context())
	d.rootCtx = rootCtx
	d.cancelFunc = signalDaemon

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	stopHealthServer := d.startHealthServer(listener, time.Now())
	address := "http://" + listener.Addr().String() + "/health"

	// SIGTERM: the daemon stops claiming and drains what it holds.
	signalDaemon()

	health := getHealth(t, address)
	if health.Status != "running" || health.ActiveRunCount != 2 {
		t.Fatalf("draining health = %+v, want a running daemon with its 2 in-flight runs", health)
	}

	// Only the shutdown that follows the drain closes the port.
	if err := stopHealthServer(); err != nil {
		t.Fatalf("stop health server: %v", err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, nil)
	if err != nil {
		t.Fatalf("build health request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err == nil {
		_ = response.Body.Close()
		t.Fatalf("health answered %d after the server was stopped", response.StatusCode)
	}
}

// getHealth reads one /health response and fails the test on anything but a
// decodable 200.
func getHealth(t *testing.T, address string) HealthResponse {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, nil)
	if err != nil {
		t.Fatalf("build health request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("read health: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", response.StatusCode)
	}
	var health HealthResponse
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	return health
}
