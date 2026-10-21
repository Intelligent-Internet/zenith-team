//go:build integration

package execution

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// The rebind and removal participant: the member's pending rows on the named
// agents move from the source runtime to the target inside the caller's
// transaction; everything a daemon already holds, every other agent's
// work, every other member's work, and every other runtime's work stays
// where it is.
func TestRetargetPendingRunsForBindingsMovesOnlyTheMembersPendingRows(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	target := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-target', 'Target', 'claude', 'online', $3, now())`,
		target, fixture.CompanyA, fixture.UserA)
	otherAgent := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO agents (id, company_id, name, title, reports_to_agent_id, provider)
		VALUES ($1, $2, 'Other', 'Operator', $3, 'codex')`,
		otherAgent, fixture.CompanyA, fixture.AgentA)
	// Another member with their own binding of Agent A on the same machine
	// is impossible (the machine is the registrant's), so the foreign work
	// that must stay put is another authorizer's row on the source, as a
	// re-registration under a new account leaves behind.
	otherMember := dbtest.NewUUID(t)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Other member')`,
		otherMember,
		otherMember.String()+"@zenith-team.test",
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		fixture.CompanyA,
		otherMember,
	)
	seed := func(agentID, authorizer uuid.UUID, status string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(
			ctx, `
			INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id, execution_authorized_by_user_id, status, fire_at, run_type)
			VALUES ($1, $2, $3, $4, $5, $6, CASE WHEN $6 = 'deferred' THEN now() + interval '1 hour' END, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
			RETURNING id`,
			fixture.CompanyA, agentID, fixture.DirectConversationA, fixture.RuntimeA, authorizer, status,
		).Scan(&id); err != nil {
			t.Fatalf("seed %s run: %v", status, err)
		}
		return id
	}
	queued := seed(fixture.AgentA, fixture.UserA, "queued")
	deferred := seed(fixture.AgentA, fixture.UserA, "deferred")
	dispatched := seed(fixture.AgentA, fixture.UserA, "dispatched")
	running := seed(fixture.AgentA, fixture.UserA, "running")
	otherQueued := seed(otherAgent, fixture.UserA, "queued")
	othersQueued := seed(fixture.AgentA, otherMember, "queued")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	queries := generated.New(tx)
	var pending []events.Event

	// An empty agent set touches nothing and runs no statement.
	work := BindingWork{CompanyID: fixture.CompanyA, SourceRuntimeID: fixture.RuntimeA, UserID: fixture.UserA}
	if moved, err := RetargetPendingRunsForBindingsInTx(
		ctx,
		queries,
		&pending,
		events.SystemActor(),
		work,
		target,
	); err != nil ||
		len(moved) != 0 {
		t.Fatalf("RetargetPendingRunsForBindingsInTx(no agents) = %+v, %v; want nothing", moved, err)
	}
	work.AgentIDs = []uuid.UUID{fixture.AgentA}
	moved, err := RetargetPendingRunsForBindingsInTx(ctx, queries, &pending, events.SystemActor(), work, target)
	if err != nil {
		t.Fatalf("RetargetPendingRunsForBindingsInTx() error = %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(moved) != 2 {
		t.Fatalf(
			"RetargetPendingRunsForBindingsInTx() moved %d rows, want the queued and deferred runs",
			len(moved),
		)
	}
	for _, run := range moved {
		if run.RuntimeID != target || run.AgentID != fixture.AgentA ||
			(run.ID != queued && run.ID != deferred) {
			t.Fatalf("moved run = %+v, want AgentA's queued or deferred run on the target", run)
		}
	}
	want := map[uuid.UUID]uuid.UUID{
		queued:       target,
		deferred:     target,
		dispatched:   fixture.RuntimeA,
		running:      fixture.RuntimeA,
		otherQueued:  fixture.RuntimeA,
		othersQueued: fixture.RuntimeA,
		fixture.RunA: fixture.RuntimeA,
	}
	for runID, wantRuntime := range want {
		var got uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT runtime_id FROM runs WHERE id = $1`, runID).
			Scan(&got); err != nil {
			t.Fatalf("read run %d: %v", runID, err)
		}
		if got != wantRuntime {
			t.Fatalf("run %d runtime = %s, want %s", runID, got, wantRuntime)
		}
	}
}
