package db

import (
	"strings"
	"testing"
)

func TestHumanInputOriginRunLockRequiresExactRunningRun(t *testing.T) {
	t.Parallel()

	query := normalizedQuery(
		t,
		queryFileSource(t, "runs.sql"),
		"LockLiveHumanInputOriginRun",
	)
	for _, fragment := range []string{
		"runs.id = sqlc.arg(run_id)::uuid",
		"runs.company_id = sqlc.arg(company_id)::uuid",
		"runs.agent_id = sqlc.arg(agent_id)::uuid",
		"runs.task_id is not distinct from sqlc.narg(task_id)::uuid",
		"runs.conversation_id = sqlc.arg(conversation_id)::uuid",
		"runs.status = 'running'",
		"for update",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("live human-input origin Run lock lacks %q: %s", fragment, query)
		}
	}
}

func TestHumanInputOriginConversationLockRequiresExactOrigin(t *testing.T) {
	t.Parallel()

	query := normalizedQuery(
		t,
		queryFileSource(t, "conversations.sql"),
		"LockHumanInputOriginConversation",
	)
	for _, fragment := range []string{
		"conversations.id = sqlc.arg(conversation_id)::uuid",
		"conversations.company_id = sqlc.arg(company_id)::uuid",
		"conversations.kind = sqlc.arg(conversation_kind)::text",
		"conversations.task_id is not distinct from sqlc.narg(task_id)::uuid",
		"conversations.kind = 'task'",
		"conversations.agent_id = sqlc.arg(agent_id)::uuid",
		"conversations.created_by_user_id is not distinct from sqlc.narg(origin_user_id)::uuid",
		"conversations.archived_at is null",
		"for update",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("human-input origin Conversation lock lacks %q: %s", fragment, query)
		}
	}
}
