package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/contentbundle"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon/execenv"
	"github.com/Intelligent-Internet/zenith-team/server/internal/runchannel"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newTestDaemon builds a Daemon wired at a temp ZENITH_TEAM_HOME and workspaces
// root, pointed at serverURL, with claude/codex discovery stubbed off.
func newTestDaemon(t *testing.T, serverURL string) *Daemon {
	t.Helper()
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	// Resolved because the darwin temp root is a symlink (/var -> /private/var)
	// and the local-directory path lock rejects symlinked paths.
	workspacesRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve workspaces root: %v", err)
	}
	d, err := New(Config{
		ServerBaseURL:     serverURL,
		DaemonID:          "daemon-1",
		DeviceName:        "test-box",
		CLIVersion:        "0.0.1-test",
		WorkspacesRoot:    workspacesRoot,
		HealthPort:        0,
		MaxConcurrentRuns: 4,
		PollInterval:      50 * time.Millisecond,
		HeartbeatInterval: 50 * time.Millisecond,
	}, testLogger())
	if err != nil {
		t.Fatalf("new daemon: %v", err)
	}
	t.Cleanup(func() {
		if d.cleanupZenithTeamExecutable != nil {
			if err := d.cleanupZenithTeamExecutable(); err != nil {
				t.Errorf("cleanup pinned Zenith Team executable: %v", err)
			}
		}
	})
	d.client.SetToken("session-token")
	d.cancelPollInterval = 20 * time.Millisecond
	return d
}

func trackRuntime(d *Daemon, rt Runtime) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cs, ok := d.companies[rt.CompanyID]
	if !ok {
		cs = &companyState{companyID: rt.CompanyID}
		d.companies[rt.CompanyID] = cs
	}
	cs.runtimeIDs = append(cs.runtimeIDs, rt.ID)
	d.runtimeIndex[rt.ID] = rt
}

func TestRunScopedAuthToken(t *testing.T) {
	const validToken = "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, err := runScopedAuthToken(Run{}); err == nil {
		t.Fatal("empty token accepted")
	}
	if _, err := runScopedAuthToken(Run{AuthToken: "session-token"}); err == nil {
		t.Fatal("non-run-scoped token accepted")
	}
	for _, invalid := range []string{
		"brt_abc",
		"brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeF",
		"brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeg",
	} {
		if _, err := runScopedAuthToken(Run{AuthToken: invalid}); err == nil {
			t.Errorf("invalid token %q accepted", invalid)
		}
	}
	token, err := runScopedAuthToken(Run{AuthToken: validToken})
	if err != nil || token != validToken {
		t.Fatalf("token = %q, err = %v", token, err)
	}
}

func TestNormalizeServerBaseURL(t *testing.T) {
	cases := map[string]string{
		"ws://localhost:8080/ws":  "http://localhost:8080",
		"wss://api.example.com":   "https://api.example.com",
		"http://localhost:8080/":  "http://localhost:8080",
		"https://api.example.com": "https://api.example.com",
	}
	for in, want := range cases {
		got, err := NormalizeServerBaseURL(in)
		if err != nil || got != want {
			t.Fatalf("NormalizeServerBaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := NormalizeServerBaseURL("ftp://x"); err == nil {
		t.Fatal("ftp scheme accepted")
	}
}

func TestLoadConfigEnvMirror(t *testing.T) {
	isolateProviderDiscovery(t, t.TempDir())
	stubLoginShellResolver(t, nil)
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	t.Setenv("ZENITH_TEAM_SERVER_URL", "wss://api.zenith-team.test")
	t.Setenv("ZENITH_TEAM_POLL_INTERVAL", "7s")
	t.Setenv("ZENITH_TEAM_HEARTBEAT_INTERVAL", "3s")
	t.Setenv("ZENITH_TEAM_MAX_CONCURRENT_RUNS", "5")
	t.Setenv("ZENITH_TEAM_WORKSPACES_ROOT", t.TempDir())
	t.Setenv("ZENITH_TEAM_CLAUDE_ARGS", `--flag "quoted value"`)
	t.Setenv("ZENITH_TEAM_CODEX_ARGS", "-c a=1")
	t.Setenv("ZENITH_TEAM_CODEX_HANDSHAKE_TIMEOUT", "9s")
	t.Setenv("ZENITH_TEAM_CODEX_SEMANTIC_INACTIVITY", "4m")
	t.Setenv("ZENITH_TEAM_MCP_EXECUTABLE", "/usr/local/bin/zenith-team-mcp")
	t.Setenv("ZENITH_TEAM_DAEMON_ID", "pinned-id")
	t.Setenv("ZENITH_TEAM_LAUNCHED_BY", "desktop")

	cfg, err := LoadConfig(Overrides{AllowNoAgents: true})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.ServerBaseURL != "https://api.zenith-team.test" {
		t.Fatalf("server url = %q", cfg.ServerBaseURL)
	}
	if cfg.PollInterval != 7*time.Second || cfg.HeartbeatInterval != 3*time.Second {
		t.Fatalf("intervals = %v / %v", cfg.PollInterval, cfg.HeartbeatInterval)
	}
	if cfg.MaxConcurrentRuns != 5 {
		t.Fatalf("max concurrent runs = %d", cfg.MaxConcurrentRuns)
	}
	if len(cfg.ClaudeArgs) != 2 || cfg.ClaudeArgs[1] != "quoted value" {
		t.Fatalf("claude args = %v", cfg.ClaudeArgs)
	}
	if len(cfg.CodexArgs) != 2 {
		t.Fatalf("codex args = %v", cfg.CodexArgs)
	}
	if cfg.CodexHandshakeTimeout != 9*time.Second {
		t.Fatalf("handshake timeout = %v", cfg.CodexHandshakeTimeout)
	}
	if cfg.CodexSemanticInactivityTimeout != 4*time.Minute {
		t.Fatalf("semantic inactivity = %v", cfg.CodexSemanticInactivityTimeout)
	}
	if cfg.MCPExecutable != "/usr/local/bin/zenith-team-mcp" {
		t.Fatalf("mcp executable = %q", cfg.MCPExecutable)
	}
	if cfg.DaemonID != "pinned-id" {
		t.Fatalf("daemon id = %q", cfg.DaemonID)
	}
	if cfg.LaunchedBy != "desktop" {
		t.Fatalf("launched by = %q, want desktop", cfg.LaunchedBy)
	}
}

func TestLoadConfigRejectsUnknownLauncher(t *testing.T) {
	isolateProviderDiscovery(t, t.TempDir())
	stubLoginShellResolver(t, nil)
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	t.Setenv("ZENITH_TEAM_LAUNCHED_BY", "some-wrapper")

	if _, err := LoadConfig(Overrides{AllowNoAgents: true}); err == nil {
		t.Fatal("LoadConfig() accepted an unknown launcher")
	}
}

func TestEnsureDaemonIDPersists(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	first, err := EnsureDaemonID()
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	second, err := EnsureDaemonID()
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if first != second {
		t.Fatalf("daemon id not stable: %q vs %q", first, second)
	}
}

func TestCLIConfigRoundTrip(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	want := CLIConfig{ServerURL: "https://api.test", CompanyID: "c1"}
	if err := SaveCLIConfig(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadCLIConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != want {
		t.Fatalf("config = %+v, want %+v", got, want)
	}
	home, _ := ZenithTeamHome()
	info, err := os.Stat(home + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestParseFlexDuration(t *testing.T) {
	got, err := parseFlexDuration("1d12h")
	if err != nil || got != 36*time.Hour {
		t.Fatalf("1d12h = %v, %v", got, err)
	}
	if _, err := parseFlexDuration("nope"); err == nil {
		t.Fatal("garbage accepted")
	}
}

// --- claim ladder -----------------------------------------------------------

func TestClaimRunsWSFirstFallsBackToHTTPWithoutRPCV1(t *testing.T) {
	var httpClaims atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/daemon/runs/claim" {
			httpClaims.Add(1)
			json.NewEncoder(w).
				Encode(map[string]any{"items": []Run{{ID: uuid.MustParse("00000000-0000-4000-8000-000000004711"), RuntimeID: "rt-1"}}})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	runs, err := d.ClaimRunsWSFirst(context.Background(), "daemon-1", []string{"rt-1"}, 4)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != uuid.MustParse("00000000-0000-4000-8000-000000004711") {
		t.Fatalf("runs = %+v", runs)
	}
	if httpClaims.Load() != 1 {
		t.Fatalf("http claims = %d", httpClaims.Load())
	}
}

func TestClaimRunsWSFirstUsesWSWhenNegotiated(t *testing.T) {
	d := newTestDaemon(t, "http://unreachable.invalid")
	generation := d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
		var msg protocol.Message
		if err := json.Unmarshal(frame, &msg); err != nil {
			t.Fatalf("bad frame: %v", err)
		}
		var req protocol.RPCRequestPayload
		if err := json.Unmarshal(msg.Payload, &req); err != nil {
			t.Fatalf("bad rpc payload: %v", err)
		}
		if req.Method != "runs.claim" {
			t.Fatalf("method = %q", req.Method)
		}
		item := &wsOutbound{data: frame}
		item.beginWrite()
		go d.wsRPC.deliver(protocol.RPCResponsePayload{
			RequestID: req.RequestID,
			Status:    200,
			Body: json.RawMessage(
				`{"items":[{"id":"00000000-0000-4000-8000-000000004721","runtimeId":"rt-1"}]}`,
			),
		})
		return item, nil
	})
	d.wsRPC.markRPCV1Supported(generation)

	runs, err := d.ClaimRunsWSFirst(context.Background(), "daemon-1", []string{"rt-1"}, 2)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != uuid.MustParse("00000000-0000-4000-8000-000000004721") {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestClaimRunsWSFirstUncertainOutcomeSkipsCycle(t *testing.T) {
	d := newTestDaemon(t, "http://unreachable.invalid")
	generation := d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
		item := &wsOutbound{data: frame}
		// The writer already sent the frame, then the connection died with no
		// response: cancel() must report "too late".
		item.beginWrite()
		go d.wsRPC.attach(nil)
		return item, nil
	})
	d.wsRPC.markRPCV1Supported(generation)

	runs, err := d.ClaimRunsWSFirst(context.Background(), "daemon-1", []string{"rt-1"}, 1)
	if err != nil || runs != nil {
		t.Fatalf("uncertain claim: runs=%v err=%v (want nil, nil)", runs, err)
	}
	if d.wsClaimHTTPFallbackAfter.Load() == 0 {
		t.Fatal("safety window not armed after uncertain outcome")
	}
	// Inside the safety window every claim cycle is skipped.
	runs, err = d.ClaimRunsWSFirst(context.Background(), "daemon-1", []string{"rt-1"}, 1)
	if err != nil || runs != nil {
		t.Fatalf("claim inside safety window: runs=%v err=%v", runs, err)
	}
}

func TestClaimRunsWSFirstCanceledAfterSendSkipsCycle(t *testing.T) {
	d := newTestDaemon(t, "http://unreachable.invalid")
	ctx, cancel := context.WithCancel(context.Background())
	generation := d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
		item := &wsOutbound{data: frame}
		item.beginWrite()
		cancel()
		return item, nil
	})
	d.wsRPC.markRPCV1Supported(generation)

	runs, err := d.ClaimRunsWSFirst(ctx, "daemon-1", []string{"rt-1"}, 1)
	if err != nil || runs != nil {
		t.Fatalf("canceled sent claim: runs=%v err=%v, want nil, nil", runs, err)
	}
	if d.wsClaimHTTPFallbackAfter.Load() == 0 {
		t.Fatal("safety window not armed after cancellation of a sent claim")
	}
}

func TestClaimRunsWSFirstMalformedSuccessSkipsCycle(t *testing.T) {
	d := newTestDaemon(t, "http://unreachable.invalid")
	generation := d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
		var msg protocol.Message
		if err := json.Unmarshal(frame, &msg); err != nil {
			t.Fatalf("bad frame: %v", err)
		}
		var req protocol.RPCRequestPayload
		if err := json.Unmarshal(msg.Payload, &req); err != nil {
			t.Fatalf("bad rpc payload: %v", err)
		}
		item := &wsOutbound{data: frame}
		item.beginWrite()
		go d.wsRPC.deliver(protocol.RPCResponsePayload{
			RequestID: req.RequestID,
			Status:    http.StatusOK,
			Body:      json.RawMessage(`{"items":`),
		})
		return item, nil
	})
	d.wsRPC.markRPCV1Supported(generation)

	runs, err := d.ClaimRunsWSFirst(context.Background(), "daemon-1", []string{"rt-1"}, 1)
	if err != nil || runs != nil {
		t.Fatalf("malformed successful claim: runs=%v err=%v, want nil, nil", runs, err)
	}
	if d.wsClaimHTTPFallbackAfter.Load() == 0 {
		t.Fatal("safety window not armed after an undecodable successful claim")
	}
}

