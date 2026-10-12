package db

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// pingFunc adapts a function to Pinger for tests. The package no longer
// exports this shape: there is exactly one required dependency in production
// and it satisfies Pinger directly.
type pingFunc func(context.Context) error

func (fn pingFunc) Ping(ctx context.Context) error { return fn(ctx) }

func TestReadinessOwnsDependencyTimeout(t *testing.T) {
	t.Parallel()

	checker, err := NewReadiness(&fakeDatabase{version: SchemaVersion}, nil)
	if err != nil {
		t.Fatalf("NewReadiness() error = %v", err)
	}
	if checker.dependencyTimeout != 2*time.Second {
		t.Fatalf("dependency timeout = %s, want 2s", checker.dependencyTimeout)
	}

	timedOutDB := &fakeDatabase{
		version: SchemaVersion,
		ping: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	checker, err = newReadinessWithTiming(timedOutDB, nil, readinessTiming{
		dependencyTimeout: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("newReadinessWithTiming() error = %v", err)
	}
	if err := checker.Ping(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Ping() error = %v, want context deadline exceeded", err)
	}
}

func TestReadinessTimeoutDoesNotDependOnDependencyContextCompliance(t *testing.T) {
	t.Parallel()

	pingStarted := make(chan struct{})
	releasePing := make(chan struct{})
	pingReturned := make(chan struct{})
	var startOnce sync.Once
	db := &fakeDatabase{
		version: SchemaVersion,
		ping: func(context.Context) error {
			startOnce.Do(func() { close(pingStarted) })
			<-releasePing
			select {
			case <-pingReturned:
			default:
				close(pingReturned)
			}
			return nil
		},
	}
	checker, err := newReadinessWithTiming(db, nil, readinessTiming{
		dependencyTimeout: 10 * time.Millisecond,
		successCacheTTL:   time.Second,
		now:               time.Now,
	})
	if err != nil {
		t.Fatalf("newReadinessWithTiming() error = %v", err)
	}
	result := make(chan error, 1)
	go func() { result <- checker.Ping(context.Background()) }()
	<-pingStarted
	if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Ping() error = %v, want owned deadline", err)
	}
	close(releasePing)
	select {
	case <-pingReturned:
	case <-time.After(time.Second):
		t.Fatal("context-ignoring fake dependency did not exit after release")
	}
	deadline := time.Now().Add(time.Second)
	for {
		checker.mu.Lock()
		finished := checker.inFlight == nil
		checker.mu.Unlock()
		if finished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed-out dependency check did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if err := checker.Ping(context.Background()); err != nil {
		t.Fatalf("fresh Ping() error = %v", err)
	}
	if got := db.pingCalls.Load(); got != 2 {
		t.Fatalf("database Ping() calls = %d, want late success not cached", got)
	}
}

func TestReadinessRequiresHealthyDatabaseAndCurrentCleanSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		db      *fakeDatabase
		wantErr string
	}{
		{name: "ready", db: &fakeDatabase{version: SchemaVersion}},
		{
			name:    "database unavailable",
			db:      &fakeDatabase{pingErr: errors.New("down")},
			wantErr: "ping PostgreSQL",
		},
		{
			name:    "schema unavailable",
			db:      &fakeDatabase{rowErr: errors.New("missing")},
			wantErr: "read migration status",
		},
		{name: "wrong schema", db: &fakeDatabase{version: SchemaVersion - 1}, wantErr: "migration version"},
		{name: "dirty schema", db: &fakeDatabase{version: SchemaVersion, dirty: true}, wantErr: "dirty"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			checker, err := NewReadiness(test.db, nil)
			if err != nil {
				t.Fatalf("NewReadiness() error = %v", err)
			}
			err = checker.Ping(context.Background())
			if test.wantErr == "" && err != nil {
				t.Fatalf("Ping() error = %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("Ping() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestReadinessPingsOnlyConfiguredRequiredDependencies(t *testing.T) {
	t.Parallel()

	db := &fakeDatabase{version: SchemaVersion}
	checker, err := NewReadiness(
		db,
		pingFunc(func(context.Context) error { return errors.New("redis unavailable") }),
	)
	if err != nil {
		t.Fatalf("NewReadiness() error = %v", err)
	}
	if err := checker.Ping(
		context.Background(),
	); err == nil ||
		!strings.Contains(err.Error(), "required dependencies") {
		t.Fatalf("Ping() error = %v, want required dependencies error", err)
	}

	checker, err = NewReadiness(db, nil)
	if err != nil {
		t.Fatalf("NewReadiness() without required dependencies error = %v", err)
	}
	if err := checker.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() without required dependencies error = %v", err)
	}
}

func TestReadinessCoalescesConcurrentChecksAndBrieflyCachesSuccess(t *testing.T) {
	t.Parallel()

	checkStarted := make(chan struct{})
	releaseCheck := make(chan struct{})
	var startedOnce sync.Once
	db := &fakeDatabase{
		version: SchemaVersion,
		ping: func(context.Context) error {
			startedOnce.Do(func() { close(checkStarted) })
			<-releaseCheck
			return nil
		},
	}
	var redisCalls atomic.Int64
	now := time.Date(2026, 7, 11, 4, 0, 0, 0, time.UTC)
	const cacheTTL = 100 * time.Millisecond
	checker, err := newReadinessWithTiming(db, pingFunc(func(context.Context) error {
		redisCalls.Add(1)
		return nil
	}), readinessTiming{
		dependencyTimeout: time.Second,
		successCacheTTL:   cacheTTL,
		now:               func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("newReadinessWithTiming() error = %v", err)
	}

	firstResult := make(chan error, 1)
	go func() { firstResult <- checker.Ping(context.Background()) }()
	<-checkStarted

	followerContext := newObservedContext()
	followerResult := make(chan error, 1)
	go func() { followerResult <- checker.Ping(followerContext) }()
	<-followerContext.observed
	close(releaseCheck)

	if err := <-firstResult; err != nil {
		t.Fatalf("first Ping() error = %v", err)
	}
	if err := <-followerResult; err != nil {
		t.Fatalf("follower Ping() error = %v", err)
	}
	assertDependencyCalls(t, db, &redisCalls, 1)

	if err := checker.Ping(context.Background()); err != nil {
		t.Fatalf("cached Ping() error = %v", err)
	}
	assertDependencyCalls(t, db, &redisCalls, 1)

	now = now.Add(cacheTTL)
	if err := checker.Ping(context.Background()); err != nil {
		t.Fatalf("expired-cache Ping() error = %v", err)
	}
	assertDependencyCalls(t, db, &redisCalls, 2)
}

func TestReadinessDoesNotCacheFailure(t *testing.T) {
	t.Parallel()

	db := &fakeDatabase{version: SchemaVersion, pingErr: errors.New("down")}
	checker, err := newReadinessWithTiming(db, nil, readinessTiming{
		dependencyTimeout: time.Second,
		successCacheTTL:   time.Second,
		now:               time.Now,
	})
	if err != nil {
		t.Fatalf("newReadinessWithTiming() error = %v", err)
	}
	for attempt := range 2 {
		if err := checker.Ping(context.Background()); err == nil {
			t.Fatalf("Ping() attempt %d error = nil, want dependency failure", attempt+1)
		}
	}
	if got := db.pingCalls.Load(); got != 2 {
		t.Fatalf("database Ping() calls = %d, want 2", got)
	}
}

func TestReadinessStopsServingReadinessBeforeDependencyChecksWhenDraining(t *testing.T) {
	t.Parallel()

	db := &fakeDatabase{version: SchemaVersion}
	checker, err := NewReadiness(db, nil)
	if err != nil {
		t.Fatalf("NewReadiness() error = %v", err)
	}
	checker.StartDraining()

	if err := checker.Ping(context.Background()); !errors.Is(err, ErrDraining) {
		t.Fatalf("Ping() error = %v, want ErrDraining", err)
	}
	if got := db.pingCalls.Load(); got != 0 {
		t.Fatalf("database Ping() calls = %d, want 0", got)
	}
}

func TestReadinessDrainingInvalidatesCachedSuccess(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 11, 4, 30, 0, 0, time.UTC)
	db := &fakeDatabase{version: SchemaVersion}
	checker, err := newReadinessWithTiming(db, nil, readinessTiming{
		dependencyTimeout: time.Second,
		successCacheTTL:   time.Second,
		now:               func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("newReadinessWithTiming() error = %v", err)
	}
	if err := checker.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	if until := cachedUntil(checker); !until.After(now) {
		t.Fatalf("cached readiness deadline = %s, want after %s", until, now)
	}

	checker.StartDraining()
	if until := cachedUntil(checker); !until.IsZero() {
		t.Fatalf("cached readiness deadline after drain = %s, want zero", until)
	}
	if err := checker.Ping(context.Background()); !errors.Is(err, ErrDraining) {
		t.Fatalf("Ping() error = %v, want ErrDraining", err)
	}
	if got := db.pingCalls.Load(); got != 1 {
		t.Fatalf("database Ping() calls = %d, want cached check only", got)
	}
}

func TestReadinessDoesNotBecomeReadyWhenDrainStartsDuringAHealthCheck(t *testing.T) {
	t.Parallel()

	pingStarted := make(chan struct{})
	continuePing := make(chan struct{})
	db := &fakeDatabase{
		version: SchemaVersion,
		ping: func(context.Context) error {
			close(pingStarted)
			<-continuePing
			return nil
		},
	}
	checker, err := NewReadiness(db, nil)
	if err != nil {
		t.Fatalf("NewReadiness() error = %v", err)
	}

	result := make(chan error, 1)
	go func() { result <- checker.Ping(context.Background()) }()
	<-pingStarted
	checker.StartDraining()

	if err := <-result; !errors.Is(err, ErrDraining) {
		t.Fatalf("Ping() error = %v, want ErrDraining", err)
	}
	close(continuePing)
}

type fakeDatabase struct {
	pingErr   error
	ping      func(context.Context) error
	rowErr    error
	version   int64
	dirty     bool
	pingCalls atomic.Int64
	rowCalls  atomic.Int64
}

func (d *fakeDatabase) Ping(ctx context.Context) error {
	d.pingCalls.Add(1)
	if d.ping != nil {
		return d.ping(ctx)
	}
	return d.pingErr
}

func (d *fakeDatabase) QueryRow(context.Context, string, ...any) pgx.Row {
	d.rowCalls.Add(1)
	return fakeRow{version: d.version, dirty: d.dirty, err: d.rowErr}
}

type observedContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func newObservedContext() *observedContext {
	return &observedContext{Context: context.Background(), observed: make(chan struct{})}
}

func (c *observedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func assertDependencyCalls(t *testing.T, db *fakeDatabase, redisCalls *atomic.Int64, want int64) {
	t.Helper()
	if got := db.pingCalls.Load(); got != want {
		t.Fatalf("database Ping() calls = %d, want %d", got, want)
	}
	if got := db.rowCalls.Load(); got != want {
		t.Fatalf("database migration queries = %d, want %d", got, want)
	}
	if got := redisCalls.Load(); got != want {
		t.Fatalf("Redis Ping() calls = %d, want %d", got, want)
	}
}

func cachedUntil(checker *Readiness) time.Time {
	checker.mu.Lock()
	defer checker.mu.Unlock()
	return checker.readyUntil
}

type fakeRow struct {
	version int64
	dirty   bool
	err     error
}

func (r fakeRow) Scan(destinations ...any) error {
	if r.err != nil {
		return r.err
	}
	*(destinations[0].(*int64)) = r.version
	*(destinations[1].(*bool)) = r.dirty
	return nil
}
