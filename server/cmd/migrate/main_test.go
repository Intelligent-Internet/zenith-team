package main

import (
	"os"
	"strings"
	"testing"
)

// The run seam takes its environment and output as arguments so these cases
// need neither a database nor process-global state.
func newTestOutput(t *testing.T) *os.File {
	t.Helper()

	file, err := os.CreateTemp(t.TempDir(), "migrate-log-*")
	if err != nil {
		t.Fatalf("create temp log: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func emptyEnv(string) (string, bool) { return "", false }

func TestRunRequiresTheDatabaseURL(t *testing.T) {
	t.Parallel()

	err := run(nil, emptyEnv, newTestOutput(t))

	if err == nil || !strings.Contains(err.Error(), "APP_DATABASE_URL is required") {
		t.Fatalf("run() error = %v, want APP_DATABASE_URL requirement", err)
	}
}

// Rolling back drops business data, so the destructive direction must not be
// reachable by a single flag typo.
func TestRunRefusesDownWithoutExplicitConfirmation(t *testing.T) {
	t.Parallel()

	env := func(name string) (string, bool) {
		if name == databaseURLVariable {
			return "postgres://zenith_team:zenith_team@127.0.0.1:1/zenith_team", true
		}
		return "", false
	}

	err := run([]string{"-direction=down"}, env, newTestOutput(t))

	if err == nil || !strings.Contains(err.Error(), "confirm-down") {
		t.Fatalf("run() error = %v, want a confirmation requirement", err)
	}
}

func TestRunRejectsAnUnknownDirection(t *testing.T) {
	t.Parallel()

	env := func(name string) (string, bool) {
		if name == databaseURLVariable {
			return "postgres://zenith_team:zenith_team@127.0.0.1:1/zenith_team", true
		}
		return "", false
	}

	err := run([]string{"-direction=sideways"}, env, newTestOutput(t))

	if err == nil || !strings.Contains(err.Error(), "unknown -direction") {
		t.Fatalf("run() error = %v, want an unknown-direction error", err)
	}
}
