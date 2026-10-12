package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSessionAdvisoryLockReleasesOnlyAfterConfirmedUnlock(t *testing.T) {
	t.Parallel()

	connection := &fakeSessionLockConnection{tryAcquired: true, unlockMatched: true}
	lock, acquired, err := acquireSessionAdvisoryLock(
		context.Background(), connection, "boardly/daemon/periodic-leader/v1", time.Minute,
	)
	if err != nil || !acquired || lock == nil {
		t.Fatalf("acquireSessionAdvisoryLock() = %v, %t, %v", lock, acquired, err)
	}
	if err := lock.Release(context.Background()); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := lock.Release(context.Background()); err != nil {
		t.Fatalf("second Release() error = %v", err)
	}
	connection.assertTerminal(t, 1, 1, 0)
	select {
	case <-lock.Lost():
		t.Fatal("Lost() closed after an ordinary release")
	default:
	}
}

func TestSessionAdvisoryLockDestroysUncertainUnlockConnection(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		unlockMatched bool
		unlockErr     error
	}{
		{name: "lock no longer held"},
		{name: "transport uncertainty", unlockErr: errors.New("secret transport detail")},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection := &fakeSessionLockConnection{
				tryAcquired: true, unlockMatched: test.unlockMatched, unlockErr: test.unlockErr,
			}
			lock, acquired, err := acquireSessionAdvisoryLock(
				context.Background(), connection, "boardly/daemon/periodic-leader/v1", time.Minute,
			)
			if err != nil || !acquired {
				t.Fatalf("acquireSessionAdvisoryLock() = %v, %t, %v", lock, acquired, err)
			}
			if err := lock.Release(
				context.Background(),
			); !errors.Is(
				err,
				ErrSessionAdvisoryUnlockUncertain,
			) {
				t.Fatalf("Release() error = %v", err)
			}
			connection.assertTerminal(t, 1, 0, 1)
		})
	}
}

func TestSessionAdvisoryLockReportsMonitoredConnectionLoss(t *testing.T) {
	t.Parallel()

	ping := make(chan struct{})
	connection := &fakeSessionLockConnection{
		tryAcquired:   true,
		unlockMatched: true,
		ping: func(context.Context) error {
			<-ping
			return errors.New("secret lost connection detail")
		},
	}
	lock, acquired, err := acquireSessionAdvisoryLock(
		context.Background(), connection, "boardly/daemon/periodic-leader/v1", time.Millisecond,
	)
	if err != nil || !acquired {
		t.Fatalf("acquireSessionAdvisoryLock() = %v, %t, %v", lock, acquired, err)
	}
	close(ping)
	select {
	case <-lock.Lost():
	case <-time.After(time.Second):
		t.Fatal("Lost() did not close")
	}
	if err := lock.Release(context.Background()); !errors.Is(err, ErrSessionAdvisoryLockLost) {
		t.Fatalf("Release() error = %v", err)
	}
	connection.assertTerminal(t, 0, 0, 1)
}

func TestSessionAdvisoryLockReleaseCancellationIsNotConnectionLoss(t *testing.T) {
	t.Parallel()

	monitorStarted := make(chan struct{})
	connection := &fakeSessionLockConnection{
		tryAcquired:   true,
		unlockMatched: true,
		ping: func(ctx context.Context) error {
			close(monitorStarted)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	lock, acquired, err := acquireSessionAdvisoryLock(
		context.Background(), connection, "boardly/daemon/periodic-leader/v1", time.Millisecond,
	)
	if err != nil || !acquired {
		t.Fatalf("acquireSessionAdvisoryLock() = %v, %t, %v", lock, acquired, err)
	}
	select {
	case <-monitorStarted:
	case <-time.After(time.Second):
		t.Fatal("monitor did not start")
	}
	if err := lock.Release(context.Background()); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	connection.assertTerminal(t, 1, 1, 0)
	select {
	case <-lock.Lost():
		t.Fatal("Lost() closed for explicit monitor cancellation")
	default:
	}
}

func TestSessionAdvisoryLockReturnsUnacquiredConnectionsToPool(t *testing.T) {
	t.Parallel()

	connection := &fakeSessionLockConnection{}
	lock, acquired, err := acquireSessionAdvisoryLock(
		context.Background(), connection, "boardly/daemon/periodic-leader/v1", time.Second,
	)
	if err != nil || acquired || lock != nil {
		t.Fatalf("acquireSessionAdvisoryLock() = %v, %t, %v", lock, acquired, err)
	}
	connection.assertTerminal(t, 0, 1, 0)
}

func TestSessionAdvisoryLockDestroysConnectionWhenAcquisitionIsUncertain(t *testing.T) {
	t.Parallel()

	connection := &fakeSessionLockConnection{tryErr: errors.New("secret acquisition detail")}
	lock, acquired, err := acquireSessionAdvisoryLock(
		context.Background(), connection, "boardly/daemon/periodic-leader/v1", time.Second,
	)
	if !errors.Is(err, ErrSessionAdvisoryLockUnavailable) || acquired || lock != nil {
		t.Fatalf("acquireSessionAdvisoryLock() = %v, %t, %v", lock, acquired, err)
	}
	connection.assertTerminal(t, 0, 0, 1)
}

func TestSessionAdvisoryLockRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	valid := &fakeSessionLockConnection{}
	for _, test := range []struct {
		name       string
		ctx        context.Context
		connection sessionLockConnection
		key        string
		interval   time.Duration
	}{
		{name: "nil context", connection: valid, key: "boardly/test", interval: time.Second},
		{name: "nil connection", ctx: context.Background(), key: "boardly/test", interval: time.Second},
		{name: "short interval", ctx: context.Background(), connection: valid, key: "boardly/test", interval: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if lock, acquired, err := acquireSessionAdvisoryLock(
				test.ctx,
				test.connection,
				test.key,
				test.interval,
			); err == nil || acquired ||
				lock != nil {
				t.Fatalf("acquireSessionAdvisoryLock() = %v, %t, %v", lock, acquired, err)
			}
		})
	}
}

type fakeSessionLockConnection struct {
	mu            sync.Mutex
	tryAcquired   bool
	tryErr        error
	unlockMatched bool
	unlockErr     error
	ping          func(context.Context) error
	unlockCalls   int
	releaseCalls  int
	destroyCalls  int
}

func (c *fakeSessionLockConnection) tryLock(context.Context, string) (bool, error) {
	return c.tryAcquired, c.tryErr
}

func (c *fakeSessionLockConnection) unlock(context.Context, string) (bool, error) {
	c.mu.Lock()
	c.unlockCalls++
	c.mu.Unlock()
	return c.unlockMatched, c.unlockErr
}

func (c *fakeSessionLockConnection) monitor(ctx context.Context) error {
	if c.ping == nil {
		return nil
	}
	return c.ping(ctx)
}

func (c *fakeSessionLockConnection) release() {
	c.mu.Lock()
	c.releaseCalls++
	c.mu.Unlock()
}

func (c *fakeSessionLockConnection) destroy() {
	c.mu.Lock()
	c.destroyCalls++
	c.mu.Unlock()
}

func (c *fakeSessionLockConnection) assertTerminal(
	t *testing.T,
	wantUnlock int,
	wantRelease int,
	wantDestroy int,
) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unlockCalls != wantUnlock || c.releaseCalls != wantRelease ||
		c.destroyCalls != wantDestroy {
		t.Fatalf("terminal calls unlock/release/destroy = %d/%d/%d, want %d/%d/%d",
			c.unlockCalls, c.releaseCalls, c.destroyCalls,
			wantUnlock, wantRelease, wantDestroy)
	}
}
