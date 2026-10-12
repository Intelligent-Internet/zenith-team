package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// doctorChecks runs `zen doctor --output json` against the seeded session
// and indexes the report's checks by name. The daemon probe is pointed at a
// closed port so the test never reaches a daemon running on this machine.
func doctorChecks(t *testing.T) map[string]doctorCheck {
	t.Helper()
	t.Setenv("BOARDLY_HEALTH_PORT", "1")
	out, err := executeCLI(t, newRootCmd, "doctor", "--output", "json")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	var report doctorReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode doctor report %q: %v", out, err)
	}
	checks := make(map[string]doctorCheck, len(report.Checks))
	for _, check := range report.Checks {
		checks[check.Name] = check
	}
	return checks
}

// serveDoctorProbe answers the two requests doctor sends an operator's
// server: /healthz for reachability and /api/v1/me for the session.
func serveDoctorProbe(t *testing.T, me http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		case "/api/v1/me":
			me(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	seedSession(t, srv.URL)
	return srv
}

// CL-AC4: an unreachable server is a connectivity failure, never a
// credential one. The session row must not send the operator to sign in
// again when nothing answered.
func TestDoctorDoesNotBlameTheSessionWhenTheServerIsUnreachable(t *testing.T) {
	seedSession(t, "http://127.0.0.1:1")

	checks := doctorChecks(t)
	if checks["server"].Status != "fail" {
		t.Fatalf("server check = %+v, want fail for a closed port", checks["server"])
	}
	accepted, present := checks["session accepted"]
	if !present || accepted.Status != "skip" {
		t.Fatalf("session accepted = %+v, want skip when the server is unreachable", accepted)
	}
	if strings.Contains(accepted.Action, "zen auth login") {
		t.Fatalf("session accepted action %q sends the operator to sign in for a network problem",
			accepted.Action)
	}
}

// The reachability probe can pass while the session request itself fails
// before the server answers (the connection drops mid-request). That is
// still unknown, not rejected.
func TestDoctorSkipsTheSessionWhenTheSessionRequestNeverGetsAnAnswer(t *testing.T) {
	serveDoctorProbe(t, func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		_ = conn.Close()
	})

	accepted := doctorChecks(t)["session accepted"]
	if accepted.Status != "skip" || strings.Contains(accepted.Action, "zen auth login") {
		t.Fatalf("session accepted = %+v, want skip without the sign-in action", accepted)
	}
}

func TestDoctorReportsARejectedSessionWithTheSignInAction(t *testing.T) {
	serveDoctorProbe(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	checks := doctorChecks(t)
	if checks["server"].Status != "ok" {
		t.Fatalf("server check = %+v, want ok", checks["server"])
	}
	accepted := checks["session accepted"]
	if accepted.Status != "fail" || !strings.Contains(accepted.Action, "zen auth login") {
		t.Fatalf("session accepted = %+v, want fail with the sign-in action", accepted)
	}
}

func TestDoctorReportsAnAcceptedSession(t *testing.T) {
	serveDoctorProbe(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"email": "user@example.com"})
	})

	accepted := doctorChecks(t)["session accepted"]
	if accepted.Status != "ok" || !strings.Contains(accepted.Detail, "user@example.com") {
		t.Fatalf("session accepted = %+v, want ok naming the account", accepted)
	}
}

// GET /me answers 200 or 401 by contract (docs/api/spec/authentication.yaml).
// Any other answer — a front door's 502, the server's own 503 — is not a
// session verdict, so the row stays unknown and names the status instead of
// sending the operator to sign in again.
func TestDoctorSkipsTheSessionWhenTheServerAnswersWithoutAVerdict(t *testing.T) {
	serveDoctorProbe(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	accepted := doctorChecks(t)["session accepted"]
	if accepted.Status != "skip" || strings.Contains(accepted.Action, "zen auth login") {
		t.Fatalf("session accepted = %+v, want skip without the sign-in action", accepted)
	}
	if !strings.Contains(accepted.Detail, "503") {
		t.Fatalf("session accepted detail %q does not name the status the server answered with",
			accepted.Detail)
	}
}
