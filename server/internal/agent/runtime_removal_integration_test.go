//go:build integration

package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"boardly.dev/boardly/server/pkg/protocol"
)

// mustRemovalService is mustRuntimeLifecycleService plus a recorder on the
// execution runtime topic, where the retargeted runs' wakeups land.
func mustRemovalService(
	t *testing.T,
	store *db.Store,
) (*DefaultRuntimeService, *agentEventLog, *topicLog[execution.RuntimeWork]) {
	t.Helper()
	bus := mustAgentTestBus(t)
	log := recordAgentEvents(t, bus)
	work := &topicLog[execution.RuntimeWork]{}
	if err := bus.Subscribe(execution.TopicRuntime, func(_ context.Context, event events.Event) error {
		if event.Name == execution.EventRuntimeWorkAvailable {
			return work.record(event)
		}
		return nil
	}); err != nil {
		t.Fatalf("subscribe to the runtime topic: %v", err)
	}
	service, err := NewRuntimeService(RuntimeServiceOptions{
		Store:            store,
		Bus:              bus,
		ModelDiscoveries: NewInMemoryModelDiscoveryStore(ModelDiscoveryStoreOptions{}),
	})
	if err != nil {
		t.Fatalf("NewRuntimeService() error = %v", err)
	}
	return service, log, work
}

func ownerOf(fixture dbtest.Fixture) auth.UserPrincipal {
	return auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
}

// setRuntimeOffline is the precondition every successful removal needs: the
// fixture's RuntimeA is seeded online.
func setRuntimeOffline(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runtimeID uuid.UUID) {
	t.Helper()
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET status = 'offline', last_seen_at = now() - interval '10 minutes' WHERE id = $1`,
		runtimeID)
}

// seedTemplateAgent adds a codex template under AgentA with a model and
// thinking level, archived when asked. It is a template: whether anyone runs
// it anywhere is a binding, which the caller writes separately.
func seedTemplateAgent(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	name string,
	archived bool,
) uuid.UUID {
	t.Helper()
	id := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO agents
			(id, company_id, name, title, reports_to_agent_id, provider, model, thinking_level, archived_at)
		VALUES ($1, $2, $3, 'Operator', $4, 'codex', 'gpt-5', 'high',
			CASE WHEN $5::boolean THEN now() ELSE NULL END)`,
		id, fixture.CompanyA, name, fixture.AgentA, archived)
	return id
}

