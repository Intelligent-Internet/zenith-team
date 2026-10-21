//go:build integration

package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestTaskPlanSchemaUsesDirectTaskRouting(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}

	for _, table := range []string{"goals", "missions"} {
		assertTableDoesNotExist(t, ctx, pool, table)
	}

	for _, column := range []string{"goal_id", "department_agent_id", "execution_type"} {
		var exists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = 'tasks' AND column_name = $1
			)`, column).Scan(&exists); err != nil {
			t.Fatalf("read retired Task column %s: %v", column, err)
		}
		if exists {
			t.Errorf("tasks.%s exists, want absent", column)
		}
	}

	for _, column := range []string{"milestone_type"} {
		var nullable string
		if err := pool.QueryRow(ctx, `
			SELECT is_nullable
			FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'tasks' AND column_name = $1`, column).Scan(&nullable); err != nil {
			t.Fatalf("read tasks.%s nullability: %v", column, err)
		}
		if nullable != "NO" {
			t.Errorf("tasks.%s is_nullable = %q, want NO", column, nullable)
		}
	}
}

func TestTaskPlanBaselineSchemaShapeAndIndexes(t *testing.T) {
	upSQL := normalizedBaselineSQL(t)
	for _, forbidden := range []string{"create table missions", "mission_id", "tasks_mission_idx"} {
		if strings.Contains(upSQL, forbidden) {
			t.Errorf("baseline migration still contains %q", forbidden)
		}
	}
	for _, fragment := range []string{
		"milestone_type text not null",
		"on tasks (company_id, milestone_type, number desc)",
	} {
		if !strings.Contains(upSQL, fragment) {
			t.Errorf("baseline migration is missing %q", fragment)
		}
	}
}
