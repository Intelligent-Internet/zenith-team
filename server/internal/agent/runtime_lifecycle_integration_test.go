//go:build integration

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

func mustRuntimeLifecycleService(t *testing.T, store *db.Store) (*DefaultRuntimeService, *agentEventLog) {
	t.Helper()
	bus := mustAgentTestBus(t)
	log := recordAgentEvents(t, bus)
	service, err := NewRuntimeService(RuntimeServiceOptions{
		Store:            store,
		Bus:              bus,
		ModelDiscoveries: NewInMemoryModelDiscoveryStore(ModelDiscoveryStoreOptions{}),
	})
	if err != nil {
		t.Fatalf("NewRuntimeService() error = %v", err)
	}
	return service, log
}

// rosterEventsForCompany narrows the roster log to one Company. The payload's
// own companyId is the routing fact the realtime bridge reads.
func rosterEventsForCompany(log *agentEventLog, companyID uuid.UUID) []DaemonRoster {
	var matched []DaemonRoster
	for _, event := range log.roster.snapshot() {
		if event.Payload.CompanyID == companyID.String() {
			matched = append(matched, event.Payload)
		}
	}
	return matched
}

func rosterEvents(log *agentEventLog, companyID uuid.UUID, action string) []DaemonRoster {
	var matched []DaemonRoster
	for _, payload := range rosterEventsForCompany(log, companyID) {
		if payload.Action == action {
			matched = append(matched, payload)
		}
	}
	return matched
}

// agentStatusEvents narrows the availability log to one Company. CompanyID
// rides the payload off the wire purely so the bridge can route on it.
func agentStatusEvents(log *agentEventLog, companyID uuid.UUID) []AvailabilityChange {
	var matched []AvailabilityChange
	for _, event := range log.status.snapshot() {
		if event.Payload.CompanyID == companyID {
			matched = append(matched, event.Payload)
		}
	}
	return matched
}

func TestRegisterRuntimesCommitsTheWholeRosterInOneTransaction(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log := mustRuntimeLifecycleService(t, store)

	registered, err := service.RegisterRuntimes(ctx, RuntimeRegistration{
		CompanyID:   fixture.CompanyA,
		OwnerUserID: fixture.UserA,
		DaemonID:    fixture.DaemonA,
		DeviceName:  "MacBook Pro",
		CLIVersion:  "0.9.0",
		LaunchedBy:  "desktop",
		Runtimes: []RuntimeRegistrationSpec{
			{Provider: "codex", Version: "1.2.3"},
			{Name: "  Claude Runtime  ", Provider: "claude", Status: "offline"},
		},
	})
	if err != nil {
		t.Fatalf("RegisterRuntimes() error = %v", err)
	}
	if len(registered) != 2 {
		t.Fatalf("RegisterRuntimes() registered %d runtimes, want 2", len(registered))
	}

	// The codex spec upserts onto the seeded RuntimeA identity
	// (company, daemon, provider); the claude spec inserts a new row.
	codex, claude := registered[0], registered[1]
	if codex.ID != fixture.RuntimeA {
		t.Fatalf("codex runtime id = %s, want the seeded %s", codex.ID, fixture.RuntimeA)
	}
	if codex.Name != "codex (MacBook Pro)" || codex.Status != "online" ||
		codex.DeviceInfo != "MacBook Pro · 1.2.3" {
		t.Fatalf("codex runtime = %+v", codex)
	}
	if claude.Name != "Claude Runtime" || claude.Status != "offline" || claude.DeviceInfo != "MacBook Pro" {
		t.Fatalf("claude runtime = %+v", claude)
	}
	// The write path and the projection agree on the metadata keys: the
	// registration announcement round-trips into the returned projection.
	if codex.ProviderVersion != "1.2.3" || codex.CLIVersion != "0.9.0" ||
		codex.LaunchedBy != RuntimeLaunchedByDesktop {
		t.Fatalf("codex runtime registration metadata = %+v", codex)
	}
	// The whole announcement is stored on the row; only the three fields
	// above are served on the user plane. The device name has its own column
	// because SQL reads it; the metadata document never repeats it.
	var metadataRaw []byte
	var deviceName *string
	if err := pool.QueryRow(
		ctx,
		`SELECT metadata, device_name FROM runtimes WHERE id = $1`,
		codex.ID,
	).Scan(&metadataRaw, &deviceName); err != nil {
		t.Fatalf("read codex runtime metadata: %v", err)
	}
	var metadata map[string]string
	if err := json.Unmarshal(metadataRaw, &metadata); err != nil ||
		metadata["version"] != "1.2.3" || metadata["cliVersion"] != "0.9.0" ||
		metadata["launchedBy"] != "desktop" || metadata["billingMode"] != "unknown" || len(metadata) != 4 ||
		deviceName == nil || *deviceName != "MacBook Pro" {
		t.Fatalf("codex metadata = %s, device name = %v, error = %v", metadataRaw, deviceName, err)
	}

	roster := rosterEvents(log, fixture.CompanyA, protocol.DaemonRosterActionRegister)
	if len(roster) != 1 || roster[0].DaemonID != fixture.DaemonA || len(roster[0].RuntimeIDs) != 2 {
		t.Fatalf("register roster events = %+v, want one with both runtime ids", roster)
	}
	// Every registered runtime fans out, but the offline claude runtime has
	// no bound agents, so only the codex registration yields a frame: one
	// batched event carrying AgentA, the one agent bound to it.
	statuses := agentStatusEvents(log, fixture.CompanyA)
	if len(statuses) != 1 || len(statuses[0].Agents) != 1 ||
		statuses[0].Agents[0].AgentID != fixture.AgentA ||
		!statuses[0].Agents[0].RuntimeAvailable {
		t.Fatalf("agent.status events = %+v, want one available entry for AgentA", statuses)
	}

	// A removed row announced again by the same machine comes back: the
	// upsert keeps the identity and clears removed_at — the user restarted
	// the daemon on purpose.
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET removed_at = now(), status = 'offline' WHERE id = $1`, fixture.RuntimeA)
	reregistered, err := service.RegisterRuntimes(ctx, RuntimeRegistration{
		CompanyID:   fixture.CompanyA,
		OwnerUserID: fixture.UserA,
		DaemonID:    fixture.DaemonA,
		DeviceName:  "MacBook Pro",
		Runtimes:    []RuntimeRegistrationSpec{{Provider: "codex", Version: "1.2.4"}},
	})
	if err != nil {
		t.Fatalf("RegisterRuntimes(removed row) error = %v", err)
	}
	if len(reregistered) != 1 || reregistered[0].ID != fixture.RuntimeA || reregistered[0].Status != "online" {
		t.Fatalf("RegisterRuntimes(removed row) = %+v, want the same id back online", reregistered)
	}
	var removedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT removed_at FROM runtimes WHERE id = $1`, fixture.RuntimeA).
		Scan(&removedAt); err != nil {
		t.Fatalf("read removed_at after re-registration: %v", err)
	}
	if removedAt != nil {
		t.Fatalf("removed_at after re-registration = %v, want NULL", removedAt)
	}
	if roster := rosterEvents(log, fixture.CompanyA, protocol.DaemonRosterActionRegister); len(roster) != 2 {
		t.Fatalf("register roster events = %d, want one per registration", len(roster))
	}
}

