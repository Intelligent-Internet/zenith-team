package execution

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/telemetry"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/runfailure"
)

// recordedRunEvent is one lifecycle frame the recorder kept: the wire name
// and the snapshot it carried.
type recordedRunEvent struct {
	Name    string
	Payload RunSnapshot
}

// eventRecorder captures the run frames the bus delivers so tests can assert
// on the post-commit stream. One subscription takes the whole domain and
// switches on the name, so a payload of the wrong shape is a typing error the
// handler reports rather than a frame counted as something it is not.
type eventRecorder struct {
	mu     sync.Mutex
	topics []string
	events []recordedRunEvent
}

// recordRunTopics subscribes the recorder to the run domain — one
// subscription for every transition, which is the point of the grouping.
func (r *eventRecorder) recordRunTopics(bus *events.Bus) error {
	return bus.Subscribe(Topic, func(_ context.Context, event events.Event) error {
		r.mu.Lock()
		r.topics = append(r.topics, event.Name)
		r.mu.Unlock()

		switch event.Name {
		case EventRunMessageCreated:
			// run.message rides the same topic on a transcript item; the
			// name is counted above, and the shape still has to be right.
			if _, err := events.PayloadOf[TranscriptItem](event); err != nil {
				return err
			}
			return nil
		default:
			snapshot, err := events.PayloadOf[RunSnapshot](event)
			if err != nil {
				return err
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			r.events = append(r.events, recordedRunEvent{Name: event.Name, Payload: snapshot})
			return nil
		}
	})
}

func (r *eventRecorder) topicCount(topic string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, t := range r.topics {
		if t == topic {
			count++
		}
	}
	return count
}

func (r *eventRecorder) lastEvent(topic string) (recordedRunEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].Name == topic {
			return r.events[i], true
		}
	}
	return recordedRunEvent{}, false
}

// terminalObserver stands in for the metrics registry: it keeps every
// terminal observation the lifecycle hands over, so a test can prove that a
// new terminal row is counted exactly once and a replay not at all.
type terminalObserver struct {
	mu           sync.Mutex
	observations []telemetry.RunTerminalObservation
}

func (o *terminalObserver) ObserveRunTerminal(value telemetry.RunTerminalObservation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.observations = append(o.observations, value)
}

func (o *terminalObserver) terminals() []telemetry.RunTerminalObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]telemetry.RunTerminalObservation(nil), o.observations...)
}

type runHarness struct {
	bus      *events.Bus
	pool     *pgxpool.Pool
	queries  *generated.Queries
	svc      *DefaultRunService
	recorder *eventRecorder
	observer *terminalObserver
	fixture  dbtest.Fixture
}

func resolveTestConversationRoute(
	ctx context.Context,
	queries *generated.Queries,
	companyID, conversationID uuid.UUID,
) (ConversationRoute, error) {
	row, err := queries.GetConversationRoute(ctx, generated.GetConversationRouteParams{
		ConversationID: conversationID,
		CompanyID:      companyID,
	})
	if err != nil {
		return ConversationRoute{}, err
	}
	return ConversationRoute{
		ConversationKind:      row.Kind,
		ConversationCreatedBy: util.NullUUIDPtr(row.CreatedByUserID),
	}, nil
}

