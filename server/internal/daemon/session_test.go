package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/authfile"
)

// writeCredential stores the daemon's own session (daemon-auth.json)
// with an access token that expires after accessTTL, as `zen daemon start`
// or the desktop app would.
func writeCredential(t *testing.T, baseURL, accessToken, refreshToken string, accessTTL time.Duration) string {
	t.Helper()
	path, err := OwnCredentialPath()
	if err != nil {
		t.Fatalf("daemon credential path: %v", err)
	}
	accessExpiry := time.Now().Add(accessTTL)
	refreshExpiry := time.Now().Add(30 * 24 * time.Hour)
	credential := authfile.Credential{
		Version:          authfile.CurrentVersion,
		APIBaseURL:       baseURL + "/api/v1",
		AccessToken:      accessToken,
		AccessExpiresAt:  &accessExpiry,
		RefreshToken:     refreshToken,
		RefreshExpiresAt: &refreshExpiry,
	}
	if err := authfile.Save(path, credential); err != nil {
		t.Fatalf("save credential: %v", err)
	}
	return path
}

func sessionServer(t *testing.T, refreshes *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/refresh" {
			http.NotFound(w, r)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode refresh body: %v", err)
		}
		if body["refreshToken"] != "refresh-1" {
			t.Fatalf("refreshToken = %q, want the stored refresh token", body["refreshToken"])
		}
		*refreshes++
		json.NewEncoder(w).Encode(map[string]any{
			"session": map[string]any{
				"transport":        "bearer",
				"accessToken":      "access-2",
				"refreshToken":     "refresh-2",
				"accessExpiresAt":  time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339),
				"refreshExpiresAt": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339),
			},
		})
	}))
}

// A daemon runs for days while access tokens live for minutes, so the session
// must renew from disk instead of holding the token it read at startup — and
// the rotated refresh token must land on disk, because the server treats a
// replayed refresh token as theft.
func TestFileSessionRenewsExpiredAccessTokenAndPersistsRotation(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	refreshes := 0
	srv := sessionServer(t, &refreshes)
	defer srv.Close()
	path := writeCredential(t, srv.URL, "access-1", "refresh-1", -time.Minute)

	token, err := NewFileSession(path).AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken() error = %v", err)
	}
	if token != "access-2" {
		t.Fatalf("AccessToken() = %q, want the renewed token", token)
	}
	if refreshes != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshes)
	}

	stored, err := authfile.Load(path)
	if err != nil {
		t.Fatalf("load credential: %v", err)
	}
	if stored.AccessToken != "access-2" || stored.RefreshToken != "refresh-2" {
		t.Fatalf("stored credential = %+v, want the rotated pair", stored)
	}
}

func TestFileSessionKeepsAValidAccessTokenWithoutRefreshing(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	refreshes := 0
	srv := sessionServer(t, &refreshes)
	defer srv.Close()
	path := writeCredential(t, srv.URL, "access-1", "refresh-1", time.Hour)

	token, err := NewFileSession(path).AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken() error = %v", err)
	}
	if token != "access-1" || refreshes != 0 {
		t.Fatalf("token = %q after %d refreshes, want the stored token untouched", token, refreshes)
	}
}

// Renew covers the server rejecting a token the file still advertises (early
// revocation, clock skew): one forced refresh, not a login prompt.
func TestFileSessionRenewForcesRefreshOfARejectedToken(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	refreshes := 0
	srv := sessionServer(t, &refreshes)
	defer srv.Close()
	path := writeCredential(t, srv.URL, "access-1", "refresh-1", time.Hour)

	token, err := NewFileSession(path).Renew(context.Background(), "access-1")
	if err != nil {
		t.Fatalf("Renew() error = %v", err)
	}
	if token != "access-2" || refreshes != 1 {
		t.Fatalf("token = %q after %d refreshes, want one forced renewal", token, refreshes)
	}
}

func TestResolveAuthReportsAMissingCredential(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	err := d.resolveAuth(context.Background())
	if err == nil || !strings.Contains(err.Error(), "zen auth login") {
		t.Fatalf("resolveAuth() error = %v, want guidance to run zen auth login", err)
	}
}

// The daemon runs only on its own session. The person's auth.json in
// the same home is the CLI's, and the daemon never falls back to it.
func TestResolveAuthNeverReadsThePersonsSession(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	personPath, err := CredentialPath()
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := authfile.Save(personPath, authfile.Credential{
		Version: authfile.CurrentVersion, APIBaseURL: "http://127.0.0.1:1/api/v1",
		AccessToken: "access-person", AccessExpiresAt: &later,
		RefreshToken: "refresh-person", RefreshExpiresAt: &later,
	}); err != nil {
		t.Fatalf("save the person's session: %v", err)
	}
	if err := d.resolveAuth(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "the daemon has no session") {
		t.Fatalf("resolveAuth() with only auth.json = %v, want no session", err)
	}

	writeCredential(t, "http://127.0.0.1:1", "access-daemon", "refresh-daemon", time.Hour)
	if err := d.resolveAuth(context.Background()); err != nil {
		t.Fatalf("resolveAuth() with daemon-auth.json: %v", err)
	}
	token, err := d.client.bearer(context.Background())
	if err != nil || token != "access-daemon" {
		t.Fatalf("the daemon sends %q (%v), want its own session", token, err)
	}
}