func TestRegisterRuntimesRollsBackEveryRuntimeWhenOneUpsertFails(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log := mustRuntimeLifecycleService(t, store)

	// The first spec would rename the seeded codex runtime; the second
	// violates the runtimes_provider_check constraint. One transaction means
	// the first upsert must roll back with it.
	_, err := service.RegisterRuntimes(ctx, RuntimeRegistration{
		CompanyID:   fixture.CompanyA,
		OwnerUserID: fixture.UserA,
		DaemonID:    fixture.DaemonA,
		DeviceName:  "MacBook Pro",
		Runtimes: []RuntimeRegistrationSpec{
			{Name: "Renamed Codex", Provider: "codex"},
			{Provider: "unsupported-provider"},
		},
	})
	if err == nil {
		t.Fatal("RegisterRuntimes() error = nil, want the constraint violation")
	}

	var name, status string
	if err := pool.QueryRow(ctx,
		`SELECT name, status FROM runtimes WHERE id = $1`, fixture.RuntimeA,
	).Scan(&name, &status); err != nil {
		t.Fatalf("read seeded runtime: %v", err)
	}
	if name != "Runtime A" {
		t.Fatalf("seeded runtime name = %q after failed registration, want the rename rolled back", name)
	}
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM runtimes WHERE company_id = $1`, fixture.CompanyA,
	).Scan(&count); err != nil {
		t.Fatalf("count company runtimes: %v", err)
	}
	if count != 1 {
		t.Fatalf("company runtimes = %d after failed registration, want 1", count)
	}
	if published := log.total(); published != 0 {
		t.Fatalf("events published after failed registration = %d, want none", published)
	}
}

func TestDeregisterRuntimesSkipsForeignRuntimesAndFansOutOffline(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log := mustRuntimeLifecycleService(t, store)

	if err := service.DeregisterRuntimes(ctx, auth.UserPrincipal{UserID: fixture.UserA}, RuntimeDeregistration{
		DaemonID: fixture.DaemonA,
		RuntimeIDs: []string{
			fixture.RuntimeA.String(),
			fixture.RuntimeB.String(), // company B: the caller is not a member
			"not-a-uuid",
		},
	}); err != nil {
		t.Fatalf("DeregisterRuntimes() error = %v, want converged ids skipped without error", err)
	}

	var statusA, statusB string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM runtimes WHERE id = $1`, fixture.RuntimeA).Scan(&statusA); err != nil {
		t.Fatalf("read runtime A: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT status FROM runtimes WHERE id = $1`, fixture.RuntimeB).Scan(&statusB); err != nil {
		t.Fatalf("read runtime B: %v", err)
	}
	if statusA != "offline" || statusB != "online" {
		t.Fatalf("statuses after deregister = %q/%q, want offline/online", statusA, statusB)
	}

	roster := rosterEvents(log, fixture.CompanyA, protocol.DaemonRosterActionDeregister)
	if len(roster) != 1 {
		t.Fatalf("deregister roster events for company A = %+v, want exactly one", roster)
	}
	if foreign := rosterEventsForCompany(log, fixture.CompanyB); len(foreign) != 0 {
		t.Fatalf("company B received roster events: %+v, want none", foreign)
	}
	statuses := agentStatusEvents(log, fixture.CompanyA)
	if len(statuses) != 1 || len(statuses[0].Agents) != 1 ||
		statuses[0].Agents[0].AgentID != fixture.AgentA ||
		statuses[0].Agents[0].RuntimeAvailable {
		t.Fatalf("agent.status events = %+v, want one unavailable entry for AgentA", statuses)
	}
}

