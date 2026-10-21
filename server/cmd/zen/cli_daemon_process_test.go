package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon"
)

// fakeDaemon is a scriptable stand-in for the daemon's health port. A nil
// health answers 503, which is what a real daemon answers in the short window
// where its health server has closed admission but the process has not exited
// yet. A silent or 503 health port is never evidence that the process is
// gone — only processAlive is, which is why the seams below script the two
// independently.
type fakeDaemon struct {
	mu       sync.Mutex
	health   *daemon.HealthResponse
	onHealth func()
}

func newFakeDaemon(t *testing.T, health *daemon.HealthResponse) *fakeDaemon {
	t.Helper()
	fake := &fakeDaemon{health: health}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if fake.onHealth != nil {
			fake.onHealth()
		}
		if fake.health == nil {
			http.Error(w, "daemon is stopping", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(fake.health)
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	port := listener.Addr().(*net.TCPAddr).Port
	t.Setenv("ZENITH_TEAM_HEALTH_PORT", strconv.Itoa(port))
	return fake
}

// set replaces the answering daemon and ends any drain script in progress.
func (f *fakeDaemon) set(health *daemon.HealthResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.health = health
	f.onHealth = nil
}

func (f *fakeDaemon) answering() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.health != nil
}

// drainAfter makes the fake keep answering for polls more health requests,
// then go silent — a daemon finishing its shutdown after the signal.
func (f *fakeDaemon) drainAfter(polls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	remaining := polls
	f.onHealth = func() {
		remaining--
		if remaining < 0 {
			f.health = nil
		}
	}
}

func runningHealth(pid int, startedAt time.Time, cliVersion string, activeRuns int64) *daemon.HealthResponse {
	return &daemon.HealthResponse{
		Status:         "running",
		PID:            pid,
		StartedAt:      startedAt,
		CLIVersion:     cliVersion,
		ActiveRunCount: activeRuns,
	}
}

func writeProcessRecord(t *testing.T, home string, health *daemon.HealthResponse) {
	t.Helper()
	err := writeDaemonProcessRecord(home, daemonProcessRecord{
		SchemaVersion: 1,
		PID:           health.PID,
		StartedAt:     health.StartedAt,
	})
	if err != nil {
		t.Fatalf("write daemon process record: %v", err)
	}
}

// shortenDaemonWaits collapses every lifecycle wait so a command-level test
// finishes in milliseconds.
func shortenDaemonWaits(t *testing.T) {
	t.Helper()
	previousPoll, previousReady := daemonPollInterval, daemonReadyTimeout
	previousStop, previousRestart := daemonStopWait, daemonRestartWait
	t.Cleanup(func() {
		daemonPollInterval, daemonReadyTimeout = previousPoll, previousReady
		daemonStopWait, daemonRestartWait = previousStop, previousRestart
	})
	daemonPollInterval = time.Millisecond
	daemonReadyTimeout = 200 * time.Millisecond
	daemonStopWait = 30 * time.Millisecond
	daemonRestartWait = 60 * time.Millisecond
}

// processSeams records, in order, every signal and spawn a command performs
// and answers processAlive from its own liveness table — deliberately not
// from the fake health port, so a test can hold a process alive after its
// health port went silent, which is exactly what a draining daemon does.
type processSeams struct {
	mu     sync.Mutex
	events []string
	spawns [][]string
	// liveness is the recorded state per pid; exitIn counts the remaining
	// liveness checks a scripted exit still answers before the process is
	// gone.
	liveness map[int]bool
	exitIn   map[int]int
	// spawnedWhileOldAlive records whether any spawn happened while the
	// process being replaced was still alive.
	spawnedWhileOldAlive bool
}

func (s *processSeams) record(event string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *processSeams) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

// pollAlive answers the processAlive seam and advances a scripted exit.
func (s *processSeams) pollAlive(pid int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if remaining, scheduled := s.exitIn[pid]; scheduled {
		if remaining <= 0 {
			delete(s.exitIn, pid)
			s.liveness[pid] = false
		} else {
			s.exitIn[pid] = remaining - 1
		}
	}
	return s.liveness[pid]
}

// alive reads the recorded liveness without advancing a scripted exit.
func (s *processSeams) alive(pid int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.liveness[pid]
}

func (s *processSeams) setAlive(pid int, alive bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.liveness[pid] = alive
	delete(s.exitIn, pid)
}

// exitAfter keeps pid alive for checks more liveness checks and reports it
// gone from the next one — a process that finishes its drain some time after
// its health port stopped answering.
func (s *processSeams) exitAfter(pid, checks int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.liveness[pid] = true
	s.exitIn[pid] = checks
}

func stubProcessSeams(t *testing.T, fake *fakeDaemon, oldPID, newPID int) *processSeams {
	t.Helper()
	previousSignal, previousAlive, previousSpawn := signalDaemonProcess, processAlive, spawnDaemonProcess
	t.Cleanup(func() {
		signalDaemonProcess, processAlive, spawnDaemonProcess = previousSignal, previousAlive, previousSpawn
	})
	seams := &processSeams{
		liveness: map[int]bool{oldPID: true},
		exitIn:   map[int]int{},
	}
	signalDaemonProcess = func(pid int) error {
		seams.record("signal " + strconv.Itoa(pid))
		return nil
	}
	processAlive = seams.pollAlive
	spawnDaemonProcess = func(binary string, args []string, _, _ *os.File) (int, error) {
		seams.record("spawn")
		seams.mu.Lock()
		seams.spawns = append(seams.spawns, append([]string{binary}, args...))
		seams.spawnedWhileOldAlive = seams.spawnedWhileOldAlive || seams.liveness[oldPID]
		seams.mu.Unlock()
		if fake.answering() {
			t.Errorf("spawned the replacement while the old daemon still answered on the health port")
		}
		seams.setAlive(newPID, true)
		fake.set(runningHealth(newPID, time.Now().UTC(), "0.9.3", 0))
		return newPID, nil
	}
	return seams
}

// sessionAPI starts the daemon's own session from the seeded login
// and serves the session check restart performs on the daemon plane,
// recording that the check happened before anything else.
func sessionAPI(t *testing.T, seams *processSeams, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/daemon-session":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(bearerSession("access-daemon", "refresh-daemon", time.Hour))
		case "/api/v1/daemon/config":
			seams.record("session check")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"studioApiOrigin":"https://studio.zenith-team.test"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDaemonRestartStartsTheReplacementOnlyAfterTheOldProcessExits(t *testing.T) {
	shortenDaemonWaits(t)
	oldPID, newPID := os.Getpid(), os.Getpid()+1
	startedAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	fake := newFakeDaemon(t, runningHealth(oldPID, startedAt, "0.9.2", 1))
	seams := stubProcessSeams(t, fake, oldPID, newPID)
	seedSession(t, sessionAPI(t, seams, http.StatusOK).URL)
	home := os.Getenv("ZENITH_TEAM_HOME")
	writeProcessRecord(t, home, runningHealth(oldPID, startedAt, "0.9.2", 1))
	// The health port goes silent three polls in, while the process keeps
	// draining for another six liveness checks: only the exit may release
	// the replacement.
	fake.drainAfter(3)
	seams.exitAfter(oldPID, 6)

	out, err := executeCLI(t, newRootCmd, "daemon", "restart")
	if err != nil {
		t.Fatalf("daemon restart: %v\n%s", err, out)
	}
	if seams.spawnedWhileOldAlive {
		t.Fatal("spawned the replacement while the old daemon process was still alive")
	}
	want := []string{"session check", "signal " + strconv.Itoa(oldPID), "spawn"}
	if got := seams.recorded(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for _, fragment := range []string{
		"version 0.9.2", "waiting for the daemon to exit (1 run(s) in flight)",
		"daemon ready (pid " + strconv.Itoa(newPID) + ", version 0.9.3)",
	} {
		if !strings.Contains(out, fragment) {
			t.Fatalf("restart output %q is missing %q", out, fragment)
		}
	}
	record, err := readDaemonProcessRecord(home)
	if err != nil {
		t.Fatalf("read daemon process record: %v", err)
	}
	if record.PID != newPID {
		t.Fatalf("process record pid = %d, want the replacement %d", record.PID, newPID)
	}
	if got := seams.spawns[0][1:4]; strings.Join(got, " ") != "daemon start --foreground" {
		t.Fatalf("spawned args = %v, want a foreground daemon start", seams.spawns[0])
	}
}

// A session that cannot sign in must be found out before the running daemon
// is stopped; otherwise restart trades a working daemon for none at all.
func TestDaemonRestartRefusesToStopWhenTheReplacementCannotSignIn(t *testing.T) {
	cases := []struct {
		name    string
		session func(t *testing.T, seams *processSeams)
		want    string
	}{
		{
			name: "no session",
			session: func(t *testing.T, _ *processSeams) {
				t.Helper()
				t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
			},
			want: "not authenticated",
		},
		{
			name: "rejected session",
			session: func(t *testing.T, seams *processSeams) {
				t.Helper()
				seedSession(t, sessionAPI(t, seams, http.StatusUnauthorized).URL)
			},
			want: "rejected the daemon session",
		},
		{
			name: "unreachable server",
			session: func(t *testing.T, _ *processSeams) {
				t.Helper()
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatalf("listen: %v", err)
				}
				deadURL := "http://" + listener.Addr().String()
				listener.Close()
				seedSession(t, deadURL)
			},
			want: "cannot reach the Zenith Team server",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shortenDaemonWaits(t)
			oldPID := os.Getpid()
			fake := newFakeDaemon(t, runningHealth(oldPID, time.Now().UTC(), "0.9.2", 0))
			seams := stubProcessSeams(t, fake, oldPID, oldPID+1)
			tc.session(t, seams)

			_, err := executeCLI(t, newRootCmd, "daemon", "restart")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("daemon restart error = %v, want %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "left untouched") {
				t.Fatalf("daemon restart error = %v, want it to say the daemon was left untouched", err)
			}
			if events := seams.recorded(); len(events) > 0 && events[0] != "session check" {
				t.Fatalf("events = %v, want no signal or spawn", events)
			}
			for _, event := range seams.recorded() {
				if event != "session check" {
					t.Fatalf("events = %v, want the running daemon untouched", seams.recorded())
				}
			}
		})
	}
}

func TestDaemonRestartReportsADaemonStillDrainingWithRunsInFlight(t *testing.T) {
	shortenDaemonWaits(t)
	oldPID := os.Getpid()
	fake := newFakeDaemon(t, runningHealth(oldPID, time.Now().UTC(), "0.9.2", 2))
	seams := stubProcessSeams(t, fake, oldPID, oldPID+1)
	seedSession(t, sessionAPI(t, seams, http.StatusOK).URL)

	out, err := executeCLI(t, newRootCmd, "daemon", "restart")
	if err == nil || !strings.Contains(err.Error(), "still stopping") ||
		!strings.Contains(err.Error(), "0.9.2") {
		t.Fatalf("daemon restart error = %v, want the drain explained with the old version", err)
	}
	if !strings.Contains(out, "2 run(s) in flight") {
		t.Fatalf("restart output %q does not narrate the in-flight runs", out)
	}
	for _, event := range seams.recorded() {
		if event == "spawn" {
			t.Fatal("spawned a replacement while the old daemon was still draining")
		}
	}
	if !seams.alive(oldPID) {
		t.Fatal("the draining daemon was reported gone while its process was alive")
	}
}

// The same drain, seen through a health port that has already stopped
// answering: a silent port is not an exit, so restart must still refuse to
// spawn and must say the daemon keeps running its old version.
func TestDaemonRestartWaitsWhenTheHealthPortGoesSilentBeforeTheProcessExits(t *testing.T) {
	shortenDaemonWaits(t)
	oldPID := os.Getpid()
	fake := newFakeDaemon(t, runningHealth(oldPID, time.Now().UTC(), "0.9.2", 2))
	seams := stubProcessSeams(t, fake, oldPID, oldPID+1)
	seedSession(t, sessionAPI(t, seams, http.StatusOK).URL)
	// Restart reads /health, then stopDaemonProcess reads it again; from the
	// wait on it answers nothing while the process drains.
	fake.drainAfter(2)

	out, err := executeCLI(t, newRootCmd, "daemon", "restart")
	if err == nil || !strings.Contains(err.Error(), "still stopping") ||
		!strings.Contains(err.Error(), "0.9.2") {
		t.Fatalf("daemon restart error = %v, want the drain explained with the old version", err)
	}
	if !strings.Contains(out, "waiting for the daemon to exit") {
		t.Fatalf("restart output %q does not narrate the wait", out)
	}
	for _, event := range seams.recorded() {
		if event == "spawn" {
			t.Fatal("spawned a replacement while the old daemon process was still alive")
		}
	}
	if !seams.alive(oldPID) {
		t.Fatal("the test never held the old process alive")
	}
}

// `daemon stop` reports the exit it can prove. The process is the only
// evidence: a health port that has gone silent while the process drains is a
// daemon that is still stopping, and saying otherwise is what lets a restart
// spawn a replacement into a port the old process still holds.
func TestDaemonStopWaitsForTheExitAndSaysSo(t *testing.T) {
	oldPID := os.Getpid()
	cases := []struct {
		name   string
		script func(fake *fakeDaemon, seams *processSeams)
		want   string
		avoids string
	}{
		{
			name: "exits",
			script: func(fake *fakeDaemon, seams *processSeams) {
				fake.drainAfter(2)
				seams.exitAfter(oldPID, 4)
			},
			want:   "daemon stopped",
			avoids: "still stopping",
		},
		{
			name:   "keeps draining",
			script: func(*fakeDaemon, *processSeams) {},
			want:   "still stopping",
			avoids: "daemon stopped",
		},
		{
			name: "health port silent while the process drains",
			script: func(fake *fakeDaemon, _ *processSeams) {
				// One answer for stopDaemonProcess, then nothing.
				fake.drainAfter(1)
			},
			want:   "still stopping",
			avoids: "daemon stopped",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shortenDaemonWaits(t)
			t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
			fake := newFakeDaemon(t, runningHealth(oldPID, time.Now().UTC(), "0.9.2", 1))
			seams := stubProcessSeams(t, fake, oldPID, oldPID+1)
			tc.script(fake, seams)

			out, err := executeCLI(t, newRootCmd, "daemon", "stop")
			if err != nil {
				t.Fatalf("daemon stop: %v", err)
			}
			if !strings.Contains(out, tc.want) || strings.Contains(out, tc.avoids) {
				t.Fatalf("stop output = %q, want %q and not %q", out, tc.want, tc.avoids)
			}
			if got := seams.recorded(); len(got) != 1 || got[0] != "signal "+strconv.Itoa(oldPID) {
				t.Fatalf("events = %v, want exactly one signal", got)
			}
		})
	}
}

