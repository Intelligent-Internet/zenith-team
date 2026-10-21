-- Analytics budget policy and notification deduplication. Usage reads aggregate
-- indexed raw facts; no duplicate usage read model is maintained.

-- name: ListAnalyticsBudgets :many
SELECT id, company_id, owner_kind, agent_id, monthly_limit_usd_ticks,
       enabled, created_by_user_id, created_at, updated_at
FROM analytics_budgets
WHERE company_id = sqlc.arg(company_id)::uuid
ORDER BY owner_kind, agent_id NULLS FIRST, id;

-- name: UpsertAnalyticsBudget :one
INSERT INTO analytics_budgets (
    company_id, owner_kind, agent_id, monthly_limit_usd_ticks,
    enabled, created_by_user_id
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(owner_kind)::text,
    sqlc.narg(agent_id)::uuid,
    sqlc.arg(monthly_limit_usd_ticks)::bigint,
    sqlc.arg(enabled)::boolean,
    sqlc.arg(created_by_user_id)::uuid
)
ON CONFLICT (company_id, owner_kind, agent_id) DO UPDATE SET
    monthly_limit_usd_ticks = EXCLUDED.monthly_limit_usd_ticks,
    enabled = EXCLUDED.enabled,
    created_by_user_id = EXCLUDED.created_by_user_id,
    updated_at = statement_timestamp()
RETURNING id, company_id, owner_kind, agent_id, monthly_limit_usd_ticks,
          enabled, created_by_user_id, created_at, updated_at;

-- name: ListAnalyticsBudgetTargets :many
SELECT b.id, b.company_id, b.owner_kind, b.agent_id,
       CASE WHEN b.owner_kind = 'agent' THEN COALESCE(a.name, 'Archived Agent') ELSE c.name END::text AS owner_name,
       b.monthly_limit_usd_ticks, b.enabled, b.created_by_user_id,
       date_trunc('month', statement_timestamp() AT TIME ZONE 'UTC')::date AS period_start
FROM analytics_budgets b
JOIN companies c ON c.id = b.company_id
LEFT JOIN agents a ON a.id = b.agent_id AND a.company_id = b.company_id
WHERE b.enabled
  AND (b.company_id = sqlc.arg(company_id)::uuid)
ORDER BY b.id
FOR UPDATE OF b;

-- name: InsertAnalyticsBudgetAlert :one
INSERT INTO analytics_budget_alerts (budget_id, period_start, threshold_percent)
VALUES (sqlc.arg(budget_id)::uuid, sqlc.arg(period_start)::date,
        sqlc.arg(threshold_percent)::smallint)
ON CONFLICT (budget_id, period_start, threshold_percent) DO NOTHING
RETURNING budget_id, period_start, threshold_percent, crossed_at;

