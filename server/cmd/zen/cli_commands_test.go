package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Intelligent-Internet/zenith-team/server/internal/authfile"
	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon"
)

const testCompanyID = "0198aa00-0000-7000-8000-0000000000c0"

// seedSession points ZENITH_TEAM_HOME at a temp dir holding a valid credential
// for serverURL and a config selecting testCompanyID — the state `zenith-team
// login` plus `zen config set company_id` leaves behind.
func seedSession(t *testing.T, serverURL string) {
	t.Helper()
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	if err := daemon.SaveCLIConfig(daemon.CLIConfig{ServerURL: serverURL, CompanyID: testCompanyID}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	path, err := daemon.CredentialPath()
	if err != nil {
		t.Fatalf("credential path: %v", err)
	}
	accessExpiry := time.Now().Add(time.Hour)
	refreshExpiry := time.Now().Add(30 * 24 * time.Hour)
	err = authfile.Save(path, authfile.Credential{
		Version:          authfile.CurrentVersion,
		APIBaseURL:       serverURL + "/api/v1",
		AccessToken:      "access-cli",
		AccessExpiresAt:  &accessExpiry,
		RefreshToken:     "refresh-cli",
		RefreshExpiresAt: &refreshExpiry,
	})
	if err != nil {
		t.Fatalf("save credential: %v", err)
	}
}

func TestConfigSetAndShowRoundTrip(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())

	for _, set := range [][]string{
		{"set", "server_url", "http://127.0.0.1:9999"},
		{"set", "company_id", testCompanyID},
		{"set", "poll_interval", "45s"},
		{"set", "max_concurrent_runs", "7"},
		{"set", "agent_timeout", "0s"},
		{"set", "log_level", "DEBUG"},
	} {
		if _, err := executeCLI(t, newRootCmd, append([]string{"config"}, set...)...); err != nil {
			t.Fatalf("config %v: %v", set, err)
		}
	}

	out, err := executeCLI(t, newRootCmd, "config", "show")
	if err != nil {
		t.Fatalf("config show: %v", err)
	}
	for _, want := range []string{
		"http://127.0.0.1:9999", testCompanyID, "45s", "7", "0s (cap disabled)", "debug",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("config show output %q is missing %q", out, want)
		}
	}

	// The root --output flag applies here as everywhere: JSON is the saved
	// settings as one object, with the file they came from.
	out, err = executeCLI(t, newRootCmd, "config", "show", "--output", "json")
	if err != nil {
		t.Fatalf("config show --output json: %v", err)
	}
	var shown map[string]any
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("config show --output json printed %q, not JSON: %v", out, err)
	}
	if shown["serverUrl"] != "http://127.0.0.1:9999" || shown["companyId"] != testCompanyID ||
		shown["maxConcurrentRuns"] != float64(7) || !strings.HasSuffix(shown["file"].(string), "config.json") {
		t.Fatalf("config show json = %v, want the saved settings and the config file", shown)
	}
	if _, err := executeCLI(t, newRootCmd, "config", "show", "--output", "xml"); err == nil ||
		exitCodeFor(err) != exitValidation {
		t.Fatalf("config show --output xml error = %v, want the usage error", err)
	}

	// Clearing writes the zero value back and reports it.
	if out, err := executeCLI(t, newRootCmd, "config", "set", "poll_interval", ""); err != nil ||
		!strings.Contains(out, "Cleared") {
		t.Fatalf("clear poll_interval: out=%q err=%v", out, err)
	}
	cfg, err := daemon.LoadCLIConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.PollInterval != "" || cfg.MaxConcurrentRuns != 7 {
		t.Fatalf("config = %+v, want poll_interval cleared and max_concurrent_runs kept", cfg)
	}
	// Persisted normalized, so `config show` and the daemon's own ladder can
	// never disagree about which level was set.
	if cfg.LogLevel != "debug" {
		t.Fatalf("log level = %q, want the normalized spelling of DEBUG", cfg.LogLevel)
	}
}

func TestConfigSetRejectsInvalidValues(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	for _, invalid := range [][]string{
		{"set", "poll_interval", "soon"},
		{"set", "poll_interval", "0s"},
		{"set", "company_id", "not-a-uuid"},
		{"set", "max_concurrent_runs", "0"},
		{"set", "log_level", "verbose"},
		{"set", "no_such_key", "x"},
	} {
		if _, err := executeCLI(t, newRootCmd, append([]string{"config"}, invalid...)...); err == nil {
			t.Fatalf("config %v was accepted", invalid)
		}
	}
}

// daemonOverrides feeds `daemon start`: a flag beats the env, the env beats
// config.json, and config.json beats the built-in default.
func TestDaemonOverridesHonorTheFlagEnvConfigLadder(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	t.Setenv("ZENITH_TEAM_DEVICE_NAME", "env-box")
	err := daemon.SaveCLIConfig(daemon.CLIConfig{
		PollInterval:      "45s",
		DeviceName:        "config-box",
		MaxConcurrentRuns: 7,
		AgentTimeout:      "0s",
	})
	if err != nil {
		t.Fatalf("save config: %v", err)
	}

	start := findSubcommand(t, newDaemonCmd(), "start")
	if err := start.Flags().Set("heartbeat-interval", "5s"); err != nil {
		t.Fatalf("set flag: %v", err)
	}

	overrides, err := daemonOverrides(start)
	if err != nil {
		t.Fatalf("daemonOverrides: %v", err)
	}
	if overrides.HeartbeatInterval != 5*time.Second {
		t.Fatalf("heartbeat = %s, want the flag value", overrides.HeartbeatInterval)
	}
	if overrides.PollInterval != 45*time.Second {
		t.Fatalf("poll = %s, want the config value", overrides.PollInterval)
	}
	if overrides.DeviceName != "" {
		t.Fatalf(
			"device name override = %q, want empty so the env wins inside LoadConfig",
			overrides.DeviceName,
		)
	}
	if overrides.MaxConcurrentRuns != 7 {
		t.Fatalf("max concurrent runs = %d, want the config value", overrides.MaxConcurrentRuns)
	}
	if overrides.AgentTimeout == nil || *overrides.AgentTimeout != 0 {
		t.Fatalf("agent timeout = %v, want an explicit 0 (cap disabled)", overrides.AgentTimeout)
	}
}

