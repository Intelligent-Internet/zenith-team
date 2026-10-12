-- Durable action approvals and human questions. The two nouns keep separate
-- tables, status vocabularies, and operation-scoped replay slots.

-- name: CreateApproval :one
INSERT INTO approvals (
    company_id, type, commerce_manifest_id, rule_key, title, summary, preview_markdown, presentation,
    requested_by_agent_id, origin_run_id, request_key, task_id,
    conversation_id, origin_user_id, canonical_action,
    request_sha256, action_sha256, expires_at
) VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(type)::text,
    sqlc.narg(commerce_manifest_id)::uuid,
    sqlc.arg(rule_key)::text,
    sqlc.arg(title)::text,
    sqlc.narg(summary)::text,
    sqlc.arg(preview_markdown)::text,
    sqlc.narg(presentation)::jsonb,
    sqlc.arg(requested_by_agent_id)::uuid,
    sqlc.arg(origin_run_id)::uuid,
    sqlc.arg(request_key)::uuid,
    sqlc.narg(task_id)::uuid,
    sqlc.arg(conversation_id)::uuid,
    sqlc.narg(origin_user_id)::uuid,
    sqlc.arg(canonical_action)::jsonb,
    sqlc.arg(request_sha256)::bytea,
    sqlc.arg(action_sha256)::bytea,
    sqlc.arg(expires_at)::timestamptz
)
ON CONFLICT (origin_run_id, request_key) DO NOTHING
RETURNING *;

-- name: GetApprovalByOriginRequestKey :one
SELECT *
FROM approvals
WHERE origin_run_id = sqlc.arg(origin_run_id)::uuid
  AND request_key = sqlc.arg(request_key)::uuid;

-- name: HasProactiveCompanyPlanApprovalAuthority :one
-- The caller already holds the canonical Company, acting membership,
-- Conversation, and live Run locks. This exact-match read decides only
-- whether that locked direct origin is the Company's still-authorized
-- Proactive Mode conversation with its active root Agent.
SELECT EXISTS (
    SELECT 1
    FROM companies
    JOIN company_members AS automation_membership
      ON automation_membership.company_id = companies.id
     AND automation_membership.user_id = sqlc.arg(actor_user_id)::uuid
     AND automation_membership.status = 'active'
     AND automation_membership.role = ANY(sqlc.arg(actor_roles)::text[])
    JOIN conversations
      ON conversations.company_id = companies.id
     AND conversations.id = companies.proactive_mode_conversation_id
     AND conversations.id = sqlc.arg(conversation_id)::uuid
     AND conversations.kind = 'direct'
     AND conversations.archived_at IS NULL
     AND conversations.created_by_user_id = automation_membership.user_id
     AND conversations.agent_id = sqlc.arg(agent_id)::uuid
    JOIN agents
      ON agents.company_id = conversations.company_id
     AND agents.id = conversations.agent_id
     AND agents.reports_to_agent_id IS NULL
     AND agents.archived_at IS NULL
    WHERE companies.id = sqlc.arg(company_id)::uuid
      AND companies.deleting_at IS NULL
      AND companies.proactive_mode_enabled
      AND companies.proactive_mode_enabled_by_user_id = automation_membership.user_id
) AS authorized;

-- name: GetApprovalProjection :one
SELECT
    sqlc.embed(approvals),
    agents.name AS requesting_agent_name,
    decided_users.display_name AS decided_by_display_name,
    decided_users.avatar_url AS decided_by_avatar_url
FROM approvals
JOIN agents
  ON agents.company_id = approvals.company_id
 AND agents.id = approvals.requested_by_agent_id
LEFT JOIN users AS decided_users ON decided_users.id = approvals.decided_by_user_id
WHERE approvals.company_id = sqlc.arg(company_id)::uuid
  AND approvals.id = sqlc.arg(approval_id)::uuid
  AND (
      approvals.origin_user_id = sqlc.arg(actor_user_id)::uuid
      -- Task origin: read by the readers of the Task chat it was raised in,
      -- so it follows the Project, also after the Task is deleted (ADR 0017).
      OR (approvals.task_id IS NOT NULL AND COALESCE((
          SELECT true FROM conversation_readers AS reader
          WHERE reader.company_id = approvals.company_id
            AND reader.conversation_id = approvals.conversation_id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
          LIMIT 1
      ), false))
  );

