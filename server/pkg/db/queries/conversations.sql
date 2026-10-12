-- The Conversation row carries the provider-session resume pointer and, for
-- direct conversations, the creator's read/pin/archive state. Task
-- conversations carry no read tracking — unread is the inbox's job.

-- name: InsertDirectConversation :one
-- Pin the creator's first active CEO chat. Existing chats, including ones the
-- creator unpinned, prevent later explicit sessions from changing that choice.
INSERT INTO conversations (
    company_id, kind, agent_id, created_by_user_id, title, request_key, pinned_at
)
SELECT
    companies.id,
    'direct',
    agents.id,
    actor_membership.user_id,
    sqlc.narg(title)::text,
    sqlc.narg(request_key)::uuid,
    CASE
        WHEN agents.reports_to_agent_id IS NULL
          AND NOT EXISTS (
              SELECT 1
              FROM conversations AS existing
              WHERE existing.company_id = companies.id
                AND existing.kind = 'direct'
                AND existing.document_thread_id IS NULL
                AND existing.agent_id = agents.id
                AND existing.created_by_user_id = actor_membership.user_id
                AND existing.archived_at IS NULL
          )
        THEN clock_timestamp()
        ELSE NULL
    END
FROM companies
JOIN company_members AS actor_membership
  ON actor_membership.company_id = companies.id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
JOIN agents
  ON agents.company_id = companies.id
 AND agents.id = sqlc.arg(agent_id)::uuid
 AND agents.archived_at IS NULL
WHERE companies.id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL
ON CONFLICT DO NOTHING
RETURNING conversations.*;

-- name: UpdateScheduleConversationDefinition :one
UPDATE conversations
SET
    agent_id = agents.id,
    title = sqlc.arg(title)::text,
    updated_at = clock_timestamp(),
    version = conversations.version + 1
FROM companies
JOIN company_members AS actor_membership
  ON actor_membership.company_id = companies.id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
 AND actor_membership.role = ANY(sqlc.arg(actor_roles)::text[])
JOIN agents
  ON agents.company_id = companies.id
 AND agents.id = sqlc.arg(agent_id)::uuid
 AND agents.archived_at IS NULL
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.kind = 'schedule'
  AND conversations.schedule_id = sqlc.arg(schedule_id)::uuid
  AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
  AND conversations.version < 9007199254740991
  AND companies.id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL
RETURNING conversations.*;

-- name: GetDirectConversationReplay :one
SELECT conversations.*
FROM conversations
JOIN companies ON companies.id = conversations.company_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.kind = 'direct' AND conversations.document_thread_id IS NULL
  AND conversations.agent_id = sqlc.arg(agent_id)::uuid
  AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
  AND companies.deleting_at IS NULL;

-- name: GetDirectConversationByAgent :one
-- An active direct Agent chat is the durable provider-session boundary.
-- Resolve the oldest matching row so repeated opens keep using the same
-- Conversation and therefore the same provider thread. Archived Conversations
-- remain history and do not block an explicit fresh chat, matching
-- ListDirectConversationsForCreator.
SELECT conversations.*
FROM conversations
JOIN companies ON companies.id = conversations.company_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
JOIN agents
  ON agents.company_id = conversations.company_id
 AND agents.id = conversations.agent_id
 AND agents.archived_at IS NULL
WHERE conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.kind = 'direct' AND conversations.document_thread_id IS NULL
  AND conversations.agent_id = sqlc.arg(agent_id)::uuid
  AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
  AND conversations.archived_at IS NULL
  AND companies.deleting_at IS NULL
ORDER BY conversations.created_at, conversations.id
LIMIT 1;

-- name: GetDirectConversationForCreator :one
SELECT conversations.*
FROM conversations
JOIN companies ON companies.id = conversations.company_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.kind = 'direct' AND conversations.document_thread_id IS NULL
  AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
  AND companies.deleting_at IS NULL;

-- The two facts that decide who may see anything a Conversation owns: a task
-- conversation is the whole Company's, any other belongs to the member who
-- opened it. Read once per event and carried on the payload, so the consumer
-- that delivers never queries.
-- name: GetConversationRoute :one
SELECT conversations.kind, conversations.created_by_user_id
FROM conversations
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid;

-- name: ResolveConversationStateForActiveUser :one
SELECT
    conversations.*,
    tasks.id AS resolved_task_id
