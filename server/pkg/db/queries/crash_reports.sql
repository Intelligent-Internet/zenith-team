-- name: InsertCrashReport :exec
INSERT INTO crash_reports (
    kind,
    platform,
    architecture,
    app_version,
    reason
)
VALUES (
    sqlc.arg(kind)::text,
    sqlc.arg(platform)::text,
    sqlc.arg(architecture)::text,
    sqlc.arg(app_version)::text,
    sqlc.arg(reason)::text
);

-- name: PurgeExpiredCrashReports :many
WITH purge_input AS MATERIALIZED (
    SELECT
        sqlc.arg(retention_milliseconds)::bigint AS retention_milliseconds,
        sqlc.arg(batch_size)::integer AS batch_size
    WHERE sqlc.arg(retention_milliseconds)::bigint BETWEEN 1 AND 31536000000
      AND sqlc.arg(batch_size)::integer BETWEEN 1 AND 1000
),
statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
),
candidates AS (
    SELECT
        crash_reports.id,
        crash_reports.created_at
    FROM crash_reports
    CROSS JOIN purge_input
    CROSS JOIN statement_clock
    WHERE crash_reports.created_at <= statement_clock.now_at
        - purge_input.retention_milliseconds * INTERVAL '1 millisecond'
    ORDER BY crash_reports.created_at, crash_reports.id
    FOR UPDATE OF crash_reports SKIP LOCKED
    LIMIT COALESCE((SELECT batch_size FROM purge_input), 0)
)
DELETE FROM crash_reports
USING candidates
WHERE crash_reports.id = candidates.id
  AND crash_reports.created_at = candidates.created_at
RETURNING crash_reports.id;
