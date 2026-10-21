//go:build integration

package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// runtimeNoticeLog records the two surfaces an Inbox item reaches: the
// durable item frames on the inbox topic, and the transient presentation the
// Presenter raises for a freshly created one.
type runtimeNoticeLog struct {
	created *topicLog[inbox.ItemWithCounts]
	updated *topicLog[inbox.ItemWithCounts]

	mu        sync.Mutex
	presented []inbox.Notification
}

func (l *runtimeNoticeLog) Present(notification inbox.Notification) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.presented = append(l.presented, notification)
}

func (l *runtimeNoticeLog) presentations() []inbox.Notification {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]inbox.Notification(nil), l.presented...)
}

// mustRuntimeNoticeService is mustRuntimeLifecycleService plus the Inbox
// surfaces: the same runtime service, with a Presenter and a recorder for the
// item frames the notices stage.
func mustRuntimeNoticeService(
	t *testing.T,
	store *db.Store,
) (*DefaultRuntimeService, *agentEventLog, *runtimeNoticeLog) {
	t.Helper()
	bus := mustAgentTestBus(t)
	agentLog := recordAgentEvents(t, bus)
	notices := &runtimeNoticeLog{
		created: &topicLog[inbox.ItemWithCounts]{},
		updated: &topicLog[inbox.ItemWithCounts]{},
	}
	if err := bus.Subscribe(inbox.Topic, func(_ context.Context, event events.Event) error {
		switch event.Name {
		case inbox.EventItemCreated:
			return notices.created.record(event)
		case inbox.EventItemUpdated:
			return notices.updated.record(event)
		}
		return nil
	}); err != nil {
		t.Fatalf("subscribe to the inbox topic: %v", err)
	}
	service, err := NewRuntimeService(RuntimeServiceOptions{
		Store:            store,
		Bus:              bus,
		ModelDiscoveries: NewInMemoryModelDiscoveryStore(ModelDiscoveryStoreOptions{}),
		Presenter:        notices,
	})
	if err != nil {
		t.Fatalf("NewRuntimeService() error = %v", err)
	}
	return service, agentLog, notices
}

// seedSecondCompanyOwner adds an active second owner to company A: the
// offline notice's "the company owner" is every active owner, not one seat.
func seedSecondCompanyOwner(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID uuid.UUID,
	email string,
	status string,
) uuid.UUID {
	t.Helper()
	userID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Second Owner')`,
		userID, email)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role, status)
		 VALUES ($1, $2, 'owner', $3)`, companyID, userID, status)
	return userID
}

// seedInFlightRun puts one run of the fixture's agent in a status the sweep
// counts and FailRunsForOfflineRuntimes fails a moment later.
func seedInFlightRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	status string,
) {
	t.Helper()
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id, status, run_type)
		VALUES ($1, $2, $3, $4, $5, $6, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))`,
		fixture.CompanyA, fixture.AgentA, fixture.TaskA, fixture.ConversationA,
		fixture.RuntimeA, status)
}

type storedNotice struct {
	recipient  uuid.UUID
	kind       string
	entityType string
	entityID   string
	title      string
	body       string
	deepLink   string
	dedupeKey  string
	read       bool
	archived   bool
	version    int64
}

func readRuntimeNotices(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID uuid.UUID,
) []storedNotice {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT recipient_user_id, kind, entity_type, entity_id, title, body,
		       coalesce(deep_link, ''), dedupe_key,
		       read_at IS NOT NULL, archived_at IS NOT NULL, version
		FROM inbox_items
		WHERE company_id = $1 AND kind = 'runtime_offline'
		ORDER BY recipient_user_id`, companyID)
	if err != nil {
		t.Fatalf("read runtime_offline items: %v", err)
	}
	defer rows.Close()
	var notices []storedNotice
	for rows.Next() {
		var notice storedNotice
		if err := rows.Scan(&notice.recipient, &notice.kind, &notice.entityType,
			&notice.entityID, &notice.title, &notice.body, &notice.deepLink,
			&notice.dedupeKey, &notice.read, &notice.archived, &notice.version); err != nil {
			t.Fatalf("scan runtime_offline item: %v", err)
		}
		notices = append(notices, notice)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read runtime_offline items: %v", err)
	}
	return notices
}

// seedOpenRuntimeNotice writes one open notice about a runtime directly, so a
// resolution test does not depend on the sweep that raises them.
func seedOpenRuntimeNotice(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID, recipientID, runtimeID uuid.UUID,
	dedupeKey string,
) {
	t.Helper()
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO inbox_items (
			company_id, recipient_user_id, kind, entity_type, entity_id,
			title, body, deep_link, dedupe_key
		) VALUES ($1, $2, 'runtime_offline', 'runtime', $3::text,
			'Runtime A went offline with 1 run in flight',
			'Runtime A (codex) stopped sending heartbeats.', $4, $5)`,
		companyID, recipientID, runtimeID, "/settings/agents/"+runtimeID.String(), dedupeKey)
}

