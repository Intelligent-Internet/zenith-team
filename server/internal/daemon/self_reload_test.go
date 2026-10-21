package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newSelfReloadTestDaemon returns a Daemon wired for trySelfReload: a real
// temp file standing in for the zenith-team binary (so the on-disk digest is the
// genuine article), the running version, and a sentinel cancelFunc the tests
// assert on to detect that a restart was scheduled.
func newSelfReloadTestDaemon(t *testing.T, runningVersion string) (*Daemon, string, *atomic.Int32) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "zenith-team")
	if err := os.WriteFile(binary, []byte("zenith-team build one"), 0o755); err != nil {
		t.Fatalf("write binary fixture: %v", err)
	}
	previousResolve := resolveSelfExecutable
	t.Cleanup(func() { resolveSelfExecutable = previousResolve })
	resolveSelfExecutable = func() (string, error) { return binary, nil }

	var restartCalls atomic.Int32
	d := &Daemon{
		cfg:        Config{CLIVersion: runningVersion, AutoReloadEnabled: true},
		logger:     testLogger(),
		cancelFunc: func() { restartCalls.Add(1) },
	}
	d.recordSelfDigest()
	return d, binary, &restartCalls
}

// replaceBinary overwrites the fixture so its digest no longer matches the
// one recorded at startup — what `make build` or an upgrade does in place.
func replaceBinary(t *testing.T, binary string) {
	t.Helper()
	if err := os.WriteFile(binary, []byte("zenith-team build two"), 0o755); err != nil {
		t.Fatalf("replace binary fixture: %v", err)
	}
}

// stubSelfVersion makes the `zen --version` probe report version (or fail)
// and counts how often it ran.
func stubSelfVersion(t *testing.T, version string, err error) *atomic.Int32 {
	t.Helper()
	previous := detectSelfVersion
	t.Cleanup(func() { detectSelfVersion = previous })
	var calls atomic.Int32
	detectSelfVersion = func(context.Context, string) (string, error) {
		calls.Add(1)
		return version, err
	}
	return &calls
}

func claimsPaused(d *Daemon) bool {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	return d.pauseClaims
}

func TestTrySelfReloadRestartsWhenTheBinaryOnDiskReportsAnotherVersion(t *testing.T) {
	d, binary, restartCalls := newSelfReloadTestDaemon(t, "0.9.2")
	replaceBinary(t, binary)
	stubSelfVersion(t, "0.9.3", nil)

	d.trySelfReload(context.Background())

	if restartCalls.Load() != 1 {
		t.Fatalf("restart scheduled %d times, want 1", restartCalls.Load())
	}
	if got := d.RestartBinary(); got != binary {
		t.Fatalf("RestartBinary() = %q, want the probed binary %q", got, binary)
	}
	if !claimsPaused(d) {
		t.Fatal("claims must stay paused across the handoff so nothing is claimed mid-shutdown")
	}
	reason := d.reloadPending()
	if !strings.Contains(reason, "0.9.3") || !strings.Contains(reason, "0.9.2") {
		t.Fatalf("reload pending reason = %q, want both versions named", reason)
	}
}

// A developer's rebuild reports the same "dev" version on both sides. The
// bytes changed, and that is the fact the restart follows.
func TestTrySelfReloadRestartsWhenADevBuildIsReplacedInPlace(t *testing.T) {
	d, binary, restartCalls := newSelfReloadTestDaemon(t, "dev")
	replaceBinary(t, binary)
	stubSelfVersion(t, "dev", nil)

	d.trySelfReload(context.Background())

	if restartCalls.Load() != 1 {
		t.Fatalf("restart scheduled %d times, want 1 for a replaced dev build", restartCalls.Load())
	}
	if reason := d.reloadPending(); !strings.Contains(reason, "replaced") {
		t.Fatalf("reload pending reason = %q, want it to say the binary was replaced", reason)
	}
}

func TestTrySelfReloadIsANoopWhileTheBinaryIsUnchanged(t *testing.T) {
	d, _, restartCalls := newSelfReloadTestDaemon(t, "0.9.2")
	probes := stubSelfVersion(t, "0.9.9", nil)
	d.setReloadPending("stale note from an earlier round")

	d.trySelfReload(context.Background())

	if restartCalls.Load() != 0 {
		t.Fatal("restart scheduled although the binary on disk is unchanged")
	}
	if probes.Load() != 0 {
		t.Fatalf("probed --version %d times, want 0 when the digest matches", probes.Load())
	}
	if got := d.reloadPending(); got != "" {
		t.Fatalf("reload pending reason = %q, want it cleared once the change went away", got)
	}
}