// The wait narrates what it can see: the in-flight count while /health still
// answers, a plain line once it does not, and it gives up at its deadline
// with the process still alive rather than claiming an exit.
func TestWaitForDaemonExitHonoursItsDeadlineAndNarratesTheDrain(t *testing.T) {
	shortenDaemonWaits(t)
	pid := os.Getpid()
	fake := newFakeDaemon(t, runningHealth(pid, time.Now().UTC(), "0.9.2", 3))
	seams := stubProcessSeams(t, fake, pid, pid+1)

	var answering bytes.Buffer
	started := time.Now()
	exited, err := waitForDaemonExit(context.Background(), &answering, pid, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForDaemonExit: %v", err)
	}
	if exited {
		t.Fatal("waitForDaemonExit reported an exit while the process was alive")
	}
	if waited := time.Since(started); waited < 20*time.Millisecond {
		t.Fatalf("waitForDaemonExit returned after %s, before its 20ms deadline", waited)
	} else if waited > 5*time.Second {
		t.Fatalf("waitForDaemonExit overran its 20ms deadline by %s", waited)
	}
	if !strings.Contains(answering.String(), "waiting for the daemon to exit (3 run(s) in flight)") {
		t.Fatalf("wait output %q does not narrate the in-flight runs", answering.String())
	}

	// The health port stops answering; the process is still the evidence.
	fake.set(nil)
	var silent bytes.Buffer
	exited, err = waitForDaemonExit(context.Background(), &silent, pid, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForDaemonExit: %v", err)
	}
	if exited {
		t.Fatal("a silent health port was taken as proof that the process exited")
	}
	if !strings.Contains(silent.String(), "waiting for the daemon to exit\n") {
		t.Fatalf("wait output %q lacks the plain waiting line", silent.String())
	}
	if strings.Contains(silent.String(), "in flight") {
		t.Fatalf("wait output %q invented a run count with no health to read", silent.String())
	}

	seams.setAlive(pid, false)
	var gone bytes.Buffer
	started = time.Now()
	exited, err = waitForDaemonExit(context.Background(), &gone, pid, 10*time.Second)
	if err != nil {
		t.Fatalf("waitForDaemonExit: %v", err)
	}
	if !exited {
		t.Fatal("waitForDaemonExit did not report the exit of a dead process")
	}
	if waited := time.Since(started); waited > 5*time.Second {
		t.Fatalf("waitForDaemonExit took %s to notice a dead process", waited)
	}
}

