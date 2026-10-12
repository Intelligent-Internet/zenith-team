-- Durable Proactive Mode Task merge jobs. A job is tied to one completed Run
-- and one Task execution generation; authority is exact and fails closed.

-- name: InsertProactiveTaskMergeJobForRun :one
INSERT INTO task_merge_jobs (
    company_id, task_id, run_id, task_execution_generation, authority_user_id
)
SELECT
    runs.company_id,
    tasks.id,
    runs.id,
    tasks.execution_generation,
    automation_member.user_id
FROM runs
JOIN tasks
  ON tasks.id = runs.task_id
 AND tasks.company_id = runs.company_id
 AND tasks.conversation_id = runs.conversation_id
 AND tasks.execution_generation = runs.task_execution_generation
JOIN companies
  ON companies.id = runs.company_id
 AND companies.deleting_at IS NULL
 AND companies.proactive_mode_enabled
JOIN company_members AS automation_member
  ON automation_member.company_id = companies.id
 AND automation_member.user_id = companies.proactive_mode_enabled_by_user_id
 AND automation_member.role = ANY(sqlc.arg(actor_roles)::text[])
 AND automation_member.status = 'active'
JOIN conversations AS root_conversation
  ON root_conversation.id = companies.proactive_mode_conversation_id
 AND root_conversation.company_id = companies.id
 AND root_conversation.kind = 'direct'
 AND root_conversation.created_by_user_id = automation_member.user_id
 AND root_conversation.archived_at IS NULL
JOIN agents AS root_agent
  ON root_agent.id = root_conversation.agent_id
 AND root_agent.company_id = companies.id
 AND root_agent.reports_to_agent_id IS NULL
 AND root_agent.archived_at IS NULL
WHERE runs.id = sqlc.arg(run_id)::uuid
  AND runs.status = 'completed'
  AND runs.task_id IS NOT NULL
  AND runs.task_execution_generation IS NOT NULL
  AND tasks.status = 'in_review'
  AND tasks.executed_by_agent_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1
      FROM approvals
      WHERE approvals.company_id = runs.company_id
        AND approvals.origin_run_id = runs.id
        AND approvals.task_id = tasks.id
        AND (
            approvals.status = 'pending'
            OR approvals.continuation_status = 'pending'
        )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM questions
      WHERE questions.company_id = runs.company_id
        AND questions.origin_run_id = runs.id
        AND questions.task_id = tasks.id
        AND (
            questions.status = 'pending'
            OR questions.continuation_status = 'pending'
        )
  )
ON CONFLICT DO NOTHING
RETURNING task_merge_jobs.*;

-- name: RefreshNextPendingTaskMergeJobAuthority :one
WITH candidate AS MATERIALIZED (
    SELECT
        task_merge_jobs.id,
        companies.proactive_mode_enabled_by_user_id AS authority_user_id
    FROM task_merge_jobs
    JOIN companies ON companies.id = task_merge_jobs.company_id
    JOIN company_members AS automation_member
      ON automation_member.company_id = companies.id
     AND automation_member.user_id = companies.proactive_mode_enabled_by_user_id
     AND automation_member.role = ANY(sqlc.arg(actor_roles)::text[])
     AND automation_member.status = 'active'
    JOIN conversations AS root_conversation
      ON root_conversation.id = companies.proactive_mode_conversation_id
     AND root_conversation.company_id = companies.id
     AND root_conversation.kind = 'direct'
     AND root_conversation.created_by_user_id = automation_member.user_id
     AND root_conversation.archived_at IS NULL
    JOIN agents AS root_agent
      ON root_agent.id = root_conversation.agent_id
     AND root_agent.company_id = companies.id
     AND root_agent.reports_to_agent_id IS NULL
     AND root_agent.archived_at IS NULL
    WHERE task_merge_jobs.status = 'pending'
      AND task_merge_jobs.request_type = 'proactive'
      AND companies.deleting_at IS NULL
      AND companies.proactive_mode_enabled
      AND task_merge_jobs.authority_user_id IS DISTINCT FROM automation_member.user_id
    ORDER BY task_merge_jobs.created_at, task_merge_jobs.id
    LIMIT 1
)
UPDATE task_merge_jobs
SET authority_user_id = candidate.authority_user_id,
    updated_at = clock_timestamp()
