package daemon

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-shellwords"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

const (
	DefaultServerURL         = "http://localhost:8080"
	DefaultPollInterval      = 30 * time.Second
	DefaultHeartbeatInterval = 15 * time.Second
	// DefaultAgentTimeout is the optional absolute wall-clock cap on a single
	// agent run. 0 = no cap: a run is bounded only by the inactivity
	// watchdogs, so a session that keeps emitting events is never killed
	// merely for running long.
	DefaultAgentTimeout                   = 0
	DefaultCodexSemanticInactivityTimeout = 10 * time.Minute
	DefaultCodexHandshakeTimeout          = 30 * time.Second
	// DefaultAgentIdleWatchdog force-stops a run when the backend has emitted
	// no message for this long AND its queue is empty.
	// Set ZENITH_TEAM_AGENT_IDLE_WATCHDOG=0 to disable.
	DefaultAgentIdleWatchdog = 30 * time.Minute
	// DefaultAgentToolWatchdog bounds how long a single tool call may stay in
	// flight before the idle watchdog force-stops the run. 0 disables.
	DefaultAgentToolWatchdog = 2 * time.Hour
	DefaultHealthPort        = 19713
	// DefaultGCInterval is the workspace garbage-collection cadence: expired
	// retained worktrees and scratch dirs are reclaimed on this ticker.
	DefaultGCInterval        = 2 * time.Hour
	DefaultMaxConcurrentRuns = 20
	// DefaultConnectorDiscoveryInterval is how often the daemon re-observes a
	// provider's Connectors so the cloud keeps admitting them to Run claims.
	DefaultConnectorDiscoveryInterval = 3 * time.Minute
)

// The keepalive cadence stays inside protocol.ConnectorDiscoveryFreshWindow:
// below the floor it spawns provider discovery needlessly often; the ceiling
// keeps two jittered intervals, one tick and one slow Codex read inside the
// window, so a single failed re-observation never lets freshness lapse.
const (
	minimumConnectorDiscoveryInterval = time.Minute
	maximumConnectorDiscoveryInterval = 3*time.Minute + 30*time.Second
)

// Config holds all daemon configuration.
type Config struct {
	// StudioAPIOrigin is resolved from the server before tools or runs start.
	StudioAPIOrigin string
	ServerBaseURL   string
	DaemonID        string
	DeviceName      string
	CLIVersion      string
	// LaunchedBy is "desktop" when spawned by the desktop app, empty for
	// standalone.
	LaunchedBy string
	// Agents is the startup availability set keyed by provider (claude,
	// codex). The live set is d.agentsAvailable.
	Agents map[string]AgentEntry
	// WorkspacesRoot is the base path for execution environments
	// (default: ~/zenith_team_workspaces).
	WorkspacesRoot                 string
	HealthPort                     int
	MaxConcurrentRuns              int
	PollInterval                   time.Duration
	HeartbeatInterval              time.Duration
	AgentTimeout                   time.Duration
	CodexSemanticInactivityTimeout time.Duration
	CodexHandshakeTimeout          time.Duration
	AgentIdleWatchdog              time.Duration
	AgentToolWatchdog              time.Duration
	ClaudeArgs                     []string
	CodexArgs                      []string
	// MCPExecutable is the zenith-team-mcp binary the daemon wires into the agent
	// process MCP config. Empty disables the zenith-team MCP server.
	MCPExecutable string
	// GCEnabled gates the periodic workspace garbage collector
	// (ZENITH_TEAM_GC_ENABLED=false disables it).
	GCEnabled bool
	// GCInterval is the workspace GC cadence (ZENITH_TEAM_GC_INTERVAL).
	GCInterval time.Duration
	// AutoReloadEnabled lets a CLI-launched daemon restart into a replaced
	// zenith-team binary once it is idle (ZENITH_TEAM_AUTO_RELOAD=false disables it).
	// Desktop-managed daemons ignore it; the app owns its bundled CLI.
	AutoReloadEnabled bool
	// ConnectorDiscoveryInterval is the Connector discovery keepalive cadence
	// (ZENITH_TEAM_CONNECTOR_DISCOVERY_INTERVAL), clamped to [1m, 3m30s].
	ConnectorDiscoveryInterval time.Duration
	// CredentialIsolationOff is the test switch
	// ZENITH_TEAM_DAEMON_CREDENTIAL_ISOLATION=not_enforced: every runtime reports
	// not_enforced and providers start without the sandbox, so an end-to-end
	// run can play a Windows computer on a Mac. It only lowers; no value
	// raises the report.
	CredentialIsolationOff bool
}

