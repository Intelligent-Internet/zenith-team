-- name: GetProactiveModeForMember :one
SELECT
    companies.id AS company_id,
    companies.proactive_mode_enabled,
    companies.proactive_mode_version,
    companies.proactive_mode_enabled_by_user_id,
    companies.proactive_mode_conversation_id,
    company_members.role AS member_role
FROM companies
JOIN company_members
  ON company_members.company_id = companies.id
 AND company_members.user_id = sqlc.arg(user_id)::uuid
 AND company_members.status = 'active'
WHERE companies.id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL;

-- name: ResolveProactiveModeConversation :one
SELECT conversations.id
FROM conversations
JOIN agents
  ON agents.id = conversations.agent_id
 AND agents.company_id = conversations.company_id
 AND agents.reports_to_agent_id IS NULL
 AND agents.archived_at IS NULL
WHERE conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.created_by_user_id = sqlc.arg(user_id)::uuid
  AND conversations.kind = 'direct'
  AND conversations.archived_at IS NULL
ORDER BY conversations.created_at, conversations.id
LIMIT 1;

-- name: UpdateProactiveMode :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
)
UPDATE companies
SET proactive_mode_enabled = sqlc.arg(enabled)::boolean,
    proactive_mode_enabled_by_user_id = sqlc.narg(enabled_by_user_id)::uuid,
    proactive_mode_conversation_id = sqlc.narg(conversation_id)::uuid,
    proactive_mode_version = companies.proactive_mode_version + 1,
    updated_at = statement_clock.now_at
FROM statement_clock
WHERE companies.id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL
  AND companies.proactive_mode_version = sqlc.arg(expected_version)::bigint
  AND companies.proactive_mode_version < 9007199254740991
RETURNING
    companies.id AS company_id,
    companies.proactive_mode_enabled,
    companies.proactive_mode_version,
    companies.proactive_mode_enabled_by_user_id,
    companies.proactive_mode_conversation_id;
