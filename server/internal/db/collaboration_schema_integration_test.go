//go:build integration

package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestCollaborationSchemaEnforcesTenantCreatorsAndSafeOrdering(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	t.Run("Task status stays a closed enum", func(t *testing.T) {
		for _, status := range []string{"failed", "error"} {
			if _, err := pool.Exec(
				ctx,
				`UPDATE tasks SET status=$1 WHERE id=$2`,
				status,
				fixture.TaskA,
			); err != nil {
				t.Fatalf("set Task status %q: %v", status, err)
			}
		}
		assertStatementRejected(t, ctx, pool, "23514", `
			UPDATE tasks SET status='archived' WHERE id=$1`, fixture.TaskA)
	})
}

func TestCollaborationSchemaIncludesDeterministicAccessIndexes(t *testing.T) {
	sql := normalizedBaselineSQL(t)
	for _, index := range []string{
		"on conversations (company_id, updated_at desc, id desc) where (kind = 'task')",
		"on conversations (company_id, created_by_user_id, updated_at desc, id desc) where (kind = 'direct')",
		"on tasks (company_id, status, number desc)",
		"on tasks (company_id, milestone_type, number desc)",
		"on tasks (company_id, assignee_user_id, number desc)",
		"on tasks (company_id, executed_by_agent_id, number desc) where (executed_by_agent_id is not null)",
		"on runs (conversation_id, created_at desc, id desc)",
		"on runs (agent_id, runtime_id, priority desc, created_at, id)",
	} {
		if !strings.Contains(sql, index) {
			t.Errorf("baseline migration is missing deterministic collaboration index %q", index)
		}
	}
}