FROM candidate
WHERE task_merge_jobs.id = candidate.id
RETURNING task_merge_jobs.*;

-- name: InsertNextProactiveTaskMergeJob :one
INSERT INTO task_merge_jobs (
    company_id, task_id, run_id, task_execution_generation, authority_user_id
)
SELECT
    runs.company_id,
    tasks.id,
    runs.id,
    tasks.execution_generation,
    automation_member.user_id
FROM tasks
JOIN companies
  ON companies.id = tasks.company_id
 AND companies.deleting_at IS NULL
 AND companies.proactive_mode_enabled
JOIN company_members AS automation_member
  ON automation_member.company_id = companies.id
 AND automation_member.user_id = companies.proactive_mode_enabled_by_user_id
 AND automation_member.role = ANY(sqlc.arg(actor_roles)::text[])
 AND automation_member.status = 'active'
JOIN conversations AS root_conversation
  ON root_conversation.id = companies.proactive_mode_conversation_id
 AND root_conversation.company_id = companies.id
 AND root_conversation.kind = 'direct'
 AND root_conversation.created_by_user_id = automation_member.user_id
 AND root_conversation.archived_at IS NULL
JOIN agents AS root_agent
  ON root_agent.id = root_conversation.agent_id
 AND root_agent.company_id = companies.id
 AND root_agent.reports_to_agent_id IS NULL
 AND root_agent.archived_at IS NULL
JOIN runs
  ON runs.company_id = tasks.company_id
 AND runs.task_id = tasks.id
 AND runs.task_execution_generation = tasks.execution_generation
 AND runs.status = 'completed'
WHERE tasks.status = 'in_review'
  AND tasks.executed_by_agent_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1
      FROM runs AS newer
      WHERE newer.company_id = runs.company_id
        AND newer.task_id = runs.task_id
        AND newer.task_execution_generation = runs.task_execution_generation
        AND newer.status = 'completed'
        AND (newer.updated_at, newer.id) > (runs.updated_at, runs.id)
  )
  AND NOT EXISTS (
      SELECT 1
      FROM approvals
      WHERE approvals.company_id = runs.company_id
        AND approvals.origin_run_id = runs.id
        AND approvals.task_id = tasks.id
        AND (
            approvals.status = 'pending'
            OR approvals.continuation_status = 'pending'
        )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM questions
      WHERE questions.company_id = runs.company_id
        AND questions.origin_run_id = runs.id
        AND questions.task_id = tasks.id
        AND (
            questions.status = 'pending'
            OR questions.continuation_status = 'pending'
        )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM task_merge_jobs
      WHERE task_merge_jobs.task_id = tasks.id
        AND task_merge_jobs.task_execution_generation = tasks.execution_generation
  )
ORDER BY runs.updated_at, runs.id
LIMIT 1
ON CONFLICT DO NOTHING
RETURNING task_merge_jobs.*;

-- name: GetTaskMergeJob :one
SELECT *
FROM task_merge_jobs
WHERE id = sqlc.arg(job_id)::uuid;

-- The first claim read deliberately does not mutate or lock. The service uses
-- the identity to acquire the Company -> acting membership -> Task lifecycle
-- fence, then ClaimTaskMergeJob repeats every authority and generation check
-- in a fresh statement after those locks are held.
-- name: GetNextTaskMergeJobClaimCandidate :one
SELECT
    task_merge_jobs.id AS job_id,
    task_merge_jobs.company_id,
    task_merge_jobs.task_id