// seedRun inserts one run of the agent on the runtime, authorized by
// authorizer, in a direct conversation (no task lane). A deferred run fires
// an hour from now.
func seedRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	agentID, runtimeID, authorizer uuid.UUID,
	status string,
) uuid.UUID {
	t.Helper()
	var fireAt *time.Time
	if status == "deferred" {
		at := time.Now().Add(time.Hour)
		fireAt = &at
	}
	var id uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id, execution_authorized_by_user_id, status, fire_at, run_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`,
		fixture.CompanyA, agentID, fixture.DirectConversationA, runtimeID, authorizer, status, fireAt,
	).Scan(&id); err != nil {
		t.Fatalf("seed %s run: %v", status, err)
	}
	return id
}

// readBindingRuntime is the machine a member's binding of an agent names, or
// uuid.Nil when they have none.
func readBindingRuntime(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	agentID, userID uuid.UUID,
) uuid.UUID {
	t.Helper()
	var runtimeID uuid.UUID
	err := pool.QueryRow(ctx,
		`SELECT runtime_id FROM agent_runtime_bindings WHERE agent_id = $1 AND user_id = $2`,
		agentID, userID,
	).Scan(&runtimeID)
	if err != nil {
		return uuid.Nil
	}
	return runtimeID
}

func readRemovedAt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runtimeID uuid.UUID) (*time.Time, string) {
	t.Helper()
	var removedAt *time.Time
	var status string
	if err := pool.QueryRow(
		ctx,
		`SELECT removed_at, status FROM runtimes WHERE id = $1`, runtimeID,
	).Scan(&removedAt, &status); err != nil {
		t.Fatalf("read runtime %s removal: %v", runtimeID, err)
	}
	return removedAt, status
}

func readRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID) (uuid.UUID, string, string) {
	t.Helper()
	var runtimeID uuid.UUID
	var status string
	var reason *string
	if err := pool.QueryRow(ctx,
		`SELECT runtime_id, status, error FROM runs WHERE id = $1`, runID,
	).Scan(&runtimeID, &status, &reason); err != nil {
		t.Fatalf("read run %s: %v", runID, err)
	}
	if reason == nil {
		return runtimeID, status, ""
	}
	return runtimeID, status, *reason
}

func assertNothingWritten(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	log *agentEventLog,
) {
	t.Helper()
	if removedAt, _ := readRemovedAt(t, ctx, pool, fixture.RuntimeA); removedAt != nil {
		t.Fatalf("removed_at = %v after a refusal, want NULL", removedAt)
	}
	if got := readBindingRuntime(t, ctx, pool, fixture.AgentA, fixture.UserA); got != fixture.RuntimeA {
		t.Fatalf("AgentA binding after a refusal = %s, want RuntimeA", got)
	}
	if published := log.total(); published != 0 {
		t.Fatalf("events published after a refusal = %d, want none", published)
	}
}

func TestRemoveRuntimeRefusesAnOnlineRuntime(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log, _ := mustRemovalService(t, store)
	target := seedRuntime(t, ctx, pool, fixture, "daemon-s", "codex", fixture.UserA)

	// RuntimeA is seeded online: a target is still refused.
	_, err := service.Remove(ctx, ownerOf(fixture), fixture.RuntimeA, RuntimeRemovalInput{TargetRuntimeID: &target})
	if !errors.Is(err, ErrRuntimeRemoveBlockedOnline) {
		t.Fatalf("Remove(online) error = %v, want ErrRuntimeRemoveBlockedOnline", err)
	}
	// And so is the targetless form: online outranks everything else.
	if _, err := service.Remove(ctx, ownerOf(fixture), fixture.RuntimeA, RuntimeRemovalInput{}); !errors.Is(err, ErrRuntimeRemoveBlockedOnline) {
		t.Fatalf("Remove(online, no target) error = %v, want ErrRuntimeRemoveBlockedOnline", err)
	}
	assertNothingWritten(t, ctx, pool, fixture, log)
}

// With a target, the registrant's bindings on the machine move there and
// their pending Runs follow; the Agents themselves are untouched, because a
// template has no machine (ADR 0014).
func TestRemoveRuntimeMovesTheRegistrantsBindingsAndPendingRuns(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log, work := mustRemovalService(t, store)
	second := seedTemplateAgent(t, ctx, pool, fixture, "Second", false)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, second, fixture.UserA, fixture.RuntimeA, "codex")
	target := seedRuntime(t, ctx, pool, fixture, "daemon-s", "codex", fixture.UserA)
	setRuntimeOffline(t, ctx, pool, fixture.RuntimeA)
	queued := seedRun(t, ctx, pool, fixture, fixture.AgentA, fixture.RuntimeA, fixture.UserA, "queued")
	deferred := seedRun(t, ctx, pool, fixture, second, fixture.RuntimeA, fixture.UserA, "deferred")
	dispatched := seedRun(t, ctx, pool, fixture, fixture.AgentA, fixture.RuntimeA, fixture.UserA, "dispatched")
	completed := seedRun(t, ctx, pool, fixture, fixture.AgentA, fixture.RuntimeA, fixture.UserA, "completed")

	receipt, err := service.Remove(ctx, ownerOf(fixture), fixture.RuntimeA, RuntimeRemovalInput{TargetRuntimeID: &target})
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if receipt.RemovedRuntimeID != fixture.RuntimeA || receipt.MovedBindingCount != 2 ||
		receipt.RetargetedRunCount != 2 || receipt.CancelledRunCount != 0 {
		t.Fatalf("receipt = %+v, want two bindings and two runs moved", receipt)
	}
	for _, agentID := range []uuid.UUID{fixture.AgentA, second} {
		if got := readBindingRuntime(t, ctx, pool, agentID, fixture.UserA); got != target {
			t.Fatalf("binding of %s = %s, want the target %s", agentID, got, target)
		}
	}
	// The templates did not move: no provider change, no version bump.
	var version int64
	var provider string
	if err := pool.QueryRow(ctx, `SELECT version, provider FROM agents WHERE id = $1`, fixture.AgentA).
		Scan(&version, &provider); err != nil || version != 1 || provider != "codex" {
		t.Fatalf("AgentA after removal = version %d provider %q, %v; want the template untouched", version, provider, err)
	}
	for _, test := range []struct {
		runID       uuid.UUID
		wantRuntime uuid.UUID
		wantStatus  string
	}{
		{queued, target, "queued"},
		{deferred, target, "deferred"},
		{dispatched, fixture.RuntimeA, "dispatched"},
		{completed, fixture.RuntimeA, "completed"},
	} {
		runtimeID, status, _ := readRun(t, ctx, pool, test.runID)
		if runtimeID != test.wantRuntime || status != test.wantStatus {
			t.Fatalf("run %s = %s / %s, want %s / %s", test.runID, runtimeID, status, test.wantRuntime, test.wantStatus)
		}
	}
	removedAt, status := readRemovedAt(t, ctx, pool, fixture.RuntimeA)
	if removedAt == nil || status != "offline" {
		t.Fatalf("removed runtime = %v / %s, want stamped and offline", removedAt, status)
	}

	// One binding_changed per moved binding, naming the target; then the
	// roster's remove; then one wakeup per moved run so the target's daemon
	// claims now rather than at its next poll.
	bindings := log.binding.snapshot()
	if len(bindings) != 2 {
		t.Fatalf("binding_changed events = %d, want one per moved binding", len(bindings))
	}
	for _, event := range bindings {
		if event.Payload.Binding == nil || event.Payload.Binding.Runtime.ID != target ||
			event.Payload.UserID != fixture.UserA {
			t.Fatalf("binding_changed = %+v, want the registrant's binding on the target", event.Payload)
		}
	}
	roster := log.roster.snapshot()
	if len(roster) != 1 || roster[0].Payload.Action != protocol.DaemonRosterActionRemove ||
		roster[0].Payload.UserID != fixture.UserA {
		t.Fatalf("roster events = %+v, want one remove for the registrant", roster)
	}
	if wakeups := work.snapshot(); len(wakeups) != 2 {
		t.Fatalf("runtime work wakeups = %d, want one per moved run", len(wakeups))
	}
}

// Without a target the bindings are dropped and the registrant's pending
// work on the machine is cancelled with the runtime_removed reason; claimed
// and terminal rows are left for the sweepers and for history.
func TestRemoveRuntimeWithoutATargetDropsBindingsAndCancelsPendingRuns(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log, work := mustRemovalService(t, store)
	setRuntimeOffline(t, ctx, pool, fixture.RuntimeA)
	queued := seedRun(t, ctx, pool, fixture, fixture.AgentA, fixture.RuntimeA, fixture.UserA, "queued")
	running := seedRun(t, ctx, pool, fixture, fixture.AgentA, fixture.RuntimeA, fixture.UserA, "running")

	receipt, err := service.Remove(ctx, ownerOf(fixture), fixture.RuntimeA, RuntimeRemovalInput{})
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if receipt.MovedBindingCount != 0 || receipt.RetargetedRunCount != 0 || receipt.CancelledRunCount != 1 {
		t.Fatalf("receipt = %+v, want one cancelled run and nothing moved", receipt)
	}
	if got := readBindingRuntime(t, ctx, pool, fixture.AgentA, fixture.UserA); got != uuid.Nil {
		t.Fatalf("binding after a targetless removal = %s, want none", got)
	}
	if _, status, reason := readRun(t, ctx, pool, queued); status != "cancelled" || reason != execution.RunReasonRuntimeRemoved {
		t.Fatalf("queued run = %s / %q, want cancelled with runtime_removed", status, reason)
	}
	if _, status, _ := readRun(t, ctx, pool, running); status != "running" {
		t.Fatalf("claimed run = %s, want left to the sweepers", status)
	}
	bindings := log.binding.snapshot()
	if len(bindings) != 1 || bindings[0].Payload.Binding != nil || bindings[0].Payload.UserID != fixture.UserA {
		t.Fatalf("binding_changed = %+v, want one explicit null for the registrant", bindings)
	}
	if wakeups := work.snapshot(); len(wakeups) != 0 {
		t.Fatalf("runtime work wakeups = %d, want none: nothing moved", len(wakeups))
	}
}

// Another member's binding of the same Agent is theirs: a removal moves and
// cancels only the registrant's own, and leaves the colleague's work alone.
func TestRemoveRuntimeTouchesOnlyTheRegistrantsOwnBindingsAndRuns(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, _, _ := mustRemovalService(t, store)
	member := seedMember(t, ctx, pool, fixture, "member@example.com", "member")
	theirs := seedRuntime(t, ctx, pool, fixture, "daemon-m", "codex", member)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, member, theirs, "codex")
	theirRun := seedRun(t, ctx, pool, fixture, fixture.AgentA, theirs, member, "queued")
	setRuntimeOffline(t, ctx, pool, fixture.RuntimeA)

	if _, err := service.Remove(ctx, ownerOf(fixture), fixture.RuntimeA, RuntimeRemovalInput{}); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if got := readBindingRuntime(t, ctx, pool, fixture.AgentA, member); got != theirs {
		t.Fatalf("the colleague's binding = %s, want their own machine %s", got, theirs)
	}
	if runtimeID, status, _ := readRun(t, ctx, pool, theirRun); runtimeID != theirs || status != "queued" {
		t.Fatalf("the colleague's run = %s / %s, want untouched", runtimeID, status)
	}
}

// The gate is the stored registrant, judged inside the transaction: nobody
// else's machine is visible to anyone, whatever their Company role, so every
// refusal is the same not-found.
func TestRemoveRuntimeIsTheRegistrantsAlone(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log, _ := mustRemovalService(t, store)
	setRuntimeOffline(t, ctx, pool, fixture.RuntimeA)
	machineOwner := seedMember(t, ctx, pool, fixture, "machine-owner@example.com", "member")
	bystander := seedMember(t, ctx, pool, fixture, "bystander@example.com", "member")
	ownedByMember := seedRuntime(t, ctx, pool, fixture, "daemon-c", "codex", machineOwner)
	setRuntimeOffline(t, ctx, pool, ownedByMember)
	revoked := seedMember(t, ctx, pool, fixture, "revoked-owner@example.com", "owner")
	dbtest.MustExec(t, ctx, pool,
		`UPDATE company_members SET status = 'revoked' WHERE company_id = $1 AND user_id = $2`,
		fixture.CompanyA, revoked)

	refusals := []struct {
		name      string
		principal auth.UserPrincipal
		runtimeID uuid.UUID
	}{
		{
			name:      "a member does not see the owner's machine",
			principal: auth.UserPrincipal{UserID: machineOwner, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember},
			runtimeID: fixture.RuntimeA,
		},
		{
			name:      "another member does not see the machine owner's",
			principal: auth.UserPrincipal{UserID: bystander, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember},
			runtimeID: ownedByMember,
		},
		{
			name:      "the company owner does not see a member's machine either",
			principal: ownerOf(fixture), runtimeID: ownedByMember,
		},
		{
			name:      "a claimed owner role loses to the stored registrant",
			principal: auth.UserPrincipal{UserID: machineOwner, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner},
			runtimeID: fixture.RuntimeA,
		},
		{
			name:      "a revoked owner sees no runtime",
			principal: auth.UserPrincipal{UserID: revoked, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner},
			runtimeID: fixture.RuntimeA,
		},
		{
			name:      "another company's owner sees no runtime",
			principal: auth.UserPrincipal{UserID: fixture.UserB, CompanyID: fixture.CompanyB, Role: auth.CompanyRoleOwner},
			runtimeID: fixture.RuntimeA,
		},
		{
			name:      "a non-member acting in the company sees no runtime",
			principal: auth.UserPrincipal{UserID: fixture.UserB, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner},
			runtimeID: fixture.RuntimeA,
		},
		{
			name:      "an unknown runtime",
			principal: ownerOf(fixture), runtimeID: dbtest.NewUUID(t),
		},
	}
	for _, refusal := range refusals {
		if _, err := service.Remove(ctx, refusal.principal, refusal.runtimeID, RuntimeRemovalInput{}); !errors.Is(err, ErrRuntimeNotFound) {
			t.Fatalf("%s: Remove() error = %v, want ErrRuntimeNotFound", refusal.name, err)
		}
	}
	assertNothingWritten(t, ctx, pool, fixture, log)
	if removedAt, _ := readRemovedAt(t, ctx, pool, ownedByMember); removedAt != nil {
		t.Fatalf("member-owned runtime removed_at = %v after refusals, want NULL", removedAt)
	}

	// The registrant removes their own machine as a plain member.
	if _, err := service.Remove(ctx,
		auth.UserPrincipal{UserID: machineOwner, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember},
		ownedByMember, RuntimeRemovalInput{},
	); err != nil {
		t.Fatalf("the machine's registrant Remove() error = %v", err)
	}
}

// The target is the caller's own live runtime of the same provider;
// anything else is a not-found, and a mismatched provider is its own
// refusal. Every one of them writes nothing.
func TestRemoveRuntimeRejectsAnUnusableTarget(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log, _ := mustRemovalService(t, store)
	setRuntimeOffline(t, ctx, pool, fixture.RuntimeA)
	removedTarget := seedRuntime(t, ctx, pool, fixture, "daemon-r", "codex", fixture.UserA)
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET status = 'offline', removed_at = now() WHERE id = $1`, removedTarget)
	member := seedMember(t, ctx, pool, fixture, "member@example.com", "member")
	theirMachine := seedRuntime(t, ctx, pool, fixture, "daemon-m", "codex", member)
	otherProvider := seedRuntime(t, ctx, pool, fixture, "daemon-claude", "claude", fixture.UserA)

	for _, test := range []struct {
		name   string
		target uuid.UUID
		want   error
	}{
		{"another company's runtime", fixture.RuntimeB, ErrRuntimeNotFound},
		{"the runtime being removed", fixture.RuntimeA, ErrRuntimeNotFound},
		{"a runtime already removed", removedTarget, ErrRuntimeNotFound},
		{"an unknown runtime", dbtest.NewUUID(t), ErrRuntimeNotFound},
		{"another member's runtime", theirMachine, ErrRuntimeNotFound},
		{"a runtime of the other provider", otherProvider, ErrBindingProviderMismatch},
	} {
		target := test.target
		if _, err := service.Remove(ctx, ownerOf(fixture), fixture.RuntimeA, RuntimeRemovalInput{
			TargetRuntimeID: &target,
		}); !errors.Is(err, test.want) {
			t.Fatalf("%s: Remove() error = %v, want %v", test.name, err, test.want)
		}
	}
	assertNothingWritten(t, ctx, pool, fixture, log)
}