FROM conversations
JOIN companies ON companies.id = conversations.company_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
LEFT JOIN tasks
  ON tasks.conversation_id = conversations.id
 AND tasks.company_id = conversations.company_id
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND companies.deleting_at IS NULL
  AND (
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind IN ('direct','schedule') AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
          AND EXISTS (SELECT 1 FROM conversation_readers reader WHERE reader.conversation_id=conversations.id AND reader.user_id=sqlc.arg(actor_user_id)::uuid))
  );

-- name: ListDirectConversationsForCreator :many
SELECT conversations.*
FROM conversations
JOIN companies ON companies.id = conversations.company_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
WHERE conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.kind = 'direct' AND conversations.document_thread_id IS NULL
  AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
  AND companies.deleting_at IS NULL
  AND (sqlc.arg(include_archived)::boolean OR conversations.archived_at IS NULL)
ORDER BY conversations.updated_at DESC, conversations.id DESC;

-- name: ListDirectConversationSummaryBasesBulk :many
-- The counters use the API's one run-counting vocabulary: queued_run_count is
-- queued + deferred (enqueued work no machine has claimed, including retry
-- backoff), active_run_count is dispatched + waiting_local_directory +
-- running.
SELECT
    conversations.id AS conversation_id,
    conversations.updated_at,
    count(runs.id) FILTER (
        WHERE runs.status IN ('queued', 'deferred')
    )::bigint AS queued_run_count,
    count(runs.id) FILTER (
        WHERE runs.status IN ('dispatched', 'waiting_local_directory', 'running')
    )::bigint AS active_run_count
FROM conversations
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
-- The status set is in the JOIN, not a FILTER over an unrestricted join: it
-- is what lets runs_conversation_live_idx serve this, and without it the
-- counters read every run the conversation ever had to render a badge.
LEFT JOIN runs
  ON runs.conversation_id = conversations.id
 AND runs.status IN ('queued', 'deferred', 'dispatched', 'waiting_local_directory', 'running')
WHERE conversations.id = ANY(sqlc.arg(conversation_ids)::uuid[])
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.kind = 'direct'
  AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
GROUP BY conversations.id
ORDER BY conversations.id;

-- name: ListDirectConversationLatestMessagesBulk :many
-- The preview is what a person can see, so the type predicate is the
-- timeline's: without it a tool call becomes the last line of a chat row.
SELECT DISTINCT ON (conversation_items.conversation_id)
    conversation_items.conversation_id,
    conversation_items.seq,
    conversation_items.created_at,
    conversation_items.type,
    conversation_items.author_kind,
    -- The preview line. Free: the row is already being fetched from the heap
    -- for its other columns, and truncating here keeps a long message from
    -- riding a fifty-row list. The wire marks a cut with contentTruncated.
    left(conversation_items.content, 200) AS content_preview,
    length(conversation_items.content) > 200 AS content_truncated
FROM conversation_items
JOIN conversations ON conversations.id = conversation_items.conversation_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
WHERE conversation_items.conversation_id = ANY(sqlc.arg(conversation_ids)::uuid[])
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.kind = 'direct'
  AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
  AND conversation_items.type IN ('user_message', 'agent_text')
ORDER BY conversation_items.conversation_id, conversation_items.seq DESC;

-- name: ListDirectConversationLatestRunsBulk :many
SELECT DISTINCT ON (runs.conversation_id)
    runs.conversation_id,
    runs.id AS run_id,
    runs.agent_id,
    runs.status,
    runs.created_at,
    runs.failure_reason,
    runs.attempt
FROM runs
JOIN conversations ON conversations.id = runs.conversation_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
WHERE runs.conversation_id = ANY(sqlc.arg(conversation_ids)::uuid[])
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.kind = 'direct'
  AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
ORDER BY runs.conversation_id, runs.created_at DESC, runs.id DESC;