-- name: GetApprovalProjectionForSystem :one
-- The same projection with no reader filter, for frames and lifecycle writes
-- the server stages itself (their audience is decided by the router) and for
-- the Commerce publication, which has its own owner fence.
SELECT
    sqlc.embed(approvals),
    agents.name AS requesting_agent_name,
    decided_users.display_name AS decided_by_display_name,
    decided_users.avatar_url AS decided_by_avatar_url
FROM approvals
JOIN agents
  ON agents.company_id = approvals.company_id
 AND agents.id = approvals.requested_by_agent_id
LEFT JOIN users AS decided_users ON decided_users.id = approvals.decided_by_user_id
WHERE approvals.company_id = sqlc.arg(company_id)::uuid
  AND approvals.id = sqlc.arg(approval_id)::uuid;

-- name: ListApprovalProjections :many
-- The Approvals the caller reads. With decide_view, only those they may
-- decide, by the rule requireApprovalDecider applies: a Company Plan
-- Approval raised in a Task chat with a Project needs
-- project.approvals.resolve there; every other one the Company
-- approvals.resolve.
-- The Projects the caller reads and may decide in (ADR 0017): one
-- project_permission_held call per Project they read, never once per listed
-- row: decided once per Project, not per row, like the readable CTE of
-- ListTaskProjectionsForActiveMember.
-- company_decider is the caller's Company approvals.resolve, read from their
-- active membership.
WITH readable AS MATERIALIZED (
    SELECT access.project_id,
           project_permission_held(
               access.company_id, access.project_id, access.user_id,
               sqlc.arg(decide_permission)::text,
               sqlc.arg(company_default_permissions)::text[],
               sqlc.arg(management_permissions)::text[],
               sqlc.arg(management_roles)::text[]
           ) AS decides
    FROM project_access AS access
    WHERE access.company_id = sqlc.arg(company_id)::uuid
      AND access.user_id = sqlc.arg(actor_user_id)::uuid
      AND sqlc.arg(decide_view)::boolean
), company_decider AS MATERIALIZED (
    SELECT EXISTS (
        SELECT 1 FROM company_members AS member
        WHERE member.company_id = sqlc.arg(company_id)::uuid
          AND member.user_id = sqlc.arg(actor_user_id)::uuid
          AND member.status = 'active'
          AND member.role = ANY(sqlc.arg(actor_roles)::text[])
    ) AS decides
)
SELECT
    sqlc.embed(approvals),
    agents.name AS requesting_agent_name,
    decided_users.display_name AS decided_by_display_name,
    decided_users.avatar_url AS decided_by_avatar_url
FROM approvals
JOIN agents
  ON agents.company_id = approvals.company_id
 AND agents.id = approvals.requested_by_agent_id
LEFT JOIN users AS decided_users ON decided_users.id = approvals.decided_by_user_id
LEFT JOIN conversations AS origin_chat
  ON origin_chat.id = approvals.conversation_id
 AND origin_chat.company_id = approvals.company_id