// TestMarkStaleRuntimesOfflineNotifiesOwnersOfInFlightRuns is the offline
// notice's first half: a machine that dies holding work tells the people who
// can restart it, once each, with the machine's name and the count.
func TestMarkStaleRuntimesOfflineNotifiesOwnersOfInFlightRuns(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, agentLog, notices := mustRuntimeNoticeService(t, store)
	// The notice goes to the machine's registrant alone (ADR 0014): a
	// second company owner is not told about someone else's computer.
	userC := seedSecondCompanyOwner(t, ctx, pool, fixture.CompanyA,
		"runtime-notice-c@example.com", "active")
	seedInFlightRun(t, ctx, pool, fixture, "running")
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

	stored := readRuntimeNotices(t, ctx, pool, fixture.CompanyA)
	if len(stored) != 1 {
		t.Fatalf("runtime_offline items = %+v, want one, for the registrant", stored)
	}
	recipients := map[uuid.UUID]bool{}
	for _, notice := range stored {
		recipients[notice.recipient] = true
		if notice.entityType != "runtime" || notice.entityID != fixture.RuntimeA.String() {
			t.Fatalf("notice entity = %s/%s, want runtime/%s",
				notice.entityType, notice.entityID, fixture.RuntimeA)
		}
		if notice.title != "Runtime A went offline" {
			t.Fatalf("notice title = %q", notice.title)
		}
		if notice.deepLink != "/settings/agents/"+fixture.RuntimeA.String() {
			t.Fatalf("notice deep link = %q, want the runtime detail", notice.deepLink)
		}
		if notice.read || notice.archived {
			t.Fatalf("notice is already resolved: %+v", notice)
		}
		if notice.dedupeKey != stored[0].dedupeKey {
			t.Fatalf("dedupe keys differ across recipients: %q vs %q",
				notice.dedupeKey, stored[0].dedupeKey)
		}
	}
	if !recipients[fixture.UserA] || recipients[userC] {
		t.Fatalf("notice recipients = %v, want the registrant alone", recipients)
	}

	created := notices.created.snapshot()
	if len(created) != 1 {
		t.Fatalf("inbox.created events = %d, want 1", len(created))
	}
	for _, event := range created {
		if event.Payload.Type != inbox.TypeRuntimeOffline {
			t.Fatalf("inbox.created kind = %q, want runtime_offline", event.Payload.Type)
		}
		if event.Payload.RecipientUserID != fixture.UserA {
			t.Fatalf(
				"inbox.created recipient = %s, want the registrant",
				event.Payload.RecipientUserID,
			)
		}
	}
	presented := notices.presentations()
	if len(presented) != 1 {
		t.Fatalf("presented notifications = %d, want 1", len(presented))
	}
	for _, notification := range presented {
		if notification.Type != inbox.TypeRuntimeOffline {
			t.Fatalf("presented kind = %q, want runtime_offline", notification.Type)
		}
	}

	// The demotion's own announcements are unchanged.
	if roster := rosterEvents(agentLog, fixture.CompanyA,
		protocol.DaemonRosterActionStaleSweep); len(roster) != 1 {
		t.Fatalf("stale-sweep roster events = %+v, want exactly one", roster)
	}
	if statuses := agentStatusEvents(agentLog, fixture.CompanyA); len(statuses) != 1 {
		t.Fatalf("agent.status events = %+v, want exactly one", statuses)
	}

	// A second sweep finds nothing stale and writes nothing more.
	flipped, err = service.MarkStaleRuntimesOffline(ctx)
	if err != nil || flipped != 0 {
		t.Fatalf("second MarkStaleRuntimesOffline() = %d, %v, want 0, nil", flipped, err)
	}
	if again := readRuntimeNotices(t, ctx, pool, fixture.CompanyA); len(again) != 1 {
		t.Fatalf("runtime_offline items after a second sweep = %d, want still 1", len(again))
	}
}

