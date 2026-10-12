-- Run tokens: brt_ credentials minted at claim for the agent process
--. One row per claim; revoked on fail.

-- name: CreateRunToken :one
INSERT INTO run_tokens (run_id, token_hash, expires_at)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetRunTokenByHash :one
SELECT * FROM run_tokens
WHERE token_hash = $1 AND expires_at > now();

-- name: ResolveLiveRunOrigin :one
-- A run token is proof for one currently-running origin only. The HTTP layer
-- still authenticates the caller as the ordinary UserPrincipal; this row is
-- supplemental agent/run scope and never becomes a second principal.
SELECT
    runs.id AS run_id,
    runs.company_id,
    runs.agent_id,
    runs.task_id,
    runs.conversation_id,
    runs.invocation_id,
    conversations.kind AS conversation_kind,
    conversations.created_by_user_id AS origin_user_id,
    invocation.caller_user_id AS invocation_caller_id,
    -- The channel or group DM a tagged Run answers into. It marks a tagged
    -- Run and bounds nothing: the Run uses its Authorizer's access (D31).
    -- NULL for any other Run.
    invocation.channel_id AS invocation_channel_id,
    -- A Task Run answers into its Task chat, whose readers are that chat's
    -- Project's readers, so it reads and files work only there (ADR 0017).
    conversations.project_id AS conversation_project_id,
    runs.execution_authorized_by_user_id,
    -- The root Agent plans the Company: in its person's direct chat, with
    -- its Company Plan tools on, its reads are not narrowed to its own
    -- Tasks (IA-FR12). Both facts are read live here, never as a filter.
    (agents.reports_to_agent_id IS NULL)::boolean AS root_agent,
    (COALESCE(agents.settings->'capabilities'->'disabledBuiltIns', '[]'::jsonb) @> '"planning"'::jsonb)::boolean AS planning_disabled
FROM run_tokens
JOIN runs ON runs.id = run_tokens.run_id
JOIN agents ON agents.company_id = runs.company_id AND agents.id = runs.agent_id
LEFT JOIN channel_invocations invocation ON invocation.id=runs.invocation_id
JOIN conversations
  ON conversations.company_id = runs.company_id
 AND conversations.id = runs.conversation_id
WHERE run_tokens.token_hash = sqlc.arg(token_hash)::bytea
  AND run_tokens.expires_at > statement_timestamp()
  AND runs.status = 'running';

-- name: DeleteRunTokensByRun :exec
DELETE FROM run_tokens WHERE run_id = $1;

-- name: DeleteExpiredRunTokens :exec
DELETE FROM run_tokens WHERE expires_at <= now();
