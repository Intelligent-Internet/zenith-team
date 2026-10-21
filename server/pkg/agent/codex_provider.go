package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// codexBlockedArgs are flags hardcoded by the daemon that must not be
// overridden by user-configured custom_args. The mcp_servers config keys
// live in the per-run `$CODEX_HOME/config.toml` (written by
// ensureCodexMcpConfig); user-supplied `-c mcp_servers.…` overrides are
// stripped separately by filterCodexCustomConfigOverrides because they
// share the `-c` flag with legitimate non-MCP overrides like `-c model=…`.
var codexBlockedArgs = map[string]blockedArgMode{
	"--listen": blockedWithValue, // stdio:// transport for daemon communication
}

// codexStderrTailBytes bounds the stderr tail captured for inclusion in
// error messages when codex exits before the JSON-RPC handshake (e.g. the
// user supplied a custom_args flag that the `app-server` subcommand
// rejects). Kept as its own constant so bumping codex independently of
// other agents stays easy if codex starts shipping longer failure traces.
const (
	codexStderrTailBytes                  = 2048
	defaultCodexSemanticInactivityTimeout = 10 * time.Minute
	// defaultCodexFirstTurnNoProgressTimeout caps how long the first turn may
	// stay completely silent after the app-server reports turn/started. Its only
	// job is to fail fast instead of waiting out
	// defaultCodexSemanticInactivityTimeout, so it only has to stay well under
	// that 10 minute backstop.
	//
	// 30s turned out to sit inside the normal range rather than above it: two
	// independent field reports measured the first progress event landing just
	// past 30s on gpt-5.5, and ~39s for a WSL app-server. Both were healthy
	// turns the watchdog killed. 60s clears that evidence with margin while
	// keeping the fast-fail value.
	defaultCodexFirstTurnNoProgressTimeout = 60 * time.Second
	// codexGracefulShutdownTimeout bounds how long the lifecycle goroutine
	// waits for codex to exit on its own after stdin is closed, before forcing
	// a context-cancel kill. A clean exit lets codex run its shutdown path and
	// flush buffered telemetry — OTEL batch exporters only force-flush on
	// graceful shutdown, so killing it immediately (the prior behavior) drops
	// the run's spans/metrics/logs.
	codexGracefulShutdownTimeout = 10 * time.Second
)

// codexGracefulShutdownTimeoutNanos optionally overrides
// codexGracefulShutdownTimeout for tests, in nanoseconds. Zero or negative
// values keep the production default. Tests for the cleanup-on-scanner-
// overflow path use it to shrink the grace window from 10 s to a few
// hundred ms so the regression runs in a normal `go test` budget instead
// of burning two full grace windows per cleanup phase.
var (
	codexGracefulShutdownTimeoutNanos atomic.Int64
	codexProcessWaitDelayNanos        atomic.Int64
	activeCodexLaunches               atomic.Int64
	maxActiveCodexLaunchesObserved    atomic.Int64
	codexCleanupConfirmationOverride  atomic.Int32
)

func sanitizeCodexDiagnostic(value string) string {
	return sanitizeAgentDiagnostic(value)
}

func codexProcessExitStatus(state *os.ProcessState) any {
	if state == nil {
		return nil
	}
	return state.String()
}

func codexGracefulShutdown() time.Duration {
	if n := codexGracefulShutdownTimeoutNanos.Load(); n > 0 {
		return time.Duration(n)
	}
	return codexGracefulShutdownTimeout
}

func codexProcessWaitDelay() time.Duration {
	if n := codexProcessWaitDelayNanos.Load(); n > 0 {
		return time.Duration(n)
	}
	return 10 * time.Second
}

type codexStderrClassification struct {
	// modelRefreshFailure is the broad catalog failure bucket. It includes the
	// narrower modelRefreshTimeout bucket, so the two counts are not additive.
	modelRefreshFailure int
	modelRefreshTimeout int
	mcpInitTransport    int
	bareTimeout         int
}

// classifyCodexStartupStderr emits bounded counters only. It deliberately does
// not make retry decisions: stderr is sibling diagnostic evidence, not proof
// that thread/start did or did not create a provider-side thread.
func classifyCodexStartupStderr(stderr string, timedOut bool) codexStderrClassification {
	lower := strings.ToLower(sanitizeCodexDiagnostic(stderr))
	classification := codexStderrClassification{
		modelRefreshFailure: strings.Count(lower, codexModelCatalogRefreshFailureSignal),
		modelRefreshTimeout: strings.Count(lower, codexModelCatalogRefreshTimeoutSignal),
	}
	for _, line := range strings.Split(lower, "\n") {
		if strings.Contains(line, "mcp") && strings.Contains(line, "transport") &&
			(strings.Contains(line, "error") || strings.Contains(line, "failed") || strings.Contains(line, "closed")) {
			classification.mcpInitTransport++
		}
	}
	// Any catalog refresh failure now owns the timeout classification, not only
	// the narrower child-process timeout signal. This intentionally makes the
	// existing bare-timeout bucket stricter across daemon versions.
	if timedOut && classification.modelRefreshFailure == 0 && classification.mcpInitTransport == 0 {
		classification.bareTimeout = 1
	}
	return classification
}

// CodexSemanticInactivityMarker prefixes timeout errors emitted when Codex
// stops making semantic progress while the process is still alive.
const CodexSemanticInactivityMarker = "codex semantic inactivity timeout"

// CodexFirstTurnNoProgressMarker identifies the app-server failure mode where
// Codex accepts a turn and then never emits any item, completion, or error.
const CodexFirstTurnNoProgressMarker = "codex app-server no progress timeout"

// CodexHandshakeTimeoutMarker identifies a Codex app-server startup RPC that
// did not answer within the bounded handshake window.
const CodexHandshakeTimeoutMarker = "codex app-server handshake timeout"

// codexResumeMarker and codexLineOverflowMarker are the two halves of the
// error text a resume-overflow produces: the method that failed (written by
// startOrResumeThread) and bufio's own ErrTooLong wording, which reaches the
// string through the reader goroutine.
const (
	codexResumeMarker       = "thread/resume failed"
	codexLineOverflowMarker = "token too long"
)

// CodexResumeOverflowError reports whether an agent error string is the
// resume-overflow failure — the thread/resume response did not fit in our
// stdout line buffer, so the thread cannot be handed to us at all.
//
// This lives next to the code that writes both halves of the text so the two
// cannot drift apart, and it is exported because the daemon has only the error
// STRING at report time: by then the typed bufio.ErrTooLong that
// isCodexResumeOverflow matches in-process is long gone. Both markers are
// required — "thread/resume failed" alone covers ordinary rejections that a
// plain retry handles, and "token too long" alone would also match an overflow
// on some other RPC, where dropping the session pointer cures nothing.
//
// The session behind such a failure is unusable for resume until it shrinks,
// which it never does — codex rollouts are append-only. Callers use this to
// stop handing the same oversized thread to the next run.
func CodexResumeOverflowError(errText string) bool {
	if errText == "" {
		return false
	}
	lower := strings.ToLower(errText)
	return strings.Contains(lower, codexResumeMarker) && strings.Contains(lower, codexLineOverflowMarker)
}

// codexModelCatalogRefreshFailureSignal matches the Codex models-manager error
// emitted when the model catalog could not be refreshed. Codex reports several
// distinct causes under this prefix ("timeout waiting for child process to
// exit", "stream disconnected before completion", ...), so match the shared
// prefix rather than one variant: they are all the same startup-blocking
// failure from the daemon's point of view.
const (
	codexModelCatalogRefreshFailureSignal = "failed to refresh available models"
	codexModelCatalogRefreshTimeoutSignal = "failed to refresh available models: timeout waiting for child process to exit"
)

var errCodexProcessExited = errors.New("codex process exited")

type codexTimeoutKind int

const (
	codexTimeoutNone codexTimeoutKind = iota
	codexTimeoutSemanticInactivity
	codexTimeoutFirstTurnNoProgress
)

type codexTimeoutDiagnostic struct {
	Kind         codexTimeoutKind
	Timeout      time.Duration
	LastActivity string
	ThreadID     string
	TurnID       string
	Model        string
	CodexVersion string
}

// codexFirstItemWaitObservation records the interval from turn/started to the
// first semantic progress or terminal outcome. start is called by the stdout
// reader before it publishes status:running, while finish is called by the
// lifecycle goroutine; the mutex keeps the cross-goroutine timestamp precise
// without changing the watchdog's existing channel-driven semantics.
type codexFirstItemWaitObservation struct {
	mu         sync.Mutex
	startedAt  time.Time
	finishedAt time.Time
	outcome    string
	stderr     codexStderrClassification
}

func (o *codexFirstItemWaitObservation) start(now time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.startedAt.IsZero() {
		o.startedAt = now
	}
}

func (o *codexFirstItemWaitObservation) finish(now time.Time, outcome string, stderr codexStderrClassification) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.startedAt.IsZero() || o.outcome != "" {
		return
	}
	o.finishedAt = now
	o.outcome = outcome
	o.stderr = stderr
}

func (o *codexFirstItemWaitObservation) snapshot() (time.Duration, string, codexStderrClassification, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.startedAt.IsZero() || o.finishedAt.IsZero() || o.outcome == "" {
		return 0, "", codexStderrClassification{}, false
	}
	return o.finishedAt.Sub(o.startedAt), o.outcome, o.stderr, true
}

// codexProvider implements Provider by spawning `codex app-server --listen stdio://`
// and communicating via JSON-RPC 2.0 over stdin/stdout. Constructed by
// New("codex", cfg).
type codexProvider struct {
	cfg Config
}

func buildCodexArgs(opts ExecOptions, logger *slog.Logger) []string {
	args := []string{"app-server", "--listen", "stdio://"}
	if opts.ToolPolicy == ToolPolicyConnectorOnly {
		if len(opts.ExtraArgs)+len(opts.CustomArgs) != 0 && logger != nil {
			logger.Warn(
				"custom_args: ignored for connector-only Codex execution",
				"count", len(opts.ExtraArgs)+len(opts.CustomArgs),
			)
		}
		return args
	}
	args = append(args, NormalizeCodexLaunchArgs(opts.ExtraArgs, opts.CustomArgs, opts.McpConfig, logger)...)
	for _, plugin := range opts.DisabledPlugins {
		args = append(args, "-c", "plugins."+strconv.Quote(plugin)+".enabled=false")
	}
	return args
}

