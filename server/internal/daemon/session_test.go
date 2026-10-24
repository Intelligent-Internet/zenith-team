package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/authfile"
)

func newTestFileSession(path, baseURL string) Session {
	return NewFileSession(path, NewClient(baseURL), slog.New(slog.DiscardHandler))
}

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

	token, err := newTestFileSession(path, srv.URL).AccessToken(context.Background())
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

	token, err := newTestFileSession(path, srv.URL).AccessToken(context.Background())
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

	token, err := newTestFileSession(path, srv.URL).Renew(context.Background(), "access-1")
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

// endingServer answers every refresh 401, as the server does once it ended
// the session, and keeps the headers of each refresh it saw.
func endingServer(t *testing.T) (*httptest.Server, func() []http.Header) {
	t.Helper()
	var mu sync.Mutex
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"session is invalid or expired","code":"session_invalid"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []http.Header {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
}

// A refresh the server refuses cannot succeed on retry, so the session signs
// out after one: no further refresh reaches the server however many loops
// ask, until a new session is stored and it resumes on its own.
func TestFileSessionSignsOutOnARefusedRefreshAndResumesOnANewSession(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	srv, refreshes := endingServer(t)
	path := writeCredential(t, srv.URL, "access-1", "refresh-1", -time.Minute)
	var logs bytes.Buffer
	client := NewClient(srv.URL)
	client.SetVersion("0.2.47")
	client.SetDaemonID("daemon-1")
	session := NewFileSession(path, client, slog.New(slog.NewJSONHandler(&logs, nil)))

	_, err := session.AccessToken(context.Background())
	if !errors.Is(err, ErrSignedOut) || !errors.Is(err, authfile.ErrSessionRejected) {
		t.Fatalf("AccessToken() error = %v, want ErrSignedOut over the rejection", err)
	}
	for range 3 {
		if _, err := session.AccessToken(context.Background()); !errors.Is(err, ErrSignedOut) {
			t.Fatalf("AccessToken() after sign-out = %v, want ErrSignedOut", err)
		}
		if _, err := session.Renew(context.Background(), "access-1"); !errors.Is(err, ErrSignedOut) {
			t.Fatalf("Renew() after sign-out = %v, want ErrSignedOut", err)
		}
	}
	seen := refreshes()
	if len(seen) != 1 || !session.SignedOut() {
		t.Fatalf(
			"refreshes = %d, signed out = %t; want one refresh, then signed out",
			len(seen),
			session.SignedOut(),
		)
	}
	// The refresh names the daemon in the server's request log.
	if seen[0].Get("X-Client-Platform") != "daemon" || seen[0].Get("X-Client-Version") != "0.2.47" ||
		seen[0].Get(daemonIDHeader) != "daemon-1" {
		t.Fatalf("refresh identity headers = %v", seen[0])
	}
	if got := strings.Count(logs.String(), "daemon signed out"); got != 1 {
		t.Fatalf("sign-out logged %d times, want once: %s", got, logs.String())
	}
	if strings.Contains(logs.String(), "refresh-1") || strings.Contains(logs.String(), "access-1") {
		t.Fatalf("daemon log carries a token: %s", logs.String())
	}

	writeCredential(t, srv.URL, "access-2", "refresh-2", time.Hour)
	if session.SignedOut() {
		t.Fatal("SignedOut() = true after a new session was stored")
	}
	token, err := session.AccessToken(context.Background())
	if err != nil || token != "access-2" || len(refreshes()) != 1 {
		t.Fatalf("AccessToken() = %q, %v after %d refreshes; want the new token", token, err, len(refreshes()))
	}
	if !strings.Contains(logs.String(), "daemon session replaced") {
		t.Fatalf("resume not logged: %s", logs.String())
	}
}

// A refresh token past its expiry is signed out without asking the server.
func TestFileSessionSignsOutOnAnExpiredRefreshTokenWithoutCallingTheServer(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	srv, refreshes := endingServer(t)
	path := writeCredential(t, srv.URL, "access-1", "refresh-1", -time.Minute)
	credential, err := authfile.Load(path)
	if err != nil {
		t.Fatalf("load credential: %v", err)
	}
	expired := time.Now().Add(-time.Minute)
	credential.RefreshExpiresAt = &expired
	if err := authfile.Save(path, credential); err != nil {
		t.Fatalf("save credential: %v", err)
	}
	session := newTestFileSession(path, srv.URL)

	_, err = session.AccessToken(context.Background())
	if !errors.Is(err, ErrSignedOut) || !errors.Is(err, authfile.ErrSessionExpired) || !session.SignedOut() {
		t.Fatalf("AccessToken() error = %v, signed out %t", err, session.SignedOut())
	}
	if len(refreshes()) != 0 {
		t.Fatalf("refreshes = %d, want none", len(refreshes()))
	}
}

// endedSession is a session the server already ended.
type endedSession struct{}

func (endedSession) AccessToken(context.Context) (string, error)   { return "", ErrSignedOut }
func (endedSession) Renew(context.Context, string) (string, error) { return "", ErrSignedOut }
func (endedSession) SignedOut() bool                               { return true }

// A signed-out daemon stays up and says so on /health, but its claim,
// heartbeat and wakeup loops send the server nothing, the wakeup WS above
// all: dialed without a token it can only be refused.
func TestSignedOutDaemonStopsCallingTheServerAndReportsIt(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	d := newTestDaemon(t, srv.URL)
	var logs bytes.Buffer
	d.logger = slog.New(slog.NewTextHandler(&logs, nil))
	d.companies["company-1"] = &companyState{companyID: "company-1", runtimeIDs: []string{"runtime-1"}}
	d.client.SetSession(endedSession{})

	if ids := d.claimableRuntimeIDs(); len(ids) != 0 {
		t.Fatalf("claimableRuntimeIDs() = %v, want none while signed out", ids)
	}
	if d.runHeartbeatTick(context.Background(), "runtime-1") {
		t.Fatal("runHeartbeatTick() counted a transient failure while signed out")
	}
	if _, err := d.runWakeupConnection(
		context.Background(), []string{"runtime-1"}, make(chan runWakeup, 1), make(chan struct{}),
	); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("runWakeupConnection() error = %v, want ErrSignedOut", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 0 {
		t.Fatalf("signed-out daemon called the server: %v", requests)
	}
	// The session logged the sign-out once; the loops add nothing per tick.
	if strings.Contains(logs.String(), "heartbeat failed") {
		t.Fatalf("heartbeat logged a failure per tick while signed out: %s", logs.String())
	}

	health, raw := readHealthBody(t, d)
	if !health.SignedOut || !strings.Contains(raw, `"signedOut":true`) {
		t.Fatalf("health = %s, want signedOut", raw)
	}
}
