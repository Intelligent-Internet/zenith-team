// Package db owns the PostgreSQL connection pool, the migration runner, the
// session advisory lock, readiness reporting, and the transaction helpers the
// domain services use: WithinTx for ordinary domain mutations, WithinRawTx for
// the few flows that also need the raw statement handle, and TryWithinSavepoint
// for a statement inside one of those transactions that is allowed to fail
// without ending it. WithinTx and WithinRawTx take explicit pgx.TxOptions; most
// callers take the server default, and the ones that need a consistent
// multi-statement snapshot say so at the call site.
//
// It holds no domain policy. A query that decides product behavior lives in
// the owning domain package as an exported function taking the
// *generated.Queries handle this package hands out (activity.Insert,
// inbox.CreateInTx, run.AuthorizeElectedRuntimeInTx) — the caller owns the
// transaction.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type Store struct {
	pool    *pgxpool.Pool
	queries *generated.Queries
}

// ErrCommitUnknown marks a failed Commit whose outcome PostgreSQL did not
// confirm. Callers that already performed provider I/O may reconcile durable
// state instead of blindly retrying the external operation.
var ErrCommitUnknown = errors.New("database transaction commit outcome is unknown")

func NewStore(pool *pgxpool.Pool) (*Store, error) {
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	return &Store{
		pool:    pool,
		queries: generated.New(pool),
	}, nil
}

// Queries returns the non-transactional query set. Statements that must see one
// snapshot or commit together — read or write — belong in WithinTx.
func (s *Store) Queries() *generated.Queries {
	return s.queries
}

// WithinTx runs fn inside one transaction and hands it a *generated.Queries
// bound to that transaction. A cross-domain write takes that handle as a
// parameter and never decides which it received — the caller does, by passing
// either Store.Queries() or the handle WithinTx yields. That is what lets
// several domains write inside one commit without any of them owning the
// transaction.
//
// The handle is deliberately the concrete query set rather than a per-domain
// interface. An interface would have to be narrowed per caller, and Go has no
// interface widening: the moment one domain handed its narrowed handle to
// another, the receiver would have to type-assert back to *generated.Queries —
// exactly the runtime failure this design replaced (go-shape pins that grep at
// zero). The cost is honest: the handle exposes the whole query set, so table
// ownership is a review convention, not a compiler-enforced boundary.
func (s *Store) WithinTx(
	ctx context.Context,
	options pgx.TxOptions,
	fn func(*generated.Queries) error,
) error {
	if fn == nil {
		return errors.New("transaction callback is required")
	}
	return s.WithinRawTx(ctx, options, func(_ generated.DBTX, queries *generated.Queries) error {
		return fn(queries)
	})
}

// WithinRawTx is WithinTx plus the raw statement handle, for the flows that
// interleave hand-written SQL with generated queries inside one commit. The
// handle is generated.DBTX and not pgx.Tx on purpose: the callback runs
// statements, it does not get to commit or roll back the transaction this
// method owns.
func (s *Store) WithinRawTx(
	ctx context.Context,
	options pgx.TxOptions,
	fn func(generated.DBTX, *generated.Queries) error,
) (returnErr error) {
	if s == nil || s.pool == nil || s.queries == nil {
		return errors.New("database store is not initialized")
	}
	if fn == nil {
		return errors.New("raw transaction callback is required")
	}
	tx, err := s.pool.BeginTx(ctx, options)
	if err != nil {
		return fmt.Errorf("begin database transaction: %w", err)
	}
	// One deferred rollback covers the callback-error, panic, and post-commit
	// paths. pgx closes the transaction before returning from Commit, so after
	// either a successful or a failed commit this reports pgx.ErrTxClosed and
	// issues no statement.
	defer func() {
		if rollbackErr := rollbackDatabaseTransaction(tx); rollbackErr != nil {
			returnErr = errors.Join(returnErr, rollbackErr)
		}
	}()

	if err := fn(tx, s.queries.WithTx(tx)); err != nil {
		return fmt.Errorf("run database transaction: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		commitErr := fmt.Errorf("commit database transaction: %w", err)
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return commitErr
		}
		return errors.Join(ErrCommitUnknown, commitErr)
	}
	return nil
}