// NormalizeCodexLaunchArgs returns the user-supplied Codex args (extra then
// custom) after lower-priority normalization: shell quoting stripped,
// protocol-critical flags removed, and — when a managed
// mcp_config owns the mcp_servers namespace — stray `-c mcp_servers.*`
// overrides dropped. buildCodexArgs prepends the fixed
// `app-server --listen stdio://` transport flags after this result.
//
// It is exported so the daemon can reconstruct the *effective* launch args
// when it needs to reason about them: a config opt-in may arrive shell-quoted
// (users commonly type custom_args with shell syntax, e.g. `'-c'
// 'key=value'`), and only after this same normalization does it match the
// shape a detector looks for. Reconstructing the args any other way lets the
// two drift.
func NormalizeCodexLaunchArgs(extraArgs, customArgs []string, mcpConfig json.RawMessage, logger *slog.Logger) []string {
	extra := filterCustomArgs(extraArgs, codexBlockedArgs, logger)
	custom := filterCustomArgs(customArgs, codexBlockedArgs, logger)
	// Only claim ownership of the `mcp_servers` namespace when the agent
	// actually has a managed mcp_config. Otherwise existing users who
	// configure MCP via `custom_args: ["-c", "mcp_servers.…"]` would silently
	// lose those entries. With managed mcp_config present, daemon-written
	// `$CODEX_HOME/config.toml` is the authoritative source and stray
	// `-c mcp_servers.*` overrides are dropped to keep last-wins from
	// re-shadowing it.
	if hasManagedCodexMcpConfig(mcpConfig) {
		extra = filterCodexCustomConfigOverrides(extra, logger)
		custom = filterCodexCustomConfigOverrides(custom, logger)
	}
	out := make([]string, 0, len(extra)+len(custom))
	out = append(out, extra...)
	out = append(out, custom...)
	return out
}

// hasManagedCodexMcpConfig reports whether the agent's mcp_config field is
// "present" in the API three-state sense: a non-null JSON value. Both
// `{}` and `{"mcpServers":{}}` count as present (the admin saved an empty
// managed set — strict mode, no global fallback); only SQL NULL or the
// literal JSON `null` count as absent (CLI default).
func hasManagedCodexMcpConfig(raw json.RawMessage) bool {
	return hasManagedMcpConfig(raw)
}

// codexManagedMcpConfigKeyRe matches the daemon-managed config namespace
// (`mcp_servers.…`) when it appears as the value of a Codex `-c` /
// `--config` flag. Used by filterCodexCustomConfigOverrides to drop user
// overrides that would otherwise shadow what the managed mcp_config writes
// into `$CODEX_HOME/config.toml`.
var codexManagedMcpConfigKeyRe = regexp.MustCompile(`^\s*mcp_servers(?:\s*\.|\s*=|\s*$)`)

// A daemon-managed shell_environment_policy must also win over profile and
// custom-arg overrides. Match root and profile policy keys without catching an
// unrelated table field that happens to use the same final key name.
const (
	codexShellEnvPolicyKeyPattern = `(?:shell_environment_policy|"shell_environment_policy"|'shell_environment_policy')`
	codexProfileNameKeyPattern    = `(?:[A-Za-z0-9_-]+|"[^"]+"|'[^']+')`
)

var codexManagedShellEnvConfigKeyRe = regexp.MustCompile(
	`^\s*(?:` + codexShellEnvPolicyKeyPattern + `|profiles\s*\.\s*` + codexProfileNameKeyPattern + `\s*\.\s*` + codexShellEnvPolicyKeyPattern + `)\s*(?:\.|=|$)`,
)

// filterCodexCustomConfigOverrides drops `-c mcp_servers.…=` and
// `--config mcp_servers.…=` entries from custom args. Codex's `-c` is
// last-wins (verified against codex-cli 0.132.0), so without this filter a
// user-written `-c mcp_servers.fetch=…` in custom_args would silently
// override whatever the managed mcp_config saved into the per-run
// config.toml. We own the `mcp_servers` namespace via the managed block, so
// user attempts to write into it are dropped with a warning rather than
// allowed to win. Other `-c`/`--config` keys (e.g. `-c model="o3"`) pass
// through unchanged.
func filterCodexCustomConfigOverrides(args []string, logger *slog.Logger) []string {
	return filterCodexConfigOverrides(args, codexManagedMcpConfigKeyRe, "mcp_servers", logger)
}

func filterCodexShellEnvConfigOverrides(args []string, logger *slog.Logger) []string {
	return filterCodexConfigOverrides(
		args,
		codexManagedShellEnvConfigKeyRe,
		shellEnvironmentPolicyConfigNamespace,
		logger,
	)
}

const shellEnvironmentPolicyConfigNamespace = "shell_environment_policy"