FROM task_merge_jobs
JOIN runs
  ON runs.id = task_merge_jobs.run_id
 AND runs.company_id = task_merge_jobs.company_id
 AND runs.task_id = task_merge_jobs.task_id
 AND runs.task_execution_generation = task_merge_jobs.task_execution_generation
JOIN runtimes
  ON runtimes.id = runs.runtime_id
 AND runtimes.company_id = runs.company_id
JOIN company_members AS daemon_member
  ON daemon_member.company_id = runs.company_id
 AND daemon_member.user_id = sqlc.arg(actor_user_id)::uuid
 AND daemon_member.status = 'active'
JOIN tasks
  ON tasks.id = task_merge_jobs.task_id
 AND tasks.company_id = task_merge_jobs.company_id
 AND tasks.execution_generation = task_merge_jobs.task_execution_generation
 AND tasks.status = 'in_review'
JOIN companies
  ON companies.id = task_merge_jobs.company_id
 AND companies.deleting_at IS NULL
LEFT JOIN company_members AS automation_member
  ON automation_member.company_id = companies.id
 AND automation_member.user_id = task_merge_jobs.authority_user_id
 AND automation_member.role = ANY(sqlc.arg(actor_roles)::text[])
 AND automation_member.status = 'active'
LEFT JOIN conversations AS root_conversation
  ON root_conversation.id = companies.proactive_mode_conversation_id
 AND root_conversation.company_id = companies.id
 AND root_conversation.kind = 'direct'
 AND root_conversation.created_by_user_id = automation_member.user_id
 AND root_conversation.archived_at IS NULL
LEFT JOIN agents AS root_agent
  ON root_agent.id = root_conversation.agent_id
 AND root_agent.company_id = companies.id
 AND root_agent.reports_to_agent_id IS NULL
 AND root_agent.archived_at IS NULL
WHERE runtimes.id = sqlc.arg(runtime_id)::uuid
  AND runtimes.daemon_id = sqlc.arg(daemon_id)::text
  AND runtimes.status = 'online'
  AND runs.status = 'completed'
  AND NOT EXISTS (
      SELECT 1
      FROM approvals
      WHERE approvals.company_id = runs.company_id
        AND approvals.origin_run_id = runs.id
        AND approvals.task_id = tasks.id
        AND (
            approvals.status = 'pending'
            OR approvals.continuation_status = 'pending'
        )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM questions
      WHERE questions.company_id = runs.company_id
        AND questions.origin_run_id = runs.id
        AND questions.task_id = tasks.id
        AND (
            questions.status = 'pending'
            OR questions.continuation_status = 'pending'
        )
  )
  AND (
      (
          task_merge_jobs.status = 'pending'
          AND task_merge_jobs.request_type = 'proactive'
          AND companies.proactive_mode_enabled
          AND companies.proactive_mode_enabled_by_user_id = task_merge_jobs.authority_user_id
          AND automation_member.user_id IS NOT NULL
          AND root_conversation.id IS NOT NULL
          AND root_agent.id IS NOT NULL
      )
      OR (
          task_merge_jobs.status = 'executing'
          AND task_merge_jobs.lease_expires_at <= clock_timestamp()
      )
  )
  AND task_merge_jobs.next_attempt_at <= clock_timestamp()
  -- ADR 0017: the daemon's member merges a Task's work into its Project
  -- only while they may run Tasks there; a job whose member lost
  -- project.tasks.execute is never claimed.
  AND project_permission_held(
      tasks.company_id, tasks.project_id, daemon_member.user_id,
      sqlc.arg(execute_permission)::text,
      sqlc.arg(company_default_permissions)::text[],
      sqlc.arg(management_permissions)::text[],
      sqlc.arg(management_roles)::text[]
  )
ORDER BY task_merge_jobs.next_attempt_at, task_merge_jobs.created_at, task_merge_jobs.id
LIMIT 1;