// TestMarkStaleRuntimesOfflineNotifiesAgainAfterARecovery is why the dedupe
// key carries the episode's clock: resolving a notice archives it and its
// UNIQUE key survives, so a fixed key would make a flapping machine silent
// forever after the first time.
func TestMarkStaleRuntimesOfflineNotifiesAgainAfterARecovery(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, _, _ := mustRuntimeNoticeService(t, store)
	seedInFlightRun(t, ctx, pool, fixture, "dispatched")
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET last_seen_at = now() - interval '10 minutes' WHERE id = $1`,
		fixture.RuntimeA)

	if _, err := service.MarkStaleRuntimesOffline(ctx); err != nil {
		t.Fatalf("first MarkStaleRuntimesOffline() error = %v", err)
	}
	first := readRuntimeNotices(t, ctx, pool, fixture.CompanyA)
	if len(first) != 1 {
		t.Fatalf("first episode items = %+v, want one", first)
	}

	// The machine comes back — the heartbeat resolves the notice — and dies
	// again with the run still in flight. The second silence starts at the
	// beat that ended the first, which is what makes the episode key move.
	if err := service.Heartbeat(ctx, auth.UserPrincipal{UserID: fixture.UserA},
		fixture.RuntimeA, fixture.DaemonA); err != nil {
		t.Fatalf("recovery Heartbeat() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET last_seen_at = now() - interval '3 minutes' WHERE id = $1`,
		fixture.RuntimeA)
	if _, err := service.MarkStaleRuntimesOffline(ctx); err != nil {
		t.Fatalf("second MarkStaleRuntimesOffline() error = %v", err)
	}

	second := readRuntimeNotices(t, ctx, pool, fixture.CompanyA)
	if len(second) != 2 {
		t.Fatalf("items after the second episode = %+v, want two", second)
	}
	if second[0].dedupeKey == second[1].dedupeKey {
		t.Fatalf("both episodes share the dedupe key %q", second[0].dedupeKey)
	}
	var open int
	for _, notice := range second {
		if !notice.archived {
			open++
		}
	}
	if open != 1 {
		t.Fatalf("open notices = %d, want only the current episode", open)
	}
}