-- name: ListAnalyticsGroups :many
-- Every Analytics page read (summary, series, heatmap, breakdown, failures)
-- is this one statement, grouped by group_by: 'none' (one row), 'bucket'
-- (epoch seconds of the local bucket start), 'heatmap' ([weekday, hour]),
-- 'failure_reason', or a breakdown dimension (agent, model, member, project,
-- task, run_type, runtime). Each row is the group key and the standard
-- metric set as JSON.
--
-- Totals count every finished Run in the window and filters; scope
-- 'mine' binds authorizer_user_id to the caller. Project access (ADR 0017)
-- decides only names: a Project's or Task's id and title show only when the
-- viewer reads that Project. Runs of any other Project group into one
-- unnamed row, synthetic 'other_work', so the rows still add up to the total
-- and nothing names the hidden work. readable_projects checks each Project
-- the window touches once, through a correlated project_access lookup naming
-- the Project and the viewer; it never builds the viewer's readable set per
-- Run. A project_id or task_id filter is checked readable by the caller
-- first (usage.analyticsFilterReadable).
--
-- A Run belongs to its Task's Project, and after the Task is deleted to its
-- Task chat's Project (the chat keeps project_id, ADR 0017), as in Run history.
-- detail is false for the heatmap and failure reads, which use only counts,
-- tokens and cost: the time percentiles and unpriced models are then skipped.
WITH scoped_runs AS MATERIALIZED (
    SELECT r.id, r.agent_id, r.task_id, r.execution_authorized_by_user_id,
           r.status, r.run_type, r.attempt, r.failure_reason,
           r.queued_at, r.claimed_at, r.started_at, r.finished_at,
           coalesce(t.project_id, CASE WHEN c.kind = 'task' THEN c.project_id END) AS project_id,
           t.title AS task_title, t.key AS task_key,
           a.name AS agent_name, a.archived_at AS agent_archived_at,
           u.display_name AS member_name,
           CASE WHEN rt.owner_user_id = sqlc.arg(viewer_user_id)::uuid THEN rt.id END AS visible_runtime_id,
           CASE WHEN rt.owner_user_id = sqlc.arg(viewer_user_id)::uuid THEN rt.name END AS visible_runtime_name
    FROM runs AS r
    LEFT JOIN tasks AS t ON t.id = r.task_id AND t.company_id = r.company_id
    JOIN conversations AS c ON c.id = r.conversation_id AND c.company_id = r.company_id
    LEFT JOIN agents AS a ON a.id = r.agent_id AND a.company_id = r.company_id
    LEFT JOIN users AS u ON u.id = r.execution_authorized_by_user_id
    LEFT JOIN runtimes AS rt ON rt.id = r.runtime_id AND rt.company_id = r.company_id
    WHERE r.company_id = sqlc.arg(company_id)::uuid
      AND r.finished_at >= sqlc.arg(start_at)::timestamptz
      AND r.finished_at < sqlc.arg(end_at)::timestamptz
      AND (sqlc.narg(authorizer_user_id)::uuid IS NULL
           OR r.execution_authorized_by_user_id = sqlc.narg(authorizer_user_id)::uuid)
      AND (sqlc.narg(agent_id)::uuid IS NULL OR r.agent_id = sqlc.narg(agent_id)::uuid)
      AND (sqlc.narg(project_id)::uuid IS NULL OR coalesce(t.project_id, CASE WHEN c.kind = 'task' THEN c.project_id END) = sqlc.narg(project_id)::uuid)
      AND (sqlc.narg(task_id)::uuid IS NULL OR r.task_id = sqlc.narg(task_id)::uuid)
      AND (sqlc.arg(run_type)::text = '' OR r.run_type = sqlc.arg(run_type)::text)
      AND (sqlc.narg(runtime_id)::uuid IS NULL OR (r.runtime_id = sqlc.narg(runtime_id)::uuid AND EXISTS (
          SELECT 1 FROM runtimes AS visible_runtime
          WHERE visible_runtime.id = sqlc.narg(runtime_id)::uuid
            AND visible_runtime.company_id = r.company_id
            AND visible_runtime.owner_user_id = sqlc.arg(viewer_user_id)::uuid)))
      AND ((sqlc.arg(provider)::text = '' AND sqlc.arg(model)::text = '') OR EXISTS (
          SELECT 1 FROM run_usage AS filter_fact
          WHERE filter_fact.run_id = r.id
            AND (sqlc.arg(provider)::text = '' OR filter_fact.provider = sqlc.arg(provider)::text)
            AND (sqlc.arg(model)::text = '' OR filter_fact.model = sqlc.arg(model)::text)))
), readable_projects AS MATERIALIZED (
    SELECT run_project.project_id, project.title AS project_name
    FROM (SELECT DISTINCT project_id FROM scoped_runs WHERE project_id IS NOT NULL) AS run_project
    JOIN projects AS project
      ON project.company_id = sqlc.arg(company_id)::uuid AND project.id = run_project.project_id
    CROSS JOIN LATERAL (
        SELECT 1 FROM project_access AS access
        WHERE access.company_id = sqlc.arg(company_id)::uuid
          AND access.project_id = run_project.project_id
          AND access.user_id = sqlc.arg(viewer_user_id)::uuid
        LIMIT 1
    ) AS reader
), priced_facts AS (
    SELECT f.run_id, f.provider, f.model, f.input_tokens, f.output_tokens,
           f.cache_read_tokens, f.cache_write_tokens, f.reasoning_tokens,
           CASE WHEN f.billing_mode <> 'subscription' THEN round(f.provider_cost_usd * 10000000000) END AS reported,
           CASE WHEN f.billing_mode = 'api' AND f.provider_cost_usd IS NULL THEN
               round((f.input_tokens * price.input_usd_per_mtok + f.output_tokens * price.output_usd_per_mtok +
                      f.cache_read_tokens * price.cache_read_usd_per_mtok +
                      f.cache_write_tokens * price.cache_write_usd_per_mtok) * 10000) END AS estimated,
           CASE WHEN f.billing_mode = 'subscription' THEN f.input_tokens::numeric + f.output_tokens ELSE 0 END AS plan_tokens,
           f.billing_mode = 'subscription' AS plan,
           CASE WHEN f.billing_mode = 'api' AND f.provider_cost_usd IS NULL AND price.model IS NULL
                THEN jsonb_build_object('provider', f.provider, 'model', f.model) END AS unpriced_model
    FROM scoped_runs AS r
    JOIN run_usage AS f ON f.run_id = r.id
    LEFT JOIN LATERAL (
        SELECT model, input_usd_per_mtok, output_usd_per_mtok, cache_read_usd_per_mtok, cache_write_usd_per_mtok
        FROM model_prices
        WHERE provider = f.provider AND model = lower(f.model) AND effective_from <= r.finished_at
        ORDER BY effective_from DESC LIMIT 1
    ) AS price ON true
    WHERE (sqlc.arg(provider)::text = '' OR f.provider = sqlc.arg(provider)::text)
      AND (sqlc.arg(model)::text = '' OR f.model = sqlc.arg(model)::text)
), facts AS (
    -- Multi-model Runs count once, except in the model breakdown, which
    -- splits a Run's facts by model.
    SELECT run_id,
           CASE WHEN sqlc.arg(group_by)::text = 'model' THEN provider ELSE '' END AS provider,
           CASE WHEN sqlc.arg(group_by)::text = 'model' THEN model ELSE '' END AS model,
           sum(input_tokens)::numeric AS input, sum(output_tokens)::numeric AS output,
           sum(cache_read_tokens)::numeric AS cache_read, sum(cache_write_tokens)::numeric AS cache_write,
           sum(reasoning_tokens)::numeric AS reasoning, sum(reported) AS reported, sum(estimated) AS estimated,
           sum(plan_tokens) AS plan_tokens,
           count(*) FILTER (WHERE reported IS NOT NULL OR estimated IS NOT NULL) AS costed,
           count(*) FILTER (WHERE NOT plan AND reported IS NULL AND estimated IS NULL) AS uncosted,
           count(*) FILTER (WHERE plan) AS plans,
           jsonb_agg(DISTINCT unpriced_model) FILTER (WHERE unpriced_model IS NOT NULL AND sqlc.arg(detail)::boolean) AS unpriced_models
    FROM priced_facts
    GROUP BY run_id,
             CASE WHEN sqlc.arg(group_by)::text = 'model' THEN provider ELSE '' END,
             CASE WHEN sqlc.arg(group_by)::text = 'model' THEN model ELSE '' END
), entries AS (
    SELECT r.id, r.agent_id, r.execution_authorized_by_user_id, r.status, r.run_type,
           r.attempt, r.failure_reason, r.started_at, r.finished_at,
           r.agent_name, r.agent_archived_at, r.member_name,
           r.visible_runtime_id, r.visible_runtime_name,
           r.task_id IS NULL AS task_deleted,
           r.project_id IS NULL AS no_project,
           -- A Run in a Project the viewer does not read names nothing.
           r.project_id IS NOT NULL AND readable.project_id IS NULL AS hidden_work,
           readable.project_id, readable.project_name,
           CASE WHEN readable.project_id IS NOT NULL THEN r.task_id END AS task_id,
           CASE WHEN readable.project_id IS NOT NULL THEN r.task_key || ' · ' || r.task_title END AS task_label,
           f.provider, f.model, f.input, f.output, f.cache_read, f.cache_write, f.reasoning,
           f.reported, f.estimated, f.plan_tokens, f.costed, f.uncosted, f.plans, f.unpriced_models,
           f.run_id IS NOT NULL AS has_usage,
           CASE WHEN r.started_at IS NOT NULL
                THEN greatest(0, extract(epoch FROM (r.finished_at - r.started_at)) * 1000) END AS run_time,
           CASE WHEN r.claimed_at IS NOT NULL AND r.queued_at IS NOT NULL
                THEN greatest(0, extract(epoch FROM (r.claimed_at - r.queued_at)) * 1000) END AS queue_wait,
           CASE WHEN r.started_at IS NOT NULL AND r.claimed_at IS NOT NULL
                THEN greatest(0, extract(epoch FROM (r.started_at - r.claimed_at)) * 1000) END AS preparation
    FROM scoped_runs AS r
    LEFT JOIN readable_projects AS readable ON readable.project_id = r.project_id
    LEFT JOIN facts AS f ON f.run_id = r.id
), grouped AS (
    SELECT
        CASE sqlc.arg(group_by)::text
            WHEN 'bucket' THEN to_jsonb(extract(epoch FROM (
                date_trunc(sqlc.arg(bucket)::text, finished_at AT TIME ZONE sqlc.arg(time_zone)::text)
                AT TIME ZONE sqlc.arg(time_zone)::text))::bigint)
            WHEN 'heatmap' THEN jsonb_build_array(
                extract(isodow FROM finished_at AT TIME ZONE sqlc.arg(time_zone)::text)::int - 1,
                extract(hour FROM finished_at AT TIME ZONE sqlc.arg(time_zone)::text)::int)
            WHEN 'failure_reason' THEN to_jsonb(coalesce(NULLIF(failure_reason, ''), 'unclassified'))
            WHEN 'agent' THEN jsonb_build_object(
                'kind', 'agent',
                'id', CASE WHEN agent_archived_at IS NULL AND agent_name IS NOT NULL THEN agent_id END,
                'label', CASE WHEN agent_archived_at IS NULL AND agent_name IS NOT NULL THEN agent_name ELSE 'Deleted Agents' END,
                'synthetic', CASE WHEN agent_archived_at IS NOT NULL OR agent_name IS NULL THEN 'deleted_agents' END)
            WHEN 'model' THEN jsonb_build_object(
                'kind', 'model', 'id', NULL,
                'label', coalesce(provider || ' / ' || model, 'Unknown model'),
                'synthetic', CASE WHEN model IS NULL THEN 'unknown_model' END)
            WHEN 'member' THEN jsonb_build_object(
                'kind', 'member', 'id', execution_authorized_by_user_id,
                'label', CASE WHEN execution_authorized_by_user_id IS NULL THEN 'System'
                              ELSE coalesce(member_name, 'Deleted member') END,
                'synthetic', CASE WHEN execution_authorized_by_user_id IS NULL THEN 'system' END)
            WHEN 'project' THEN jsonb_build_object(
                'kind', 'project', 'id', project_id,
                'label', CASE WHEN hidden_work THEN 'Other work'
                              WHEN no_project THEN 'No Project' ELSE project_name END,
                'synthetic', CASE WHEN hidden_work THEN 'other_work' WHEN no_project THEN 'no_project' END)
            WHEN 'task' THEN jsonb_build_object(
                'kind', 'task', 'id', task_id,
                'label', CASE WHEN run_type = 'direct_chat' THEN 'Direct chats'
                              WHEN run_type = 'schedule' THEN 'Schedules'
                              WHEN run_type = 'channel_agent' THEN 'Channel chats'
                              WHEN hidden_work THEN 'Other work'
                              WHEN task_deleted THEN 'Deleted Tasks' ELSE task_label END,
                'synthetic', CASE WHEN run_type = 'direct_chat' THEN 'direct_chats'
                                  WHEN run_type = 'schedule' THEN 'schedules'
                                  WHEN run_type = 'channel_agent' THEN 'channel_chats'
                                  WHEN hidden_work THEN 'other_work'
                                  WHEN task_deleted THEN 'deleted_tasks' END)
            WHEN 'run_type' THEN jsonb_build_object('kind', 'run_type', 'id', NULL, 'label', run_type, 'synthetic', run_type)
            WHEN 'runtime' THEN jsonb_build_object(
                'kind', 'runtime', 'id', visible_runtime_id,
                'label', coalesce(visible_runtime_name, 'Other computers'),
                'synthetic', CASE WHEN visible_runtime_id IS NULL THEN 'other_computers' END)
            ELSE 'null'::jsonb
        END AS key,
        jsonb_build_object(
            'tokens', jsonb_build_object(
                'input', coalesce(sum(input), 0)::text, 'output', coalesce(sum(output), 0)::text,
                'cacheRead', coalesce(sum(cache_read), 0)::text, 'cacheWrite', coalesce(sum(cache_write), 0)::text,
                'reasoning', sum(reasoning)::text),
            'cost', jsonb_build_object(
                'reportedUsdTicks', sum(reported)::text, 'estimatedUsdTicks', sum(estimated)::text,
                'planCoveredTokens', coalesce(sum(plan_tokens), 0)::text,
                'coverageState', CASE WHEN coalesce(sum(costed), 0) + coalesce(sum(plans), 0) = 0 THEN 'missing'
                                      WHEN coalesce(sum(uncosted), 0) > 0 THEN 'partial' ELSE 'complete' END,
                'costedFactCount', coalesce(sum(costed), 0)::text,
                'uncostedFactCount', coalesce(sum(uncosted), 0)::text,
                'unpricedModels', coalesce((
                    SELECT jsonb_agg(DISTINCT unpriced.value)
                    FROM unnest(array_agg(unpriced_models) FILTER (WHERE sqlc.arg(detail)::boolean)) AS models(value)
                    CROSS JOIN LATERAL jsonb_array_elements(models.value) AS unpriced(value)), '[]'::jsonb)),
            'runs', jsonb_build_object(
                'total', count(*)::text,
                'completed', count(*) FILTER (WHERE status = 'completed')::text,
                'failed', count(*) FILTER (WHERE status = 'failed' AND coalesce(failure_reason, '') <> 'cancelled')::text,
                'cancelled', count(*) FILTER (WHERE status = 'cancelled' OR failure_reason = 'cancelled')::text,
                'task', count(*) FILTER (WHERE run_type = 'task')::text,
                'directChat', count(*) FILTER (WHERE run_type = 'direct_chat')::text,
                'channelAgent', count(*) FILTER (WHERE run_type = 'channel_agent')::text,
                'schedule', count(*) FILTER (WHERE run_type = 'schedule')::text,
                'withoutUsage', count(*) FILTER (WHERE NOT has_usage AND started_at IS NOT NULL)::text,
                'withoutTiming', count(*) FILTER (WHERE started_at IS NULL)::text,
                'retries', count(*) FILTER (WHERE attempt > 1)::text),
            'time', jsonb_build_object(
                'runTimeMs', round(sum(run_time))::text,
                'p50RunTimeMs', round((percentile_cont(0.5) WITHIN GROUP (ORDER BY run_time) FILTER (WHERE sqlc.arg(detail)::boolean))::numeric)::text,
                'p90RunTimeMs', round((percentile_cont(0.9) WITHIN GROUP (ORDER BY run_time) FILTER (WHERE sqlc.arg(detail)::boolean))::numeric)::text,
                'p50QueueWaitMs', round((percentile_cont(0.5) WITHIN GROUP (ORDER BY queue_wait) FILTER (WHERE sqlc.arg(detail)::boolean))::numeric)::text,
                'p90QueueWaitMs', round((percentile_cont(0.9) WITHIN GROUP (ORDER BY queue_wait) FILTER (WHERE sqlc.arg(detail)::boolean))::numeric)::text,
                'p50PreparationMs', round((percentile_cont(0.5) WITHIN GROUP (ORDER BY preparation) FILTER (WHERE sqlc.arg(detail)::boolean))::numeric)::text)
        ) AS metrics
    FROM entries
    GROUP BY 1
)
SELECT grouped.key::jsonb AS key, grouped.metrics::jsonb AS metrics
FROM grouped
ORDER BY CASE sqlc.arg(sort_by)::text
             WHEN 'tokens' THEN (metrics -> 'tokens' ->> 'input')::numeric + (metrics -> 'tokens' ->> 'output')::numeric
             WHEN 'cost' THEN coalesce((metrics -> 'cost' ->> 'reportedUsdTicks')::numeric, 0)
                            + coalesce((metrics -> 'cost' ->> 'estimatedUsdTicks')::numeric, 0)
             WHEN 'runs' THEN (metrics -> 'runs' ->> 'total')::numeric
             WHEN 'run_time' THEN (metrics -> 'time' ->> 'runTimeMs')::numeric
             WHEN 'failures' THEN (metrics -> 'runs' ->> 'failed')::numeric
         END DESC NULLS LAST,
         grouped.key::text
