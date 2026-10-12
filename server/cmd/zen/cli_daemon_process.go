package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	daemonpkg "boardly.dev/boardly/server/internal/daemon"
)

// Process supervision for the local daemon: spawn a detached instance and
// wait until it reports ready, stop the instance answering on the health port
// and wait until it is gone, and prove the session a replacement needs before
// the running one is touched (verifyDaemonSession). `daemon start`, `daemon stop`, `daemon restart`
// and the daemon's own handoff into a replaced binary all compose these.
//
// /health picks the target and the process proves the exit: a command signals
// only the instance /health names — the process record exists to detect PID
// reuse, never to pick a target — and then watches that pid, because the
// daemon holds the health port (its single-instance lock) until it exits, so
// the port going quiet says nothing about whether a successor could bind it.

const (
	daemonPidFileName    = "daemon.pid"
	daemonLogFileName    = "daemon.log"
	daemonErrLogFileName = "daemon.err.log"
	// daemonLogRotateBytes rotates a grown log aside (single .old generation)
	// before the next start appends to a fresh file.
	daemonLogRotateBytes = 10 << 20
	// daemonExitNoteInterval paces the "still waiting" lines while a stopping
	// daemon drains its runs.
	daemonExitNoteInterval = 5 * time.Second
	// daemonRestartWaitMargin is how far daemonRestartWait sits above the
	// daemon's own shutdown budget: wall-clock slack around the waits that
	// budget sums — process scheduling, a deregister that uses its whole
	// timeout, the final log flush — none of which the daemon's constants
	// account for.
	daemonRestartWaitMargin = 60 * time.Second
)

// The waits are variables so command-level tests can collapse them.
var (
	// daemonReadyTimeout bounds the readiness poll of the health port:
	// liveness (port up) is not readiness (status running).
	daemonReadyTimeout = 45 * time.Second
	// daemonStopWait bounds how long `daemon stop` watches for the exit before
	// handing the wait back to the user; the daemon keeps draining regardless.
	daemonStopWait = 15 * time.Second
	// daemonRestartWait bounds how long a restart waits for the old process
	// before it refuses to start the replacement, so it must outlast the
	// daemon's own worst-case shutdown — daemonpkg.ShutdownDrainBudget, the
	// provider drain plus the terminal-callback extension, deregister, app
	// preview cleanup, and the health server's shutdown, summed from the
	// daemon's own constants. Sitting on that number exactly would let a
	// genuinely healthy slow shutdown trip the "still stopping" branch, which
	// costs the user their daemon; the margin is what makes the wait never the
	// reason a restart gives up.
	daemonRestartWait = daemonpkg.ShutdownDrainBudget + daemonRestartWaitMargin
	// daemonPollInterval paces every readiness and exit poll.
	daemonPollInterval = 500 * time.Millisecond
)

var errDaemonNotRunning = errors.New("daemon is not running")

// The three process seams. Tests replace them so a lifecycle command can be
// exercised end to end against a fake health port without forking or
// signalling anything.
var (
	signalDaemonProcess = func(pid int) error {
		return syscall.Kill(pid, syscall.SIGTERM)
	}
	processAlive = func(pid int) bool {
		process, err := os.FindProcess(pid)
		if err != nil {
			return false
		}
		return process.Signal(syscall.Signal(0)) == nil
	}
	// spawnDaemonProcess starts binary with args in a fresh session, so
	// closing the shell never delivers SIGHUP to it, and returns its pid.
	spawnDaemonProcess = func(binary string, args []string, stdout, stderr *os.File) (int, error) {
		// Deliberately exec.Command and not exec.CommandContext: this child is
		// the detached daemon and must outlive the process that spawned it.
		//nolint:noctx // the detached daemon outlives this process by design
		child := exec.Command(binary, args...)
		child.Stdout = stdout
		child.Stderr = stderr
		child.Stdin = nil
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			return 0, fmt.Errorf("spawn daemon: %w", err)
		}
		pid := child.Process.Pid
		// The child outlives this process; releasing avoids a zombie reap
		// obligation the CLI cannot fulfill after it exits.
		_ = child.Process.Release()
		return pid, nil
	}
)

type daemonProcessRecord struct {
	SchemaVersion int       `json:"schemaVersion"`
	PID           int       `json:"pid"`
	StartedAt     time.Time `json:"startedAt"`
}