func TestDaemonOverridesRejectACorruptConfigDuration(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	if err := daemon.SaveCLIConfig(daemon.CLIConfig{PollInterval: "soon"}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	start := findSubcommand(t, newDaemonCmd(), "start")
	if _, err := daemonOverrides(start); err == nil ||
		!strings.Contains(err.Error(), "poll_interval") {
		t.Fatalf("daemonOverrides error = %v, want the corrupt key named", err)
	}
}

// The daemon's verbosity walks the same ladder as its config knobs, and the
// WS-first claim ladder logs at debug — without this knob a stuck run cannot
// be diagnosed from the daemon log at all.
func TestDaemonLogLevelHonorsTheFlagEnvConfigLadder(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())

	// Nothing set anywhere: the built-in default.
	start := findSubcommand(t, newDaemonCmd(), "start")
	if level, err := daemonLogLevel(start); err != nil || level != slog.LevelInfo {
		t.Fatalf("bare level = %v (err %v), want the info default", level, err)
	}

	if err := daemon.SaveCLIConfig(daemon.CLIConfig{LogLevel: "warn"}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if level, err := daemonLogLevel(start); err != nil || level != slog.LevelWarn {
		t.Fatalf("config level = %v (err %v), want the config value", level, err)
	}

	t.Setenv("ZENITH_TEAM_LOG_LEVEL", "error")
	if level, err := daemonLogLevel(start); err != nil || level != slog.LevelError {
		t.Fatalf("env level = %v (err %v), want the env to beat config.json", level, err)
	}

	if err := start.Flags().Set("log-level", "DEBUG"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if level, err := daemonLogLevel(start); err != nil || level != slog.LevelDebug {
		t.Fatalf("flag level = %v (err %v), want the flag to beat the env", level, err)
	}
}

func TestDaemonLogLevelRejectsAnUnknownLevel(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())

	start := findSubcommand(t, newDaemonCmd(), "start")
	if err := start.Flags().Set("log-level", "verbose"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if _, err := daemonLogLevel(start); err == nil ||
		!strings.Contains(err.Error(), "--log-level") {
		t.Fatalf("error = %v, want the offending source named", err)
	}

	// A level persisted by an older or hand-edited config.json is named too,
	// so the daemon does not start with a silently ignored setting.
	fresh := findSubcommand(t, newDaemonCmd(), "start")
	if err := daemon.SaveCLIConfig(daemon.CLIConfig{LogLevel: "chatty"}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if _, err := daemonLogLevel(fresh); err == nil ||
		!strings.Contains(err.Error(), "config log_level") {
		t.Fatalf("error = %v, want the config key named", err)
	}
}

// `daemon start` re-execs itself detached, so a flag that is not forwarded is
// a flag the daemon never sees.
func TestForwardedStartFlagsSurviveTheDetachedReExec(t *testing.T) {
	start := findSubcommand(t, newDaemonCmd(), "start")
	for _, set := range [][2]string{
		{"foreground", "true"},
		{"log-level", "debug"},
		{"poll-interval", "5s"},
	} {
		if err := start.Flags().Set(set[0], set[1]); err != nil {
			t.Fatalf("set %s: %v", set[0], err)
		}
	}

	forwarded := strings.Join(forwardedStartFlags(start), " ")
	if !strings.Contains(forwarded, "--log-level=debug") {
		t.Fatalf("forwarded flags %q drop the log level", forwarded)
	}
	if !strings.Contains(forwarded, "--poll-interval=5s") {
		t.Fatalf("forwarded flags %q drop poll-interval", forwarded)
	}
	// --foreground is the re-exec's own doing, never inherited.
	if strings.Contains(forwarded, "foreground") {
		t.Fatalf("forwarded flags %q re-forward --foreground", forwarded)
	}
}

func findSubcommand(t *testing.T, parent *cobra.Command, name string) *cobra.Command {
	t.Helper()
	for _, sub := range parent.Commands() {
		if sub.Name() == name {
			return sub
		}
	}
	t.Fatalf("command %q not found", name)
	return nil
}

func TestAgentListRendersTheCompanyAgents(t *testing.T) {
	var authorization, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/companies/"+testCompanyID+"/agents" {
			http.NotFound(w, r)
			return
		}
		authorization = r.Header.Get("Authorization")
		query = r.URL.RawQuery
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{
					"id":       "0198aa00-0000-7000-8000-0000000000a1",
					"name":     "Scout",
					"title":    "Researcher",
					"provider": "claude",
					"model":    "claude-sonnet-5",
					"reportsTo": map[string]any{
						"id":   "0198aa00-0000-7000-8000-0000000000a0",
						"name": "Andrew",
					},
					"binding": map[string]any{
						"id":      "0198aa00-0000-7000-8000-0000000000c1",
						"agentId": "0198aa00-0000-7000-8000-0000000000a1",
						"runtime": map[string]any{
							"id":       "0198aa00-0000-7000-8000-0000000000b1",
							"name":     "Runtime A",
							"provider": "claude",
							"status":   "online",
						},
						"createdAt": "2026-09-22T00:00:00Z",
					},
					"runtimeAvailable": true,
					"archivedAt":       nil,
					"revision":         3,
				},
			},
		})
	}))
	defer srv.Close()
	seedSession(t, srv.URL)

	out, err := executeCLI(t, newRootCmd, "agent", "list")
	if err != nil {
		t.Fatalf("agent list: %v", err)
	}
	if authorization != "Bearer access-cli" {
		t.Fatalf("authorization = %q, want the stored access token", authorization)
	}
	// The Agent collection is bounded: no limit, no cursor.
	if query != "" {
		t.Fatalf("query = %q, want no query parameters", query)
	}
	// The runtime column is the caller's own setup, by name (ADR 0014).
	for _, want := range []string{
		"Scout", "Researcher", "Andrew", "claude", "claude-sonnet-5", "Runtime A",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("agent table %q is missing %q", out, want)
		}
	}
	if _, err := executeCLI(t, newRootCmd, "agent", "list", "--include-archived"); err != nil {
		t.Fatalf("agent list --include-archived: %v", err)
	}
	if query != "include_archived=true" {
		t.Fatalf("query = %q, want the documented include_archived filter", query)
	}
}

