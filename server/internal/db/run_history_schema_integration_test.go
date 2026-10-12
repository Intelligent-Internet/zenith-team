//go:build integration

package db

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestRunHistoryUsesDatabaseUUIDIdentity(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var dataType, defaultExpression string
	err := pool.QueryRow(ctx, `
		SELECT udt_name, column_default FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'runs' AND column_name = 'id'
	`).Scan(&dataType, &defaultExpression)
	if err != nil {
		t.Fatal(err)
	}
	if dataType != "uuid" || !strings.Contains(defaultExpression, "gen_random_uuid()") {
		t.Fatalf("Run identity = %s DEFAULT %s, want database-generated UUID", dataType, defaultExpression)
	}
	var numericReferences int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND udt_name = 'int8'
		  AND (column_name = 'run_id' OR column_name LIKE '%\_run_id' ESCAPE '\')
	`).Scan(&numericReferences); err != nil {
		t.Fatal(err)
	}
	if numericReferences != 0 {
		t.Fatalf("numeric Run reference columns = %d, want 0", numericReferences)
	}
}

func TestRunUUIDMigrationPreservesInboxReferences(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	runner, err := newMigrationRunner(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Migrate(13); err != nil {
		t.Fatal(err)
	}
	if sourceErr, databaseErr := runner.Close(); sourceErr != nil || databaseErr != nil {
		t.Fatalf("close old migration: %v, %v", sourceErr, databaseErr)
	}
	companyID, userID := dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO companies (id, name, slug, task_prefix) VALUES ($1, 'Company', 'company', 'C')`, companyID)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO users (id, email, display_name) VALUES ($1, 'test@example.com', 'User')`, userID)
	for _, link := range []string{"/app?companyId=company&runId=4841&tab=run", "/runs/4841"} {
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO inbox_items (company_id, recipient_user_id, kind, entity_type, entity_id, title, body, deep_link, dedupe_key)
			VALUES ($1, $2, 'run_completed', 'run', '4841', 'Run 4841', 'Original content 4841', $3, $4)`,
			companyID, userID, link, "run:4841:"+map[bool]string{true: "completed", false: "failed"}[strings.HasPrefix(link, "/app?")])
	}
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var runID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT run_id FROM run_identity_mappings WHERE legacy_id = 4841`).Scan(&runID); err != nil {
		t.Fatalf("Inbox-only retained mapping: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM inbox_items WHERE entity_id = $1
		  AND deep_link IN ($2, $3) AND dedupe_key IN ($4, $5)
		  AND title = 'Run 4841' AND body = 'Original content 4841'`,
		runID.String(), fmt.Sprintf("/app?companyId=company&runId=%s&tab=run", runID), "/runs/"+runID.String(),
		"run:"+runID.String()+":completed", "run:"+runID.String()+":failed").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("preserved Inbox notifications = %d, want 2", count)
	}
}

func TestRunUUIDMigrationPreservesOrphanActivityIdentity(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	runner, err := newMigrationRunner(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Migrate(13); err != nil {
		t.Fatal(err)
	}
	if sourceErr, databaseErr := runner.Close(); sourceErr != nil || databaseErr != nil {
		t.Fatalf("close old migration: %v, %v", sourceErr, databaseErr)
	}
	companyID := dbtest.NewUUID(t)
	var activityID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO activity_events (company_id, action, entity_type, entity_id, run_id, details)
		VALUES ($1, 'run.completed', 'run', '4837', 4837, '{"id":4837,"status":"completed"}')
		RETURNING id`, companyID).Scan(&activityID)
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var mappedID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT run_id FROM run_identity_mappings WHERE legacy_id = 4837
	`).Scan(&mappedID); err != nil {
		t.Fatalf("retained orphan Run mapping: %v", err)
	}
	var subject string
	if err := pool.QueryRow(ctx, `SELECT entity_id FROM activity_events WHERE id = $1`, activityID).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	if mappedID == uuid.Nil || subject != mappedID.String() {
		t.Fatalf("retained Activity subject = %q, want mapped UUID %s", subject, mappedID)
	}
	var runCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 0 {
		t.Fatalf("migration fabricated %d Run rows from retained Activity", runCount)
	}
}