func TestDaemonStartExplainsWhenTheRunningDaemonIsAnotherVersion(t *testing.T) {
	cases := []struct {
		name     string
		running  string
		wantHint bool
	}{
		{name: "older daemon", running: "0.9.2", wantHint: true},
		{name: "same version", running: version, wantHint: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shortenDaemonWaits(t)
			oldPID := os.Getpid()
			fake := newFakeDaemon(t, runningHealth(oldPID, time.Now().UTC(), tc.running, 0))
			seams := stubProcessSeams(t, fake, oldPID, oldPID+1)
			seedSession(t, sessionAPI(t, seams, http.StatusOK).URL)

			out, err := executeCLI(t, newRootCmd, "daemon", "start")
			if err != nil {
				t.Fatalf("daemon start: %v", err)
			}
			if !strings.Contains(out, "already running") || !strings.Contains(out, "version "+tc.running) {
				t.Fatalf("start output = %q, want the running pid and version", out)
			}
			if hinted := strings.Contains(out, "zen daemon restart"); hinted != tc.wantHint {
				t.Fatalf("start output = %q, restart hint = %v, want %v", out, hinted, tc.wantHint)
			}
			if events := seams.recorded(); len(events) != 0 {
				t.Fatalf("events = %v, want the running daemon untouched", events)
			}
		})
	}
}

