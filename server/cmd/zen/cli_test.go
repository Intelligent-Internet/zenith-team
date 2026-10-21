package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Intelligent-Internet/zenith-team/server/internal/authfile"
	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon"
)

// executeCLI runs a freshly built command the way a user would, capturing
// its output. Commands print through cmd.OutOrStdout(), so SetOut is the
// only seam the tests need.
func executeCLI(t *testing.T, newCmd func() *cobra.Command, args ...string) (string, error) {
	t.Helper()
	cmd := newCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// bearerSession is the login/refresh payload the server returns to a desktop
// client: a short-lived access token plus the rotating refresh token.
func bearerSession(accessToken, refreshToken string, accessTTL time.Duration) map[string]any {
	return map[string]any{
		"user": map[string]any{"id": "u1"},
		"session": map[string]any{
			"transport":        "bearer",
			"accessToken":      accessToken,
			"refreshToken":     refreshToken,
			"accessExpiresAt":  time.Now().Add(accessTTL).UTC().Format(time.RFC3339),
			"refreshExpiresAt": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
}

func TestRunLoginPersistsRenewableSession(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	var verified string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode login body: %v", err)
			}
			if req["client"] != "desktop" {
				t.Fatalf("client = %v, want desktop", req["client"])
			}
			json.NewEncoder(w).Encode(bearerSession("access-token-1", "refresh-token-1", 15*time.Minute))
		case "/api/v1/me":
			verified = r.Header.Get("Authorization")
			json.NewEncoder(w).Encode(map[string]any{"id": "u1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	out, err := executeCLI(
		t,
		newRootCmd,
		"auth",
		"login",
		"--server",
		srv.URL,
		"--email",
		"user@example.com",
		"--password",
		"hunter22",
	)
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	if verified != "Bearer access-token-1" {
		t.Fatalf("verification authorization = %q, want the new access token", verified)
	}

	path, err := daemon.CredentialPath()
	if err != nil {
		t.Fatalf("credential path: %v", err)
	}
	credential, err := authfile.Load(path)
	if err != nil {
		t.Fatalf("load credential: %v", err)
	}
	if credential.AccessToken != "access-token-1" || credential.RefreshToken != "refresh-token-1" {
		t.Fatalf("credential = %+v, want both tokens persisted", credential)
	}
	if credential.APIBaseURL != srv.URL+"/api/v1" {
		t.Fatalf("APIBaseURL = %q, want the server's /api/v1 base", credential.APIBaseURL)
	}
	if credential.AccessExpiresAt == nil || credential.RefreshExpiresAt == nil {
		t.Fatal("credential kept no expiries, so the daemon cannot renew ahead of expiry")
	}
	if credential.State(time.Now()) != authfile.StateAuthenticated {
		t.Fatalf("state = %s, want authenticated", credential.State(time.Now()))
	}

	// Settings stay in config.json; secrets stay out of it.
	cfg, err := daemon.LoadCLIConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.ServerURL != srv.URL {
		t.Fatalf("config = %+v, want the server URL", cfg)
	}
	configPath, err := daemon.ZenithTeamHome()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	raw, err := os.ReadFile(configPath + "/config.json")
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, secret := range []string{"access-token-1", "refresh-token-1"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("config.json leaked %q", secret)
		}
	}

	if !strings.Contains(out, "Signed in") {
		t.Fatalf("output = %q", out)
	}
}

func TestRunLoginRejectsSessionTheServerWillNotAccept(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			json.NewEncoder(w).Encode(bearerSession("access-token-1", "refresh-token-1", 15*time.Minute))
		case "/api/v1/me":
			// A credential the server refuses must never reach disk: the
			// daemon would otherwise fail mid-run instead of at login.
			w.WriteHeader(http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	if _, err := executeCLI(
		t,
		newRootCmd,
		"auth",
		"login",
		"--server",
		srv.URL,
		"--email",
		"user@example.com",
		"--password",
		"hunter22",
	); err == nil {
		t.Fatal("login accepted a session the server rejected")
	}
	path, err := daemon.CredentialPath()
	if err != nil {
		t.Fatalf("credential path: %v", err)
	}
	if _, err := authfile.Load(path); err == nil {
		t.Fatal("credential was written despite failed verification")
	}
}

func TestRunLoginSurfacesAPIError(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{"detail": "invalid credentials"})
	}))
	defer srv.Close()

	_, err := executeCLI(
		t,
		newRootCmd,
		"auth",
		"login",
		"--server",
		srv.URL,
		"--email",
		"user@example.com",
		"--password",
		"wrong",
	)
	if err == nil || !strings.Contains(err.Error(), "invalid credentials") {
		t.Fatalf("login error = %v, want the server detail", err)
	}
}

