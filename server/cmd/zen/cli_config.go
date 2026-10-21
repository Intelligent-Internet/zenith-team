package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	daemonpkg "github.com/Intelligent-Internet/zenith-team/server/internal/daemon"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage the Zenith Team CLI configuration",
		RunE:  runConfigShow,
	}
	show := &cobra.Command{
		Use:   "show",
		Short: "Show the current CLI configuration",
		RunE:  runConfigShow,
	}
	set := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a CLI configuration value",
		Long: "Supported keys: server_url, company_id, device_name, workspaces_root, " +
			"poll_interval, heartbeat_interval, max_concurrent_runs, agent_timeout, " +
			"codex_semantic_inactivity_timeout, codex_handshake_timeout, log_level, " +
			"disable_auto_reload.\n\n" +
			"server_url and company_id are the CLI's own defaults. The daemon keys " +
			"mirror their `daemon start` --flag / ZENITH_TEAM_* env counterparts and are " +
			"read by `daemon start` when neither the flag nor the env var is set. " +
			"Precedence: --flag > ZENITH_TEAM_* env > config.json > built-in default. " +
			"Duration keys take a positive Go duration (e.g. '10s', '1m30s'); " +
			"agent_timeout also accepts '0s', which disables the wall-clock cap. " +
			"log_level takes debug, info, warn or error and defaults to info. " +
			"disable_auto_reload true keeps a running daemon on its binary even after " +
			"that binary is replaced on disk (restart it with `daemon restart`). " +
			"Pass an empty string to clear a persisted value " +
			"(e.g. `config set poll_interval \"\"`).",
		Args: cobra.ExactArgs(2),
		RunE: runConfigSet,
	}
	path := &cobra.Command{
		Use:   "path",
		Short: "Print the state directory (ZENITH_TEAM_HOME) and the files zen keeps there",
		Args:  cobra.NoArgs,
		RunE:  runConfigPath,
	}
	operatorOnly(set)
	cmd.AddCommand(show, set, path)
	return cmd
}

func runConfigPath(cmd *cobra.Command, _ []string) error {
	home, err := daemonpkg.ZenithTeamHome()
	if err != nil {
		return err
	}
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	files := map[string]string{
		"home":             home,
		"config":           filepath.Join(home, "config.json"),
		"credential":       filepath.Join(home, "auth.json"),
		"daemonCredential": filepath.Join(home, "daemon-auth.json"),
		"daemonId":         filepath.Join(home, "daemon.id"),
		"daemonLog":        filepath.Join(home, "daemon.log"),
	}
	if format == outputJSON {
		return printJSON(cmd.OutOrStdout(), files)
	}
	printKeyValues(cmd.OutOrStdout(), [][2]string{
		{"home", files["home"]},
		{"config", files["config"]},
		{"credential", files["credential"]},
		{"daemon credential", files["daemonCredential"]},
		{"daemon id", files["daemonId"]},
		{"daemon log", files["daemonLog"]},
	})
	return nil
}

func runConfigShow(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	cfg, err := daemonpkg.LoadCLIConfig()
	if err != nil {
		return err
	}
	home, err := daemonpkg.ZenithTeamHome()
	if err != nil {
		return err
	}
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	if format == outputJSON {
		// The saved settings untouched, under the keys config.json uses,
		// plus the file they were read from.
		return printJSON(out, struct {
			File string `json:"file"`
			daemonpkg.CLIConfig
		}{File: filepath.Join(home, "config.json"), CLIConfig: cfg})
	}

	unset := func(value string) string {
		if value == "" {
			return "(not set)"
		}
		return value
	}
	fmt.Fprintf(out, "Config file: %s\n", filepath.Join(home, "config.json"))
	fmt.Fprintf(out, "%-34s %s\n", "server_url:", unset(cfg.ServerURL))
	fmt.Fprintf(out, "%-34s %s\n", "company_id:", unset(cfg.CompanyID))
	fmt.Fprintf(out, "%-34s %s\n", "device_name:", unset(cfg.DeviceName))
	fmt.Fprintf(out, "%-34s %s\n", "workspaces_root:", unset(cfg.WorkspacesRoot))
	fmt.Fprintf(out, "%-34s %s\n", "poll_interval:", unset(cfg.PollInterval))
	fmt.Fprintf(out, "%-34s %s\n", "heartbeat_interval:", unset(cfg.HeartbeatInterval))
	maxRuns := "(not set)"
	if cfg.MaxConcurrentRuns > 0 {
		maxRuns = strconv.Itoa(cfg.MaxConcurrentRuns)
	}
	fmt.Fprintf(out, "%-34s %s\n", "max_concurrent_runs:", maxRuns)
	agentTimeout := unset(cfg.AgentTimeout)
	if cfg.AgentTimeout == "0s" {
		agentTimeout = "0s (cap disabled)"
	}
	fmt.Fprintf(out, "%-34s %s\n", "agent_timeout:", agentTimeout)
	fmt.Fprintf(out, "%-34s %s\n", "codex_semantic_inactivity_timeout:", unset(cfg.CodexSemanticInactivityTimeout))
	fmt.Fprintf(out, "%-34s %s\n", "codex_handshake_timeout:", unset(cfg.CodexHandshakeTimeout))
	fmt.Fprintf(out, "%-34s %s\n", "log_level:", unset(cfg.LogLevel))
	autoReload := "(not set)"
	if cfg.DisableAutoReload {
		autoReload = "true"
	}
	fmt.Fprintf(out, "%-34s %s\n", "disable_auto_reload:", autoReload)
	return nil
}