-- name: ListDirectConversationAgentRefsBulk :many
-- The agent on the other side of each direct chat, denormalized to a name,
-- title and online flag so a list row or a panel header renders without a
-- roster read. LEFT JOIN because agent_id is SET NULL when the agent is
-- deleted: that conversation still lists, with a null agent. Both verdicts
-- are the VIEWER's (ADR 0014): bound says whether the viewer has set the
-- agent up on one of their own runtimes, and runtime_available whether that
-- runtime is online, usable and signed in — the same predicate the Agent
-- projection computes for the viewer, so the composer and the roster agree.
SELECT
    conversations.id AS conversation_id,
    agents.id AS agent_id,
    agents.name AS agent_name,
    agents.title AS agent_title,
    (
        agents.archived_at IS NULL
        AND viewer_runtimes.id IS NOT NULL
    )::boolean AS bound,
    (
        agents.archived_at IS NULL
        AND viewer_runtimes.id IS NOT NULL
        AND viewer_runtimes.status = 'online'
        AND viewer_runtimes.unusable_reason_code IS NULL
        AND viewer_runtimes.provider_sign_in_state IS DISTINCT FROM 'signed_out'
    )::boolean AS runtime_available
FROM conversations
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
LEFT JOIN agents ON agents.id = conversations.agent_id
LEFT JOIN agent_runtime_bindings AS viewer_binding
  ON viewer_binding.company_id = conversations.company_id
 AND viewer_binding.agent_id = agents.id
 AND viewer_binding.user_id = sqlc.arg(actor_user_id)::uuid
LEFT JOIN runtimes AS viewer_runtimes
  ON viewer_runtimes.company_id = viewer_binding.company_id
 AND viewer_runtimes.id = viewer_binding.runtime_id
 AND viewer_runtimes.removed_at IS NULL
 AND viewer_runtimes.owner_user_id = viewer_binding.user_id
WHERE conversations.id = ANY(sqlc.arg(conversation_ids)::uuid[])
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.kind = 'direct'
  AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
ORDER BY conversations.id;

-- name: LockConversationForActiveUserMutation :one
SELECT conversations.*
FROM conversations
JOIN companies ON companies.id = conversations.company_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL
  AND (
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind IN ('direct','schedule') AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
          AND EXISTS (SELECT 1 FROM conversation_readers reader WHERE reader.conversation_id=conversations.id AND reader.user_id=sqlc.arg(actor_user_id)::uuid))
  )
FOR UPDATE OF conversations;

-- A Run token is resolved before the domain transaction starts, so its
-- Conversation facts must be locked and matched again after the Company fence
-- is acquired. This lock precedes the exact live Run lock below it in the
-- human-input services, preserving Conversation -> Run order. Task
-- Conversations deliberately have no agent_id; their exact Agent is proved
-- by the following Run lock, while direct and Schedule Conversations carry
-- and match their Agent here.
-- name: LockHumanInputOriginConversation :one
SELECT conversations.*
FROM conversations
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.kind = sqlc.arg(conversation_kind)::text
  AND conversations.task_id IS NOT DISTINCT FROM sqlc.narg(task_id)::uuid
  AND (
      conversations.kind = 'task'
      OR conversations.agent_id = sqlc.arg(agent_id)::uuid
  )
  AND (conversations.created_by_user_id IS NOT DISTINCT FROM sqlc.narg(origin_user_id)::uuid OR (conversations.kind='channel_agent' AND EXISTS(SELECT 1 FROM channel_invocations v WHERE v.conversation_id=conversations.id AND v.caller_user_id=sqlc.narg(origin_user_id)::uuid AND v.status IN ('active','waiting_input'))))
  AND conversations.archived_at IS NULL
FOR UPDATE OF conversations;

-- name: BumpConversationProjection :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE conversations
SET updated_at = statement_clock.now_at,
    version = conversations.version + 1
FROM statement_clock
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
RETURNING conversations.*;

-- name: SetDirectConversationFirstMessageTitle :exec
-- The caller holds the Conversation lock and performs the projection bump
-- after inserting the first user message. Explicit titles stay unchanged.
UPDATE conversations
SET title = sqlc.arg(title)::text
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.kind = 'direct'
  AND conversations.title IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM conversation_items
      WHERE conversation_items.conversation_id = conversations.id
        AND conversation_items.type = 'user_message'
        AND conversation_items.id <> sqlc.arg(message_id)::uuid
  );

