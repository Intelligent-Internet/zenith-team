-- name: GetCompanyBootstrapCounts :one
-- Company-wide aggregates for the opening snapshot. The run counters use the
-- one counting vocabulary: queued covers queued+deferred (scheduled work),
-- active covers dispatched+waiting_local_directory+running (work in flight).
SELECT
    count(DISTINCT tasks.id) FILTER (
        WHERE tasks.status NOT IN ('done', 'cancelled')
    )::bigint AS active_task_count,
    (
        SELECT count(*)::bigint
        FROM runs
        WHERE runs.company_id = companies.id
          AND runs.status IN ('queued', 'deferred')
    ) AS queued_run_count,
    (
        SELECT count(*)::bigint
        FROM runs
        WHERE runs.company_id = companies.id
          AND runs.status IN ('dispatched', 'waiting_local_directory', 'running')
    ) AS active_run_count
FROM companies
LEFT JOIN tasks ON tasks.company_id = companies.id
WHERE companies.id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL
GROUP BY companies.id;