// A member's daemon that did not register the runtime cannot take
// it offline. The foreign daemon id is skipped like an unknown id — the batch
// still answers without error — and nothing about the runtime changes.
func TestDeregisterRuntimesSkipsRuntimesOfAnotherDaemon(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log := mustRuntimeLifecycleService(t, store)

	if err := service.DeregisterRuntimes(ctx, auth.UserPrincipal{UserID: fixture.UserA}, RuntimeDeregistration{
		DaemonID:   "daemon-m",
		RuntimeIDs: []string{fixture.RuntimeA.String()},
	}); err != nil {
		t.Fatalf("DeregisterRuntimes() error = %v, want the foreign daemon's id skipped without error", err)
	}

	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM runtimes WHERE id = $1`, fixture.RuntimeA).Scan(&status); err != nil {
		t.Fatalf("read runtime A: %v", err)
	}
	if status != "online" {
		t.Fatalf("runtime A status = %q after a foreign daemon's deregister, want online", status)
	}
	if log.total() != 0 {
		t.Fatalf("events published = %d, want none when nothing was taken offline", log.total())
	}
}

// A runtime is its registrant's alone (ADR 0014): another member's daemon
// cannot take it offline, even when it sends no daemon id and so skips the
// daemon match. The id is skipped like an unknown one and nothing changes.
func TestDeregisterRuntimesSkipsAnotherMembersRuntime(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log := mustRuntimeLifecycleService(t, store)
	colleague := seedMember(t, ctx, pool, fixture, "deregister-colleague@example.com", "member")

	if err := service.DeregisterRuntimes(ctx, auth.UserPrincipal{UserID: colleague}, RuntimeDeregistration{
		RuntimeIDs: []string{fixture.RuntimeA.String()},
	}); err != nil {
		t.Fatalf("DeregisterRuntimes() error = %v, want another member's runtime skipped without error", err)
	}

	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM runtimes WHERE id = $1`, fixture.RuntimeA).Scan(&status); err != nil {
		t.Fatalf("read runtime A: %v", err)
	}
	if status != "online" {
		t.Fatalf("runtime A status = %q after another member's deregister, want online", status)
	}
	if log.total() != 0 {
		t.Fatalf("events published = %d, want none when nothing was taken offline", log.total())
	}
}