// spawnDetachedDaemon launches binary as `daemon start --foreground <args>`
// with its output on the log files under home, waits for /health to report
// the new process as running, records its identity, and returns that health.
func spawnDetachedDaemon(
	ctx context.Context,
	out io.Writer,
	home, binary string,
	args []string,
) (*daemonpkg.HealthResponse, error) {
	logPath := filepath.Join(home, daemonLogFileName)
	errPath := filepath.Join(home, daemonErrLogFileName)
	rotateLog(logPath)
	rotateLog(errPath)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open daemon log: %w", err)
	}
	defer logFile.Close()
	errFile, err := os.OpenFile(errPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open daemon error log: %w", err)
	}
	defer errFile.Close()

	pid, err := spawnDaemonProcess(
		binary,
		append([]string{"daemon", "start", "--foreground"}, args...),
		logFile,
		errFile,
	)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "daemon starting (pid %d)\n", pid)

	deadline := time.Now().Add(daemonReadyTimeout)
	for {
		health, err := fetchDaemonHealth(ctx)
		if err == nil && health.Status == "running" {
			if health.PID != pid || health.StartedAt.IsZero() {
				return nil, errors.New("daemon health identity does not match the process that was started")
			}
			if err := writeDaemonProcessRecord(home, daemonProcessRecord{
				SchemaVersion: 1,
				PID:           health.PID,
				StartedAt:     health.StartedAt,
			}); err != nil {
				fmt.Fprintf(out, "warning: write daemon process record failed: %v\n", err)
			}
			fmt.Fprintf(out, "daemon ready (pid %d, version %s)\n", health.PID, health.CLIVersion)
			return health, nil
		}
		if !processAlive(pid) {
			return nil, fmt.Errorf("daemon exited during startup; check %s", errPath)
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("daemon did not become ready within %s; check %s", daemonReadyTimeout, errPath)
		}
		if err := sleepDaemonPoll(ctx); err != nil {
			return nil, err
		}
	}
}

// restartIntoBinary is the daemon's own handoff: once Run has returned because
// a replaced boardly binary was detected, the old process starts the
// replacement detached with the same start flags and only exits once the
// replacement reports ready.
func restartIntoBinary(ctx context.Context, out io.Writer, binary string, args []string) error {
	home, err := daemonpkg.BoardlyHome()
	if err != nil {
		return err
	}
	if _, err := spawnDetachedDaemon(ctx, out, home, binary, args); err != nil {
		return fmt.Errorf("restart into %s: %w", binary, err)
	}
	return nil
}

// stopDaemonProcess signals only the exact process instance currently
// answering on the health port, then waits up to wait for that process to
// exit. It returns the health of the instance it signalled and whether the
// instance is gone; a false second value means the daemon is still draining
// in-flight runs, still holds the health port, and will exit on its own.
func stopDaemonProcess(
	ctx context.Context,
	out io.Writer,
	home string,
	wait time.Duration,
) (*daemonpkg.HealthResponse, bool, error) {
	health, err := fetchDaemonHealth(ctx)
	if err != nil {
		return nil, false, errDaemonNotRunning
	}
	if health.PID < 1 || health.StartedAt.IsZero() || health.StartedAt.After(time.Now().Add(5*time.Second)) {
		return nil, false, errors.New("daemon health returned an invalid process identity")
	}
	record, err := readDaemonProcessRecord(home)
	if err == nil && (record.PID != health.PID || !record.StartedAt.Equal(health.StartedAt)) {
		return nil, false, errors.New("refusing to signal a daemon whose process identity changed")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("read daemon process record: %w", err)
	}
	if !processAlive(health.PID) {
		return nil, false, errDaemonNotRunning
	}
	if err := signalDaemonProcess(health.PID); err != nil {
		return nil, false, fmt.Errorf("signal daemon: %w", err)
	}
	fmt.Fprintf(out, "sent SIGTERM to daemon (pid %d)\n", health.PID)
	exited, err := waitForDaemonExit(ctx, out, health.PID, wait)
	return health, exited, err
}

