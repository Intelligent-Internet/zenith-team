package authfile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadValidCredential(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "auth.json")
	writeCredentialFixture(t, path, `{
  "version": 1,
  "apiBaseUrl": "https://api.example.com/api/v1",
  "accessToken": "access-token",
  "accessExpiresAt": "2026-07-21T11:00:00Z",
  "refreshToken": "refresh-token",
  "refreshExpiresAt": "2026-07-28T10:00:00Z"
}`)

	credential, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if credential.APIBaseURL != "https://api.example.com/api/v1" {
		t.Fatalf("APIBaseURL = %q", credential.APIBaseURL)
	}
	if credential.AccessToken != "access-token" || credential.RefreshToken != "refresh-token" {
		t.Fatal("Load() did not preserve tokens")
	}
}

func TestSaveRoundTrip(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "auth.json")
	credential := Credential{
		Version:          1,
		APIBaseURL:       "http://localhost:8788/api/v1",
		AccessToken:      "new-access-token",
		AccessExpiresAt:  timestamp(t, "2026-07-21T11:00:00.123456Z"),
		RefreshToken:     "new-refresh-token",
		RefreshExpiresAt: timestamp(t, "2026-07-28T10:00:00.654321Z"),
	}

	if err := Save(path, credential); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.AccessToken != credential.AccessToken || loaded.RefreshToken != credential.RefreshToken {
		t.Fatal("round trip changed tokens")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("auth file permissions = %04o", permissions)
	}
}

func TestWithLockSerializesOperations(t *testing.T) {
	t.Parallel()
	authPath := filepath.Join(t.TempDir(), "auth.json")
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)

	go func() {
		firstDone <- WithLock(context.Background(), authPath, func() error {
			entered <- struct{}{}
			<-release
			return nil
		})
	}()
	<-entered
	go func() {
		secondDone <- WithLock(context.Background(), authPath, func() error {
			entered <- struct{}{}
			return nil
		})
	}()

	select {
	case <-entered:
		t.Fatal("second operation entered before first released the lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first WithLock() error = %v", err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("second operation did not acquire released lock")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second WithLock() error = %v", err)
	}
}

func TestLoadRejectsCredentialWithoutRefreshToken(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "auth.json")
	writeCredentialFixture(t, path, `{
  "version": 1,
  "accessToken": "access-token",
  "refreshToken": null
}`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "refreshToken") {
		t.Fatalf("Load() error = %v, want refreshToken validation", err)
	}
}

func TestCredentialState(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		credential Credential
		want       State
	}{
		{
			name: "access token is ready",
			credential: Credential{
				AccessToken:      "access-token",
				AccessExpiresAt:  timestamp(t, "2026-07-21T11:00:00Z"),
				RefreshToken:     "refresh-token",
				RefreshExpiresAt: timestamp(t, "2026-07-28T10:00:00Z"),
			},
			want: StateAuthenticated,
		},
		{
			name: "access token needs refresh",
			credential: Credential{
				AccessToken:      "access-token",
				AccessExpiresAt:  timestamp(t, "2026-07-21T09:00:00Z"),
				RefreshToken:     "refresh-token",
				RefreshExpiresAt: timestamp(t, "2026-07-28T10:00:00Z"),
			},
			want: StateRefreshRequired,
		},
		{
			name: "refresh token expired",
			credential: Credential{
				AccessToken:      "access-token",
				AccessExpiresAt:  timestamp(t, "2026-07-21T09:00:00Z"),
				RefreshToken:     "refresh-token",
				RefreshExpiresAt: timestamp(t, "2026-07-21T10:00:00Z"),
			},
			want: StateExpired,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.credential.State(now); got != test.want {
				t.Fatalf("State() = %q, want %q", got, test.want)
			}
		})
	}
}

func writeCredentialFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write credential fixture: %v", err)
	}
}

func timestamp(t *testing.T, value string) *time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse timestamp: %v", err)
	}
	return &parsed
}
