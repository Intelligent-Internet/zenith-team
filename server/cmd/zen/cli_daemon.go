package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"boardly.dev/boardly/server/internal/authfile"
	daemonpkg "boardly.dev/boardly/server/internal/daemon"
	"boardly.dev/boardly/server/pkg/agent"
)

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the local Zenith Team agent daemon",
	}

	start := &cobra.Command{
		Use:   "start",
		Short: "Start the daemon (detached unless --foreground)",
		RunE:  runDaemonStart,
	}
	f := start.Flags()
	f.Bool("foreground", false, "run the daemon in this process instead of detaching")
	f.String("server", "", "Zenith Team server URL (env: BOARDLY_SERVER_URL)")
	f.String(
		"daemon-id",
		"",
		"unique daemon identifier (env: BOARDLY_DAEMON_ID; default: persisted ~/.boardly/daemon.id)",
	)
	f.String("device-name", "", "human-readable device name (env: BOARDLY_DEVICE_NAME; default: hostname)")
	f.String("workspaces-root", "", "base path for run workspaces (env: BOARDLY_WORKSPACES_ROOT)")
	f.Duration("poll-interval", 0, "run poll interval (env: BOARDLY_POLL_INTERVAL)")
	f.Duration("heartbeat-interval", 0, "heartbeat interval (env: BOARDLY_HEARTBEAT_INTERVAL)")
	f.Int("max-concurrent-runs", 0, "max runs executing in parallel (env: BOARDLY_MAX_CONCURRENT_RUNS)")
	f.Duration(
		"agent-timeout",
		0,
		"absolute per-run wall-clock cap; 0 disables the cap and relies on the watchdogs (env: BOARDLY_AGENT_TIMEOUT)",
	)
	f.Duration(
		"codex-semantic-inactivity-timeout",
		0,
		"codex semantic inactivity timeout (env: BOARDLY_CODEX_SEMANTIC_INACTIVITY)",
	)
	f.Duration(
		"codex-handshake-timeout",
		0,
		"codex app-server startup RPC timeout (env: BOARDLY_CODEX_HANDSHAKE_TIMEOUT)",
	)
	f.String(
		"log-level",
		"",
		"daemon log verbosity: debug, info, warn or error (env: BOARDLY_LOG_LEVEL; default: info)",
	)
	f.Bool(
		"no-auto-reload",
		false,
		"keep running this binary even after it is replaced on disk "+
			"(env: BOARDLY_AUTO_RELOAD=false; config: disable_auto_reload)",
	)

	stop := &cobra.Command{
		Use:   "stop",
		Short: "Stop the running daemon",
		RunE:  runDaemonStop,
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show the daemon's health status",
		RunE:  runDaemonStatus,
	}
	restart := &cobra.Command{
		Use:   "restart",
		Short: "Restart the daemon into this binary, once its in-flight runs finish",
		RunE:  runDaemonRestart,
	}
	logs := &cobra.Command{
		Use:   "logs",
		Short: "Print the daemon log file paths and tail the main log",
		RunE:  runDaemonLogs,
	}
	logs.Flags().BoolP("follow", "f", false, "follow log output")
	logs.Flags().IntP("lines", "n", 50, "number of lines to show")
	probe := &cobra.Command{
		Use:    "probe-runtimes",
		Short:  "Probe locally installed provider CLIs and print the result as JSON",
		Hidden: true,
		RunE:   runDaemonProbeRuntimes,
	}

	for _, operator := range []*cobra.Command{start, stop, restart, logs, probe} {
		operatorOnly(operator)
	}
	migrate := newMigrateRunHistoryCmd()
	migrate.Hidden = true
	operatorOnly(migrate)
	cmd.AddCommand(start, stop, status, restart, logs, probe, migrate)
	return cmd
}

func runDaemonStart(cmd *cobra.Command, _ []string) error {
	if foreground, _ := cmd.Flags().GetBool("foreground"); foreground {
		return runDaemonForeground(cmd)
	}
	return startDaemonDetached(cmd)
}