WHERE approvals.company_id = sqlc.arg(company_id)::uuid
  AND (
      approvals.origin_user_id = sqlc.arg(actor_user_id)::uuid
      -- Task origin: read by the readers of the Task chat it was raised in,
      -- so it follows the Project, also after the Task is deleted (ADR 0017).
      OR (approvals.task_id IS NOT NULL AND COALESCE((
          SELECT true FROM conversation_readers AS reader
          WHERE reader.company_id = approvals.company_id
            AND reader.conversation_id = approvals.conversation_id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
          LIMIT 1
      ), false))
  )
  AND (
      NOT sqlc.arg(has_statuses)::boolean
      OR approvals.status = ANY(sqlc.arg(statuses)::text[])
  )
  AND (
      NOT sqlc.arg(has_agent_id)::boolean
      OR approvals.requested_by_agent_id = sqlc.arg(agent_id)::uuid
  )
  AND (
      NOT sqlc.arg(has_task_id)::boolean
      OR approvals.task_id = sqlc.arg(task_id)::uuid
  )
  AND (
      NOT sqlc.arg(decide_view)::boolean
      OR CASE
          WHEN approvals.task_id IS NOT NULL
           AND approvals.type = 'company_plan_apply'
           AND origin_chat.kind = 'task'
           AND origin_chat.project_id IS NOT NULL
              THEN origin_chat.project_id IN (SELECT readable.project_id FROM readable WHERE readable.decides)
          ELSE (SELECT company_decider.decides FROM company_decider)
      END
  )
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (approvals.created_at, approvals.id) < (
          sqlc.arg(before_created_at)::timestamptz,
          sqlc.arg(before_id)::uuid
      )
  )
ORDER BY approvals.created_at DESC, approvals.id DESC
LIMIT sqlc.arg(page_size)::integer;

-- name: LockApprovalForResolution :one
SELECT *
FROM approvals
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(approval_id)::uuid
FOR UPDATE;

-- name: LockCommerceManifestApproval :one
SELECT *
FROM approvals
WHERE company_id = sqlc.arg(company_id)::uuid
  AND type = 'commerce_manifest_publish'
  AND commerce_manifest_id = sqlc.arg(commerce_manifest_id)::uuid
FOR UPDATE;

-- name: ResolveApproval :one
-- Opens the continuation receipt but does NOT name a Run. The receipt is an
-- intent — the reconciler may still defer or abandon it — so the id is stamped
-- later, by DeliverApprovalContinuation, in the transaction that inserts the
-- Run it names.
UPDATE approvals
SET status = sqlc.arg(status)::text,
    decision_note = sqlc.narg(decision_note)::text,
    decided_by_user_id = sqlc.arg(decided_by_user_id)::uuid,
    execution_status = sqlc.narg(execution_status)::text,
    execution_result = sqlc.narg(execution_result)::jsonb,
    skipped_reason = sqlc.narg(skipped_reason)::text,
    continuation_prompt = sqlc.arg(continuation_prompt)::text,
    continuation_status = 'pending',
    continuation_next_attempt_at = statement_timestamp(),
    updated_at = statement_timestamp(),
    version = version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(approval_id)::uuid
  AND status = 'pending'
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: ResolveApprovalInline :one
-- Proactive Mode completes the Company Plan action in the request transaction.
-- No continuation Run is needed, so this terminal receipt is deliberately
-- ineligible for the continuation worker.
UPDATE approvals
SET status = 'approved',
    decision_note = NULL,
    decided_by_user_id = sqlc.arg(decided_by_user_id)::uuid,
    execution_status = sqlc.arg(execution_status)::text,
    execution_result = sqlc.narg(execution_result)::jsonb,
    skipped_reason = sqlc.narg(skipped_reason)::text,
    continuation_run_id = NULL,
    continuation_prompt = NULL,
    continuation_status = 'not_required',
    continuation_next_attempt_at = NULL,
    continuation_abandon_reason = NULL,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(approval_id)::uuid
  AND status = 'pending'
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: ExpireDueApprovals :many
WITH due AS (
    SELECT id
    FROM approvals
    WHERE status = 'pending'
      AND expires_at <= statement_timestamp()
    ORDER BY expires_at, id
    LIMIT sqlc.arg(batch_size)::integer
    FOR UPDATE SKIP LOCKED
)
UPDATE approvals
SET status = 'expired',
    continuation_prompt = 'The approval request expired without a decision.',
    continuation_status = 'pending',
    continuation_next_attempt_at = statement_timestamp(),
    updated_at = statement_timestamp(),
    version = version + 1
