-- name: InsertTaskReportEvent :one
WITH RECURSIVE reporting_chain AS (
    SELECT agents.id, agents.company_id, agents.reports_to_agent_id, 0 AS depth
    FROM agents
    WHERE agents.id = sqlc.arg(source_agent_id)::uuid
      AND agents.company_id = sqlc.arg(company_id)::uuid
      AND agents.archived_at IS NULL

    UNION ALL

    SELECT manager.id, manager.company_id, manager.reports_to_agent_id, reporting_chain.depth + 1
    FROM agents AS manager
    JOIN reporting_chain ON reporting_chain.reports_to_agent_id = manager.id
    WHERE manager.company_id = sqlc.arg(company_id)::uuid
      AND manager.archived_at IS NULL
), recipient_agent AS (
    SELECT reporting_chain.id
    FROM reporting_chain
    WHERE reporting_chain.reports_to_agent_id IS NULL
      AND reporting_chain.depth > 0
    ORDER BY reporting_chain.depth DESC
    LIMIT 1
)
INSERT INTO task_report_events (
	company_id, task_id, task_version, task_key, task_title,
	source_agent_id, recipient_agent_id,
	completion_source, completed_at, result_summary, source_run_id, request_key,
	project_id
)
SELECT
    sqlc.arg(company_id)::uuid,
    sqlc.arg(task_id)::uuid,
    sqlc.arg(task_version)::bigint,
    sqlc.arg(task_key)::text,
    sqlc.arg(task_title)::text,
    source_agent.id,
    recipient_agent.id,
	sqlc.arg(completion_source)::text,
	sqlc.arg(completed_at)::timestamptz,
	COALESCE(NULLIF(sqlc.arg(result_summary)::text, ''), latest_output.content, '')::text,
	sqlc.narg(source_run_id)::uuid,
	sqlc.narg(request_key)::uuid,
	task.project_id
FROM agents AS source_agent
JOIN recipient_agent ON true
JOIN tasks AS task
  ON task.company_id = sqlc.arg(company_id)::uuid
 AND task.id = sqlc.arg(task_id)::uuid
LEFT JOIN LATERAL (
    SELECT conversation_items.content
    FROM runs
    JOIN conversation_items
      ON conversation_items.run_id = runs.id
     AND conversation_items.type = 'agent_text'
    WHERE runs.company_id = sqlc.arg(company_id)::uuid
      AND runs.task_id = sqlc.arg(task_id)::uuid
      AND runs.agent_id = source_agent.id
      AND runs.status = 'completed'
    ORDER BY runs.updated_at DESC, runs.id DESC, conversation_items.seq DESC
    LIMIT 1
) AS latest_output ON true
WHERE source_agent.id = sqlc.arg(source_agent_id)::uuid
  AND source_agent.company_id = sqlc.arg(company_id)::uuid
  AND source_agent.archived_at IS NULL
ON CONFLICT (task_id, task_version) DO NOTHING
RETURNING task_report_events.*;

-- name: GetTaskFailureReplayForUpdate :one
SELECT task_report_events.*
FROM task_report_events
WHERE task_report_events.source_run_id = sqlc.arg(source_run_id)::uuid
  AND task_report_events.request_key = sqlc.arg(request_key)::uuid
FOR UPDATE;

-- name: GetTaskReportConversationTarget :one
SELECT
    conversations.id AS conversation_id,
    conversations.agent_id AS recipient_agent_id,
    EXISTS (
        SELECT 1
        FROM runs
        WHERE runs.company_id = conversations.company_id
          AND runs.agent_id = conversations.agent_id
          AND runs.status IN (
              'queued', 'dispatched', 'waiting_local_directory', 'running', 'deferred'
          )
    ) AS agent_busy
FROM conversations
JOIN companies
  ON companies.id = conversations.company_id
 AND companies.deleting_at IS NULL
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
JOIN agents
  ON agents.id = conversations.agent_id
 AND agents.company_id = conversations.company_id
 AND agents.archived_at IS NULL
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.kind = 'direct'
  AND conversations.created_by_user_id = actor_membership.user_id
LIMIT 1;

-- Serialize the global recipient-idle verdict against every Run insert. A Run
-- insert's composite Agent FK takes KEY SHARE on this row, so the winner is
-- linearized before the separate fresh GetTaskReportConversationTarget read.
-- Keeping the busy read out of this statement matters under READ COMMITTED: a
-- statement that waits for this lock retains its pre-wait snapshot.
-- name: LockTaskReportRecipientAgent :one
SELECT agents.id
FROM agents
WHERE agents.company_id = sqlc.arg(company_id)::uuid
  AND agents.id = sqlc.arg(agent_id)::uuid
  AND agents.archived_at IS NULL