// runDaemonRestart replaces the running daemon with this binary. It proves
// the replacement can sign in before the running daemon is touched, waits
// for the old process to drain its runs and exit, and only then starts the
// new one — so the machine never ends up with no daemon, and never with the
// old one still answering while the new one fails to bind.
func runDaemonRestart(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	health, err := fetchDaemonHealth(ctx)
	if err != nil {
		fmt.Fprintln(out, "daemon is not running; starting it")
		return startDaemonDetached(cmd)
	}
	if err := verifyDaemonSession(ctx); err != nil {
		return fmt.Errorf("refusing to restart: %w; the running daemon was left untouched", err)
	}
	if _, err := daemonLogLevel(cmd); err != nil {
		return err
	}
	home, err := daemonpkg.BoardlyHome()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "restarting daemon (pid %d, version %s) into version %s\n", health.PID, health.CLIVersion, version)
	stopped, exited, err := stopDaemonProcess(ctx, out, home, daemonRestartWait)
	if err != nil {
		return err
	}
	if !exited {
		return fmt.Errorf(
			"daemon (pid %d) is still stopping after %s with runs in flight; it keeps running version %s "+
				"until they finish — rerun `zen daemon restart` then, or watch `zen daemon status`",
			stopped.PID, daemonRestartWait, stopped.CLIVersion,
		)
	}
	return startDaemonDetached(cmd)
}

// runDaemonForeground runs the daemon in this process until SIGINT/SIGTERM,
// or until the daemon asks to be restarted into a replaced boardly binary —
// then this process starts the replacement detached and exits once it is
// ready.
func runDaemonForeground(cmd *cobra.Command) error {
	level, err := daemonLogLevel(cmd)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(daemonLogDestination(), &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	overrides, err := daemonOverrides(cmd)
	if err != nil {
		return err
	}
	cfg, err := daemonpkg.LoadConfig(overrides)
	if err != nil {
		return err
	}
	cfg.CLIVersion = version
	// The daemon runs on its own session (D31); a foreground start from a
	// terminal mints it from `zen auth login` as a detached start does.
	if err := ensureDaemonSession(cmd.Context()); err != nil {
		return err
	}

	d, err := daemonpkg.New(cfg, logger)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := d.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	binary := d.RestartBinary()
	if binary == "" {
		return nil
	}
	logger.Info("restarting into the replaced boardly binary", "binary", binary)
	return restartIntoBinary(ctx, cmd.OutOrStdout(), binary, forwardedStartFlags(cmd))
}

// daemonOverrides builds the LoadConfig overrides from `daemon start` flags
// and the saved config file, honoring the flag > BOARDLY_* env > config.json
// > built-in default ladder. LoadConfig itself prefers a non-zero override
// over the env, so a knob contributes an override only when its flag is set,
// or when the env is unset and config.json carries a value.
func daemonOverrides(cmd *cobra.Command) (daemonpkg.Overrides, error) {
	saved, err := daemonpkg.LoadCLIConfig()
	if err != nil {
		return daemonpkg.Overrides{}, err
	}

	stringSetting := func(flagName, envName, savedValue string) string {
		if v, _ := cmd.Flags().GetString(flagName); v != "" {
			return v
		}
		if os.Getenv(envName) != "" {
			return ""
		}
		return savedValue
	}
	durationSetting := func(flagName, envName, key, savedValue string) (time.Duration, error) {
		if d, _ := cmd.Flags().GetDuration(flagName); d > 0 {
			return d, nil
		}
		if os.Getenv(envName) != "" || savedValue == "" {
			return 0, nil
		}
		d, err := time.ParseDuration(savedValue)
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("config %s %q is not a positive duration", key, savedValue)
		}
		return d, nil
	}

	overrides := daemonpkg.Overrides{
		ServerURL:      stringSetting("server", "BOARDLY_SERVER_URL", saved.ServerURL),
		DaemonID:       stringSetting("daemon-id", "BOARDLY_DAEMON_ID", ""),
		DeviceName:     stringSetting("device-name", "BOARDLY_DEVICE_NAME", saved.DeviceName),
		WorkspacesRoot: stringSetting("workspaces-root", "BOARDLY_WORKSPACES_ROOT", saved.WorkspacesRoot),
	}
	if overrides.PollInterval, err = durationSetting(
		"poll-interval", "BOARDLY_POLL_INTERVAL", "poll_interval", saved.PollInterval,
	); err != nil {
		return daemonpkg.Overrides{}, err
	}
	if overrides.HeartbeatInterval, err = durationSetting(
		"heartbeat-interval",
		"BOARDLY_HEARTBEAT_INTERVAL",
		"heartbeat_interval",
		saved.HeartbeatInterval,
	); err != nil {
		return daemonpkg.Overrides{}, err
	}
	if overrides.CodexSemanticInactivityTimeout, err = durationSetting(
		"codex-semantic-inactivity-timeout", "BOARDLY_CODEX_SEMANTIC_INACTIVITY",
		"codex_semantic_inactivity_timeout", saved.CodexSemanticInactivityTimeout,
	); err != nil {
		return daemonpkg.Overrides{}, err
	}
	if overrides.CodexHandshakeTimeout, err = durationSetting(
		"codex-handshake-timeout", "BOARDLY_CODEX_HANDSHAKE_TIMEOUT",
		"codex_handshake_timeout", saved.CodexHandshakeTimeout,
	); err != nil {
		return daemonpkg.Overrides{}, err
	}

	if n, _ := cmd.Flags().GetInt("max-concurrent-runs"); n > 0 {
		overrides.MaxConcurrentRuns = n
	} else if os.Getenv("BOARDLY_MAX_CONCURRENT_RUNS") == "" && saved.MaxConcurrentRuns > 0 {
		overrides.MaxConcurrentRuns = saved.MaxConcurrentRuns
	}

	// agent-timeout distinguishes "unset" from "0" (0 disables the cap), so
	// only an explicitly changed flag or a saved value produces an override.
	if cmd.Flags().Changed("agent-timeout") {
		d, _ := cmd.Flags().GetDuration("agent-timeout")
		overrides.AgentTimeout = &d
	} else if os.Getenv("BOARDLY_AGENT_TIMEOUT") == "" && saved.AgentTimeout != "" {
		d, err := time.ParseDuration(saved.AgentTimeout)
		if err != nil || d < 0 {
			return daemonpkg.Overrides{}, fmt.Errorf(
				"config agent_timeout %q is not a non-negative duration", saved.AgentTimeout,
			)
		}
		overrides.AgentTimeout = &d
	}

	// The binary watch is single-direction: the flag and the saved value can
	// only turn it off. An explicit BOARDLY_AUTO_RELOAD is honored by
	// LoadConfig itself, so the saved value applies only when the env is
	// unset — the same ladder as every other knob.
	if noReload, _ := cmd.Flags().GetBool("no-auto-reload"); noReload {
		overrides.DisableAutoReload = true
	} else if os.Getenv("BOARDLY_AUTO_RELOAD") == "" && saved.DisableAutoReload {
		overrides.DisableAutoReload = true
	}
	return overrides, nil
}

