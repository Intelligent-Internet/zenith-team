//go:build integration

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestCreateAgentAtomicallyPersistsDatabaseGeneratedID(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, eventLog := mustAgentService(t, store)
	input := validCreateInput()
	input.ReportsToAgentID = &fixture.rootAgentID
	input.RuntimeID = &fixture.runtimeID
	input.Provider = "codex"
	input.AvatarKey = stringPointer("engineering-male")

	var companyVersion int64
	var companyUpdatedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT version, updated_at
		FROM companies WHERE id = $1`, fixture.companyID).Scan(
		&companyVersion, &companyUpdatedAt,
	); err != nil {
		t.Fatalf("read Company before Agent create: %v", err)
	}

	created, err := service.Create(ctx, fixture.principal, input)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID == uuid.Nil || created.CompanyID != fixture.companyID ||
		created.ReportsToAgentID == nil || *created.ReportsToAgentID != fixture.rootAgentID ||
		created.AvatarKey == nil || *created.AvatarKey != "engineering-male" {
		t.Fatalf("Create() = %+v", created)
	}
	if created.Model != nil || created.ThinkingLevel != nil {
		t.Fatalf(
			"Create() provider defaults = model %v thinking %v, want nil",
			created.Model,
			created.ThinkingLevel,
		)
	}

	var agentCount, runtimeCount int
	var gotCompanyVersion int64
	var gotCompanyUpdatedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agents WHERE id = $1`, created.ID).
		Scan(&agentCount); err != nil {
		t.Fatalf("count Agents: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtimes WHERE company_id = $1`, fixture.companyID).
		Scan(&runtimeCount); err != nil {
		t.Fatalf("count Runtimes: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT version, updated_at
		FROM companies WHERE id = $1`, fixture.companyID).Scan(
		&gotCompanyVersion, &gotCompanyUpdatedAt,
	); err != nil {
		t.Fatalf("read Company after Agent create: %v", err)
	}
	if agentCount != 1 || runtimeCount != 1 || gotCompanyVersion != companyVersion ||
		!gotCompanyUpdatedAt.Equal(companyUpdatedAt) {
		t.Fatalf(
			"atomic state = agent %d runtime %d Company version %d updated %s",
			agentCount,
			runtimeCount,
			gotCompanyVersion,
			gotCompanyUpdatedAt,
		)
	}

	// Filtering by the payload's CompanyID is the routing fact itself: it is
	// what the realtime bridge reads to pick the Company room.
	createdEventsList := agentEventsForCompany(eventLog.created, fixture.companyID)
	if len(createdEventsList) != 1 {
		t.Fatalf("agent.created events = %d, want 1", len(createdEventsList))
	}
	published := createdEventsList[0]
	event := published.Payload
	if published.Name != "agent.created" {
		t.Errorf("event name = %q, want agent.created", published.Name)
	}
	if published.Actor != events.UserActor(fixture.userID.String()) {
		t.Errorf("agent.created actor = %#v, want the creating member %s",
			published.Actor, fixture.userID)
	}
	// The response is the creator's own projection, set up on their machine;
	// the Company-wide frame is the template with none of that (ADR 0014).
	if created.Binding == nil || created.Binding.Runtime.ID != fixture.runtimeID {
		t.Errorf("Create() binding = %+v, want the creator's runtime", created.Binding)
	}
	want := created.withoutViewerSetup()
	if !reflect.DeepEqual(event, want) {
		t.Errorf("agent.created payload = %+v, want %+v", event, want)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("encode agent.created: %v", err)
	}
	for _, secret := range []string{fixture.runtimeID.String(), "Fixture Runtime"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("agent.created names the creator's machine (%q): %s", secret, encoded)
		}
	}
	if t.Failed() {
		t.Fatalf("agent.created event payload = %+v", event)
	}
}

func TestCreateAgentRejectsInvalidParentsAndDeletingCompany(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	service, _ := mustAgentService(t, store)
	first := seedAgentCompany(t, ctx, pool, "owner")
	second := seedAgentCompany(t, ctx, pool, "owner")
	missingParent := validCreateInput()
	missingParent.RuntimeID = &first.runtimeID
	missingParent.Provider = "codex"
	if _, err := service.Create(ctx, first.principal, missingParent); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Create(missing parent) error = %v, want ErrNotFound", err)
	}
	crossCompanyParent := validCreateInput()
	crossCompanyParent.ReportsToAgentID = &second.rootAgentID
	crossCompanyParent.RuntimeID = &first.runtimeID
	crossCompanyParent.Provider = "codex"
	if _, err := service.Create(ctx, first.principal, crossCompanyParent); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Create(cross-Company parent) error = %v, want ErrNotFound", err)
	}
	archivedParentID := uuid.Must(uuid.NewV7())
	mustAgentExec(t, ctx, pool, `
		WITH statement_clock AS MATERIALIZED (SELECT clock_timestamp() AS now_at)
		INSERT INTO agents (id, company_id, name, title, reports_to_agent_id, provider, archived_at, updated_at)
		SELECT $1, $2, 'Archived', 'Archived', $3, 'codex', now_at, now_at
		FROM statement_clock`,
		archivedParentID, first.companyID, first.rootAgentID)
	archivedParent := validCreateInput()
	archivedParent.ReportsToAgentID = &archivedParentID
	archivedParent.RuntimeID = &first.runtimeID
	archivedParent.Provider = "codex"
	if _, err := service.Create(ctx, first.principal, archivedParent); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Create(archived parent) error = %v, want ErrNotFound", err)
	}

	deleted := seedAgentCompany(t, ctx, pool, "owner")
	mustAgentExec(
		t,
		ctx,
		pool,
		`UPDATE companies SET deleting_at = clock_timestamp() WHERE id = $1`,
		deleted.companyID,
	)
	deletedInput := validCreateInput()
	deletedInput.ReportsToAgentID = &deleted.rootAgentID
	deletedInput.RuntimeID = &deleted.runtimeID
	deletedInput.Provider = "codex"
	if _, err := service.Create(ctx, deleted.principal, deletedInput); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Create(deleting Company) error = %v, want ErrNotFound", err)
	}
}

func TestCreateAgentRechecksStaleOwnerAfterCompanyLock(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	service, _ := mustAgentService(t, store)

	tests := []struct {
		name      string
		mutation  string
		wantError error
		wantCount int
	}{
		{
			name: "revoked while stale middleware principal waits",
			mutation: `UPDATE company_members
				SET status = 'revoked', revoked_at = clock_timestamp(), revoked_by_user_id = user_id,
				    updated_at = clock_timestamp(), version = version + 1
				WHERE company_id = $1 AND user_id = $2`,
			wantError: ErrNotFound,
			wantCount: 0,
		},
		{
			// A demoted owner is still a member, and members create Agents
			// (ADR 0013): the recheck sees the new role and the create goes through.
			name:      "demoted while stale middleware principal waits",
			mutation:  `UPDATE company_members SET role = 'member', updated_at = clock_timestamp(), version = version + 1 WHERE company_id = $1 AND user_id = $2`,
			wantError: nil,
			wantCount: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := seedAgentCompany(t, ctx, pool, "owner")
			tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
			if err != nil {
				t.Fatalf("begin membership race: %v", err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err := tx.Exec(
				ctx,
				`SELECT id FROM companies WHERE id = $1 FOR UPDATE`,
				fixture.companyID,
			); err != nil {
				t.Fatalf("lock Company before membership race: %v", err)
			}
			if _, err := tx.Exec(ctx, test.mutation, fixture.companyID, fixture.userID); err != nil {
				t.Fatalf("mutate membership during race: %v", err)
			}
			input := validCreateInput()
			input.ReportsToAgentID = &fixture.rootAgentID
			input.RuntimeID = &fixture.runtimeID
			input.Provider = "codex"
			result := make(chan error, 1)
			go func() {
				_, createErr := service.Create(ctx, fixture.principal, input)
				result <- createErr
			}()
			select {
			case err := <-result:
				t.Fatalf("Create() returned before Company mutation committed: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatalf("commit membership race: %v", err)
			}
			if err := <-result; !errors.Is(err, test.wantError) {
				t.Fatalf("Create() after membership race error = %v, want %v", err, test.wantError)
			}
			var count int
			if err := pool.QueryRow(ctx, `
				SELECT count(*) FROM agents
				WHERE company_id = $1 AND name = $2`, fixture.companyID, input.Name).
				Scan(&count); err != nil {
				t.Fatalf("count Agents after membership race: %v", err)
			}
			if count != test.wantCount {
				t.Fatalf("Agent count after the membership race = %d, want %d", count, test.wantCount)
			}
		})
	}
}

type agentCompanyFixture struct {
	companyID   uuid.UUID
	userID      uuid.UUID
	rootAgentID uuid.UUID
	runtimeID   uuid.UUID
	principal   auth.UserPrincipal
}

func seedAgentCompany(t *testing.T, ctx context.Context, pool *pgxpool.Pool, role string) agentCompanyFixture {
	t.Helper()
	companyID := uuid.Must(uuid.NewV7())
	userID := uuid.Must(uuid.NewV7())
	rootAgentID := uuid.Must(uuid.NewV7())
	runtimeID := uuid.Must(uuid.NewV7())
	short := companyID.String()
	mustAgentExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Owner')`,
		userID, fmt.Sprintf("%s@zenith-team.test", short))
	mustAgentExec(t, ctx, pool,
		`INSERT INTO companies (id, name, slug, task_prefix) VALUES ($1, 'Company', $2, 'BRD')`,
		companyID, "company-"+short)
	mustAgentExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, $3)`,
		companyID, userID, role)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id)
		VALUES ($1, $2, $3, 'Fixture Runtime', 'codex', 'online', $4)`,
		runtimeID, companyID, "daemon-"+short, userID)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO agents (id, company_id, name, title, provider, created_by_user_id)
		VALUES ($1, $2, 'CEO', 'Chief Executive Officer', 'codex', $3)`,
		rootAgentID, companyID, userID)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO agent_runtime_bindings (company_id, agent_id, user_id, runtime_id, provider)
		VALUES ($1, $2, $3, $4, 'codex')`,
		companyID, rootAgentID, userID, runtimeID)
	return agentCompanyFixture{
		companyID:   companyID,
		userID:      userID,
		rootAgentID: rootAgentID,
		runtimeID:   runtimeID,
		principal: auth.UserPrincipal{
			UserID: userID, SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindBearer,
			CompanyID: companyID, Role: auth.CompanyRoleOwner,
		},
	}
}

