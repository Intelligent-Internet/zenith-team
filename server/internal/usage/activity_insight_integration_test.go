//go:build integration

package usage

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestActivityInsightAggregatesOneAuthorizedRunSet(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	var lastActiveAt time.Time
	if err := pool.QueryRow(ctx, `SELECT clock_timestamp() + interval '1 second'`).
		Scan(&lastActiveAt); err != nil {
		t.Fatalf("read test clock: %v", err)
	}
	// lastActiveAt is when the caller's most recent run started, so the failed
	// run starts exactly there and finishes a minute later: a reading of the
	// last update would come back one minute late and fail the assertion.
	// The queued run started a minute earlier and has not moved since.
	var failedRunID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, conversation_id, runtime_id, status,
			created_at, updated_at
		, run_type) VALUES
			($1, $2, $3, $4, 'failed', $6, $7, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 )),
			($1, $2, $3, $4, 'queued', $5, $5, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA, fixture.DirectConversationA,
		fixture.RuntimeA, lastActiveAt.Add(-time.Minute), lastActiveAt, lastActiveAt.Add(time.Minute),
	).Scan(&failedRunID); err != nil {
		t.Fatalf("insert insight runs: %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO run_usage (run_id, provider, model, raw_json) VALUES
			($1, 'codex', 'gpt-5.6-sol', '{}'),
			($2, 'codex', 'gpt-5.6-sol', '{}')`, fixture.RunA, failedRunID)

	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewUsageService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	insight, err := service.GetActivityInsight(ctx, auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	})
	if err != nil {
		t.Fatalf("GetActivityInsight() error = %v", err)
	}
	if insight.MostUsedProvider == nil || *insight.MostUsedProvider != "codex" ||
		insight.MostUsedModel == nil || *insight.MostUsedModel != "gpt-5.6-sol" ||
		insight.SuccessfulTasks != "1" || insight.TotalSessions != "2" ||
		insight.CompletedRuns != "1" || insight.FailedRuns != "1" ||
		insight.TotalRuns != "3" || insight.LastActiveAt == nil ||
		!insight.LastActiveAt.Equal(lastActiveAt) {
		t.Fatalf("GetActivityInsight() = %+v", insight)
	}
}
