//go:build integration

package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/migrations"
)

func TestTaskCalendarUpgradePreservesExistingWork(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	down, err := migrations.Files.ReadFile("000109_scheduler.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, err := migrations.Files.ReadFile("000109_scheduler.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Restore the previous schema head while retaining ordinary Tasks, their
	// conversations and Runs. Reapplying the additive upgrade must preserve them.
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := pool.QueryRow(ctx, `SELECT row_to_json(t)::text FROM tasks t WHERE id=$1`, fixture.TaskA).
		Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(up)); err != nil {
		t.Fatal(err)
	}
	var accepted, unscheduled bool
	var conversationID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT assignment_status='accepted', scheduled_at IS NULL AND task_schedule_id IS NULL,
		conversation_id FROM tasks WHERE id=$1`, fixture.TaskA).
		Scan(&accepted, &unscheduled, &conversationID); err != nil {
		t.Fatal(err)
	}
	if !accepted || !unscheduled || conversationID != fixture.ConversationA {
		t.Fatalf(
			"legacy Task changed: accepted=%t unscheduled=%t conversation=%s",
			accepted,
			unscheduled,
			conversationID,
		)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := pool.QueryRow(ctx, `SELECT row_to_json(t)::text FROM tasks t WHERE id=$1`, fixture.TaskA).
		Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("Calendar upgrade/rollback changed existing Task fields")
	}
}

func TestSchedulerDowngradePreservesCalendarAssignments(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	if _, err := pool.Exec(
		ctx,
		`UPDATE tasks SET assignment_status='pending' WHERE id=$1`,
		fixture.TaskA,
	); err != nil {
		t.Fatal(err)
	}
	down, err := migrations.Files.ReadFile("000109_scheduler.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err == nil || !strings.Contains(err.Error(), "discard history") {
		t.Fatalf("downgrade error = %v, want history protection", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT assignment_status FROM tasks WHERE id=$1`, fixture.TaskA).
		Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("assignment = %s, want pending", status)
	}
}

func TestSchedulerDowngradePreservesRunOrigin(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET task_scheduled=true WHERE id=$1`, fixture.RunA)
	down, err := migrations.Files.ReadFile("000109_scheduler.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err == nil || !strings.Contains(err.Error(), "discard history") {
		t.Fatalf("downgrade error = %v, want history protection", err)
	}
	var scheduled bool
	if err := pool.QueryRow(ctx, `SELECT task_scheduled FROM runs WHERE id=$1`, fixture.RunA).
		Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	if !scheduled {
		t.Fatal("rejected downgrade changed Run origin")
	}
}