// daemonLogLevel resolves the daemon's log verbosity from the same
// --flag > BOARDLY_* env > config.json > built-in default ladder as
// daemonOverrides. It walks the whole ladder itself because logging is a
// process concern rather than a daemon config field, so there is no
// LoadConfig stage behind it reading the env.
func daemonLogLevel(cmd *cobra.Command) (slog.Level, error) {
	source := "--log-level"
	value, _ := cmd.Flags().GetString("log-level")
	if value == "" {
		source, value = "BOARDLY_LOG_LEVEL", os.Getenv("BOARDLY_LOG_LEVEL")
	}
	if value == "" {
		saved, err := daemonpkg.LoadCLIConfig()
		if err != nil {
			return 0, err
		}
		source, value = "config log_level", saved.LogLevel
	}
	if strings.TrimSpace(value) == "" {
		return slog.LevelInfo, nil
	}
	level, ok := parseLogLevel(value)
	if !ok {
		return 0, fmt.Errorf("%s %q must be debug, info, warn, or error", source, value)
	}
	return level, nil
}

// daemonLogDestination is where the daemon writes its structured log.
//
// Stdout, because `daemon start` redirects the detached child's stdout into
// $BOARDLY_HOME/daemon.log — the file `daemon logs` tails and the product
// page points troubleshooting at. Stderr is left to the crash sink
// (daemon.err.log): what the runtime prints when it dies without logging.
// Sending the log to stderr instead left daemon.log empty for the life of
// the daemon and every registration and heartbeat failure in a file no
// command reads.
func daemonLogDestination() *os.File {
	return os.Stdout
}

// parseLogLevel maps the CLI's closed log-level vocabulary onto slog levels.
// The daemon logs text rather than the server's JSON, so it parses its own
// level instead of borrowing the server's logger constructor.
func parseLogLevel(value string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return 0, false
	}
}

