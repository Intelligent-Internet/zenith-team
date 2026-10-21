package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/migrations"
)

const (
	defaultSuccessCacheTTL = 250 * time.Millisecond
)

// SchemaVersion is the migration version readiness requires. It is derived
// from the embedded migration set so adding a migration cannot silently
// desynchronize the readiness fence.
var SchemaVersion = latestEmbeddedSchemaVersion()

func latestEmbeddedSchemaVersion() int64 {
	version, err := migrations.LatestVersion()
	if err != nil {
		panic(fmt.Sprintf("readiness schema version: %v", err))
	}
	return version
}

var ErrDraining = errors.New("server is draining")

type readinessDatabase interface {
	Ping(context.Context) error
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Pinger interface {
	Ping(context.Context) error
}

// Readiness reports whether this API replica can safely serve traffic, and
// lets shutdown remove it from service before requests are drained.
type Readiness struct {
	database             readinessDatabase
	requiredDependencies Pinger
	dependencyTimeout    time.Duration
	successCacheTTL      time.Duration
	now                  func() time.Time
	draining             atomic.Bool

	mu         sync.Mutex
	inFlight   *dependencyCheck
	readyUntil time.Time
}

func NewReadiness(database readinessDatabase, requiredDependencies Pinger) (*Readiness, error) {
	return newReadinessWithTiming(database, requiredDependencies, readinessTiming{
		dependencyTimeout: 2 * time.Second,
		successCacheTTL:   defaultSuccessCacheTTL,
		now:               time.Now,
	})
}

type readinessTiming struct {
	dependencyTimeout time.Duration
	successCacheTTL   time.Duration
	now               func() time.Time
}

type dependencyCheck struct {
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	err    error
}

func newReadinessWithTiming(
	database readinessDatabase,
	requiredDependencies Pinger,
	timing readinessTiming,
) (*Readiness, error) {
	if database == nil {
		return nil, errors.New("readiness database is required")
	}
	if timing.dependencyTimeout <= 0 {
		return nil, errors.New("readiness dependency timeout must be positive")
	}
	if timing.successCacheTTL < 0 {
		return nil, errors.New("readiness success cache TTL must not be negative")
	}
	if timing.successCacheTTL == 0 {
		timing.successCacheTTL = defaultSuccessCacheTTL
	}
	if timing.now == nil {
		timing.now = time.Now
	}
	return &Readiness{
		database:             database,
		requiredDependencies: requiredDependencies,
		dependencyTimeout:    timing.dependencyTimeout,
		successCacheTTL:      timing.successCacheTTL,
		now:                  timing.now,
	}, nil
}

func (c *Readiness) StartDraining() {
	c.draining.Store(true)
	c.mu.Lock()
	c.readyUntil = time.Time{}
	if c.inFlight != nil {
		c.inFlight.cancel()
	}
	c.mu.Unlock()
}

func (c *Readiness) Ping(ctx context.Context) error {
	if c.draining.Load() {
		return ErrDraining
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	if c.draining.Load() {
		c.mu.Unlock()
		return ErrDraining
	}
	if c.now().Before(c.readyUntil) {
		c.mu.Unlock()
		return nil
	}
	check := c.inFlight
	if check == nil {
		dependencyContext, cancel := context.WithTimeout(context.Background(), c.dependencyTimeout)
		check = &dependencyCheck{
			done:   make(chan struct{}),
			ctx:    dependencyContext,
			cancel: cancel,
		}
		c.inFlight = check
		go c.runDependencyCheck(check)
	}
	c.mu.Unlock()

	var err error
	select {
	case <-check.done:
		err = check.err
	case <-ctx.Done():
		err = ctx.Err()
	case <-check.ctx.Done():
		select {
		case <-check.done:
			err = check.err
		default:
			err = fmt.Errorf("readiness dependency check: %w", check.ctx.Err())
		}
	}
	// StartDraining cancels an in-flight check, so a drain that began while
	// this Ping was waiting lands here and is never reported ready.
	if c.draining.Load() {
		return ErrDraining
	}
	return err
}

func (c *Readiness) runDependencyCheck(check *dependencyCheck) {
	err := c.checkDependencies(check.ctx)
	if contextErr := check.ctx.Err(); contextErr != nil {
		err = contextErr
	}

	c.mu.Lock()
	if err == nil && !c.draining.Load() {
		c.readyUntil = c.now().Add(c.successCacheTTL)
	}
	check.err = err
	if c.inFlight == check {
		c.inFlight = nil
	}
	close(check.done)
	c.mu.Unlock()
	check.cancel()
}

func (c *Readiness) checkDependencies(ctx context.Context) error {
	if err := c.database.Ping(ctx); err != nil {
		return fmt.Errorf("ping PostgreSQL: %w", err)
	}

	var version int64
	var dirty bool
	if err := c.database.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations LIMIT 1`).
		Scan(&version, &dirty); err != nil {
		return fmt.Errorf("read migration status: %w", err)
	}
	if version != SchemaVersion {
		return fmt.Errorf("migration version is %d, want %d", version, SchemaVersion)
	}
	if dirty {
		return fmt.Errorf("migration version %d is dirty", version)
	}
	if c.requiredDependencies != nil {
		if err := c.requiredDependencies.Ping(ctx); err != nil {
			return fmt.Errorf("ping required dependencies: %w", err)
		}
	}
	return nil
}