func TestRequireSessionGuidesAnUnauthenticatedStart(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	err := requireSession()
	if err == nil || !strings.Contains(err.Error(), "zen auth login") {
		t.Fatalf("requireSession() error = %v, want guidance to run zen auth login", err)
	}
	status := sessionStatus()
	if !strings.Contains(status, "none yet") || !strings.Contains(status, "zen auth login") {
		t.Fatalf("sessionStatus() = %q, want a report that the daemon has no session yet", status)
	}
}

// The Google path drives the server's desktop OAuth flow: start with a PKCE
// challenge, browser consent (simulated by hitting the loopback redirect),
// complete with the verifier, and persist the same bearer session shape the
// password login yields.
func TestRunBrowserLoginCompletesTheDesktopOAuthFlow(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	restore := openBrowser
	openBrowser = func(string) {}
	t.Cleanup(func() { openBrowser = restore })
	var flowChallenge, completedVerifier string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/google/desktop/start":
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode start body: %v", err)
			}
			flowChallenge = req["codeChallenge"]
			// Simulate the user's consent: Google redirects the browser to
			// the CLI's loopback listener with the state echo and a code.
			go func(redirect string) {
				resp, err := http.Get(redirect + "?state=state-1&code=google-code-1")
				if err == nil {
					resp.Body.Close()
				}
			}(req["redirectUri"])
			json.NewEncoder(w).Encode(map[string]any{
				"flowId":           "0195c48e-7bb4-7f2a-9e21-101b9715febb",
				"state":            "state-1",
				"authorizationUrl": "https://accounts.google.example/consent",
			})
		case "/api/v1/auth/google/desktop/complete":
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode complete body: %v", err)
			}
			if req["code"] != "google-code-1" || req["state"] != "state-1" {
				t.Fatalf("complete request = %+v, want the callback's code and state", req)
			}
			completedVerifier = req["verifier"]
			json.NewEncoder(w).Encode(bearerSession("access-oauth", "refresh-oauth", 15*time.Minute))
		case "/api/v1/me":
			json.NewEncoder(w).Encode(map[string]any{"id": "u1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	if _, err := executeCLI(t, newRootCmd, "auth", "login", "--server", srv.URL, "--google"); err != nil {
		t.Fatalf("browser login: %v", err)
	}
	if flowChallenge == "" || completedVerifier == "" {
		t.Fatal("PKCE pair never reached the server")
	}
	sum := sha256.Sum256([]byte(completedVerifier))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != flowChallenge {
		t.Fatal("verifier does not hash to the challenge sent at start")
	}
	path, err := daemon.CredentialPath()
	if err != nil {
		t.Fatalf("credential path: %v", err)
	}
	credential, err := authfile.Load(path)
	if err != nil {
		t.Fatalf("load credential: %v", err)
	}
	if credential.AccessToken != "access-oauth" || credential.RefreshToken != "refresh-oauth" {
		t.Fatalf("credential = %+v, want the OAuth session persisted", credential)
	}
}

func TestRunCodeLoginRedeemsAHandoffCode(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/desktop/handoff/complete":
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode handoff body: %v", err)
			}
			if req["code"] != "handoff-1" {
				t.Fatalf("code = %v, want the pasted handoff code", req["code"])
			}
			json.NewEncoder(w).Encode(bearerSession("access-handoff", "refresh-handoff", 15*time.Minute))
		case "/api/v1/me":
			json.NewEncoder(w).Encode(map[string]any{"id": "u1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	if _, err := executeCLI(
		t,
		newRootCmd,
		"auth",
		"login",
		"--server",
		srv.URL,
		"--code",
		"handoff-1",
	); err != nil {
		t.Fatalf("code login: %v", err)
	}
	path, err := daemon.CredentialPath()
	if err != nil {
		t.Fatalf("credential path: %v", err)
	}
	if _, err := authfile.Load(path); err != nil {
		t.Fatalf("load credential: %v", err)
	}
}