-- name: SetDirectConversationTitle :execrows
-- The caller holds the Company, membership and Conversation locks and records
-- the title change with one subsequent Conversation projection bump.
UPDATE conversations
SET title = sqlc.arg(title)::text
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.kind = 'direct'
  AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
  AND conversations.version = sqlc.arg(expected_version)::bigint
  AND EXISTS (
      SELECT 1 FROM conversation_readers AS reader
      WHERE reader.company_id = conversations.company_id
        AND reader.conversation_id = conversations.id
        AND reader.user_id = sqlc.arg(actor_user_id)::uuid
  );

-- name: BumpConversationProjectionsBulk :many
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
), target_ids AS MATERIALIZED (
    SELECT DISTINCT unnest(sqlc.arg(conversation_ids)::uuid[]) AS conversation_id
)
UPDATE conversations
SET updated_at = statement_clock.now_at,
    version = conversations.version + 1
FROM statement_clock, target_ids
WHERE conversations.id = target_ids.conversation_id
  AND conversations.company_id = sqlc.arg(company_id)::uuid
RETURNING conversations.*;

-- These system-only bulk reads rebuild every list-visible Conversation summary
-- touched by a Run transition. They deliberately do not apply current-user
-- visibility predicates: the conversation row itself carries the kind,
-- creator, and task_id the event audience and payload need, while Task
-- Conversations fan out to the company scope. Keeping the projections as
-- fixed-count reads avoids an N+1 query path inside Run transactions.

-- name: ListRunConversationSummaryBasesBulk :many
-- Same counting vocabulary as the direct-conversation counters above:
-- queued_run_count is queued + deferred, active_run_count is dispatched +
-- waiting_local_directory + running.
SELECT
    conversations.id AS conversation_id,
    conversations.updated_at,
    count(runs.id) FILTER (
        WHERE runs.status IN ('queued', 'deferred')
    )::bigint AS queued_run_count,
    count(runs.id) FILTER (
        WHERE runs.status IN ('dispatched', 'waiting_local_directory', 'running')
    )::bigint AS active_run_count
FROM conversations
-- Same status set in the JOIN as the direct-conversation counters above.
LEFT JOIN runs
  ON runs.conversation_id = conversations.id
 AND runs.status IN ('queued', 'deferred', 'dispatched', 'waiting_local_directory', 'running')
WHERE conversations.id = ANY(sqlc.arg(conversation_ids)::uuid[])
  AND conversations.company_id = sqlc.arg(company_id)::uuid
GROUP BY conversations.id
ORDER BY conversations.id;

-- name: ListRunConversationLatestMessagesBulk :many
SELECT DISTINCT ON (conversation_items.conversation_id)
    conversation_items.conversation_id,
    conversation_items.seq,
    conversation_items.created_at,
    conversation_items.type,
    conversation_items.author_kind,
    -- The preview line, matching the other two projections of this same wire
    -- type: a run transition publishes the summary clients cache-write, so a
    -- summary without its preview blanks the row the list just rendered. The
    -- type predicate is the timeline's, for the same reason as above.
    left(conversation_items.content, 200) AS content_preview,
    length(conversation_items.content) > 200 AS content_truncated
FROM conversation_items
JOIN conversations ON conversations.id = conversation_items.conversation_id
WHERE conversation_items.conversation_id = ANY(sqlc.arg(conversation_ids)::uuid[])
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversation_items.type IN ('user_message', 'agent_text')
ORDER BY conversation_items.conversation_id, conversation_items.seq DESC;

-- name: ListRunConversationLatestRunsBulk :many
SELECT DISTINCT ON (runs.conversation_id)
    runs.conversation_id,
    runs.id AS run_id,
    runs.agent_id,
    runs.status,
    runs.created_at,
    runs.failure_reason,
    runs.attempt
FROM runs
JOIN conversations ON conversations.id = runs.conversation_id
WHERE runs.conversation_id = ANY(sqlc.arg(conversation_ids)::uuid[])
  AND conversations.company_id = sqlc.arg(company_id)::uuid
ORDER BY runs.conversation_id, runs.created_at DESC, runs.id DESC;

-- name: GetConversationListSummaryBase :one
-- Same counting vocabulary as the bulk counters: queued_run_count is
-- queued + deferred, active_run_count is dispatched +
-- waiting_local_directory + running.
SELECT
    conversations.updated_at,
    COALESCE(run_counts.queued_count, 0)::bigint AS queued_run_count,
    COALESCE(run_counts.active_count, 0)::bigint AS active_run_count