func TestDaemonStatusShowsVersionAndAPendingRestart(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	health := runningHealth(os.Getpid(), time.Now().UTC(), "0.9.2", 0)
	health.ReloadPendingReason = "zenith-team binary on disk reports 0.9.3, running 0.9.2"
	fake := newFakeDaemon(t, health)

	out, err := executeCLI(t, newRootCmd, "daemon", "status")
	if err != nil {
		t.Fatalf("daemon status: %v", err)
	}
	for _, fragment := range []string{"version:", "0.9.2", "restart pending:", "0.9.3"} {
		if !strings.Contains(out, fragment) {
			t.Fatalf("status output %q is missing %q", out, fragment)
		}
	}

	health.ReloadPendingReason = ""
	fake.set(health)
	out, err = executeCLI(t, newRootCmd, "daemon", "status")
	if err != nil {
		t.Fatalf("daemon status: %v", err)
	}
	if strings.Contains(out, "restart pending") {
		t.Fatalf("status output %q shows a restart row with nothing pending", out)
	}
}

// /health reports the companies the daemon registered runtimes in and the
// providers it skipped; the table form has to show both, and in a stable
// order — the daemon ranges over maps to build them.
func TestDaemonStatusListsCompaniesAndSkippedProviders(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	health := runningHealth(os.Getpid(), time.Now().UTC(), "0.9.2", 0)
	health.Agents = []string{"codex", "claude"}
	health.SkippedAgents = map[string]string{
		"codex": "codex version 0.90.0 is below minimum required 0.100.0 — please upgrade",
	}
	health.Companies = []daemon.HealthCompany{
		{ID: "0198aa00-0000-7000-8000-0000000000c1", Runtimes: []string{"r1", "r2"}},
		{ID: "0198aa00-0000-7000-8000-0000000000c0", Runtimes: []string{"r3"}},
	}
	fake := newFakeDaemon(t, health)

	out, err := executeCLI(t, newRootCmd, "daemon", "status")
	if err != nil {
		t.Fatalf("daemon status: %v", err)
	}
	for _, fragment := range []string{
		"agents:", "claude, codex",
		"companies:",
		"0198aa00-0000-7000-8000-0000000000c0 (1 runtime)",
		"0198aa00-0000-7000-8000-0000000000c1 (2 runtimes)",
		"skipped agents:", "codex: codex version 0.90.0",
	} {
		if !strings.Contains(out, fragment) {
			t.Fatalf("status output %q is missing %q", out, fragment)
		}
	}
	if strings.Index(out, "0000000000c0 (1 runtime)") > strings.Index(out, "0000000000c1 (2 runtimes)") {
		t.Fatalf("status output %q lists companies out of id order", out)
	}

	health.SkippedAgents = nil
	health.Companies = nil
	fake.set(health)
	out, err = executeCLI(t, newRootCmd, "daemon", "status")
	if err != nil {
		t.Fatalf("daemon status: %v", err)
	}
	if !strings.Contains(out, "companies:") || !strings.Contains(out, "none") {
		t.Fatalf("status output %q should say the daemon holds no companies", out)
	}
	if strings.Contains(out, "skipped agents") {
		t.Fatalf("status output %q shows a skipped agents row with nothing skipped", out)
	}
}