func filterCodexConfigOverrides(
	args []string,
	managedKeyRe *regexp.Regexp,
	namespace string,
	logger *slog.Logger,
) []string {
	if len(args) == 0 {
		return args
	}
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		flag := arg
		inlineValue := ""
		hasInlineValue := false
		if idx := strings.Index(arg, "="); idx > 0 {
			flag = arg[:idx]
			inlineValue = arg[idx+1:]
			hasInlineValue = true
		}
		if flag == "-c" || flag == "--config" {
			value := inlineValue
			if !hasInlineValue && i+1 < len(args) {
				value = args[i+1]
			}
			if managedKeyRe.MatchString(value) {
				if logger != nil {
					// Log the key only, never the value. Managed config values
					// may contain secrets and must stay out of logs/argv.
					key := value
					if eqIdx := strings.Index(value, "="); eqIdx >= 0 {
						key = value[:eqIdx]
					}
					logger.Warn("custom_args: blocked managed Codex config override",
						"namespace", namespace, "flag", flag, "key", strings.TrimSpace(key))
				}
				if !hasInlineValue && i+1 < len(args) {
					i++ // skip the value arg
				}
				continue
			}
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

// Markers delimiting the daemon-managed `[mcp_servers.*]` block in
// `$CODEX_HOME/config.toml`. Kept grep-able so ops can find every managed
// block consistently.
const (
	zenithTeamCodexMcpBeginMarker = "# BEGIN zenith-team-managed mcp_servers (do not edit; regenerated by daemon)"
	zenithTeamCodexMcpEndMarker   = "# END zenith-team-managed mcp_servers"
)

var codexMcpBlockRe = regexp.MustCompile(
	`(?ms)^` + regexp.QuoteMeta(zenithTeamCodexMcpBeginMarker) +
		`.*?^` + regexp.QuoteMeta(zenithTeamCodexMcpEndMarker) + `\n*`)

// userCodexMcpServersTableHeaderRe matches `[mcp_servers.<name>]` (and its
// quoted-key form `[mcp_servers."<name>"]`) at the start of a line. Used
// to strip user-provided mcp_servers tables from the per-run config when
// the agent has its own mcp_config — mirrors Claude's `--strict-mcp-config`
// model where the daemon's set is authoritative.
var userCodexMcpServersTableHeaderRe = regexp.MustCompile(
	`^\s*\[\s*mcp_servers\s*\.\s*(?:"[^"]*"|[^\]\s]+)\s*\]\s*(?:#.*)?$`)

// ensureCodexMcpConfig writes (or clears) the daemon-managed
// `[mcp_servers.*]` block in `$CODEX_HOME/config.toml`. The block is the
// authoritative source of MCP servers for this run: with mcp_config set
// on the agent the daemon also strips any inherited
// `[mcp_servers.*]` tables from the per-run config so the user's global
// `~/.codex/config.toml` doesn't shadow or collide with the managed set.
//
// The file mode is 0o600 because `mcp_servers.<id>.env` values may carry
// secrets (API keys, bearer tokens); the per-run home is owned by the
// daemon's user, so 0o600 keeps secrets out of any world-readable copy
// while still letting the codex child read them.
//
// A malformed mcp_config is returned as an error and the caller decides
// whether to surface or warn — same fail-soft contract the prior argv
// path had.
func ensureCodexMcpConfig(configPath string, mcpConfig json.RawMessage, logger *slog.Logger) error {
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config.toml: %w", err)
	}
	existing := string(data)

	// Always strip a prior managed block so reruns and clear-config flows
	// converge on a clean state.
	stripped := codexMcpBlockRe.ReplaceAllString(existing, "")

	managed := hasManagedCodexMcpConfig(mcpConfig)
	block, _, renderErr := renderCodexMcpServersBlock(mcpConfig)
	if renderErr != nil {
		return renderErr
	}

	var updated string
	if managed {
		// Agent has a managed MCP set (possibly empty — `{}` /
		// `{"mcpServers":{}}` count as "saved an empty set" in the API's
		// three-state semantics, distinct from nil/null which means
		// "fall back to CLI default"). Strip any user-defined
		// `[mcp_servers.*]` tables inherited from `~/.codex/config.toml`
		// so the managed set is strict — mirrors Claude's
		// `--strict-mcp-config`. Two reasons we cannot mix:
		//   1. TOML rejects redefining the same table; a user table
		//      named `[mcp_servers.fetch]` would crash codex if the
		//      agent also defined `fetch`.
		//   2. An admin saving an explicit list would otherwise see
		//      user-global servers silently joined in, which contradicts
		//      the UI affordance.
		stripped = stripCodexUserMcpServerTables(stripped)
		stripped = strings.TrimRight(stripped, "\n")
		// When the managed set is empty we still write the marker
		// block (with no tables between). This pins "managed but
		// empty" on disk so the next run can find and strip the
		// markers, and so the file's intent is grep-able by ops.
		if block == "" {
			block = zenithTeamCodexMcpBeginMarker + "\n" + zenithTeamCodexMcpEndMarker + "\n"
		}
		if stripped == "" {
			updated = block
		} else {
			updated = stripped + "\n\n" + block
		}
	} else {
		// No managed config: just remove any prior managed block and
		// leave inherited user tables alone (CLI default fallback).
		updated = stripped
	}

	if updated == existing {
		return nil
	}
	if err := os.WriteFile(configPath, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("write config.toml: %w", err)
	}
	// os.WriteFile applies the mode only when creating a new file; if the
	// per-run config.toml was already on disk at 0o644 (a common default
	// when seeding from ~/.codex/config.toml), the secret-bearing values we
	// just wrote would inherit that wider mode. Chmod unconditionally to
	// keep the secret in the daemon owner's lane regardless of the prior
	// mode.
	if err := os.Chmod(configPath, 0o600); err != nil {
		return fmt.Errorf("chmod config.toml to 0600: %w", err)
	}
	if logger != nil {
		logger.Debug("codex: wrote managed mcp_servers block to config.toml",
			"config_path", configPath, "managed", managed)
	}
	return nil
}

// renderCodexMcpServersBlock renders the agent's mcp_config JSON
// (Claude-style `{"mcpServers": {...}}`) as a TOML block of
// `[mcp_servers.<name>]` tables wrapped in BEGIN/END markers. Returns
// (block, hasServers, err); hasServers=false means the input had no
// servers to render (empty/null mcp_config) and the caller should only
// strip the prior managed block.
//
// Stdio server keys (`args`, `env`, `command`) pass through verbatim —
// Codex's config schema happens to use the same names today. Remote HTTP
// servers use Codex-specific keys, so they are normalised here rather than
// leaking provider details into the UI/dispatch layer.
func renderCodexMcpServersBlock(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 {
		return "", false, nil
	}
	var parsed struct {
		McpServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", false, fmt.Errorf("parse mcp_config json: %w", err)
	}
	if len(parsed.McpServers) == 0 {
		return "", false, nil
	}

	names := make([]string, 0, len(parsed.McpServers))
	for name := range parsed.McpServers {
		names = append(names, name)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString(zenithTeamCodexMcpBeginMarker)
	sb.WriteString("\n")
	for i, name := range names {
		if !isCodexBareTomlKey(name) {
			return "", false, fmt.Errorf(
				"mcp server name %q must be ASCII alphanumeric / _ / - to fit Codex's bare-key requirement",
				name,
			)
		}
		var serverVal map[string]any
		if err := json.Unmarshal(parsed.McpServers[name], &serverVal); err != nil {
			return "", false, fmt.Errorf("mcp_servers.%s: %w", name, err)
		}
		if serverVal == nil {
			return "", false, fmt.Errorf("mcp_servers.%s must be a JSON object", name)
		}
		serverVal = normalizeCodexMcpServerConfig(serverVal)
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("[mcp_servers.")
		sb.WriteString(name)
		sb.WriteString("]\n")
		keys := make([]string, 0, len(serverVal))
		for k := range serverVal {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			tomlValue, err := jsonValueToCodexTOMLInline(serverVal[k])
			if err != nil {
				return "", false, fmt.Errorf("mcp_servers.%s.%s: %w", name, k, err)
			}
			sb.WriteString(codexTOMLKey(k))
			sb.WriteString(" = ")
			sb.WriteString(tomlValue)
			sb.WriteString("\n")
		}
	}
	sb.WriteString(zenithTeamCodexMcpEndMarker)
	sb.WriteString("\n")
	return sb.String(), true, nil
}

func normalizeCodexMcpServerConfig(server map[string]any) map[string]any {
	if !isCodexRemoteMcpServer(server) {
		normalized := make(map[string]any, len(server))
		for k, v := range server {
			if isZenithTeamMcpSelectorKey(k) {
				continue
			}
			normalized[k] = v
		}
		return normalized
	}

	normalized := make(map[string]any, len(server)+1)
	for k, v := range server {
		switch {
		case isZenithTeamMcpSelectorKey(k):
			continue
		case k == "type":
			continue
		case k == "headers":
			if _, ok := server["http_headers"]; !ok {
				normalized["http_headers"] = v
			}
		default:
			normalized[k] = v
		}
	}
	normalized["experimental_use_rmcp_client"] = true
	return normalized
}

func isZenithTeamMcpSelectorKey(k string) bool {
	switch k {
	case "tools", "prompts", "resources":
		return true
	default:
		return false
	}
}

func isCodexRemoteMcpServer(server map[string]any) bool {
	if typ, ok := server["type"].(string); ok && strings.EqualFold(typ, "http") {
		return true
	}
	_, hasURL := server["url"]
	_, hasCommand := server["command"]
	return hasURL && !hasCommand
}

// stripCodexUserMcpServerTables removes every `[mcp_servers.*]` table
// (header + body lines until the next top-level table header or EOF) from
// a TOML config string. Sub-tables like `[mcp_servers.fetch.env]` count
// as part of the parent table and are dropped along with it.
func stripCodexUserMcpServerTables(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	skipping := false
	for _, line := range lines {
		if userCodexMcpServersTableHeaderRe.MatchString(line) {
			skipping = true
			continue
		}
		if skipping {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[") {
				// Next table header. If it's still an `mcp_servers.*`
				// table (including a sub-table), keep skipping; otherwise
				// stop and emit this line.
				if userCodexMcpServersTableHeaderRe.MatchString(line) ||
					strings.HasPrefix(trimmed, "[mcp_servers.") ||
					strings.HasPrefix(trimmed, "[ mcp_servers.") {
					continue
				}
				skipping = false
				out = append(out, line)
				continue
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// jsonValueToCodexTOMLInline serializes a JSON value as a TOML inline
// value. Only the subset Codex's `-c` accepts is supported: strings,
// numbers, booleans, arrays, and inline tables. JSON nulls are rejected
// because TOML has no null and silently dropping them would be confusing.
func jsonValueToCodexTOMLInline(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", fmt.Errorf("null is not a valid TOML value")
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10), nil
		}
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case string:
		return codexTOMLBasicString(x), nil
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			p, err := jsonValueToCodexTOMLInline(e)
			if err != nil {
				return "", err
			}
			parts[i] = p
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			p, err := jsonValueToCodexTOMLInline(x[k])
			if err != nil {
				return "", err
			}
			parts[i] = codexTOMLKey(k) + " = " + p
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	default:
		return "", fmt.Errorf("unsupported value type %T", v)
	}
}

func codexTOMLBasicString(s string) string {
	var sb strings.Builder
	sb.Grow(len(s) + 2)
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			sb.WriteString(`\\`)
		case '"':
			sb.WriteString(`\"`)
		case '\b':
			sb.WriteString(`\b`)
		case '\t':
			sb.WriteString(`\t`)
		case '\n':
			sb.WriteString(`\n`)
		case '\f':
			sb.WriteString(`\f`)
		case '\r':
			sb.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&sb, `\u%04x`, r)
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

func codexTOMLKey(s string) string {
	if isCodexBareTomlKey(s) {
		return s
	}
	return codexTOMLBasicString(s)
}

func isCodexBareTomlKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

type codexAttempt struct {
	messages <-chan providerMessage
	result   <-chan Result
}

func (b *codexProvider) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Execution, error) {
	if err := validateConnectorExecutionOptions(opts); err != nil {
		return nil, fmt.Errorf("validate codex connector scope: %w", err)
	}
	opts = normalizeConnectorExecutionOptions(opts)
	firstSession, err := b.executeOnce(ctx, prompt, opts, 1)
	if err != nil {
		return nil, err
	}
	eventStream := newExecutionEventStream()

	go func() {
		defer eventStream.close()
		session := firstSession
		attemptOpts := opts
		syntheticItemSequence := 0
		emitMessage := func(msg providerMessage) bool {
			defer msg.acknowledgeProcessing()
			// An ephemeral connector-only thread writes no rollout, so it is
			// never a resume pointer: announce no SessionReady for it.
			if opts.ToolPolicy == ToolPolicyConnectorOnly && msg.Type == providerMessageStatus {
				msg.SessionID = ""
			}
			event, ok := codexExecutionEvent(msg, &syntheticItemSequence)
			if !ok {
				return true
			}
			return eventStream.send(ctx, event)
		}
		for attempt := 1; attempt <= 2; attempt++ {
			if attempt > 1 {
				var err error
				session, err = b.executeOnce(ctx, prompt, attemptOpts, attempt)
				if err != nil {
					eventStream.send(ctx, Finished{
						Result: Result{Status: "failed", Error: err.Error()},
					})
					return
				}
			}
			// Hold back the leading session-pin status messages until this
			// attempt proves it made real progress. A retry never continues the
			// discarded attempt's thread (initialize retries fail before any
			// thread exists; catalog retries clear ResumeSessionID below), so
			// forwarding its pin would leave the resume pointer aimed at a
			// thread that never produced a turn. The first non-pin message
			// means the attempt is live: flush and stream from then on.
			var heldPins []providerMessage
			holdingPins := true
			flushHeldPins := func() bool {
				for _, held := range heldPins {
					if !emitMessage(held) {
						return false
					}
				}
				heldPins = nil
				holdingPins = false
				return true
			}
			for msg := range session.messages {
				if holdingPins && msg.Type == providerMessageStatus && msg.Status == "running" {
					heldPins = append(heldPins, msg)
					continue
				}
				if holdingPins {
					if !flushHeldPins() {
						return
					}
				}
				if !emitMessage(msg) {
					return
				}
			}
			result, ok := <-session.result
			if !ok {
				if !flushHeldPins() {
					return
				}
				eventStream.send(ctx, Finished{
					Result: Result{Status: "failed", Error: "codex attempt closed without result"},
				})
				return
			}
			retryReason := ""
			switch {
			case result.codexInitializeRetrySafe:
				retryReason = "initialize"
			case result.codexStartupRefreshRetrySafe:
				retryReason = "model_catalog_refresh"
			}
			if retryReason == "" || attempt == 2 {
				if !flushHeldPins() {
					return
				}
				eventStream.send(ctx, Finished{Result: result})
				return
			}
			// The model catalog refresh reaches the network, so give the
			// transient failure a little more room than the local initialize
			// handshake gets. Both stay well inside the run timeout, and
			// ctx.Done() below keeps the retry from extending it.
			backoff := 75*time.Millisecond + time.Duration(time.Now().UnixNano()%50)*time.Millisecond
			if retryReason == "model_catalog_refresh" {
				backoff = 500*time.Millisecond + time.Duration(
					time.Now().UnixNano()%1000,
				)*time.Millisecond
				// The stalled attempt already reached turn/started, so the prior
				// thread may hold the submitted input or an unfinished turn.
				// Resuming it again could duplicate that input; start a fresh
				// thread instead and keep ResumeExpected so
				// codexTurnInputWithConnectors prepends the caller's continuity
				// notice about the lost context.
				if attemptOpts.ResumeSessionID != "" {
					b.cfg.Logger.Warn(
						"codex retry dropping resume pointer after model catalog refresh failure",
						"prior_thread_id",
						attemptOpts.ResumeSessionID,
					)
					attemptOpts.ResumeSessionID = ""
					attemptOpts.ResumeExpected = true
				}
			}
			b.cfg.Logger.Warn(
				"codex retry scheduled",
				"reason",
				retryReason,
				"attempt",
				attempt,
				"next_attempt",
				attempt+1,
				"backoff",
				backoff.String(),
			)
			select {
			case <-ctx.Done():
				if !flushHeldPins() {
					return
				}
				eventStream.send(ctx, Finished{Result: result})
				return
			case <-time.After(backoff):
			}
		}
	}()

	return eventStream.execution(), nil
}

func (b *codexProvider) executeOnce(
	ctx context.Context,
	prompt string,
	opts ExecOptions,
	attempt int,
) (*codexAttempt, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "codex"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("codex executable not found at %q: %w", execPath, err)
	}

	timeout := opts.Timeout
	semanticInactivityTimeout := opts.SemanticInactivityTimeout
	if semanticInactivityTimeout == 0 {
		semanticInactivityTimeout = defaultCodexSemanticInactivityTimeout
	}
	handshakeTimeout := opts.HandshakeTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = 30 * time.Second
	}
	runCtx, cancel := runContext(ctx, timeout)

	// Materialize the agent's MCP config into the per-run
	// `$CODEX_HOME/config.toml`. Argv would be the simpler path, but
	// `mcp_servers.<id>.env` is allowed to carry secrets (Codex docs:
	// https://developers.openai.com/codex/mcp#configure-with-configtoml)
	// and mcp_config is already treated as a redacted-for-non-admins
	// field. Process argv ends up in OS-level `ps` listings and is also
	// echoed into the daemon's `agent command` log line below, so any
	// inline env-bearing TOML would defeat the redaction. Writing through
	// config.toml at 0o600 keeps the secret values out of argv and logs.
	codexHome := strings.TrimSpace(b.cfg.Env["CODEX_HOME"])
	if codexHome != "" {
		if err := ensureCodexMcpConfig(
			filepath.Join(codexHome, "config.toml"),
			opts.McpConfig,
			b.cfg.Logger,
		); err != nil {
			// Fail closed when we can't materialize the managed config.
			// Warning-and-launching would silently fall back to the
			// user's global `~/.codex/config.toml` MCP servers and
			// look indistinguishable from "the saved config was
			// applied", which is exactly the surprise managed MCP is
			// supposed to remove.
			cancel()
			return nil, fmt.Errorf("apply codex mcp_config: %w", err)
		}
	} else if hasManagedCodexMcpConfig(opts.McpConfig) {
		// Managed mcp_config saved but no CODEX_HOME to anchor it.
		// Same reasoning as above: silently launching would inherit
		// whatever MCP setup the host user has, which is the wrong
		// shape of failure.
		cancel()
		return nil, fmt.Errorf(
			"codex: mcp_config is set but CODEX_HOME env var is not configured; cannot apply managed MCP",
		)
	}

	if codexHome != "" {
		// The daemon owns shell_environment_policy in the run-local config.
		// Codex -c/--config overrides are last-wins, so remove user-provided
		// root or profile policy overrides before building the final argv.
		opts.ExtraArgs = filterCodexShellEnvConfigOverrides(opts.ExtraArgs, b.cfg.Logger)
		opts.CustomArgs = filterCodexShellEnvConfigOverrides(opts.CustomArgs, b.cfg.Logger)
	}
	codexArgs := buildCodexArgs(opts, b.cfg.Logger)
	launchPath, launchArgs := opts.Sandbox.Command(execPath, codexArgs)
	cmd := exec.CommandContext(runCtx, launchPath, launchArgs...)
	hideAgentWindow(cmd)
	// Run codex in its own process group so a cancel-on-stuck cleanup
	// reaches the whole tree — the codex Node wrapper plus the native
	// Rust app-server it spawns — not just the direct child. Without
	// this, killing the leader leaves grandchildren as orphans that
	// keep consuming memory until the OS reaps them; a scanner overflow
	// during thread/resume otherwise leaked Codex processes indefinitely.
	configureProcessGroup(cmd)
	// Override the default exec.CommandContext cancel behavior. The
	// default sends SIGKILL only to cmd.Process (the leader); we instead
	// signal the whole process group so descendants die too. Returning
	// nil keeps exec from logging a spurious error; cmd.WaitDelay below
	// still backstops cmd.Wait() if the kill leaves an open pipe.
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			signalProcessGroup(cmd.Process, syscall.SIGKILL)
		}
		return nil
	}
	// Bound the wait after the context is cancelled so a stuck child (or an
	// open pipe held by a grandchild) can't hang cmd.Wait() forever. Matches
	// the claude adapter.
	cmd.WaitDelay = codexProcessWaitDelay()
	b.cfg.Logger.Info("agent command", "exec", execPath, "args", codexArgs, "sandboxed", opts.Sandbox != nil)
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("codex stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("codex stdin pipe: %w", err)
	}
	// Codex stderr can contain auth/provider diagnostics. Capture a bounded
	// tail and emit it only through the sanitizer in the cleanup event.
	stderrBuf := newStderrTail(io.Discard, codexStderrTailBytes)
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start codex: %w", err)
	}
	activeLaunches := activeCodexLaunches.Add(1)
	for {
		maxSeen := maxActiveCodexLaunchesObserved.Load()
		if activeLaunches <= maxSeen || maxActiveCodexLaunchesObserved.CompareAndSwap(maxSeen, activeLaunches) {
			break
		}
	}
	launchStarted := time.Now()
	codexVersion := strings.TrimSpace(b.cfg.CodexVersion)
	if codexVersion == "" {
		codexVersion = "unknown"
	}

	b.cfg.Logger.Info(
		"codex lifecycle",
		"phase",
		"spawn",
		"run_id",
		b.cfg.RunID,
		"runtime_id",
		b.cfg.RuntimeID,
		"pid",
		cmd.Process.Pid,
		"process_group",
		cmd.Process.Pid,
		"cwd",
		opts.Cwd,
		"attempt",
		attempt,
		"active_launches",
		activeLaunches,
		"codex_version",
		codexVersion,
		"daemon_version",
		b.cfg.DaemonVersion,
	)

	// Keep the provider-native stream unbuffered too. Otherwise the stdout
	// reader can run hundreds of completed items ahead of PostgreSQL while the
	// outer execution stream waits for one durable acknowledgement.
	msgCh := make(chan providerMessage)
	resCh := make(chan Result, 1)
	semanticActivityCh := make(chan string, 256)

	var outputMu sync.Mutex
	// Result.Output is "final user-facing output selected by the backend"
	// (agent.go), so it holds the deliverable only. finalAnswer is the text the
	// app-server labeled `phase: "final_answer"`; lastAgentMessage is the
	// fallback for the legacy `agent_message` protocol, which carries no phase.
	// Every agent message still flows to msgCh, so the transcript is unchanged —
	// only what the daemon forwards to a chat reply narrows.
	var finalAnswer, lastAgentMessage string
	var semanticObserved atomic.Bool
	var deliveryBlocked atomic.Bool
	var providerToolsMu sync.Mutex
	providerTools := make(map[string]struct{})
	providerToolInFlight := func() bool {
		providerToolsMu.Lock()
		defer providerToolsMu.Unlock()
		return len(providerTools) > 0
	}
	trackProviderTool := func(msg providerMessage) {
		key := msg.CallID
		if key == "" {
			key = msg.ItemKey
		}
		if key == "" {
			return
		}
		providerToolsMu.Lock()
		defer providerToolsMu.Unlock()
		switch msg.Type {
		case providerMessageToolUse:
			providerTools[key] = struct{}{}
		case providerMessageToolResult:
			delete(providerTools, key)
		case providerMessageCompletedItem:
			for _, part := range msg.Parts {
				if result, ok := part.(ToolResultPart); ok {
					delete(providerTools, result.CallID)
				}
			}
		}
	}
	turnNotificationGate := &codexTurnNotificationGate{}
	firstItemWait := &codexFirstItemWaitObservation{}

	// turnDone is set before starting the reader goroutine so there is no
	// race between the lifecycle goroutine writing and the reader reading.
	turnDone := make(chan bool, 1) // true = aborted

	c := &codexClient{
		cfg:                  b.cfg,
		toolPolicy:           opts.ToolPolicy,
		stdin:                stdin,
		pending:              make(map[int]*pendingRPC),
		processDone:          make(chan struct{}),
		handshakeTimeout:     handshakeTimeout,
		pid:                  cmd.Process.Pid,
		attempt:              attempt,
		activeLaunches:       activeLaunches,
		notificationProtocol: "unknown",
		acceptNotification:   turnNotificationGate.accept,
		onDiscardedNotification: func(string, map[string]any) {
			// Any app-server notification proves the process made semantic
			// progress, even when it is intentionally excluded from the active
			// turn. Preserve initialize-retry safety without replaying content.
			semanticObserved.Store(true)
		},
		onMessage: func(msg providerMessage) {
			logCodexAgentMessage(b.cfg.Logger, msg)
			needsProcessingAck := providerMessageNeedsProcessingAcknowledgement(msg.Type)
			if needsProcessingAck {
				// Cover the hand-off from active tool state to durable delivery as
				// one continuous watchdog-suppression interval. Clearing the tool
				// first leaves a timer race that can cancel a healthy completion.
				deliveryBlocked.Store(true)
			}
			trackProviderTool(msg)
			if msg.Type == providerMessageText {
				outputMu.Lock()
				lastAgentMessage = msg.Content
				outputMu.Unlock()
			}
			activity := describeCodexSemanticActivity(msg)
			if activity == "status:running" {
				firstItemWait.start(time.Now())
			}
			sendProviderMessage(ctx, msgCh, msg)
			if needsProcessingAck {
				deliveryBlocked.Store(false)
				trySendString(semanticActivityCh, "delivery:acknowledged")
			}
			trySendString(semanticActivityCh, activity)
			if activity != "" {
				semanticObserved.Store(true)
			}
		},
		onFinalAnswer: func(text string) {
			outputMu.Lock()
			finalAnswer = text
			outputMu.Unlock()
		},
		onSemanticActivity: func(description string) {
			semanticObserved.Store(true)
			b.cfg.Logger.Debug("codex semantic activity observed", "activity", description)
			trySendString(semanticActivityCh, description)
		},
		onTurnDone: func(aborted bool) {
			select {
			case turnDone <- aborted:
			default:
			}
		},
	}

	// Start reading stdout in background
	readerDone := make(chan struct{})
	go func() {
		defer close(msgCh)
		defer close(readerDone)
		scanner := newAgentStreamScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			c.handleLine(line)
		}
		if err := scanner.Err(); err != nil {
			// %w on BOTH: callers match errCodexProcessExited to decide the
			// process is gone, and bufio.ErrTooLong to tell "we could not read
			// the response" apart from "codex died". startOrResumeThread needs
			// that distinction to report an oversized resume as a rejected
			// resume rather than a crash.
			c.markProcessExited(fmt.Errorf("%w: %w", errCodexProcessExited, err))
			return
		}
		c.markProcessExited(errCodexProcessExited)
	}()

	// drainAndWait closes stdin so codex shuts down, then joins cmd.Wait().
	// cmd.Wait() is the only Go-stdlib-documented synchronization point for
	// os/exec's internal stderr/stdout copy goroutines — until it returns,
	// stderrBuf may not have observed every byte codex wrote before it
	// exited, and stderrBuf.Tail() can come back empty or truncated. Any
	// code that reads stderrBuf.Tail() must call drainAndWait() first.
	// sync.Once makes it safe to call from both error paths and the deferred
	// cleanup.
	//
	// drainAndWait is also the cleanup safety net for the scanner-overflow
	// path. When codex emits a single stdout line larger than the scanner's
	// MaxScanTokenSize, the reader goroutine returns with scanner.Err() set,
	// fails all in-flight RPCs via markProcessExited, and closes readerDone —
	// but the codex child process is still alive and is now blocked trying to
	// write the rest of the oversized line into a stdout pipe nobody is
	// reading. A naive stdin.Close()+cmd.Wait() then hangs forever: codex
	// never reaches its stdin-read syscall, so it never sees EOF, never
	// exits, and cmd.Wait() never returns. The lifecycle goroutine therefore
	// never sends a failed Result, the outer daemon blocks on its result
	// channel, and the higher-level fresh-session fallback never fires.
	//
	// To stay correct under both clean shutdown and the stuck-child case,
	// drainAndWait runs in two bounded phases:
	//
	//  1. Close stdin and wait for the reader goroutine to finish, capped by
	//     codexGracefulShutdownTimeout. The reader exits when codex closes
	//     stdout on its own (clean shutdown — gives OTEL batch exporters a
	//     chance to flush) OR when the scanner errors out (overflow case —
	//     readerDone is already closed and the select returns immediately).
	//     Per os/exec docs, calling cmd.Wait() while reads are still
	//     in-flight on a StdoutPipe-returned pipe is incorrect because Wait
	//     closes the pipe and turns pending reads into spurious errors, so
	//     we must wait for the reader first.
	//
	//  2. Wait for cmd.Wait() to return, capped by another
	//     codexGracefulShutdownTimeout. Normally this returns immediately
	//     because the process has already exited. In the stuck-child case
	//     the process is still alive — we cancel the runCtx, which fires
	//     cmd.Cancel (the group-SIGKILL helper installed above), and
	//     cmd.WaitDelay then guarantees cmd.Wait() returns even if pipes
	//     stay open.
	var waitOnce sync.Once
	var cleanupConfirmed bool
	var waitReturned bool
	var cleanupWaitErr error
	drainAndWait := func() {
		waitOnce.Do(func() {
			stdin.Close()

			grace := codexGracefulShutdown()
			waitCh := make(chan struct{})
			var startWait sync.Once
			startProcessWait := func() {
				startWait.Do(func() {
					go func() {
						cleanupWaitErr = cmd.Wait()
						close(waitCh)
					}()
				})
			}

			// Phase 1: let the reader finish before invoking cmd.Wait().
			select {
			case <-readerDone:
				// reader drained cleanly (codex shutdown closed stdout)
				// or aborted early (e.g. scanner overflow). Either way it
				// is now safe to call cmd.Wait().
			case <-time.After(grace):
				// codex did not close stdout within the grace window. Force
				// the shutdown via context cancellation — cmd.Cancel
				// group-kills the tree, the reader unblocks when stdout
				// EOFs, and we proceed to phase 2.
				b.cfg.Logger.Warn("codex did not close stdout after stdin EOF; forcing shutdown",
					"pid", cmd.Process.Pid,
					"grace", grace.String(),
				)
				cancel()
				// A descendant may keep inherited stdout open indefinitely.
				// Start Wait now so os/exec's WaitDelay closes the pipe after
				// its bounded deadline and lets the reader finish.
				startProcessWait()
				<-waitCh
				select {
				case <-readerDone:
				case <-time.After(grace):
					b.cfg.Logger.Warn(
						"codex stdout reader remained open after bounded process wait",
						"pid",
						cmd.Process.Pid,
						"grace",
						grace.String(),
					)
					// A descendant can keep an inherited stdout descriptor open
					// after the app-server process is reaped. Close our read end so
					// the sole msgCh producer exits before channel ownership moves
					// on; the reader, not the lifecycle goroutine, closes msgCh.
					_ = stdout.Close()
					<-readerDone
				}
			}

			// Phase 2: bound cmd.Wait() in case the process is still alive
			// (scanner-overflow case: reader exited early on its own while
			// codex stayed blocked writing into a full stdout pipe).
			startProcessWait()
			select {
			case <-waitCh:
				waitReturned = true
				// reaped cleanly.
			case <-time.After(grace):
				b.cfg.Logger.Warn("codex process still alive after reader exited; forcing shutdown",
					"pid", cmd.Process.Pid,
					"grace", grace.String(),
				)
				cancel()
				// WaitDelay (10s) is the final backstop: even if the
				// group-kill races with an open pipe held by a
				// descendant, cmd.Wait() returns within WaitDelay of the
				// cancel.
				<-waitCh
				waitReturned = true
			}
			// Wait returning with a ProcessState is the os/exec reap boundary.
			// On Unix, ProcessState.Exited reports false for a process terminated
			// by SIGKILL even though Wait successfully reaped it.
			cleanupConfirmed = waitReturned && cmd.ProcessState != nil &&
				waitProcessGroupGone(cmd.Process, grace)
			if codexCleanupConfirmationOverride.Load() < 0 {
				cleanupConfirmed = false
			}
			// Release the launch before publishing cleanup or a terminal result.
			// The caller may start its next attempt as soon as it receives either.
			activeCodexLaunches.Add(-1)
			b.cfg.Logger.Info("codex lifecycle",
				"phase", "cleanup",
				"run_id", b.cfg.RunID,
				"runtime_id", b.cfg.RuntimeID,
				"pid", cmd.Process.Pid,
				"process_group", cmd.Process.Pid,
				"attempt", attempt,
				"latency", time.Since(launchStarted).Round(time.Millisecond).String(),
				"reaped", cleanupConfirmed,
				"exit_status", codexProcessExitStatus(cmd.ProcessState),
				"wait_error", cleanupWaitErr,
				"stderr_bytes", stderrBuf.TotalBytes(),
				"stderr_truncated", stderrBuf.TotalBytes() > codexStderrTailBytes,
			)
		})
	}

	// Drive the session lifecycle in a goroutine.
	// Shutdown sequence: lifecycle goroutine closes stdin + cancels context →
	// codex process exits → reader goroutine's scanner.Scan() returns false →
	// readerDone closes → lifecycle goroutine collects final output and sends Result.
	go func() {
		defer cancel()
		defer close(resCh)
		defer drainAndWait()

		startTime := time.Now()
		finalStatus := ResultCompleted
		var finalError string

		// 1. Initialize handshake
		initializeStarted := time.Now()
		b.cfg.Logger.Info(
			"codex lifecycle",
			"phase",
			"initialize_sent",
			"run_id",
			b.cfg.RunID,
			"runtime_id",
			b.cfg.RuntimeID,
			"pid",
			cmd.Process.Pid,
			"attempt",
			attempt,
			"active_launches",
			activeLaunches,
		)
		_, err := c.request(runCtx, "initialize", map[string]any{
			"clientInfo": map[string]any{
				"name":    "zenith-team-agent-sdk",
				"title":   "Zenith Team Agent SDK",
				"version": "0.2.0",
			},
			"capabilities": map[string]any{
				"experimentalApi": true,
			},
		})
		if err != nil {
			initializeLatency := time.Since(initializeStarted)
			var handshakeErr *codexHandshakeTimeoutError
			timedOut := errors.As(err, &handshakeErr) && handshakeErr.Method == "initialize"
			if timedOut {
				// A timed-out initialize may still complete after the host gives up.
				// Kill the whole process group before waiting so a leader that exits
				// on stdin EOF cannot leave detached-stdio descendants behind.
				signalProcessGroup(cmd.Process, syscall.SIGKILL)
			}
			// The child may exit in response to cancellation before the RPC
			// observes ctx.Done(), so err alone cannot identify that path.
			// Sample runCtx before cleanup, which can cancel it independently.
			contextEnded := runCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) ||
				errors.Is(err, context.Canceled)
			drainAndWait() // flush os/exec stderr goroutine before sampling Tail
			finalStatus = "failed"
			finalError = fmt.Sprintf("codex initialize failed: %v", err)
			if !timedOut && !contextEnded && ctx.Err() == nil {
				// Timeout stderr is untrusted provider output and may echo opaque
				// Config.Env/auth values that pattern sanitization cannot identify.
				// The same applies when the parent run deadline/cancellation wins
				// the race against the per-RPC handshake timeout.
				// Keep it out of persisted/user-visible Results; cleanup lifecycle
				// still records bounded byte/truncation metadata.
				finalError = withAgentStderr(
					finalError,
					"codex",
					sanitizeCodexDiagnostic(stderrBuf.Tail()),
				)
			}
			retrySafe := timedOut && !semanticObserved.Load() && cleanupConfirmed &&
				codexInitializeRetrySupported()
			if timedOut && !cleanupConfirmed {
				finalError += "; retry suppressed: process cleanup/reap not confirmed"
			} else if timedOut && cleanupConfirmed && !codexInitializeRetrySupported() {
				finalError += "; retry suppressed: process-tree cleanup cannot be confirmed on this platform"
			}
			b.cfg.Logger.Warn(
				"codex lifecycle",
				"phase",
				"initialize_failure",
				"run_id",
				b.cfg.RunID,
				"runtime_id",
				b.cfg.RuntimeID,
				"pid",
				cmd.Process.Pid,
				"attempt",
				attempt,
				"latency",
				initializeLatency.Round(time.Millisecond).String(),
				"semantic_activity",
				semanticObserved.Load(),
				"cleanup_confirmed",
				cleanupConfirmed,
				"retry_safe",
				retrySafe,
			)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds(), codexInitializeRetrySafe: retrySafe}
			return
		}
		b.cfg.Logger.Info(
			"codex lifecycle",
			"phase",
			"initialize_response",
			"run_id",
			b.cfg.RunID,
			"runtime_id",
			b.cfg.RuntimeID,
			"pid",
			cmd.Process.Pid,
			"attempt",
			attempt,
			"latency",
			time.Since(initializeStarted).Round(time.Millisecond).String(),
		)
		c.notify("initialized")
		if len(opts.DisabledPlugins) > 0 && opts.ToolPolicy != ToolPolicyConnectorOnly {
			raw, err := c.request(
				runCtx,
				"plugin/list",
				map[string]any{
					"cwds":             []string{opts.Cwd},
					"forceRefetch":     false,
					"marketplaceKinds": nil,
				},
			)
			if err == nil {
				err = checkCodexDisabledPlugins(raw, opts.DisabledPlugins)
			}
			if err != nil {
				finalStatus, finalError = "failed", "verify agent plugin settings: "+err.Error()
				signalProcessGroup(cmd.Process, syscall.SIGKILL)
				drainAndWait()
				return
			}
		}

		// 2. Start a new thread, or resume the prior one for this conversation.
		// When resume fails (thread GCed on the server, schema drift, etc.) we
		// fall back to a fresh thread so the run still makes progress.
		threadID, resumed, err := c.startOrResumeThread(runCtx, opts, b.cfg.Logger)
		if err != nil {
			var handshakeErr *codexHandshakeTimeoutError
			timedOut := errors.As(err, &handshakeErr) && handshakeErr.Method == "thread/start"
			if timedOut {
				// A timed-out thread/start has an uncertain provider outcome. Kill
				// the whole process group before waiting so a leader that exits on
				// EOF cannot leave detached-stdio descendants behind.
				signalProcessGroup(cmd.Process, syscall.SIGKILL)
			}
			drainAndWait() // flush os/exec stderr goroutine before sampling Tail
			finalStatus = "failed"
			stderrTail := sanitizeCodexDiagnostic(stderrBuf.Tail())
			finalError = err.Error()
			if c.threadStartSent {
				classification := classifyCodexStartupStderr(stderrTail, timedOut)
				b.cfg.Logger.Warn("codex lifecycle",
					"phase", "thread_start_failure",
					"run_id", b.cfg.RunID,
					"runtime_id", b.cfg.RuntimeID,
					"pid", cmd.Process.Pid,
					"attempt", attempt,
					"active_launches", activeLaunches,
					"method", "thread/start",
					"latency", time.Since(c.threadStartStarted).Round(time.Millisecond).String(),
					"latency_ms", time.Since(c.threadStartStarted).Milliseconds(),
					"cleanup_confirmed", cleanupConfirmed,
					"reaped", cleanupConfirmed,
					"retry_safe", false,
					"retry_attempted", false,
					"stderr_model_refresh_failure_count", classification.modelRefreshFailure,
					"stderr_model_refresh_timeout_count", classification.modelRefreshTimeout,
					"stderr_mcp_init_transport_count", classification.mcpInitTransport,
					"stderr_bare_timeout_count", classification.bareTimeout,
				)
			}
			resCh <- Result{
				Status:         finalStatus,
				Error:          finalError,
				DurationMs:     time.Since(startTime).Milliseconds(),
				ResumeRejected: isCodexResumeOverflow(opts, err),
			}
			return
		}
		c.setThreadID(threadID)
		if resumed {
			b.cfg.Logger.Info("codex thread resumed", "thread_id", threadID)
		} else {
			b.cfg.Logger.Info("codex thread started", "thread_id", threadID)
		}

		// 3. Send turn and wait for completion. When a resume was expected but we
		// ended up on a fresh thread (the live thread/resume RPC was rejected — a
		// corrupt/incompatible rollout, server-side thread GC, schema drift — or a
		// transport failure forced a fresh retry), prepend the caller's continuity
		// notice so the agent does not assume continuity it no longer has. The
		// daemon's pre-flight gates only catch cases detectable before launch;
		// this covers the ones only the live resume reveals.
		//
		// Whether that notice asks the agent to tell the USER is the caller's
		// call, not ours: it depends on whether this surface's conversation is
		// still readable, which this package cannot see.
		turnParams := map[string]any{
			"threadId": threadID,
			"input": codexTurnInputWithConnectors(
				prompt,
				opts.ResumeExpected,
				resumed,
				opts.ResumeContinuityNotice,
				opts.ConnectorApps,
			),
		}
		applyCodexConnectorOnlyTurnPolicy(turnParams, opts)
		// Per-turn reasoning override. Mirrors the per-thread injection in
		// startOrResumeThread; keeping both in sync is enforced by the
		// shared reasoning-injection fixture in codex_provider_test.go — the
		// injection points must not drift independently.
		applyCodexReasoningEffort(turnParams, opts.ThinkingLevel)
		waitingForTurn := true
		var timeoutDiagnostic codexTimeoutDiagnostic
		var processExitErr error
		finishFirstItemWait := func(outcome string) {
			firstItemWait.finish(
				time.Now(),
				outcome,
				classifyCodexStartupStderr(stderrBuf.Tail(), strings.HasSuffix(outcome, "_timeout")),
			)
		}
		finishTurn := func(aborted bool) {
			waitingForTurn = false
			switch {
			case aborted:
				finishFirstItemWait("turn_aborted")
				finalStatus = "aborted"
				if errMsg := c.getTurnError(); errMsg != "" {
					finalError = errMsg
				} else {
					finalError = "turn was aborted"
				}
			default:
				if errMsg := c.getTurnError(); errMsg != "" {
					finishFirstItemWait("turn_failed")
					finalStatus = "failed"
					finalError = errMsg
				} else {
					finishFirstItemWait("turn_completed")
				}
			}
		}
		turnNotificationGate.arm()
		_, err = c.request(runCtx, "turn/start", turnParams)
		if err != nil {
			select {
			case aborted := <-turnDone:
				finishTurn(aborted)
			default:
				drainAndWait() // flush os/exec stderr goroutine before sampling Tail
				finalStatus = "failed"
				finalError = withAgentStderr(
					fmt.Sprintf("codex turn/start failed: %v", err),
					"codex",
					sanitizeCodexDiagnostic(stderrBuf.Tail()),
				)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
		}

		lastSemanticActivity := time.Now()
		lastSemanticActivityDescription := "turn/start"
		semanticTimer := time.NewTimer(semanticInactivityTimeout)
		defer semanticTimer.Stop()

		firstTurnNoProgressTimeout := codexFirstTurnNoProgressTimeout(semanticInactivityTimeout)
		var firstTurnNoProgressTimer *time.Timer
		var firstTurnNoProgressTimerC <-chan time.Time
		firstTurnStarted := false
		firstTurnProgressObserved := false
		stopFirstTurnNoProgressTimer := func() {
			if firstTurnNoProgressTimer == nil {
				return
			}
			stopTimer(firstTurnNoProgressTimer)
			firstTurnNoProgressTimerC = nil
		}
		defer stopFirstTurnNoProgressTimer()

		finishRunContextDone := func() {
			waitingForTurn = false
			if runCtx.Err() == context.DeadlineExceeded {
				finishFirstItemWait("execution_timeout")
				finalStatus = "timeout"
				finalError = fmt.Sprintf("codex timed out after %s", timeout)
			} else {
				finishFirstItemWait("cancelled")
				finalStatus = "aborted"
				finalError = "execution cancelled"
			}
		}
		for waitingForTurn {
			select {
			case aborted := <-turnDone:
				finishTurn(aborted)
			case activity := <-semanticActivityCh:
				lastSemanticActivity = time.Now()
				lastSemanticActivityDescription = activity
				resetTimer(semanticTimer, semanticInactivityTimeout)
				if activity == "status:running" && !firstTurnStarted {
					firstTurnStarted = true
					firstItemWait.start(time.Now())
					firstTurnNoProgressTimer = time.NewTimer(firstTurnNoProgressTimeout)
					firstTurnNoProgressTimerC = firstTurnNoProgressTimer.C
				} else if firstTurnStarted && !firstTurnProgressObserved && isCodexFirstTurnProgressActivity(activity) {
					firstTurnProgressObserved = true
					if activity == "error:terminal" {
						finishFirstItemWait("turn_failed")
					} else {
						finishFirstItemWait("progress")
					}
					stopFirstTurnNoProgressTimer()
				}
			case <-firstTurnNoProgressTimerC:
				if deliveryBlocked.Load() || providerToolInFlight() {
					lastSemanticActivity = time.Now()
					lastSemanticActivityDescription = "delivery:waiting_for_consumer"
					resetTimer(firstTurnNoProgressTimer, firstTurnNoProgressTimeout)
					continue
				}
				waitingForTurn = false
				finishFirstItemWait("no_progress_timeout")
				finalStatus = "timeout"
				timeoutDiagnostic = codexTimeoutDiagnostic{
					Kind:         codexTimeoutFirstTurnNoProgress,
					Timeout:      firstTurnNoProgressTimeout,
					LastActivity: lastSemanticActivityDescription,
					ThreadID:     threadID,
					TurnID:       c.getTurnID(),
					Model:        opts.Model,
				}
				b.cfg.Logger.Warn(CodexFirstTurnNoProgressMarker,
					"pid", cmd.Process.Pid,
					"thread_id", threadID,
					"turn_id", c.getTurnID(),
					"timeout", firstTurnNoProgressTimeout.String(),
					"last_activity", lastSemanticActivityDescription,
				)
			case <-semanticTimer.C:
				if deliveryBlocked.Load() || providerToolInFlight() {
					lastSemanticActivity = time.Now()
					lastSemanticActivityDescription = "delivery:waiting_for_consumer"
					resetTimer(semanticTimer, semanticInactivityTimeout)
					continue
				}
				waitingForTurn = false
				finishFirstItemWait("semantic_inactivity_timeout")
				finalStatus = "timeout"
				timeoutDiagnostic = codexTimeoutDiagnostic{
					Kind:         codexTimeoutSemanticInactivity,
					Timeout:      semanticInactivityTimeout,
					LastActivity: lastSemanticActivityDescription,
					ThreadID:     threadID,
					TurnID:       c.getTurnID(),
					Model:        opts.Model,
				}
				b.cfg.Logger.Warn(CodexSemanticInactivityMarker,
					"pid", cmd.Process.Pid,
					"thread_id", threadID,
					"turn_id", c.getTurnID(),
					"timeout", semanticInactivityTimeout.String(),
					"last_activity", lastSemanticActivityDescription,
					"idle_for", time.Since(lastSemanticActivity).Round(time.Millisecond).String(),
				)
			case <-runCtx.Done():
				finishRunContextDone()
			case <-c.processDone:
				select {
				case aborted := <-turnDone:
					finishTurn(aborted)
				default:
					if runCtx.Err() != nil {
						finishRunContextDone()
					} else {
						waitingForTurn = false
						finishFirstItemWait("process_exit")
						finalStatus = "failed"
						processExitErr = c.getProcessErr()
						if processExitErr == nil {
							processExitErr = errCodexProcessExited
						}
						finalError = processExitErr.Error()
					}
				}
			}
		}

		duration := time.Since(startTime)
		b.cfg.Logger.Info(
			"codex finished",
			"pid",
			cmd.Process.Pid,
			"status",
			finalStatus,
			"duration",
			duration.Round(time.Millisecond).String(),
		)

		// Run cleanup. drainAndWait handles the graceful-then-cancel pattern
		// in two bounded phases (see its declaration): wait for the reader,
		// then wait for cmd.Wait(), force-cancelling either if the grace
		// window expires. A clean shutdown lets codex flush OTEL telemetry;
		// a stuck process is killed via the process-group SIGKILL.
		drainAndWait()

		if processExitErr != nil {
			finalError = withAgentStderr(
				processExitErr.Error(),
				"codex",
				sanitizeCodexDiagnostic(stderrBuf.Tail()),
			)
		}
		stderrTail := sanitizeCodexDiagnostic(stderrBuf.Tail())
		if timeoutDiagnostic.Kind != codexTimeoutNone {
			timeoutDiagnostic.CodexVersion = detectCodexVersionForDiagnostics(
				context.Background(),
				execPath,
				cmd.Env,
				b.cfg.Logger,
			)
			finalError = buildCodexTimeoutDiagnosticError(timeoutDiagnostic, stderrTail)
		}

		// A first turn that produced no semantic progress because Codex could
		// not load its model catalog is a startup-only failure: no tool ran and
		// no content reached the user, so replaying the prompt cannot duplicate
		// side effects. Reuse the same process-tree evidence initialize retries
		// require (cleanupConfirmed plus platform support) rather than a bare
		// ProcessState check: a surviving app-server would race the retry.
		startupRefreshRetrySafe := timeoutDiagnostic.Kind == codexTimeoutFirstTurnNoProgress &&
			!firstTurnProgressObserved &&
			strings.Contains(stderrTail, codexModelCatalogRefreshFailureSignal) &&
			cleanupConfirmed && codexInitializeRetrySupported()
		if startupRefreshRetrySafe {
			b.cfg.Logger.Warn("codex startup model catalog refresh failure is retry safe",
				"pid", cmd.Process.Pid,
				"thread_id", threadID,
				"attempt", attempt,
			)
		}

		if waitLatency, outcome, classification, ok := firstItemWait.snapshot(); ok {
			if waitLatency < 0 {
				waitLatency = 0
			}
			// On timeout, cmd.Wait is the synchronization point that guarantees
			// the stderr copy goroutine has drained. Reclassify from the complete
			// bounded tail so a last-moment catalog/MCP signal is not reported as
			// a bare timeout. Successful waits keep the snapshot taken at first
			// progress so later turn stderr cannot pollute first-item telemetry.
			if strings.HasSuffix(outcome, "_timeout") {
				classification = classifyCodexStartupStderr(stderrTail, true)
			}
			fields := []any{
				"phase", "first_item_wait",
				"run_id", b.cfg.RunID,
				"runtime_id", b.cfg.RuntimeID,
				"pid", cmd.Process.Pid,
				"attempt", attempt,
				"active_launches", activeLaunches,
				"method", "turn/start",
				"thread_id", threadID,
				"turn_id", c.getTurnID(),
				"outcome", outcome,
				"latency", waitLatency.Round(time.Millisecond).String(),
				"latency_ms", waitLatency.Milliseconds(),
				"timeout", firstTurnNoProgressTimeout.String(),
				"semantic_inactivity_timeout", semanticInactivityTimeout.String(),
				"codex_version", codexVersion,
				"daemon_version", b.cfg.DaemonVersion,
				"cleanup_confirmed", cleanupConfirmed,
				"reaped", cleanupConfirmed,
				// retry_safe describes the terminal attempt, not the measured wait
				// interval. Successful samples therefore report false by design.
				"retry_safe", startupRefreshRetrySafe,
				"stderr_model_refresh_failure_count", classification.modelRefreshFailure,
				"stderr_model_refresh_timeout_count", classification.modelRefreshTimeout,
				"stderr_mcp_init_transport_count", classification.mcpInitTransport,
				"stderr_bare_timeout_count", classification.bareTimeout,
			}
			switch outcome {
			case "progress", "turn_completed":
				b.cfg.Logger.Info("codex lifecycle", fields...)
			default:
				b.cfg.Logger.Warn("codex lifecycle", fields...)
			}
		}

		outputMu.Lock()
		finalOutput := codexDeliverableOutput(finalAnswer, lastAgentMessage)
		outputMu.Unlock()

		// Build usage map from accumulated codex usage.
		// First check JSON-RPC notifications (often empty for Codex).
		var usageMap map[string]TokenUsage
		c.usageMu.Lock()
		u := c.usage
		c.usageMu.Unlock()

		// Fallback: if no usage from JSON-RPC, scan Codex session JSONL logs.
		// Codex writes token_count events to $CODEX_HOME/sessions/YYYY/MM/DD/*.jsonl;
		// scan this backend's per-run CODEX_HOME, since sessions are isolated
		// there rather than in the shared ~/.codex/sessions.
		if u.InputTokens == 0 && u.OutputTokens == 0 {
			runCodexHome := strings.TrimSpace(b.cfg.Env["CODEX_HOME"])
			if scanned := scanCodexSessionUsage(
				startTime,
				runCodexHome,
				threadID,
				resumed,
			); scanned != nil {
				u = scanned.usage
				if scanned.model != "" && opts.Model == "" {
					opts.Model = scanned.model
				}
			}
		}

		if u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheReadTokens > 0 || u.CacheWriteTokens > 0 {
			model := opts.Model
			if model == "" {
				model = "unknown"
			}
			// The adapter subtracts the resumed session baseline before it
			// returns this fact, so the counters belong to this run.
			u.UsageBasis = UsageBasisPerRun
			usageMap = map[string]TokenUsage{model: u}
		}

		// An ephemeral connector-only thread is no session: Codex keeps no
		// rollout of it, so nothing a later Run could resume.
		sessionID := threadID
		if opts.ToolPolicy == ToolPolicyConnectorOnly {
			sessionID = ""
		}
		resCh <- Result{
			Status:                       finalStatus,
			Output:                       finalOutput,
			Error:                        finalError,
			SessionID:                    sessionID,
			DurationMs:                   duration.Milliseconds(),
			Usage:                        usageMap,
			codexStartupRefreshRetrySafe: startupRefreshRetrySafe,
		}
	}()

	return &codexAttempt{messages: msgCh, result: resCh}, nil
}