// The deregister loop writes on the caller's handle: when the surrounding
// transaction fails, every offline write it made is gone and nothing was
// announced — the daemon's 200 receipt means the whole batch applied.
func TestDeregisterRuntimesInTxWritesRollBackWithTheTransaction(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	_, log := mustRuntimeLifecycleService(t, store)

	errAbort := errors.New("abort after the offline write")
	err := store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		offlined, err := deregisterRuntimesInTx(
			ctx,
			tx,
			auth.UserPrincipal{UserID: fixture.UserA},
			RuntimeDeregistration{
				DaemonID:   fixture.DaemonA,
				RuntimeIDs: []string{fixture.RuntimeA.String()},
			},
		)
		if err != nil {
			t.Fatalf("deregisterRuntimesInTx() error = %v", err)
		}
		if len(offlined) != 1 || offlined[0].ID != fixture.RuntimeA {
			t.Fatalf("offlined = %+v, want RuntimeA", offlined)
		}
		return errAbort
	})
	if !errors.Is(err, errAbort) {
		t.Fatalf("WithinTx() error = %v, want the abort sentinel", err)
	}

	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM runtimes WHERE id = $1`, fixture.RuntimeA).Scan(&status); err != nil {
		t.Fatalf("read runtime A: %v", err)
	}
	if status != "online" {
		t.Fatalf("runtime A status = %q after rollback, want online", status)
	}
	if log.total() != 0 {
		t.Fatalf("events published = %d, want none for a rolled-back batch", log.total())
	}
}

// A departing member's bindings go with them (ADR 0014): the participant
// deletes every binding they held in the company and stages one
// binding_changed per deleted row — to them alone, since a binding is
// nobody else's business — and does nothing for a company where they had
// none.
func TestRevokeRuntimeOwnerInTxDeletesTheMembersBindings(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)

	secondAgent := dbtest.NewUUID(t)
	dbtest.SeedAgent(t, ctx, pool, secondAgent, fixture.CompanyA, "Agent A2", "codex", &fixture.AgentA)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, secondAgent, fixture.UserA, fixture.RuntimeA, "codex")
	// A colleague's binding of the same Agent on their own machine is not
	// the departing member's to lose.
	member := seedMember(t, ctx, pool, fixture, "member@example.com", "member")
	theirs := seedRuntime(t, ctx, pool, fixture, "daemon-m", "codex", member)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, member, theirs, "codex")

	var pending []events.Event
	err := store.WithinRawTx(ctx, pgx.TxOptions{}, func(raw generated.DBTX, tx *generated.Queries) error {
		// The revocation and the staging share one transaction, as they do in
		// account deletion: the predicate must see the uncommitted 'revoked'.
		if _, err := raw.Exec(ctx,
			`UPDATE company_members SET status = 'revoked' WHERE company_id = $1 AND user_id = $2`,
			fixture.CompanyA, fixture.UserA); err != nil {
			return err
		}
		if err := RevokeRuntimeOwnerInTx(ctx, tx, &pending, fixture.CompanyA, fixture.UserA); err != nil {
			return err
		}
		return RevokeRuntimeOwnerInTx(ctx, tx, &pending, fixture.CompanyB, fixture.UserA)
	})
	if err != nil {
		t.Fatalf("revoke inside the revocation transaction: %v", err)
	}

	if len(pending) != 2 {
		t.Fatalf("pending = %+v, want one frame per deleted binding in company A", pending)
	}
	seen := map[uuid.UUID]bool{}
	for _, frame := range pending {
		if frame.Name != EventAgentBindingChanged || frame.EntityType != Topic ||
			frame.CompanyID != fixture.CompanyA {
			t.Fatalf("frame = %+v, want an agent.binding_changed for company A", frame)
		}
		payload, err := events.PayloadOf[BindingChange](frame)
		if err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.Binding != nil || payload.UserID != fixture.UserA {
			t.Fatalf("payload = %+v, want an explicit null for the departing member", payload)
		}
		seen[payload.AgentID] = true
	}
	if !seen[fixture.AgentA] || !seen[secondAgent] {
		t.Fatalf("frames named %v, want both of the member's bindings", seen)
	}

	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_runtime_bindings WHERE company_id = $1 AND user_id = $2`,
		fixture.CompanyA, fixture.UserA,
	).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("the departing member's bindings = %d, %v; want none", remaining, err)
	}
	var colleagues int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_runtime_bindings WHERE company_id = $1 AND user_id = $2`,
		fixture.CompanyA, member,
	).Scan(&colleagues); err != nil || colleagues != 1 {
		t.Fatalf("the colleague's bindings = %d, %v; want theirs kept", colleagues, err)
	}
}

func TestMarkStaleRuntimesOfflineFlipsOnlyStaleRuntimes(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log := mustRuntimeLifecycleService(t, store)
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET last_seen_at = now() - interval '10 minutes' WHERE id = $1`,
		fixture.RuntimeA)

	flipped, err := service.MarkStaleRuntimesOffline(ctx)
	if err != nil {
		t.Fatalf("MarkStaleRuntimesOffline() error = %v", err)
	}
	if flipped != 1 {
		t.Fatalf("MarkStaleRuntimesOffline() = %d, want 1", flipped)
	}

	var statusA, statusB string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM runtimes WHERE id = $1`, fixture.RuntimeA).Scan(&statusA); err != nil {
		t.Fatalf("read runtime A: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT status FROM runtimes WHERE id = $1`, fixture.RuntimeB).Scan(&statusB); err != nil {
		t.Fatalf("read runtime B: %v", err)
	}
	if statusA != "offline" || statusB != "online" {
		t.Fatalf("statuses after sweep = %q/%q, want offline/online", statusA, statusB)
	}

	roster := rosterEvents(log, fixture.CompanyA, protocol.DaemonRosterActionStaleSweep)
	if len(roster) != 1 {
		t.Fatalf("stale-sweep roster events = %+v, want exactly one for company A", roster)
	}
	statuses := agentStatusEvents(log, fixture.CompanyA)
	if len(statuses) != 1 || len(statuses[0].Agents) != 1 ||
		statuses[0].Agents[0].AgentID != fixture.AgentA ||
		statuses[0].Agents[0].RuntimeAvailable {
		t.Fatalf("agent.status events = %+v, want one unavailable entry for AgentA", statuses)
	}

	// The offline notice is scoped to a demotion that costs work: the
	// fixture's only run is already completed, so the owner is told nothing.
	var notices int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_items WHERE kind = 'runtime_offline'`).
		Scan(&notices); err != nil {
		t.Fatalf("count runtime_offline items: %v", err)
	}
	if notices != 0 {
		t.Fatalf("runtime_offline items after a sweep with nothing in flight = %d, want 0",
			notices)
	}

	// A second sweep finds nothing and publishes nothing more.
	flipped, err = service.MarkStaleRuntimesOffline(ctx)
	if err != nil || flipped != 0 {
		t.Fatalf("second MarkStaleRuntimesOffline() = %d, %v, want 0, nil", flipped, err)
	}
}

// TestAgentStatusEventBatchesTheBindersRosterInOneFrame pins the two
// properties of the agent.status fanout under ADR 0014: every Agent the
// registrant has set up on the flipping machine rides ONE frame addressed
// to them, and runtimeAvailable is the same composite the Agent projection
// serves that member — so a colleague's binding of the same Agent on their
// own machine is nowhere in it.
func TestAgentStatusEventBatchesTheBindersRosterInOneFrame(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log := mustRuntimeLifecycleService(t, store)

	secondAgent := dbtest.NewUUID(t)
	dbtest.SeedAgent(t, ctx, pool, secondAgent, fixture.CompanyA, "Agent A2", "codex", &fixture.AgentA)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, secondAgent, fixture.UserA, fixture.RuntimeA, "codex")
	// A colleague runs the same Agent on their own machine.
	member := seedMember(t, ctx, pool, fixture, "member@example.com", "member")
	theirs := seedRuntime(t, ctx, pool, fixture, "daemon-m", "codex", member)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, member, theirs, "codex")
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET status = 'offline' WHERE id = $1`, fixture.RuntimeA)

	if _, err := service.RegisterRuntimes(ctx, RuntimeRegistration{
		CompanyID:   fixture.CompanyA,
		OwnerUserID: fixture.UserA,
		DaemonID:    fixture.DaemonA,
		Runtimes:    []RuntimeRegistrationSpec{{Provider: "codex"}},
	}); err != nil {
		t.Fatalf("RegisterRuntimes() error = %v", err)
	}

	statuses := agentStatusEvents(log, fixture.CompanyA)
	if len(statuses) != 1 {
		t.Fatalf("agent.status events = %d, want the binder's whole roster in one event", len(statuses))
	}
	if statuses[0].UserID != fixture.UserA {
		t.Fatalf("agent.status binder = %s, want the registrant %s", statuses[0].UserID, fixture.UserA)
	}
	entries := statuses[0].Agents
	// Both agent ids are random, so which one sorts first is a coin flip.
	// The fanout orders by agent id, so derive the order rather than assume it.
	firstID, lastID := fixture.AgentA, secondAgent
	if lastID.String() < firstID.String() {
		firstID, lastID = lastID, firstID
	}
	if len(entries) != 2 || entries[0].AgentID != firstID || entries[1].AgentID != lastID {
		t.Fatalf("agent.status entries = %+v, want both of the binder's agents in id order", entries)
	}
	for _, entry := range entries {
		if !entry.RuntimeAvailable {
			t.Fatalf("agent %s reported unavailable on the machine that just came online", entry.AgentID)
		}
	}

	// The colleague's own machine going offline is their own frame, naming
	// only the Agent they run there.
	dbtest.MustExec(t, ctx, pool, `UPDATE runtimes SET status = 'offline' WHERE id = $1`, theirs)
	service.publishAgentStatuses(ctx, fixture.CompanyA, theirs)
	statuses = agentStatusEvents(log, fixture.CompanyA)
	if len(statuses) != 2 {
		t.Fatalf("agent.status events = %d, want one per machine", len(statuses))
	}
	theirFrame := statuses[1]
	if theirFrame.UserID != member || len(theirFrame.Agents) != 1 ||
		theirFrame.Agents[0].AgentID != fixture.AgentA || theirFrame.Agents[0].RuntimeAvailable {
		t.Fatalf("the colleague's frame = %+v, want their one Agent, unavailable", theirFrame)
	}
}