// Overrides allows CLI flags to override environment variables and defaults.
// Zero values are ignored and the env/default value is used instead.
type Overrides struct {
	ServerURL         string
	WorkspacesRoot    string
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	MaxConcurrentRuns int
	DaemonID          string
	DeviceName        string
	HealthPort        int
	LaunchedBy        string
	// AgentTimeout is a pointer because 0 is meaningful: it disables the
	// wall-clock cap, so "not set" and "set to 0" must stay distinguishable.
	AgentTimeout                   *time.Duration
	CodexSemanticInactivityTimeout time.Duration
	CodexHandshakeTimeout          time.Duration
	// AllowNoAgents is reserved for read-only configuration probes. Daemon
	// startup keeps the default false and refuses to run with no agent CLI.
	AllowNoAgents bool
	// DisableAutoReload is single-direction: it can only turn the on-disk
	// binary watch off, never force it on over ZENITH_TEAM_AUTO_RELOAD=false.
	DisableAutoReload bool
}

// LoadConfig builds the daemon configuration from environment variables and
// optional CLI flag overrides.
func LoadConfig(overrides Overrides) (Config, error) {
	rawServerURL := envOrDefault("ZENITH_TEAM_SERVER_URL", DefaultServerURL)
	if overrides.ServerURL != "" {
		rawServerURL = overrides.ServerURL
	}
	serverBaseURL, err := NormalizeServerBaseURL(rawServerURL)
	if err != nil {
		return Config{}, err
	}

	agents := probeAgentCLIs()
	if len(agents) == 0 && !overrides.AllowNoAgents {
		return Config{}, fmt.Errorf("no agent CLI found: install claude or codex and ensure it is on PATH")
	}

	claudeArgs, err := shellArgsFromEnv("ZENITH_TEAM_CLAUDE_ARGS")
	if err != nil {
		return Config{}, err
	}
	codexArgs, err := shellArgsFromEnv("ZENITH_TEAM_CODEX_ARGS")
	if err != nil {
		return Config{}, err
	}

	host := defaultDeviceName()

	pollInterval, err := durationFromEnv("ZENITH_TEAM_POLL_INTERVAL", DefaultPollInterval)
	if err != nil {
		return Config{}, err
	}
	if overrides.PollInterval > 0 {
		pollInterval = overrides.PollInterval
	}

	heartbeatInterval, err := durationFromEnv("ZENITH_TEAM_HEARTBEAT_INTERVAL", DefaultHeartbeatInterval)
	if err != nil {
		return Config{}, err
	}
	if overrides.HeartbeatInterval > 0 {
		heartbeatInterval = overrides.HeartbeatInterval
	}

	agentTimeout, err := durationFromEnv("ZENITH_TEAM_AGENT_TIMEOUT", DefaultAgentTimeout)
	if err != nil {
		return Config{}, err
	}
	if overrides.AgentTimeout != nil {
		agentTimeout = *overrides.AgentTimeout
	}

	codexSemanticInactivityTimeout, err := durationFromEnv(
		"ZENITH_TEAM_CODEX_SEMANTIC_INACTIVITY",
		DefaultCodexSemanticInactivityTimeout,
	)
	if err != nil {
		return Config{}, err
	}
	if overrides.CodexSemanticInactivityTimeout > 0 {
		codexSemanticInactivityTimeout = overrides.CodexSemanticInactivityTimeout
	}

	codexHandshakeTimeout, err := durationFromEnv(
		"ZENITH_TEAM_CODEX_HANDSHAKE_TIMEOUT",
		DefaultCodexHandshakeTimeout,
	)
	if err != nil {
		return Config{}, err
	}
	if overrides.CodexHandshakeTimeout > 0 {
		codexHandshakeTimeout = overrides.CodexHandshakeTimeout
	}
	if codexHandshakeTimeout <= 0 {
		codexHandshakeTimeout = DefaultCodexHandshakeTimeout
	}

	// ZENITH_TEAM_AGENT_IDLE_WATCHDOG=0 disables the per-run idle watchdog.
	agentIdleWatchdog, err := durationFromEnv("ZENITH_TEAM_AGENT_IDLE_WATCHDOG", DefaultAgentIdleWatchdog)
	if err != nil {
		return Config{}, err
	}
	// ZENITH_TEAM_AGENT_TOOL_WATCHDOG=0 disables the in-flight-tool backstop.
	agentToolWatchdog, err := durationFromEnv("ZENITH_TEAM_AGENT_TOOL_WATCHDOG", DefaultAgentToolWatchdog)
	if err != nil {
		return Config{}, err
	}

	maxConcurrentRuns, err := intFromEnv("ZENITH_TEAM_MAX_CONCURRENT_RUNS", DefaultMaxConcurrentRuns)
	if err != nil {
		return Config{}, err
	}
	if overrides.MaxConcurrentRuns > 0 {
		maxConcurrentRuns = overrides.MaxConcurrentRuns
	}

	// daemon_id resolution: override > env > persistent UUID on disk. The
	// persistent UUIDv7 is written once to ~/.zenith-team/daemon.id and reused
	// forever so hostname drift never mints a new runtime identity.
	daemonID := strings.TrimSpace(os.Getenv("ZENITH_TEAM_DAEMON_ID"))
	if overrides.DaemonID != "" {
		daemonID = overrides.DaemonID
	}
	if daemonID == "" {
		persisted, err := EnsureDaemonID()
		if err != nil {
			return Config{}, fmt.Errorf("ensure daemon id: %w", err)
		}
		daemonID = persisted
	}

	deviceName := envOrDefault("ZENITH_TEAM_DEVICE_NAME", host)
	if overrides.DeviceName != "" {
		deviceName = overrides.DeviceName
	}

	workspacesRoot, err := ResolveWorkspacesRoot(overrides.WorkspacesRoot)
	if err != nil {
		return Config{}, err
	}

	gcInterval, err := durationFromEnv("ZENITH_TEAM_GC_INTERVAL", DefaultGCInterval)
	if err != nil {
		return Config{}, err
	}
	if gcInterval <= 0 {
		gcInterval = DefaultGCInterval
	}
	gcEnabled := envOrDefault("ZENITH_TEAM_GC_ENABLED", "true") != "false"

	connectorDiscoveryEvery, err := durationFromEnv(
		"ZENITH_TEAM_CONNECTOR_DISCOVERY_INTERVAL",
		DefaultConnectorDiscoveryInterval,
	)
	if err != nil {
		return Config{}, err
	}

	healthPort, err := intFromEnv("ZENITH_TEAM_HEALTH_PORT", DefaultHealthPort)
	if err != nil {
		return Config{}, err
	}
	if overrides.HealthPort > 0 {
		healthPort = overrides.HealthPort
	}
	launchedBy := strings.TrimSpace(os.Getenv("ZENITH_TEAM_LAUNCHED_BY"))
	if overrides.LaunchedBy != "" {
		launchedBy = strings.TrimSpace(overrides.LaunchedBy)
	}
	if launchedBy != "" && launchedBy != "desktop" {
		return Config{}, fmt.Errorf("invalid ZENITH_TEAM_LAUNCHED_BY %q", launchedBy)
	}
	autoReload := envOrDefault("ZENITH_TEAM_AUTO_RELOAD", "true") != "false"
	if overrides.DisableAutoReload {
		autoReload = false
	}
	isolationSwitch := strings.TrimSpace(os.Getenv(credentialIsolationEnv))
	if isolationSwitch != "" && isolationSwitch != protocol.CredentialIsolationNotEnforced {
		return Config{}, fmt.Errorf(
			"invalid %s %q: the only value is %s", credentialIsolationEnv, isolationSwitch,
			protocol.CredentialIsolationNotEnforced,
		)
	}

	return Config{
		ServerBaseURL:                  serverBaseURL,
		DaemonID:                       daemonID,
		DeviceName:                     deviceName,
		LaunchedBy:                     launchedBy,
		Agents:                         agents,
		WorkspacesRoot:                 workspacesRoot,
		HealthPort:                     healthPort,
		MaxConcurrentRuns:              maxConcurrentRuns,
		PollInterval:                   pollInterval,
		HeartbeatInterval:              heartbeatInterval,
		AgentTimeout:                   agentTimeout,
		CodexSemanticInactivityTimeout: codexSemanticInactivityTimeout,
		CodexHandshakeTimeout:          codexHandshakeTimeout,
		AgentIdleWatchdog:              agentIdleWatchdog,
		AgentToolWatchdog:              agentToolWatchdog,
		ClaudeArgs:                     claudeArgs,
		CodexArgs:                      codexArgs,
		MCPExecutable:                  strings.TrimSpace(os.Getenv("ZENITH_TEAM_MCP_EXECUTABLE")),
		GCEnabled:                      gcEnabled,
		GCInterval:                     gcInterval,
		AutoReloadEnabled:              autoReload,
		ConnectorDiscoveryInterval:     connectorDiscoveryInterval(connectorDiscoveryEvery),
		CredentialIsolationOff:         isolationSwitch != "",
	}, nil
}