func codexExecutionEvent(msg providerMessage, syntheticItemSequence *int) (Event, bool) {
	itemKey := msg.ItemKey
	if itemKey == "" && msg.Type != providerMessageStatus && msg.Type != providerMessageLog {
		*syntheticItemSequence++
		itemKey = fmt.Sprintf("codex-item-%d", *syntheticItemSequence)
	}
	switch msg.Type {
	case providerMessageItemDelta:
		return ItemDelta{
			ItemKey:   itemKey,
			Channel:   msg.Channel,
			Operation: msg.Operation,
			Content:   msg.Content,
		}, itemKey != "" && msg.Content != ""
	case providerMessageCompletedItem:
		parts := append([]Part{}, msg.Parts...)
		return CompletedItem{Key: itemKey, Role: msg.Role, Parts: parts},
			itemKey != "" && msg.Role != "" && len(parts) > 0
	case providerMessageText:
		return CompletedItem{
				Key:   itemKey,
				Role:  RoleAgent,
				Parts: []Part{TextPart{Text: msg.Content, Final: msg.FinalAnswer}},
			},
			msg.Content != ""
	case providerMessageThinking:
		return CompletedItem{
			Key: itemKey, Role: RoleAgent, Parts: []Part{ReasoningPart{Text: msg.Content}},
		}, msg.Content != ""
	case providerMessageToolUse:
		if msg.ItemKey != "" {
			// The invocation is immutable as soon as it starts. Give it its own
			// identity so live clients can show the target before the result arrives.
			itemKey = "codex-tool-call:" + msg.ItemKey
		}
		return CompletedItem{
			Key:   itemKey,
			Role:  RoleAgent,
			Parts: []Part{ToolCallPart{CallID: msg.CallID, Name: msg.Tool, Input: msg.Input}},
		}, true
	case providerMessageToolResult:
		if msg.ItemKey != "" {
			parts := make([]Part, 0, 2)
			if msg.Input != nil {
				parts = append(parts, ToolCallPart{
					CallID: msg.CallID,
					Name:   msg.Tool,
					Input:  msg.Input,
				})
			}
			parts = append(parts, ToolResultPart{
				CallID:     msg.CallID,
				Name:       msg.Tool,
				Output:     msg.Output,
				FilePath:   msg.FilePath,
				Status:     ToolResultStatus(msg.Status),
				ExitCode:   msg.ExitCode,
				DurationMs: msg.DurationMs,
			})
			// Status and exit code already describe the tool's outcome;
			// duplicating them as an ErrorPart adds no diagnostic information.
			return CompletedItem{Key: itemKey, Role: RoleTool, Parts: parts}, true
		}
		return CompletedItem{
			Key:  itemKey,
			Role: RoleTool,
			Parts: []Part{ToolResultPart{
				CallID:     msg.CallID,
				Name:       msg.Tool,
				Output:     msg.Output,
				FilePath:   msg.FilePath,
				Status:     ToolResultStatus(msg.Status),
				ExitCode:   msg.ExitCode,
				DurationMs: msg.DurationMs,
			}},
		}, true
	case providerMessageError:
		return CompletedItem{
			Key: itemKey, Role: RoleSystem, Parts: []Part{ErrorPart{Message: msg.Content}},
		}, msg.Content != ""
	case providerMessageStatus:
		return SessionReady{SessionID: msg.SessionID}, msg.SessionID != ""
	default:
		return nil, false
	}
}