func TestClaimRunsHTTPBatchErrorDoesNotProbeSingleRuntimeRoute(t *testing.T) {
	var singleRuntimeClaims atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/daemon/runs/claim":
			http.NotFound(w, r)
		case "/api/v1/daemon/runtimes/rt-1/runs/claim":
			singleRuntimeClaims.Add(1)
			json.NewEncoder(w).
				Encode(map[string]any{"run": Run{ID: uuid.MustParse("00000000-0000-4000-8000-000000009999"), RuntimeID: "rt-1"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	runs, err := d.ClaimRunsWSFirst(context.Background(), "daemon-1", []string{"rt-1"}, 1)
	if err == nil || runs != nil {
		t.Fatalf("claim: runs=%v err=%v, want the batch error", runs, err)
	}
	if singleRuntimeClaims.Load() != 0 {
		t.Fatalf("single-runtime claims = %d, want 0", singleRuntimeClaims.Load())
	}
}

// --- heartbeat --------------------------------------------------------------

func TestRunHeartbeatTickSkipsWhileWSAckFresh(t *testing.T) {
	var beats atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/daemon/heartbeat" {
			beats.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	d.cfg.HeartbeatInterval = time.Hour // freshness window: 2h
	d.recordWSHeartbeatAck("rt-1")
	if d.runHeartbeatTick(context.Background(), "rt-1") {
		t.Fatal("fresh-ack tick reported transient failure")
	}
	if beats.Load() != 0 {
		t.Fatalf("HTTP heartbeat sent despite fresh WS ack: %d", beats.Load())
	}
	d.clearWSHeartbeatAcks()
	d.runHeartbeatTick(context.Background(), "rt-1")
	if beats.Load() != 1 {
		t.Fatalf("HTTP heartbeat not resumed after ack clear: %d", beats.Load())
	}
}

// New wires cfg.DaemonID into the client, so every control-plane request —
// both the POST and the GET request paths — names the machine.
func TestDaemonSendsItsDaemonIDOnEveryControlPlaneRequest(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Header.Get("X-Zenith-Team-Daemon-ID")
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/daemon/heartbeat":
			json.NewEncoder(w).Encode(map[string]any{"status": "ok", "runtimeId": "rt-1"})
		case "/api/v1/daemon/companies":
			json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		}
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	ctx := context.Background()
	if _, err := d.client.SendHeartbeat(ctx, "rt-1", d.credentialIsolation()); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if _, err := d.client.ListCompanies(ctx); err != nil {
		t.Fatalf("list companies: %v", err)
	}
	if err := d.client.Deregister(ctx, []string{"rt-1"}); err != nil {
		t.Fatalf("deregister: %v", err)
	}
	if err := d.client.RecoverOrphans(ctx, "rt-1"); err != nil {
		t.Fatalf("recover orphans: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{
		"/api/v1/daemon/heartbeat",
		"/api/v1/daemon/companies",
		"/api/v1/daemon/deregister",
		"/api/v1/daemon/runtimes/rt-1/recover-orphans",
	} {
		if seen[path] != "daemon-1" {
			t.Errorf("%s: X-Zenith-Team-Daemon-ID = %q, want daemon-1", path, seen[path])
		}
	}
}

func TestHeartbeatRuntimeGonePrunesAndReregisters(t *testing.T) {
	var registers atomic.Int32
	var recovers atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/daemon/heartbeat":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"detail":"runtime not found","code":"runtime_not_found"}`))
		case "/api/v1/daemon/register":
			registers.Add(1)
			json.NewEncoder(w).
				Encode(map[string]any{"items": []Runtime{{ID: "rt-2", CompanyID: "company-1", Provider: "claude", Status: "online"}}})
		case "/api/v1/daemon/runtimes/rt-2/recover-orphans":
			recovers.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"orphaned": 0, "retried": 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	agents := map[string]AgentEntry{"claude": {Path: "/bin/echo", Command: "claude"}}
	d.agentsAvailable.Store(&agents)
	d.probes.version = func(context.Context, string) (string, error) { return "2.5.0", nil }
	d.probes.signIn = func(context.Context, string, string) protocol.RuntimeSignIn {
		return protocol.RuntimeSignIn{State: protocol.RuntimeSignInSignedIn}
	}

	stragglerEntryAt := time.Now()
	trackRuntime(d, Runtime{ID: "rt-1", CompanyID: "company-1", Provider: "claude"})
	if transient := d.runHeartbeatTick(context.Background(), "rt-1"); transient {
		t.Fatal("runtime-gone tick reported transient failure")
	}
	// The tick hands recovery to a tracked goroutine: prune, re-register,
	// index, recover-orphans, then stamp the completed register. Join it so
	// every check below reads the settled outcome.
	d.runtimeGoneWorkers.Wait()
	if d.findRuntime("rt-1") != nil {
		t.Fatal("stale runtime not pruned")
	}
	if d.findRuntime("rt-2") == nil {
		t.Fatal("replacement runtime not indexed")
	}
	if registers.Load() != 1 {
		t.Fatalf("register calls = %d, want 1", registers.Load())
	}
	if recovers.Load() != 1 {
		t.Fatalf("recover-orphans calls = %d, want 1", recovers.Load())
	}
	// A same-wave straggler — whose entry predates the completed register —
	// is coalesced; a genuinely later wave may claim again.
	if d.tryClaimRegisterSlot("company-1", stragglerEntryAt, time.Now()) {
		t.Fatal("same-wave straggler re-claimed the register slot")
	}
	laterWave := time.Now().Add(time.Minute)
	if !d.tryClaimRegisterSlot("company-1", laterWave, laterWave) {
		t.Fatal("a later wave could not claim the register slot")
	}
}

// --- drain ------------------------------------------------------------------

type fakeBackend struct {
	events  []agent.Event
	result  agent.Result
	execErr error
	// hold keeps the event stream open until closed, for watchdog tests.
	hold chan struct{}
}

type cancellationDrainBackend struct {
	// finished is closed once the consumer has taken Finished, before the
	// stream closes, so a consumer that drains to the close has observed it.
	finished chan struct{}
}

func (b *cancellationDrainBackend) Execute(
	ctx context.Context,
	_ string,
	_ agent.ExecOptions,
) (*agent.Execution, error) {
	events := make(chan agent.Event)
	go func() {
		defer close(events)
		events <- agent.CompletedItem{
			Role:  agent.RoleAgent,
			Parts: []agent.Part{agent.TextPart{Text: "invalid without a key"}},
		}
		<-ctx.Done()
		events <- agent.CompletedItem{
			Key:   "must-not-persist",
			Role:  agent.RoleAgent,
			Parts: []agent.Part{agent.TextPart{Text: "discarded while draining"}},
		}
		events <- agent.Finished{Result: agent.Result{Status: "cancelled"}}
		close(b.finished)
	}()
	return &agent.Execution{Events: events}, nil
}

func (b *fakeBackend) Execute(
	ctx context.Context,
	_ string,
	_ agent.ExecOptions,
) (*agent.Execution, error) {
	if b.execErr != nil {
		return nil, b.execErr
	}
	events := make(chan agent.Event, len(b.events)+1)
	go func() {
		defer close(events)
		for _, event := range b.events {
			events <- event
		}
		if b.hold != nil {
			select {
			case <-b.hold:
			case <-ctx.Done():
			}
		}
		events <- agent.Finished{Result: b.result}
	}()
	return &agent.Execution{Events: events}, nil
}

type capturedRequest struct {
	path string
	body []byte
}

func newCaptureServer(t *testing.T) (*httptest.Server, func() []capturedRequest) {
	t.Helper()
	var mu sync.Mutex
	var captured []capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		captured = append(captured, capturedRequest{path: r.URL.Path, body: body})
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []capturedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedRequest(nil), captured...)
	}
}

func attachTranscriptAppendRPC(
	t *testing.T,
	d *Daemon,
) func() []protocol.RunTranscriptAppendRequest {
	t.Helper()
	var mu sync.Mutex
	requests := make([]protocol.RunTranscriptAppendRequest, 0, 4)
	generation := d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
		var message protocol.Message
		if err := json.Unmarshal(frame, &message); err != nil {
			t.Fatalf("unmarshal ws message: %v", err)
		}
		var rpc protocol.RPCRequestPayload
		if err := json.Unmarshal(message.Payload, &rpc); err != nil {
			t.Fatalf("unmarshal rpc request: %v", err)
		}
		var request protocol.RunTranscriptAppendRequest
		if err := json.Unmarshal(rpc.Body, &request); err != nil {
			t.Fatalf("unmarshal transcript append: %v", err)
		}
		mu.Lock()
		requests = append(requests, request)
		seq := int64(len(requests))
		mu.Unlock()

		receipt, err := json.Marshal(protocol.RunTranscriptAppendReceipt{
			ID:          "message-row",
			RunID:       request.RunID,
			ItemKey:     request.ItemKey,
			Seq:         seq,
			ThroughSeq:  seq,
			ItemDigest:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			CommittedAt: "2026-08-21T00:00:00Z",
		})
		if err != nil {
			t.Fatalf("marshal transcript receipt: %v", err)
		}
		outbound := &wsOutbound{data: frame}
		outbound.beginWrite()
		go d.wsRPC.deliver(protocol.RPCResponsePayload{
			RequestID: rpc.RequestID,
			Status:    http.StatusOK,
			Body:      receipt,
		})
		return outbound, nil
	})
	d.wsRPC.markRPCV1Supported(generation)
	return func() []protocol.RunTranscriptAppendRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]protocol.RunTranscriptAppendRequest(nil), requests...)
	}
}