// An Agent is created as a template for one provider, and the create may set
// it up on one of the caller's own runtimes at the same time (ADR 0014).
func TestAgentCreateNamesTheProviderAndLeavesIDToTheDatabase(t *testing.T) {
	var body map[string]any
	var idempotencyKey string
	const createdAgentID = "0198aa00-0000-7000-8000-0000000000a1"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost ||
			r.URL.Path != "/api/v1/companies/"+testCompanyID+"/agents" {
			http.NotFound(w, r)
			return
		}
		idempotencyKey = r.Header.Get("Idempotency-Key")
		// Decoding into a live map merges keys; each request must be read on
		// its own.
		body = nil
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode create body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": createdAgentID})
	}))
	defer srv.Close()
	seedSession(t, srv.URL)

	reportsTo := "0198aa00-0000-7000-8000-0000000000a2"
	runtimeID := "0198aa00-0000-7000-8000-0000000000b2"
	out, err := executeCLI(t, newRootCmd, "agent", "create",
		"--name", "Scout", "--title", "Researcher", "--provider", "codex",
		"--reports-to", reportsTo, "--runtime-id", runtimeID)
	if err != nil {
		t.Fatalf("agent create: %v", err)
	}

	if idempotencyKey != "" {
		t.Fatalf("Idempotency-Key = %q, want no custom create key", idempotencyKey)
	}
	if _, present := body["id"]; present {
		t.Fatalf("create body = %v, Agent id must be database-generated", body)
	}
	if body["reportsToAgentId"] != reportsTo || body["runtimeId"] != runtimeID ||
		body["provider"] != "codex" {
		t.Fatalf("create body = %v, want the required fields", body)
	}
	// Without a runtime the Agent is a template nobody runs yet.
	if _, err := executeCLI(t, newRootCmd, "agent", "create",
		"--name", "Scout", "--title", "Researcher", "--provider", "codex"); err != nil {
		t.Fatalf("agent create without a runtime: %v", err)
	}
	if _, present := body["runtimeId"]; present {
		t.Fatalf("create body = %v, want no runtime when none was given", body)
	}
	if _, err := executeCLI(t, newRootCmd, "agent", "create",
		"--name", "Scout", "--title", "Researcher", "--provider", "gemini"); err == nil {
		t.Fatal("agent create --provider gemini was accepted")
	}
	// model/thinkingLevel must be present-but-null: the API requires the
	// keys, and null means "use the runtime default".
	for _, key := range []string{"model", "thinkingLevel"} {
		if value, present := body[key]; !present || value != nil {
			t.Fatalf("create body %s = %v (present=%t), want an explicit null", key, value, present)
		}
	}
	if !strings.Contains(out, createdAgentID) {
		t.Fatalf("output %q does not echo the created agent", out)
	}
}

func TestAgentUpdateUsesTheCurrentRevision(t *testing.T) {
	agentID := "0198aa00-0000-7000-8000-0000000000a3"
	var patch map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/companies/"+testCompanyID+"/agents/"+agentID {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{"id": agentID, "revision": 7})
		case http.MethodPatch:
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				t.Fatalf("decode patch body: %v", err)
			}
			json.NewEncoder(w).Encode(map[string]any{"id": agentID, "revision": 8})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	seedSession(t, srv.URL)

	_, err := executeCLI(t, newRootCmd, "agent", "update", agentID, "--name", "Scout II", "--model", "")
	if err != nil {
		t.Fatalf("agent update: %v", err)
	}
	if patch["expectedRevision"] != float64(7) {
		t.Fatalf("expectedRevision = %v, want the fetched revision", patch["expectedRevision"])
	}
	if patch["name"] != "Scout II" {
		t.Fatalf("name = %v, want the flag value", patch["name"])
	}
	if value, present := patch["model"]; !present || value != nil {
		t.Fatalf("model = %v (present=%t), want an explicit null clearing the override", value, present)
	}
	if _, present := patch["title"]; present {
		t.Fatalf("patch = %v, want untouched fields absent", patch)
	}

	if _, err := executeCLI(t, newRootCmd, "agent", "update", agentID); err == nil ||
		!strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("flagless update error = %v, want guidance", err)
	}
}

func TestAgentArchiveUsesTheCurrentRevision(t *testing.T) {
	agentID := "0198aa00-0000-7000-8000-0000000000a4"
	var archived map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/companies/" + testCompanyID + "/agents/" + agentID:
			json.NewEncoder(w).Encode(map[string]any{"id": agentID, "revision": 4})
		case "/api/v1/companies/" + testCompanyID + "/agents/" + agentID + "/archive":
			if err := json.NewDecoder(r.Body).Decode(&archived); err != nil {
				t.Fatalf("decode archive body: %v", err)
			}
			json.NewEncoder(w).Encode(map[string]any{"id": agentID})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	seedSession(t, srv.URL)

	if _, err := executeCLI(t, newRootCmd, "agent", "archive", agentID); err != nil {
		t.Fatalf("agent archive: %v", err)
	}
	if archived["expectedRevision"] != float64(4) {
		t.Fatalf("expectedRevision = %v, want the fetched revision", archived["expectedRevision"])
	}
}