FROM conversations
JOIN companies ON companies.id = conversations.company_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
LEFT JOIN LATERAL (
    SELECT
        count(*) FILTER (
            WHERE runs.status IN ('queued', 'deferred')
        )::bigint AS queued_count,
        count(*) FILTER (
            WHERE runs.status IN ('dispatched', 'waiting_local_directory', 'running')
        )::bigint AS active_count
    FROM runs
    WHERE runs.conversation_id = conversations.id
      -- Same status set as the bulk counters: it is what keeps this an
      -- index-only count over live runs instead of the whole history.
      AND runs.status IN ('queued', 'deferred', 'dispatched', 'waiting_local_directory', 'running')
) AS run_counts ON true
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL
  AND (
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind IN ('direct','schedule') AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
          AND EXISTS (SELECT 1 FROM conversation_readers reader WHERE reader.conversation_id=conversations.id AND reader.user_id=sqlc.arg(actor_user_id)::uuid))
  );

-- name: GetLatestConversationMessageForSummary :one
SELECT
    conversation_items.seq,
    conversation_items.created_at,
    conversation_items.type,
    conversation_items.author_kind,
    -- The preview line, matching the bulk projection: the row is already read
    -- from the heap, so truncating here keeps a long body off a list row.
    left(conversation_items.content, 200) AS content_preview,
    length(conversation_items.content) > 200 AS content_truncated
FROM conversation_items
JOIN conversations ON conversations.id = conversation_items.conversation_id
JOIN companies ON companies.id = conversations.company_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
WHERE conversation_items.conversation_id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL
  AND (
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind IN ('direct','schedule') AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
          AND EXISTS (SELECT 1 FROM conversation_readers reader WHERE reader.conversation_id=conversations.id AND reader.user_id=sqlc.arg(actor_user_id)::uuid))
  )
  AND conversation_items.type IN ('user_message', 'agent_text')
ORDER BY conversation_items.seq DESC
LIMIT 1;

-- name: GetLatestConversationRunForSummary :one
SELECT
    runs.id AS run_id,
    runs.agent_id,
    runs.status,
    runs.created_at,
    runs.failure_reason,
    runs.attempt
FROM runs
JOIN conversations ON conversations.id = runs.conversation_id
JOIN companies ON companies.id = conversations.company_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
WHERE runs.conversation_id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL
  AND (
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind IN ('direct','schedule') AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
          AND EXISTS (SELECT 1 FROM conversation_readers reader WHERE reader.conversation_id=conversations.id AND reader.user_id=sqlc.arg(actor_user_id)::uuid))
  )
ORDER BY runs.created_at DESC, runs.id DESC
LIMIT 1;

-- Creator pin/archive state lives on the direct conversation row. There is no
-- read watermark: the Inbox is what tells a member something happened, and it
-- carries its own per-recipient read state.

-- name: GetDirectConversationUserState :one
SELECT
    conversations.id AS conversation_id,
    conversations.pinned_at,
    conversations.archived_at,
    conversations.updated_at,
    conversations.version
FROM conversations
JOIN companies ON companies.id = conversations.company_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.kind = 'direct'
  AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
  AND companies.deleting_at IS NULL;

-- name: UpdateDirectConversationUserState :one
-- Optimistic against the conversation revision. Pin and archive are
-- idempotent toggles, stamped once.
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE conversations
SET pinned_at = CASE
        WHEN sqlc.arg(pinned)::boolean
            THEN COALESCE(conversations.pinned_at, statement_clock.now_at)
        ELSE NULL
    END,
    archived_at = CASE
        WHEN sqlc.arg(archived)::boolean
            THEN COALESCE(conversations.archived_at, statement_clock.now_at)
        ELSE NULL
    END,
    -- Filing a legacy default chat turns it into an independent session so
    -- restoring it can coexist with a newer default chat for the same Agent.
    request_key = CASE
        WHEN conversations.request_key IS NULL
          AND (sqlc.arg(archived)::boolean OR conversations.archived_at IS NOT NULL)
            THEN gen_random_uuid()
        ELSE conversations.request_key
    END,
    updated_at = statement_clock.now_at,
    version = conversations.version + 1