// startDaemonDetached re-execs this binary as a detached daemon (forwarding
// every set start flag) and waits for it to report ready. A daemon that is
// already running is left alone; when it runs another version than this
// binary the user is pointed at `daemon restart`, which is the one command
// that switches it.
func startDaemonDetached(cmd *cobra.Command) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	// Give the daemon its own session here (D31), minted from the person's
	// `zen auth login` when it has none: a detached child that cannot
	// authenticate exits into its log file, leaving `daemon start` to report
	// a readiness timeout instead of the real cause.
	if err := ensureDaemonSession(ctx); err != nil {
		return err
	}
	// Same reasoning: a child that rejects its log level exits into the log
	// file, leaving `daemon start` to report a readiness timeout instead.
	if _, err := daemonLogLevel(cmd); err != nil {
		return err
	}
	home, err := daemonpkg.BoardlyHome()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("create boardly home: %w", err)
	}

	if health, err := fetchDaemonHealth(ctx); err == nil {
		fmt.Fprintf(out, "daemon already running (pid %d, status %s, version %s)\n",
			health.PID, health.Status, health.CLIVersion)
		if health.CLIVersion != version {
			fmt.Fprintf(out, "this boardly binary is version %s; run `zen daemon restart` to switch the daemon to it\n",
				version)
		}
		return nil
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own executable: %w", err)
	}
	_, err = spawnDetachedDaemon(ctx, out, home, self, forwardedStartFlags(cmd))
	return err
}

// forwardedStartFlags re-serializes the start flags the user set so the
// detached child observes the exact configuration `daemon start` was given.
func forwardedStartFlags(cmd *cobra.Command) []string {
	var args []string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if f.Name == "foreground" {
			return
		}
		args = append(args, "--"+f.Name+"="+f.Value.String())
	})
	return args
}

// runDaemonStop signals the exact process instance currently answering on
// the health port and waits a short while for it to exit. A daemon draining
// in-flight runs is reported as still stopping, not as a failure.
func runDaemonStop(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	home, err := daemonpkg.BoardlyHome()
	if err != nil {
		return err
	}
	_, exited, err := stopDaemonProcess(cmd.Context(), out, home, daemonStopWait)
	if err != nil {
		return err
	}
	if exited {
		fmt.Fprintln(out, "daemon stopped")
		return nil
	}
	fmt.Fprintln(out, "daemon is still stopping: in-flight runs are draining and it exits when they finish; "+
		"watch with `zen daemon status`")
	return nil
}

func runDaemonStatus(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	output, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	health, err := fetchDaemonHealth(cmd.Context())
	if output == outputJSON {
		report := map[string]any{"running": err == nil, "session": sessionStatus()}
		if err == nil {
			report["health"] = health
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	if err != nil {
		fmt.Fprintln(out, "daemon is not running")
		return nil
	}
	printDaemonStatusReport(out, health, sessionStatus())
	return nil
}

// printDaemonStatusReport renders the table form of `daemon status`. The
// `companies` row always prints (`none` when the daemon holds no company);
// the `skipped agents` row and the `restart pending` row appear only when
// there is something to explain — a provider the daemon found but refused,
// or a confirmed binary change waiting for the daemon to go idle — so each
// reads as an explanation rather than as a status line. The daemon builds
// agents and companies by ranging over maps, so both are sorted here.
func printDaemonStatusReport(out io.Writer, health *daemonpkg.HealthResponse, session string) {
	agents := slices.Clone(health.Agents)
	slices.Sort(agents)
	type row struct{ key, value string }
	rows := []row{
		{"status", health.Status},
		{"pid", fmt.Sprint(health.PID)},
		{"version", health.CLIVersion},
		{"uptime", health.Uptime},
		{"server", health.ServerURL},
		{"daemon id", health.DaemonID},
		{"agents", strings.Join(agents, ", ")},
		{"companies", formatHealthCompanies(health.Companies)},
		{"active runs", fmt.Sprint(health.ActiveRunCount)},
		{"session", session},
	}
	if len(health.SkippedAgents) > 0 {
		rows = append(rows, row{"skipped agents", formatSkippedAgents(health.SkippedAgents)})
	}
	if health.ReloadPendingReason != "" {
		rows = append(rows, row{"restart pending", health.ReloadPendingReason})
	}
	width := 0
	for _, r := range rows {
		width = max(width, len(r.key)+1)
	}
	for _, r := range rows {
		fmt.Fprintf(out, "%-*s %s\n", width, r.key+":", r.value)
	}
}

// formatHealthCompanies renders the companies row: each company id with the
// number of runtimes the daemon holds for it, sorted by id.
func formatHealthCompanies(companies []daemonpkg.HealthCompany) string {
	if len(companies) == 0 {
		return "none"
	}
	sorted := slices.Clone(companies)
	slices.SortFunc(sorted, func(a, b daemonpkg.HealthCompany) int {
		return strings.Compare(a.ID, b.ID)
	})
	parts := make([]string, 0, len(sorted))
	for _, company := range sorted {
		noun := "runtimes"
		if len(company.Runtimes) == 1 {
			noun = "runtime"
		}
		parts = append(parts, fmt.Sprintf("%s (%d %s)", company.ID, len(company.Runtimes), noun))
	}
	return strings.Join(parts, ", ")
}

// formatSkippedAgents renders the skipped agents row: `provider: reason`
// per provider the daemon found but refused, sorted by provider.
func formatSkippedAgents(skipped map[string]string) string {
	providers := slices.Sorted(maps.Keys(skipped))
	parts := make([]string, 0, len(providers))
	for _, provider := range providers {
		parts = append(parts, provider+": "+skipped[provider])
	}
	return strings.Join(parts, "; ")
}

func runDaemonLogs(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	follow, _ := cmd.Flags().GetBool("follow")
	lines, _ := cmd.Flags().GetInt("lines")

	home, err := daemonpkg.BoardlyHome()
	if err != nil {
		return err
	}
	logPath := filepath.Join(home, daemonLogFileName)
	errPath := filepath.Join(home, daemonErrLogFileName)
	fmt.Fprintf(out, "log:     %s\nerrors:  %s\n\n", logPath, errPath)
	data, err := os.ReadFile(logPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(out, "no log file yet")
			return nil
		}
		return err
	}
	tail := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if lines > 0 && len(tail) > lines {
		tail = tail[len(tail)-lines:]
	}
	for _, line := range tail {
		fmt.Fprintln(out, line)
	}
	if !follow {
		return nil
	}
	return followLog(out, logPath, int64(len(data)))
}

// followLog polls the log file for growth and streams appended bytes until
// interrupted. A rotation (file shrank or was replaced) restarts from the
// new beginning.
func followLog(out io.Writer, path string, offset int64) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.Size() < offset {
			offset = 0
		}
		if info.Size() == offset {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		if _, err := file.Seek(offset, io.SeekStart); err == nil {
			read, _ := io.Copy(out, file)
			offset += read
		}
		file.Close()
	}
}