// runtimeListBody is the real `{items:[Runtime]}` page the server sends for
// GET /companies/{companyId}/runtimes (docs/api/runs.md): one runtime with an
// owner and a last heartbeat, one with neither.
func runtimeListBody() map[string]any {
	return map[string]any{
		"items": []map[string]any{{
			"id": "0198aa00-0000-7000-8000-0000000000b1", "companyId": testCompanyID,
			"daemonId": "8f1c0b6a-3d92-4d1e-9a77-2b5f0c8e1d34", "name": "macbook claude",
			"provider": "claude", "status": "online",
			"owner": map[string]any{
				"id": "0198aa00-0000-7000-8000-0000000000a1", "kind": "user",
				"name": "Alex Rivera", "avatarUrl": nil,
			},
			"customName": "Office Mac",
			"signIn": map[string]any{
				"state":     "signed_out",
				"detail":    "Not signed in. Run `claude auth login` on this computer, then refresh.",
				"checkedAt": "2026-08-14T10:00:00Z",
			},
			"deviceInfo": "macOS 15.5 (arm64)", "lastSeenAt": "2026-08-14T10:00:00Z",
			"createdAt": "2026-08-14T09:00:00Z", "updatedAt": "2026-08-14T10:00:00Z",
		}, {
			"id": "0198aa00-0000-7000-8000-0000000000b2", "companyId": testCompanyID,
			"daemonId": "8f1c0b6a-3d92-4d1e-9a77-2b5f0c8e1d34", "name": "macbook codex",
			"provider": "codex", "status": "offline", "owner": nil, "customName": nil,
			"deviceInfo": "macOS 15.5 (arm64)", "lastSeenAt": nil,
			"createdAt": "2026-08-14T09:00:00Z", "updatedAt": "2026-08-14T09:30:00Z",
		}},
	}
}

func serveRuntimeList(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := runtimeListBody()
		items := page["items"].([]map[string]any)
		for _, item := range items {
			if r.URL.Path == "/api/v1/companies/"+testCompanyID+"/runtimes/"+item["id"].(string) {
				_ = json.NewEncoder(w).Encode(item)
				return
			}
		}
		if r.URL.Path != "/api/v1/companies/"+testCompanyID+"/runtimes" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	t.Cleanup(srv.Close)
	seedSession(t, srv.URL)
	return srv
}

func TestRuntimeListRendersTheCompanyRuntimes(t *testing.T) {
	serveRuntimeList(t)

	out, err := executeCLI(t, newRootCmd, "runtime", "list")
	if err != nil {
		t.Fatalf("runtime list: %v", err)
	}
	for _, want := range []string{
		"ID", "NAME", "PROVIDER", "STATUS", "SIGN_IN", "OWNER", "LAST_SEEN",
		"claude", "online", "signed_out", "Alex Rivera", "2026-08-14T10:00:00Z",
		"macbook codex", "codex", "offline",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("runtime table %q is missing %q", out, want)
		}
	}
	// A machine somebody renamed shows that name here too, exactly as the
	// roster and every picker do.
	// The daemon's own name is what the custom one replaces, not a second
	// column, so it must be gone from the table.
	if !strings.Contains(out, "Office Mac") {
		t.Fatalf("runtime table %q does not show the machine's custom name", out)
	}
	if strings.Contains(out, "macbook claude") {
		t.Fatalf("runtime table %q still shows the daemon's name for a renamed machine", out)
	}
	for _, stale := range []string{"MODE", "runtimeMode"} {
		if strings.Contains(out, stale) {
			t.Fatalf("runtime table %q still prints the retired %q column", out, stale)
		}
	}
	// The codex runtime has no owner and never heartbeat: both cells are "-".
	var codexRow string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "macbook codex") {
			codexRow = line
		}
	}
	if codexRow == "" || strings.Count(codexRow, "-") < 2 {
		t.Fatalf("codex row %q should render \"-\" for the missing owner and last seen", codexRow)
	}
}

// `runtime get` prints the daemon's verdict with its sentence and the
// moment it was checked — the wire's own RuntimeSignIn, not a re-spelling
// that drops the check time.
func TestRuntimeGetShowsTheSignInVerdictWithItsCheckTime(t *testing.T) {
	serveRuntimeList(t)

	out, err := executeCLI(t, newRootCmd, "runtime", "get",
		"0198aa00-0000-7000-8000-0000000000b1", "--output", "table")
	if err != nil {
		t.Fatalf("runtime get: %v", err)
	}
	wantSignIn := "signed_out — Not signed in. Run `claude auth login` on this computer, then refresh. " +
		"(checked 2026-08-14T10:00:00Z)"
	if !strings.Contains(out, wantSignIn) {
		t.Fatalf("runtime get sign-in line missing the verdict, sentence and check time:\n%s", out)
	}
	out, err = executeCLI(t, newRootCmd, "runtime", "get",
		"0198aa00-0000-7000-8000-0000000000b2", "--output", "table")
	if err != nil || !strings.Contains(out, "sign-in") {
		t.Fatalf("runtime get (no verdict) = %q, %v", out, err)
	}
	if !strings.Contains(out, "sign-in:      -") {
		t.Fatalf("runtime get without a verdict should print a dash:\n%s", out)
	}
}

func TestRuntimeListJSONPrintsTheServerBody(t *testing.T) {
	serveRuntimeList(t)

	out, err := executeCLI(t, newRootCmd, "runtime", "list", "--output", "json")
	if err != nil {
		t.Fatalf("runtime list --output json: %v", err)
	}
	var printed map[string]any
	if err := json.Unmarshal([]byte(out), &printed); err != nil {
		t.Fatalf("decode printed json %q: %v", out, err)
	}
	// printJSON re-indents, so the passthrough is proved by value, not bytes.
	served, err := json.Marshal(runtimeListBody())
	if err != nil {
		t.Fatalf("marshal served body: %v", err)
	}
	var want map[string]any
	if err := json.Unmarshal(served, &want); err != nil {
		t.Fatalf("decode served body: %v", err)
	}
	if !reflect.DeepEqual(printed, want) {
		t.Fatalf("json output = %v, want the served body %v", printed, want)
	}
}

