//go:build integration

package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestSessionAdvisoryLockElectsExactlyOneHolderAndTransfersAfterUnlock(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	first, acquired, err := store.TryAcquireSessionAdvisoryLock(
		ctx, "boardly/daemon/periodic-leader/v1", time.Minute,
	)
	if err != nil || !acquired || first == nil {
		t.Fatalf("first TryAcquireSessionAdvisoryLock() = %v, %t, %v", first, acquired, err)
	}
	defer func() { _ = first.Release(context.Background()) }()

	contender, acquired, err := store.TryAcquireSessionAdvisoryLock(
		ctx, "boardly/daemon/periodic-leader/v1", time.Minute,
	)
	if err != nil || acquired || contender != nil {
		t.Fatalf("contending TryAcquireSessionAdvisoryLock() = %v, %t, %v", contender, acquired, err)
	}
	if err := first.Release(ctx); err != nil {
		t.Fatalf("first Release() error = %v", err)
	}

	successor, acquired, err := store.TryAcquireSessionAdvisoryLock(
		ctx, "boardly/daemon/periodic-leader/v1", time.Minute,
	)
	if err != nil || !acquired || successor == nil {
		t.Fatalf("successor TryAcquireSessionAdvisoryLock() = %v, %t, %v", successor, acquired, err)
	}
	if err := successor.Release(ctx); err != nil {
		t.Fatalf("successor Release() error = %v", err)
	}
}

func TestSessionAdvisoryLockConnectionLossSignalsAndAllowsTakeover(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	decoy, acquired, err := store.TryAcquireSessionAdvisoryLock(
		ctx, "boardly/test/session-loss-decoy/v1", time.Minute,
	)
	if err != nil || !acquired || decoy == nil {
		t.Fatalf("acquire decoy SessionAdvisoryLock = %v, %t, %v", decoy, acquired, err)
	}
	defer func() { _ = decoy.Release(context.Background()) }()

	const lockKey = "boardly/test/session-loss/v1"
	lock, acquired, err := store.TryAcquireSessionAdvisoryLock(
		ctx, lockKey, time.Millisecond,
	)
	if err != nil || !acquired || lock == nil {
		t.Fatalf("TryAcquireSessionAdvisoryLock() = %v, %t, %v", lock, acquired, err)
	}
	var holderPID int32
	if err := pool.QueryRow(ctx, `
		SELECT pid
		FROM pg_locks
		WHERE locktype = 'advisory'
		  AND granted
		  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		  AND classid = ((hashtextextended($1::text, 0) >> 32) & 4294967295)::oid
		  AND objid = (hashtextextended($1::text, 0) & 4294967295)::oid
		  AND objsubid = 1
	`, lockKey).Scan(&holderPID); err != nil {
		t.Fatalf("find advisory-lock backend: %v", err)
	}
	var terminated bool
	if err := pool.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, holderPID).
		Scan(&terminated); err != nil ||
		!terminated {
		t.Fatalf("terminate advisory-lock backend = %t, %v", terminated, err)
	}
	select {
	case <-lock.Lost():
	case <-time.After(5 * time.Second):
		t.Fatal("Lost() did not close after backend termination")
	}
	if err := lock.Release(ctx); !errors.Is(err, ErrSessionAdvisoryLockLost) {
		t.Fatalf("Release(after loss) error = %v", err)
	}

	successor, acquired, err := store.TryAcquireSessionAdvisoryLock(
		ctx, lockKey, time.Minute,
	)
	if err != nil || !acquired || successor == nil {
		t.Fatalf("successor TryAcquireSessionAdvisoryLock() = %v, %t, %v", successor, acquired, err)
	}
	if err := successor.Release(ctx); err != nil {
		t.Fatalf("successor Release() error = %v", err)
	}
}
