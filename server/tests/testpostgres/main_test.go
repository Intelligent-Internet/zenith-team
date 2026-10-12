package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestRunWithDatabaseOwnsOneServerForTheWholeChildCommand(t *testing.T) {
	t.Parallel()

	var events []string
	server := databaseServer{
		url: "postgres://boardly:boardly@127.0.0.1:54321/boardly?sslmode=disable",
		close: func(context.Context) error {
			events = append(events, "close")
			return nil
		},
	}
	start := func(context.Context) (databaseServer, error) {
		events = append(events, "start")
		return server, nil
	}
	run := func(_ context.Context, args []string, databaseURL string) error {
		events = append(events, "run")
		if got, want := args, []string{"go", "test", "./..."}; !reflect.DeepEqual(got, want) {
			t.Fatalf("child args = %q, want %q", got, want)
		}
		if databaseURL != server.url {
			t.Fatalf("child database URL = %q, want %q", databaseURL, server.url)
		}
		return nil
	}

	if err := runWithDatabase(context.Background(), []string{"go", "test", "./..."}, start, run); err != nil {
		t.Fatalf("runWithDatabase() error = %v", err)
	}
	if got, want := events, []string{"start", "run", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle events = %q, want %q", got, want)
	}
}

func TestRunWithDatabaseClosesServerAfterChildFailure(t *testing.T) {
	t.Parallel()

	runErr := errors.New("child failed")
	closeErr := errors.New("cleanup failed")
	closed := false
	server := databaseServer{
		url: "postgres://shared",
		close: func(context.Context) error {
			closed = true
			return closeErr
		},
	}
	start := func(context.Context) (databaseServer, error) { return server, nil }
	run := func(context.Context, []string, string) error { return runErr }

	err := runWithDatabase(context.Background(), []string{"go", "test"}, start, run)
	if !closed {
		t.Fatal("server close was not called after child failure")
	}
	if !errors.Is(err, runErr) || !errors.Is(err, closeErr) {
		t.Fatalf("runWithDatabase() error = %v, want child and cleanup errors", err)
	}
}

func TestExitCodeIsPortable(t *testing.T) {
	t.Parallel()

	if got := exitCode(errors.New("not a child exit")); got != 1 {
		t.Fatalf("exitCode(non-exit error) = %d, want 1", got)
	}
	for _, test := range []struct {
		name string
		code int
		want int
	}{
		{name: "signaled", code: -1, want: 1},
		{name: "success is not an error", code: 0, want: 1},
		{name: "ordinary failure", code: 7, want: 7},
		{name: "non-portable", code: 126, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := portableExitCode(test.code); got != test.want {
				t.Fatalf("portableExitCode(%d) = %d, want %d", test.code, got, test.want)
			}
		})
	}
}

func TestCommandEnvironmentReplacesSharedDatabaseURL(t *testing.T) {
	t.Parallel()

	base := []string{
		"PATH=/usr/bin",
		"BOARDLY_TEST_DATABASE_URL=postgres://stale",
		"CI=true",
	}
	got := commandEnvironment(base, "postgres://shared")
	want := []string{
		"PATH=/usr/bin",
		"BOARDLY_TEST_DATABASE_URL=postgres://shared",
		"CI=true",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commandEnvironment() = %q, want %q", got, want)
	}
}
