package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	daemonpkg "github.com/Intelligent-Internet/zenith-team/server/internal/daemon"
)

// doctor composes the independent facts an operator needs when an Agent
// cannot execute — session, server reachability, daemon process health,
// provider CLIs, effective configuration — and states one next action per
// blocking condition instead of a single ambiguous "offline". It never
// prints a token, an OAuth code or a private prompt.
func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose sign-in, server, daemon and provider readiness with a next action for each problem",
		Args:  cobra.NoArgs,
		RunE:  runDoctor,
	}
}

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail | skip
	Detail string `json:"detail"`
	Action string `json:"action,omitempty"`
}

type doctorReport struct {
	Version string                    `json:"version"`
	Mode    string                    `json:"mode"`
	Home    string                    `json:"home"`
	Auth    authStatusReport          `json:"auth"`
	Config  map[string]string         `json:"config"`
	Checks  []doctorCheck             `json:"checks"`
	Daemon  *daemonpkg.HealthResponse `json:"daemon,omitempty"`
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	report := doctorReport{Version: version, Config: map[string]string{}}
	home, err := daemonpkg.ZenithTeamHome()
	if err != nil {
		return err
	}
	report.Home = home
	auth, err := buildAuthStatusReport(cmd)
	if err != nil {
		return err
	}
	report.Auth = auth
	report.Mode = auth.Mode
	if cfg, err := daemonpkg.LoadCLIConfig(); err == nil {
		report.Config = effectiveConfig(cfg)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
	defer cancel()

	// 1. Session.
	switch {
	case auth.Mode == "agent" && auth.RunID != "":
		report.Checks = append(
			report.Checks,
			doctorCheck{Name: "session", Status: "ok", Detail: "Agent mode: bound to Run " + auth.RunID},
		)
	case auth.Mode == "agent":
		report.Checks = append(
			report.Checks,
			doctorCheck{
				Name:   "session",
				Status: "fail",
				Detail: auth.Session,
				Action: "the daemon must place run.json in this Run's ZENITH_TEAM_HOME; check `zen daemon status` on the host",
			},
		)
	case auth.Session == "active" || auth.Session == "renewing on next request":
		report.Checks = append(
			report.Checks,
			doctorCheck{Name: "session", Status: "ok", Detail: auth.Session + " for " + auth.Server},
		)
	case auth.Session == "signed out":
		report.Checks = append(
			report.Checks,
			doctorCheck{
				Name:   "session",
				Status: "fail",
				Detail: "no saved session",
				Action: "run `zen auth login`",
			},
		)
	default:
		report.Checks = append(
			report.Checks,
			doctorCheck{
				Name:   "session",
				Status: "fail",
				Detail: auth.Session,
				Action: "run `zen auth login` again",
			},
		)
	}

	// 2. Server reachability, independent of credentials. The session is
	// probed only when the server answers at all: a connection failure is a
	// network problem, and telling the operator to sign in again for it
	// would misclassify it as bad credentials. This also covers a
	// token refresh, whose network failure the transport reports as a
	// session error.
	if auth.Mode == "operator" {
		server := checkServerReachable(ctx, auth.Server)
		report.Checks = append(report.Checks, server)
		if auth.Session == "active" || auth.Session == "renewing on next request" {
			if server.Status == "fail" {
				report.Checks = append(report.Checks, doctorCheck{
					Name:   "session accepted",
					Status: "skip",
					Detail: "not checked: the server is unreachable",
					Action: "fix the server check above, then run `zen doctor` again",
				})
			} else {
				report.Checks = append(report.Checks, checkSessionAccepted(ctx))
			}
		}
	}

	// 3. Daemon process and providers.
	health, err := fetchDaemonHealth(ctx)
	if err != nil {
		report.Checks = append(
			report.Checks,
			doctorCheck{
				Name:   "daemon",
				Status: "warn",
				Detail: "no daemon answering on port " + fmt.Sprint(daemonHealthPort()),
				Action: "run `zen daemon start` on this machine (or open the desktop app)",
			},
		)
	} else {
		report.Daemon = health
		status := "ok"
		if health.Status != "running" {
			status = "warn"
		}
		report.Checks = append(report.Checks, doctorCheck{
			Name:   "daemon",
			Status: status,
			Detail: fmt.Sprintf(
				"%s (pid %d, version %s, %d active runs, %d companies)",
				health.Status,
				health.PID,
				health.CLIVersion,
				health.ActiveRunCount,
				len(health.Companies),
			),
		})
		if health.CLIVersion != "" && health.CLIVersion != version {
			report.Checks = append(
				report.Checks,
				doctorCheck{
					Name:   "daemon version",
					Status: "warn",
					Detail: "daemon runs " + health.CLIVersion + ", this binary is " + version,
					Action: "run `zen daemon restart` to switch the daemon to this binary",
				},
			)
		}
		if len(health.Agents) == 0 {
			report.Checks = append(
				report.Checks,
				doctorCheck{
					Name:   "providers",
					Status: "fail",
					Detail: "the daemon found no provider CLI",
					Action: "install claude or codex, sign in to it, then `zen daemon restart`",
				},
			)
		} else {
			report.Checks = append(
				report.Checks,
				doctorCheck{Name: "providers", Status: "ok", Detail: strings.Join(health.Agents, ", ")},
			)
		}
		for provider, reason := range health.SkippedAgents {
			report.Checks = append(
				report.Checks,
				doctorCheck{
					Name:   "provider " + provider,
					Status: "warn",
					Detail: reason,
					Action: "upgrade or repair the " + provider + " CLI, then `zen daemon restart`",
				},
			)
		}
		if health.ReloadPendingReason != "" {
			report.Checks = append(
				report.Checks,
				doctorCheck{
					Name:   "restart pending",
					Status: "warn",
					Detail: health.ReloadPendingReason,
				},
			)
		}
		if health.SignedOut {
			report.Checks = append(
				report.Checks,
				doctorCheck{
					Name:   "daemon session",
					Status: "fail",
					Detail: "the server ended the daemon's session; it calls nothing until a new one is stored",
					Action: "run `zen daemon start` (or sign in to the desktop app)",
				},
			)
		}
	}

	// 4. Local files the CLI depends on.
	for _, name := range []string{"config.json", "daemon.id"} {
		if _, err := os.Stat(filepath.Join(home, name)); err != nil {
			report.Checks = append(
				report.Checks,
				doctorCheck{Name: name, Status: "skip", Detail: "not present yet"},
			)
		}
	}

	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if format == outputJSON {
		return printJSON(out, report)
	}
	fmt.Fprintf(out, "zen %s · mode %s · home %s\n\n", report.Version, report.Mode, report.Home)
	rows := make([][]string, 0, len(report.Checks))
	for _, check := range report.Checks {
		rows = append(rows, []string{strings.ToUpper(check.Status), check.Name, check.Detail, check.Action})
	}
	printTable(out, []string{"STATUS", "CHECK", "DETAIL", "NEXT ACTION"}, rows)
	if len(report.Config) != 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(
			out,
			"effective configuration (precedence: --flag > ZENITH_TEAM_* env > config.json > default):",
		)
		keys := make([]string, 0, len(report.Config))
		for key := range report.Config {
			keys = append(keys, key)
		}
		sortStrings(keys)
		for _, key := range keys {
			fmt.Fprintf(out, "  %-34s %s\n", key+":", report.Config[key])
		}
	}
	return nil
}

