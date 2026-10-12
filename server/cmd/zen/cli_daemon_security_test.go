package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	daemonpkg "boardly.dev/boardly/server/internal/daemon"
)

func TestWriteDaemonProcessRecordDoesNotFollowDestinationSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink replacement semantics are platform-specific")
	}
	home := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("seed symlink target: %v", err)
	}
	recordPath := filepath.Join(home, daemonPidFileName)
	if err := os.Symlink(victim, recordPath); err != nil {
		t.Fatalf("seed process-record symlink: %v", err)
	}
	record := daemonProcessRecord{
		SchemaVersion: 1,
		PID:           4321,
		StartedAt:     time.Date(2026, time.August, 30, 9, 0, 0, 0, time.UTC),
	}
	if err := writeDaemonProcessRecord(home, record); err != nil {
		t.Fatalf("writeDaemonProcessRecord() error = %v", err)
	}
	gotVictim, err := os.ReadFile(victim)
	if err != nil || string(gotVictim) != "keep me" {
		t.Fatalf("symlink target = %q, %v; want unchanged", gotVictim, err)
	}
	info, err := os.Lstat(recordPath)
	if err != nil {
		t.Fatalf("lstat process record: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("process record mode = %v, want regular 0600", info.Mode())
	}
	gotRecord, err := readDaemonProcessRecord(home)
	if err != nil || gotRecord != record {
		t.Fatalf("process record = %+v, %v; want %+v", gotRecord, err, record)
	}
}

func TestDaemonStopSignalsOnlyTheHealthVerifiedProcessInstance(t *testing.T) {
	shortenDaemonWaits(t)
	home := t.TempDir()
	t.Setenv("BOARDLY_HOME", home)
	startedAt := time.Date(2026, time.August, 30, 9, 0, 0, 0, time.UTC)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(daemonpkg.HealthResponse{
			Status:    "running",
			PID:       os.Getpid(),
			StartedAt: startedAt,
		})
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	t.Setenv("BOARDLY_HEALTH_PORT", strconv.Itoa(port))

	writeRecord := func(value string) {
		t.Helper()
		if err := os.WriteFile(
			filepath.Join(home, daemonPidFileName),
			[]byte(value),
			0o600,
		); err != nil {
			t.Fatalf("write daemon process record: %v", err)
		}
	}
	writeRecord(`{"schemaVersion":1,"pid":` + strconv.Itoa(os.Getpid()) +
		`,"startedAt":"` + startedAt.Format(time.RFC3339Nano) + `"}`)
	if err := os.WriteFile(
		filepath.Join(home, "commerce-dev.json"),
		[]byte("invalid legacy Commerce control that daemon stop must not read"),
		0o600,
	); err != nil {
		t.Fatalf("seed unrelated Commerce control: %v", err)
	}
	previousSignal := signalDaemonProcess
	t.Cleanup(func() { signalDaemonProcess = previousSignal })
	var signaledPID int
	signalDaemonProcess = func(pid int) error {
		signaledPID = pid
		return nil
	}

	if _, err := executeCLI(t, newRootCmd, "daemon", "stop"); err != nil {
		t.Fatalf("daemon stop: %v", err)
	}
	if signaledPID != os.Getpid() {
		t.Fatalf("signaled PID = %d, want %d", signaledPID, os.Getpid())
	}

	signaledPID = 0
	writeRecord(`{"schemaVersion":1,"pid":` + strconv.Itoa(os.Getpid()) +
		`,"startedAt":"2026-08-30T08:59:59Z"}`)
	if _, err := executeCLI(t, newRootCmd, "daemon", "stop"); err == nil {
		t.Fatal("daemon stop accepted a stale process record")
	}
	if signaledPID != 0 {
		t.Fatalf("stale process record signaled PID %d", signaledPID)
	}
}
