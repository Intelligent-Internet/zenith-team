package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A config written before the JSON contract moved to camelCase must still
// load. Decoding it as empty would not fail — the daemon would simply forget
// its server URL and company and start against the built-in default, which is
// the one failure mode a user cannot see.
func TestLoadCLIConfigReadsAConfigWrittenBeforeTheCamelCaseContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ZENITH_TEAM_HOME", home)
	legacy := `{
		"server_url": "https://zenith-team.example",
		"company_id": "0198aa00-0000-7000-8000-0000000000c0",
		"device_name": "old-laptop",
		"poll_interval": "45s",
		"max_concurrent_runs": 7
	}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	cfg, err := LoadCLIConfig()
	if err != nil {
		t.Fatalf("LoadCLIConfig() error = %v", err)
	}
	if cfg.ServerURL != "https://zenith-team.example" || cfg.CompanyID != "0198aa00-0000-7000-8000-0000000000c0" {
		t.Fatalf("config = %+v, want the legacy server URL and company preserved", cfg)
	}
	if cfg.DeviceName != "old-laptop" || cfg.PollInterval != "45s" || cfg.MaxConcurrentRuns != 7 {
		t.Fatalf("config = %+v, want every legacy daemon key preserved", cfg)
	}

	// Saving rewrites the file in the current shape, so the migration is a
	// one-time read path rather than a permanent dual contract.
	if err := SaveCLIConfig(cfg); err != nil {
		t.Fatalf("SaveCLIConfig() error = %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatalf("read rewritten config: %v", err)
	}
	if contents := string(raw); !strings.Contains(contents, `"serverUrl"`) ||
		strings.Contains(contents, `"server_url"`) {
		t.Fatalf("rewritten config = %s, want camelCase keys only", contents)
	}
}

func TestLoadCLIConfigPrefersTheCurrentKeyWhenBothSpellingsArePresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ZENITH_TEAM_HOME", home)
	mixed := `{"serverUrl": "https://current.example", "server_url": "https://legacy.example"}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(mixed), 0o600); err != nil {
		t.Fatalf("write mixed config: %v", err)
	}

	cfg, err := LoadCLIConfig()
	if err != nil {
		t.Fatalf("LoadCLIConfig() error = %v", err)
	}
	if cfg.ServerURL != "https://current.example" {
		t.Fatalf("ServerURL = %q, want the current spelling to win", cfg.ServerURL)
	}
}

// The credential paths a Run's Claude settings deny are the daemon's real
// home (where its daemon-auth.json lives), the CLI's default home and, when
// the desktop names it, the desktop app's own session store.
func TestProtectedCredentialPathsNameTheDaemonHomeAndTheCLIHome(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("USERPROFILE", user)
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	desktopHome := filepath.Join(t.TempDir(), "zenith-team-home")
	desktopStore := filepath.Join(filepath.Dir(desktopHome), "desktop-session.bin")

	tests := []struct {
		name           string
		zenithTeamHome string
		desktopStore   string
		want           []string
	}{
		{
			name:           "desktop daemon home",
			zenithTeamHome: desktopHome,
			want:           []string{desktopHome, filepath.Join(user, ".zenith-team")},
		},
		{
			name:           "desktop session store",
			zenithTeamHome: desktopHome,
			desktopStore:   desktopStore,
			want:           []string{desktopHome, filepath.Join(user, ".zenith-team"), desktopStore},
		},
		{
			name:           "relative home made absolute",
			zenithTeamHome: "rig-home",
			want: []string{
				filepath.Join(working, "rig-home"),
				filepath.Join(user, ".zenith-team"),
			},
		},
		{
			name: "default home named once",
			want: []string{filepath.Join(user, ".zenith-team")},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("ZENITH_TEAM_HOME", test.zenithTeamHome)
			t.Setenv(DesktopSessionPathEnv, test.desktopStore)
			got := protectedCredentialPaths()
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Fatalf("protectedCredentialPaths() = %q, want %q", got, test.want)
			}
		})
	}
}

// The daemon's own session sits beside the CLI's in the same home, in a
// file of its own; the daemon reads only that one.
func TestOwnCredentialPathIsBesideTheCLISession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ZENITH_TEAM_HOME", home)
	daemonPath, err := OwnCredentialPath()
	if err != nil {
		t.Fatal(err)
	}
	cliPath, err := CredentialPath()
	if err != nil {
		t.Fatal(err)
	}
	if daemonPath != filepath.Join(home, "daemon-auth.json") || cliPath != filepath.Join(home, "auth.json") {
		t.Fatalf("daemon session at %q and CLI session at %q; want daemon-auth.json and auth.json in %q",
			daemonPath, cliPath, home)
	}
}