func runConfigSet(cmd *cobra.Command, args []string) error {
	key, value := args[0], strings.TrimSpace(args[1])
	cfg, err := daemonpkg.LoadCLIConfig()
	if err != nil {
		return err
	}

	// A positive Go duration; the empty string clears the persisted value.
	duration := func(target *string, allowZero bool) error {
		if value == "" {
			*target = ""
			return nil
		}
		d, err := time.ParseDuration(value)
		if err != nil || d < 0 || (d == 0 && !allowZero) {
			return fmt.Errorf("%s must be a positive Go duration (e.g. '10s', '1m30s')", key)
		}
		*target = d.String()
		return nil
	}

	switch key {
	case "server_url":
		if value == "" {
			cfg.ServerURL = ""
			break
		}
		normalized, err := daemonpkg.NormalizeServerBaseURL(value)
		if err != nil {
			return err
		}
		cfg.ServerURL = normalized
	case "company_id":
		if value != "" {
			if _, err := uuid.Parse(value); err != nil {
				return fmt.Errorf("company_id %q is not a UUID", value)
			}
		}
		cfg.CompanyID = value
	case "device_name":
		cfg.DeviceName = value
	case "workspaces_root":
		cfg.WorkspacesRoot = value
	case "poll_interval":
		if err := duration(&cfg.PollInterval, false); err != nil {
			return err
		}
	case "heartbeat_interval":
		if err := duration(&cfg.HeartbeatInterval, false); err != nil {
			return err
		}
	case "max_concurrent_runs":
		if value == "" {
			cfg.MaxConcurrentRuns = 0
			break
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return fmt.Errorf("max_concurrent_runs must be a positive integer")
		}
		cfg.MaxConcurrentRuns = n
	case "agent_timeout":
		// 0 is meaningful here: it disables the per-run wall-clock cap.
		if err := duration(&cfg.AgentTimeout, true); err != nil {
			return err
		}
	case "codex_semantic_inactivity_timeout":
		if err := duration(&cfg.CodexSemanticInactivityTimeout, false); err != nil {
			return err
		}
	case "codex_handshake_timeout":
		if err := duration(&cfg.CodexHandshakeTimeout, false); err != nil {
			return err
		}
	case "log_level":
		if value == "" {
			cfg.LogLevel = ""
			break
		}
		// Store the normalized spelling so `config show` and the daemon's
		// own ladder never disagree about what was persisted.
		if _, ok := parseLogLevel(value); !ok {
			return fmt.Errorf("log_level must be debug, info, warn, or error")
		}
		cfg.LogLevel = strings.ToLower(value)
	case "disable_auto_reload":
		if value == "" {
			cfg.DisableAutoReload = false
			break
		}
		disabled, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("disable_auto_reload must be true or false")
		}
		cfg.DisableAutoReload = disabled
	default:
		return usagef("unsupported key %q; run `zen config set --help` for the supported keys", key)
	}

	if err := daemonpkg.SaveCLIConfig(cfg); err != nil {
		return err
	}
	if value == "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Cleared %s\n", key)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Set %s\n", key)
	}
	return nil
}
