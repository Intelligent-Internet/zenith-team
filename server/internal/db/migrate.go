package db

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	migratepostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/Intelligent-Internet/zenith-team/server/migrations"
)

func MigrateUp(ctx context.Context, pool *pgxpool.Pool) error {
	return runMigration(ctx, pool, "migrate database up", (*migrate.Migrate).Up)
}

func MigrateDown(ctx context.Context, pool *pgxpool.Pool) error {
	return runMigration(ctx, pool, "migrate database down", (*migrate.Migrate).Down)
}

func runMigration(
	ctx context.Context,
	pool *pgxpool.Pool,
	operation string,
	direction func(*migrate.Migrate) error,
) error {
	gate, err := acquireMigrationGate(ctx, pool)
	if err != nil {
		return err
	}

	runner, err := newMigrationRunner(ctx, pool)
	if err != nil {
		return errors.Join(err, wrapError("release migration gate", releaseMigrationGate(gate)))
	}
	migrationErr := direction(runner)
	if errors.Is(migrationErr, migrate.ErrNoChange) {
		migrationErr = nil
	}
	sourceErr, databaseErr := runner.Close()
	gateErr := releaseMigrationGate(gate)
	return errors.Join(
		wrapError(operation, migrationErr),
		wrapError("close migration source", sourceErr),
		wrapError("close migration database", databaseErr),
		wrapError("release migration gate", gateErr),
	)
}

// acquireMigrationGate keeps concurrent replicas out of golang-migrate's
// blocking advisory-lock call. That call runs on context.Background(), so a
// caller deadline cannot interrupt it — the session statement_timeout set
// below is what bounds the wait (pinned by
// TestMigrationUpBoundsDriverAdvisoryLockWait). Polling with completed
// try-lock statements is also required because CREATE INDEX CONCURRENTLY
// waits for active transactions; a replica blocked inside pg_advisory_lock
// would otherwise deadlock with it.
func acquireMigrationGate(ctx context.Context, pool *pgxpool.Pool) (*SessionAdvisoryLock, error) {
	store, err := NewStore(pool)
	if err != nil {
		return nil, err
	}
	lockID, err := migratedb.GenerateAdvisoryLockId(
		pool.Config().ConnConfig.Database,
		"public",
		migratepostgres.DefaultMigrationsTable,
		"zenith_team_migration_gate",
	)
	if err != nil {
		return nil, fmt.Errorf("generate migration gate lock ID: %w", err)
	}
	lockKey := "zenith-team/migrations/startup/" + lockID

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		gate, acquired, err := store.TryAcquireSessionAdvisoryLock(
			ctx,
			lockKey,
			30*time.Second,
		)
		if err != nil {
			return nil, fmt.Errorf("try migration gate lock: %w", err)
		}
		if acquired {
			return gate, nil
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for migration gate: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func releaseMigrationGate(gate *SessionAdvisoryLock) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return gate.Release(cleanupCtx)
}

func newMigrationRunner(ctx context.Context, pool *pgxpool.Pool) (*migrate.Migrate, error) {
	statementTimeout, err := migrationStatementTimeout(ctx)
	if err != nil {
		return nil, err
	}
	sourceDriver, err := iofs.New(migrations.Files, ".")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}

	config := pool.Config().ConnConfig.Copy()
	// A migration is intentionally a multi-statement SQL file. Simple protocol
	// allows pgx to execute the consolidated file without splitting SQL in Go.
	config.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	if config.RuntimeParams == nil {
		config.RuntimeParams = make(map[string]string)
	}
	// golang-migrate's PostgreSQL lock uses context.Background. A session-level
	// statement timeout therefore bounds both advisory-lock waits and SQL even
	// when a replica shuts down while another replica is migrating.
	config.RuntimeParams["statement_timeout"] = strconv.FormatInt(statementTimeout.Milliseconds(), 10)
	sqlDB := stdlib.OpenDB(*config)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sourceDriver.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping migration database: %w", err)
	}

	databaseDriver, err := migratepostgres.WithInstance(sqlDB, &migratepostgres.Config{
		StatementTimeout: statementTimeout,
	})
	if err != nil {
		_ = sourceDriver.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("open migration database driver: %w", err)
	}
	runner, err := migrate.NewWithInstance("iofs", sourceDriver, "postgres", databaseDriver)
	if err != nil {
		_ = sourceDriver.Close()
		_ = databaseDriver.Close()
		return nil, fmt.Errorf("construct migration runner: %w", err)
	}
	return runner, nil
}

func migrationStatementTimeout(ctx context.Context) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("migration context: %w", err)
	}
	timeout := 5 * time.Minute
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, fmt.Errorf("migration context: %w", context.DeadlineExceeded)
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	// Never return zero: both sinks read it as "unlimited". PostgreSQL treats
	// statement_timeout=0 as disabled, and golang-migrate skips
	// context.WithTimeout entirely on a zero StatementTimeout, leaving every
	// DDL statement on a bare context.Background().
	if timeout < time.Millisecond {
		timeout = time.Millisecond
	}
	return timeout, nil
}

func wrapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
