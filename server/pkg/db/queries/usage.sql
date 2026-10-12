-- Usage reports read immutable facts directly. A direct conversation is
-- visible only to its creator. Task-chat Runs are counted Company-wide on
-- purpose, including those in Projects the reader cannot open (ADR 0017):
-- a count names no Task or chat. The top Tasks list is scoped by Project in
-- Go (readUsageTopTasks).

-- name: GetActivityInsight :one
WITH actor_runs AS MATERIALIZED (
    SELECT runs.id, runs.task_id, runs.conversation_id, runs.status, runs.created_at, runs.updated_at
    FROM runs
    JOIN conversations ON conversations.id = runs.conversation_id
    LEFT JOIN tasks ON tasks.id = runs.task_id
    WHERE runs.company_id = sqlc.arg(company_id)::uuid
      AND (
          (conversations.kind = 'direct'
              AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
          OR (conversations.kind = 'task'
              AND tasks.creator_user_id = sqlc.arg(actor_user_id)::uuid)
      )
), totals AS (
    SELECT
        count(DISTINCT task_id) FILTER (
            WHERE task_id IS NOT NULL AND status = 'completed'
        )::text AS successful_tasks,
        count(DISTINCT conversation_id) FILTER (
            WHERE updated_at IS NOT NULL
        )::text AS total_sessions,
        count(*) FILTER (WHERE status = 'completed')::text AS completed_runs,
        count(*) FILTER (WHERE status = 'failed')::text AS failed_runs,
        count(*)::text AS total_runs,
        -- When the caller's most recent run started, as the contract says:
        -- updated_at would move with every heartbeat of a long run.
        max(created_at)::timestamptz AS last_active_at
    FROM actor_runs
), ranked_model AS (
    SELECT run_usage.provider, run_usage.model
    FROM run_usage
    JOIN actor_runs ON actor_runs.id = run_usage.run_id
    GROUP BY run_usage.provider, run_usage.model
    ORDER BY
        count(DISTINCT actor_runs.id) DESC,
        max(run_usage.created_at) DESC,
        run_usage.provider,
        run_usage.model
    LIMIT 1
)
SELECT
    ranked_model.provider,
    ranked_model.model,
    totals.successful_tasks,
    totals.total_sessions,
    totals.completed_runs,
    totals.failed_runs,
    totals.total_runs,
    totals.last_active_at
FROM totals
LEFT JOIN ranked_model ON true;

-- name: ListCompanyUsageAgents :many
WITH eligible_agents AS MATERIALIZED (
    SELECT
        agents.id AS agent_id,
        agents.name,
        agents.title,
        COALESCE(agents.avatar_key, '')::text AS avatar_key,
        (agents.reports_to_agent_id IS NULL)::boolean AS is_ceo
    FROM agents
    WHERE agents.company_id = sqlc.arg(company_id)::uuid
      AND agents.archived_at IS NULL
), visible_runs AS MATERIALIZED (
    SELECT runs.id, runs.agent_id
    FROM runs
    JOIN conversations ON conversations.id = runs.conversation_id
    JOIN eligible_agents ON eligible_agents.agent_id = runs.agent_id
    WHERE runs.company_id = sqlc.arg(company_id)::uuid
      AND runs.created_at >= sqlc.arg(start_at)::timestamptz
      AND runs.created_at < sqlc.arg(end_at)::timestamptz
      AND (
          conversations.kind = 'task'
          OR conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
      )
), token_totals AS (
    SELECT
        visible_runs.agent_id,
        COALESCE(sum(run_usage.input_tokens), 0)::text AS input_tokens,
        COALESCE(sum(run_usage.output_tokens), 0)::text AS output_tokens,
        COALESCE(sum(run_usage.input_tokens::numeric + run_usage.output_tokens::numeric), 0)::text
            AS input_output_tokens,
        COALESCE(sum(run_usage.cache_read_tokens), 0)::text AS cache_read_tokens,
        COALESCE(sum(run_usage.cache_write_tokens), 0)::text AS cache_write_tokens
    FROM visible_runs
    JOIN run_usage ON run_usage.run_id = visible_runs.id
    GROUP BY visible_runs.agent_id
), run_totals AS (
    SELECT agent_id, count(*)::text AS run_count
    FROM visible_runs
    GROUP BY agent_id
)
SELECT
    eligible_agents.agent_id,
    eligible_agents.name,
    eligible_agents.title,
    eligible_agents.avatar_key,
    COALESCE(token_totals.input_tokens, '0')::text AS input_tokens,
    COALESCE(token_totals.output_tokens, '0')::text AS output_tokens,
    COALESCE(token_totals.input_output_tokens, '0')::text AS input_output_tokens,
    COALESCE(token_totals.cache_read_tokens, '0')::text AS cache_read_tokens,
    COALESCE(token_totals.cache_write_tokens, '0')::text AS cache_write_tokens,
    COALESCE(run_totals.run_count, '0')::text AS run_count
FROM eligible_agents
LEFT JOIN token_totals ON token_totals.agent_id = eligible_agents.agent_id
LEFT JOIN run_totals ON run_totals.agent_id = eligible_agents.agent_id
ORDER BY
    COALESCE(token_totals.input_output_tokens, '0')::numeric DESC,
    eligible_agents.is_ceo DESC,
    lower(eligible_agents.name),
    eligible_agents.agent_id;

-- name: GetCompanyUsageTotals :one
WITH visible_runs AS MATERIALIZED (
    SELECT runs.id
    FROM runs
    JOIN conversations ON conversations.id = runs.conversation_id
    WHERE runs.company_id = sqlc.arg(company_id)::uuid
      AND runs.created_at >= sqlc.arg(start_at)::timestamptz
      AND runs.created_at < sqlc.arg(end_at)::timestamptz
      AND (
          conversations.kind = 'task'
          OR conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
      )
), token_totals AS (
    SELECT
        COALESCE(sum(run_usage.input_tokens), 0)::text AS input_tokens,
        COALESCE(sum(run_usage.output_tokens), 0)::text AS output_tokens,
        COALESCE(sum(run_usage.input_tokens::numeric + run_usage.output_tokens::numeric), 0)::text
            AS input_output_tokens,
        COALESCE(sum(run_usage.cache_read_tokens), 0)::text AS cache_read_tokens,
        COALESCE(sum(run_usage.cache_write_tokens), 0)::text AS cache_write_tokens
    FROM run_usage
    JOIN visible_runs ON visible_runs.id = run_usage.run_id
)
SELECT token_totals.*, (SELECT count(*)::text FROM visible_runs) AS run_count
FROM token_totals;

-- name: InsertRunUsage :one
INSERT INTO run_usage (
    run_id, provider, model, input_tokens, output_tokens,
    cache_read_tokens, cache_write_tokens, usage_basis, provider_cost_usd, raw_json,
    reasoning_tokens, turns, provider_duration_ms, billing_mode
)
SELECT
    sqlc.arg(run_id)::uuid,
    sqlc.arg(provider)::text,
    sqlc.arg(model)::text,
    sqlc.arg(input_tokens)::bigint,
    sqlc.arg(output_tokens)::bigint,
    sqlc.arg(cache_read_tokens)::bigint,
    sqlc.arg(cache_write_tokens)::bigint,
    sqlc.arg(usage_basis)::text,
    sqlc.narg(cost_usd_ticks)::bigint * 0.0000000001,
    sqlc.arg(raw_json)::jsonb,
    sqlc.narg(reasoning_tokens)::bigint,
    sqlc.narg(turns)::integer,
    sqlc.narg(provider_duration_ms)::bigint,
    sqlc.arg(billing_mode)::text
ON CONFLICT (run_id, provider, model) DO NOTHING
RETURNING *;

-- name: GetRunUsage :one
SELECT *
FROM run_usage
WHERE run_id = sqlc.arg(run_id)::uuid
  AND provider = sqlc.arg(provider)::text
  AND model = sqlc.arg(model)::text;