-- name: ClaimTaskMergeJob :one
WITH candidate AS MATERIALIZED (
    SELECT task_merge_jobs.id
    FROM task_merge_jobs
    JOIN runs
      ON runs.id = task_merge_jobs.run_id
     AND runs.company_id = task_merge_jobs.company_id
     AND runs.task_id = task_merge_jobs.task_id
     AND runs.task_execution_generation = task_merge_jobs.task_execution_generation
    JOIN runtimes
      ON runtimes.id = runs.runtime_id
     AND runtimes.company_id = runs.company_id
    JOIN company_members AS daemon_member
      ON daemon_member.company_id = runs.company_id
     AND daemon_member.user_id = sqlc.arg(actor_user_id)::uuid
     AND daemon_member.status = 'active'
    JOIN tasks
      ON tasks.id = task_merge_jobs.task_id
     AND tasks.company_id = task_merge_jobs.company_id
     AND tasks.execution_generation = task_merge_jobs.task_execution_generation
     AND tasks.status = 'in_review'
    JOIN companies
      ON companies.id = task_merge_jobs.company_id
     AND companies.deleting_at IS NULL
    LEFT JOIN company_members AS automation_member
      ON automation_member.company_id = companies.id
     AND automation_member.user_id = task_merge_jobs.authority_user_id
     AND automation_member.role = ANY(sqlc.arg(actor_roles)::text[])
     AND automation_member.status = 'active'
    LEFT JOIN conversations AS root_conversation
      ON root_conversation.id = companies.proactive_mode_conversation_id
     AND root_conversation.company_id = companies.id
     AND root_conversation.kind = 'direct'
     AND root_conversation.created_by_user_id = automation_member.user_id
     AND root_conversation.archived_at IS NULL
    LEFT JOIN agents AS root_agent
      ON root_agent.id = root_conversation.agent_id
     AND root_agent.company_id = companies.id
     AND root_agent.reports_to_agent_id IS NULL
     AND root_agent.archived_at IS NULL
    WHERE task_merge_jobs.id = sqlc.arg(job_id)::uuid
      AND runtimes.id = sqlc.arg(runtime_id)::uuid
      AND runtimes.daemon_id = sqlc.arg(daemon_id)::text
      AND runtimes.status = 'online'
      AND runs.status = 'completed'
      AND NOT EXISTS (
          SELECT 1
          FROM approvals
          WHERE approvals.company_id = runs.company_id
            AND approvals.origin_run_id = runs.id
            AND approvals.task_id = tasks.id
            AND (
                approvals.status = 'pending'
                OR approvals.continuation_status = 'pending'
            )
      )
      AND NOT EXISTS (
          SELECT 1
          FROM questions
          WHERE questions.company_id = runs.company_id
            AND questions.origin_run_id = runs.id
            AND questions.task_id = tasks.id
            AND (
                questions.status = 'pending'
                OR questions.continuation_status = 'pending'
            )
      )
      AND (
          (
              task_merge_jobs.status = 'pending'
              AND task_merge_jobs.request_type = 'proactive'
              AND companies.proactive_mode_enabled
              AND companies.proactive_mode_enabled_by_user_id = task_merge_jobs.authority_user_id
              AND automation_member.user_id IS NOT NULL
              AND root_conversation.id IS NOT NULL
              AND root_agent.id IS NOT NULL
          )
          OR (
              task_merge_jobs.status = 'executing'
              AND task_merge_jobs.lease_expires_at <= clock_timestamp()
          )
      )
      AND task_merge_jobs.next_attempt_at <= clock_timestamp()
      -- ADR 0017: the daemon's member merges a Task's work into its Project
      -- only while they may run Tasks there; a job whose member lost
      -- project.tasks.execute is never claimed.
      AND project_permission_held(
          tasks.company_id, tasks.project_id, daemon_member.user_id,
          sqlc.arg(execute_permission)::text,
          sqlc.arg(company_default_permissions)::text[],
          sqlc.arg(management_permissions)::text[],
          sqlc.arg(management_roles)::text[]
      )
    ORDER BY task_merge_jobs.next_attempt_at, task_merge_jobs.created_at, task_merge_jobs.id
    LIMIT 1
    FOR UPDATE OF task_merge_jobs SKIP LOCKED
), claimed AS (
    UPDATE task_merge_jobs
    SET status = 'executing',
        attempt_count = task_merge_jobs.attempt_count + 1,
        lease_expires_at = clock_timestamp() + INTERVAL '5 minutes',
        updated_at = clock_timestamp()
    FROM candidate
    WHERE task_merge_jobs.id = candidate.id
    RETURNING task_merge_jobs.*
)
SELECT
    claimed.id AS job_id,
    claimed.company_id,
    claimed.task_id,
    claimed.run_id,
    claimed.task_execution_generation,
    claimed.attempt_count AS attempt,
    runs.conversation_id,
    runs.context
