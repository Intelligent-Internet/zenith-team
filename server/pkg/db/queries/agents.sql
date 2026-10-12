-- name: LockActiveAgentParent :one
SELECT agents.*
FROM agents
WHERE agents.id = sqlc.arg(agent_id)::uuid
  AND agents.company_id = sqlc.arg(company_id)::uuid
  AND agents.archived_at IS NULL
FOR SHARE;

-- name: LockActiveCompanyAgents :many
-- The active Agents a Company Plan names, held FOR SHARE in id order so none
-- is archived while the apply assigns work to it.
SELECT agents.id
FROM agents
WHERE agents.company_id = sqlc.arg(company_id)::uuid
  AND agents.id = ANY(sqlc.arg(agent_ids)::uuid[])
  AND agents.archived_at IS NULL
ORDER BY agents.id
FOR SHARE;

-- name: InsertAgent :one
INSERT INTO agents (
    company_id, name, title, instructions, reports_to_agent_id,
    provider, created_by_user_id, model,
    thinking_level, settings, avatar_key, accent_color, max_concurrent_runs
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(name)::text,
    sqlc.arg(title)::text,
    sqlc.arg(instructions)::text,
    sqlc.narg(reports_to_agent_id)::uuid,
    -- The template's provider (ADR 0014): chosen at creation, immutable, and
    -- the provider every binding of this Agent must match.
    sqlc.arg(provider)::text,
    -- The creator: who may configure the Agent afterwards, with the Company
    -- owners and admins (ADR 0013).
    sqlc.arg(created_by_user_id)::uuid,
    sqlc.narg(model)::text,
    sqlc.narg(thinking_level)::text,
    '{}'::jsonb,
    sqlc.narg(avatar_key)::text,
    sqlc.narg(accent_color)::text,
    -- 1..10; the request binder owns that rule, so a supplied cap arrives
    -- validated and an absent one takes the column default.
    COALESCE(sqlc.narg(max_concurrent_runs)::integer, 3)
)
RETURNING agents.*;

-- name: LockCompanyAgentGraph :many
SELECT agents.*
FROM agents
WHERE agents.company_id = sqlc.arg(company_id)::uuid
ORDER BY agents.id
FOR UPDATE;

-- name: UpdateAgentConfiguration :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
)
UPDATE agents
SET name = sqlc.arg(name)::text,
    title = sqlc.arg(title)::text,
    instructions = sqlc.arg(instructions)::text,
    settings = agents.settings
        || CASE WHEN sqlc.narg(capabilities)::jsonb IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('capabilities', sqlc.narg(capabilities)::jsonb) END
        || CASE WHEN sqlc.narg(voice)::jsonb IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('voice', sqlc.narg(voice)::jsonb) END,
    -- The Run brief filters Plugins on this column, written from the same
    -- capabilities value as the document above.
    disabled_plugin_ids = CASE WHEN sqlc.narg(capabilities)::jsonb IS NULL THEN agents.disabled_plugin_ids
        ELSE COALESCE(sqlc.narg(disabled_plugin_ids)::uuid[], '{}') END,
    reports_to_agent_id = sqlc.narg(reports_to_agent_id)::uuid,
    model = sqlc.narg(model)::text,
    thinking_level = sqlc.narg(thinking_level)::text,
    avatar_key = sqlc.narg(avatar_key)::text,
    accent_color = sqlc.narg(accent_color)::text,
    -- 1..10; the request binder owns that rule (the named maxConcurrentRuns
    -- binding), so like every other column here the value arrives validated.
    max_concurrent_runs = sqlc.arg(max_concurrent_runs)::integer,
    updated_at = statement_clock.now_at,
    version = agents.version + 1
FROM statement_clock
WHERE agents.id = sqlc.arg(agent_id)::uuid
  AND agents.company_id = sqlc.arg(company_id)::uuid
  AND agents.version = sqlc.arg(expected_version)::bigint
RETURNING agents.*;

-- name: ArchiveAgent :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
)
UPDATE agents
SET archived_at = statement_clock.now_at,
    updated_at = statement_clock.now_at,
    version = agents.version + 1
FROM statement_clock
WHERE agents.id = sqlc.arg(agent_id)::uuid
  AND agents.company_id = sqlc.arg(company_id)::uuid
  AND agents.version = sqlc.arg(expected_version)::bigint
  AND agents.archived_at IS NULL
RETURNING agents.*;