func TestAPIClientGuidesWhenLoginOrCompanyIsMissing(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	if _, err := executeCLI(t, newRootCmd, "agent", "list"); err == nil ||
		!strings.Contains(err.Error(), "zen auth login") {
		t.Fatalf("unauthenticated error = %v, want login guidance", err)
	}

	// Authenticated but no company selected anywhere.
	seedSession(t, "http://127.0.0.1:1")
	if err := daemon.SaveCLIConfig(daemon.CLIConfig{ServerURL: "http://127.0.0.1:1"}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if _, err := executeCLI(t, newRootCmd, "runtime", "list"); err == nil ||
		!strings.Contains(err.Error(), "zen company use") {
		t.Fatalf("company-less error = %v, want company guidance", err)
	}
}

func TestAgentCreateSendsTheOptionalCapAndRejectsAnOutOfRangeOneLocally(t *testing.T) {
	var body map[string]any
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body = nil
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode create body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": "0198aa00-0000-7000-8000-0000000000a9"})
	}))
	defer srv.Close()
	seedSession(t, srv.URL)
	runtimeID := "0198aa00-0000-7000-8000-0000000000b2"

	if _, err := executeCLI(t, newRootCmd, "agent", "create",
		"--name", "Scout", "--title", "Researcher", "--provider", "codex", "--runtime-id", runtimeID,
		"--max-concurrent-runs", "11"); err == nil || !strings.Contains(err.Error(), "between 1 and 10") {
		t.Fatalf("agent create --max-concurrent-runs 11 error = %v, want the local 1..10 rule", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want none for a cap the CLI refuses itself", requests)
	}

	if _, err := executeCLI(t, newRootCmd, "agent", "create",
		"--name", "Scout", "--title", "Researcher", "--provider", "codex", "--runtime-id", runtimeID,
		"--max-concurrent-runs", "5"); err != nil {
		t.Fatalf("agent create: %v", err)
	}
	if body["maxConcurrentRuns"] != float64(5) {
		t.Fatalf("create body = %v, want maxConcurrentRuns 5", body)
	}
	// The cap is optional: an untouched flag sends no key, so the server
	// applies its own default.
	if _, err := executeCLI(t, newRootCmd, "agent", "create",
		"--name", "Scout", "--title", "Researcher", "--provider", "codex",
		"--runtime-id", runtimeID); err != nil {
		t.Fatalf("agent create: %v", err)
	}
	if _, present := body["maxConcurrentRuns"]; present {
		t.Fatalf("create body = %v, want no maxConcurrentRuns key when the flag is not passed", body)
	}
}

func TestAgentCopyBuildsAnOrdinaryCreateFromTheSource(t *testing.T) {
	const sourceID = "0198aa00-0000-7000-8000-0000000000a5"
	const managerID = "0198aa00-0000-7000-8000-0000000000a2"
	const sourceRuntimeID = "0198aa00-0000-7000-8000-0000000000b2"
	const otherRuntimeID = "0198aa00-0000-7000-8000-0000000000b3"
	source := map[string]any{
		"id": sourceID, "name": "Scout", "title": "Researcher",
		"instructions": "Investigate carefully.",
		"reportsTo":    map[string]any{"id": managerID, "name": "Andrew"},
		"binding": map[string]any{
			"id":      "0198aa00-0000-7000-8000-0000000000c5",
			"agentId": sourceID,
			"runtime": map[string]any{
				"id": sourceRuntimeID, "name": "codex (Mac)",
				"provider": "codex", "status": "online",
			},
			"createdAt": "2026-09-22T00:00:00Z",
		},
		"provider":          "codex",
		"model":             "gpt-5-codex",
		"thinkingLevel":     "high",
		"avatarKey":         "fold-periwinkle",
		"accentColor":       "#58B8F2",
		"maxConcurrentRuns": 4, "revision": 3,
	}
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/companies/"+testCompanyID+"/agents/"+sourceID:
			json.NewEncoder(w).Encode(source)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/companies/"+testCompanyID+"/agents":
			body = nil
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).
				Encode(map[string]any{"id": "0198aa00-0000-7000-8000-0000000000a6", "name": body["name"]})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	seedSession(t, srv.URL)

	// Same provider, no flags: every portable field travels, the name gets
	// the copy suffix, the copy reports to the source's manager, and it is
	// set up on the same computer of the caller's (ADR 0014).
	out, err := executeCLI(t, newRootCmd, "agent", "copy", sourceID)
	if err != nil {
		t.Fatalf("agent copy: %v", err)
	}
	if _, present := body["id"]; present {
		t.Fatalf("copy body = %v, the id must be database-generated", body)
	}
	want := map[string]any{
		"name": "Scout (copy)", "title": "Researcher", "instructions": "Investigate carefully.",
		"reportsToAgentId": managerID, "runtimeId": sourceRuntimeID, "provider": "codex",
		"model": "gpt-5-codex", "thinkingLevel": "high",
		"avatarKey": "fold-periwinkle", "accentColor": "#58B8F2", "maxConcurrentRuns": float64(4),
	}
	for key, value := range want {
		if body[key] != value {
			t.Fatalf("copy body[%s] = %v, want %v (body %v)", key, body[key], value, body)
		}
	}
	if !strings.Contains(out, "Scout (copy)") {
		t.Fatalf("output %q does not echo the created copy", out)
	}

	// Copying onto the other provider is how an Agent changes provider: the
	// provider-specific overrides go, and the explicit flags stand.
	if _, err := executeCLI(t, newRootCmd, "agent", "copy", sourceID,
		"--provider", "claude", "--runtime-id", otherRuntimeID, "--name", "Scout on Claude",
		"--avatar-key", "", "--max-concurrent-runs", "2"); err != nil {
		t.Fatalf("agent copy --provider: %v", err)
	}
	for _, key := range []string{"model", "thinkingLevel", "avatarKey"} {
		if value, present := body[key]; !present || value != nil {
			t.Fatalf("copy body %s = %v (present=%t), want an explicit null on a fork", key, value, present)
		}
	}
	if body["runtimeId"] != otherRuntimeID || body["provider"] != "claude" ||
		body["name"] != "Scout on Claude" ||
		body["maxConcurrentRuns"] != float64(2) || body["accentColor"] != "#58B8F2" {
		t.Fatalf("fork body = %v", body)
	}

	// A copy of the root reports to the root itself: a company has one.
	delete(source, "reportsTo")
	source["reportsTo"] = nil
	if _, err := executeCLI(t, newRootCmd, "agent", "copy", sourceID, "--thinking-level", ""); err != nil {
		t.Fatalf("agent copy (root): %v", err)
	}
	if body["reportsToAgentId"] != sourceID || body["thinkingLevel"] != nil || body["model"] != "gpt-5-codex" {
		t.Fatalf("root copy body = %v, want reportsTo the root, the cleared level and the copied model", body)
	}
}