// TestMarkStaleRuntimesOfflineSkipsAnIneligibleRecipient: a revoked
// registrant is not a sweep failure. The demotion still happens; nobody is
// told, because the machine was theirs alone (ADR 0014).
func TestMarkStaleRuntimesOfflineSkipsAnIneligibleRecipient(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, _, _ := mustRuntimeNoticeService(t, store)
	revoked := seedSecondCompanyOwner(t, ctx, pool, fixture.CompanyA,
		"runtime-notice-revoked@example.com", "revoked")
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET owner_user_id = $2,
		 last_seen_at = now() - interval '10 minutes' WHERE id = $1`,
		fixture.RuntimeA, revoked)
	seedInFlightRun(t, ctx, pool, fixture, "waiting_local_directory")

	flipped, err := service.MarkStaleRuntimesOffline(ctx)
	if err != nil {
		t.Fatalf("MarkStaleRuntimesOffline() error = %v", err)
	}
	if flipped != 1 {
		t.Fatalf("MarkStaleRuntimesOffline() = %d, want 1", flipped)
	}
	stored := readRuntimeNotices(t, ctx, pool, fixture.CompanyA)
	if len(stored) != 0 {
		t.Fatalf("runtime_offline items = %+v, want none: the only recipient is revoked", stored)
	}
}

// TestHeartbeatResolvesRuntimeOfflineItems is the offline notice's second
// half over the ordinary return path: the machine beats again and every
// recipient's copy resolves itself, in the same transaction that records the
// machine alive.
func TestHeartbeatResolvesRuntimeOfflineItems(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, agentLog, notices := mustRuntimeNoticeService(t, store)
	userC := seedSecondCompanyOwner(t, ctx, pool, fixture.CompanyA,
		"runtime-heartbeat-c@example.com", "active")
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET status = 'offline', last_seen_at = now() - interval '10 minutes'
		 WHERE id = $1`, fixture.RuntimeA)
	seedOpenRuntimeNotice(t, ctx, pool, fixture.CompanyA, fixture.UserA,
		fixture.RuntimeA, "runtime:a:offline:1")
	seedOpenRuntimeNotice(t, ctx, pool, fixture.CompanyA, userC,
		fixture.RuntimeA, "runtime:a:offline:2")

	// A refusal writes nothing: the notices stay open and nothing is
	// announced.
	if err := service.Heartbeat(ctx, auth.UserPrincipal{UserID: fixture.UserB},
		fixture.RuntimeA, fixture.DaemonA); !errors.Is(err, ErrRuntimeAccessDenied) {
		t.Fatalf("foreign Heartbeat() error = %v, want ErrRuntimeAccessDenied", err)
	}
	if err := service.Heartbeat(ctx, auth.UserPrincipal{UserID: fixture.UserA},
		dbtest.NewUUID(t), fixture.DaemonA); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("missing Heartbeat() error = %v, want ErrRuntimeNotFound", err)
	}
	for _, notice := range readRuntimeNotices(t, ctx, pool, fixture.CompanyA) {
		if notice.archived || notice.read {
			t.Fatalf("a refused heartbeat resolved %+v", notice)
		}
	}
	if len(notices.updated.snapshot()) != 0 {
		t.Fatal("a refused heartbeat announced an inbox change")
	}

	if err := service.Heartbeat(ctx, auth.UserPrincipal{UserID: fixture.UserA},
		fixture.RuntimeA, fixture.DaemonA); err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
	resolved := readRuntimeNotices(t, ctx, pool, fixture.CompanyA)
	if len(resolved) != 2 {
		t.Fatalf("runtime_offline items = %d, want the two seeded ones", len(resolved))
	}
	for _, notice := range resolved {
		if !notice.read || !notice.archived || notice.version != 2 {
			t.Fatalf("notice after the return heartbeat = %+v, want read, archived, version 2",
				notice)
		}
	}
	updated := notices.updated.snapshot()
	if len(updated) != 2 {
		t.Fatalf("inbox.updated events = %d, want one per recipient", len(updated))
	}
	for _, event := range updated {
		if event.Payload.ArchivedAt == nil {
			t.Fatalf("inbox.updated carried an unarchived item: %+v", event.Payload.Item)
		}
	}
	// The offline → online edge still publishes exactly one status frame.
	if statuses := agentStatusEvents(agentLog, fixture.CompanyA); len(statuses) != 1 {
		t.Fatalf("agent.status events = %+v, want exactly one", statuses)
	}

	// The steady-state beat stays silent — and cheap.
	if err := service.Heartbeat(ctx, auth.UserPrincipal{UserID: fixture.UserA},
		fixture.RuntimeA, fixture.DaemonA); err != nil {
		t.Fatalf("second Heartbeat() error = %v", err)
	}
	if len(notices.updated.snapshot()) != 2 {
		t.Fatal("a steady-state heartbeat announced another inbox change")
	}
}