// connectorDiscoveryInterval maps a configured keepalive cadence onto the
// supported range; zero or negative means the default.
func connectorDiscoveryInterval(configured time.Duration) time.Duration {
	if configured <= 0 {
		return DefaultConnectorDiscoveryInterval
	}
	return min(max(configured, minimumConnectorDiscoveryInterval), maximumConnectorDiscoveryInterval)
}

// NormalizeServerBaseURL converts a WebSocket or HTTP URL to a base HTTP URL.
func NormalizeServerBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid ZENITH_TEAM_SERVER_URL: %w", err)
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	case "http", "https":
	default:
		return "", fmt.Errorf("ZENITH_TEAM_SERVER_URL must use ws, wss, http, or https")
	}
	if u.Path == "/ws" {
		u.Path = ""
	}
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

// ResolveWorkspacesRoot returns the absolute path the daemon treats as the
// workspaces root. Resolution order: explicit override > ZENITH_TEAM_WORKSPACES_ROOT
// env > $HOME/zenith_team_workspaces.
func ResolveWorkspacesRoot(override string) (string, error) {
	root := strings.TrimSpace(os.Getenv("ZENITH_TEAM_WORKSPACES_ROOT"))
	if override != "" {
		root = override
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf(
				"resolve home directory: %w (set ZENITH_TEAM_WORKSPACES_ROOT to override)",
				err,
			)
		}
		root = filepath.Join(home, "zenith_team_workspaces")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve absolute workspaces root: %w", err)
	}
	return abs, nil
}

