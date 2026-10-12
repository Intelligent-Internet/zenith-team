package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Self-reload: a CLI-launched daemon follows the zen binary it was
// started from. A rebuild, a re-download or a package upgrade replaces the
// file at the same path, and without this the daemon keeps serving the build
// it booted with — its version string is frozen at compile time and nothing
// else on the machine notices an in-place overwrite. Desktop-managed daemons
// skip it: the app owns its bundled CLI and restarts the daemon itself on a
// version or executable-digest mismatch.
//
// The mechanism is deliberately stateless across ticks: a busy daemon defers
// to the next tick instead of parking claims behind a pending-restart state
// machine, so a deferral can never leave the daemon unable to claim. The
// deferral is recorded in reloadPendingReason purely so /health and
// `zen daemon status` can explain why the daemon is still on the old
// version.

// selfReloadInitialDelay is how long the loop waits after startup before its
// first check; the daemon has enough to do while registering. Overridable
// for tests.
var selfReloadInitialDelay = 2 * time.Minute

// selfReloadCheckInterval bounds how long a user waits after replacing the
// binary. A tick that lands while the daemon is busy defers to the next one,
// so the real wait is "the first tick that is both due and idle". One file
// digest per tick, machine-level. Overridable for tests.
var selfReloadCheckInterval = 10 * time.Minute

// selfReloadProbeTimeout bounds the `--version` fork/exec of the replaced
// binary. Generous: the point is to stop a wedged binary from parking the
// loop, not to police a slow disk.
var selfReloadProbeTimeout = 10 * time.Second

// resolveSelfExecutable is the path a restart re-execs. A seam so tests never
// touch the real process image.
var resolveSelfExecutable = os.Executable

// detectSelfVersion runs `<path> --version` and returns the version field.
// A seam so tests never fork a real process.
var detectSelfVersion = func(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, path, "--version")
	// Context cancellation only kills the direct child; WaitDelay force-closes
	// the pipes after the kill so a misbehaving replacement cannot park the
	// loop goroutine past the deadline.
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("run %s --version: %w", path, err)
	}
	return ParseSelfVersion(string(out)), nil
}

// ParseSelfVersion pulls the version out of `zen --version` output, whose
// first line is rendered by cmd/zen's version template as
// `zen <version>`. The extracted field is exactly what the build stamps
// into main.version, which is also what lands in Config.CLIVersion — so the
// two are directly comparable. Anything that does not match the template
// shape is returned trimmed, which compares unequal and is therefore
// reported rather than silently ignored.
//
// Exported solely so cmd/zen can pin that contract from the producing
// side: this parser and the version template have to agree, and nothing
// else would notice if a template edit silently broke the probe.
func ParseSelfVersion(raw string) string {
	line, _, _ := strings.Cut(raw, "\n")
	line = strings.TrimSpace(line)
	if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "zen" {
		return fields[1]
	}
	return line
}

// executableDigest hashes the file at path. The digest, not the version
// string, is the signal that the binary was replaced: a developer's rebuild
// reports the same "dev" version on both sides, and only the bytes tell them
// apart.
func executableDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// recordSelfDigest captures the baseline the reload check compares against.
// Called once at startup; a failure leaves no baseline, and the first
// readable digest is adopted later instead of being mistaken for a change.
func (d *Daemon) recordSelfDigest() {
	target, err := resolveSelfExecutable()
	if err != nil {
		d.logger.Warn("auto-reload: could not resolve own executable", "error", err)
		return
	}
	digest, err := executableDigest(target)
	if err != nil {
		d.logger.Warn("auto-reload: could not read own executable", "binary", target, "error", err)
		return
	}
	d.selfDigest.Store(&digest)
}

// selfReloadLoop runs the on-disk check on its own goroutine until ctx ends.
func (d *Daemon) selfReloadLoop(ctx context.Context) {
	if d.cfg.LaunchedBy == "desktop" {
		d.logger.Info("auto-reload: skipped (managed by the desktop app)")
		return
	}
	if !d.cfg.AutoReloadEnabled {
		d.logger.Info("auto-reload: disabled")
		return
	}
	d.logger.Info("auto-reload: watching the zen binary on disk",
		"interval", selfReloadCheckInterval, "current", d.cfg.CLIVersion)

	if err := sleepWithContext(ctx, selfReloadInitialDelay); err != nil {
		return
	}
	d.trySelfReload(ctx)

	// The ticker starts only now, after the first check, so an interval
	// shorter than the delay cannot buffer a tick and fire twice in a row.
	ticker := time.NewTicker(selfReloadCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.trySelfReload(ctx)
		}
	}
}