func checkServerReachable(ctx context.Context, server string) doctorCheck {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(server, "/")+"/healthz", nil)
	if err != nil {
		return doctorCheck{Name: "server", Status: "fail", Detail: err.Error()}
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		return doctorCheck{
			Name:   "server",
			Status: "fail",
			Detail: server + " unreachable: " + err.Error(),
			Action: "check the network, or fix the server URL with `zen config set server_url <url>` / ZENITH_TEAM_SERVER_URL",
		}
	}
	_ = response.Body.Close()
	if response.StatusCode >= 400 {
		return doctorCheck{
			Name:   "server",
			Status: "warn",
			Detail: fmt.Sprintf("%s answered /healthz with status %d", server, response.StatusCode),
		}
	}
	return doctorCheck{Name: "server", Status: "ok", Detail: server + " reachable"}
}

func checkSessionAccepted(ctx context.Context) doctorCheck {
	transport, err := newOperatorTransport()
	if err != nil {
		return doctorCheck{Name: "session accepted", Status: "fail", Detail: err.Error()}
	}
	client := &apiClient{context: resolveCommandContext(), transport: transport}
	var me struct {
		Email string `json:"email"`
	}
	// Only a verdict from the server can reject a session: GET /me answers
	// 200 or 401 by contract, and the transport reports 401 as a rejected
	// session. A request that failed before an answer arrived, or an answer
	// that is not a verdict (a front door's 502, the server's own 503),
	// leaves the session unknown, not rejected.
	if err := client.get(ctx, "/me", &me); err != nil {
		var network *networkError
		var api *apiError
		switch {
		case errors.As(err, &network):
			return doctorCheck{
				Name:   "session accepted",
				Status: "skip",
				Detail: "could not reach the server: " + err.Error(),
				Action: "check the network, then run `zen doctor` again",
			}
		case errors.As(err, &api) && api.Status != http.StatusUnauthorized && api.Status != http.StatusForbidden:
			return doctorCheck{
				Name:   "session accepted",
				Status: "skip",
				Detail: "not checked: " + err.Error(),
				Action: "check the server, then run `zen doctor` again",
			}
		}
		return doctorCheck{
			Name:   "session accepted",
			Status: "fail",
			Detail: err.Error(),
			Action: "run `zen auth login` again",
		}
	}
	return doctorCheck{Name: "session accepted", Status: "ok", Detail: "signed in as " + me.Email}
}