// The binary is most likely unreadable or half-written during the very
// upgrade this check exists to detect. "Could not probe it" is never a reason
// to restart into it.
func TestTrySelfReloadDoesNotRestartIntoABinaryThatFailsItsProbe(t *testing.T) {
	d, binary, restartCalls := newSelfReloadTestDaemon(t, "0.9.2")
	replaceBinary(t, binary)
	stubSelfVersion(t, "", errors.New("fork/exec: text file busy"))

	d.trySelfReload(context.Background())

	if restartCalls.Load() != 0 {
		t.Fatal("restart scheduled on a failed probe")
	}
	if claimsPaused(d) {
		t.Fatal("a failed probe must not pause claiming")
	}
	if got := d.reloadPending(); got != "" {
		t.Fatalf("reload pending reason = %q, want empty after a failed probe", got)
	}
}

// A run is never interrupted by a restart. Both shapes of busy defer, the
// deferral is visible on /health, and the next idle tick restarts.
func TestTrySelfReloadDefersWhileTheDaemonIsBusy(t *testing.T) {
	cases := []struct {
		name string
		busy func(*testing.T, *Daemon)
		idle func(*Daemon)
	}{
		{
			name: "run in flight",
			busy: func(_ *testing.T, d *Daemon) { d.activeRuns.Store(1) },
			idle: func(d *Daemon) { d.activeRuns.Store(0) },
		},
		{
			name: "claim in flight",
			busy: func(t *testing.T, d *Daemon) {
				t.Helper()
				if !d.enterClaim() {
					t.Fatal("enterClaim refused while nothing holds the barrier")
				}
			},
			idle: func(d *Daemon) { d.exitClaim() },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, binary, restartCalls := newSelfReloadTestDaemon(t, "0.9.2")
			replaceBinary(t, binary)
			probes := stubSelfVersion(t, "0.9.3", nil)
			tc.busy(t, d)

			d.trySelfReload(context.Background())

			if restartCalls.Load() != 0 {
				t.Fatal("restart scheduled while the daemon was busy")
			}
			if claimsPaused(d) {
				t.Fatal("a deferred reload must leave claiming enabled")
			}
			if d.reloadPending() == "" {
				t.Fatal("a deferred reload must be explained on /health")
			}

			tc.idle(d)
			d.trySelfReload(context.Background())

			if restartCalls.Load() != 1 {
				t.Fatalf(
					"restart scheduled %d times after the daemon went idle, want 1",
					restartCalls.Load(),
				)
			}
			if probes.Load() != 2 {
				t.Fatalf("probed %d times, want one probe per tick", probes.Load())
			}
		})
	}
}

func TestTrySelfReloadSchedulesTheRestartOnce(t *testing.T) {
	d, binary, restartCalls := newSelfReloadTestDaemon(t, "0.9.2")
	replaceBinary(t, binary)
	stubSelfVersion(t, "0.9.3", nil)

	d.trySelfReload(context.Background())
	d.trySelfReload(context.Background())

	if restartCalls.Load() != 1 {
		t.Fatalf("cancelFunc fired %d times, want 1", restartCalls.Load())
	}
}

// When the daemon could not read its own binary at startup there is no
// baseline to compare against; the first readable digest becomes the baseline
// instead of counting as a change.
func TestTrySelfReloadAdoptsTheBaselineWhenStartupCouldNotReadTheBinary(t *testing.T) {
	d, _, restartCalls := newSelfReloadTestDaemon(t, "0.9.2")
	d.selfDigest.Store(nil)
	stubSelfVersion(t, "0.9.3", nil)

	d.trySelfReload(context.Background())

	if restartCalls.Load() != 0 {
		t.Fatal("restart scheduled without a baseline digest")
	}
	if d.selfDigest.Load() == nil {
		t.Fatal("the first readable digest was not adopted as the baseline")
	}
}

// The claim barrier is the poller's contract: enterClaim refuses while the
// barrier holds, and the barrier refuses while a claim or a run is in flight.
func TestClaimBarrierAndPollerExcludeEachOther(t *testing.T) {
	d := &Daemon{logger: testLogger()}

	if !d.enterClaim() {
		t.Fatal("enterClaim refused with no barrier held")
	}
	if d.trySetClaimBarrier() {
		t.Fatal("barrier acquired while a claim was in flight")
	}
	d.exitClaim()

	d.activeRuns.Store(1)
	if d.trySetClaimBarrier() {
		t.Fatal("barrier acquired while a run was in flight")
	}
	d.activeRuns.Store(0)

	if !d.trySetClaimBarrier() {
		t.Fatal("barrier refused on an idle daemon")
	}
	if d.trySetClaimBarrier() {
		t.Fatal("barrier double-acquired")
	}
	if d.enterClaim() {
		t.Fatal("enterClaim succeeded while the barrier holds")
	}
	d.releaseClaimBarrier()
	if !d.enterClaim() {
		t.Fatal("enterClaim refused after the barrier was released")
	}
	d.exitClaim()
}