// The continuity notice this backend prepends is supplied by the caller via
// ExecOptions.ResumeContinuityNotice rather than written here. It used to be a
// constant in this file that mirrored the daemon's, which meant two hand-kept
// copies of the same paragraph and no way for this package to know which
// surface it was running on — the wording is only correct if you know whether
// the conversation is still readable.
//
// codexTurnInputWithConnectors builds the input content for the first
// turn/start. When a resume was expected (resumeExpected) but the backend
// landed on a fresh thread (!resumed), it prepends the caller's continuity
// notice so the run does not silently continue as new. The notice is folded
// into the same text block as the prompt to stay within the single-text-block
// turn input Codex accepts. Reviewed connector apps are announced twice — as
// "$id" mentions at the head of that text block and as sibling mention blocks —
// because Codex needs both to bind a mention to an app.
//
// An empty notice means the caller has already disclosed the loss in the prompt
// itself, so this must add nothing — that is the whole guard against a turn
// carrying the same paragraph twice.
func codexTurnInputWithConnectors(
	prompt string,
	resumeExpected bool,
	resumed bool,
	notice string,
	apps []ConnectorApp,
) []map[string]any {
	text := prompt
	if resumeExpected && !resumed {
		text = notice + prompt
	}
	input := make([]map[string]any, 0, 1+len(apps))
	if len(apps) != 0 {
		mentions := make([]string, len(apps))
		for index, app := range apps {
			mentions[index] = "$" + app.ID
		}
		text = strings.Join(mentions, " ") + " " + text
	}
	input = append(input, map[string]any{"type": "text", "text": text})
	for _, app := range apps {
		input = append(input, map[string]any{
			"type": "mention", "name": app.Name, "path": "app://" + app.ID,
		})
	}
	return input
}