FROM companies, company_members AS actor_membership, statement_clock
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.kind = 'direct'
  AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
  AND conversations.version = sqlc.arg(expected_version)::bigint
  AND companies.id = conversations.company_id
  AND companies.deleting_at IS NULL
  AND actor_membership.company_id = conversations.company_id
  AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
  AND actor_membership.status = 'active'
  AND (
      sqlc.arg(pinned)::boolean <> (conversations.pinned_at IS NOT NULL)
      OR sqlc.arg(archived)::boolean <> (conversations.archived_at IS NOT NULL)
  )
RETURNING conversations.*;

-- name: GetDirectConversationByRequestKey :one
SELECT conversations.* FROM conversations
JOIN companies ON companies.id = conversations.company_id
JOIN company_members ON company_members.company_id = conversations.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
WHERE conversations.company_id = sqlc.arg(company_id)::uuid
 AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
 AND conversations.request_key = sqlc.arg(request_key)::uuid
 AND conversations.kind = 'direct' AND conversations.document_thread_id IS NULL
 AND companies.deleting_at IS NULL;

-- A person's pin on a Task chat exists only while they read the chat's
-- Project (ADR 0017): conversation_readers is the whole visibility rule.
-- name: GetTaskConversationUserState :one
-- The reader check is the correlated conversation_readers lookup (ADR 0017).
SELECT state.* FROM conversation_user_states AS state
JOIN conversations ON conversations.id=state.conversation_id AND conversations.company_id=state.company_id
WHERE state.company_id=sqlc.arg(company_id)::uuid AND state.conversation_id=sqlc.arg(conversation_id)::uuid
 AND state.user_id=sqlc.arg(actor_user_id)::uuid AND conversations.kind='task'
 AND EXISTS (
  SELECT 1 FROM conversation_readers AS reader
  WHERE reader.company_id=conversations.company_id AND reader.conversation_id=conversations.id
   AND reader.user_id=sqlc.arg(actor_user_id)::uuid
 );

-- name: EnsureTaskConversationUserState :exec
INSERT INTO conversation_user_states(company_id,conversation_id,user_id)
SELECT conversations.company_id,conversations.id,sqlc.arg(actor_user_id)::uuid
FROM conversations
WHERE conversations.company_id=sqlc.arg(company_id)::uuid AND conversations.id=sqlc.arg(conversation_id)::uuid
 AND conversations.kind='task'
 AND EXISTS (
  SELECT 1 FROM conversation_readers AS reader
  WHERE reader.company_id=conversations.company_id AND reader.conversation_id=conversations.id
   AND reader.user_id=sqlc.arg(actor_user_id)::uuid
 )
ON CONFLICT DO NOTHING;

-- name: UpdateTaskConversationUserState :one
WITH statement_clock AS MATERIALIZED (SELECT clock_timestamp() AS now_at)
UPDATE conversation_user_states AS state
SET pinned_at=CASE WHEN sqlc.arg(pinned)::boolean THEN COALESCE(state.pinned_at,statement_clock.now_at) ELSE NULL END,
 archived_at=CASE WHEN sqlc.arg(archived)::boolean THEN COALESCE(state.archived_at,statement_clock.now_at) ELSE NULL END,
 updated_at=statement_clock.now_at,version=state.version+1
FROM statement_clock
WHERE state.company_id=sqlc.arg(company_id)::uuid AND state.conversation_id=sqlc.arg(conversation_id)::uuid
 AND state.user_id=sqlc.arg(actor_user_id)::uuid AND state.version=sqlc.arg(expected_version)::bigint
 AND state.version<9007199254740991
RETURNING state.*;