// The usage route takes exactly start_at, end_at, time_zone and bucket, each
// an exact local midnight in the zone (docs/api/activity-and-usage.md). The
// CLI used to send camelCase names at the current instant, which the server
// refused every time.
func TestAgentUsageSendsSnakeCaseLocalMidnightBounds(t *testing.T) {
	agentID := "0198aa00-0000-7000-8000-0000000000a7"
	var query url.Values
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/companies/"+testCompanyID+"/agents/"+agentID+"/usage" {
			http.NotFound(w, r)
			return
		}
		requests++
		query = r.URL.Query()
		json.NewEncoder(w).Encode(map[string]any{
			"totals": map[string]any{"inputTokens": "91743", "outputTokens": "1200", "runCount": "1"},
			"series": []map[string]any{},
			"models": []map[string]any{{
				"provider": "claude", "model": "claude-sonnet-5",
				"inputTokens": "91743", "outputTokens": "1200", "providerCostUsdTicks": "12500000000",
			}},
			"topTasks": []map[string]any{},
		})
	}))
	defer srv.Close()
	seedSession(t, srv.URL)

	out, err := executeCLI(t, newRootCmd, "agent", "usage", agentID,
		"--days", "7", "--time-zone", "Asia/Ho_Chi_Minh", "--bucket", "day")
	if err != nil {
		t.Fatalf("agent usage: %v", err)
	}
	if len(query) != 4 {
		t.Fatalf("query = %v, want exactly start_at, end_at, time_zone and bucket", query)
	}
	if query.Get("time_zone") != "Asia/Ho_Chi_Minh" || query.Get("bucket") != "day" {
		t.Fatalf("query = %v, want the flag values under the documented names", query)
	}
	location, err := time.LoadLocation("Asia/Ho_Chi_Minh")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	bounds := make(map[string]time.Time, 2)
	for _, key := range []string{"start_at", "end_at"} {
		value, err := time.Parse(time.RFC3339, query.Get(key))
		if err != nil {
			t.Fatalf("%s = %q, want RFC 3339: %v", key, query.Get(key), err)
		}
		local := value.In(location)
		if local.Hour() != 0 || local.Minute() != 0 || local.Second() != 0 || local.Nanosecond() != 0 {
			t.Fatalf("%s = %s, want exact local midnight in the zone", key, local)
		}
		bounds[key] = value
	}
	if got := bounds["end_at"].Sub(bounds["start_at"]); got != 7*24*time.Hour {
		t.Fatalf("window = %s, want --days worth of days", got)
	}
	// The window ends at the next local midnight so today counts.
	if now := time.Now().In(location); !bounds["end_at"].After(now) {
		t.Fatalf("end_at = %s is not after now (%s), so today's usage would be missing", bounds["end_at"], now)
	}
	for _, want := range []string{"claude", "claude-sonnet-5", "91743", "1.25", "runs: 1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("usage table %q is missing %q", out, want)
		}
	}

	// A bad zone or bucket is a usage error the CLI catches before any request.
	for _, args := range [][]string{
		{"--time-zone", "Mars/Olympus"},
		{"--time-zone", "Local"},
		{"--bucket", "week"},
	} {
		invocation := append([]string{"agent", "usage", agentID}, args...)
		if _, err := executeCLI(t, newRootCmd, invocation...); err == nil {
			t.Fatalf("agent usage %v was accepted", args)
		}
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want only the valid call to reach the server", requests)
	}
}

// A transcript row carries `role` and `parts` (RunTranscriptEntry), and the
// next page continues from the last returned row's seq, not from
// throughSeq, which is the conversation head and would skip rows.
func TestConversationTranscriptRendersRolePartsAndContinuesFromTheLastRow(t *testing.T) {
	const conversationID = "0198aa00-0000-7000-8000-00000000e001"
	const runID = "0198aa00-0000-7000-8000-00000000f001"
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/companies/"+testCompanyID+"/conversations/"+conversationID+"/transcript" {
			http.NotFound(w, r)
			return
		}
		query = r.URL.RawQuery
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{
					"id":      "0198aa00-0000-7000-8000-00000000e101",
					"runId":   runID,
					"seq":     4,
					"itemKey": "msg_0cf5",
					"role":    "agent",
					"parts": []map[string]any{
						{"type": "text", "text": "Reading the revenue file"},
					},
					"itemDigest": strings.Repeat("a", 64),
					"createdAt":  "2026-09-17T00:00:00Z",
				},
				{
					"id":      "0198aa00-0000-7000-8000-00000000e102",
					"runId":   runID,
					"seq":     6,
					"itemKey": "exec-dd5b",
					"role":    "tool",
					"parts": []map[string]any{{
						"type": "toolResult", "callId": "call-1", "name": "read_file",
						"output": "week,revenue", "status": "completed",
					}},
					"itemDigest": strings.Repeat("b", 64),
					"createdAt":  "2026-09-17T00:00:01Z",
				},
			},
			"throughSeq": 31,
			"hasMore":    true,
		})
	}))
	defer srv.Close()
	seedSession(t, srv.URL)
	t.Setenv("ZENITH_TEAM_RUN_ID", "")
	t.Setenv("ZENITH_TEAM_RUN_TOKEN", "")

	out, err := executeCLI(t, newRootCmd, "conversation", "transcript", conversationID, "--limit", "2")
	if err != nil {
		t.Fatalf("conversation transcript: %v", err)
	}
	if query != "limit=2" {
		t.Fatalf("query = %q, want the limit only", query)
	}
	for _, want := range []string{"ROLE", "PARTS", "agent", "tool", "msg_0cf5", "exec-dd5b", "Reading the revenue file", "read_file"} {
		if !strings.Contains(out, want) {
			t.Fatalf("transcript table %q is missing %q", out, want)
		}
	}
	if !strings.Contains(out, "--after-seq 6") || strings.Contains(out, "--after-seq 31") {
		t.Fatalf("transcript hint in %q should name the last returned row's seq (6), not throughSeq (31)", out)
	}
}

