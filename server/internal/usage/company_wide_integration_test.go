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

// Usage is a company account, not a per-caller view: an admin's report holds
// the owner's private-chat runs and the owner's holds the admin's. The Run
// list keeps its creator rule; token sums reveal nothing of a chat.
func TestCompanyUsageCountsEveryMembersRuns(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	admin := dbtest.NewUUID(t)
	adminChat := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO users (id, email, display_name) VALUES ($1, 'usage-admin@example.com', 'Usage Admin')`, admin)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'admin')`, fixture.CompanyA, admin)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversations (id, company_id, kind, task_id, agent_id, created_by_user_id, title)
		VALUES ($1, $2, 'direct', NULL, $3, $4, 'Admin chat')`, adminChat, fixture.CompanyA, fixture.AgentA, admin)
	var ownerRun, adminRun uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id, status, updated_at, run_type, revision)
		VALUES ($1, $2, $3, $4, 'completed', now(), 'direct_chat', 1) RETURNING id`,
		fixture.CompanyA, fixture.AgentA, fixture.DirectConversationA, fixture.RuntimeA,
	).Scan(&ownerRun); err != nil {
		t.Fatalf("insert owner chat run: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id, status, updated_at, run_type, revision)
		VALUES ($1, $2, $3, $4, 'completed', now(), 'direct_chat', 1) RETURNING id`,
		fixture.CompanyA, fixture.AgentA, adminChat, fixture.RuntimeA,
	).Scan(&adminRun); err != nil {
		t.Fatalf("insert admin chat run: %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO run_usage (run_id, provider, model, input_tokens, output_tokens, raw_json) VALUES
			($1, 'codex', 'gpt-5.6-luna', 1000, 100, '{}'),
			($2, 'codex', 'gpt-5.6-luna', 500, 50, '{}')`, ownerRun, adminRun)

	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewUsageService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	period := CompanyPeriod{Type: CompanyPeriodMonth, TimeZone: "UTC"}
	for _, principal := range []auth.UserPrincipal{
		{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner},
		{UserID: admin, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleAdmin},
	} {
		report, err := service.GetCompany(ctx, principal, period)
		if err != nil {
			t.Fatalf("GetCompany(%s) error = %v", principal.Role, err)
		}
		// The fixture's task run makes three runs; only the two chat runs
		// carry usage facts.
		if report.Totals.RunCount != "3" || report.Totals.InputTokens != "1500" {
			t.Fatalf(
				"GetCompany(%s) totals = %+v, want both members' chat runs",
				principal.Role,
				report.Totals,
			)
		}
	}
}