-- name: RestoreAgent :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
)
UPDATE agents
SET archived_at = NULL,
    updated_at = statement_clock.now_at,
    version = agents.version + 1
FROM statement_clock
WHERE agents.id = sqlc.arg(agent_id)::uuid
  AND agents.company_id = sqlc.arg(company_id)::uuid
  AND agents.version = sqlc.arg(expected_version)::bigint
  AND agents.archived_at IS NOT NULL
RETURNING agents.*;

-- name: LockActiveTasksExecutedByAgent :many
SELECT tasks.id
FROM tasks
WHERE tasks.company_id = sqlc.arg(company_id)::uuid
  AND tasks.executed_by_agent_id = sqlc.arg(agent_id)::uuid
  AND tasks.status NOT IN ('done', 'failed', 'cancelled')
ORDER BY tasks.id
FOR UPDATE;

-- name: CountActiveTaskLaneRunsByAgentForBinder :many
-- activeRunCount for the Agent projection: how many of each agent's runs hold
-- one of its max_concurrent_runs slots on the VIEWER's binding runtime right
-- now — capacity is per binding (ADR 0014), so the count is the viewer's. The
-- status and lane predicate is deliberately identical to
-- CountActiveTaskLaneRuns in runs.sql — the claim gate's count — so the number
-- a client renders and the number the gate enforces can never disagree. That
-- includes the divergence: non-Task runs in the direct and Schedule lanes
-- neither wait for a slot nor occupy one, so they are excluded here exactly as
-- they are there. TestCapacityGateCountPredicatesMatch (internal/db) holds the
-- two to that word for word, so this comment is not the only thing keeping
-- them together.
--
-- This is a display read, not the gate. It takes no locks and may lag a claim
-- committing under it; the gate recounts under the claim-slot lock.
SELECT runs.agent_id, count(*)::bigint AS active_run_count
FROM runs
JOIN conversations ON conversations.id = runs.conversation_id
JOIN agent_runtime_bindings AS binding
  ON binding.company_id = runs.company_id
 AND binding.agent_id = runs.agent_id
 AND binding.runtime_id = runs.runtime_id
 AND binding.user_id = sqlc.arg(user_id)::uuid
WHERE runs.company_id = sqlc.arg(company_id)::uuid
  AND runs.status IN ('dispatched', 'running', 'waiting_local_directory')
  AND conversations.kind = 'task'
GROUP BY runs.agent_id;

-- name: ListAgentStatusesForRuntime :many
-- The agent.status availability fanout: every binding on the runtime whose
-- liveness just changed, each carrying the binder and the same availability
-- the Agent projection serves that binder (not archived, runtime online and
-- usable, provider signed in) so the event and the roster row can never
-- disagree. Every binding on a runtime belongs to its registrant, so the
-- frame goes to one member.
SELECT
    agents.id,
    binding.user_id,
    (
        agents.archived_at IS NULL
        AND runtimes.removed_at IS NULL
        AND runtimes.status = 'online'
        AND runtimes.unusable_reason_code IS NULL
        AND runtimes.provider_sign_in_state IS DISTINCT FROM 'signed_out'
    )::boolean AS runtime_available
FROM agent_runtime_bindings AS binding
JOIN agents
  ON agents.company_id = binding.company_id
 AND agents.id = binding.agent_id
JOIN runtimes
  ON runtimes.company_id = binding.company_id
 AND runtimes.id = binding.runtime_id
WHERE binding.runtime_id = sqlc.arg(runtime_id)::uuid
  AND agents.archived_at IS NULL
ORDER BY agents.id;

-- name: ListAgentCapabilitySources :many
-- The ids a capability selection may deny: the Company's plugin and MCP
-- catalog for the Agent's provider. Template state, so no runtime narrows it —
-- an id the binding's machine does not carry is simply not there at run time.
SELECT plugins.id, 'plugin'::text AS source_type
FROM plugin_catalog_items AS plugins
WHERE plugins.company_id = sqlc.arg(company_id)::uuid
  AND plugins.provider = sqlc.arg(provider)::text
UNION ALL
SELECT connectors.id, 'mcp'::text AS source_type
FROM connector_catalog_items AS connectors
WHERE connectors.company_id = sqlc.arg(company_id)::uuid
  AND connectors.provider = sqlc.arg(provider)::text
  AND connectors.source IN ('codex_mcp', 'claude_mcp');
