// Command migrate applies or rolls back the embedded PostgreSQL migration
// ladder without booting the API.
//
// This does NOT replace the server's startup migration. deploy/README.md makes
// that the rollout mechanism: the server applies migrations before serving, and
// a failed migration exits non-zero so the previous deployment keeps serving.
// This binary exists for the cases that are awkward without an API process —
// preparing a local or CI database, inspecting the applied version, and manual
// recovery from a dirty ladder.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/config"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/migrations"
)

// databaseURLVariable matches the server's own configuration so an operator
// never has to keep two names in step.
const databaseURLVariable = "APP_DATABASE_URL"

func main() {
	if err := run(os.Args[1:], os.LookupEnv, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, lookup func(string) (string, bool), logDestination *os.File) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(logDestination)
	direction := flags.String("direction", "up", `"up" to apply the ladder, "down" to roll it back`)
	confirmDown := flags.Bool(
		"confirm-down",
		false,
		"required with -direction=down; rolling back drops business data",
	)
	timeout := flags.Duration("timeout", 5*time.Minute, "overall deadline for the migration run")
	if err := flags.Parse(args); err != nil {
		return err
	}

	logger, err := config.NewLogger(logDestination, "info")
	if err != nil {
		return fmt.Errorf("construct logger: %w", err)
	}

	// Validate arguments before touching the network: an unknown direction or a
	// missing -confirm-down should fail instantly and without a database.
	switch *direction {
	case "up":
	case "down":
		if !*confirmDown {
			return errors.New(
				`-direction=down requires -confirm-down: rolling back the ladder drops business data`,
			)
		}
	default:
		return fmt.Errorf(`unknown -direction %q: want "up" or "down"`, *direction)
	}

	databaseURL, ok := lookup(databaseURLVariable)
	if !ok || databaseURL == "" {
		return fmt.Errorf("%s is required", databaseURLVariable)
	}

	// Signal-aware: golang-migrate holds a PostgreSQL advisory lock for the
	// duration, so an operator cancelling with Ctrl-C should unwind the context
	// rather than leave the lock held by a killed process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	// A migration run is one connection doing one thing; the server's pool
	// sizing is irrelevant here.
	pool, err := db.NewPool(ctx, db.PoolConfig{URL: databaseURL, MaxConns: 2, MinConns: 1})
	if err != nil {
		return fmt.Errorf("open PostgreSQL pool: %w", err)
	}
	defer pool.Close()

	latest, err := migrations.LatestVersion()
	if err != nil {
		return fmt.Errorf("resolve embedded migration version: %w", err)
	}

	if *direction == "down" {
		logger.InfoContext(ctx, "rolling back migrations")
		if err := db.MigrateDown(ctx, pool); err != nil {
			return fmt.Errorf("roll back PostgreSQL migrations: %w", err)
		}
		logger.InfoContext(ctx, "migrations rolled back")
		return nil
	}

	logger.InfoContext(ctx, "applying migrations", "target_version", latest)
	if err := db.MigrateUp(ctx, pool); err != nil {
		return fmt.Errorf("apply PostgreSQL migrations: %w", err)
	}
	logger.InfoContext(ctx, "migrations applied", "version", latest)
	return nil
}