-- name: ListPinnedConversationStatesForActiveMember :many
-- The person's own pinned direct chats, and the Task chats they pinned and
-- still read: each pinned Task chat is checked through a correlated
-- conversation_readers lookup (ADR 0017), so an unpinned or unreadable one
-- costs nothing.
WITH direct_pins AS (
 SELECT conversations.id AS conversation_id,conversations.company_id,conversations.created_by_user_id AS user_id,
  conversations.pinned_at,conversations.updated_at,conversations.version
 FROM conversations
 JOIN companies ON companies.id=conversations.company_id AND companies.deleting_at IS NULL
 JOIN company_members ON company_members.company_id=conversations.company_id
  AND company_members.user_id=sqlc.arg(actor_user_id)::uuid AND company_members.status='active'
 WHERE conversations.company_id=sqlc.arg(company_id)::uuid
  AND conversations.kind='direct' AND conversations.created_by_user_id=sqlc.arg(actor_user_id)::uuid
  AND conversations.archived_at IS NULL AND conversations.pinned_at IS NOT NULL
), task_pins AS (
 SELECT state.conversation_id,state.company_id,state.user_id,state.pinned_at,state.updated_at,state.version
 FROM conversation_user_states AS state
 JOIN conversations ON conversations.id=state.conversation_id AND conversations.company_id=state.company_id
 CROSS JOIN LATERAL (
  SELECT 1 FROM conversation_readers AS reader
  WHERE reader.company_id=state.company_id AND reader.conversation_id=state.conversation_id
   AND reader.user_id=sqlc.arg(actor_user_id)::uuid
  LIMIT 1
 ) AS readable
 WHERE state.company_id=sqlc.arg(company_id)::uuid AND state.user_id=sqlc.arg(actor_user_id)::uuid
  AND state.pinned_at IS NOT NULL AND conversations.kind='task' AND state.archived_at IS NULL
)
SELECT * FROM direct_pins
UNION ALL
SELECT * FROM task_pins
ORDER BY pinned_at DESC,conversation_id;

-- name: LockCompanyPlanTaskConversations :many
-- A Company Plan apply's Task chats (ADR 0017): the chat of every Task in
-- the Projects in the apply's scope that the applier reads, row-locked in id
-- order before any Task row, which keeps the Conversation -> Run -> Task
-- order of the terminal paths. The caller holds the dependency-graph lock,
-- so the set of these chats cannot change underneath it.
SELECT conversations.id
FROM conversations
JOIN tasks
  ON tasks.company_id = conversations.company_id
 AND tasks.conversation_id = conversations.id
JOIN project_access AS access
  ON access.company_id = tasks.company_id
 AND access.project_id = tasks.project_id
 AND access.user_id = sqlc.arg(actor_user_id)::uuid
WHERE conversations.company_id = sqlc.arg(company_id)::uuid
  AND tasks.project_id = ANY(sqlc.arg(project_ids)::uuid[])
ORDER BY conversations.id
FOR UPDATE OF conversations;

-- name: RenameCompanyPlanTaskConversation :execrows
-- A Company Plan apply renames a Task's chat with its Task (ADR 0017): the
-- applier must hold fence_permission (project.tasks.write) on the chat's
-- Project in this statement. The caller holds the chat's lock.
UPDATE conversations
SET title = sqlc.arg(title)::text,
    updated_at = clock_timestamp(),
    version = conversations.version + 1
WHERE conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.kind = 'task'
  AND project_permission_held(
      conversations.company_id,
      conversations.project_id,
      sqlc.arg(fence_user_id)::uuid,
      sqlc.arg(fence_permission)::text,
      sqlc.arg(company_default_permissions)::text[],
      sqlc.arg(management_permissions)::text[],
      sqlc.arg(management_roles)::text[]
  );

-- name: ListConversationStatesForActiveMember :many
-- The person's own pinned or archived direct chats, and the Task chats they
-- pinned or archived and still read: each Task chat state is checked through
-- a correlated conversation_readers lookup (ADR 0017), so a state left on a
-- chat the person no longer reads names nothing.
WITH direct_states AS (
    SELECT conversations.id AS conversation_id, conversations.company_id,
        conversations.created_by_user_id AS user_id,
        conversations.pinned_at, conversations.archived_at, conversations.updated_at, conversations.version
    FROM conversations
    JOIN companies ON companies.id = conversations.company_id AND companies.deleting_at IS NULL
    JOIN company_members ON company_members.company_id = conversations.company_id
        AND company_members.user_id = sqlc.arg(actor_user_id)::uuid AND company_members.status = 'active'
    WHERE conversations.company_id = sqlc.arg(company_id)::uuid
        AND conversations.kind = 'direct'
        AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
        AND (conversations.pinned_at IS NOT NULL OR conversations.archived_at IS NOT NULL)
), task_states AS (
    SELECT state.conversation_id, state.company_id, state.user_id,
        state.pinned_at, state.archived_at, state.updated_at, state.version
    FROM conversation_user_states AS state
    JOIN conversations ON conversations.id = state.conversation_id AND conversations.company_id = state.company_id
    CROSS JOIN LATERAL (
        SELECT 1 FROM conversation_readers AS reader
        WHERE reader.company_id = state.company_id AND reader.conversation_id = state.conversation_id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
        LIMIT 1
    ) AS readable
    WHERE state.company_id = sqlc.arg(company_id)::uuid AND state.user_id = sqlc.arg(actor_user_id)::uuid
        AND conversations.kind = 'task'
        AND (state.pinned_at IS NOT NULL OR state.archived_at IS NOT NULL)
)
SELECT * FROM direct_states
UNION ALL
SELECT * FROM task_states
ORDER BY updated_at DESC, conversation_id;