// The provider the daemon registered as unusable is
// listed by `daemon status` with the same sentence the roster shows — the
// daemon records the registered message under /health.skippedAgents, and
// the table renders that map verbatim.
func TestDaemonStatusListsSkippedProvidersWithTheirReason(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	health := runningHealth(os.Getpid(), time.Now().UTC(), "0.9.2", 0)
	health.Agents = []string{"claude", "codex"}
	health.SkippedAgents = map[string]string{
		"codex": "Codex CLI 0.90.0 is below the minimum 0.100.0",
	}
	fake := newFakeDaemon(t, health)

	out, err := executeCLI(t, newRootCmd, "daemon", "status")
	if err != nil {
		t.Fatalf("daemon status: %v", err)
	}
	if !strings.Contains(out, "skipped agents:") ||
		!strings.Contains(out, "codex: Codex CLI 0.90.0 is below the minimum 0.100.0") {
		t.Fatalf("status output %q does not list codex with its unusable reason", out)
	}

	health.SkippedAgents = map[string]string{}
	fake.set(health)
	out, err = executeCLI(t, newRootCmd, "daemon", "status")
	if err != nil {
		t.Fatalf("daemon status: %v", err)
	}
	if strings.Contains(out, "skipped agents") {
		t.Fatalf("status output %q shows a skipped agents row after the CLI was fixed", out)
	}
}

