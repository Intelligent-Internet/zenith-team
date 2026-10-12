//go:build integration

package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/events"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"boardly.dev/boardly/server/pkg/protocol"
)

func TestRuntimeServiceListsCanonicalCompanyProjection(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	deferredAgentID := uuid.Must(uuid.NewV7())
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runtimes
		SET device_info = 'macOS arm64', metadata = '{"arch":"arm64","os":"darwin"}'::jsonb
		WHERE id = $1`, fixture.RuntimeA)
	dbtest.SeedBoundAgent(t, ctx, pool, deferredAgentID, fixture.CompanyA, "Deferred Agent", "codex",
		&fixture.AgentA, fixture.UserA, fixture.RuntimeA)
	// runs.id is a bigserial: let the sequence assign both ids. Both serve a
	// task, so both hold the task lane.
	dbtest.MustExec(
		t, ctx, pool, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id, status
		, run_type) VALUES
			($1, $2, $3, $4, $5, 'queued', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 )),
			($1, $6, $3, $4, $5, 'deferred', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))`,
		fixture.CompanyA,
		fixture.AgentA,
		fixture.TaskA,
		fixture.ConversationA,
		fixture.RuntimeA,
		deferredAgentID,
	)
	service, err := NewRuntimeService(RuntimeServiceOptions{
		Store: store, Bus: mustAgentTestBus(t),
		ModelDiscoveries: NewInMemoryModelDiscoveryStore(ModelDiscoveryStoreOptions{}),
	})
	if err != nil {
		t.Fatalf("NewRuntimeService() error = %v", err)
	}

	page, err := service.List(ctx, auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}, RuntimeListOptions{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("List().Items = %d, want 1", len(page.Items))
	}
	runtime := page.Items[0]
	if runtime.ID != fixture.RuntimeA || runtime.CompanyID != fixture.CompanyA ||
		runtime.DaemonID != fixture.DaemonA ||
		runtime.Provider != "codex" || runtime.Status != "online" ||
		runtime.DeviceInfo != "macOS arm64" {
		t.Fatalf("List().Items[0] = %+v", runtime)
	}
	if runtime.Owner == nil || runtime.Owner.ID != fixture.UserA ||
		runtime.Owner.Kind != protocol.ActorKindUser || runtime.Owner.DisplayName != "Schema A" {
		t.Fatalf("List().Items[0].Owner = %+v", runtime.Owner)
	}
	if runtime.LastSeenAt == nil || runtime.CreatedAt.Location() != time.UTC ||
		runtime.UpdatedAt.Location() != time.UTC || runtime.LastSeenAt.Location() != time.UTC {
		t.Fatalf("runtime timestamps are not normalized to UTC: %+v", runtime)
	}
	// A row registered before the daemon announced versions projects empty
	// versions and a terminal launch, never an error.
	if runtime.ProviderVersion != "" || runtime.CLIVersion != "" ||
		runtime.LaunchedBy != RuntimeLaunchedByCLI {
		t.Fatalf("List().Items[0] registration metadata = %+v", runtime)
	}

	filtered, err := service.List(ctx, auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember,
	}, RuntimeListOptions{Provider: stringPointer("claude")})
	if err != nil {
		t.Fatalf("List(filtered) error = %v", err)
	}
	if len(filtered.Items) != 0 {
		t.Fatalf("List(filtered).Items = %+v, want empty", filtered.Items)
	}

	// A removed row is off the roster and off the detail read, unfiltered.
	dbtest.MustExec(t, ctx, pool, `UPDATE runtimes SET removed_at = now() WHERE id = $1`, fixture.RuntimeA)
	owner := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	afterRemoval, err := service.List(ctx, owner, RuntimeListOptions{})
	if err != nil || len(afterRemoval.Items) != 0 {
		t.Fatalf("List() after removal = %+v, %v; want empty", afterRemoval.Items, err)
	}
	if _, err := service.Get(ctx, owner, fixture.RuntimeA); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("Get(removed) error = %v, want ErrRuntimeNotFound", err)
	}
}