// TestHeartbeatRecoversARuntimeTheSweepDemotedUnderIt is the race the sweep
// creates every 30 s: a beat arrives while the machine is online, the sweep
// commits it offline — failing its in-flight runs and raising the notice —
// and the beat's own write is what brings it back. The recovery therefore has
// to be judged by that write. A beat that decided from an earlier read would
// put the row back online and publish nothing: every client would keep
// caching the bound agents as unavailable, and the owner would keep a notice
// about a machine that is alive.
func TestHeartbeatRecoversARuntimeTheSweepDemotedUnderIt(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, agentLog, notices := mustRuntimeNoticeService(t, store)

	// The sweep's transaction takes the runtime row's write lock first, the
	// way MarkRuntimesOfflineByIDs does, and holds it while the beat arrives.
	sweep, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the sweep transaction: %v", err)
	}
	defer func() { _ = sweep.Rollback(ctx) }()
	if _, err := sweep.Exec(ctx,
		`SELECT id FROM runtimes WHERE id = $1 FOR UPDATE`, fixture.RuntimeA); err != nil {
		t.Fatalf("lock the runtime row: %v", err)
	}

	beat := make(chan error, 1)
	go func() {
		beat <- service.Heartbeat(ctx, auth.UserPrincipal{UserID: fixture.UserA},
			fixture.RuntimeA, fixture.DaemonA)
	}()

	// The rendezvous, and the whole reason this test is deterministic rather
	// than timed: wait until the beat's own write is queued behind the
	// sweep's lock. Only a write can wait there — every read on the path is
	// an ordinary MVCC SELECT that this lock does not block — so once
	// something is waiting, the beat has already seen the runtime online.
	waitForBlockedRuntimeWrite(t, ctx, pool)

	// Now the demotion commits underneath the waiting beat: the row goes
	// offline and the owner's notice is raised, both in the sweep's own
	// transaction.
	if _, err := sweep.Exec(ctx,
		`UPDATE runtimes SET status = 'offline', updated_at = now() WHERE id = $1`,
		fixture.RuntimeA); err != nil {
		t.Fatalf("demote the runtime: %v", err)
	}
	if _, err := sweep.Exec(ctx, `
		INSERT INTO inbox_items (
			company_id, recipient_user_id, kind, entity_type, entity_id,
			title, body, deep_link, dedupe_key
		) VALUES ($1, $2, 'runtime_offline', 'runtime', $3::text,
			'Runtime A went offline with 1 run in flight',
			'Runtime A (codex) stopped sending heartbeats.', $4, $5)`,
		fixture.CompanyA, fixture.UserA, fixture.RuntimeA,
		"/settings/agents/"+fixture.RuntimeA.String(),
		"runtime:a:offline:swept"); err != nil {
		t.Fatalf("raise the runtime_offline notice: %v", err)
	}
	if err := sweep.Commit(ctx); err != nil {
		t.Fatalf("commit the sweep: %v", err)
	}

	if err := <-beat; err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}

	var status string
	var lastSeen time.Time
	if err := pool.QueryRow(
		ctx,
		`SELECT status, last_seen_at FROM runtimes WHERE id = $1`, fixture.RuntimeA,
	).Scan(&status, &lastSeen); err != nil {
		t.Fatalf("read the runtime after the raced heartbeat: %v", err)
	}
	if status != "online" || time.Since(lastSeen) > time.Minute {
		t.Fatalf("runtime after the raced heartbeat = %q at %s, want online just now",
			status, lastSeen)
	}
	// The row alone is not the fix: the bound agents are cached unavailable
	// in every open client until this frame arrives.
	statuses := agentStatusEvents(agentLog, fixture.CompanyA)
	if len(statuses) != 1 || len(statuses[0].Agents) != 1 ||
		statuses[0].Agents[0].AgentID != fixture.AgentA ||
		!statuses[0].Agents[0].RuntimeAvailable {
		t.Fatalf("agent.status events = %+v, want one available entry for AgentA", statuses)
	}
	resolved := readRuntimeNotices(t, ctx, pool, fixture.CompanyA)
	if len(resolved) != 1 || !resolved[0].read || !resolved[0].archived {
		t.Fatalf("runtime_offline items after the raced heartbeat = %+v, want the swept one resolved",
			resolved)
	}
	if len(notices.updated.snapshot()) != 1 {
		t.Fatalf("inbox.updated events = %d, want one for the resolved notice",
			len(notices.updated.snapshot()))
	}
}

