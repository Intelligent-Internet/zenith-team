package dbtest

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestOpenDatabaseRequiresSharedServer(t *testing.T) {
	t.Setenv(SharedDatabaseURLEnv, "")

	_, err := OpenDatabase(context.Background())
	if !errors.Is(err, ErrSharedDatabaseUnavailable) {
		t.Fatalf("OpenDatabase() error = %v, want ErrSharedDatabaseUnavailable", err)
	}
}

func TestOpenDatabaseDoesNotClassifyInvalidSharedServerAsUnavailable(t *testing.T) {
	t.Setenv(SharedDatabaseURLEnv, "mysql://zenith-team:zenith-team@127.0.0.1/zenith-team")

	_, err := OpenDatabase(context.Background())
	if err == nil {
		t.Fatal("OpenDatabase() error = nil, want unsupported scheme error")
	}
	if errors.Is(err, ErrSharedDatabaseUnavailable) {
		t.Fatalf("OpenDatabase() error = %v, must not be classified as unavailable", err)
	}
}

func TestClosePoolHonorsContext(t *testing.T) {
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := closePool(ctx, func() { <-release })
	close(release)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("closePool() error = %v, want context canceled", err)
	}
}

func TestIsolatedDatabaseNameIsSafeAndDeterministic(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse("019c9f4e-809c-7f63-bb01-fb2533917798")
	if got, want := isolatedDatabaseName(id), "zenith_team_test_019c9f4e809c7f63bb01fb2533917798"; got != want {
		t.Fatalf("isolatedDatabaseName() = %q, want %q", got, want)
	}
}

func TestDatabaseURLForNamePreservesConnectionOptions(t *testing.T) {
	t.Parallel()

	got, err := databaseURLForName(
		"postgres://zenith_team:zenith_team@127.0.0.1:54321/zenith_team?sslmode=disable&connect_timeout=5",
		"zenith_team_test_123",
	)
	if err != nil {
		t.Fatalf("databaseURLForName() error = %v", err)
	}
	const want = "postgres://zenith_team:zenith_team@127.0.0.1:54321/zenith_team_test_123?sslmode=disable&connect_timeout=5"
	if got != want {
		t.Fatalf("databaseURLForName() = %q, want %q", got, want)
	}
}
