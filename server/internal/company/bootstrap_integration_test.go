//go:build integration

package company

import (
	"context"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestBootstrapReturnsOneOwnerSnapshotWithExactLiveCounters(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO inbox_items (
			company_id, recipient_user_id, kind, entity_type, entity_id, title, body, dedupe_key
		) VALUES ($1, $2, 'task_assigned', 'task', $3, 'Assigned', 'Open it', 'bootstrap-unread')`,
		fixture.CompanyA, fixture.UserA, fixture.TaskA.String())
	statuses := []string{"queued", "dispatched", "waiting_local_directory", "running", "deferred"}
	for _, status := range statuses {
		// No explicit id: runs.id is a bigserial, and seeding one by hand would
		// leave runs_id_seq behind the table.
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO runs (
				company_id, agent_id, task_id, conversation_id, runtime_id, status
			, run_type) VALUES ($1, $2, NULL, $3, $4, $5, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))`,
			fixture.CompanyA, fixture.AgentA,
			fixture.DirectConversationA, fixture.RuntimeA, status)
	}

	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewCompanyService(ServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatalf("NewCompanyService() error = %v", err)
	}
	result, err := service.Bootstrap(ctx, auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	})
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	// The seed holds one run per status: queued and deferred are scheduled
	// (queuedRunCount 2); dispatched, waiting_local_directory, and running
	// are in flight (activeRunCount 3) — the one counting vocabulary.
	if result.Company.ID != fixture.CompanyA ||
		result.InboxCounts.UnreadCount != 1 ||
		result.CEOAgent == nil || result.CEOAgent.ID != fixture.AgentA ||
		result.CEOConversationID == nil || *result.CEOConversationID != fixture.DirectConversationA ||
		result.ActiveTaskCount != 1 || result.QueuedRunCount != 2 || result.ActiveRunCount != 3 {
		t.Fatalf("Bootstrap() = %+v", result)
	}
}