func TestFormatHealthCompaniesAndSkippedAgents(t *testing.T) {
	companyTests := []struct {
		name      string
		companies []daemon.HealthCompany
		want      string
	}{
		{name: "none", companies: nil, want: "none"},
		{
			name:      "singular",
			companies: []daemon.HealthCompany{{ID: "c1", Runtimes: []string{"r1"}}},
			want:      "c1 (1 runtime)",
		},
		{
			name: "sorted by id with plural counts",
			companies: []daemon.HealthCompany{
				{ID: "c2", Runtimes: []string{"r1", "r2"}},
				{ID: "c1", Runtimes: nil},
			},
			want: "c1 (0 runtimes), c2 (2 runtimes)",
		},
	}
	for _, test := range companyTests {
		t.Run("companies "+test.name, func(t *testing.T) {
			if got := formatHealthCompanies(test.companies); got != test.want {
				t.Fatalf("formatHealthCompanies() = %q, want %q", got, test.want)
			}
		})
	}

	skippedTests := []struct {
		name    string
		skipped map[string]string
		want    string
	}{
		{name: "none", skipped: nil, want: ""},
		{name: "one", skipped: map[string]string{"codex": "too old"}, want: "codex: too old"},
		{
			name:    "sorted by provider",
			skipped: map[string]string{"codex": "too old", "claude": "not executable"},
			want:    "claude: not executable; codex: too old",
		},
	}
	for _, test := range skippedTests {
		t.Run("skipped "+test.name, func(t *testing.T) {
			if got := formatSkippedAgents(test.skipped); got != test.want {
				t.Fatalf("formatSkippedAgents() = %q, want %q", got, test.want)
			}
		})
	}
}

