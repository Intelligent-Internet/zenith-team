//go:build integration

package db

import (
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// The bindings cutover (000059, ADR 0014) cancels the pending Runs the new
// claim can never admit: those whose authorizer does not own their runtime,
// and unclaimed ones on a runtime that no binding of their Agent and
// authorizer names — an Agent that moved machines left its queued Runs
// behind. A channel tag's Run (000042) holds its invocation's one active
// slot in the channel-Agent Conversation, so the cutover must close that
// invocation too, or every tag queued behind it stalls. The runtime owner's
// tag on the Agent's current runtime is untouched, and so is a Run already
// claimed and waiting for its local directory.
func TestBindingsCutoverClosesTheChannelTagsItCancels(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := runMigration(ctx, pool, "prepare channel tags", func(m *migrate.Migrate) error {
		return m.Migrate(58)
	}); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	colleagueID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO users (id, email, display_name)
		VALUES ($1, 'channel-colleague@example.com', 'Channel Colleague')`, colleagueID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, f.CompanyA, colleagueID)
	// A second machine of UserA's that AgentA ran on before it moved to
	// RuntimeA: its Runs there stayed behind.
	leftRuntimeID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes
			(id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-left', 'Left laptop', 'codex', 'online', $3, now())`,
		leftRuntimeID, f.CompanyA, f.UserA)

	// Before 000059 AgentA runs on RuntimeA, which UserA registered.
	ownerTag := seedActiveChannelTag(t, f, pool, f.UserA, f.RuntimeA)
	colleagueTag := seedActiveChannelTag(t, f, pool, colleagueID, f.RuntimeA)
	leftBehindTag := seedActiveChannelTag(t, f, pool, f.UserA, leftRuntimeID)
	var deferredID, waitingID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs
			(company_id, agent_id, conversation_id, runtime_id, run_type,
			 status, fire_at, execution_authorized_by_user_id)
		VALUES ($1, $2, $3, $4, 'direct_chat', 'deferred', now() + interval '1 hour', $5)
		RETURNING id`, f.CompanyA, f.AgentA, f.DirectConversationA, leftRuntimeID, f.UserA).
		Scan(&deferredID); err != nil {
		t.Fatalf("seed deferred Run: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs
			(company_id, agent_id, conversation_id, runtime_id, run_type,
			 status, execution_authorized_by_user_id)
		VALUES ($1, $2, $3, $4, 'direct_chat', 'waiting_local_directory', $5)
		RETURNING id`, f.CompanyA, f.AgentA, f.DirectConversationA, leftRuntimeID, f.UserA).
		Scan(&waitingID); err != nil {
		t.Fatalf("seed waiting Run: %v", err)
	}

	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate to head: %v", err)
	}

	for id, want := range map[uuid.UUID]string{deferredID: "cancelled", waitingID: "waiting_local_directory"} {
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, id).Scan(&status); err != nil {
			t.Fatalf("read run %s: %v", id, err)
		}
		if status != want {
			t.Errorf("Run left on the old runtime = %s, want %s", status, want)
		}
	}

	for _, tc := range []struct {
		name                                              string
		tag                                               channelTag
		wantInvocation, wantPublication, wantReason, want string
	}{
		{"colleague's tag", colleagueTag, "cancelled", "blocked", "agent_unbound", "cancelled"},
		{"owner's tag left behind", leftBehindTag, "cancelled", "blocked", "agent_unbound", "cancelled"},
		{"runtime owner's tag", ownerTag, "active", "pending", "", "queued"},
	} {
		var status, publication, reason string
		if err := pool.QueryRow(ctx, `
			SELECT status, publication_status, COALESCE(failure_reason, '')
			FROM channel_invocations WHERE id = $1`, tc.tag.invocationID).
			Scan(&status, &publication, &reason); err != nil {
			t.Fatalf("%s: read invocation: %v", tc.name, err)
		}
		if status != tc.wantInvocation || publication != tc.wantPublication || reason != tc.wantReason {
			t.Errorf("%s: invocation = %s/%s/%q, want %s/%s/%q",
				tc.name, status, publication, reason,
				tc.wantInvocation, tc.wantPublication, tc.wantReason)
		}
		var runStatus string
		if err := pool.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, tc.tag.runID).
			Scan(&runStatus); err != nil {
			t.Fatalf("%s: read run: %v", tc.name, err)
		}
		if runStatus != tc.want {
			t.Errorf("%s: run status = %s, want %s", tc.name, runStatus, tc.want)
		}
	}
}

type channelTag struct {
	invocationID, runID uuid.UUID
}

// seedActiveChannelTag writes the rows a channel tag has once promoted
// (000042): a channel with the caller and AgentA, the caller's message, the
// channel-Agent Conversation, the active invocation and its queued Run on
// runtimeID, authorized by the caller.
func seedActiveChannelTag(
	t *testing.T,
	f dbtest.Fixture,
	pool *pgxpool.Pool,
	callerID, runtimeID uuid.UUID,
) channelTag {
	t.Helper()
	ctx := t.Context()
	channelID := dbtest.SeedDiscussionChannel(t, ctx, pool, f.CompanyA, callerID, []uuid.UUID{f.AgentA})
	var tag channelTag
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		var messageID, conversationID uuid.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO conversation_items
				(company_id, conversation_id, seq, type, author_kind, author_id,
				 content, body, request_key, send_fingerprint)
			VALUES ($1, $2, 1, 'user_message', 'user', $3, '@Agent please',
				'{"markdown":"@Agent please"}'::jsonb, gen_random_uuid(),
				decode(repeat('11', 32), 'hex'))
			RETURNING id`, f.CompanyA, channelID, callerID).Scan(&messageID); err != nil {
			t.Fatalf("seed channel message: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO conversations (company_id, kind, channel_id, agent_id, created_by_user_id, title)
			VALUES ($1, 'channel_agent', $2, $3, $4, 'Agent A')
			RETURNING id`, f.CompanyA, channelID, f.AgentA, f.UserA).Scan(&conversationID); err != nil {
			t.Fatalf("seed channel-Agent Conversation: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO channel_invocations
				(company_id, channel_id, conversation_id, agent_id, trigger_message_id,
				 thread_root_id, caller_user_id, agent_generation, member_generation,
				 channel_generation, trigger_content, trigger_revision, status)
			VALUES ($1, $2, $3, $4, $5, $5, $6, 1, 1, 1, '@Agent please', 1, 'active')
			RETURNING id`, f.CompanyA, channelID, conversationID, f.AgentA, messageID, callerID).
			Scan(&tag.invocationID); err != nil {
			t.Fatalf("seed channel invocation: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO runs
				(company_id, agent_id, conversation_id, runtime_id, run_type,
				 status, invocation_id, execution_authorized_by_user_id)
			VALUES ($1, $2, $3, $4, 'channel_agent', 'queued', $5, $6)
			RETURNING id`, f.CompanyA, f.AgentA, conversationID, runtimeID,
			tag.invocationID, callerID).Scan(&tag.runID); err != nil {
			t.Fatalf("seed channel Run: %v", err)
		}
	})
	return tag
}