// attachTranscriptReplayRPC answers every transcript append with one fixed
// receipt — what an exact replay of an already-committed item returns through
// the (run_id, item_key) fence.
func attachTranscriptReplayRPC(
	t *testing.T,
	d *Daemon,
	receipt protocol.RunTranscriptAppendReceipt,
) func() []protocol.RunTranscriptAppendRequest {
	t.Helper()
	var mu sync.Mutex
	var requests []protocol.RunTranscriptAppendRequest
	generation := d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
		var message protocol.Message
		if err := json.Unmarshal(frame, &message); err != nil {
			t.Fatalf("unmarshal ws message: %v", err)
		}
		var rpc protocol.RPCRequestPayload
		if err := json.Unmarshal(message.Payload, &rpc); err != nil {
			t.Fatalf("unmarshal rpc request: %v", err)
		}
		var request protocol.RunTranscriptAppendRequest
		if err := json.Unmarshal(rpc.Body, &request); err != nil {
			t.Fatalf("unmarshal transcript append: %v", err)
		}
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()

		body, err := json.Marshal(receipt)
		if err != nil {
			t.Fatalf("marshal transcript receipt: %v", err)
		}
		outbound := &wsOutbound{data: frame}
		outbound.beginWrite()
		go d.wsRPC.deliver(protocol.RPCResponsePayload{
			RequestID: rpc.RequestID, Status: http.StatusOK, Body: body,
		})
		return outbound, nil
	})
	d.wsRPC.markRPCV1Supported(generation)
	return func() []protocol.RunTranscriptAppendRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]protocol.RunTranscriptAppendRequest(nil), requests...)
	}
}