func TestHeartbeatTouchesLivenessAndKeepsDenialSemantics(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log, wakeups := mustRemovalService(t, store)
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET status = 'offline', last_seen_at = now() - interval '10 minutes'
		 WHERE id = $1`, fixture.RuntimeA)

	// A non-member is denied, not told the runtime is gone — and nothing is
	// written.
	err := service.Heartbeat(ctx, auth.UserPrincipal{UserID: fixture.UserB}, fixture.RuntimeA, fixture.DaemonA)
	if !errors.Is(err, ErrRuntimeAccessDenied) {
		t.Fatalf("foreign Heartbeat() error = %v, want ErrRuntimeAccessDenied", err)
	}
	// A genuinely missing runtime is the gone signal the daemon prunes on.
	err = service.Heartbeat(ctx, auth.UserPrincipal{UserID: fixture.UserA}, dbtest.NewUUID(t), fixture.DaemonA)
	if !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("missing Heartbeat() error = %v, want ErrRuntimeNotFound", err)
	}
	// A member beating from a daemon that did not register the
	// runtime is denied exactly like a non-member — never told "gone", so
	// the daemon does not prune and re-register — and liveness is untouched.
	err = service.Heartbeat(ctx, auth.UserPrincipal{UserID: fixture.UserA}, fixture.RuntimeA, "daemon-m")
	if !errors.Is(err, ErrRuntimeAccessDenied) {
		t.Fatalf("other-daemon Heartbeat() error = %v, want ErrRuntimeAccessDenied", err)
	}
	var deniedStatus string
	var deniedLastSeen time.Time
	if err := pool.QueryRow(ctx,
		`SELECT status, last_seen_at FROM runtimes WHERE id = $1`, fixture.RuntimeA,
	).Scan(&deniedStatus, &deniedLastSeen); err != nil {
		t.Fatalf("read runtime after refused heartbeats: %v", err)
	}
	if deniedStatus != "offline" || time.Since(deniedLastSeen) < 9*time.Minute {
		t.Fatalf("runtime after refused heartbeats = %q at %s, want offline and untouched",
			deniedStatus, deniedLastSeen)
	}
	if got := agentStatusEvents(log, fixture.CompanyA); len(got) != 0 {
		t.Fatalf("agent.status events after refused heartbeats = %+v, want none", got)
	}

	// The member's beat brings the runtime online and broadcasts the
	// recomputed availability once.
	if err := service.Heartbeat(
		ctx,
		auth.UserPrincipal{UserID: fixture.UserA},
		fixture.RuntimeA,
		fixture.DaemonA,
	); err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
	var status string
	var lastSeen time.Time
	if err := pool.QueryRow(ctx,
		`SELECT status, last_seen_at FROM runtimes WHERE id = $1`, fixture.RuntimeA,
	).Scan(&status, &lastSeen); err != nil {
		t.Fatalf("read runtime after heartbeat: %v", err)
	}
	if status != "online" || time.Since(lastSeen) > time.Minute {
		t.Fatalf("runtime after heartbeat = %q at %s, want online just now", status, lastSeen)
	}
	statuses := agentStatusEvents(log, fixture.CompanyA)
	if len(statuses) != 1 || len(statuses[0].Agents) != 1 ||
		statuses[0].Agents[0].AgentID != fixture.AgentA ||
		!statuses[0].Agents[0].RuntimeAvailable {
		t.Fatalf("agent.status events = %+v, want one available entry for AgentA", statuses)
	}

	work := wakeups.snapshot()
	if len(work) != 1 || work[0].Payload.RuntimeID != fixture.RuntimeA.String() || work[0].Payload.RunID != "" {
		t.Fatalf("return-online wakeups = %+v, want one runtime-wide cache invalidation", work)
	}

	// A beat over an already-online runtime is silent: liveness only.
	if err := service.Heartbeat(
		ctx,
		auth.UserPrincipal{UserID: fixture.UserA},
		fixture.RuntimeA,
		fixture.DaemonA,
	); err != nil {
		t.Fatalf("second Heartbeat() error = %v", err)
	}
	if statuses := agentStatusEvents(log, fixture.CompanyA); len(statuses) != 1 {
		t.Fatalf("agent.status events after steady-state heartbeat = %d, want still 1", len(statuses))
	}

	if work := wakeups.snapshot(); len(work) != 1 {
		t.Fatalf("steady-state wakeups = %+v, want no duplicate wakeup", work)
	}

	// The fact that silence is judged from: the touch reports the status the
	// row held immediately before its own write, so no reader standing
	// between the two can be wrong about whether the machine came back.
	touched, err := store.Queries().TouchRuntimeHeartbeat(ctx, fixture.RuntimeA)
	if err != nil || touched.PreviousStatus != "online" || touched.Status != "online" {
		t.Fatalf("TouchRuntimeHeartbeat() = %+v, error = %v; want online reported before and after",
			touched, err)
	}

	// An empty daemon id is the unfenced rollout form: the transport admits
	// a daemon that sent no X-Zenith-Team-Daemon-ID and the domain skips the
	// daemon check, keeping membership as the only gate.
	if err := service.Heartbeat(ctx, auth.UserPrincipal{UserID: fixture.UserA}, fixture.RuntimeA, ""); err != nil {
		t.Fatalf("unfenced Heartbeat() error = %v", err)
	}

	// A removed row is gone to its daemon: the beat cannot resurrect it,
	// the daemon reads runtime-gone and re-registers, and nothing flips.
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET removed_at = now(), status = 'offline' WHERE id = $1`, fixture.RuntimeA)
	err = service.Heartbeat(ctx, auth.UserPrincipal{UserID: fixture.UserA}, fixture.RuntimeA, fixture.DaemonA)
	if !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("removed Heartbeat() error = %v, want ErrRuntimeNotFound", err)
	}
	var removedStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM runtimes WHERE id = $1`, fixture.RuntimeA).
		Scan(&removedStatus); err != nil {
		t.Fatalf("read removed runtime: %v", err)
	}
	if removedStatus != "offline" {
		t.Fatalf("removed runtime status after a heartbeat = %q, want offline", removedStatus)
	}
	if statuses := agentStatusEvents(log, fixture.CompanyA); len(statuses) != 1 {
		t.Fatalf("agent.status events after a heartbeat over a removed row = %d, want still 1", len(statuses))
	}
}

func TestPruneOfflineRuntimesDeletesOnlyExpiredUnreferencedRuntimes(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log := mustRuntimeLifecycleService(t, store)
	expired := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes
			(id, company_id, daemon_id, name, provider, status, last_seen_at)
		VALUES ($1, $2, 'daemon-expired', 'Expired', 'claude', 'offline',
			now() - interval '8 days')`,
		expired, fixture.CompanyA)

	if err := service.PruneOfflineRuntimes(ctx); err != nil {
		t.Fatalf("PruneOfflineRuntimes() error = %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM runtimes WHERE id = $1`, expired).Scan(&count); err != nil {
		t.Fatalf("count expired runtime: %v", err)
	}
	if count != 0 {
		t.Fatal("expired offline runtime survived the prune")
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM runtimes WHERE id = $1`, fixture.RuntimeA).Scan(&count); err != nil {
		t.Fatalf("count live runtime: %v", err)
	}
	if count != 1 {
		t.Fatal("live runtime was pruned")
	}
	roster := rosterEvents(log, fixture.CompanyA, protocol.DaemonRosterActionRuntimeGC)
	if len(roster) != 1 {
		t.Fatalf("runtime-gc roster events = %+v, want exactly one for company A", roster)
	}
}

// On the server, a registration carrying an unusableReason lands
// offline whatever status the daemon announced, the reason projects on the
// runtime and as the bound agent's `runtime_unusable`, a heartbeat keeps the
// row fresh but offline, and the next registration without the reason
// clears everything.
func TestRegisterRuntimesWithUnusableReasonForcesOfflineProjectsAndClears(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log := mustRuntimeLifecycleService(t, store)
	owner := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	reason := protocol.RuntimeUnusableReason{
		Code:    protocol.RuntimeUnusableCodeBelowMinimumVersion,
		Message: "Codex CLI 0.90.0 is below the minimum 0.100.0",
	}

	registered, err := service.RegisterRuntimes(ctx, RuntimeRegistration{
		CompanyID:   fixture.CompanyA,
		OwnerUserID: fixture.UserA,
		DaemonID:    fixture.DaemonA,
		DeviceName:  "MacBook Pro",
		Runtimes: []RuntimeRegistrationSpec{
			// The daemon says online; the reason wins.
			{Provider: "codex", Version: "0.90.0", Status: "online", UnusableReason: &reason},
			{Provider: "claude", Version: "2.5.0"},
		},
	})
	if err != nil {
		t.Fatalf("RegisterRuntimes() error = %v", err)
	}
	if len(registered) != 2 {
		t.Fatalf("registered %d runtimes, want 2", len(registered))
	}
	codex, claude := registered[0], registered[1]
	if codex.ID != fixture.RuntimeA || codex.Status != "offline" || codex.ProviderVersion != "0.90.0" ||
		codex.UnusableReason == nil || *codex.UnusableReason != reason {
		t.Fatalf(
			"codex runtime = %+v (reason %+v), want offline carrying %+v",
			codex,
			codex.UnusableReason,
			reason,
		)
	}
	if claude.Status != "online" || claude.UnusableReason != nil {
		t.Fatalf("claude runtime = %+v, want online and unaffected", claude)
	}
	var status, code, message string
	if err := pool.QueryRow(ctx,
		`SELECT status, unusable_reason_code, unusable_reason_message FROM runtimes WHERE id = $1`,
		fixture.RuntimeA).Scan(&status, &code, &message); err != nil {
		t.Fatalf("read codex row: %v", err)
	}
	if status != "offline" || code != reason.Code || message != reason.Message {
		t.Fatalf("codex row = %s / %s / %s, want offline with the reason stored", status, code, message)
	}
	// The seeded row was online, so the bound agent flips to unavailable
	// in one frame; the new claude row has no agents and announces nothing.
	statuses := agentStatusEvents(log, fixture.CompanyA)
	if len(statuses) != 1 || len(statuses[0].Agents) != 1 ||
		statuses[0].Agents[0].AgentID != fixture.AgentA || statuses[0].Agents[0].RuntimeAvailable {
		t.Fatalf("agent.status events = %+v, want AgentA unavailable once", statuses)
	}
	rows, err := ListProjectionsForMember(ctx, store.Queries(), owner, ListOptions{})
	if err != nil || len(rows) != 1 || rows[0].RuntimeAvailable ||
		rows[0].UnavailableReason == nil || *rows[0].UnavailableReason != "runtime_unusable" {
		t.Fatalf("projection after unusable registration = %+v, %v; want runtime_unusable", rows, err)
	}

	// A heartbeat refreshes liveness but cannot bring an unusable runtime
	// online, and therefore announces nothing.
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET last_seen_at = now() - interval '10 minutes' WHERE id = $1`, fixture.RuntimeA)
	if err := service.Heartbeat(
		ctx,
		auth.UserPrincipal{UserID: fixture.UserA},
		fixture.RuntimeA,
		fixture.DaemonA,
	); err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
	var lastSeen time.Time
	if err := pool.QueryRow(
		ctx,
		`SELECT status, last_seen_at FROM runtimes WHERE id = $1`,
		fixture.RuntimeA,
	).Scan(&status, &lastSeen); err != nil {
		t.Fatalf("read codex row after heartbeat: %v", err)
	}
	if status != "offline" || time.Since(lastSeen) > time.Minute {
		t.Fatalf("codex row after heartbeat = %q at %s, want offline and fresh", status, lastSeen)
	}
	if got := agentStatusEvents(log, fixture.CompanyA); len(got) != 1 {
		t.Fatalf("agent.status events after the heartbeat = %d, want still 1", len(got))
	}

	// The upgrade re-registers without the reason: the row comes online,
	// the columns clear, and the agent is available again.
	registered, err = service.RegisterRuntimes(ctx, RuntimeRegistration{
		CompanyID:   fixture.CompanyA,
		OwnerUserID: fixture.UserA,
		DaemonID:    fixture.DaemonA,
		DeviceName:  "MacBook Pro",
		Runtimes:    []RuntimeRegistrationSpec{{Provider: "codex", Version: "0.101.0"}},
	})
	if err != nil {
		t.Fatalf("RegisterRuntimes() after the upgrade error = %v", err)
	}
	if len(registered) != 1 || registered[0].Status != "online" || registered[0].UnusableReason != nil {
		t.Fatalf("re-registered runtime = %+v, want online without a reason", registered)
	}
	var nullCode, nullMessage *string
	if err := pool.QueryRow(ctx,
		`SELECT unusable_reason_code, unusable_reason_message FROM runtimes WHERE id = $1`,
		fixture.RuntimeA).Scan(&nullCode, &nullMessage); err != nil {
		t.Fatalf("read codex row after the upgrade: %v", err)
	}
	if nullCode != nil || nullMessage != nil {
		t.Fatalf("unusable columns after the upgrade = %v / %v, want NULL", nullCode, nullMessage)
	}
	statuses = agentStatusEvents(log, fixture.CompanyA)
	if len(statuses) != 2 || len(statuses[1].Agents) != 1 ||
		statuses[1].Agents[0].AgentID != fixture.AgentA || !statuses[1].Agents[0].RuntimeAvailable {
		t.Fatalf(
			"agent.status events after the upgrade = %+v, want AgentA available in a second frame",
			statuses,
		)
	}
	rows, err = ListProjectionsForMember(ctx, store.Queries(), owner, ListOptions{})
	if err != nil || len(rows) != 1 || !rows[0].RuntimeAvailable || rows[0].UnavailableReason != nil {
		t.Fatalf("projection after the upgrade = %+v, %v; want available", rows, err)
	}
}