func mustAgentExec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, statement string, arguments ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, statement, arguments...); err != nil {
		t.Fatalf("execute Agent test statement: %v", err)
	}
}

func startAgentPostgres(t *testing.T) (*pgxpool.Pool, *db.Store) {
	t.Helper()
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
	return pool, store
}

// recordedEvent is one published event with its payload re-typed, so a test
// reads the payload as itself instead of type-asserting at every assertion.
type recordedEvent[P any] struct {
	Name    string
	Actor   events.Actor
	Payload P
}

// topicLog collects what one event name publishes post-commit, replacing the
// deleted realtime_outbox table as the event assertion surface. A payload of
// the wrong shape is returned as an error, so the bus logs it against the
// subscription rather than the test silently skipping an event it meant to
// check.
type topicLog[P any] struct {
	mu     sync.Mutex
	events []recordedEvent[P]
}

func (l *topicLog[P]) record(event events.Event) error {
	payload, err := events.PayloadOf[P](event)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, recordedEvent[P]{
		Name:    event.Name,
		Actor:   event.Actor,
		Payload: payload,
	})
	return nil
}

func (l *topicLog[P]) snapshot() []recordedEvent[P] {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]recordedEvent[P](nil), l.events...)
}

// agentEventLog records everything the agent domain publishes, split by event
// name so assertions keep their created/updated/status/roster vocabulary.
type agentEventLog struct {
	created *topicLog[Response]
	updated *topicLog[Response]
	status  *topicLog[AvailabilityChange]
	binding *topicLog[BindingChange]
	roster  *topicLog[DaemonRoster]
}

