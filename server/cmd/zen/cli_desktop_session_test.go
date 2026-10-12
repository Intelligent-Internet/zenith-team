package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"boardly.dev/boardly/server/internal/authfile"
	"boardly.dev/boardly/server/internal/daemon"
	"boardly.dev/boardly/server/internal/runchannel"
)

// executeDesktopSessionCLI runs `zen desktop-session <args>` from the root,
// as the desktop app does, with input on stdin.
func executeDesktopSessionCLI(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	cmd.SetIn(strings.NewReader(input))
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"desktop-session"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

// desktopShell is the desktop app's environment: its own BOARDLY_HOME and
// no Run.
func desktopShell(t *testing.T) {
	t.Helper()
	t.Setenv("BOARDLY_HOME", t.TempDir())
	t.Setenv("BOARDLY_RUN_ID", "")
	t.Setenv("BOARDLY_RUN_TOKEN", "")
}

// The desktop installs the daemon's own session (D31) into daemon-auth.json,
// never into the CLI's auth.json, and the command never prints the refresh
// token back.
func TestDesktopSessionInstallWritesTheDaemonSession(t *testing.T) {
	desktopShell(t)
	input := `{
		"apiBaseUrl":"https://api.boardly.test/api/v1",
		"accessToken":"access-daemon",
		"accessExpiresAt":"2026-08-19T13:30:00Z",
		"refreshToken":"refresh-daemon",
		"refreshExpiresAt":"2026-09-19T13:30:00Z"
	}`

	installed, err := executeDesktopSessionCLI(t, input, "install")
	if err != nil {
		t.Fatalf("desktop-session install: %v", err)
	}
	if strings.Contains(installed, "refresh-daemon") || strings.Contains(installed, `"refreshToken"`) {
		t.Fatalf("install output = %s, want no refresh token", installed)
	}

	path, err := daemon.OwnCredentialPath()
	if err != nil {
		t.Fatalf("daemon credential path: %v", err)
	}
	credential, err := authfile.Load(path)
	if err != nil {
		t.Fatalf("load the daemon session: %v", err)
	}
	if credential.AccessToken != "access-daemon" || credential.RefreshToken != "refresh-daemon" {
		t.Fatalf("daemon session tokens = %q / %q, want the installed tokens",
			credential.AccessToken, credential.RefreshToken)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the daemon session: %v", err)
	}
	if contents := string(raw); !strings.Contains(contents, `"accessToken"`) ||
		strings.Contains(contents, `"access_token"`) {
		t.Fatalf("daemon session = %s, want canonical camelCase keys", contents)
	}
	cliPath, err := daemon.CredentialPath()
	if err != nil {
		t.Fatalf("credential path: %v", err)
	}
	if _, err := authfile.Load(cliPath); !errors.Is(err, authfile.ErrNotFound) {
		t.Fatalf("auth.json after install: %v, want none", err)
	}
}

// read and ensure printed the session the desktop shared with the daemon;
// with the desktop holding its own session they are gone (D31).
func TestDesktopSessionReadAndEnsureAreGone(t *testing.T) {
	desktopShell(t)
	for _, action := range []string{"read", "ensure"} {
		out, err := executeDesktopSessionCLI(t, "", action)
		if err == nil || strings.Contains(out, `"session"`) {
			t.Fatalf("desktop-session %s = %q, %v; want an unknown command", action, out, err)
		}
	}
}

// clear signs the daemon's session out on the server with its own refresh
// token and deletes daemon-auth.json; it is idempotent, and it leaves the
// CLI's auth.json alone. When the server cannot be told, the file still goes
// and the command fails so the desktop can say so.
func TestDesktopSessionClearSignsTheDaemonSessionOut(t *testing.T) {
	desktopShell(t)
	var mu sync.Mutex
	var loggedOut []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/logout" {
			http.NotFound(w, r)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode logout: %v", err)
		}
		mu.Lock()
		loggedOut = append(loggedOut, body["client"]+":"+body["refreshToken"])
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	daemonPath, err := daemon.OwnCredentialPath()
	if err != nil {
		t.Fatalf("daemon credential path: %v", err)
	}
	cliPath, err := daemon.CredentialPath()
	if err != nil {
		t.Fatalf("credential path: %v", err)
	}
	for path, token := range map[string]string{daemonPath: "daemon", cliPath: "person"} {
		if err := authfile.Save(path, authfile.Credential{
			Version: authfile.CurrentVersion, APIBaseURL: server.URL + "/api/v1",
			AccessToken: "access-" + token, RefreshToken: "refresh-" + token,
		}); err != nil {
			t.Fatalf("save %s session: %v", token, err)
		}
	}

	for range 2 {
		if _, err := executeDesktopSessionCLI(t, "", "clear"); err != nil {
			t.Fatalf("desktop-session clear: %v", err)
		}
	}
	if _, err := authfile.Load(daemonPath); !errors.Is(err, authfile.ErrNotFound) {
		t.Fatalf("daemon session after clear: %v, want none", err)
	}
	if _, err := authfile.Load(cliPath); err != nil {
		t.Fatalf("the CLI session after clear: %v, want it kept", err)
	}
	mu.Lock()
	signedOut := strings.Join(loggedOut, ",")
	mu.Unlock()
	if signedOut != "desktop:refresh-daemon" {
		t.Fatalf("signed out %q, want the daemon session once", signedOut)
	}

	if err := authfile.Save(daemonPath, authfile.Credential{
		Version: authfile.CurrentVersion, APIBaseURL: "http://127.0.0.1:1/api/v1",
		AccessToken: "access-daemon", RefreshToken: "refresh-daemon",
	}); err != nil {
		t.Fatalf("save unreachable daemon session: %v", err)
	}
	if _, err := executeDesktopSessionCLI(t, "", "clear"); err == nil {
		t.Fatal("desktop-session clear reported success when the server could not be told")
	}
	if _, err := authfile.Load(daemonPath); !errors.Is(err, authfile.ErrNotFound) {
		t.Fatalf("daemon session after an unreachable clear: %v, want none", err)
	}
}