-- name: DetachDeletedTaskConversation :one
UPDATE conversations SET task_id = NULL, version = version + 1, updated_at = clock_timestamp()
WHERE company_id = sqlc.arg(company_id)::uuid AND id = sqlc.arg(conversation_id)::uuid
  AND task_id = sqlc.arg(task_id)::uuid RETURNING *;

-- name: LockStudioProjectRun :one
-- Company, membership and Conversation are already locked by the caller.
SELECT r.id FROM runs r
JOIN runtimes rt ON rt.id = r.runtime_id
JOIN run_tokens token ON token.run_id = r.id
JOIN conversations c ON c.id = r.conversation_id
WHERE r.id = sqlc.arg(run_id) AND r.company_id = sqlc.arg(company_id)
  AND r.runtime_id = sqlc.arg(runtime_id)
  AND rt.owner_user_id = sqlc.arg(user_id) AND rt.daemon_id = sqlc.arg(daemon_id)
  AND token.token_hash = sqlc.arg(token_hash) AND token.expires_at > now()
  AND r.status = 'running' AND c.kind IN ('direct','task','channel_agent')
  AND r.studio_enabled
FOR UPDATE OF r;

-- name: GetConversationStudioProject :one
SELECT * FROM conversation_studio_projects
WHERE company_id = $1 AND conversation_id = $2;

-- name: ReserveConversationStudioProject :one
INSERT INTO conversation_studio_projects (company_id, conversation_id, title, description, api_origin)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (conversation_id) DO NOTHING
RETURNING *;

-- name: BindConversationStudioProject :one
UPDATE conversation_studio_projects SET project_id = sqlc.arg(project_id)
WHERE company_id = sqlc.arg(company_id) AND conversation_id = sqlc.arg(conversation_id)
  AND id = sqlc.arg(id) AND (project_id IS NULL OR project_id = sqlc.arg(project_id))
RETURNING *;

-- name: LockStudioEditorRuntime :one
SELECT * FROM runtimes WHERE id = sqlc.arg(runtime_id) AND company_id = sqlc.arg(company_id)
    AND owner_user_id = sqlc.arg(user_id) AND removed_at IS NULL
FOR UPDATE;

-- name: ReserveStudioReceipt :one
INSERT INTO conversation_studio_projects (
    company_id, conversation_id, title, description, api_origin, creation_protocol,
    creation_account_id, creation_namespace, creation_request_hash, created_by_user_id
) VALUES ($1,$2,$3,$4,$5,'receipt_v1',$6,$7,$8,$9)
RETURNING *;

-- name: SetStudioReceiptHash :one
-- The DB generated reservation id is part of the hash; filled in the same transaction.
UPDATE conversation_studio_projects SET creation_request_hash = $2 WHERE id = $1 RETURNING *;

-- name: FinalizeStudioReceipt :one
UPDATE conversation_studio_projects
SET project_id = sqlc.arg(project_id), binding_revision = binding_revision + 1
WHERE id = sqlc.arg(id) AND binding_revision = sqlc.arg(binding_revision)
    AND api_origin = sqlc.arg(api_origin) AND creation_account_id = sqlc.arg(creation_account_id)
    AND creation_request_hash = sqlc.arg(creation_request_hash) AND project_id IS NULL
RETURNING *;

-- name: GetStudioRunOrigin :one
SELECT COALESCE(context->>'studioApiOrigin', 'https://api-agent.ii.inc')::text AS api_origin
FROM runs WHERE id = $1 AND company_id = $2;

-- name: LockStudioBinding :one
SELECT * FROM conversation_studio_projects
WHERE company_id = $1 AND conversation_id = $2
FOR UPDATE;