// The daemon probes `zen --version` to learn what a replaced binary
// reports; the template and the parser have to agree.
func TestVersionTemplateMatchesTheDaemonProbe(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("zen --version: %v", err)
	}
	if got := daemon.ParseSelfVersion(out.String()); got != version {
		t.Fatalf("ParseSelfVersion(%q) = %q, want the build version %q", out.String(), got, version)
	}
}

func TestRestartIntoBinarySpawnsTheReplacementWithTheSameStartFlags(t *testing.T) {
	shortenDaemonWaits(t)
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	newPID := os.Getpid() + 1
	fake := newFakeDaemon(t, nil)
	seams := stubProcessSeams(t, fake, os.Getpid(), newPID)

	var out bytes.Buffer
	err := restartIntoBinary(context.Background(), &out, "/opt/zenith-team/bin/zen", []string{"--log-level=debug"})
	if err != nil {
		t.Fatalf("restartIntoBinary: %v", err)
	}
	want := "/opt/zenith-team/bin/zen daemon start --foreground --log-level=debug"
	if got := strings.Join(seams.spawns[0], " "); got != want {
		t.Fatalf("spawned %q, want %q", got, want)
	}
	record, err := readDaemonProcessRecord(os.Getenv("ZENITH_TEAM_HOME"))
	if err != nil || record.PID != newPID {
		t.Fatalf("process record = %+v (err %v), want the replacement pid %d", record, err, newPID)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("ZENITH_TEAM_HOME"), daemonLogFileName)); err != nil {
		t.Fatalf("daemon log was not opened for the replacement: %v", err)
	}
}