func TestExecuteRunStreamPipeline(t *testing.T) {
	srv, captured := newCaptureServer(t)
	d := newTestDaemon(t, srv.URL)
	appended := attachTranscriptAppendRPC(t, d)
	preview, unsubscribe, ok := d.localPreview.subscribe()
	if !ok {
		t.Fatal("subscribe() rejected the first local preview subscriber")
	}
	defer unsubscribe()

	longOutput := make([]byte, 9000)
	for i := range longOutput {
		longOutput[i] = 'x'
	}
	backend := &fakeBackend{
		events: []agent.Event{
			agent.SessionReady{SessionID: "sess-1"},
			agent.ItemDelta{
				ItemKey: "answer-1", Channel: agent.DeltaChannelReasoning,
				Operation: agent.DeltaOperationAppend, Content: "th-1 ",
			},
			agent.ItemDelta{
				ItemKey: "answer-1", Channel: agent.DeltaChannelText,
				Operation: agent.DeltaOperationAppend, Content: "hello world",
			},
			agent.CompletedItem{
				Key: "tool-call-1", Role: agent.RoleAgent,
				Parts: []agent.Part{agent.ToolCallPart{
					CallID: "call-1", Name: "Bash", Input: map[string]any{
						"cmd":     "ls",
						"api_key": "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz0123456789abcdefghij-abcdefAA",
					},
				}},
			},
			agent.CompletedItem{
				Key: "tool-result-1", Role: agent.RoleTool,
				Parts: []agent.Part{agent.ToolResultPart{
					CallID: "call-1", Name: "Bash", Output: string(longOutput),
				}},
			},
			agent.CompletedItem{
				Key: "answer-1", Role: agent.RoleAgent,
				Parts: []agent.Part{agent.TextPart{Text: "hello world"}},
			},
		},
		result: agent.Result{Status: "completed", Output: "done", SessionID: "sess-1"},
	}

	outcome, err := d.executeRunStream(
		context.Background(),
		backend,
		"prompt",
		agent.ExecOptions{Cwd: "/tmp/wd"},
		testLogger(),
		Run{
			ID:             uuid.MustParse("00000000-0000-4000-8000-000000004711"),
			CompanyID:      "company-1",
			ConversationID: "conversation-1",
		},
		"",
	)
	if err != nil {
		t.Fatalf("executeRunStream: %v", err)
	}
	if outcome.Result.Status != "completed" || outcome.ToolCount != 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.Transcript.ThroughSeq != 3 || outcome.Transcript.FinalItemKey != "answer-1" {
		t.Fatalf("transcript head = %+v", outcome.Transcript)
	}

	// The session pin runs on a background goroutine; give it a beat.
	time.Sleep(200 * time.Millisecond)

	requests := appended()
	if len(requests) != 3 {
		t.Fatalf("appended requests = %d, want 3", len(requests))
	}
	if requests[0].ItemKey != "tool-call-1" || requests[1].ItemKey != "tool-result-1" ||
		requests[2].ItemKey != "answer-1" {
		t.Fatalf("append order = %#v", requests)
	}
	if got := requests[1].Parts[0].Output; len(got) != len(longOutput) {
		t.Fatalf("tool result length = %d, want %d", len(got), len(longOutput))
	}
	encodedInput := string(requests[0].Parts[0].Input)
	if encodedInput == "" ||
		encodedInput == `{"cmd":"ls","api_key":"sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz0123456789abcdefghij-abcdefAA"}` {
		t.Fatalf("tool input was not redacted: %s", encodedInput)
	}

	previewCtx, cancelPreview := context.WithTimeout(context.Background(), time.Second)
	defer cancelPreview()
	firstPreview, ok := preview.next(previewCtx)
	if !ok || firstPreview.ItemKey != "answer-1" || firstPreview.Channel != localPreviewChannelReasoning {
		t.Fatalf("first preview = %+v, ok=%v", firstPreview, ok)
	}
	secondPreview, ok := preview.next(previewCtx)
	if !ok || secondPreview.ItemKey != "answer-1" || secondPreview.Channel != localPreviewChannelText ||
		secondPreview.Content != "hello world" {
		t.Fatalf("second preview = %+v, ok=%v", secondPreview, ok)
	}

	var sawPin bool
	for _, req := range captured() {
		if req.path == "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/session" {
			sawPin = true
		}
	}
	if !sawPin {
		t.Fatal("session not pinned from status message")
	}
}

func TestExecuteRunStreamPreservesAcknowledgedHeadOnAppendFailure(t *testing.T) {
	d := newTestDaemon(t, "http://unused.invalid")
	requests := 0
	generation := d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
		var message protocol.Message
		if err := json.Unmarshal(frame, &message); err != nil {
			t.Fatalf("unmarshal ws message: %v", err)
		}
		var rpc protocol.RPCRequestPayload
		if err := json.Unmarshal(message.Payload, &rpc); err != nil {
			t.Fatalf("unmarshal rpc request: %v", err)
		}
		var request protocol.RunTranscriptAppendRequest
		if err := json.Unmarshal(rpc.Body, &request); err != nil {
			t.Fatalf("unmarshal transcript append: %v", err)
		}
		requests++
		response := protocol.RPCResponsePayload{RequestID: rpc.RequestID}
		if requests == 1 {
			response.Status = http.StatusOK
			response.Body, _ = json.Marshal(protocol.RunTranscriptAppendReceipt{
				ID: "row-1", RunID: request.RunID, ItemKey: request.ItemKey,
				Seq: 1, ThroughSeq: 1,
				ItemDigest:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				CommittedAt: "2026-08-21T00:00:00Z",
			})
		} else {
			response.Status = http.StatusConflict
			response.Error = "conflict"
		}
		outbound := &wsOutbound{data: frame}
		outbound.beginWrite()
		go d.wsRPC.deliver(response)
		return outbound, nil
	})
	d.wsRPC.markRPCV1Supported(generation)

	outcome, err := d.executeRunStream(
		context.Background(),
		&fakeBackend{
			events: []agent.Event{
				agent.CompletedItem{
					Key: "answer-1", Role: agent.RoleAgent,
					Parts: []agent.Part{agent.TextPart{Text: "first"}},
				},
				agent.CompletedItem{
					Key: "answer-2", Role: agent.RoleAgent,
					Parts: []agent.Part{agent.TextPart{Text: "second"}},
				},
			},
			result: agent.Result{Status: "completed"},
		},
		"prompt",
		agent.ExecOptions{},
		testLogger(),
		Run{
			ID:             uuid.MustParse("00000000-0000-4000-8000-000000004711"),
			CompanyID:      "company-1",
			ConversationID: "conversation-1",
		},
		"",
	)
	if err == nil {
		t.Fatal("executeRunStream accepted a conflicting second item")
	}
	if outcome.Transcript.ThroughSeq != 1 || outcome.Transcript.FinalItemKey != "answer-1" {
		t.Fatalf("preserved transcript head = %+v", outcome.Transcript)
	}
}