func TestRuntimeServiceGetReadsOneRuntimeFencedToTheCompany(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runtimes
		SET metadata = '{"version":"1.2.3","cliVersion":"0.9.0","launchedBy":"desktop","deviceName":"MacBook Pro"}'::jsonb
		WHERE id = $1`, fixture.RuntimeA)
	service, err := NewRuntimeService(RuntimeServiceOptions{
		Store: store, Bus: mustAgentTestBus(t),
		ModelDiscoveries: NewInMemoryModelDiscoveryStore(ModelDiscoveryStoreOptions{}),
	})
	if err != nil {
		t.Fatalf("NewRuntimeService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}

	runtime, err := service.Get(ctx, principal, fixture.RuntimeA)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if runtime.ID != fixture.RuntimeA || runtime.CompanyID != fixture.CompanyA ||
		runtime.DaemonID != fixture.DaemonA || runtime.Provider != "codex" || runtime.Status != "online" {
		t.Fatalf("Get() = %+v", runtime)
	}
	if runtime.Owner == nil || runtime.Owner.ID != fixture.UserA ||
		runtime.Owner.Kind != protocol.ActorKindUser || runtime.Owner.DisplayName != "Schema A" {
		t.Fatalf("Get().Owner = %+v", runtime.Owner)
	}
	if runtime.ProviderVersion != "1.2.3" || runtime.CLIVersion != "0.9.0" ||
		runtime.LaunchedBy != RuntimeLaunchedByDesktop {
		t.Fatalf("Get() registration metadata = %+v", runtime)
	}
	if runtime.LastSeenAt == nil || runtime.CreatedAt.Location() != time.UTC ||
		runtime.UpdatedAt.Location() != time.UTC || runtime.LastSeenAt.Location() != time.UTC {
		t.Fatalf("runtime timestamps are not normalized to UTC: %+v", runtime)
	}

	// The query repeats the company fence, so another company's runtime and
	// an unknown id are the same answer.
	if _, err := service.Get(ctx, principal, fixture.RuntimeB); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("Get(foreign) error = %v, want ErrRuntimeNotFound", err)
	}
	if _, err := service.Get(ctx, principal, uuid.Must(uuid.NewV7())); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("Get(unknown) error = %v, want ErrRuntimeNotFound", err)
	}
}

func TestRuntimeServiceModelDiscoveryUsesExactOnlineRuntime(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	bus := mustAgentTestBus(t)
	notifier := &pendingWorkRecorder{}
	if err := bus.Subscribe(TopicRuntime,
		func(_ context.Context, event events.Event) error {
			if event.Name != EventRuntimePendingWork {
				return nil
			}
			payload, err := events.PayloadOf[PendingWork](event)
			if err != nil {
				return err
			}
			notifier.runtimeID = payload.RuntimeID
			notifier.kind = payload.Kind
			notifier.calls++
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
	owner := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}

	request, err := service.InitiateModelDiscovery(ctx, owner, fixture.RuntimeA)
	if err != nil {
		t.Fatalf("InitiateModelDiscovery() error = %v", err)
	}
	if request.RuntimeID != fixture.RuntimeA || request.Status != ModelDiscoveryPending ||
		notifier.runtimeID != fixture.RuntimeA.String() || notifier.kind != ModelDiscoveryPendingWork {
		t.Fatalf("request/notifier = %+v / %+v", request, notifier)
	}
	coalesced, err := service.InitiateModelDiscovery(ctx, owner, fixture.RuntimeA)
	if err != nil {
		t.Fatalf("InitiateModelDiscovery(coalesced) error = %v", err)
	}
	if coalesced.ID != request.ID {
		t.Fatalf("coalesced id = %s, want %s", coalesced.ID, request.ID)
	}

	foreign := owner
	foreign.CompanyID = fixture.CompanyB
	if _, err := service.InitiateModelDiscovery(
		ctx,
		foreign,
		fixture.RuntimeA,
	); !errors.Is(
		err,
		ErrRuntimeNotFound,
	) {
		t.Fatalf("InitiateModelDiscovery(foreign) error = %v, want ErrRuntimeNotFound", err)
	}
	// Discovery is the machine's to grant, not a role's (ADR 0013): the
	// registrant may always ask, whatever their role; another member cannot
	// even see the private runtime.
	member := owner
	member.Role = auth.CompanyRoleMember
	if _, err := service.InitiateModelDiscovery(ctx, member, fixture.RuntimeA); err != nil {
		t.Fatalf("InitiateModelDiscovery(registrant as member) error = %v", err)
	}
	bystander := principalFor(fixture, seedMember(t, ctx, pool, fixture, "eli.test", "member"), auth.CompanyRoleMember)
	if _, err := service.InitiateModelDiscovery(ctx, bystander, fixture.RuntimeA); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("InitiateModelDiscovery(another member, private runtime) error = %v, want ErrRuntimeNotFound", err)
	}
	if err := service.RequestRefresh(ctx, bystander, fixture.RuntimeA); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("RequestRefresh(another member, private runtime) error = %v, want ErrRuntimeNotFound", err)
	}
	// The machine is only ever the registrant's to ask (ADR 0014): a member
	// asks their own, and nobody else's, whatever their role.
	theirs := seedRuntime(t, ctx, pool, fixture, "daemon-eli", "codex", bystander.UserID)
	if _, err := service.InitiateModelDiscovery(ctx, bystander, theirs); err != nil {
		t.Fatalf("InitiateModelDiscovery(their own runtime) error = %v", err)
	}
	if err := service.RequestRefresh(ctx, bystander, theirs); err != nil {
		t.Fatalf("RequestRefresh(their own runtime) error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runtimes SET status = 'offline' WHERE id = $1`, fixture.RuntimeA)
	if _, err := service.InitiateModelDiscovery(ctx, owner, fixture.RuntimeA); !errors.Is(err, ErrRuntimeOffline) {
		t.Fatalf("InitiateModelDiscovery(offline) error = %v, want ErrRuntimeOffline", err)
	}
}

// pendingWorkRecorder captures the heartbeat-now hint model discovery
// publishes, which used to be a direct call on a notifier it held.
type pendingWorkRecorder struct {
	runtimeID string
	kind      string
	calls     int
}
