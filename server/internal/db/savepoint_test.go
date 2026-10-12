package db

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// recordingTx is a generated.DBTX that remembers the statements issued against
// it and can be told to fail one of them. It answers the savepoint bracket
// without a database, which is the whole of what these tests check: which
// statements TryWithinSavepoint sends, in which order, and what it returns.
type recordingTx struct {
	statements []string
	failures   map[string]error
}

func (t *recordingTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	t.statements = append(t.statements, sql)
	if err, failing := t.failures[sql]; failing {
		return pgconn.CommandTag{}, err
	}
	return pgconn.CommandTag{}, nil
}

func (t *recordingTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("recordingTx issues no queries")
}

func (t *recordingTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}

const (
	openSavepoint     = "SAVEPOINT boardly_attempt"
	rollbackSavepoint = "ROLLBACK TO SAVEPOINT boardly_attempt"
	releaseSavepoint  = "RELEASE SAVEPOINT boardly_attempt"
)

func TestTryWithinSavepointReleasesAnAttemptThatSucceeded(t *testing.T) {
	t.Parallel()

	tx := &recordingTx{}
	store := &Store{queries: generated.New(tx)}
	called := 0
	if err := store.TryWithinSavepoint(context.Background(), tx, func(q *generated.Queries) error {
		called++
		if q == nil {
			t.Error("TryWithinSavepoint() supplied nil queries")
		}
		return nil
	}); err != nil {
		t.Fatalf("TryWithinSavepoint() error = %v", err)
	}
	if called != 1 {
		t.Fatalf("attempt ran %d times, want 1", called)
	}
	want := []string{openSavepoint, releaseSavepoint}
	if !slices.Equal(tx.statements, want) {
		t.Fatalf("statements = %q, want %q", tx.statements, want)
	}
}

// The point of the helper: a failed attempt is rolled back to the savepoint, so
// whatever the caller writes next runs on a transaction PostgreSQL still
// accepts statements on.
func TestTryWithinSavepointRollsBackAFailedAttempt(t *testing.T) {
	t.Parallel()

	attemptErr := errors.New("the unique index rejected this insert")
	tx := &recordingTx{}
	store := &Store{queries: generated.New(tx)}
	err := store.TryWithinSavepoint(context.Background(), tx, func(*generated.Queries) error {
		return attemptErr
	})
	// Returned as it arrived, not wrapped in savepoint machinery: the caller
	// classifies this error against its own sentinels to decide what to record.
	if !errors.Is(err, attemptErr) || errors.Unwrap(err) != nil {
		t.Fatalf("TryWithinSavepoint() error = %v, want the attempt's own error unwrapped", err)
	}
	// The trailing RELEASE destroys the marker: ROLLBACK TO leaves a savepoint
	// established, and a later attempt in the same transaction must start clean
	// rather than stacking one subtransaction per try.
	want := []string{openSavepoint, rollbackSavepoint, releaseSavepoint}
	if !slices.Equal(tx.statements, want) {
		t.Fatalf("statements = %q, want %q", tx.statements, want)
	}
}

// A rollback that itself fails leaves the transaction aborted, and the one
// thing the return value must NOT do is still match the attempt's sentinel:
// the caller's decision to keep writing is conditioned on the rollback having
// worked. The attempt error survives as text only; the rollback failure is
// what classifies.
func TestTryWithinSavepointDoesNotClassifyTheAttemptWhenRollbackFails(t *testing.T) {
	t.Parallel()

	attemptErr := errors.New("the unique index rejected this insert")
	rollbackErr := errors.New("connection lost")
	tx := &recordingTx{failures: map[string]error{rollbackSavepoint: rollbackErr}}
	store := &Store{queries: generated.New(tx)}
	err := store.TryWithinSavepoint(context.Background(), tx, func(*generated.Queries) error {
		return attemptErr
	})
	if errors.Is(err, attemptErr) {
		t.Fatalf(
			"TryWithinSavepoint() error = %v; the attempt sentinel must not classify past a failed rollback",
			err,
		)
	}
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("TryWithinSavepoint() error = %v, want the rollback failure matchable", err)
	}
	if !strings.Contains(err.Error(), attemptErr.Error()) {
		t.Fatalf("TryWithinSavepoint() error = %v, want the attempt preserved as text", err)
	}
}

// A savepoint that cannot be opened is not a recoverable attempt: the statement
// must not run at all, because a failure would then abort the transaction with
// nothing to roll back to.
func TestTryWithinSavepointSkipsTheAttemptWhenTheSavepointCannotOpen(t *testing.T) {
	t.Parallel()

	openErr := errors.New("no transaction is in progress")
	tx := &recordingTx{failures: map[string]error{openSavepoint: openErr}}
	store := &Store{queries: generated.New(tx)}
	called := 0
	err := store.TryWithinSavepoint(context.Background(), tx, func(*generated.Queries) error {
		called++
		return nil
	})
	if !errors.Is(err, openErr) {
		t.Fatalf("TryWithinSavepoint() error = %v, want %v", err, openErr)
	}
	if called != 0 {
		t.Fatalf("attempt ran %d times, want 0", called)
	}
	want := []string{openSavepoint}
	if !slices.Equal(tx.statements, want) {
		t.Fatalf("statements = %q, want %q", tx.statements, want)
	}
}

// A release that fails means the attempt's work is in an unknown state, which
// is an error even though the attempt itself succeeded.
func TestTryWithinSavepointReportsAFailedRelease(t *testing.T) {
	t.Parallel()

	releaseErr := errors.New("connection lost")
	tx := &recordingTx{failures: map[string]error{releaseSavepoint: releaseErr}}
	store := &Store{queries: generated.New(tx)}
	err := store.TryWithinSavepoint(context.Background(), tx, func(*generated.Queries) error {
		return nil
	})
	if !errors.Is(err, releaseErr) {
		t.Fatalf("TryWithinSavepoint() error = %v, want %v", err, releaseErr)
	}
}

func TestTryWithinSavepointRejectsMissingArguments(t *testing.T) {
	t.Parallel()

	tx := &recordingTx{}
	store := &Store{queries: generated.New(tx)}
	noop := func(*generated.Queries) error { return nil }
	for name, call := range map[string]func() error{
		"nil store": func() error {
			return (*Store)(nil).TryWithinSavepoint(context.Background(), tx, noop)
		},
		"nil transaction handle": func() error {
			return store.TryWithinSavepoint(context.Background(), nil, noop)
		},
		"nil callback": func() error {
			return store.TryWithinSavepoint(context.Background(), tx, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("TryWithinSavepoint() error = nil, want error")
			}
		})
	}
	if len(tx.statements) != 0 {
		t.Fatalf("rejected calls issued %q, want no statements", tx.statements)
	}
}