FOR UPDATE;

-- name: ListPendingTaskReportEvents :many
SELECT task_report_events.*
FROM task_report_events
WHERE task_report_events.company_id = sqlc.arg(company_id)::uuid
  AND task_report_events.recipient_agent_id = sqlc.arg(recipient_agent_id)::uuid
  AND task_report_events.batch_id IS NULL
  AND task_report_events.superseded_at IS NULL
  -- A report reaches the member's chat with the root Agent only when the
  -- member reads its Task's Project (ADR 0017); the rest stay queued until a
  -- reader asks. One probe per row.
  AND (task_report_events.project_id IS NULL OR COALESCE((
      SELECT true FROM project_access AS project_reader
      WHERE project_reader.company_id = task_report_events.company_id
        AND project_reader.project_id = task_report_events.project_id
        AND project_reader.user_id = sqlc.arg(reader_user_id)::uuid
      LIMIT 1
  ), false))
ORDER BY task_report_events.created_at, task_report_events.id
LIMIT sqlc.arg(page_size)::integer;

-- name: CountPendingTaskReportEvents :one
SELECT count(*)::bigint
FROM task_report_events
WHERE task_report_events.company_id = sqlc.arg(company_id)::uuid
  AND task_report_events.recipient_agent_id = sqlc.arg(recipient_agent_id)::uuid
  AND task_report_events.batch_id IS NULL
  AND task_report_events.superseded_at IS NULL
  -- A report reaches the member's chat with the root Agent only when the
  -- member reads its Task's Project (ADR 0017); the rest stay queued until a
  -- reader asks. One probe per row.
  AND (task_report_events.project_id IS NULL OR COALESCE((
      SELECT true FROM project_access AS project_reader
      WHERE project_reader.company_id = task_report_events.company_id
        AND project_reader.project_id = task_report_events.project_id
        AND project_reader.user_id = sqlc.arg(reader_user_id)::uuid
      LIMIT 1
  ), false));

-- name: LockPendingTaskReportEvents :many
SELECT task_report_events.*
FROM task_report_events
WHERE task_report_events.company_id = sqlc.arg(company_id)::uuid
  AND task_report_events.recipient_agent_id = sqlc.arg(recipient_agent_id)::uuid
  AND task_report_events.batch_id IS NULL
  AND task_report_events.superseded_at IS NULL
  -- A report reaches the member's chat with the root Agent only when the
  -- member reads its Task's Project (ADR 0017); the rest stay queued until a
  -- reader asks. One probe per row.
  AND (task_report_events.project_id IS NULL OR COALESCE((
      SELECT true FROM project_access AS project_reader
      WHERE project_reader.company_id = task_report_events.company_id
        AND project_reader.project_id = task_report_events.project_id
        AND project_reader.user_id = sqlc.arg(reader_user_id)::uuid
      LIMIT 1
  ), false))
ORDER BY task_report_events.created_at, task_report_events.id
FOR UPDATE OF task_report_events;

-- name: SupersedePendingTaskReportEvents :many
UPDATE task_report_events
SET superseded_at = clock_timestamp()
WHERE company_id = sqlc.arg(company_id)::uuid
  AND task_id = sqlc.arg(task_id)::uuid
  AND batch_id IS NULL
  AND superseded_at IS NULL
RETURNING recipient_agent_id;

-- name: GetTaskReportBatchReplay :one
SELECT task_report_batches.*
FROM task_report_batches
WHERE task_report_batches.confirmed_by_user_id = sqlc.arg(actor_user_id)::uuid
  AND task_report_batches.request_key = sqlc.arg(request_key)::uuid
  AND task_report_batches.company_id = sqlc.arg(company_id)::uuid
  AND task_report_batches.delivery_type = 'manual';

-- name: LockTaskReportRequest :exec
SELECT pg_advisory_xact_lock(hashtextextended(
    'boardly/task-report-send/v1/' ||
    sqlc.arg(company_id)::uuid::text || '/' ||
    sqlc.arg(actor_user_id)::uuid::text || '/' ||
    sqlc.arg(request_key)::uuid::text,
    0
));

-- name: InsertTaskReportBatch :one
INSERT INTO task_report_batches (
    company_id, recipient_agent_id, conversation_id,
    confirmed_by_user_id, delivery_type, automation_user_id,
    request_key, message_id, run_id, event_count
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(recipient_agent_id)::uuid,
    sqlc.arg(conversation_id)::uuid,
    sqlc.narg(confirmed_by_user_id)::uuid,
    sqlc.arg(delivery_type)::text,
    sqlc.narg(automation_user_id)::uuid,
    sqlc.arg(request_key)::uuid,
    sqlc.arg(message_id)::uuid,
    sqlc.arg(run_id)::uuid,
    sqlc.arg(event_count)::integer
)
RETURNING task_report_batches.*;