func TestExecuteRunStreamCancelsAndDrainsAfterMappingFailure(t *testing.T) {
	d := newTestDaemon(t, "http://unused.invalid")
	appended := attachTranscriptAppendRPC(t, d)
	backend := &cancellationDrainBackend{finished: make(chan struct{})}

	_, err := d.executeRunStream(
		context.Background(),
		backend,
		"prompt",
		agent.ExecOptions{},
		testLogger(),
		Run{
			ID:             uuid.MustParse("00000000-0000-4000-8000-000000004711"),
			CompanyID:      "company-1",
			ConversationID: "conversation-1",
		},
		"",
	)
	if err == nil {
		t.Fatal("executeRunStream accepted an invalid completed item")
	}
	// The fixture closes finished before the stream, so a drain that ran to
	// the close has it closed; returning earlier leaves the fixture blocked
	// on an unread send with finished still open.
	select {
	case <-backend.finished:
	default:
		t.Fatal("executeRunStream returned before the cancelled provider stream reached Finished")
	}
	if requests := appended(); len(requests) != 0 {
		t.Fatalf("drain persisted %d completed items after the mapping failure", len(requests))
	}
}

func TestExecuteAndDrainIdleWatchdog(t *testing.T) {
	srv, _ := newCaptureServer(t)
	d := newTestDaemon(t, srv.URL)
	d.cfg.AgentIdleWatchdog = 50 * time.Millisecond

	backend := &fakeBackend{
		result: agent.Result{Status: "aborted"},
		hold:   make(chan struct{}),
	}
	outcome, err := d.executeRunStream(
		context.Background(),
		backend,
		"p",
		agent.ExecOptions{},
		testLogger(),
		Run{
			ID:             uuid.MustParse("00000000-0000-4000-8000-000000004713"),
			CompanyID:      "company-1",
			ConversationID: "conversation-1",
		},
		"",
	)
	if err != nil {
		t.Fatalf("executeRunStream: %v", err)
	}
	if outcome.Result.Status != "idle_watchdog" {
		t.Fatalf("status = %q, want idle_watchdog", outcome.Result.Status)
	}
	if outcome.Transcript != (RunTranscriptProof{}) {
		t.Fatalf("empty transcript proof = %+v", outcome.Transcript)
	}
}

// --- handleRun --------------------------------------------------------------

func TestHandleRunReportsUsageCompleteAndCancelAck(t *testing.T) {
	var mu sync.Mutex
	paths := map[string]int{}
	var reportedUsage []RunUsageEntry
	statusResponse := `{"status":"running"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths[r.Method+" "+r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/usage" {
			var body struct {
				Usage []RunUsageEntry `json:"usage"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode usage request: %v", err)
			} else {
				mu.Lock()
				reportedUsage = body.Usage
				mu.Unlock()
			}
		}
		if r.URL.Path == "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/status" {
			w.Write([]byte(statusResponse))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	trackRuntime(d, Runtime{ID: "rt-1", CompanyID: "company-1", Provider: "claude"})
	d.runner = runnerFunc(
		func(_ context.Context, _ Run, provider string, _ int, _ *slog.Logger) (RunResult, error) {
			return RunResult{
				Disposition: RunDispositionCompleted,
				Comment:     "did the thing",
				Usage: []RunUsageEntry{{
					Provider: provider, Model: "m", InputTokens: 10,
					RawJSON: json.RawMessage(`{"input_tokens":10}`),
				}},
			}, nil
		},
	)

	d.handleRun(
		context.Background(),
		Run{
			ID:             uuid.MustParse("00000000-0000-4000-8000-000000004711"),
			RuntimeID:      "rt-1",
			CompanyID:      "company-1",
			ConversationID: "conv-1",
		},
		0,
	)

	mu.Lock()
	defer mu.Unlock()
	if paths["POST /api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/usage"] != 1 {
		t.Fatalf(
			"usage posts = %d",
			paths["POST /api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/usage"],
		)
	}
	if paths["POST /api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/complete"] != 1 {
		t.Fatalf(
			"complete posts = %d",
			paths["POST /api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/complete"],
		)
	}
	if len(reportedUsage) != 1 || string(reportedUsage[0].RawJSON) != `{"input_tokens":10}` {
		t.Fatalf("reported usage = %+v", reportedUsage)
	}
}