LIMIT sqlc.arg(row_limit)::integer OFFSET sqlc.arg(row_offset)::integer;

-- name: CountAnalyticsActiveRuns :one
-- The summary's "active now": a count of live Runs under the same filters
-- (no window). A project_id filter is checked readable first, so it reaches
-- the Project's Tasks through task_access for the viewer.
SELECT count(*)::text AS active
FROM runs AS r
WHERE r.company_id = sqlc.arg(company_id)::uuid
  AND r.status IN ('queued', 'deferred', 'dispatched', 'waiting_local_directory', 'running')
  AND (sqlc.narg(authorizer_user_id)::uuid IS NULL
       OR r.execution_authorized_by_user_id = sqlc.narg(authorizer_user_id)::uuid)
  AND (sqlc.narg(agent_id)::uuid IS NULL OR r.agent_id = sqlc.narg(agent_id)::uuid)
  AND (sqlc.narg(project_id)::uuid IS NULL OR r.task_id IN (
      SELECT access.task_id FROM task_access AS access
      WHERE access.company_id = sqlc.arg(company_id)::uuid
        AND access.project_id = sqlc.narg(project_id)::uuid
        AND access.user_id = sqlc.arg(viewer_user_id)::uuid))
  AND (sqlc.narg(task_id)::uuid IS NULL OR r.task_id = sqlc.narg(task_id)::uuid)
  AND (sqlc.arg(run_type)::text = '' OR r.run_type = sqlc.arg(run_type)::text)
  AND (sqlc.narg(runtime_id)::uuid IS NULL OR (r.runtime_id = sqlc.narg(runtime_id)::uuid AND EXISTS (
      SELECT 1 FROM runtimes AS visible_runtime
      WHERE visible_runtime.id = sqlc.narg(runtime_id)::uuid
        AND visible_runtime.company_id = r.company_id
        AND visible_runtime.owner_user_id = sqlc.arg(viewer_user_id)::uuid)))
  -- A live Run has no usage facts yet: the provider comes from its
  -- runtime and the model from the model frozen into its context; a default
  -- the daemon resolves later stays unknown.
  AND (sqlc.arg(provider)::text = '' OR EXISTS (
      SELECT 1 FROM runtimes AS live_runtime
      WHERE live_runtime.id = r.runtime_id
        AND live_runtime.company_id = r.company_id
        AND live_runtime.provider = sqlc.arg(provider)::text))
  AND (sqlc.arg(model)::text = '' OR r.context #>> '{agent,model}' = sqlc.arg(model)::text);

-- name: GetTaskUsageTaskForReader :one
-- The Task usage read opens only a Task whose Project the caller reads (ADR
-- 0017): the management bridge never opens a Task. role decides whether the
-- caller sees every Run's tokens and cost (usage.read) or only their own.
SELECT member.role, task.created_at, task.completed_at
FROM tasks AS task
JOIN companies AS company ON company.id = task.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = task.company_id
 AND member.user_id = sqlc.arg(user_id)::uuid
 AND member.status = 'active'
JOIN project_access AS access
  ON access.company_id = task.company_id
 AND access.project_id = task.project_id
 AND access.user_id = member.user_id
WHERE task.company_id = sqlc.arg(company_id)::uuid
  AND task.id = sqlc.arg(task_id)::uuid;

-- name: ListTaskUsageRuns :many
-- One Task's Runs with their timing, tokens and cost, for a caller who reads
-- the Task's Project (ADR 0017): the task_access lookup names the Task and the
-- caller, so a non-reader gets no rows from this statement on its own.
SELECT r.id, r.status, coalesce(a.name, 'Deleted Agent')::text AS agent_name,
       coalesce(min(u.model), '')::text AS model,
       r.queued_at, r.claimed_at, r.started_at, r.finished_at,
       coalesce(sum(u.input_tokens::numeric + u.output_tokens)::text, '')::text AS tokens,
       coalesce(sum(CASE
           WHEN u.billing_mode <> 'subscription' AND u.provider_cost_usd IS NOT NULL
               THEN round(u.provider_cost_usd * 10000000000)
           WHEN u.billing_mode = 'api'
               THEN round((u.input_tokens * price.input_usd_per_mtok + u.output_tokens * price.output_usd_per_mtok +
                           u.cache_read_tokens * price.cache_read_usd_per_mtok +
                           u.cache_write_tokens * price.cache_write_usd_per_mtok) * 10000)
       END)::text, '')::text AS cost_usd_ticks,
       count(u.run_id)::text AS fact_count,
       r.execution_authorized_by_user_id
FROM runs AS r
LEFT JOIN agents AS a ON a.id = r.agent_id AND a.company_id = r.company_id
LEFT JOIN run_usage AS u ON u.run_id = r.id
LEFT JOIN LATERAL (
    SELECT input_usd_per_mtok, output_usd_per_mtok, cache_read_usd_per_mtok, cache_write_usd_per_mtok
    FROM model_prices
    WHERE provider = u.provider AND model = lower(u.model) AND effective_from <= coalesce(r.finished_at, now())
    ORDER BY effective_from DESC LIMIT 1
) AS price ON true
WHERE r.company_id = sqlc.arg(company_id)::uuid
  AND r.task_id = sqlc.arg(task_id)::uuid
  AND EXISTS (
      SELECT 1 FROM task_access AS access
      WHERE access.company_id = sqlc.arg(company_id)::uuid
        AND access.task_id = sqlc.arg(task_id)::uuid
        AND access.user_id = sqlc.arg(user_id)::uuid)
GROUP BY r.id, r.status, a.name, r.queued_at, r.claimed_at, r.started_at, r.finished_at,
         r.execution_authorized_by_user_id
ORDER BY coalesce(r.finished_at, r.created_at) DESC, r.id DESC;