func TestDesktopSessionInstallRejectsUnknownFields(t *testing.T) {
	desktopShell(t)
	input := `{
		"apiBaseUrl":"https://api.boardly.test/api/v1",
		"accessToken":"access-desktop",
		"refreshToken":"refresh-desktop",
		"secretCopy":"must-not-be-ignored"
	}`

	if _, err := executeDesktopSessionCLI(t, input, "install"); err == nil {
		t.Fatal("desktop-session install accepted an unknown field")
	}
}

// An Agent shell must never reach the session file through this command,
// whichever sign of a Run it carries: run.json in its BOARDLY_HOME, or only
// one of the Run variables.
func TestDesktopSessionRefusesInsideARun(t *testing.T) {
	tests := []struct {
		name  string
		shell func(t *testing.T)
	}{
		{
			name: "run.json in BOARDLY_HOME",
			shell: func(t *testing.T) {
				t.Helper()
				channel := newFakeRunChannel(t, func(runchannel.Request) (int, string) {
					t.Error("a refused command used the Run channel")
					return http.StatusInternalServerError, `{}`
				})
				seedAgentShell(t, channel, testRunTaskID, nil)
				t.Setenv("BOARDLY_RUN_ID", "")
				t.Setenv("BOARDLY_RUN_TOKEN", "")
			},
		},
		{
			name: "BOARDLY_RUN_ID set",
			shell: func(t *testing.T) {
				t.Helper()
				desktopShell(t)
				t.Setenv("BOARDLY_RUN_ID", testRunID)
			},
		},
		{
			name: "BOARDLY_RUN_TOKEN set",
			shell: func(t *testing.T) {
				t.Helper()
				desktopShell(t)
				t.Setenv("BOARDLY_RUN_TOKEN", "brt_"+strings.Repeat("ab", 32))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.shell(t)
			path, err := daemon.OwnCredentialPath()
			if err != nil {
				t.Fatalf("daemon credential path: %v", err)
			}
			if err := authfile.Save(path, authfile.Credential{
				Version:      authfile.CurrentVersion,
				APIBaseURL:   "https://api.boardly.test/api/v1",
				AccessToken:  "access-desktop",
				RefreshToken: "refresh-desktop",
			}); err != nil {
				t.Fatalf("save credential: %v", err)
			}
			replacement := `{"apiBaseUrl":"https://api.boardly.test/api/v1",` +
				`"accessToken":"access-agent","refreshToken":"refresh-agent"}`

			for _, action := range []string{"install", "clear"} {
				out, err := executeDesktopSessionCLI(t, replacement, action)
				want := "`zen desktop-session " + action + "` is an operator command"
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("desktop-session %s error = %v, want %q", action, err, want)
				}
				if code := exitCodeFor(err); code != exitAuth {
					t.Fatalf("desktop-session %s exit code = %d, want %d", action, code, exitAuth)
				}
				if strings.Contains(out, "access-desktop") || strings.Contains(out, "refresh-desktop") {
					t.Fatalf("desktop-session %s output = %q, want no token", action, out)
				}
			}
			credential, err := authfile.Load(path)
			if err != nil {
				t.Fatalf("load credential after refusals: %v", err)
			}
			if credential.AccessToken != "access-desktop" || credential.RefreshToken != "refresh-desktop" {
				t.Fatalf("credential tokens = %q / %q, want the file untouched",
					credential.AccessToken, credential.RefreshToken)
			}
		})
	}
}