FROM due
WHERE approvals.id = due.id
RETURNING approvals.*;

-- name: PeekDueApprovalContinuationCompany :one
-- The continuation worker's first statement, and it locks nothing: it names
-- the Company of the oldest due receipt so the worker can take that Company's
-- share lock BEFORE it claims a receipt. Company before receipt is the order
-- account deletion and every membership or Task mutation take, so the worker
-- can never hold a receipt while it waits for its Company. A Company already
-- being deleted is skipped; its receipts go with it.
SELECT approvals.company_id
FROM approvals
JOIN companies ON companies.id = approvals.company_id
WHERE approvals.continuation_status = 'pending'
  AND approvals.continuation_next_attempt_at <= statement_timestamp()
  AND companies.deleting_at IS NULL
ORDER BY approvals.continuation_next_attempt_at, approvals.id
LIMIT 1;

-- name: ClaimDueApprovalContinuation :one
-- Claims inside the Company the caller has already locked FOR SHARE (see
-- PeekDueApprovalContinuationCompany).
SELECT *
FROM approvals
WHERE company_id = sqlc.arg(company_id)::uuid
  AND continuation_status = 'pending'
  AND continuation_next_attempt_at <= statement_timestamp()
ORDER BY continuation_next_attempt_at, id
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: DeliverApprovalContinuation :one
-- The delivery receipt and the identity it names are ONE write. The caller has
-- already INSERTed the continuation Run inside this transaction and passes the
-- id the database-generated UUID allocated, so the stamp and the receipt commit together or
-- not at all. This is the only statement that ever sets continuation_run_id,
-- which is why a deferred or abandoned continuation — neither of which reaches
-- here — leaves the column NULL instead of naming a Run nobody wrote.
UPDATE approvals
SET continuation_run_id = sqlc.arg(continuation_run_id)::uuid,
    continuation_status = 'delivered',
    continuation_attempts = continuation_attempts + 1,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(approval_id)::uuid
  AND continuation_status = 'pending'
RETURNING *;

-- name: DeferApprovalContinuation :one
UPDATE approvals
SET continuation_next_attempt_at = sqlc.arg(next_attempt_at)::timestamptz,
    continuation_attempts = continuation_attempts + 1
WHERE id = sqlc.arg(approval_id)::uuid
  AND continuation_status = 'pending'
RETURNING *;

-- name: AbandonApprovalContinuation :one
-- Delivery and abandonment each change what a client reads — the Run id, or
-- the settled fact that there will be none — so both advance the revision the
-- re-sent approval.resolved frame carries. A deferral changes neither.
UPDATE approvals
SET continuation_status = 'abandoned',
    continuation_abandon_reason = sqlc.arg(reason)::text,
    continuation_attempts = continuation_attempts + 1,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(approval_id)::uuid
  AND continuation_status = 'pending'
RETURNING *;

-- name: ApprovalStatsByRule :many
-- Counts exactly the rows ListApprovalProjections answers with decide_view,
-- whatever their status (ADR 0017): the rule requireApprovalDecider applies.
-- A Company Plan Approval raised in a Task chat with a Project counts where
-- the caller holds project.approvals.resolve; any other Approval of a Task
-- chat they read, and their own Approvals raised outside a Task, count when
-- they hold the Company approvals.resolve. Another member's direct
-- Approval, and a Project the caller cannot read, add nothing.
-- The Projects the caller reads and may decide in (ADR 0017): one
-- project_permission_held call per Project they read, never once per listed
-- row: decided once per Project, not per row, like the readable CTE of
-- ListTaskProjectionsForActiveMember.
-- company_decider is the caller's Company approvals.resolve, read from their
-- active membership.
WITH readable AS MATERIALIZED (
    SELECT access.project_id,
           project_permission_held(
               access.company_id, access.project_id, access.user_id,
               sqlc.arg(decide_permission)::text,
               sqlc.arg(company_default_permissions)::text[],
               sqlc.arg(management_permissions)::text[],
               sqlc.arg(management_roles)::text[]
           ) AS decides
    FROM project_access AS access
    WHERE access.company_id = sqlc.arg(company_id)::uuid
      AND access.user_id = sqlc.arg(actor_user_id)::uuid
), company_decider AS MATERIALIZED (
    SELECT EXISTS (
        SELECT 1 FROM company_members AS member
        WHERE member.company_id = sqlc.arg(company_id)::uuid
          AND member.user_id = sqlc.arg(actor_user_id)::uuid
          AND member.status = 'active'
          AND member.role = ANY(sqlc.arg(actor_roles)::text[])
    ) AS decides
)
SELECT
    approvals.rule_key,
    count(*)::bigint AS requested,
    count(*) FILTER (WHERE approvals.status = 'approved')::bigint AS approved,
    count(*) FILTER (WHERE approvals.status = 'rejected')::bigint AS rejected,
    count(*) FILTER (WHERE approvals.status = 'expired')::bigint AS expired,
    count(*) FILTER (WHERE approvals.status = 'cancelled')::bigint AS cancelled