// waitForDaemonExit polls the process itself until it is gone, narrating the
// drain as it goes. Only the exit proves the daemon released the health port
// its successor has to bind: the port stops answering somewhere inside the
// shutdown — 503 while the health server closes admission, then nothing —
// while the process is still finishing its runs, so a silent port is not an
// exit. False means the deadline passed with the process still alive.
func waitForDaemonExit(ctx context.Context, out io.Writer, pid int, wait time.Duration) (bool, error) {
	deadline := time.Now().Add(wait)
	var lastNote time.Time
	for {
		if !processAlive(pid) {
			return true, nil
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		if lastNote.IsZero() || time.Since(lastNote) >= daemonExitNoteInterval {
			noteDaemonExitWait(ctx, out, pid)
			lastNote = time.Now()
		}
		if err := sleepDaemonPoll(ctx); err != nil {
			return false, err
		}
	}
}

// noteDaemonExitWait prints one "still waiting" line, carrying the in-flight
// run count while /health answers and falling back to the bare line once the
// daemon's health server has shut down ahead of the process.
func noteDaemonExitWait(ctx context.Context, out io.Writer, pid int) {
	health, err := fetchDaemonHealth(ctx)
	if err != nil || health.PID != pid {
		fmt.Fprintln(out, "waiting for the daemon to exit")
		return
	}
	fmt.Fprintf(out, "waiting for the daemon to exit (%d run(s) in flight)\n", health.ActiveRunCount)
}

func sleepDaemonPoll(ctx context.Context) error {
	timer := time.NewTimer(daemonPollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func writeDaemonProcessRecord(home string, record daemonProcessRecord) (returnErr error) {
	if record.SchemaVersion != 1 || record.PID < 1 || record.StartedAt.IsZero() {
		return errors.New("daemon process record is invalid")
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode daemon process record: %w", err)
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(home, ".daemon-process-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary daemon process record: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if temporary != nil {
			returnErr = errors.Join(returnErr, temporary.Close())
		}
		if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("remove temporary daemon process record: %w", err),
			)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary daemon process record: %w", err)
	}
	written, err := temporary.Write(encoded)
	if err != nil {
		return fmt.Errorf("write temporary daemon process record: %w", err)
	}
	if written != len(encoded) {
		return fmt.Errorf("write temporary daemon process record: %w", io.ErrShortWrite)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary daemon process record: %w", err)
	}
	closeErr := temporary.Close()
	temporary = nil
	if closeErr != nil {
		return fmt.Errorf("close temporary daemon process record: %w", closeErr)
	}
	if err := os.Rename(temporaryPath, filepath.Join(home, daemonPidFileName)); err != nil {
		return fmt.Errorf("install daemon process record: %w", err)
	}
	return nil
}

func readDaemonProcessRecord(home string) (daemonProcessRecord, error) {
	raw, err := os.ReadFile(filepath.Join(home, daemonPidFileName))
	if err != nil {
		return daemonProcessRecord{}, err
	}
	var record daemonProcessRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return daemonProcessRecord{}, fmt.Errorf("decode daemon process record: %w", err)
	}
	if record.SchemaVersion != 1 || record.PID < 1 || record.StartedAt.IsZero() {
		return daemonProcessRecord{}, errors.New("daemon process record is invalid")
	}
	return record, nil
}

func rotateLog(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < daemonLogRotateBytes {
		return
	}
	_ = os.Rename(path, path+".old")
}

// daemonHealthPort is the loopback port of the daemon this shell belongs
// to: the operator's configured port, or — inside a Run — the port the
// daemon injected as BOARDLY_DAEMON_PORT, so `zen daemon status` in an
// agent shell can never describe a different daemon.
func daemonHealthPort() int {
	for _, name := range []string{"BOARDLY_HEALTH_PORT", "BOARDLY_DAEMON_PORT"} {
		if raw := strings.TrimSpace(os.Getenv(name)); raw != "" {
			if port, err := strconv.Atoi(raw); err == nil && port > 0 {
				return port
			}
		}
	}
	return daemonpkg.DefaultHealthPort
}

func fetchDaemonHealth(ctx context.Context) (*daemonpkg.HealthResponse, error) {
	httpClient := &http.Client{Timeout: 2 * time.Second}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d/health", daemonHealthPort()),
		nil,
	)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("health returned %d", resp.StatusCode)
	}
	var health daemonpkg.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return nil, err
	}
	return &health, nil
}