// An archived Agent's binding moves with the rest: a template's archive
// state says nothing about which machine its member runs it on, and a
// restore should not strand them on a removed row.
func TestRemoveRuntimeMovesArchivedAgentsBindingsToo(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, _, _ := mustRemovalService(t, store)
	archived := seedTemplateAgent(t, ctx, pool, fixture, "Archived", true)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, archived, fixture.UserA, fixture.RuntimeA, "codex")
	target := seedRuntime(t, ctx, pool, fixture, "daemon-s", "codex", fixture.UserA)
	setRuntimeOffline(t, ctx, pool, fixture.RuntimeA)

	receipt, err := service.Remove(ctx, ownerOf(fixture), fixture.RuntimeA, RuntimeRemovalInput{TargetRuntimeID: &target})
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if receipt.MovedBindingCount != 2 {
		t.Fatalf("moved bindings = %d, want the archived Agent's too", receipt.MovedBindingCount)
	}
	if got := readBindingRuntime(t, ctx, pool, archived, fixture.UserA); got != target {
		t.Fatalf("archived Agent's binding = %s, want the target", got)
	}
}

// The removed row leaves every runtime read and the claim path, its run
// history stays readable, and the same machine registering again revives it.
func TestRemoveRuntimeHidesTheRowAndKeepsItsHistory(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, _, _ := mustRemovalService(t, store)
	setRuntimeOffline(t, ctx, pool, fixture.RuntimeA)
	historic := seedRun(t, ctx, pool, fixture, fixture.AgentA, fixture.RuntimeA, fixture.UserA, "completed")

	if _, err := service.Remove(ctx, ownerOf(fixture), fixture.RuntimeA, RuntimeRemovalInput{}); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if page, err := service.List(ctx, ownerOf(fixture), RuntimeListOptions{}); err != nil || len(page.Items) != 0 {
		t.Fatalf("List() after removal = %+v, %v; want the row gone", page.Items, err)
	}
	if _, err := service.Get(ctx, ownerOf(fixture), fixture.RuntimeA); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("Get() after removal error = %v, want ErrRuntimeNotFound", err)
	}
	if runtimeID, status, _ := readRun(t, ctx, pool, historic); runtimeID != fixture.RuntimeA || status != "completed" {
		t.Fatalf("run history after removal = %s / %s, want the machine's history intact", runtimeID, status)
	}

	// The same machine registering the same runtime again revives the row,
	// which is what a deliberate restart means.
	registered, err := service.RegisterRuntimes(ctx, RuntimeRegistration{
		CompanyID: fixture.CompanyA, OwnerUserID: fixture.UserA, DaemonID: fixture.DaemonA,
		DeviceName: "MacBook Pro",
		Runtimes:   []RuntimeRegistrationSpec{{Name: "Runtime A", Provider: "codex", Version: "1.0.0"}},
	})
	if err != nil || len(registered) != 1 || registered[0].ID != fixture.RuntimeA {
		t.Fatalf("re-registration = %+v, %v; want the same row revived", registered, err)
	}
	if removedAt, status := readRemovedAt(t, ctx, pool, fixture.RuntimeA); removedAt != nil || status != "online" {
		t.Fatalf("revived runtime = %v / %s, want removed_at cleared and online", removedAt, status)
	}
}