func shellArgsFromEnv(name string) ([]string, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, nil
	}
	args, err := shellwords.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", name, err)
	}
	return args, nil
}

// resolveToolExecutablePath returns the symlink-resolved path of a helper
// executable the daemon runs (node, npm, ffmpeg). Provider CLIs use
// findProviderExecutable instead, which keeps the path they were found at.
func resolveToolExecutablePath(cmd string) (string, error) {
	resolved, err := exec.LookPath(cmd)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return resolved, nil
	}
	if resolvedTarget, err := filepath.EvalSymlinks(abs); err == nil {
		return resolvedTarget, nil
	}
	return abs, nil
}

// findProviderExecutable returns the absolute path a provider CLI is found
// at — /opt/homebrew/bin/codex, ~/.local/bin/claude — without following the
// symlink behind it. The path stays absolute, so a later PATH change cannot
// redirect run launches. Not following the link is the point: installers
// upgrade by moving it (Homebrew deletes Caskroom/<old>, Codex and Claude
// retarget a current or versions/<v> link), and a version manager's shim
// picks the real program from the name it is called by. The kept path
// follows each upgrade instead of pinning, or losing, the old version. A
// pinned ZENITH_TEAM_*_PATH (cmd with a separator) is kept exactly as written.
func findProviderExecutable(cmd string) (string, error) {
	found, err := exec.LookPath(cmd)
	if err != nil {
		return "", err
	}
	if abs, err := filepath.Abs(found); err == nil {
		found = abs
	}
	if strings.ContainsAny(cmd, "/\\") {
		return found, nil
	}
	return settleShellScopedPath(found), nil
}

// settleShellScopedPath rewrites a path found through fnm's per-shell PATH
// entry to the Node install that entry points at now. fnm puts
// $FNM_MULTISHELL_PATH/bin on PATH, and $FNM_MULTISHELL_PATH is a link it
// repoints on every `fnm use` in the shell that started the daemon — a path
// kept through it would quietly switch Node versions, or vanish, under a
// running daemon. Every other link is kept as found; the login-shell
// fallback settles the same directory with `pwd -P`.
func settleShellScopedPath(path string) string {
	multishell := os.Getenv("FNM_MULTISHELL_PATH")
	if multishell == "" {
		return path
	}
	rel, err := filepath.Rel(multishell, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	target, err := filepath.EvalSymlinks(multishell)
	if err != nil {
		return path
	}
	return filepath.Join(target, rel)
}

// agentExecutablePresent reports whether path currently resolves to a
// runnable executable, using the exact check the agent backends apply at
// launch (exec.LookPath).
func agentExecutablePresent(path string) bool {
	if path == "" {
		return false
	}
	_, err := exec.LookPath(path)
	return err == nil
}

func defaultArgsForProvider(cfg Config, provider string) []string {
	var args []string
	switch provider {
	case "claude":
		args = cfg.ClaudeArgs
	case "codex":
		args = cfg.CodexArgs
	default:
		return nil
	}
	return append([]string(nil), args...)
}