FROM claimed
JOIN runs
  ON runs.company_id = claimed.company_id
 AND runs.id = claimed.run_id;

-- An explicit desktop acceptance immediately converts the exact reviewed Run
-- into an executing durable job. The service holds the Company -> acting
-- membership -> Task locks before this statement, and every predicate is
-- repeated here so caller-supplied ids can never become merge authority.
-- A proactive job that has not executed is safely repurposed; a live attempt
-- remains the sole authority until it terminalizes or its lease expires.
-- name: ClaimManualTaskMergeJob :one
WITH candidate AS MATERIALIZED (
    SELECT
        runs.company_id,
        runs.task_id,
        runs.id AS run_id,
        runs.task_execution_generation,
        runs.conversation_id,
        runs.context
    FROM runs
    JOIN tasks
      ON tasks.id = runs.task_id
     AND tasks.company_id = runs.company_id
     AND tasks.conversation_id = runs.conversation_id
     AND tasks.execution_generation = runs.task_execution_generation
    JOIN runtimes
      ON runtimes.id = runs.runtime_id
     AND runtimes.company_id = runs.company_id
    JOIN companies
      ON companies.id = runs.company_id
     AND companies.deleting_at IS NULL
    JOIN company_members AS daemon_member
      ON daemon_member.company_id = runs.company_id
     AND daemon_member.user_id = sqlc.arg(actor_user_id)::uuid
     AND daemon_member.status = 'active'
    WHERE runs.company_id = sqlc.arg(company_id)::uuid
      AND runs.task_id = sqlc.arg(task_id)::uuid
      AND runs.id = sqlc.arg(run_id)::uuid
      AND runs.runtime_id = sqlc.arg(runtime_id)::uuid
      AND runtimes.daemon_id = sqlc.arg(daemon_id)::text
      AND runtimes.status = 'online'
      AND runs.status = 'completed'
      AND runs.task_execution_generation = sqlc.arg(expected_execution_generation)::bigint
      AND tasks.status = 'in_review'
      AND tasks.version = sqlc.arg(expected_revision)::bigint
      AND NOT EXISTS (
          SELECT 1
          FROM runs AS newer
          WHERE newer.company_id = runs.company_id
            AND newer.task_id = runs.task_id
            AND newer.task_execution_generation = runs.task_execution_generation
            AND newer.status = 'completed'
            AND (newer.updated_at, newer.id) > (runs.updated_at, runs.id)
      )
      -- ADR 0017: the daemon's member merges a Task's work into its Project
      -- only while they may run Tasks there; a job whose member lost
      -- project.tasks.execute is never claimed.
      AND project_permission_held(
          tasks.company_id, tasks.project_id, daemon_member.user_id,
          sqlc.arg(execute_permission)::text,
          sqlc.arg(company_default_permissions)::text[],
          sqlc.arg(management_permissions)::text[],
          sqlc.arg(management_roles)::text[]
      )
), claimed AS (
    INSERT INTO task_merge_jobs (
        company_id,
        task_id,
        run_id,
        task_execution_generation,
        authority_user_id,
        request_type,
        status,
        attempt_count,
        lease_expires_at
    )
    SELECT
        candidate.company_id,
        candidate.task_id,
        candidate.run_id,
        candidate.task_execution_generation,
        sqlc.arg(actor_user_id)::uuid,
        'manual',
        'executing',
        1,
        clock_timestamp() + INTERVAL '5 minutes'
    FROM candidate
    ON CONFLICT (task_id, task_execution_generation) DO UPDATE
    SET run_id = EXCLUDED.run_id,
        authority_user_id = EXCLUDED.authority_user_id,
        request_type = 'manual',
        status = 'executing',
        attempt_count = CASE
            WHEN task_merge_jobs.request_type = 'manual'
             AND task_merge_jobs.status = 'executing'
             AND task_merge_jobs.lease_expires_at > clock_timestamp()
             AND task_merge_jobs.run_id = EXCLUDED.run_id
             AND task_merge_jobs.authority_user_id = EXCLUDED.authority_user_id
                THEN task_merge_jobs.attempt_count
            ELSE task_merge_jobs.attempt_count + 1
        END,
        next_attempt_at = CASE
            WHEN task_merge_jobs.request_type = 'manual'
             AND task_merge_jobs.status = 'executing'
             AND task_merge_jobs.lease_expires_at > clock_timestamp()
             AND task_merge_jobs.run_id = EXCLUDED.run_id
             AND task_merge_jobs.authority_user_id = EXCLUDED.authority_user_id
                THEN task_merge_jobs.next_attempt_at
            ELSE clock_timestamp()
        END,
        lease_expires_at = CASE
            WHEN task_merge_jobs.request_type = 'manual'
             AND task_merge_jobs.status = 'executing'
             AND task_merge_jobs.lease_expires_at > clock_timestamp()
             AND task_merge_jobs.run_id = EXCLUDED.run_id
             AND task_merge_jobs.authority_user_id = EXCLUDED.authority_user_id
                THEN task_merge_jobs.lease_expires_at
            ELSE clock_timestamp() + INTERVAL '5 minutes'
        END,
        result_code = NULL,
        merged_commit = NULL,
        updated_at = CASE
            WHEN task_merge_jobs.request_type = 'manual'
             AND task_merge_jobs.status = 'executing'
             AND task_merge_jobs.lease_expires_at > clock_timestamp()
             AND task_merge_jobs.run_id = EXCLUDED.run_id
             AND task_merge_jobs.authority_user_id = EXCLUDED.authority_user_id
                THEN task_merge_jobs.updated_at
            ELSE clock_timestamp()
        END,
        completed_at = NULL
    WHERE (
           task_merge_jobs.request_type = 'manual'
           AND task_merge_jobs.status = 'executing'
           AND task_merge_jobs.lease_expires_at > clock_timestamp()
           AND task_merge_jobs.run_id = EXCLUDED.run_id
           AND task_merge_jobs.authority_user_id = EXCLUDED.authority_user_id
       )
       OR task_merge_jobs.status IN ('pending', 'blocked')
       OR (
           task_merge_jobs.status = 'executing'
           AND task_merge_jobs.lease_expires_at <= clock_timestamp()
       )
    RETURNING task_merge_jobs.*
)
SELECT
    claimed.id AS job_id,
    claimed.company_id,
    claimed.task_id,
    claimed.run_id,
    claimed.task_execution_generation,
    claimed.attempt_count AS attempt,
    candidate.conversation_id,
    candidate.context