// The schedule page is `{schedules, nextCursor}` (SchedulePage) and each
// schedule carries nextRunAt/lastRunAt; the table used to decode `items`
// with occurrence names that are not on the wire, so it was always empty.
func TestScheduleListRendersTheCompanySchedules(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/companies/"+testCompanyID+"/schedules" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"schedules": []map[string]any{
				{
					"id":           "0198aa00-0000-7000-8000-00000000e201",
					"agentId":      "0198aa00-0000-7000-8000-0000000000a1",
					"name":         "Morning digest",
					"instructions": "Summarize overnight activity.",
					"presetKey":    nil,
					"provider":     "claude",
					"trigger": map[string]any{
						"type":       "cron",
						"expression": "0 8 * * 1-5",
						"timeZone":   "UTC",
					},
					"sources":     []map[string]any{},
					"status":      "active",
					"pauseReason": nil,
					"nextRunAt":   "2026-09-18T08:00:00Z",
					"lastRunAt":   "2026-09-17T08:00:00Z",
					"revision":    3,
					"createdAt":   "2026-09-01T00:00:00Z",
					"updatedAt":   "2026-09-17T08:00:00Z",
				},
				{
					"id":           "0198aa00-0000-7000-8000-00000000e202",
					"agentId":      "0198aa00-0000-7000-8000-0000000000a1",
					"name":         "One-off review",
					"instructions": "Review the launch post.",
					"presetKey":    nil,
					"provider":     "claude",
					"trigger":      map[string]any{"type": "once", "at": "2026-09-20T09:00:00Z"},
					"sources":      []map[string]any{},
					"status":       "paused",
					"pauseReason":  "runtime_offline",
					"nextRunAt":    nil,
					"lastRunAt":    nil,
					"revision":     1,
					"createdAt":    "2026-09-16T00:00:00Z",
					"updatedAt":    "2026-09-16T00:00:00Z",
				},
			},
			"nextCursor": nil,
		})
	}))
	defer srv.Close()
	seedSession(t, srv.URL)

	out, err := executeCLI(t, newRootCmd, "schedule", "list")
	if err != nil {
		t.Fatalf("schedule list: %v", err)
	}
	for _, want := range []string{
		"NEXT", "LAST", "Morning digest", "active", "2026-09-18T08:00:00Z", "2026-09-17T08:00:00Z",
		"One-off review", "paused",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("schedule table %q is missing %q", out, want)
		}
	}
	var pausedRow string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "One-off review") {
			pausedRow = line
		}
	}
	if strings.Count(pausedRow, "-") < 2 {
		t.Fatalf("paused row %q should render \"-\" for the missing next and last run", pausedRow)
	}
}

// runBody is a contract-shaped Run (docs/api/spec/runs.yaml `Run`): its only
// timestamps are createdAt, updatedAt and fireAt, and the agent is an id the
// client joins against the roster. There is no startedAt, finishedAt or
// agentName anywhere in the contract.
func runBody() map[string]any {
	return map[string]any{
		"runType": "task", "revision": 3,
		"source": map[string]any{
			"id": "0198aa00-0000-7000-8000-0000000000d1", "type": "task",
			"key": "ENG-12", "title": "Fix the login page",
		},
		"id": testRunID, "companyId": testCompanyID, "agentId": testRunAgentID,
		"taskId": "0198aa00-0000-7000-8000-0000000000d1", "taskExecutionGeneration": 1,
		"conversationId": "0198aa00-0000-7000-8000-0000000000c1",
		"runtimeId":      "0198aa00-0000-7000-8000-0000000000b1",
		"status":         "completed", "attempt": 1, "maxAttempts": 3,
		"parentRunId": nil, "triggerMessageId": nil, "transcriptThroughSeq": 41,
		"failureReason": nil, "failureMessage": nil, "waitReason": nil,
		"createdAt": "2026-09-16T10:00:00Z", "updatedAt": "2026-09-16T10:05:00Z", "fireAt": nil,
		"error": nil,
	}
}

// serveRuns answers the run read, the run list and the agent roster the
// tables join against; rosterStatus lets a test break the roster alone.
func serveRuns(t *testing.T, rosterStatus int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/companies/" + testCompanyID + "/runs/" + testRunID:
			_ = json.NewEncoder(w).Encode(runBody())
		case "/api/v1/companies/" + testCompanyID + "/runs":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{runBody()}, "nextCursor": nil,
			})
		case "/api/v1/companies/" + testCompanyID + "/agents":
			if rosterStatus != http.StatusOK {
				w.WriteHeader(rosterStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"id": testRunAgentID, "name": "Andrew"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	seedSession(t, srv.URL)
}

// The run tables render only what the Run contract carries, with the agent
// name joined from the roster as the projection prescribes.
func TestRunGetTableRendersTheContractsColumns(t *testing.T) {
	serveRuns(t, http.StatusOK)

	out, err := executeCLI(t, newRootCmd, "run", "get", testRunID, "--output", "table")
	if err != nil {
		t.Fatalf("run get: %v", err)
	}
	for _, want := range []string{
		"Andrew (" + testRunAgentID + ")", "2026-09-16T10:05:00Z", "ENG-12 Fix the login page",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("run get table %q is missing %q", out, want)
		}
	}
	for _, stale := range []string{"started", "finished", "- ("} {
		if strings.Contains(out, stale) {
			t.Fatalf("run get table %q still renders %q, which the contract never carries", out, stale)
		}
	}
}