// runDaemonProbeRuntimes reports the locally resolvable provider CLIs as
// JSON. It is a read-only configuration probe for machine callers (the
// desktop app), so it must not require a session or any provider present.
func runDaemonProbeRuntimes(cmd *cobra.Command, _ []string) error {
	overrides, err := daemonOverrides(cmd)
	if err != nil {
		return err
	}
	overrides.AllowNoAgents = true
	cfg, err := daemonpkg.LoadConfig(overrides)
	if err != nil {
		return err
	}
	type providerProbe struct {
		Path         string `json:"path"`
		Model        string `json:"model,omitempty"`
		LaunchHeader string `json:"launchHeader,omitempty"`
	}
	providers := make(map[string]providerProbe, len(cfg.Agents))
	for provider, entry := range cfg.Agents {
		providers[provider] = providerProbe{
			Path:         entry.Path,
			Model:        entry.Model,
			LaunchHeader: agent.LaunchHeader(provider),
		}
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
		"probeResult": "success",
		"providers":   providers,
	})
}

// requireSession reports a missing or expired credential in the CLI's voice,
// pointing at the one command that fixes it.
func requireSession() error {
	path, err := daemonpkg.CredentialPath()
	if err != nil {
		return err
	}
	credential, err := authfile.Load(path)
	if errors.Is(err, authfile.ErrNotFound) {
		return errors.New("not authenticated: run `zen auth login` first")
	}
	if err != nil {
		return fmt.Errorf("read boardly session: %w", err)
	}
	if credential.State(time.Now()) == authfile.StateExpired {
		return errors.New("Zenith Team session has expired: run `zen auth login` again")
	}
	return nil
}

// sessionStatus renders the state of the daemon's own session
// (daemon-auth.json, D31) for `daemon status`.
func sessionStatus() string {
	path, err := daemonpkg.OwnCredentialPath()
	if err != nil {
		return "unknown"
	}
	credential, err := authfile.Load(path)
	if errors.Is(err, authfile.ErrNotFound) {
		return "none yet (run `zen auth login`, then `zen daemon start`)"
	}
	if err != nil {
		return "unreadable (run `zen auth login`)"
	}
	switch credential.State(time.Now()) {
	case authfile.StateAuthenticated:
		return "active"
	case authfile.StateRefreshRequired:
		return "renewing on next request"
	default:
		return "expired (run `zen auth login`)"
	}
}
