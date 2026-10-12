package authfile

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// D31: the daemon's own session is minted with the person's bearer and
// comes back for the same API URL; a 401 is ErrSessionRejected, so the
// caller replaces the file instead of retrying it.
func TestMintDaemonSessionUsesThePersonsBearer(t *testing.T) {
	t.Parallel()
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/auth/daemon-session" {
			http.NotFound(w, r)
			return
		}
		authorization = r.Header.Get("Authorization")
		if authorization == "Bearer person-ended" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).
				Encode(map[string]string{"code": "session_invalid", "detail": "session is invalid"})
			return
		}
		expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client": "daemon",
			"session": map[string]string{
				"transport": "bearer", "accessToken": "daemon-access", "refreshToken": "daemon-refresh",
				"accessExpiresAt": expires, "refreshExpiresAt": expires,
			},
		})
	}))
	defer server.Close()

	person := Credential{
		APIBaseURL:   server.URL + "/api/v1",
		AccessToken:  "person-access",
		RefreshToken: "person-refresh",
	}
	minted, err := MintDaemonSession(context.Background(), server.Client(), person)
	if err != nil {
		t.Fatalf("MintDaemonSession() error = %v", err)
	}
	if authorization != "Bearer person-access" || minted.AccessToken != "daemon-access" ||
		minted.RefreshToken != "daemon-refresh" || minted.APIBaseURL != person.APIBaseURL ||
		minted.Version != CurrentVersion {
		t.Fatalf("minted %+v with %q; want the daemon pair for the person's server", minted, authorization)
	}

	person.AccessToken = "person-ended"
	_, err = MintDaemonSession(context.Background(), server.Client(), person)
	if !errors.Is(err, ErrSessionRejected) {
		t.Fatalf("MintDaemonSession(ended) error = %v, want ErrSessionRejected", err)
	}
}

// Logout sends the stored refresh token on the desktop transport; the
// server's refusal is an error.
func TestLogoutSendsTheRefreshToken(t *testing.T) {
	t.Parallel()
	var body map[string]string
	status := http.StatusNoContent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(status)
	}))
	defer server.Close()

	credential := Credential{APIBaseURL: server.URL + "/api/v1", AccessToken: "access", RefreshToken: "refresh"}
	if err := Logout(context.Background(), server.Client(), credential); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if body["client"] != "desktop" || body["refreshToken"] != "refresh" {
		t.Fatalf("logout body = %v, want the desktop transport and the refresh token", body)
	}
	status = http.StatusInternalServerError
	if err := Logout(context.Background(), server.Client(), credential); err == nil {
		t.Fatal("Logout() accepted a server error")
	}
}

// Each credential file has its own lock, named after it: auth.json keeps
// auth.lock, so a daemon renewing daemon-auth.json never waits on the CLI.
func TestWithLockLocksEachCredentialFileApart(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	cliPath := filepath.Join(directory, "auth.json")
	daemonPath := filepath.Join(directory, "daemon-auth.json")
	err := WithLock(context.Background(), cliPath, func() error {
		if _, err := os.Stat(filepath.Join(directory, "auth.lock")); err != nil {
			t.Errorf("auth.json lock: %v", err)
		}
		return WithLock(context.Background(), daemonPath, func() error {
			if _, err := os.Stat(filepath.Join(directory, "daemon-auth.lock")); err != nil {
				t.Errorf("daemon-auth.json lock: %v", err)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("nested locks of two files: %v", err)
	}
}