// trySelfReload restarts the daemon when the zen binary on disk is no
// longer the one this process was started from.
//
// A failed probe is never treated as a change. The binary is most likely to
// be unreadable or half-written during the few hundred milliseconds of the
// very upgrade this check exists to detect, and "could not read it" is not a
// reason to restart into it.
func (d *Daemon) trySelfReload(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if d.RestartBinary() != "" {
		return
	}
	target, err := resolveSelfExecutable()
	if err != nil {
		d.logger.Warn("auto-reload: could not resolve own executable — will retry", "error", err)
		return
	}
	digest, err := executableDigest(target)
	if err != nil {
		d.logger.Warn("auto-reload: could not read the binary on disk — will retry",
			"binary", target, "error", err)
		return
	}
	baseline := d.selfDigest.Load()
	if baseline == nil {
		d.selfDigest.Store(&digest)
		return
	}
	if digest == *baseline {
		d.clearReloadPending()
		return
	}

	// The replacement must answer --version before it is trusted with the
	// handoff; a half-written file fails here and is retried next tick.
	probeCtx, cancel := context.WithTimeout(ctx, selfReloadProbeTimeout)
	defer cancel()
	onDisk, err := detectSelfVersion(probeCtx, target)
	if err != nil {
		d.logger.Warn("auto-reload: binary on disk changed but does not answer --version yet — will retry",
			"binary", target, "error", err)
		return
	}
	reason := describeReload(onDisk, d.cfg.CLIVersion)
	d.setReloadPending(reason)

	if !d.trySetClaimBarrier() {
		d.logger.Info("auto-reload: deferring — a run or a claim is in flight", "reason", reason)
		return
	}
	barrierReleased := false
	defer func() {
		if !barrierReleased {
			d.releaseClaimBarrier()
		}
	}()

	d.logger.Info("auto-reload: restarting into the binary on disk", "reason", reason, "binary", target)
	if !d.triggerRestart(target) {
		// A failed handoff must never cost the daemon its ability to claim.
		return
	}
	barrierReleased = true
}

// describeReload words the pending-restart reason for /health and the CLI.
func describeReload(onDisk, running string) string {
	if onDisk != running {
		return fmt.Sprintf("zen binary on disk reports %s, running %s", onDisk, running)
	}
	return fmt.Sprintf("zen binary on disk was replaced (still reports %s)", running)
}

// setReloadPending records that a binary change is confirmed but the restart
// is waiting for the daemon to go idle. Purely diagnostic — surfaced on
// /health and `daemon status` — and it gates nothing.
func (d *Daemon) setReloadPending(reason string) {
	d.reloadPendingReason.Store(&reason)
}

// clearReloadPending drops the note once the binary on disk matches again.
func (d *Daemon) clearReloadPending() {
	d.reloadPendingReason.Store(nil)
}

// reloadPending reports the deferred-restart reason, empty when none.
func (d *Daemon) reloadPending() string {
	if reason := d.reloadPendingReason.Load(); reason != nil {
		return *reason
	}
	return ""
}

// enterClaim is the poller's gate: it refuses while a restart barrier holds
// and otherwise counts the claim in flight until exitClaim.
func (d *Daemon) enterClaim() bool {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	if d.pauseClaims {
		return false
	}
	d.claimsInFlight++
	return true
}

// exitClaim is called once every run the claim returned has been counted in
// activeRuns, so the barrier check never sees a gap between the two.
func (d *Daemon) exitClaim() {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	d.claimsInFlight--
}

// trySetClaimBarrier pauses claiming only when nothing is being claimed and
// nothing is running. It refuses when the barrier is already held so two
// holders cannot release it out from under each other.
func (d *Daemon) trySetClaimBarrier() bool {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	if d.pauseClaims || d.claimsInFlight > 0 || d.activeRuns.Load() > 0 {
		return false
	}
	d.pauseClaims = true
	return true
}

func (d *Daemon) releaseClaimBarrier() {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	d.pauseClaims = false
}

// triggerRestart records the binary to re-exec and cancels the root context
// so Run returns; the CLI's foreground command then hands the process over.
// Reports false when no handoff can happen, so the caller restores whatever
// it paused.
func (d *Daemon) triggerRestart(binary string) bool {
	d.restartMu.Lock()
	defer d.restartMu.Unlock()
	if d.restartBinary != "" {
		return true
	}
	if d.cancelFunc == nil {
		d.logger.Error("auto-reload: restart requested before the daemon started")
		return false
	}
	d.restartBinary = binary
	d.cancelFunc()
	return true
}

// RestartBinary is the path Run's caller must re-exec once Run returns, or
// empty when the daemon stopped for any other reason.
func (d *Daemon) RestartBinary() string {
	d.restartMu.Lock()
	defer d.restartMu.Unlock()
	return d.restartBinary
}