// waitForBlockedRuntimeWrite returns once a backend in this test's database
// is waiting on a lock to write the runtimes table. It is a condition, not a
// delay: the test proceeds the moment the race it needs exists, and fails
// loudly if it never does.
func waitForBlockedRuntimeWrite(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database()
			  AND pid <> pg_backend_pid()
			  AND wait_event_type = 'Lock'
			  AND query ILIKE '%UPDATE runtimes%'`).Scan(&waiting); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the heartbeat never queued behind the sweep's row lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRegisterRuntimesResolvesRuntimeOfflineItems covers the return path a
// heartbeat-only rule misses: a restarted daemon registers the
// runtime online BEFORE its first beat, so the heartbeat edge never fires.
func TestRegisterRuntimesResolvesRuntimeOfflineItems(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, _, notices := mustRuntimeNoticeService(t, store)
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET status = 'offline', last_seen_at = now() - interval '10 minutes'
		 WHERE id = $1`, fixture.RuntimeA)
	seedOpenRuntimeNotice(t, ctx, pool, fixture.CompanyA, fixture.UserA,
		fixture.RuntimeA, "runtime:a:offline:1")

	registration := RuntimeRegistration{
		CompanyID:   fixture.CompanyA,
		OwnerUserID: fixture.UserA,
		DaemonID:    fixture.DaemonA,
		DeviceName:  "MacBook Pro",
		Runtimes:    []RuntimeRegistrationSpec{{Provider: "codex", Status: "online"}},
	}
	if _, err := service.RegisterRuntimes(ctx, registration); err != nil {
		t.Fatalf("RegisterRuntimes() error = %v", err)
	}
	stored := readRuntimeNotices(t, ctx, pool, fixture.CompanyA)
	if len(stored) != 1 || !stored[0].archived || !stored[0].read {
		t.Fatalf("notice after the re-registration = %+v, want resolved", stored)
	}
	if len(notices.updated.snapshot()) != 1 {
		t.Fatalf("inbox.updated events = %d, want one", len(notices.updated.snapshot()))
	}

	// A registration that lands offline is not a return: a fresh notice
	// raised while the machine is unusable stays open.
	seedOpenRuntimeNotice(t, ctx, pool, fixture.CompanyA, fixture.UserA,
		fixture.RuntimeA, "runtime:a:offline:2")
	registration.Runtimes = []RuntimeRegistrationSpec{{Provider: "codex", Status: "offline"}}
	if _, err := service.RegisterRuntimes(ctx, registration); err != nil {
		t.Fatalf("offline RegisterRuntimes() error = %v", err)
	}
	open := 0
	for _, notice := range readRuntimeNotices(t, ctx, pool, fixture.CompanyA) {
		if !notice.archived {
			open++
		}
	}
	if open != 1 {
		t.Fatalf("open notices after an offline registration = %d, want 1", open)
	}
	if len(notices.updated.snapshot()) != 1 {
		t.Fatalf("inbox.updated events = %d, want still one",
			len(notices.updated.snapshot()))
	}
}

// TestMarkStaleRuntimesOfflineDemotesWhenANoticeCannotBeRaised is the
// liveness/notification split: the demotion is the sweep's job and the Inbox
// item is a courtesy, so a courtesy that cannot be written must not keep the
// fleet online. `runtimes.name` is NOT NULL but not constrained non-empty, so
// a row that reached the table with a blank name produces a title starting
// with a space — which inbox.CreateInTx refuses before it writes anything.
// The sweep logs that one and carries on: both stale runtimes go offline,
// both are counted for the run-failing step that follows, and the healthy
// machine's owner is still told.
func TestMarkStaleRuntimesOfflineDemotesWhenANoticeCannotBeRaised(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, agentLog, notices := mustRuntimeNoticeService(t, store)
	seedInFlightRun(t, ctx, pool, fixture, "running")
	nameless := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes
			(id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-b', '', 'claude', 'online', $3, now())`,
		nameless, fixture.CompanyA, fixture.UserA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id, status, run_type)
		VALUES ($1, $2, $3, $4, 'running', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))`,
		fixture.CompanyA, fixture.AgentA, fixture.ConversationA, nameless)
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET last_seen_at = now() - interval '10 minutes' WHERE company_id = $1`,
		fixture.CompanyA)

	flipped, err := service.MarkStaleRuntimesOffline(ctx)
	if err != nil {
		t.Fatalf("MarkStaleRuntimesOffline() error = %v, want the failing notice swallowed", err)
	}
	if flipped != 2 {
		t.Fatalf("MarkStaleRuntimesOffline() = %d, want both stale runtimes demoted", flipped)
	}
	for _, runtimeID := range []uuid.UUID{fixture.RuntimeA, nameless} {
		var status string
		if err := pool.QueryRow(ctx,
			`SELECT status FROM runtimes WHERE id = $1`, runtimeID).Scan(&status); err != nil {
			t.Fatalf("read status of %s: %v", runtimeID, err)
		}
		if status != "offline" {
			t.Fatalf("runtime %s status = %q, want offline", runtimeID, status)
		}
	}

	stored := readRuntimeNotices(t, ctx, pool, fixture.CompanyA)
	if len(stored) != 1 || stored[0].entityID != fixture.RuntimeA.String() ||
		stored[0].recipient != fixture.UserA {
		t.Fatalf("runtime_offline items = %+v, want exactly the healthy machine's notice", stored)
	}
	if created := notices.created.snapshot(); len(created) != 1 {
		t.Fatalf("inbox.created events = %d, want one", len(created))
	}
	if presented := notices.presentations(); len(presented) != 1 {
		t.Fatalf("presented notifications = %d, want one", len(presented))
	}
	if roster := rosterEvents(agentLog, fixture.CompanyA,
		protocol.DaemonRosterActionStaleSweep); len(roster) != 1 {
		t.Fatalf("stale-sweep roster events = %+v, want exactly one", roster)
	}
}

