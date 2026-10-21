package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// ZenithTeamHome returns the daemon state directory (~/.zenith-team, or
// $ZENITH_TEAM_HOME when set).
func ZenithTeamHome() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("ZENITH_TEAM_HOME")); configured != "" {
		return configured, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".zenith-team"), nil
}

// DesktopSessionPathEnv names the desktop app's own session store, which the
// desktop passes to the daemon it starts. The daemon never reads the
// file; it only keeps every Run away from it.
const DesktopSessionPathEnv = "ZENITH_TEAM_DESKTOP_SESSION_PATH"

// protectedCredentialPaths lists where a Zenith Team sign-in can live on
// this computer: the daemon's real ZENITH_TEAM_HOME (its daemon-auth.json, and an
// operator's auth.json), the CLI's default home, ~/.zenith-team, and the desktop
// app's own session store when the desktop names it. Every Run's Claude
// settings deny its file tools these paths (execenv); the ones that do not
// exist are dropped there.
func protectedCredentialPaths() []string {
	paths := make([]string, 0, 3)
	if home, err := ZenithTeamHome(); err == nil {
		if absolute, err := filepath.Abs(home); err == nil {
			paths = append(paths, absolute)
		}
	}
	if user, err := os.UserHomeDir(); err == nil {
		if cliHome := filepath.Join(user, ".zenith-team"); !slices.Contains(paths, cliHome) {
			paths = append(paths, cliHome)
		}
	}
	if store := strings.TrimSpace(os.Getenv(DesktopSessionPathEnv)); store != "" {
		if absolute, err := filepath.Abs(store); err == nil && !slices.Contains(paths, absolute) {
			paths = append(paths, absolute)
		}
	}
	return paths
}

// EnsureDaemonID returns a stable UUIDv7 for this daemon instance, persisting
// it to ~/.zenith-team/daemon.id on first call. Once created, the UUID inside is
// the daemon's identity forever — hostname changes and system renames no
// longer mint a new identity. A corrupt file is regenerated so the daemon can
// still start.
func EnsureDaemonID() (string, error) {
	dir, err := ZenithTeamHome()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "daemon.id")

	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			if _, perr := uuid.Parse(id); perr == nil {
				return id, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read daemon id file: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate daemon id: %w", err)
	}
	if err := writeDaemonIDFile(path, id.String()); err != nil {
		return "", err
	}
	return id.String(), nil
}

// writeDaemonIDFile writes the UUID to path atomically with 0600 mode.
func writeDaemonIDFile(path, id string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".daemon-*.id.tmp")
	if err != nil {
		return fmt.Errorf("create temp daemon id file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(id + "\n"); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp daemon id file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp daemon id file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp daemon id file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename daemon id file: %w", err)
	}
	return nil
}

// CLIConfig is the persisted ~/.zenith-team/config.json: the CLI's non-secret
// settings file, written by `zen auth login` and `zen config set`. The
// session credential deliberately lives elsewhere (CredentialPath, written
// 0600 with a lock) so tokens never share a file with settings a user may
// paste into a bug report.
//
// The daemon keys (deviceName through logLevel) mirror their
// `daemon start` --flag / ZENITH_TEAM_* env counterparts and apply only when
// neither the flag nor the env var is set. Durations are stored as Go
// duration strings ("30s", "1m30s") and validated by `config set`.
type CLIConfig struct {
	ServerURL string `json:"serverUrl,omitempty"`
	CompanyID string `json:"companyId,omitempty"`

	DeviceName                     string `json:"deviceName,omitempty"`
	WorkspacesRoot                 string `json:"workspacesRoot,omitempty"`
	PollInterval                   string `json:"pollInterval,omitempty"`
	HeartbeatInterval              string `json:"heartbeatInterval,omitempty"`
	MaxConcurrentRuns              int    `json:"maxConcurrentRuns,omitempty"`
	AgentTimeout                   string `json:"agentTimeout,omitempty"`
	CodexSemanticInactivityTimeout string `json:"codexSemanticInactivityTimeout,omitempty"`
	CodexHandshakeTimeout          string `json:"codexHandshakeTimeout,omitempty"`
	LogLevel                       string `json:"logLevel,omitempty"`
	// DisableAutoReload persists `config set disable_auto_reload true`: the
	// daemon then keeps running a replaced zenith-team binary until restarted.
	DisableAutoReload bool `json:"disableAutoReload,omitempty"`
}