func TestDaemonOverridesHonorTheAutoReloadLadder(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	if err := daemon.SaveCLIConfig(daemon.CLIConfig{DisableAutoReload: true}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	t.Run("config applies when the env is unset", func(t *testing.T) {
		t.Setenv("ZENITH_TEAM_AUTO_RELOAD", "")
		overrides, err := daemonOverrides(findSubcommand(t, newDaemonCmd(), "start"))
		if err != nil {
			t.Fatalf("daemonOverrides: %v", err)
		}
		if !overrides.DisableAutoReload {
			t.Fatal("config disable_auto_reload was not applied")
		}
	})
	t.Run("env wins over config", func(t *testing.T) {
		t.Setenv("ZENITH_TEAM_AUTO_RELOAD", "true")
		overrides, err := daemonOverrides(findSubcommand(t, newDaemonCmd(), "start"))
		if err != nil {
			t.Fatalf("daemonOverrides: %v", err)
		}
		if overrides.DisableAutoReload {
			t.Fatal("config disable_auto_reload overrode an explicit env value")
		}
	})
	t.Run("flag wins over everything", func(t *testing.T) {
		t.Setenv("ZENITH_TEAM_AUTO_RELOAD", "true")
		start := findSubcommand(t, newDaemonCmd(), "start")
		if err := start.Flags().Set("no-auto-reload", "true"); err != nil {
			t.Fatalf("set flag: %v", err)
		}
		overrides, err := daemonOverrides(start)
		if err != nil {
			t.Fatalf("daemonOverrides: %v", err)
		}
		if !overrides.DisableAutoReload {
			t.Fatal("--no-auto-reload was not applied")
		}
		if forwarded := strings.Join(
			forwardedStartFlags(start),
			" ",
		); !strings.Contains(
			forwarded,
			"--no-auto-reload=true",
		) {
			t.Fatalf("forwarded flags %q drop --no-auto-reload", forwarded)
		}
	})
}

func TestConfigSetDisableAutoReloadRoundTrip(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
	if _, err := executeCLI(t, newRootCmd, "config", "set", "disable_auto_reload", "true"); err != nil {
		t.Fatalf("config set: %v", err)
	}
	cfg, err := daemon.LoadCLIConfig()
	if err != nil || !cfg.DisableAutoReload {
		t.Fatalf("config = %+v (err %v), want disable_auto_reload persisted", cfg, err)
	}
	out, err := executeCLI(t, newRootCmd, "config", "show")
	if err != nil || !strings.Contains(out, "disable_auto_reload:") || !strings.Contains(out, "true") {
		t.Fatalf("config show = %q (err %v), want the knob listed", out, err)
	}
	if _, err := executeCLI(t, newRootCmd, "config", "set", "disable_auto_reload", "sometimes"); err == nil {
		t.Fatal("config set accepted a non-boolean disable_auto_reload")
	}
	if _, err := executeCLI(t, newRootCmd, "config", "set", "disable_auto_reload", ""); err != nil {
		t.Fatalf("config clear: %v", err)
	}
	if cfg, _ := daemon.LoadCLIConfig(); cfg.DisableAutoReload {
		t.Fatal("clearing disable_auto_reload left it set")
	}
}

// The restart wait has to outlast the daemon's own worst-case shutdown, or a
// perfectly healthy slow drain trips the "still stopping" branch and the
// replacement is never started. daemon.ShutdownDrainBudget is that worst case,
// summed from the daemon's own constants; the wait carries a real margin over
// it for the wall-clock slack around them.
func TestDaemonRestartWaitOutlastsTheDaemonShutdownBudget(t *testing.T) {
	margin := daemonRestartWait - daemon.ShutdownDrainBudget
	if margin < daemonRestartWaitMargin {
		t.Fatalf("daemonRestartWait = %v, only %v over the daemon's %v shutdown budget; want at least %v",
			daemonRestartWait, margin, daemon.ShutdownDrainBudget, daemonRestartWaitMargin)
	}
	if daemonRestartWaitMargin < 30*time.Second {
		t.Fatalf("daemonRestartWaitMargin = %v, want a margin worth the name", daemonRestartWaitMargin)
	}
}