-- name: GetNextProactiveTaskReportTarget :one
SELECT
    companies.id AS company_id,
    automation_member.user_id AS actor_user_id,
    automation_member.role AS actor_role,
    root_conversation.id AS conversation_id,
    root_agent.id AS recipient_agent_id
FROM companies
JOIN company_members AS automation_member
  ON automation_member.company_id = companies.id
 AND automation_member.user_id = companies.proactive_mode_enabled_by_user_id
 AND automation_member.status = 'active'
 AND automation_member.role = ANY(sqlc.arg(actor_roles)::text[])
JOIN conversations AS root_conversation
  ON root_conversation.id = companies.proactive_mode_conversation_id
 AND root_conversation.company_id = companies.id
 AND root_conversation.created_by_user_id = automation_member.user_id
 AND root_conversation.kind = 'direct'
 AND root_conversation.archived_at IS NULL
JOIN agents AS root_agent
  ON root_agent.id = root_conversation.agent_id
 AND root_agent.company_id = companies.id
 AND root_agent.reports_to_agent_id IS NULL
 AND root_agent.archived_at IS NULL
WHERE companies.proactive_mode_enabled
  AND companies.deleting_at IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM runs
      WHERE runs.company_id = companies.id
        AND runs.agent_id = root_agent.id
        AND runs.status IN (
            'queued', 'dispatched', 'waiting_local_directory', 'running', 'deferred'
        )
  )
  AND EXISTS (
      SELECT 1
      FROM task_report_events
      WHERE task_report_events.company_id = companies.id
        AND task_report_events.recipient_agent_id = root_agent.id
        AND task_report_events.batch_id IS NULL
        AND task_report_events.superseded_at IS NULL
        -- Only a report the automation member reads (ADR 0017); otherwise a
        -- Company whose pending reports that member cannot read would be
        -- picked on every pass and starve the rest.
        AND (task_report_events.project_id IS NULL OR COALESCE((
            SELECT true FROM project_access AS project_reader
            WHERE project_reader.company_id = task_report_events.company_id
              AND project_reader.project_id = task_report_events.project_id
              AND project_reader.user_id = automation_member.user_id
            LIMIT 1
        ), false))
  )
ORDER BY companies.updated_at, companies.id
LIMIT 1;

-- name: LockProactiveTaskReportTarget :one
SELECT
    automation_member.user_id AS actor_user_id,
    root_agent.id AS recipient_agent_id
FROM companies
JOIN company_members AS automation_member
  ON automation_member.company_id = companies.id
 AND automation_member.user_id = companies.proactive_mode_enabled_by_user_id
 AND automation_member.status = 'active'
 AND automation_member.role = ANY(sqlc.arg(actor_roles)::text[])
JOIN conversations AS root_conversation
  ON root_conversation.id = companies.proactive_mode_conversation_id
 AND root_conversation.company_id = companies.id
 AND root_conversation.created_by_user_id = automation_member.user_id
 AND root_conversation.kind = 'direct'
 AND root_conversation.archived_at IS NULL
JOIN agents AS root_agent
  ON root_agent.id = root_conversation.agent_id
 AND root_agent.company_id = companies.id
 AND root_agent.reports_to_agent_id IS NULL
 AND root_agent.archived_at IS NULL
WHERE companies.id = sqlc.arg(company_id)::uuid
  AND companies.proactive_mode_enabled
  AND companies.deleting_at IS NULL
  AND automation_member.user_id = sqlc.arg(actor_user_id)::uuid
  AND root_conversation.id = sqlc.arg(conversation_id)::uuid
  AND root_agent.id = sqlc.arg(recipient_agent_id)::uuid
  AND NOT EXISTS (
      SELECT 1
      FROM runs
      WHERE runs.company_id = companies.id
        AND runs.agent_id = root_agent.id
        AND runs.status IN (
            'queued', 'dispatched', 'waiting_local_directory', 'running', 'deferred'
        )
  )
FOR UPDATE OF companies;

-- name: AssignTaskReportEventsToBatch :execrows
UPDATE task_report_events
SET batch_id = sqlc.arg(batch_id)::uuid
WHERE id = ANY(sqlc.arg(event_ids)::uuid[])
  AND company_id = sqlc.arg(company_id)::uuid
  AND recipient_agent_id = sqlc.arg(recipient_agent_id)::uuid
  AND batch_id IS NULL
  AND superseded_at IS NULL;