// attemptSavepointName is the only savepoint this package takes. A savepoint
// name is an SQL identifier, so it cannot be a bind parameter; keeping it a
// constant is what keeps caller-supplied text out of the statement. The fixed
// name means attempts must NOT nest: ROLLBACK TO does not destroy a savepoint,
// so an inner attempt's leftover marker would capture an outer ROLLBACK TO of
// the same name and silently undo too little. Every attempt therefore ends by
// destroying its marker (RELEASE on both paths below), and there is exactly
// one attempt in flight per transaction.
const attemptSavepointName = "boardly_attempt"

// TryWithinSavepoint runs fn behind a savepoint on the transaction tx already
// belongs to, and returns fn's error unchanged so the caller can classify it.
//
// The savepoint is what makes a failure recoverable. PostgreSQL aborts the
// WHOLE transaction on any statement error: every later statement fails with
// 25P02 until the transaction ends. So a caller that treats one particular
// failure as an expected outcome — losing a race on a unique index, say — and
// wants to record what it decided cannot simply catch the error and write the
// note, because that write would fail too. A savepoint is the marker to roll
// back to: after ROLLBACK TO SAVEPOINT the transaction is healthy again and the
// caller's outcome write commits together with everything that came before the
// attempt.
//
// This is not commit or rollback control over the transaction WithinRawTx owns,
// for the same reason that method hands out generated.DBTX instead of pgx.Tx:
// the savepoint opens and closes inside this call, and all the caller chooses
// is which statements run behind it.
func (s *Store) TryWithinSavepoint(
	ctx context.Context,
	tx generated.DBTX,
	fn func(*generated.Queries) error,
) error {
	if s == nil {
		return errors.New("database store is not initialized")
	}
	if tx == nil {
		return errors.New("savepoint transaction handle is required")
	}
	if fn == nil {
		return errors.New("savepoint callback is required")
	}
	if _, err := tx.Exec(ctx, "SAVEPOINT "+attemptSavepointName); err != nil {
		return fmt.Errorf("open savepoint: %w", err)
	}
	if err := fn(generated.New(tx)); err != nil {
		// Returned as it arrived: the caller matches it against its own
		// sentinels, and the savepoint is how the transaction survives the
		// failure, not part of what the failure was.
		//
		// Unless the rollback itself fails. Then the transaction is still
		// aborted, and the one thing this return value must NOT do is match
		// the caller's expected-outcome sentinel — the caller's decision to
		// keep writing is conditioned on the transaction being healthy again.
		// The attempt error survives only as text.
		if rollbackErr := rollbackToSavepoint(tx); rollbackErr != nil {
			// The %v on the attempt is the contract, not an oversight:
			// errors.Is against the caller's sentinel has to fail here.
			return fmt.Errorf(
				"savepoint rollback failed (attempt error: %v): %w",
				err, rollbackErr, //nolint:errorlint // attempt stays text
			)
		}
		return err
	}
	if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT "+attemptSavepointName); err != nil {
		return fmt.Errorf("release savepoint: %w", err)
	}
	return nil
}

// rollbackToSavepoint discards whatever the failed attempt did and leaves the
// transaction usable again. It runs on its own deadline on a context that
// cannot be cancelled, for the reason rollbackDatabaseTransaction gives below:
// pgx kills the connection when a statement runs on an already-cancelled
// context, and this statement runs exactly when something has just gone wrong.
func rollbackToSavepoint(tx generated.DBTX) error {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := tx.Exec(rollbackCtx, "ROLLBACK TO SAVEPOINT "+attemptSavepointName); err != nil {
		return fmt.Errorf("roll back to savepoint: %w", err)
	}
	// ROLLBACK TO leaves the savepoint established; RELEASE destroys it, so a
	// later attempt in the same transaction starts clean instead of stacking a
	// subtransaction per try.
	if _, err := tx.Exec(rollbackCtx, "RELEASE SAVEPOINT "+attemptSavepointName); err != nil {
		return fmt.Errorf("release savepoint after rollback: %w", err)
	}
	return nil
}

// rollbackDatabaseTransaction rolls back on its own deadline on a context that
// cannot be cancelled, deliberately not the caller's. pgx kills the connection
// when a rollback runs on an already-cancelled context, so reusing the caller's
// context would destroy a pooled connection on every client disconnect —
// draining the pool exactly when the server is already shedding load.
// pgx.ErrTxClosed means the transaction already finished and is not an error.
func rollbackDatabaseTransaction(tx pgx.Tx) error {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := tx.Rollback(rollbackCtx)
	if err == nil || errors.Is(err, pgx.ErrTxClosed) {
		return nil
	}
	return fmt.Errorf("rollback database transaction: %w", err)
}