func TestHandleRunFailClosedOnBlocked(t *testing.T) {
	var mu sync.Mutex
	var failBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/fail" {
			raw := make([]byte, 1<<16)
			n, _ := r.Body.Read(raw)
			mu.Lock()
			failBody = raw[:n]
			mu.Unlock()
		}
		if r.URL.Path == "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/status" {
			w.Write([]byte(`{"status":"running"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	trackRuntime(d, Runtime{ID: "rt-1", CompanyID: "company-1", Provider: "claude"})
	d.runner = runnerFunc(func(context.Context, Run, string, int, *slog.Logger) (RunResult, error) {
		return RunResult{
			Disposition:   RunDispositionFailed,
			Comment:       "rate limited",
			SessionID:     "sess-9",
			FailureReason: "timeout",
		}, nil
	})

	d.handleRun(
		context.Background(),
		Run{
			ID:             uuid.MustParse("00000000-0000-4000-8000-000000004711"),
			RuntimeID:      "rt-1",
			CompanyID:      "company-1",
			ConversationID: "conv-1",
		},
		0,
	)

	mu.Lock()
	defer mu.Unlock()
	if failBody == nil {
		t.Fatal("blocked result did not reach the fail endpoint")
	}
	var payload map[string]any
	if err := json.Unmarshal(failBody, &payload); err != nil {
		t.Fatalf("fail body: %v", err)
	}
	if payload["error"] != "rate limited" || payload["failureReason"] != "timeout" ||
		payload["sessionId"] != "sess-9" {
		t.Fatalf("fail payload = %v", payload)
	}
}

func TestHandleRunUntrackedRuntimeFailsBack(t *testing.T) {
	var mu sync.Mutex
	var failCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/fail" {
			mu.Lock()
			failCalls++
			mu.Unlock()
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	d.runner = runnerFunc(func(context.Context, Run, string, int, *slog.Logger) (RunResult, error) {
		t.Fatal("runner must not execute for an untracked runtime")
		return RunResult{}, nil
	})
	d.handleRun(
		context.Background(),
		Run{
			ID:        uuid.MustParse("00000000-0000-4000-8000-000000004711"),
			RuntimeID: "ghost",
			CompanyID: "company-1",
		},
		0,
	)
	mu.Lock()
	defer mu.Unlock()
	if failCalls != 1 {
		t.Fatalf("fail calls = %d", failCalls)
	}
}

func decodeDaemonRequestBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("decode %s body: %v", r.URL.Path, err)
	}
	return body
}

// Bug R2: the dispatched → running transition is fenced on the runtime that
// claims to be executing, so the request has to name one. The run row cannot:
// the server would be comparing it to itself.
func TestStartRunNamesTheExecutingRuntime(t *testing.T) {
	var mu sync.Mutex
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/start" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		mu.Lock()
		body = decodeDaemonRequestBody(t, r)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	if err := d.client.StartRun(
		context.Background(),
		"rt-1",
		uuid.MustParse("00000000-0000-4000-8000-000000004711"),
	); err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if body["runtimeId"] != "rt-1" {
		t.Fatalf("start body = %v, want the executing runtime", body)
	}
}

// --- terminal head reconciliation -------------------------------------------

// Bug R1: a terminal transition refused because the daemon's proof named a
// stale transcript prefix was classified permanent. The daemon re-sent the
// SAME proof through FailRun, was refused again, logged "fail run fallback
// also failed", and gave up — leaving the run in 'running' for a sweeper to
// kill while a perfectly good answer sat durable in the conversation. These
// tests pin the recovery and its bound.

const replayItemDigest = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

// headMismatchBody is the 409 the daemon reconciles against: the standard
// {detail, code, requestId} envelope carrying the server's OWN head flat
// beside it, which is the shape protocol.RunTerminalHeadMismatch declares.
func headMismatchBody(runID, throughSeq int64) string {
	return fmt.Sprintf(
		`{"detail":"terminal transcript head does not match the committed run transcript",`+
			`"code":"run_transcript_head_mismatch","requestId":"req-1",`+
			`"runId":"00000000-0000-4000-8000-%012d","throughSeq":%d}`,
		runID, throughSeq,
	)
}

func writeHeadMismatch(w http.ResponseWriter, runID, throughSeq int64) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_, _ = io.WriteString(w, headMismatchBody(runID, throughSeq))
}

// The daemon must never adopt a head it was not told. A refusal that is a
// different conflict, names another run, or carries a seq no head could hold
// is not reconcilable — treating it as one fabricates a proof.
func TestTerminalHeadMismatchRefusesWhatItCannotVerify(t *testing.T) {
	t.Parallel()

	const path = "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/complete"
	conflict := func(body string) error {
		return newRequestError(http.MethodPost, path, http.StatusConflict, body)
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"this run's own head", conflict(headMismatchBody(4711, 9)), true},
		{
			"the other terminal conflict",
			conflict(`{"detail":"final item is not the latest","code":"run_terminal_item_invalid"}`),
			false,
		},
		{"another run's head", conflict(headMismatchBody(4712, 9)), false},
		{"a seq no transcript head could hold", conflict(headMismatchBody(4711, -1)), false},
		{
			"the right body under the wrong status",
			newRequestError(
				http.MethodPost, path, http.StatusInternalServerError,
				headMismatchBody(4711, 9),
			),
			false,
		},
		{"a transport failure", errors.New("connection reset by peer"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, ok := terminalHeadMismatch(
				tt.err,
				uuid.MustParse("00000000-0000-4000-8000-000000004711"),
			); ok != tt.want {
				t.Fatalf("terminalHeadMismatch() reconcilable = %v, want %v", ok, tt.want)
			}
		})
	}
}

func TestReportTerminalRunAdoptsTheRefusedHeadAndReproves(t *testing.T) {
	var mu sync.Mutex
	var completes []map[string]any
	failCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/complete":
			body := decodeDaemonRequestBody(t, r)
			mu.Lock()
			completes = append(completes, body)
			attempt := len(completes)
			mu.Unlock()
			if attempt == 1 {
				writeHeadMismatch(w, 4711, 9)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/fail":
			mu.Lock()
			failCalls++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		}
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	err := d.reportTerminalRun(context.Background(), terminalRunReport{
		kind:       terminalRunReportComplete,
		runID:      uuid.MustParse("00000000-0000-4000-8000-000000004711"),
		transcript: RunTranscriptProof{ThroughSeq: 4, FinalItemKey: "item-7"},
	})
	if err != nil {
		t.Fatalf("reportTerminalRun() error = %v, want the reconciled transition to succeed", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(completes) != 2 {
		t.Fatalf("complete posts = %d, want the refusal plus one re-proof", len(completes))
	}
	if completes[0]["throughSeq"] != float64(4) {
		t.Fatalf("first proof = %v, want the daemon's own head", completes[0])
	}
	if completes[1]["throughSeq"] != float64(9) {
		t.Fatalf("second proof = %v, want the head the server reported", completes[1])
	}
	// Nothing told the daemon a different item is the run's answer, so the
	// nomination it already had must survive the reconciliation untouched.
	if completes[1]["finalItemKey"] != "item-7" {
		t.Fatalf("second proof final item = %v, want item-7", completes[1]["finalItemKey"])
	}
	if failCalls != 0 {
		t.Fatalf("fail posts = %d, want none for a run that completed", failCalls)
	}
}

// A head that keeps moving is genuine divergence. The reconciliation must stop
// re-proving, and the caller must NOT re-send the same rejected proof through
// fail — doing that is what wedged the run in the first place.
func TestReportRunResultStopsAfterBoundedReconciliationWithoutFailing(t *testing.T) {
	var mu sync.Mutex
	completes := 0
	failCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/complete":
			mu.Lock()
			completes++
			// A new head every time, so adopting one is never enough.
			seq := int64(9 + completes)
			mu.Unlock()
			writeHeadMismatch(w, 4711, seq)
		case "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/fail":
			mu.Lock()
			failCalls++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		}
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	d.reportRunResult(context.Background(), uuid.MustParse("00000000-0000-4000-8000-000000004711"), RunResult{
		Disposition: RunDispositionCompleted,
		Transcript:  RunTranscriptProof{ThroughSeq: 4},
	}, testLogger())

	mu.Lock()
	defer mu.Unlock()
	if want := terminalHeadReconcileAttempts + 1; completes != want {
		t.Fatalf("complete posts = %d, want %d (the first send plus its bounded re-proofs)", completes, want)
	}
	if failCalls != 0 {
		t.Fatalf("fail posts = %d; the fail proves the same prefix the complete could not", failCalls)
	}
}

// The item whose receipt never arrived is the reason a proof goes stale, and
// replaying it is the only way the daemon learns the item is committed — which
// is what lets a completion nominate it as the run's answer. Adopting the
// refusal's head alone would leave an older item nominated, and the server
// refuses that separately.
func TestReportTerminalRunReplaysTheUnacknowledgedItemBeforeReproving(t *testing.T) {
	var mu sync.Mutex
	var completes []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/complete" {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
			return
		}
		body := decodeDaemonRequestBody(t, r)
		mu.Lock()
		completes = append(completes, body)
		attempt := len(completes)
		mu.Unlock()
		if attempt == 1 {
			writeHeadMismatch(w, 4711, 9)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	replays := attachTranscriptReplayRPC(t, d, protocol.RunTranscriptAppendReceipt{
		ID:          "row-9",
		RunID:       uuid.MustParse("00000000-0000-4000-8000-000000004711"),
		ItemKey:     "item-9",
		Seq:         9,
		ThroughSeq:  9,
		ItemDigest:  replayItemDigest,
		CommittedAt: "2026-08-21T00:00:00Z",
	})

	pending := &protocol.RunTranscriptAppendRequest{
		RunID: uuid.MustParse(
			"00000000-0000-4000-8000-000000004711",
		),
		ItemKey: "item-9",
		Role:    protocol.RunItemRoleAgent,
		Parts:   []protocol.RunItemPart{{Type: protocol.RunItemPartTypeText, Text: "Done."}},
	}
	err := d.reportTerminalRun(context.Background(), terminalRunReport{
		kind:        terminalRunReportComplete,
		runID:       uuid.MustParse("00000000-0000-4000-8000-000000004711"),
		transcript:  RunTranscriptProof{ThroughSeq: 4, FinalItemKey: "item-7"},
		pendingItem: pending,
	})
	if err != nil {
		t.Fatalf("reportTerminalRun() error = %v", err)
	}

	if got := replays(); len(got) != 1 || got[0].ItemKey != "item-9" {
		t.Fatalf("transcript replays = %+v, want exactly one replay of item-9", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(completes) != 2 {
		t.Fatalf("complete posts = %d, want the refusal plus one re-proof", len(completes))
	}
	if completes[1]["throughSeq"] != float64(9) {
		t.Fatalf("re-proof = %v, want the replayed receipt's head", completes[1])
	}
	if completes[1]["finalItemKey"] != "item-9" {
		t.Fatalf("re-proof final item = %v, want the replayed agent text", completes[1]["finalItemKey"])
	}
}

// --- poller -----------------------------------------------------------------

func TestRunSlotSemaphore(t *testing.T) {
	sem := newRunSlotSemaphore(3)
	slots := drainAvailableSlots(sem, 3)
	if len(slots) != 3 {
		t.Fatalf("drained %d slots", len(slots))
	}
	if extra := drainAvailableSlots(sem, 1); len(extra) != 0 {
		t.Fatalf("over-drained: %v", extra)
	}
	sem <- slots[0]
	slot, acquired, _, err := waitForRunSlot(context.Background(), sem, nil, 10*time.Millisecond)
	if err != nil || !acquired || slot != slots[0] {
		t.Fatalf("slot = %d acquired = %v err = %v", slot, acquired, err)
	}
}

// --- wakeup -----------------------------------------------------------------

func TestRunWakeupURL(t *testing.T) {
	got, err := runWakeupURL("https://api.zenith-team.test", []string{"b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	want := "wss://api.zenith-team.test/api/v1/daemon/ws?runtime_ids=a%2Cb"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestDecodeRunContextLenient(t *testing.T) {
	doc := decodeRunContext(
		Run{Context: json.RawMessage(`{"prompt":"hi","agent":{"name":"Ada","thinkingLevel":"high"}}`)},
		testLogger(),
	)
	if doc.Prompt != "hi" || doc.Agent.Name != "Ada" || doc.Agent.ThinkingLevel != "high" {
		t.Fatalf("doc = %+v", doc)
	}
	if doc := decodeRunContext(Run{Context: json.RawMessage(`{broken`)}, testLogger()); doc.Prompt != "" {
		t.Fatalf("malformed context not degraded: %+v", doc)
	}
}

// zenith-team-mcp is handed the Run's isolated home, whose run.json opens the Run
// channel, and no credential: not the daemon's ZENITH_TEAM_HOME (where
// daemon-auth.json lives), and not the Run token, which only the daemon's
// forward carries.
func TestZenithTeamMcpConfigMergesAgentServers(t *testing.T) {
	d := newTestDaemon(t, "http://unreachable.invalid")
	d.cfg.MCPExecutable = "/opt/zenith-team-mcp"
	runHome := filepath.Join(t.TempDir(), ".zenith-team-home-71")
	merged := d.zenithTeamMcpConfig(
		json.RawMessage(`{"mcpServers":{"custom":{"command":"x"}}}`),
		Run{
			ID: uuid.MustParse(
				"00000000-0000-4000-8000-000000000071",
			),
			CompanyID:      "company-id",
			ConversationID: "conversation-id",
			AgentID:        "agent-id",
			TaskID:         "task-id",
			AuthToken:      "brt_tok",
		},
		[]string{"create_mail_drafts"},
		"/snap",
		"/scratch/run-71/company-plan",
		runHome,
		nil,
	)
	var doc struct {
		McpServers map[string]struct {
			Command string            `json:"command"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(merged, &doc); err != nil {
		t.Fatalf("merged config: %v", err)
	}
	if doc.McpServers["custom"].Command != "x" {
		t.Fatal("agent server lost in merge")
	}
	zenithTeam := doc.McpServers["zenith-team"]
	if _, leaked := zenithTeam.Env["ZENITH_TEAM_RUN_TOKEN"]; leaked {
		t.Fatalf("zenith-team-mcp was handed the Run token: %+v", zenithTeam.Env)
	}
	if strings.Contains(string(merged), "brt_tok") {
		t.Fatalf("the MCP config carries the Run token: %s", merged)
	}
	if zenithTeam.Command != "/opt/zenith-team-mcp" ||
		zenithTeam.Env["ZENITH_TEAM_HOME"] != runHome ||
		zenithTeam.Env["ZENITH_TEAM_COMPANY_ID"] != "company-id" ||
		zenithTeam.Env["ZENITH_TEAM_MCP_COMPANY_ID"] != "company-id" ||
		zenithTeam.Env["ZENITH_TEAM_MCP_CONVERSATION_ID"] != "conversation-id" ||
		zenithTeam.Env["ZENITH_TEAM_MCP_TASK_ID"] != "task-id" ||
		zenithTeam.Env["ZENITH_TEAM_MCP_RUN_ID"] != "00000000-0000-4000-8000-000000000071" ||
		zenithTeam.Env["ZENITH_TEAM_MCP_AGENT_ID"] != "agent-id" ||
		!strings.Contains(zenithTeam.Env["ZENITH_TEAM_MCP_ALLOWED_TOOLS"], "create_mail_drafts") ||
		strings.Contains(zenithTeam.Env["ZENITH_TEAM_MCP_ALLOWED_TOOLS"], "mark_task_failed") ||
		zenithTeam.Env["ZENITH_TEAM_MCP_CLAIM_ID"] != "" ||
		zenithTeam.Env["ZENITH_TEAM_SKILL_SNAPSHOT_DIR"] != "/snap" ||
		zenithTeam.Env["ZENITH_TEAM_MCP_PLAN_ROOT"] != "/scratch/run-71/company-plan" ||
		zenithTeam.Env["ZENITH_TEAM_MCP_WORKSPACE_ROOT"] != "" ||
		zenithTeam.Env["ZENITH_TEAM_MCP_APP_PREVIEW_ENDPOINT"] != "" ||
		zenithTeam.Env["ZENITH_TEAM_MCP_APP_PREVIEW_CAPABILITY"] != "" {
		t.Fatalf("zenith-team server entry = %+v", zenithTeam)
	}

	connectorOnly := d.zenithTeamMcpConfig(
		json.RawMessage(`{"mcpServers":{"connector":{"command":"connector"}}}`),
		Run{
			ID:             uuid.MustParse("00000000-0000-4000-8000-000000000072"),
			CompanyID:      "company-id",
			ConversationID: "conversation-id",
			AgentID:        "agent-id",
		},
		nil,
		"",
		"",
		runHome,
		nil,
	)
	doc.McpServers = nil
	if err := json.Unmarshal(connectorOnly, &doc); err != nil {
		t.Fatalf("connector-only config: %v", err)
	}
	if _, exists := doc.McpServers["zenith-team"]; exists || doc.McpServers["connector"].Command != "connector" {
		t.Fatalf("connector-only MCP config = %+v", doc.McpServers)
	}

	// No channel, no zenith-team server: zenith-team-mcp has no other way to reach
	// the server, and it never falls back to a bearer.
	agentServers := json.RawMessage(`{"mcpServers":{"custom":{"command":"x"}}}`)
	withoutChannel := d.zenithTeamMcpConfig(
		agentServers,
		Run{
			ID:             uuid.MustParse("00000000-0000-4000-8000-000000000073"),
			CompanyID:      "company-id",
			ConversationID: "conversation-id",
			AgentID:        "agent-id",
		},
		[]string{"create_mail_drafts"},
		"",
		"",
		"",
		nil,
	)
	if !bytes.Equal(withoutChannel, agentServers) {
		t.Fatalf("MCP config without a Run channel = %s, want the agent's own servers only", withoutChannel)
	}
}

// The MCP entry names the same isolated home whose run.json opens the
// channel, so zenith-team-mcp's channel is the Run's own; and a Run whose daemon
// has no local endpoint gets no channel and therefore no zenith-team server.
func TestZenithTeamMcpGetsTheRunChannelHomeOrNothing(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", filepath.Join(t.TempDir(), "daemon-home"))
	run := Run{
		ID:             uuid.MustParse("00000000-0000-4000-8000-000000000074"),
		CompanyID:      "01900000-0000-4000-8000-0000000000f0",
		AgentID:        "01900000-0000-4000-8000-0000000000f1",
		ConversationID: "01900000-0000-4000-8000-0000000000f2",
		AuthToken:      testChannelRunToken,
	}
	for name, endpoint := range map[string]string{
		"channel":    "http://127.0.0.1:19713",
		"no channel": "",
	} {
		t.Run(name, func(t *testing.T) {
			d := &Daemon{
				cfg:                  Config{MCPExecutable: "/opt/zenith-team-mcp"},
				logger:               testLogger(),
				zenithTeamExecutable: pinnedTestExecutable(t),
				localPlaneEndpoint:   endpoint,
			}
			cliEnvironment, err := d.prepareRunCLIEnvironment(
				run, RunContext{}, t.TempDir(), "", execenv.SeatbeltOptions{},
			)
			if err != nil {
				t.Fatalf("prepareRunCLIEnvironment() error = %v", err)
			}
			t.Cleanup(func() { _ = cliEnvironment.cleanup() })
			merged := d.zenithTeamMcpConfig(
				nil, run, zenithTeamMCPToolsForRun(RunContext{}), "", "", cliEnvironment.channelHome(),
				cliEnvironment.outputChannel,
			)
			if endpoint == "" {
				if merged != nil {
					t.Fatalf("a Run without a channel got MCP config %s", merged)
				}
				return
			}
			var doc struct {
				McpServers map[string]struct {
					Env map[string]string `json:"env"`
				} `json:"mcpServers"`
			}
			if err := json.Unmarshal(merged, &doc); err != nil {
				t.Fatalf("merged config: %v", err)
			}
			home := doc.McpServers["zenith-team"].Env["ZENITH_TEAM_HOME"]
			if home != cliEnvironment.zenithTeamHome || home == os.Getenv("ZENITH_TEAM_HOME") {
				t.Fatalf(
					"zenith-team-mcp ZENITH_TEAM_HOME = %q, want the Run's isolated home %q",
					home,
					cliEnvironment.zenithTeamHome,
				)
			}
			control, err := runchannel.LoadRunControlConfig(home)
			if err != nil || control.RunID != run.ID.String() {
				t.Fatalf("run.json in zenith-team-mcp's home = %+v, %v", control, err)
			}
		})
	}
}

func TestIsBlockedEnvKey(t *testing.T) {
	for _, blocked := range []string{"ZENITH_TEAM_RUN_TOKEN", "zenith_team_server_url", "HOME", "PATH", "CODEX_HOME", "TMPDIR"} {
		if !isBlockedEnvKey(blocked) {
			t.Fatalf("%q not blocked", blocked)
		}
	}
	for _, allowed := range []string{"ANTHROPIC_API_KEY", "MY_VAR"} {
		if isBlockedEnvKey(allowed) {
			t.Fatalf("%q blocked", allowed)
		}
	}
}

func TestSkillBundleCacheRoundTrip(t *testing.T) {
	cache := NewSkillBundleCache(t.TempDir())
	bundle, err := contentbundle.BuildSkill(
		"docs",
		"Write docs.",
		map[string][]byte{"SKILL.md": []byte("body"), "ref.md": []byte("r")},
	)
	if err != nil {
		t.Fatal(err)
	}
	bundle.ID, bundle.Source = "s1", "company"
	ref := contentbundle.SkillReference(bundle)
	if err := cache.Store("company-1", bundle); err != nil {
		t.Fatalf("store: %v", err)
	}
	got, ok := cache.Load("company-1", ref)
	if !ok || got.Content != "body" {
		t.Fatalf("load = %+v, %v", got, ok)
	}
	// A ref with a different hash misses.
	if _, ok := cache.Load("company-1", SkillRefData{ID: "s1", Source: "company", Hash: "h2"}); ok {
		t.Fatal("hash-mismatched load hit")
	}
	// Unsafe file paths invalidate the cached bundle.
	evil := bundle
	evil.Files = []SkillFileData{{Path: "../escape", Content: "x"}}
	if err := cache.Store("company-1", evil); err != nil {
		t.Fatalf("store evil: %v", err)
	}
	if _, ok := cache.Load("company-1", ref); ok {
		t.Fatal("bundle with unsafe path served from cache")
	}
}

// The enqueue kernel freezes the user's words into the run context, so the
// prompt is resolved from the claim and from nothing else. A resolver that
// reaches for the conversation API instead is how a run once answered "just
// tell me a joke" with a lecture about missing workspace context: the fetch
// decoded the wrong envelope, matched no item, and degraded in silence.
func TestResolveRunPromptReadsOnlyTheFrozenContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("prompt resolution called the server: %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}))
	defer server.Close()

	d := newTestDaemon(t, server.URL)
	run := Run{ID: uuid.MustParse("00000000-0000-4000-8000-000000004711"), ConversationID: "conv-1"}

	frozen := d.resolveRunPrompt(run, RunContext{Prompt: "  just tell me a joke  "}, testLogger())
	if frozen != "just tell me a joke" {
		t.Fatalf("prompt = %q, want the frozen context's words", frozen)
	}

	assignment := d.resolveRunPrompt(run, RunContext{ExecutionProfile: RunExecutionProfileContext{
		Source: "assign",
		Task:   &RunTaskContext{Title: "Ship the import", Description: "Preserve existing customers."},
	}}, testLogger())
	if assignment != "Ship the import\n---\nPreserve existing customers." {
		t.Fatalf("assignment prompt = %q, want frozen Task title and description", assignment)
	}

	// A direct Run with neither user words nor a Task profile degrades honestly
	// instead of pretending a claim-time lookup succeeded.
	degraded := d.resolveRunPrompt(run, RunContext{}, testLogger())
	if !strings.HasPrefix(degraded, "Continue working on the current conversation") {
		t.Fatalf("prompt = %q, want the generic continue instruction", degraded)
	}
}

// The Company Plan scratch is a per-Run directory under the environment root
// the daemon already owns for that Run, so a draft never lands in the
// repository the Agent works in.
func TestEnsureRunCompanyPlanRootLivesInTheRunEnvironment(t *testing.T) {
	envRoot := t.TempDir()
	planRoot, err := ensureRunCompanyPlanRoot(envRoot)
	if err != nil {
		t.Fatalf("ensureRunCompanyPlanRoot() error = %v", err)
	}
	if planRoot != filepath.Join(envRoot, "company-plan") {
		t.Fatalf("plan root = %q, want it under %q", planRoot, envRoot)
	}
	info, err := os.Stat(planRoot)
	if err != nil || !info.IsDir() {
		t.Fatalf("plan root %q: %v", planRoot, err)
	}
	if again, err := ensureRunCompanyPlanRoot(envRoot); err != nil || again != planRoot {
		t.Fatalf("second ensureRunCompanyPlanRoot() = %q, %v", again, err)
	}
}

func TestOutputMCPConfigRequiresBoundChannel(t *testing.T) {
	d := newTestDaemon(t, "http://unreachable.invalid")
	d.cfg.MCPExecutable = "/opt/zenith-team-mcp"
	run := Run{ID: uuid.New()}
	channelHome := t.TempDir()
	for _, channel := range []*runchannel.RunControlConfig{nil, {Endpoint: "http://127.0.0.1:1234", Capability: "run-capability"}} {
		config := d.zenithTeamMcpConfig(
			nil,
			run,
			[]string{"ask_human", "present_output"},
			"",
			"",
			channelHome,
			channel,
		)
		var decoded struct {
			Servers map[string]struct {
				Env map[string]string `json:"env"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal(config, &decoded); err != nil {
			t.Fatal(err)
		}
		env := decoded.Servers[zenithTeamMCPServerName].Env
		// The output tool reaches the channel through run.json in
		// ZENITH_TEAM_HOME, like every other tool; no capability in the env.
		if env["ZENITH_TEAM_HOME"] != channelHome {
			t.Fatalf(
				"ZENITH_TEAM_HOME = %q, want the Run channel home %q",
				env["ZENITH_TEAM_HOME"],
				channelHome,
			)
		}
		for name, value := range env {
			if strings.Contains(value, "run-capability") || strings.Contains(name, "CAPABILITY") ||
				strings.Contains(name, "RUN_ENDPOINT") {
				t.Fatalf("MCP child env carries the Run channel %s=%q", name, value)
			}
		}
		offered := strings.Contains(env["ZENITH_TEAM_MCP_ALLOWED_TOOLS"], "present_output")
		if channel == nil && offered {
			t.Fatal("advertised unbound output tool")
		}
		if channel != nil && !offered {
			t.Fatal("bound output channel did not offer present_output")
		}
	}
}
