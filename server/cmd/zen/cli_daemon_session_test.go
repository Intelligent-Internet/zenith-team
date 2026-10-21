package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/authfile"
	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon"
)

// fakeAccessToken is an unsigned token shaped like the server's, naming
// subject; only accessTokenSubject reads it.
func fakeAccessToken(subject, salt string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"` + subject + `","tokenUse":"access"}`))
	return "header." + payload + "." + salt
}

// daemonSessionServer answers the routes the daemon session helpers call
// and records them.
type daemonSessionServer struct {
	mu        sync.Mutex
	calls     []string
	refresh   int
	minted    int
	rejectOld bool
	server    *httptest.Server
}

func newDaemonSessionServer(t *testing.T) *daemonSessionServer {
	t.Helper()
	fake := &daemonSessionServer{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.calls = append(fake.calls, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		fake.mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/auth/daemon-session":
			fake.mu.Lock()
			fake.minted++
			minted := fake.minted
			fake.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			body := bearerSession(
				fakeAccessToken("person-1", "daemon"),
				"refresh-daemon-"+string(rune('0'+minted)),
				time.Hour,
			)
			body["client"] = "daemon"
			_ = json.NewEncoder(w).Encode(body)
		case "/api/v1/auth/refresh":
			var request map[string]string
			_ = json.NewDecoder(r.Body).Decode(&request)
			fake.mu.Lock()
			fake.refresh++
			reject := fake.rejectOld && request["refreshToken"] == "refresh-ended"
			fake.mu.Unlock()
			if reject {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).
					Encode(map[string]string{"code": "session_invalid", "detail": "session is invalid or expired"})
				return
			}
			_ = json.NewEncoder(w).
				Encode(bearerSession(fakeAccessToken("person-1", "renewed"), "refresh-renewed", time.Hour))
		case "/api/v1/auth/logout":
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/daemon/config":
			_ = json.NewEncoder(w).
				Encode(map[string]string{"studioApiOrigin": "https://studio.zenith-team.test"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *daemonSessionServer) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *daemonSessionServer) mints() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.minted
}

func saveSession(t *testing.T, path string, credential authfile.Credential) {
	t.Helper()
	credential.Version = authfile.CurrentVersion
	if err := authfile.Save(path, credential); err != nil {
		t.Fatalf("save %s: %v", path, err)
	}
}

func sessionPaths(t *testing.T) (string, string) {
	t.Helper()
	personPath, err := daemon.CredentialPath()
	if err != nil {
		t.Fatal(err)
	}
	daemonPath, err := daemon.OwnCredentialPath()
	if err != nil {
		t.Fatal(err)
	}
	return personPath, daemonPath
}

// `zen daemon start` gives the daemon its own session, minted once from
// the person's `zen auth login` with the person's bearer, and keeps it while
// the server accepts it. The person's auth.json is left as it was.
func TestDaemonStartMintsTheDaemonSessionFromThePersonsLogin(t *testing.T) {
	desktopShell(t)
	fake := newDaemonSessionServer(t)
	personPath, daemonPath := sessionPaths(t)
	personAccess := fakeAccessToken("person-1", "person")
	later := time.Now().Add(time.Hour)
	saveSession(t, personPath, authfile.Credential{
		APIBaseURL: fake.server.URL + "/api/v1", AccessToken: personAccess, AccessExpiresAt: &later,
		RefreshToken: "refresh-person", RefreshExpiresAt: &later,
	})

	for range 2 {
		if err := ensureDaemonSession(context.Background()); err != nil {
			t.Fatalf("ensureDaemonSession() error = %v", err)
		}
	}
	if fake.mints() != 1 {
		t.Fatalf("minted %d daemon sessions, want one; calls %v", fake.mints(), fake.recorded())
	}
	if calls := fake.recorded(); len(calls) != 1 ||
		calls[0] != "POST /api/v1/auth/daemon-session Bearer "+personAccess {
		t.Fatalf("calls = %v, want one mint with the person's bearer", calls)
	}
	minted, err := authfile.Load(daemonPath)
	if err != nil {
		t.Fatalf("load the daemon session: %v", err)
	}
	if minted.RefreshToken != "refresh-daemon-1" || minted.APIBaseURL != fake.server.URL+"/api/v1" {
		t.Fatalf("daemon session = %+v, want the minted one for this server", minted)
	}
	person, err := authfile.Load(personPath)
	if err != nil || person.AccessToken != personAccess || person.RefreshToken != "refresh-person" {
		t.Fatalf("the person's session = %+v, %v; want it unchanged", person, err)
	}
}

// A daemon session the server ended is replaced, not retried forever; with
// no person's session to mint from, the start says how to sign in.
func TestDaemonStartReplacesADaemonSessionTheServerEnded(t *testing.T) {
	desktopShell(t)
	fake := newDaemonSessionServer(t)
	fake.rejectOld = true
	personPath, daemonPath := sessionPaths(t)
	earlier := time.Now().Add(-time.Minute)
	later := time.Now().Add(time.Hour)
	saveSession(t, daemonPath, authfile.Credential{
		APIBaseURL: fake.server.URL + "/api/v1", AccessToken: fakeAccessToken("person-1", "old"),
		AccessExpiresAt: &earlier, RefreshToken: "refresh-ended", RefreshExpiresAt: &later,
	})

	err := ensureDaemonSession(context.Background())
	if err == nil || !strings.Contains(err.Error(), "zen auth login") {
		t.Fatalf("ensureDaemonSession() without a login = %v, want guidance to sign in", err)
	}
	if _, err := authfile.Load(daemonPath); !errors.Is(err, authfile.ErrNotFound) {
		t.Fatalf("the ended daemon session is still stored: %v", err)
	}

	saveSession(t, personPath, authfile.Credential{
		APIBaseURL: fake.server.URL + "/api/v1", AccessToken: fakeAccessToken("person-1", "person"),
		AccessExpiresAt: &later, RefreshToken: "refresh-person", RefreshExpiresAt: &later,
	})
	saveSession(t, daemonPath, authfile.Credential{
		APIBaseURL: fake.server.URL + "/api/v1", AccessToken: fakeAccessToken("person-1", "old"),
		AccessExpiresAt: &earlier, RefreshToken: "refresh-ended", RefreshExpiresAt: &later,
	})
	if err := ensureDaemonSession(context.Background()); err != nil {
		t.Fatalf("ensureDaemonSession() error = %v", err)
	}
	replaced, err := authfile.Load(daemonPath)
	if err != nil || replaced.RefreshToken != "refresh-daemon-1" {
		t.Fatalf("daemon session = %+v, %v; want a newly minted one", replaced, err)
	}
}

// An ended daemon session is deleted only while the file still holds it: a
// session the desktop app installed after the start read the file stays.
func TestDaemonStartKeepsADaemonSessionInstalledMeanwhile(t *testing.T) {
	desktopShell(t)
	fake := newDaemonSessionServer(t)
	_, daemonPath := sessionPaths(t)
	later := time.Now().Add(time.Hour)
	saveSession(t, daemonPath, authfile.Credential{
		APIBaseURL: fake.server.URL + "/api/v1", AccessToken: fakeAccessToken("person-1", "installed"),
		AccessExpiresAt: &later, RefreshToken: "refresh-installed", RefreshExpiresAt: &later,
	})

	removed, err := removeEndedDaemonSession(context.Background(), daemonPath, "refresh-ended")
	if err != nil || removed {
		t.Fatalf("removeEndedDaemonSession() = %v, %v; want the newer session kept", removed, err)
	}
	if kept, err := authfile.Load(daemonPath); err != nil || kept.RefreshToken != "refresh-installed" {
		t.Fatalf("daemon session = %+v, %v; want the installed one", kept, err)
	}
	removed, err = removeEndedDaemonSession(context.Background(), daemonPath, "refresh-installed")
	if err != nil || !removed {
		t.Fatalf("removeEndedDaemonSession() = %v, %v; want the ended session deleted", removed, err)
	}
	if _, err := authfile.Load(daemonPath); !errors.Is(err, authfile.ErrNotFound) {
		t.Fatalf("the ended daemon session is still stored: %v", err)
	}
}

// The restart's session check reads the daemon plane with the daemon's own
// session; GET /me would refuse it, because a daemon session is never its
// person.
func TestDaemonRestartChecksTheDaemonSessionOnTheDaemonPlane(t *testing.T) {
	desktopShell(t)
	fake := newDaemonSessionServer(t)
	_, daemonPath := sessionPaths(t)
	later := time.Now().Add(time.Hour)
	daemonAccess := fakeAccessToken("person-1", "daemon")
	saveSession(t, daemonPath, authfile.Credential{
		APIBaseURL: fake.server.URL + "/api/v1", AccessToken: daemonAccess, AccessExpiresAt: &later,
		RefreshToken: "refresh-daemon", RefreshExpiresAt: &later,
	})
	if err := verifyDaemonSession(context.Background()); err != nil {
		t.Fatalf("verifyDaemonSession() error = %v", err)
	}
	if calls := fake.recorded(); len(calls) != 1 || calls[0] != "GET /api/v1/daemon/config Bearer "+daemonAccess {
		t.Fatalf("calls = %v, want one daemon-plane read with the daemon's session", calls)
	}
}

// Signing out of the CLI ends the daemon's session too, so a running daemon
// stops acting for the person; signing in as someone else ends the old
// person's daemon session, and signing in again as the same person keeps it.
func TestSignOutAndAnotherPersonsSignInEndTheDaemonSession(t *testing.T) {
	desktopShell(t)
	fake := newDaemonSessionServer(t)
	personPath, daemonPath := sessionPaths(t)
	later := time.Now().Add(time.Hour)
	seed := func() {
		saveSession(t, personPath, authfile.Credential{
			APIBaseURL: fake.server.URL + "/api/v1", AccessToken: fakeAccessToken("person-1", "person"),
			AccessExpiresAt: &later, RefreshToken: "refresh-person", RefreshExpiresAt: &later,
		})
		saveSession(t, daemonPath, authfile.Credential{
			APIBaseURL: fake.server.URL + "/api/v1", AccessToken: fakeAccessToken("person-1", "daemon"),
			AccessExpiresAt: &later, RefreshToken: "refresh-daemon", RefreshExpiresAt: &later,
		})
	}

	seed()
	if _, err := executeCLI(t, newRootCmd, "auth", "logout"); err != nil {
		t.Fatalf("auth logout: %v", err)
	}
	for _, path := range []string{personPath, daemonPath} {
		if _, err := authfile.Load(path); !errors.Is(err, authfile.ErrNotFound) {
			t.Fatalf("%s after logout: %v, want none", path, err)
		}
	}

	seed()
	same := authfile.Credential{
		Version: authfile.CurrentVersion, APIBaseURL: fake.server.URL + "/api/v1",
		AccessToken: fakeAccessToken("person-1", "again"), RefreshToken: "refresh-again",
	}
	forgetDaemonSessionOfAnotherPerson(context.Background(), &strings.Builder{}, same)
	if _, err := authfile.Load(daemonPath); err != nil {
		t.Fatalf("the same person's daemon session after sign-in: %v, want it kept", err)
	}
	other := same
	other.AccessToken = fakeAccessToken("person-2", "other")
	forgetDaemonSessionOfAnotherPerson(context.Background(), &strings.Builder{}, other)
	if _, err := authfile.Load(daemonPath); !errors.Is(err, authfile.ErrNotFound) {
		t.Fatalf("another person's daemon session after sign-in: %v, want none", err)
	}
	if accessTokenSubject("not-a-token") != "" {
		t.Fatal("a malformed token names a person")
	}
}