FROM approvals
LEFT JOIN conversations AS origin_chat
  ON origin_chat.id = approvals.conversation_id
 AND origin_chat.company_id = approvals.company_id
WHERE approvals.company_id = sqlc.arg(company_id)::uuid
  AND approvals.created_at >= sqlc.arg(since)::timestamptz
  AND (
      NOT sqlc.arg(has_agent_id)::boolean
      OR approvals.requested_by_agent_id = sqlc.arg(agent_id)::uuid
  )
  AND (
      approvals.origin_user_id = sqlc.arg(actor_user_id)::uuid
      OR (approvals.task_id IS NOT NULL AND COALESCE((
          SELECT true FROM conversation_readers AS reader
          WHERE reader.company_id = approvals.company_id
            AND reader.conversation_id = approvals.conversation_id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
          LIMIT 1
      ), false))
  )
  AND CASE
      WHEN approvals.task_id IS NOT NULL
       AND approvals.type = 'company_plan_apply'
       AND origin_chat.kind = 'task'
       AND origin_chat.project_id IS NOT NULL
          THEN origin_chat.project_id IN (SELECT readable.project_id FROM readable WHERE readable.decides)
      ELSE (SELECT company_decider.decides FROM company_decider)
  END
GROUP BY approvals.rule_key
ORDER BY approvals.rule_key;

-- name: CreateQuestion :one
INSERT INTO questions (
    company_id, title, asked_by_agent_id, origin_run_id, request_key,
    task_id, conversation_id, origin_user_id, prompts, request_sha256, expires_at
) VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(title)::text,
    sqlc.arg(asked_by_agent_id)::uuid,
    sqlc.arg(origin_run_id)::uuid,
    sqlc.arg(request_key)::uuid,
    sqlc.narg(task_id)::uuid,
    sqlc.arg(conversation_id)::uuid,
    sqlc.narg(origin_user_id)::uuid,
    sqlc.arg(prompts)::jsonb,
    sqlc.arg(request_sha256)::bytea,
    sqlc.arg(expires_at)::timestamptz
)
ON CONFLICT (origin_run_id, request_key) DO NOTHING
RETURNING *;

-- name: GetQuestionByOriginRequestKey :one
SELECT *
FROM questions
WHERE origin_run_id = sqlc.arg(origin_run_id)::uuid
  AND request_key = sqlc.arg(request_key)::uuid;

-- name: GetQuestionProjection :one
SELECT
    sqlc.embed(questions),
    agents.name AS asking_agent_name,
    resolved_users.display_name AS resolved_by_display_name,
    resolved_users.avatar_url AS resolved_by_avatar_url
FROM questions
JOIN agents
  ON agents.company_id = questions.company_id
 AND agents.id = questions.asked_by_agent_id
