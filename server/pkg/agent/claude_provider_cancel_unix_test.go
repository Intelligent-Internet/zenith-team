//go:build unix

package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// claudeCancelFakeScript returns a POSIX-sh script that impersonates a
// long-running `claude` in stream-json mode: it spawns a background grandchild
// (standing in for an MCP server or tool subprocess), records both its own
// (process-group-leader) pid and the grandchild pid, emits a stream-json system
// init line, then streams stdout in a tight loop forever. This is the shape
// that orphaned a descendant for over an hour when the daemon only killed the
// leader. When ignoreTerm is true the whole group ignores SIGTERM, forcing the
// SIGKILL escalation path.
func claudeCancelFakeScript(ignoreTerm bool) string {
	trap := "trap 'exit 0' TERM\n"
	if ignoreTerm {
		trap = "trap '' TERM\n"
	}
	return "#!/bin/sh\n" + trap +
		`# Background grandchild so the test can assert the *whole* group is
# terminated on cancellation, not just the direct child.
( sleep 300 ) &
child=$!
if [ -n "$CLAUDE_PID_FILE" ]; then
  printf '%s %s\n' "$$" "$child" > "$CLAUDE_PID_FILE"
fi
printf '{"type":"system","session_id":"ses_fake"}\n'
while true; do
  printf '{"type":"system","session_id":"ses_fake"}\n'
  sleep 0.1
done
`
}

// claudeMixedSignalFakeScript returns a fake `claude` whose leader RESPECTS
// SIGTERM (so it exits and cmd.Wait() returns the instant the group is
// signaled) while a background grandchild IGNORES SIGTERM and detaches its
// stdio, so it holds neither the leader alive nor claude's stdout pipe. This is
// the mixed case that leaks when escalation keys off the leader's exit
// (procDone) instead of the whole process group: the leader is reaped, procDone
// closes, the SIGKILL is skipped, and the SIGTERM-resistant descendant survives
// — the exact orphan the group-wide escalation exists for.
func claudeMixedSignalFakeScript() string {
	return "#!/bin/sh\n" + "trap 'exit 0' TERM\n" +
		`# Grandchild ignores TERM and redirects its stdio away from the pipe so
# it does not keep claude's stdout open after the leader exits.
( trap '' TERM; sleep 300 ) </dev/null >/dev/null 2>&1 &
child=$!
if [ -n "$CLAUDE_PID_FILE" ]; then
  printf '%s %s\n' "$$" "$child" > "$CLAUDE_PID_FILE"
fi
printf '{"type":"system","session_id":"ses_fake"}\n'
while true; do
  printf '{"type":"system","session_id":"ses_fake"}\n'
  sleep 0.1
done
`
}

// TestClaudeCancellationTerminatesProcessGroupGraceful verifies that cancelling
// a run terminates a SIGTERM-respecting claude and its whole process group,
// returns an "aborted" result without hanging, and leaves no orphaned
// descendant.
func TestClaudeCancellationTerminatesProcessGroupGraceful(t *testing.T) {
	runClaudeCancellationTest(t, claudeCancelFakeScript(false))
}

// TestClaudeCancellationEscalatesToSIGKILL verifies the worst case: claude
// (and the descendants it spawned) ignore SIGTERM and keep running.
// Cancellation must escalate to a group SIGKILL, still return promptly, and
// still reap the whole group — without deadlocking on the stdout scanner or
// closing the pipe under a live writer.
func TestClaudeCancellationEscalatesToSIGKILL(t *testing.T) {
	claudeTerminateGraceNanos.Store(int64(300 * time.Millisecond))
	t.Cleanup(func() { claudeTerminateGraceNanos.Store(0) })
	runClaudeCancellationTest(t, claudeCancelFakeScript(true))
}

// TestClaudeCancellationEscalatesWhenDescendantIgnoresTERM is the mixed-signal
// regression: a SIGTERM-respecting leader plus a SIGTERM-ignoring,
// stdio-detached descendant. Cancellation must still reap the descendant, which
// only holds when the SIGKILL escalation is gated on the whole process group
// (not the leader's exit). This fails against the leader-keyed escalation.
func TestClaudeCancellationEscalatesWhenDescendantIgnoresTERM(t *testing.T) {
	claudeTerminateGraceNanos.Store(int64(300 * time.Millisecond))
	t.Cleanup(func() { claudeTerminateGraceNanos.Store(0) })
	runClaudeCancellationTest(t, claudeMixedSignalFakeScript())
}

func runClaudeCancellationTest(t *testing.T, script string) {
	t.Helper()

	tempDir := t.TempDir()
	pidFile := filepath.Join(tempDir, "pids")
	fakePath := filepath.Join(tempDir, "claude")
	writeTestExecutable(t, fakePath, []byte(script))

	provider := mustNewProvider(t, "claude", Config{
		ExecutablePath: fakePath,
		Logger:         slog.Default(),
		Env:            map[string]string{"CLAUDE_PID_FILE": pidFile},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session, err := provider.Execute(ctx, "prompt-ignored", ExecOptions{Cwd: tempDir})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	results := executionResults(session)

	pids := waitForPids(t, pidFile)

	cancel() // user cancels the run

	select {
	case res := <-results:
		if res.Status != "aborted" {
			t.Errorf("status = %q, want aborted", res.Status)
		}
	case <-time.After(providerFixtureWaitTimeout):
		t.Fatal("Execute did not return after cancellation (possible scanner deadlock or unkilled process)")
	}

	// The leader and the grandchild must both be gone — cancellation reaped the
	// whole group, leaving no orphan spinning.
	for _, pid := range pids {
		waitProcessGone(t, pid)
	}
}

// waitForPids polls pidFile until it contains the space-separated pids the fake
// recorded, then returns them.
func waitForPids(t *testing.T, pidFile string) []int {
	t.Helper()
	deadline := time.Now().Add(providerFixtureWaitTimeout)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			fields := strings.Fields(string(raw))
			if len(fields) >= 2 {
				pids := make([]int, 0, len(fields))
				ok := true
				for _, f := range fields {
					n, perr := strconv.Atoi(f)
					if perr != nil || n <= 0 {
						ok = false
						break
					}
					pids = append(pids, n)
				}
				if ok {
					return pids
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fake agent never recorded its pids in %s", pidFile)
	return nil
}

// waitProcessGone polls until signal 0 to pid reports the process no longer
// exists (ESRCH), failing if it is still alive after the deadline.
func waitProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(providerFixtureWaitTimeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("process %d still alive after cancellation — orphaned/leaked", pid)
}
