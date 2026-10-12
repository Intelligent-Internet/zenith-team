package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `zen daemon logs` is the documented way to see why a daemon is not
// registering or heartbeating, and the product page promises those causes
// land in the file it tails. That only holds if the daemon's structured log
// goes to the stream `daemon start` redirects into that file. It used to go
// to the other one, so daemon.log stayed 0 bytes for the life of the daemon
// and the command printed two paths and nothing else, while every line sat
// in daemon.err.log.
func TestDaemonLogStreamIsTheFileDaemonLogsTails(t *testing.T) {
	if daemonLogDestination() != os.Stdout {
		t.Fatal("the daemon must write its structured log to stdout — that is the stream " +
			"spawnDetachedDaemon redirects into daemon.log, which `daemon logs` tails")
	}
	// daemon.err.log stays the crash sink: whatever the runtime writes when
	// it dies without logging (a panic) rather than a stream nobody reads.
	if daemonLogFileName == daemonErrLogFileName {
		t.Fatal("the log and the crash sink must be different files")
	}
}

// The two halves the CLI owns must name the same file: the child's stdout and
// the path the tail reads.
func TestDaemonLogsTailsTheChildsStdoutFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BOARDLY_HOME", home)

	logPath := filepath.Join(home, daemonLogFileName)
	if err := os.WriteFile(logPath, []byte("registered runtime claude\nheartbeat accepted\n"), 0o644); err != nil {
		t.Fatalf("seed daemon log: %v", err)
	}
	// The crash sink must not be what the command shows.
	err := os.WriteFile(filepath.Join(home, daemonErrLogFileName), []byte("panic: not this file\n"), 0o644)
	if err != nil {
		t.Fatalf("seed daemon error log: %v", err)
	}

	out, runErr := executeCLI(t, newRootCmd, "daemon", "logs", "-n", "10")
	if runErr != nil {
		t.Fatalf("daemon logs: %v", runErr)
	}
	for _, want := range []string{logPath, "registered runtime claude", "heartbeat accepted"} {
		if !strings.Contains(out, want) {
			t.Fatalf("daemon logs output %q is missing %q", out, want)
		}
	}
	if strings.Contains(out, "panic: not this file") {
		t.Fatalf("daemon logs output %q tails the crash sink instead of the log", out)
	}
}
