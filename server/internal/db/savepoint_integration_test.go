//go:build integration

package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// The property the reconcilers depend on, against a real server: a statement
// that violates a unique index aborts the transaction, and only a savepoint
// makes the rest of that transaction — the part that records what the failure
// means — commit anyway.
func TestTryWithinSavepointKeepsTheTransactionUsableAfterAFailedStatement(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	const takenEmail = "savepoint-taken@example.com"
	const laterEmail = "savepoint-later@example.com"
	err = store.WithinRawTx(ctx, pgx.TxOptions{}, func(tx generated.DBTX, q *generated.Queries) error {
		if _, err := q.CreateUser(ctx, generated.CreateUserParams{
			Email: takenEmail, DisplayName: "First",
		}); err != nil {
			return fmt.Errorf("seed the address: %w", err)
		}
		attemptErr := store.TryWithinSavepoint(ctx, tx, func(q *generated.Queries) error {
			_, err := q.CreateUser(ctx, generated.CreateUserParams{
				Email: takenEmail, DisplayName: "Duplicate",
			})
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(attemptErr, &pgErr) || pgErr.Code != "23505" {
			return fmt.Errorf("duplicate insert error = %w, want SQLSTATE 23505", attemptErr)
		}
		// Without the savepoint this write raises 25P02 and the seeded user
		// above never commits either.
		if _, err := q.CreateUser(ctx, generated.CreateUserParams{
			Email: laterEmail, DisplayName: "After the failed attempt",
		}); err != nil {
			return fmt.Errorf("write after the failed attempt: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinRawTx() error = %v", err)
	}

	var addresses int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM users WHERE email = ANY($1::text[])`,
		[]string{takenEmail, laterEmail}).Scan(&addresses); err != nil {
		t.Fatalf("count committed users: %v", err)
	}
	if addresses != 2 {
		t.Fatalf("committed users = %d, want 2 — the transaction survived the failure", addresses)
	}
	var duplicates int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM users WHERE display_name = 'Duplicate'`).Scan(&duplicates); err != nil {
		t.Fatalf("count rolled-back users: %v", err)
	}
	if duplicates != 0 {
		t.Fatalf("rolled-back rows = %d, want 0", duplicates)
	}
}

// The savepoint is scoped to the attempt, not to the transaction: rolling back
// a failed attempt leaves everything written before it in place.
func TestTryWithinSavepointRollsBackOnlyTheAttempt(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	const keptEmail = "savepoint-kept@example.com"
	attemptFailure := errors.New("this attempt gives up")
	err = store.WithinRawTx(ctx, pgx.TxOptions{}, func(tx generated.DBTX, q *generated.Queries) error {
		if _, err := q.CreateUser(ctx, generated.CreateUserParams{
			Email: keptEmail, DisplayName: "Kept",
		}); err != nil {
			return fmt.Errorf("write before the attempt: %w", err)
		}
		attemptErr := store.TryWithinSavepoint(ctx, tx, func(q *generated.Queries) error {
			if _, err := q.CreateUser(ctx, generated.CreateUserParams{
				Email: "savepoint-discarded@example.com", DisplayName: "Discarded",
			}); err != nil {
				return err
			}
			return attemptFailure
		})
		if !errors.Is(attemptErr, attemptFailure) {
			return fmt.Errorf("attempt error = %w, want %w", attemptErr, attemptFailure)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinRawTx() error = %v", err)
	}

	var kept, discarded int
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE display_name = 'Kept'),
			count(*) FILTER (WHERE display_name = 'Discarded')
		FROM users`).Scan(&kept, &discarded); err != nil {
		t.Fatalf("count users around the savepoint: %v", err)
	}
	if kept != 1 || discarded != 0 {
		t.Fatalf("kept=%d discarded=%d, want 1 and 0", kept, discarded)
	}
}
