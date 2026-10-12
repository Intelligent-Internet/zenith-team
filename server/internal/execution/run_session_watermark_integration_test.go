//go:build integration

package execution

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// seedTerminalRun inserts one terminal direct-chat Run with the session and
// transcript head a claim will read back, updated_at spaced so the lookup's
// newest-first order is deterministic.
func seedTerminalRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	status, sessionID string,
	transcriptThroughSeq int64,
	age time.Duration,
) uuid.UUID {
	t.Helper()
	var session any
	if sessionID != "" {
		session = sessionID
	}
	var runID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs
			(company_id, agent_id, conversation_id, runtime_id, status, updated_at,
			 run_type, revision, session_id, transcript_through_seq)
		VALUES ($1, $2, $3, $4, $5, now() - $6::interval, 'direct_chat', 1, $7, $8)
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA, fixture.DirectConversationA, fixture.RuntimeA,
		status, age.String(), session, transcriptThroughSeq,
	).Scan(&runID); err != nil {
		t.Fatalf("seed %s run: %v", status, err)
	}
	return runID
}

// The claim tells the daemon what the resumed provider session has already
// received, so the daemon inlines only what came after. That watermark is the
// Run's own trigger or its transcript head, whichever is later.
func TestGetLastRunSessionForConversationReportsTheSessionWatermark(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)
	lookup := func(t *testing.T) generated.GetLastRunSessionForConversationRow {
		t.Helper()
		row, err := queries.GetLastRunSessionForConversation(ctx, generated.GetLastRunSessionForConversationParams{
			ConversationID: fixture.DirectConversationA, AgentID: fixture.AgentA,
		})
		if err != nil {
			t.Fatalf("GetLastRunSessionForConversation() error = %v", err)
		}
		return row
	}

	completed := seedTerminalRun(t, ctx, pool, fixture, "completed", "sess-completed", 5, 4*time.Minute)
	seedChatItem(t, ctx, pool, fixture.CompanyA, fixture.DirectConversationA, completed, 1, "user_message", "user", fixture.UserA, "first")
	if row := lookup(t); row.SessionID.String != "sess-completed" || row.ThroughSeq != 5 {
		t.Fatalf("after a completed Run: session %q, through_seq %d; want sess-completed through 5 (its transcript head)", row.SessionID.String, row.ThroughSeq)
	}

	failedBeforeExecution := seedTerminalRun(t, ctx, pool, fixture, "failed", "", 0, 3*time.Minute)
	seedChatItem(t, ctx, pool, fixture.CompanyA, fixture.DirectConversationA, failedBeforeExecution, 6, "user_message", "user", fixture.UserA, "second")
	if row := lookup(t); row.SessionID.Valid || row.ThroughSeq != 6 {
		t.Fatalf("after a Run that failed before execution: session %q (valid=%v), through_seq %d; want no session and its trigger seq 6", row.SessionID.String, row.SessionID.Valid, row.ThroughSeq)
	}

	cancelled := seedTerminalRun(t, ctx, pool, fixture, "cancelled", "sess-cancelled", 9, 2*time.Minute)
	seedChatItem(t, ctx, pool, fixture.CompanyA, fixture.DirectConversationA, cancelled, 7, "user_message", "user", fixture.UserA, "third")
	if row := lookup(t); row.SessionID.Valid || row.ThroughSeq != 6 {
		t.Fatalf("after a cancelled Run: session %q, through_seq %d; want the cancelled Run skipped", row.SessionID.String, row.ThroughSeq)
	}

	seedTerminalRun(t, ctx, pool, fixture, "failed", "", 0, time.Minute)
	if row := lookup(t); row.SessionID.Valid || row.ThroughSeq != 0 {
		t.Fatalf("after a retry child with no trigger and no items: session %q, through_seq %d; want 0, which over-inlines rather than omits", row.SessionID.String, row.ThroughSeq)
	}
}