func newRunHarness(t *testing.T) *runHarness {
	t.Helper()
	ctx := context.Background()
	pool, _ := dbtest.StartPostgres(t)
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE conversations
		SET archived_at = clock_timestamp(), updated_at = clock_timestamp(), version = version + 1
		WHERE id = $1`, fixture.DirectConversationA)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	recorder := &eventRecorder{}
	if err := recorder.recordRunTopics(bus); err != nil {
		t.Fatalf("subscribe recorder: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	queries := store.Queries()
	observer := &terminalObserver{}
	service := NewRunService(RunServiceOptions{
		Queries:       queries,
		Transactor:    store,
		Bus:           bus,
		RouteResolver: resolveTestConversationRoute,
		RunObserver:   observer,
	})
	return &runHarness{
		bus:      bus,
		pool:     pool,
		queries:  queries,
		svc:      service,
		recorder: recorder,
		observer: observer,
		fixture:  fixture,
	}
}

func newPGUUID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new uuid: %v", err)
	}
	return id
}

// seedDirectConversation inserts one direct conversation for (AgentA, UserA)
// in CompanyA and returns its id.
func (h *runHarness) seedDirectConversation(t *testing.T) uuid.UUID {
	t.Helper()
	id := newPGUUID(t)
	dbtest.MustExec(t, context.Background(), h.pool, `
		INSERT INTO conversations (id, company_id, kind, agent_id, created_by_user_id, title)
		VALUES ($1, $2, 'direct', $3, $4, 'Direct')`,
		id, h.fixture.CompanyA, h.fixture.AgentA, h.fixture.UserA)
	return id
}

// seedQueuedRun inserts a queued direct-conversation run bound to RuntimeA,
// authorized by the fixture owner who registered that runtime — the shape the
// enqueue kernel writes, so the claim's consent fence admits it. The id comes
// back from the sequence rather than being minted: runs.id is a bigserial,
// and seeding one by hand would leave runs_id_seq behind it.
func (h *runHarness) seedQueuedRun(t *testing.T, conversationID uuid.UUID) uuid.UUID {
	t.Helper()
	var runID uuid.UUID
	if err := h.pool.QueryRow(
		context.Background(), `
		INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id, execution_authorized_by_user_id, status, run_type)
		VALUES ($1, $2, $3, $4, $5, 'queued', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`,
		h.fixture.CompanyA, h.fixture.AgentA, conversationID, h.fixture.RuntimeA, h.fixture.UserA,
	).Scan(&runID); err != nil {
		t.Fatalf("seed queued run: %v", err)
	}
	return runID
}

// seedUserItem writes the user's words for a Run that already exists — the
// production order, since the item is what carries run_id. seq is allocated
// conversation-wide, so it is taken from whatever the conversation is already
// at rather than passed in.
func (h *runHarness) seedUserItem(
	t *testing.T,
	conversationID uuid.UUID,
	runID uuid.UUID,
	content string,
) uuid.UUID {
	t.Helper()
	id := newPGUUID(t)
	dbtest.MustExec(t, context.Background(), h.pool, `
		INSERT INTO conversation_items
			(id, company_id, conversation_id, run_id, seq, type, author_kind,
			 author_id, content)
		SELECT $1, $2, $3, $4,
		       COALESCE((
		           SELECT max(seq) FROM conversation_items
		           WHERE conversation_id = $3
		       ), 0) + 1,
		       'user_message', 'user', $5, $6`,
		id, h.fixture.CompanyA, conversationID, runID, h.fixture.UserA, content)
	return id
}

func TestResumeUnsafeFailureUsesCurrentProviderSignals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		failureReason string
		errorText     string
		want          bool
	}{
		{
			name:      "claude invalid request",
			errorText: `API Error: 400 {"type":"invalid_request_error"}`,
			want:      true,
		},
		{
			name:          "canonical context overflow",
			failureReason: string(runfailure.ReasonAgentContextOverflow),
			want:          true,
		},
		{
			name:      "unsupported credential resolver wording",
			errorText: "could not resolve authentication method",
			want:      false,
		},
		{
			name:      "unsupported message-position wording",
			errorText: "the message at position 4 with role assistant must not be empty",
			want:      false,
		},
		{
			name:          "ordinary codex process failure",
			failureReason: string(runfailure.ReasonAgentProcessFailure),
			errorText:     "codex process exited with status 1",
			want:          false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ResumeUnsafeFailure(test.failureReason, test.errorText); got != test.want {
				t.Fatalf(
					"ResumeUnsafeFailure(%q, %q) = %v, want %v",
					test.failureReason,
					test.errorText,
					got,
					test.want,
				)
			}
		})
	}
}

// The wire pair attempt/maxAttempts has to answer "is a retry coming?" on
// its own, because failureReason is an open set a client cannot classify. A
// reason the auto-retry path will not act on therefore reports the run's own
// attempt as the ceiling, so attempt == maxAttempts always reads as final.
func TestMaxAttemptsForReportsTheAttemptAsFinalWhenNoRetryComes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		reason  string
		attempt int32
		want    int32
	}{
		{name: "not failed", reason: "", attempt: 1, want: 2},
		{name: "runtime offline", reason: string(runfailure.ReasonRuntimeOffline), attempt: 1, want: 2},
		{name: "provider network", reason: string(runfailure.ReasonAgentProviderNetwork), attempt: 2, want: 3},
		{name: "provider quota", reason: string(runfailure.ReasonAgentProviderQuotaLimit), attempt: 1, want: 1},
		{
			name:   "provider quota on a retry child",
			reason: string(runfailure.ReasonAgentProviderQuotaLimit), attempt: 2, want: 2,
		},
		{name: "process failure", reason: string(runfailure.ReasonAgentProcessFailure), attempt: 1, want: 1},
		// Two provider stream cuts earn a third attempt, which sits above the
		// generic ceiling: whatever it ends as, it never reads "3 of 2".
		{name: "third attempt not failed", reason: "", attempt: 3, want: 3},
		{
			name:   "third attempt runtime offline",
			reason: string(runfailure.ReasonRuntimeOffline), attempt: 3, want: 3,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := MaxAttemptsFor(test.reason, test.attempt); got != test.want {
				t.Fatalf("MaxAttemptsFor(%q, %d) = %d, want %d",
					test.reason, test.attempt, got, test.want)
			}
		})
	}
}

func jsonContainsKey(encoded []byte, key string) bool {
	var object map[string]any
	if json.Unmarshal(encoded, &object) != nil {
		return false
	}
	_, ok := object[key]
	return ok
}
