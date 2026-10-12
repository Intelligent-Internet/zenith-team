package db

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrSessionAdvisoryLockUnavailable = errors.New("session advisory lock unavailable")
	ErrSessionAdvisoryLockLost        = errors.New("session advisory lock connection lost")
	ErrSessionAdvisoryUnlockUncertain = errors.New("session advisory lock unlock uncertain")
)

// SessionAdvisoryLock owns one session-scoped PostgreSQL advisory lock on one
// dedicated pool connection. Release must be called before the Store closes.
// Lost closes only when monitoring proves the owning connection failed.
type SessionAdvisoryLock struct {
	connection  sessionLockConnection
	key         string
	cancel      context.CancelFunc
	monitorDone chan struct{}
	lost        chan struct{}

	releaseOnce sync.Once
	releaseErr  error
}

type sessionLockConnection interface {
	tryLock(context.Context, string) (bool, error)
	unlock(context.Context, string) (bool, error)
	monitor(context.Context) error
	release()
	destroy()
}

type pooledSessionLockConnection struct {
	connection *pgxpool.Conn
}

// TryAcquireSessionAdvisoryLock attempts one nonblocking session lock. A false
// acquired result is ordinary leader contention, not an error.
func (s *Store) TryAcquireSessionAdvisoryLock(
	ctx context.Context,
	key string,
	monitorInterval time.Duration,
) (*SessionAdvisoryLock, bool, error) {
	// ctx and the interval are checked before Acquire: a nil ctx panics inside
	// the pool, and a non-positive interval panics time.NewTicker in the
	// monitor goroutine. Checking after Acquire would leak the connection.
	if s == nil || s.pool == nil || ctx == nil || monitorInterval <= 0 {
		return nil, false, ErrSessionAdvisoryLockUnavailable
	}
	connection, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, ErrSessionAdvisoryLockUnavailable
	}
	return acquireSessionAdvisoryLock(
		ctx,
		&pooledSessionLockConnection{connection: connection},
		key,
		monitorInterval,
	)
}

func acquireSessionAdvisoryLock(
	ctx context.Context,
	connection sessionLockConnection,
	key string,
	monitorInterval time.Duration,
) (*SessionAdvisoryLock, bool, error) {
	if ctx == nil || connection == nil || monitorInterval <= 0 {
		return nil, false, ErrSessionAdvisoryLockUnavailable
	}
	acquired, err := connection.tryLock(ctx, key)
	if err != nil {
		// A transport failure cannot prove whether PostgreSQL acquired the
		// session lock. Destroy the session rather than return it to the pool.
		connection.destroy()
		return nil, false, ErrSessionAdvisoryLockUnavailable
	}
	if !acquired {
		connection.release()
		return nil, false, nil
	}

	monitorCtx, cancel := context.WithCancel(context.Background())
	lock := &SessionAdvisoryLock{
		connection: connection, key: key, cancel: cancel,
		monitorDone: make(chan struct{}), lost: make(chan struct{}),
	}
	go lock.monitor(monitorCtx, monitorInterval)
	return lock, true, nil
}

func (l *SessionAdvisoryLock) monitor(ctx context.Context, interval time.Duration) {
	defer close(l.monitorDone)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := l.connection.monitor(ctx); err == nil {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		// Only this goroutine ever closes l.lost, and it returns immediately
		// after, so the close needs no re-entrancy guard.
		close(l.lost)
		l.connection.destroy()
		return
	}
}

// Lost is closed after the dedicated PostgreSQL session fails. An ordinary
// explicit Release leaves it open so callers can distinguish shutdown from
// leadership loss.
func (l *SessionAdvisoryLock) Lost() <-chan struct{} {
	if l == nil || l.lost == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return l.lost
}

// Release stops monitoring, positively unlocks the exact owning session, and
// only then returns it to the pool. A false or uncertain unlock destroys the
// session so no hidden session lock can escape into ordinary pool traffic.
func (l *SessionAdvisoryLock) Release(ctx context.Context) error {
	if l == nil || l.cancel == nil || l.monitorDone == nil || l.connection == nil {
		return ErrSessionAdvisoryUnlockUncertain
	}
	if ctx == nil {
		ctx = context.Background()
	}
	l.releaseOnce.Do(func() {
		l.cancel()
		<-l.monitorDone

		// <-l.monitorDone above establishes happens-before with the monitor
		// goroutine's close, so this read is not racy.
		lost := false
		select {
		case <-l.lost:
			lost = true
		default:
		}
		if lost {
			l.releaseErr = ErrSessionAdvisoryLockLost
			return
		}
		matched, err := l.connection.unlock(ctx, l.key)
		if err != nil || !matched {
			l.connection.destroy()
			l.releaseErr = ErrSessionAdvisoryUnlockUncertain
			return
		}
		l.connection.release()
	})
	return l.releaseErr
}

func (c *pooledSessionLockConnection) tryLock(ctx context.Context, key string) (bool, error) {
	var acquired bool
	err := c.connection.QueryRow(ctx,
		"SELECT pg_try_advisory_lock(hashtextextended($1::text, 0))", key,
	).Scan(&acquired)
	return acquired, err
}

func (c *pooledSessionLockConnection) unlock(ctx context.Context, key string) (bool, error) {
	var matched bool
	err := c.connection.QueryRow(ctx,
		"SELECT pg_advisory_unlock(hashtextextended($1::text, 0))", key,
	).Scan(&matched)
	return matched, err
}

func (c *pooledSessionLockConnection) monitor(ctx context.Context) error {
	return c.connection.Conn().Ping(ctx)
}

func (c *pooledSessionLockConnection) release() {
	c.connection.Release()
}

func (c *pooledSessionLockConnection) destroy() {
	hijacked := c.connection.Hijack()
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hijacked.Close(closeCtx)
}