const (
	cliConfigFileName = "config.json"
	// credentialName is the person's session `zen auth login` writes for
	// the CLI. The daemon never reads it, and zenith-team-mcp reaches the
	// server only through the Run channel.
	credentialName = "auth.json"
	// daemonCredentialName is the daemon's own session, minted from
	// the person's by `zen daemon start` or installed by the desktop app.
	daemonCredentialName = "daemon-auth.json"
)

// CredentialPath is where `zen auth login` stores the person's rotating
// session credential for the CLI.
func CredentialPath() (string, error) {
	dir, err := ZenithTeamHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, credentialName), nil
}

// OwnCredentialPath is where the daemon keeps its own session. The
// server accepts it only on the daemon plane and beside a live Run proof of
// the same person, so the daemon reads this file and never auth.json.
func OwnCredentialPath() (string, error) {
	dir, err := ZenithTeamHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, daemonCredentialName), nil
}

// LoadCLIConfig reads ~/.zenith-team/config.json. A missing file returns a zero
// config, not an error — callers decide whether an empty token is fatal.
func LoadCLIConfig() (CLIConfig, error) {
	dir, err := ZenithTeamHome()
	if err != nil {
		return CLIConfig{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, cliConfigFileName))
	if errors.Is(err, os.ErrNotExist) {
		return CLIConfig{}, nil
	}
	if err != nil {
		return CLIConfig{}, fmt.Errorf("read CLI config: %w", err)
	}
	var cfg CLIConfig
	if err := json.Unmarshal(migrateLegacyConfigKeys(data), &cfg); err != nil {
		return CLIConfig{}, fmt.Errorf("parse CLI config: %w", err)
	}
	return cfg, nil
}

// migrateLegacyConfigKeys rekeys a config written before the JSON contract
// moved to camelCase. Without it an upgrade decodes every field as empty and
// the daemon silently forgets its server URL and company instead of failing.
// The next SaveCLIConfig rewrites the file in the current shape.
func migrateLegacyConfigKeys(data []byte) []byte {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return data
	}
	migrated := false
	for key, value := range raw {
		if !strings.Contains(key, "_") {
			continue
		}
		delete(raw, key)
		if camel := camelConfigKey(key); camel != "" {
			if _, taken := raw[camel]; !taken {
				raw[camel] = value
			}
			migrated = true
		}
	}
	if !migrated {
		return data
	}
	rekeyed, err := json.Marshal(raw)
	if err != nil {
		return data
	}
	return rekeyed
}

func camelConfigKey(key string) string {
	parts := strings.Split(key, "_")
	var builder strings.Builder
	for index, part := range parts {
		if part == "" {
			continue
		}
		if index == 0 {
			builder.WriteString(part)
			continue
		}
		builder.WriteString(strings.ToUpper(part[:1]))
		builder.WriteString(part[1:])
	}
	return builder.String()
}

// SaveCLIConfig writes ~/.zenith-team/config.json with 0600 mode.
func SaveCLIConfig(cfg CLIConfig) error {
	dir, err := ZenithTeamHome()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create zenith-team home: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal CLI config: %w", err)
	}
	path := filepath.Join(dir, cliConfigFileName)
	tmp, err := os.CreateTemp(dir, ".config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp CLI config: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp CLI config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp CLI config: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp CLI config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename CLI config: %w", err)
	}
	return nil
}
