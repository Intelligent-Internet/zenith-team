//go:build integration

package agent

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// A member's next tag of the Agent in a channel waits behind their first
// one. When an unbind or a runtime removal without a target cancels the
// first tag's Run, the promotion that follows must find no binding for that
// member and close the next tag with agent_unbound. Queuing its Run on the
// machine the member just left would hold the channel-Agent Conversation's
// one active slot with a Run no claim can admit.
func TestDroppingABindingClosesTheMembersNextTag(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		drop   func(*testing.T, bindingDropEnv) error
	}{
		{
			name:   "unbind",
			reason: execution.RunReasonAgentUnbound,
			drop: func(t *testing.T, env bindingDropEnv) error {
				t.Helper()
				bindings, err := NewBindingService(BindingServiceOptions{Store: env.store, Bus: mustAgentTestBus(t)})
				if err != nil {
					t.Fatalf("NewBindingService() error = %v", err)
				}
				return bindings.Unbind(env.ctx, ownerOf(env.fixture), env.fixture.AgentA)
			},
		},
		{
			name:   "runtime removal without a target",
			reason: execution.RunReasonRuntimeRemoved,
			drop: func(t *testing.T, env bindingDropEnv) error {
				t.Helper()
				setRuntimeOffline(t, env.ctx, env.pool, env.fixture.RuntimeA)
				service, _, _ := mustRemovalService(t, env.store)
				_, err := service.Remove(env.ctx, ownerOf(env.fixture), env.fixture.RuntimeA, RuntimeRemovalInput{})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, store := startAgentPostgres(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			fixture := dbtest.SeedFixture(t, ctx, pool)
			tags := seedSameCallerTags(t, ctx, pool, store, fixture)

			env := bindingDropEnv{ctx: ctx, pool: pool, store: store, fixture: fixture}
			if err := tc.drop(t, env); err != nil {
				t.Fatalf("drop binding error = %v", err)
			}

			_, runStatus, runReason := readRun(t, ctx, pool, tags.firstRun)
			if runStatus != "cancelled" || runReason != tc.reason {
				t.Fatalf("first tag Run = %s / %q, want cancelled / %s",
					runStatus, runReason, tc.reason)
			}
			if status, _, _ := readInvocation(t, ctx, pool, tags.first); status != "cancelled" {
				t.Fatalf("first tag = %s, want cancelled", status)
			}
			status, publication, reason := readInvocation(t, ctx, pool, tags.next)
			if status != "cancelled" || publication != "blocked" || reason != "agent_unbound" {
				t.Fatalf("next tag = %s/%s/%q, want cancelled/blocked/agent_unbound",
					status, publication, reason)
			}
			var runs int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE invocation_id = $1`, tags.next).
				Scan(&runs); err != nil {
				t.Fatalf("count next tag Runs: %v", err)
			}
			if runs != 0 {
				t.Fatalf("next tag Runs = %d, want none queued on the machine the member left", runs)
			}
		})
	}
}

// bindingDropEnv is what a case of TestDroppingABindingClosesTheMembersNextTag
// needs to drop the fixture owner's binding.
type bindingDropEnv struct {
	ctx     context.Context
	pool    *pgxpool.Pool
	store   *db.Store
	fixture dbtest.Fixture
}

// sameCallerTags are two tags of AgentA by the fixture owner in one channel:
// the first active with its queued Run on RuntimeA, the next queued behind it.
type sameCallerTags struct {
	first, firstRun, next uuid.UUID
}

func seedSameCallerTags(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store *db.Store,
	fixture dbtest.Fixture,
) sameCallerTags {
	t.Helper()
	q := store.Queries()
	channelID := dbtest.SeedDiscussionChannel(t, ctx, pool, fixture.CompanyA, fixture.UserA,
		[]uuid.UUID{fixture.AgentA})
	conversation, err := q.EnsureChannelAgentConversation(ctx, generated.EnsureChannelAgentConversationParams{
		ChannelID: channelID, AgentID: fixture.AgentA, CompanyID: fixture.CompanyA,
	})
	if err != nil {
		t.Fatalf("seed channel-Agent Conversation: %v", err)
	}
	var invocations []uuid.UUID
	for seq := 1; seq <= 2; seq++ {
		var messageID uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO conversation_items
				(company_id, conversation_id, seq, type, author_kind, author_id,
				 content, body, request_key, send_fingerprint)
			VALUES ($1, $2, $3, 'user_message', 'user', $4, '@Agent please',
				'{"markdown":"@Agent please"}'::jsonb, gen_random_uuid(),
				sha256(gen_random_uuid()::text::bytea))
			RETURNING id`, fixture.CompanyA, channelID, seq, fixture.UserA).Scan(&messageID); err != nil {
			t.Fatalf("seed channel message %d: %v", seq, err)
		}
		invocation, err := q.InsertChannelInvocation(ctx, generated.InsertChannelInvocationParams{
			ConversationID: conversation.ID, AgentID: fixture.AgentA, CallerUserID: fixture.UserA,
			MessageID: messageID, ChannelID: channelID, CompanyID: fixture.CompanyA,
		})
		if err != nil {
			t.Fatalf("seed channel tag %d: %v", seq, err)
		}
		invocations = append(invocations, invocation.ID)
	}
	tags := sameCallerTags{first: invocations[0], next: invocations[1]}
	dbtest.MustExec(t, ctx, pool, `UPDATE channel_invocations SET status = 'active' WHERE id = $1`, tags.first)
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs
			(company_id, agent_id, conversation_id, runtime_id, run_type,
			 status, invocation_id, execution_authorized_by_user_id)
		VALUES ($1, $2, $3, $4, 'channel_agent', 'queued', $5, $6)
		RETURNING id`, fixture.CompanyA, fixture.AgentA, conversation.ID, fixture.RuntimeA,
		tags.first, fixture.UserA).Scan(&tags.firstRun); err != nil {
		t.Fatalf("seed first tag Run: %v", err)
	}
	return tags
}

func readInvocation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (string, string, string) {
	t.Helper()
	var status, publication, reason string
	if err := pool.QueryRow(ctx, `
		SELECT status, publication_status, COALESCE(failure_reason, '')
		FROM channel_invocations WHERE id = $1`, id).Scan(&status, &publication, &reason); err != nil {
		t.Fatalf("read channel tag %s: %v", id, err)
	}
	return status, publication, reason
}