LEFT JOIN users AS resolved_users ON resolved_users.id = questions.resolved_by_user_id
WHERE questions.company_id = sqlc.arg(company_id)::uuid
  AND questions.id = sqlc.arg(question_id)::uuid
  AND (
      questions.origin_user_id = sqlc.arg(actor_user_id)::uuid
      -- Task origin: read by the readers of the Task chat it was raised in,
      -- so it follows the Project, also after the Task is deleted (ADR 0017).
      OR (questions.task_id IS NOT NULL AND COALESCE((
          SELECT true FROM conversation_readers AS reader
          WHERE reader.company_id = questions.company_id
            AND reader.conversation_id = questions.conversation_id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
          LIMIT 1
      ), false))
  );

-- name: GetQuestionProjectionForSystem :one
-- GetApprovalProjectionForSystem for a Question.
SELECT
    sqlc.embed(questions),
    agents.name AS asking_agent_name,
    resolved_users.display_name AS resolved_by_display_name,
    resolved_users.avatar_url AS resolved_by_avatar_url
FROM questions
JOIN agents
  ON agents.company_id = questions.company_id
 AND agents.id = questions.asked_by_agent_id
LEFT JOIN users AS resolved_users ON resolved_users.id = questions.resolved_by_user_id
WHERE questions.company_id = sqlc.arg(company_id)::uuid
  AND questions.id = sqlc.arg(question_id)::uuid;

-- name: ListQuestionProjections :many
-- The Questions the caller reads. With decide_view, only those they may
-- answer, by the rule the Question service applies: a Question raised in a
-- Task chat with a Project needs project.approvals.resolve there; the
-- caller's own direct Question and one of a Task chat with no Project need
-- nothing more than reading it.
WITH decidable AS MATERIALIZED (
    SELECT access.project_id
    FROM project_access AS access
    WHERE access.company_id = sqlc.arg(company_id)::uuid
      AND access.user_id = sqlc.arg(actor_user_id)::uuid
      AND sqlc.arg(decide_view)::boolean
      AND project_permission_held(
          access.company_id, access.project_id, access.user_id,
          sqlc.arg(decide_permission)::text,
          sqlc.arg(company_default_permissions)::text[],
          sqlc.arg(management_permissions)::text[],
          sqlc.arg(management_roles)::text[]
      )
)
SELECT
    sqlc.embed(questions),
    agents.name AS asking_agent_name,
    resolved_users.display_name AS resolved_by_display_name,
    resolved_users.avatar_url AS resolved_by_avatar_url
FROM questions
JOIN agents
  ON agents.company_id = questions.company_id
 AND agents.id = questions.asked_by_agent_id
LEFT JOIN users AS resolved_users ON resolved_users.id = questions.resolved_by_user_id
LEFT JOIN conversations AS origin_chat
  ON origin_chat.id = questions.conversation_id
 AND origin_chat.company_id = questions.company_id
WHERE questions.company_id = sqlc.arg(company_id)::uuid
  AND (
      questions.origin_user_id = sqlc.arg(actor_user_id)::uuid
      -- Task origin: read by the readers of the Task chat it was raised in,
      -- so it follows the Project, also after the Task is deleted (ADR 0017).
      OR (questions.task_id IS NOT NULL AND COALESCE((
          SELECT true FROM conversation_readers AS reader
          WHERE reader.company_id = questions.company_id
            AND reader.conversation_id = questions.conversation_id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
          LIMIT 1
      ), false))
  )
  AND (
      NOT sqlc.arg(has_statuses)::boolean
      OR questions.status = ANY(sqlc.arg(statuses)::text[])
  )
  AND (
      NOT sqlc.arg(has_agent_id)::boolean
      OR questions.asked_by_agent_id = sqlc.arg(agent_id)::uuid
  )
  AND (
      NOT sqlc.arg(has_task_id)::boolean
      OR questions.task_id = sqlc.arg(task_id)::uuid
  )
  AND (
      NOT sqlc.arg(decide_view)::boolean
      OR questions.task_id IS NULL
      OR origin_chat.kind IS DISTINCT FROM 'task'
      OR origin_chat.project_id IS NULL
      OR origin_chat.project_id IN (SELECT decidable.project_id FROM decidable)
  )
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (questions.created_at, questions.id) < (
          sqlc.arg(before_created_at)::timestamptz,
          sqlc.arg(before_id)::uuid
      )
  )