// runListCells returns the cells of the one data row `run list` printed.
func runListCells(t *testing.T, out string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, testRunID) {
			return strings.Fields(line)
		}
	}
	t.Fatalf("run list table %q has no row for the run", out)
	return nil
}

func TestRunListTableJoinsTheAgentNameAndShowsUpdated(t *testing.T) {
	serveRuns(t, http.StatusOK)

	out, err := executeCLI(t, newRootCmd, "run", "list")
	if err != nil {
		t.Fatalf("run list: %v", err)
	}
	if !strings.Contains(out, "UPDATED") {
		t.Fatalf("run list table %q has no UPDATED column", out)
	}
	// ID STATUS AGENT TASK CONVERSATION CREATED UPDATED FAILURE
	cells := runListCells(t, out)
	if len(cells) != 8 || cells[2] != "Andrew" || cells[6] != "2026-09-16T10:05:00Z" {
		t.Fatalf("run list row = %v, want the agent's name and the updated time", cells)
	}
}

// A roster the caller cannot read must not hide the runs it already has:
// the cell falls back to the id and the reason goes to stderr.
func TestRunListTableFallsBackToTheAgentIDWithoutTheRoster(t *testing.T) {
	serveRuns(t, http.StatusInternalServerError)

	out, err := executeCLI(t, newRootCmd, "run", "list")
	if err != nil {
		t.Fatalf("run list: %v", err)
	}
	if cells := runListCells(t, out); cells[2] != shortID(testRunAgentID) {
		t.Fatalf("run list row = %v, want the agent id in the AGENT cell", cells)
	}
	if !strings.Contains(out, "agent names unavailable") {
		t.Fatalf("run list output %q does not say why the names are missing", out)
	}
}

// A failure terminates non-zero and stdout stays parseable. Cobra
// only rejects an unknown subcommand at the root, so every family must
// refuse `zen <family> <unknown>` the same way instead of printing its help
// to stdout and exiting 0.
func TestUnknownFamilySubcommandFailsLikeTheRoot(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	for _, args := range [][]string{
		{"task", "frobnicate", "--output", "json"},
		{"runtime", "frob"},
		{"config", "frob"}, // runnable family: must not silently run `config show`
	} {
		out, err := executeCLI(t, newRootCmd, args...)
		if err == nil || !strings.Contains(err.Error(), `unknown command "`+args[1]+`" for "zen `+args[0]+`"`) {
			t.Fatalf("zen %s: err = %v, want unknown command", strings.Join(args, " "), err)
		}
		if out != "" {
			t.Fatalf("zen %s wrote %q to stdout for a failure", strings.Join(args, " "), out)
		}
		if code := exitCodeFor(err); code != exitGeneric {
			t.Fatalf("zen %s exit code = %d, want %d like the root",
				strings.Join(args, " "), code, exitGeneric)
		}
	}

	// A bare family still prints its help and succeeds.
	out, err := executeCLI(t, newRootCmd, "task")
	if err != nil {
		t.Fatalf("zen task: %v", err)
	}
	if !strings.Contains(out, "Available Commands") || !strings.Contains(out, "list") {
		t.Fatalf("zen task output %q is not the family help", out)
	}
}

// A conversation list row carries the agent as a reference (`agent`, never a
// bare `agentId`, docs/api/conversations.md), so the AGENT column shows the
// name; `-` only when the agent was deleted.
func TestConversationListRendersTheAgentName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/companies/"+testCompanyID+"/conversations" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{
				"id": "0198aa00-0000-7000-8000-0000000000c1", "companyId": testCompanyID,
				"kind": "direct", "title": "Andrew conversation", "taskId": nil,
				"agent": map[string]any{
					"id": "0198aa00-0000-7000-8000-0000000000a1", "name": "Andrew",
					"title": "CEO", "runtimeAvailable": true,
				},
				"updatedAt": "2026-09-16T16:46:28Z",
			}, {
				"id": "0198aa00-0000-7000-8000-0000000000c2", "companyId": testCompanyID,
				"kind": "direct", "title": "Deleted agent", "taskId": nil, "agent": nil,
				"updatedAt": "2026-09-16T16:47:28Z",
			}},
		})
	}))
	defer srv.Close()
	seedSession(t, srv.URL)

	out, err := executeCLI(t, newRootCmd, "conversation", "list")
	if err != nil {
		t.Fatalf("conversation list: %v", err)
	}
	var andrewRow, deletedRow string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "Andrew conversation"):
			andrewRow = line
		case strings.Contains(line, "Deleted agent"):
			deletedRow = line
		}
	}
	if !strings.Contains(andrewRow, "Andrew  ") || strings.Contains(andrewRow, "0000000000a1") {
		t.Fatalf("row %q should show the agent's name, not its id", andrewRow)
	}
	if !strings.Contains(deletedRow, "  -  ") {
		t.Fatalf("row %q should render - for a deleted agent", deletedRow)
	}
}

// The CLI's own refresh carries the same client metadata as its API calls,
// so a refused refresh names the CLI in the server's request log.
func TestOperatorRefreshSendsTheCLIClientMetadata(t *testing.T) {
	var refreshPlatform string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/refresh" {
			refreshPlatform = r.Header.Get("X-Client-Platform")
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	seedSession(t, srv.URL)
	path, err := daemon.CredentialPath()
	if err != nil {
		t.Fatalf("credential path: %v", err)
	}
	credential, err := authfile.Load(path)
	if err != nil {
		t.Fatalf("load credential: %v", err)
	}
	expired := time.Now().Add(-time.Minute)
	credential.AccessExpiresAt = &expired
	if err := authfile.Save(path, credential); err != nil {
		t.Fatalf("save credential: %v", err)
	}
	t.Setenv("ZENITH_TEAM_RUN_ID", "")
	t.Setenv("ZENITH_TEAM_RUN_TOKEN", "")

	if _, err := executeCLI(t, newRootCmd, "task", "list"); err == nil {
		t.Fatal("task list succeeded on a refused refresh")
	}
	if refreshPlatform != "cli" {
		t.Fatalf("refresh X-Client-Platform = %q, want cli", refreshPlatform)
	}
}