// startOrResumeThread picks between Codex's thread/resume and thread/start
// based on opts.ResumeSessionID. When a prior thread ID is provided it first
// tries thread/resume; recoverable protocol errors (unknown thread, schema
// mismatch) fall back to thread/start so the run still executes, while
// transport/process failures fail fast because the app-server can no longer
// answer a fresh start request. The returned threadID is what subsequent
// turn/start calls must reference, and resumed indicates whether the prior
// thread was picked up (only useful for logging).
func (c *codexClient) startOrResumeThread(
	ctx context.Context,
	opts ExecOptions,
	logger *slog.Logger,
) (string, bool, error) {
	if priorThreadID := opts.ResumeSessionID; priorThreadID != "" {
		// thread/resume reuses the thread's persisted model and reasoning
		// effort; override the live run brief together with the fields the
		// daemon owns. The persisted prompt history is refreshed below.
		resumeParams := map[string]any{
			"threadId":              priorThreadID,
			"cwd":                   opts.Cwd,
			"model":                 nilIfEmpty(opts.Model),
			"developerInstructions": nilIfEmpty(opts.SystemPrompt),
		}
		applyCodexConnectorScope(resumeParams, opts.ConnectorApps)
		applyCodexConnectorOnlyThreadPolicy(resumeParams, opts)
		// Explicit override of the persisted reasoning effort: without
		// this, a Codex resume silently reuses whatever level the prior
		// session was created with, even when the user has flipped the
		// agent's thinking_level since. Resume must honor the live
		// config, not the stored one.
		applyCodexReasoningEffort(resumeParams, opts.ThinkingLevel)
		resumeResult, err := c.request(ctx, "thread/resume", resumeParams)
		if err == nil {
			if threadID := extractThreadID(resumeResult); threadID != "" {
				if err := c.injectRuntimeInstructions(ctx, threadID, opts.SystemPrompt); err != nil {
					return "", false, err
				}
				return threadID, true, nil
			}
			logger.Warn(
				"codex thread/resume returned no thread ID; falling back to thread/start",
				"prior_thread_id",
				priorThreadID,
			)
		} else {
			if isCodexTransportError(err) {
				logger.Warn(
					"codex thread/resume failed due to transport error; not falling back to thread/start",
					"prior_thread_id",
					priorThreadID,
					"error",
					err,
				)
				return "", false, fmt.Errorf("codex thread/resume failed: %w", err)
			}
			logger.Warn(
				"codex thread/resume failed; falling back to thread/start",
				"prior_thread_id",
				priorThreadID,
				"error",
				err,
			)
		}
	}

	startParams := map[string]any{
		"model":                  nilIfEmpty(opts.Model),
		"modelProvider":          nil,
		"profile":                nil,
		"cwd":                    opts.Cwd,
		"approvalPolicy":         nil,
		"sandbox":                nil,
		"config":                 nil,
		"baseInstructions":       nil,
		"developerInstructions":  nilIfEmpty(opts.SystemPrompt),
		"compactPrompt":          nil,
		"includeApplyPatchTool":  nil,
		"experimentalRawEvents":  false,
		"persistExtendedHistory": true,
	}
	applyCodexConnectorScope(startParams, opts.ConnectorApps)
	applyCodexConnectorOnlyThreadPolicy(startParams, opts)
	applyCodexReasoningEffort(startParams, opts.ThinkingLevel)
	c.threadStartSent = true
	c.threadStartStarted = time.Now()
	logger.Info("codex lifecycle",
		"phase", "thread_start_sent",
		"run_id", c.cfg.RunID,
		"runtime_id", c.cfg.RuntimeID,
		"pid", c.pid,
		"attempt", c.attempt,
		"active_launches", c.activeLaunches,
		"method", "thread/start",
	)
	startResult, err := c.request(ctx, "thread/start", startParams)
	if err != nil {
		return "", false, fmt.Errorf("codex thread/start failed: %w", err)
	}
	threadID := extractThreadID(startResult)
	if threadID == "" {
		return "", false, fmt.Errorf("codex thread/start returned no thread ID")
	}
	logger.Info("codex lifecycle",
		"phase", "thread_start_response",
		"run_id", c.cfg.RunID,
		"runtime_id", c.cfg.RuntimeID,
		"pid", c.pid,
		"attempt", c.attempt,
		"active_launches", c.activeLaunches,
		"method", "thread/start",
		"latency", time.Since(c.threadStartStarted).Round(time.Millisecond).String(),
		"latency_ms", time.Since(c.threadStartStarted).Milliseconds(),
	)
	// Codex refuses metadata updates on an ephemeral connector-only thread.
	if opts.ToolPolicy != ToolPolicyConnectorOnly {
		c.trySetThreadName(ctx, threadID, opts.ThreadName, logger)
	}
	return threadID, false, nil
}