func TestSelfReloadLoopSkipsDesktopManagedAndDisabledDaemons(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*Daemon)
	}{
		{"managed by desktop", func(d *Daemon) { d.cfg.LaunchedBy = "desktop" }},
		{"auto-reload disabled", func(d *Daemon) { d.cfg.AutoReloadEnabled = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, binary, restartCalls := newSelfReloadTestDaemon(t, "0.9.2")
			replaceBinary(t, binary)
			probes := stubSelfVersion(t, "0.9.3", nil)
			shortenSelfReloadTimers(t)
			tc.setup(d)

			// Returns immediately rather than looping, so no cancel is needed.
			d.selfReloadLoop(context.Background())

			if probes.Load() != 0 || restartCalls.Load() != 0 {
				t.Fatalf(
					"probes = %d, restarts = %d; want the loop to do nothing",
					probes.Load(),
					restartCalls.Load(),
				)
			}
		})
	}
}

func TestSelfReloadLoopFollowsAReplacedBinary(t *testing.T) {
	d, binary, restartCalls := newSelfReloadTestDaemon(t, "0.9.2")
	replaceBinary(t, binary)
	stubSelfVersion(t, "0.9.3", nil)
	shortenSelfReloadTimers(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		d.selfReloadLoop(ctx)
		close(done)
	}()
	deadline := time.Now().Add(4 * time.Second)
	for restartCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if restartCalls.Load() != 1 {
		t.Fatalf("restart scheduled %d times by the loop, want 1", restartCalls.Load())
	}
}

func TestHealthExplainsADeferredRestart(t *testing.T) {
	d := &Daemon{cfg: Config{CLIVersion: "0.9.2"}, logger: testLogger()}
	read := func() HealthResponse {
		t.Helper()
		recorder := httptest.NewRecorder()
		d.healthHandler(time.Now())(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
		var health HealthResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &health); err != nil {
			t.Fatalf("decode health: %v", err)
		}
		return health
	}
	if got := read().ReloadPendingReason; got != "" {
		t.Fatalf("reloadPendingReason = %q, want empty when nothing is pending", got)
	}
	d.setReloadPending("zenith-team binary on disk reports 0.9.3, running 0.9.2")
	if got := read().ReloadPendingReason; !strings.Contains(got, "0.9.3") {
		t.Fatalf("reloadPendingReason = %q, want the pending reason", got)
	}
}

// ZENITH_TEAM_AUTO_RELOAD=false and the --no-auto-reload override can only turn
// the watch off; nothing forces it on over an explicit env opt-out.
func TestLoadConfigAutoReloadLadder(t *testing.T) {
	isolateProviderDiscovery(t, t.TempDir())
	stubLoginShellResolver(t, nil)
	cases := []struct {
		name      string
		env       string
		overrides Overrides
		want      bool
	}{
		{name: "default on", env: "", want: true},
		{name: "env off", env: "false", want: false},
		{name: "override off", env: "", overrides: Overrides{DisableAutoReload: true}, want: false},
		{
			name:      "env on and override off",
			env:       "true",
			overrides: Overrides{DisableAutoReload: true},
			want:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ZENITH_TEAM_HOME", t.TempDir())
			t.Setenv("ZENITH_TEAM_AUTO_RELOAD", tc.env)
			tc.overrides.AllowNoAgents = true
			cfg, err := LoadConfig(tc.overrides)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.AutoReloadEnabled != tc.want {
				t.Fatalf("AutoReloadEnabled = %v, want %v", cfg.AutoReloadEnabled, tc.want)
			}
		})
	}
}

func TestParseSelfVersion(t *testing.T) {
	cases := []struct{ name, raw, want string }{
		{"version template", "zen 0.9.3\n", "0.9.3"},
		{"dev build", "zen dev\n", "dev"},
		{"unexpected shape falls back to the first line", "0.9.3\n", "0.9.3"},
		{"empty output", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseSelfVersion(tc.raw); got != tc.want {
				t.Fatalf("ParseSelfVersion(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// shortenSelfReloadTimers collapses the loop's startup delay and interval so a
// loop-level test finishes in milliseconds.
func shortenSelfReloadTimers(t *testing.T) {
	t.Helper()
	previousDelay, previousInterval := selfReloadInitialDelay, selfReloadCheckInterval
	t.Cleanup(func() {
		selfReloadInitialDelay = previousDelay
		selfReloadCheckInterval = previousInterval
	})
	selfReloadInitialDelay = time.Millisecond
	selfReloadCheckInterval = 5 * time.Millisecond
}
