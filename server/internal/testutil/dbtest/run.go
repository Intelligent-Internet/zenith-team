package dbtest

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RunSeed names the Run SeedRun inserts. The delegate rule
// (run_permission_held, D31) reads what a Run may do from the Run's own row,
// so a test that reads under a Run's scope needs a real Run.
type RunSeed struct {
	CompanyID, AgentID, RuntimeID uuid.UUID
	// ConversationID is the chat the Run answers into.
	ConversationID uuid.UUID
	// AuthorizerID is the person the Run works for.
	AuthorizerID uuid.UUID
	// TaskID is a Task chat's Task; zero for any other chat.
	TaskID uuid.UUID
	// InvocationID is a tagged Run's channel invocation; zero for any other
	// Run.
	InvocationID uuid.UUID
}

// SeedRun inserts a running Run and returns its id. Its type follows the
// seed: a Task Run with a Task, a channel Agent's Run with an invocation,
// else a direct chat's Run.
func SeedRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, seed RunSeed) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id, invocation_id,
		                  execution_authorized_by_user_id, status, run_type, context)
		VALUES ($1, $2, NULLIF($3, '00000000-0000-0000-0000-000000000000'::uuid), $4, $5,
		        NULLIF($6, '00000000-0000-0000-0000-000000000000'::uuid), $7, 'running',
		        CASE WHEN $3 <> '00000000-0000-0000-0000-000000000000'::uuid THEN 'task'
		             WHEN $6 <> '00000000-0000-0000-0000-000000000000'::uuid THEN 'channel_agent'
		             ELSE 'direct_chat' END,
		        '{}'::jsonb)
		RETURNING id`,
		seed.CompanyID, seed.AgentID, seed.TaskID, seed.ConversationID, seed.RuntimeID,
		seed.InvocationID, seed.AuthorizerID,
	).Scan(&id); err != nil {
		t.Fatalf("seed Run: %v", err)
	}
	return id
}

// SeedTaggedRun tags agentID in a channel or a direct message between people
// for callerID and inserts the Run that answers it, as a tag would: the
// caller's message in the chat, the channel Agent's own chat, an active
// invocation naming the chat, and a running Run the caller authorizes. The
// Run answers into the channel Agent's chat and uses the caller's access
// (D31). It returns the Run's id. One Agent holds one active tag per
// chat, so seed one Run per chat and Agent.
func SeedTaggedRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID, chatID, agentID, runtimeID, callerID uuid.UUID,
) uuid.UUID {
	t.Helper()
	var messageID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO conversation_items
			(company_id, conversation_id, seq, type, author_kind, author_id,
			 content, body, request_key, send_fingerprint)
		SELECT $1, $2, COALESCE(max(item.seq), 0) + 1, 'user_message', 'user', $3, '@Agent',
		       '{"markdown":"@Agent"}'::jsonb, gen_random_uuid(), decode(repeat('11', 32), 'hex')
		FROM conversation_items AS item WHERE item.conversation_id = $2
		RETURNING id`, companyID, chatID, callerID).Scan(&messageID); err != nil {
		t.Fatalf("seed the tag message: %v", err)
	}
	var agentChatID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO conversations (company_id, kind, channel_id, agent_id, created_by_user_id, title)
		VALUES ($1, 'channel_agent', $2, $3, $4, 'Tagged Agent')
		ON CONFLICT (channel_id, agent_id) WHERE kind = 'channel_agent' DO UPDATE SET title = EXCLUDED.title
		RETURNING id`, companyID, chatID, agentID, callerID).Scan(&agentChatID); err != nil {
		t.Fatalf("seed the channel Agent's chat: %v", err)
	}
	var invocationID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO channel_invocations
			(company_id, channel_id, conversation_id, agent_id, trigger_message_id, thread_root_id,
			 caller_user_id, agent_generation, member_generation, channel_generation,
			 trigger_content, trigger_revision, status)
		VALUES ($1, $2, $3, $4, $5, $5, $6, 1, 1, 1, '@Agent', 1, 'active')
		RETURNING id`, companyID, chatID, agentChatID, agentID, messageID, callerID).Scan(&invocationID); err != nil {
		t.Fatalf("seed the tag's invocation: %v", err)
	}
	return SeedRun(t, ctx, pool, RunSeed{
		CompanyID: companyID, AgentID: agentID, RuntimeID: runtimeID,
		ConversationID: agentChatID, AuthorizerID: callerID, InvocationID: invocationID,
	})
}