func recordAgentEvents(t *testing.T, bus *events.Bus) *agentEventLog {
	t.Helper()
	log := &agentEventLog{
		created: &topicLog[Response]{},
		updated: &topicLog[Response]{},
		status:  &topicLog[AvailabilityChange]{},
		binding: &topicLog[BindingChange]{},
		roster:  &topicLog[DaemonRoster]{},
	}
	// One subscription per topic, switching on the name — the same shape a
	// production consumer has.
	if err := bus.Subscribe(Topic, func(_ context.Context, event events.Event) error {
		switch event.Name {
		case EventAgentCreated:
			return log.created.record(event)
		case EventAgentUpdated:
			return log.updated.record(event)
		case EventAgentStatusChanged:
			return log.status.record(event)
		case EventAgentBindingChanged:
			return log.binding.record(event)
		}
		return nil
	}); err != nil {
		t.Fatalf("subscribe to the agent topic: %v", err)
	}
	if err := bus.Subscribe(TopicDaemon, func(_ context.Context, event events.Event) error {
		if event.Name == EventDaemonRosterChanged {
			return log.roster.record(event)
		}
		return nil
	}); err != nil {
		t.Fatalf("subscribe to the daemon topic: %v", err)
	}
	return log
}

// total counts every agent-domain event published so far, whatever its
// topic — what a test asserting "nothing was announced" needs.
func (l *agentEventLog) total() int {
	return len(l.created.snapshot()) + len(l.updated.snapshot()) +
		len(l.status.snapshot()) + len(l.binding.snapshot()) + len(l.roster.snapshot())
}

func mustAgentTestBus(t *testing.T) *events.Bus {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct test bus: %v", err)
	}
	return bus
}

func mustAgentService(t *testing.T, store *db.Store) (*DefaultAgentService, *agentEventLog) {
	t.Helper()
	bus := mustAgentTestBus(t)
	log := recordAgentEvents(t, bus)
	service, err := NewAgentService(ServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatalf("NewAgentService() error = %v", err)
	}
	return service, log
}

// agentEventsForCompany narrows a lifecycle log to one Company. CompanyID on
// the projection is the routing fact the realtime bridge reads, so filtering
// on it is exactly what delivery does.
func agentEventsForCompany(
	log *topicLog[Response],
	companyID uuid.UUID,
) []recordedEvent[Response] {
	var matched []recordedEvent[Response]
	for _, event := range log.snapshot() {
		if event.Payload.CompanyID == companyID {
			matched = append(matched, event)
		}
	}
	return matched
}