FROM claimed
JOIN candidate
  ON candidate.company_id = claimed.company_id
 AND candidate.task_id = claimed.task_id
 AND candidate.run_id = claimed.run_id;

-- Result identity is resolved without a lock. RecordResult then acquires the
-- Company -> acting membership -> Task lifecycle fence before it locks and
-- rechecks this exact job through LockTaskMergeJobForResult.
-- name: GetTaskMergeJobResultCandidate :one
SELECT
    task_merge_jobs.company_id,
    task_merge_jobs.task_id
FROM task_merge_jobs
JOIN runs
  ON runs.id = task_merge_jobs.run_id
 AND runs.company_id = task_merge_jobs.company_id
 AND runs.task_id = task_merge_jobs.task_id
 AND runs.task_execution_generation = task_merge_jobs.task_execution_generation
JOIN runtimes
  ON runtimes.id = runs.runtime_id
 AND runtimes.company_id = runs.company_id
WHERE task_merge_jobs.id = sqlc.arg(job_id)::uuid
  AND runtimes.id = sqlc.arg(runtime_id)::uuid
  AND runtimes.daemon_id = sqlc.arg(daemon_id)::text;

-- name: LockTaskMergeJobForResult :one
SELECT
    sqlc.embed(task_merge_jobs),
    tasks.conversation_id,
    CAST(
        COALESCE(task_merge_jobs.lease_expires_at > clock_timestamp(), false)
        AS boolean
    ) AS lease_active