ORDER BY questions.created_at DESC, questions.id DESC
LIMIT sqlc.arg(page_size)::integer;

-- name: LockQuestionForResolution :one
SELECT *
FROM questions
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(question_id)::uuid
FOR UPDATE;

-- name: ResolveQuestion :one
-- Opens the continuation receipt without naming a Run, exactly as
-- ResolveApproval does.
UPDATE questions
SET status = sqlc.arg(status)::text,
    answers = sqlc.narg(answers)::jsonb,
    resolved_by_user_id = sqlc.arg(resolved_by_user_id)::uuid,
    continuation_prompt = sqlc.arg(continuation_prompt)::text,
    continuation_status = 'pending',
    continuation_next_attempt_at = statement_timestamp(),
    updated_at = statement_timestamp(),
    version = version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(question_id)::uuid
  AND status = 'pending'
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: ExpireDueQuestions :many
WITH due AS (
    SELECT id
    FROM questions
    WHERE status = 'pending'
      AND expires_at <= statement_timestamp()
    ORDER BY expires_at, id
    LIMIT sqlc.arg(batch_size)::integer
    FOR UPDATE SKIP LOCKED
)
UPDATE questions
SET status = 'expired',
    continuation_prompt = 'The question expired without an answer.',
    continuation_status = 'pending',
    continuation_next_attempt_at = statement_timestamp(),
    updated_at = statement_timestamp(),
    version = version + 1
FROM due
WHERE questions.id = due.id
RETURNING questions.*;

-- name: PeekDueQuestionContinuationCompany :one
-- PeekDueApprovalContinuationCompany for Questions: unlocked, so the worker
-- takes the Company lock before the receipt lock.
SELECT questions.company_id
FROM questions
JOIN companies ON companies.id = questions.company_id
WHERE questions.continuation_status = 'pending'
  AND questions.continuation_next_attempt_at <= statement_timestamp()
  AND companies.deleting_at IS NULL
ORDER BY questions.continuation_next_attempt_at, questions.id
LIMIT 1;

-- name: ClaimDueQuestionContinuation :one
-- Claims inside the Company the caller has already locked FOR SHARE.
SELECT *
FROM questions
WHERE company_id = sqlc.arg(company_id)::uuid
  AND continuation_status = 'pending'
  AND continuation_next_attempt_at <= statement_timestamp()
ORDER BY continuation_next_attempt_at, id
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: DeliverQuestionContinuation :one
-- The Approval receipt's twin: the continuation Run's database-generated UUID id and the
-- delivered receipt are one write in the transaction that INSERTed the Run.
UPDATE questions
SET continuation_run_id = sqlc.arg(continuation_run_id)::uuid,
    continuation_status = 'delivered',
    continuation_attempts = continuation_attempts + 1,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(question_id)::uuid
  AND continuation_status = 'pending'
RETURNING *;

-- name: DeferQuestionContinuation :one
UPDATE questions
SET continuation_next_attempt_at = sqlc.arg(next_attempt_at)::timestamptz,
    continuation_attempts = continuation_attempts + 1
WHERE id = sqlc.arg(question_id)::uuid
  AND continuation_status = 'pending'
RETURNING *;

-- name: AbandonQuestionContinuation :one
-- Advances the revision for the same reason AbandonApprovalContinuation does.
UPDATE questions
SET continuation_status = 'abandoned',
    continuation_abandon_reason = sqlc.arg(reason)::text,
    continuation_attempts = continuation_attempts + 1,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(question_id)::uuid
  AND continuation_status = 'pending'
RETURNING *;

-- name: ListActiveMemberUserIDs :many
SELECT user_id
FROM company_members
WHERE company_id = sqlc.arg(company_id)::uuid
  AND status = 'active'
ORDER BY user_id;