// A resume override can leave the original developer message in the prompt
// history. Append the current runtime brief at developer priority before the
// next turn, preserving the conversation while replacing stale instructions.
func (c *codexClient) injectRuntimeInstructions(ctx context.Context, threadID, instructions string) error {
	if instructions == "" {
		return nil
	}
	_, err := c.request(ctx, "thread/inject_items", map[string]any{
		"threadId": threadID,
		"items": []map[string]any{{
			"type": "message",
			"role": "developer",
			"content": []map[string]any{{
				"type": "input_text",
				"text": instructions,
			}},
		}},
	})
	if err != nil {
		return fmt.Errorf("refresh codex runtime instructions: %w", err)
	}
	return nil
}

func (c *codexClient) trySetThreadName(ctx context.Context, threadID, name string, logger *slog.Logger) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	if err := c.setThreadName(ctx, threadID, name); err != nil {
		logger.Warn("codex thread/name/set failed; continuing without provider-native thread title",
			"thread_id", threadID, "error", err)
	}
}

func (c *codexClient) setThreadName(ctx context.Context, threadID, name string) error {
	_, err := c.request(ctx, "thread/name/set", map[string]any{
		"threadId": threadID,
		"name":     name,
	})
	return err
}

// applyCodexReasoningEffort writes the per-agent thinking_level into a
// Codex app-server request. The three points — thread/start.config,
// thread/resume.config, turn/start.effort — all flow through this helper
// so any future protocol/key change touches one site rather than three.
//
// The shape is detected from the params keys:
//   - turn/start always carries `input`, and the schema exposes the
//     reasoning override as the top-level `effort` field.
//   - thread/start and thread/resume nest it under
//     `config.model_reasoning_effort`.
//
// Empty `level` is a no-op: we deliberately do NOT emit a key when the
// caller didn't request an override, so the upstream defaults (config
// file, account-scoped model preference) stay in charge. This also
// guarantees `effort: ""` never reaches the CLI — Codex rejects empty
// strings on this field.
func applyCodexReasoningEffort(params map[string]any, level string) {
	if params == nil || level == "" {
		return
	}
	if _, isTurnStart := params["input"]; isTurnStart {
		params["effort"] = level
		return
	}
	cfg, _ := params["config"].(map[string]any)
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["model_reasoning_effort"] = level
	params["config"] = cfg
}