// TestHeartbeatAnnouncesTheReturnWhenTheNoticeCannotBeClosed is the same
// liveness/notification split on the way back up. Once the beat's own write
// has turned the row online the machine IS back, and every client is still
// caching its agents as unavailable — only agent.status_changed clears that.
// The Inbox item the sweep raised is a courtesy, so a close that fails must
// be logged and left behind, never returned in place of the announcement.
// The close is broken here with a trigger on inbox_items: a driver error,
// the harshest shape, and one no retry would fix.
func TestHeartbeatAnnouncesTheReturnWhenTheNoticeCannotBeClosed(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, agentLog, notices := mustRuntimeNoticeService(t, store)
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET status = 'offline', last_seen_at = now() - interval '10 minutes'
		 WHERE id = $1`, fixture.RuntimeA)
	seedOpenRuntimeNotice(t, ctx, pool, fixture.CompanyA, fixture.UserA,
		fixture.RuntimeA, "runtime:a:offline:1")
	dbtest.MustExec(t, ctx, pool, `
		CREATE FUNCTION refuse_inbox_close() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'inbox is unavailable'; END; $$`)
	dbtest.MustExec(t, ctx, pool, `
		CREATE TRIGGER refuse_inbox_close BEFORE UPDATE ON inbox_items
		FOR EACH ROW EXECUTE FUNCTION refuse_inbox_close()`)

	if err := service.Heartbeat(ctx, auth.UserPrincipal{UserID: fixture.UserA},
		fixture.RuntimeA, fixture.DaemonA); err != nil {
		t.Fatalf("Heartbeat() error = %v, want the failing notice close swallowed", err)
	}

	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM runtimes WHERE id = $1`, fixture.RuntimeA).Scan(&status); err != nil {
		t.Fatalf("read runtime status: %v", err)
	}
	if status != "online" {
		t.Fatalf("runtime status = %q, want the beat's own write kept", status)
	}
	statuses := agentStatusEvents(agentLog, fixture.CompanyA)
	if len(statuses) != 1 || len(statuses[0].Agents) != 1 ||
		statuses[0].Agents[0].AgentID != fixture.AgentA || !statuses[0].Agents[0].RuntimeAvailable {
		t.Fatalf("agent.status events = %+v, want AgentA announced available once", statuses)
	}
	// The item is still open and nothing was announced about it: the close
	// is dropped whole, never half-published.
	if updated := notices.updated.snapshot(); len(updated) != 0 {
		t.Fatalf("inbox.updated events = %d, want none for a close that never committed", len(updated))
	}
	stored := readRuntimeNotices(t, ctx, pool, fixture.CompanyA)
	if len(stored) != 1 || stored[0].archived {
		t.Fatalf("runtime_offline items = %+v, want the seeded one still open", stored)
	}
}