FROM task_merge_jobs
JOIN runs
  ON runs.id = task_merge_jobs.run_id
 AND runs.company_id = task_merge_jobs.company_id
 AND runs.task_id = task_merge_jobs.task_id
 AND runs.task_execution_generation = task_merge_jobs.task_execution_generation
JOIN runtimes
  ON runtimes.id = runs.runtime_id
 AND runtimes.company_id = runs.company_id
JOIN company_members AS daemon_member
  ON daemon_member.company_id = runs.company_id
 AND daemon_member.user_id = sqlc.arg(actor_user_id)::uuid
 AND daemon_member.status = 'active'
JOIN tasks
  ON tasks.id = task_merge_jobs.task_id
 AND tasks.company_id = task_merge_jobs.company_id
WHERE task_merge_jobs.id = sqlc.arg(job_id)::uuid
  AND runtimes.id = sqlc.arg(runtime_id)::uuid
  AND runtimes.daemon_id = sqlc.arg(daemon_id)::text
  AND task_merge_jobs.attempt_count = sqlc.arg(attempt)::integer
FOR UPDATE OF task_merge_jobs;

-- name: RenewTaskMergeJobLease :one
UPDATE task_merge_jobs
SET lease_expires_at = clock_timestamp() + INTERVAL '5 minutes',
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(job_id)::uuid
  AND status = 'executing'
  AND attempt_count = sqlc.arg(attempt)::integer
  AND lease_expires_at > clock_timestamp()
RETURNING lease_expires_at;

-- name: CompleteTaskMergeJob :one
UPDATE task_merge_jobs
SET status = sqlc.arg(status)::text,
    result_code = sqlc.narg(result_code)::text,
    merged_commit = sqlc.narg(merged_commit)::text,
    lease_expires_at = NULL,
    completed_at = clock_timestamp(),
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(job_id)::uuid
  AND status = 'executing'
  AND attempt_count = sqlc.arg(attempt)::integer
  AND lease_expires_at > clock_timestamp()
RETURNING task_merge_jobs.*;

-- A pending job has no daemon-side effect and may be superseded safely. Once
-- executing, however, the daemon may already be mutating Git: fresh work must
-- remain fenced until a claimant or recovery attempt records a terminal result.
-- name: HasExecutingTaskMergeJob :one
SELECT EXISTS (
    SELECT 1
    FROM task_merge_jobs
    WHERE task_id = sqlc.arg(task_id)::uuid
      AND status = 'executing'
);

-- name: SupersedePendingTaskMergeJobsForTask :execrows
UPDATE task_merge_jobs
SET status = 'blocked',
    result_code = 'superseded',
    merged_commit = NULL,
    lease_expires_at = NULL,
    completed_at = clock_timestamp(),
    updated_at = clock_timestamp()
WHERE task_id = sqlc.arg(task_id)::uuid
  AND status = 'pending';