func applyCodexConnectorScope(params map[string]any, apps []ConnectorApp) {
	if params == nil || apps == nil {
		return
	}
	config, _ := params["config"].(map[string]any)
	if config == nil {
		config = map[string]any{}
	}
	appConfig := map[string]any{
		"_default": map[string]any{
			"enabled": false, "default_tools_approval_mode": "prompt",
			"destructive_enabled": false, "open_world_enabled": false,
		},
	}
	for _, app := range apps {
		tools := make(map[string]any, len(app.Tools))
		for _, tool := range app.Tools {
			tools[tool.Name] = map[string]any{"enabled": tool.Enabled}
		}
		appConfig[app.ID] = map[string]any{
			"enabled": true, "default_tools_enabled": false,
			"default_tools_approval_mode": "approve",
			"destructive_enabled":         true, "open_world_enabled": true,
			"tools": tools,
		}
	}
	config["apps"] = appConfig
	params["config"] = config
}

func resetTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func codexFirstTurnNoProgressTimeout(semanticInactivityTimeout time.Duration) time.Duration {
	if semanticInactivityTimeout <= 0 || semanticInactivityTimeout > defaultCodexFirstTurnNoProgressTimeout {
		return defaultCodexFirstTurnNoProgressTimeout
	}
	scaled := semanticInactivityTimeout * 4 / 5
	if scaled <= 0 {
		return semanticInactivityTimeout
	}
	return scaled
}

func isCodexFirstTurnProgressActivity(activity string) bool {
	return activity != "" && activity != "status:running" && activity != "error:retry"
}

func buildCodexTimeoutDiagnosticError(diag codexTimeoutDiagnostic, stderrTail string) string {
	stderrTail = sanitizeCodexDiagnostic(stderrTail)
	var msg string
	switch diag.Kind {
	case codexTimeoutFirstTurnNoProgress:
		msg = fmt.Sprintf(
			"%s after %s: received turn start but no item, message, tool, turn/completed, or error event (%s)",
			CodexFirstTurnNoProgressMarker,
			diag.Timeout,
			formatCodexDiagnosticFields(diag),
		)
	case codexTimeoutSemanticInactivity:
		msg = fmt.Sprintf("%s after %s without agent progress (last activity: %s; %s)",
			CodexSemanticInactivityMarker,
			diag.Timeout,
			nonEmptyCodexDiagnosticValue(diag.LastActivity),
			formatCodexDiagnosticFields(diag),
		)
	default:
		msg = "codex timed out"
	}
	msg = appendCodexKnownStderrHint(msg, stderrTail)
	return withAgentStderr(msg, "codex", stderrTail)
}

func formatCodexDiagnosticFields(diag codexTimeoutDiagnostic) string {
	return fmt.Sprintf("codex_version=%q thread_id=%q turn_id=%q model=%q",
		nonEmptyCodexDiagnosticValue(diag.CodexVersion),
		nonEmptyCodexDiagnosticValue(diag.ThreadID),
		nonEmptyCodexDiagnosticValue(diag.TurnID),
		formatCodexDiagnosticModel(diag.Model),
	)
}

func nonEmptyCodexDiagnosticValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func formatCodexDiagnosticModel(model string) string {
	if strings.TrimSpace(model) == "" {
		return "default(empty)"
	}
	return model
}

func appendCodexKnownStderrHint(msg, stderrTail string) string {
	if strings.Contains(stderrTail, codexModelCatalogRefreshFailureSignal) {
		return msg + "; diagnosis: Codex could not load its model catalog, which blocks the first turn. This is usually a transient network failure reaching the Codex service. Check network/proxy connectivity and retry the run, or switch to another runtime while the Codex service is unreachable"
	}
	return msg
}

func detectCodexVersionForDiagnostics(ctx context.Context, execPath string, env []string, logger *slog.Logger) string {
	versionCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(versionCtx, execPath, "--version")
	cmd.Env = env
	data, err := cmd.Output()
	if err != nil {
		if logger != nil {
			logger.Debug("codex version diagnostic failed", "error", err)
		}
		return "unknown"
	}
	version := extractVersionLine(string(data))
	if strings.TrimSpace(version) == "" {
		return "unknown"
	}
	return version
}

func trySendString(ch chan<- string, value string) {
	select {
	case ch <- value:
	default:
	}
}

// codexDeliverableOutput picks Result.Output from what the turn produced: the
// message the app-server itself labeled `phase: "final_answer"`, or — for the
// legacy `agent_message` protocol, which carries no phase — the most recent
// agent message. Never every message joined: the intermediate ones narrate the
// work between tool calls, which belongs to the transcript and not to a chat
// reply.
func codexDeliverableOutput(finalAnswer, lastAgentMessage string) string {
	if finalAnswer != "" {
		return finalAnswer
	}
	return lastAgentMessage
}

func logCodexAgentMessage(logger *slog.Logger, msg providerMessage) {
	if logger == nil {
		return
	}
	attrs := []any{
		"type", string(msg.Type),
		"tool", msg.Tool,
		"call_id", msg.CallID,
		"status", msg.Status,
		"content_len", len(msg.Content),
		"output_len", len(msg.Output),
	}
	logger.Info("codex agent message received", attrs...)
	if msg.Type == providerMessageToolResult {
		logger.Info(
			"codex tool_result observed",
			"tool",
			msg.Tool,
			"call_id",
			msg.CallID,
			"output_len",
			len(msg.Output),
		)
	}
}

func describeCodexSemanticActivity(msg providerMessage) string {
	switch msg.Type {
	case providerMessageToolUse, providerMessageToolResult:
		if msg.Tool != "" {
			return fmt.Sprintf("%s:%s", msg.Type, msg.Tool)
		}
	case providerMessageStatus:
		if msg.Status != "" {
			return fmt.Sprintf("%s:%s", msg.Type, msg.Status)
		}
	}
	return string(msg.Type)
}