// effectiveConfig lists the non-secret values and the rung each came from.
func effectiveConfig(cfg daemonpkg.CLIConfig) map[string]string {
	explain := func(env, saved, fallback string) string {
		if value := strings.TrimSpace(os.Getenv(env)); value != "" {
			return value + "  (" + env + ")"
		}
		if saved != "" {
			return saved + "  (config.json)"
		}
		return fallback + "  (default)"
	}
	return map[string]string{
		"server_url":  explain("ZENITH_TEAM_SERVER_URL", cfg.ServerURL, daemonpkg.DefaultServerURL),
		"company_id":  explain("ZENITH_TEAM_COMPANY_ID", cfg.CompanyID, "(not set)"),
		"device_name": explain("ZENITH_TEAM_DEVICE_NAME", cfg.DeviceName, "hostname"),
		"workspaces_root": explain(
			"ZENITH_TEAM_WORKSPACES_ROOT",
			cfg.WorkspacesRoot,
			"~/zenith_team_workspaces",
		),
		"poll_interval": explain(
			"ZENITH_TEAM_POLL_INTERVAL",
			cfg.PollInterval,
			daemonpkg.DefaultPollInterval.String(),
		),
		"heartbeat_interval": explain(
			"ZENITH_TEAM_HEARTBEAT_INTERVAL",
			cfg.HeartbeatInterval,
			daemonpkg.DefaultHeartbeatInterval.String(),
		),
		"max_concurrent_runs": explain(
			"ZENITH_TEAM_MAX_CONCURRENT_RUNS",
			intOrEmpty(cfg.MaxConcurrentRuns),
			fmt.Sprint(daemonpkg.DefaultMaxConcurrentRuns),
		),
		"log_level":   explain("ZENITH_TEAM_LOG_LEVEL", cfg.LogLevel, "info"),
		"health_port": explain("ZENITH_TEAM_HEALTH_PORT", "", fmt.Sprint(daemonpkg.DefaultHealthPort)),
	}
}

func intOrEmpty(value int) string {
	if value == 0 {
		return ""
	}
	return fmt.Sprint(value)
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

var _ = json.Marshal
