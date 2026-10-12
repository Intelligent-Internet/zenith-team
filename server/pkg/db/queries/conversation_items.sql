-- The one conversation stream. A user's words and everything the Agent
-- produced answering them are rows in conversation_items ordered by one
-- per-conversation counter; what a reader is shown is derived from `type`,
-- never from a stored flag.
--
--   chat timeline       = one Conversation, type IN ('user_message', 'agent_text')
--   provider transcript = one Conversation, every type except 'user_message',
--                         with each row naming its Run
--   workspace preview   = one Run's bounded tail of that transcript
--
-- The two overlap on 'agent_text', deliberately: the answer is the chat's
-- message and its Run's output, read twice rather than stored twice.
--
-- The user path allocates seq under the Conversation row lock; the execution
-- module appends the Agent's items under the same lock, so the chat page and
-- Conversation transcript can never disagree about what came first.

-- name: ResolveMessageTargetForActiveUser :one
SELECT
    conversations.company_id,
    conversations.id AS conversation_id,
    conversations.kind AS conversation_kind,
    tasks.id AS task_id,
    run_agents.id AS effective_agent_id,
    tasks.assignee_user_id,
    conversations.project_id,
    conversations.document_thread_id
FROM conversations
JOIN companies ON companies.id = conversations.company_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
LEFT JOIN tasks
  ON tasks.conversation_id = conversations.id
 AND tasks.company_id = conversations.company_id
LEFT JOIN task_run_agents ON task_run_agents.task_id = tasks.id
LEFT JOIN agents AS run_agents ON run_agents.id = COALESCE(task_run_agents.agent_id, conversations.agent_id)
 AND run_agents.company_id = conversations.company_id
WHERE sqlc.arg(target_kind)::text = 'conversation'
  AND conversations.id = sqlc.arg(target_id)::uuid
  AND (
      -- A Task chat is read by its Project's readers (ADR 0017).
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (
          conversations.kind = 'direct'
          AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
          AND EXISTS (SELECT 1 FROM conversation_readers reader
              WHERE reader.conversation_id=conversations.id
                AND reader.user_id=sqlc.arg(actor_user_id)::uuid)
      )
  )
  AND companies.deleting_at IS NULL
UNION ALL
SELECT
    tasks.company_id,
    tasks.conversation_id,
    conversations.kind AS conversation_kind,
    tasks.id AS task_id,
    run_agents.id AS effective_agent_id,
    tasks.assignee_user_id,
    conversations.project_id,
    conversations.document_thread_id
FROM tasks
JOIN conversations
  ON conversations.id = tasks.conversation_id
 AND conversations.company_id = tasks.company_id
 AND conversations.kind = 'task'
JOIN companies ON companies.id = tasks.company_id
-- A Task is read by its Project's readers (ADR 0017).
JOIN task_access AS access
  ON access.company_id = tasks.company_id
 AND access.task_id = tasks.id
 AND access.user_id = sqlc.arg(actor_user_id)::uuid
LEFT JOIN task_run_agents ON task_run_agents.task_id = tasks.id
LEFT JOIN agents AS run_agents ON run_agents.id = COALESCE(task_run_agents.agent_id, conversations.agent_id)
 AND run_agents.company_id = conversations.company_id
WHERE sqlc.arg(target_kind)::text = 'task'
  AND tasks.id = sqlc.arg(target_id)::uuid
  AND companies.deleting_at IS NULL
LIMIT 1;

-- name: GetRequestedUserMessageForReplay :one
-- Exact send replay, arbitrated by conversation_items_request_key_idx. The
-- type predicate is the whole visibility rule: only a 'user_message' can carry
-- a caller's request key.
SELECT conversation_items.*
FROM conversation_items
JOIN conversations ON conversations.id = conversation_items.conversation_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
WHERE conversation_items.request_key = sqlc.arg(request_key)::uuid
  AND conversation_items.conversation_id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversation_items.type = 'user_message'
  AND conversation_items.author_id = sqlc.arg(actor_user_id)::uuid
  AND (
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind IN ('direct','schedule') AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
  )
FOR UPDATE OF conversation_items;

-- name: GetMessageTriggeredRunForReplay :one
-- A user message and the Run that consumes it commit together, and the item
-- carries that run_id for the rest of its life. Task reassignment can change
-- the currently resolved Agent before a retry, so replay follows the item's own
-- run_id instead of filtering by the target's current Agent.
SELECT runs.*
FROM conversation_items
JOIN runs ON runs.id = conversation_items.run_id
WHERE conversation_items.id = sqlc.arg(message_id)::uuid
  AND conversation_items.conversation_id = sqlc.arg(conversation_id)::uuid
  AND conversation_items.company_id = sqlc.arg(company_id)::uuid;

-- name: InsertUserMessageAfterConversationLock :one
-- The caller must first acquire LockConversationForActiveUserMutation in the
-- same READ COMMITTED transaction. Keeping allocation in this later statement
-- gives a waiter a fresh snapshot after the previous writer commits. This is
-- the internal prompt seam: both id and request_key remain database-owned/NULL.
--
-- run_id is supplied, not derived: the Run row is inserted first in the same
-- transaction, because runs.id is a database-generated UUID that needs nothing from the item.
WITH authorized_conversation AS MATERIALIZED (
    SELECT conversations.id, conversations.company_id, actor_membership.user_id
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
          OR (conversations.kind IN ('direct','schedule') AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
      )
), next_item AS MATERIALIZED (
    -- A correlated scalar subquery, not a LEFT JOIN with GROUP BY. PostgreSQL
    -- rewrites a bare max() into an ORDER BY seq DESC LIMIT 1 against
    -- conversation_items_conversation_id_seq_key, but it bails out of that
    -- rewrite when the query has a GROUP BY or more than one FROM item — so the
    -- grouped shape scanned every prior item in the conversation on every
    -- insert. The ceiling guard stays in this same statement: it must be
    -- evaluated against the value actually being allocated.
    SELECT
        authorized_conversation.id AS conversation_id,
        authorized_conversation.company_id,
        authorized_conversation.user_id AS author_id,
        (COALESCE((
            SELECT max(conversation_items.seq)
            FROM conversation_items
            WHERE conversation_items.conversation_id = authorized_conversation.id
        ), 0) + 1)::bigint AS seq
    FROM authorized_conversation
    WHERE COALESCE((
        SELECT max(conversation_items.seq)
        FROM conversation_items
        WHERE conversation_items.conversation_id = authorized_conversation.id
    ), 0) < 9007199254740991
)
INSERT INTO conversation_items (
    company_id, conversation_id, run_id, seq, type, author_kind, author_id, content
)
SELECT
    next_item.company_id,
    next_item.conversation_id,
    sqlc.arg(run_id)::uuid,
    next_item.seq,
    'user_message',
    'user',
    next_item.author_id,
    sqlc.arg(content)::text
FROM next_item
RETURNING conversation_items.*;

-- name: InsertRequestedUserMessageAfterConversationLock :one
-- Public send path: PostgreSQL owns the item id while request_key owns
-- actor-scoped exact replay.
--
-- Posting into a Task chat needs project.tasks.write on its Project (ADR
-- 0017). The service checks it; this statement checks it again through
-- project_permission_held, so a revoke that lands after that check refuses
-- the insert. A legacy Task chat with no Project has no Task left to answer
-- and is not fenced here.
WITH authorized_conversation AS MATERIALIZED (
    SELECT conversations.id, conversations.company_id, actor_membership.user_id
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
          OR (conversations.kind IN ('direct','schedule') AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
      )
      AND (
          conversations.kind <> 'task'
          OR conversations.project_id IS NULL
          OR project_permission_held(
              conversations.company_id, conversations.project_id,
              sqlc.arg(actor_user_id)::uuid, sqlc.arg(write_permission)::text,
              sqlc.arg(company_default_permissions)::text[],
              sqlc.arg(management_permissions)::text[],
              sqlc.arg(management_roles)::text[]
          )
      )
), next_item AS MATERIALIZED (
    SELECT
        authorized_conversation.id AS conversation_id,
        authorized_conversation.company_id,
        authorized_conversation.user_id AS author_id,
        (COALESCE((
            SELECT max(conversation_items.seq)
            FROM conversation_items
            WHERE conversation_items.conversation_id = authorized_conversation.id
        ), 0) + 1)::bigint AS seq
    FROM authorized_conversation
    WHERE COALESCE((
        SELECT max(conversation_items.seq)
        FROM conversation_items
        WHERE conversation_items.conversation_id = authorized_conversation.id
    ), 0) < 9007199254740991
)
INSERT INTO conversation_items (
    company_id, conversation_id, run_id, seq, type, author_kind, author_id,
    request_key, content
)
SELECT
    next_item.company_id,
    next_item.conversation_id,
    sqlc.arg(run_id)::uuid,
    next_item.seq,
    'user_message',
    'user',
    next_item.author_id,
    sqlc.arg(request_key)::uuid,
    sqlc.arg(content)::text
FROM next_item
RETURNING conversation_items.*;

-- name: InsertReservedUserMessageAfterConversationLock :one
-- Queue promotion materializes the database-reserved item id and preserves the
-- original send request_key on the immutable item.
WITH authorized_conversation AS MATERIALIZED (
    SELECT conversations.id, conversations.company_id, actor_membership.user_id
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
          OR (conversations.kind IN ('direct','schedule') AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
      )
), next_item AS MATERIALIZED (
    SELECT
        authorized_conversation.id AS conversation_id,
        authorized_conversation.company_id,
        authorized_conversation.user_id AS author_id,
        (COALESCE((
            SELECT max(conversation_items.seq)
            FROM conversation_items
            WHERE conversation_items.conversation_id = authorized_conversation.id
        ), 0) + 1)::bigint AS seq
    FROM authorized_conversation
    WHERE COALESCE((
        SELECT max(conversation_items.seq)
        FROM conversation_items
        WHERE conversation_items.conversation_id = authorized_conversation.id
    ), 0) < 9007199254740991
)
INSERT INTO conversation_items (
    id, company_id, conversation_id, run_id, seq, type, author_kind, author_id,
    request_key, content
)
SELECT
    sqlc.arg(message_id)::uuid,
    next_item.company_id,
    next_item.conversation_id,
    sqlc.arg(run_id)::uuid,
    next_item.seq,
    'user_message',
    'user',
    next_item.author_id,
    sqlc.arg(request_key)::uuid,
    sqlc.arg(content)::text
FROM next_item
RETURNING conversation_items.*;

-- name: AppendRunItem :one
-- The one append for everything an Agent produces during a Run: its text, its
-- reasoning, its tool calls and results, and the error that ended the turn.
-- There is no second write and no terminal copy — what the run said is already
-- in the conversation the moment this returns.
--
-- The caller holds the Conversation row lock (LockConversationForSystemRunQueue
-- Mutation), which is what serializes seq allocation against the user path.
-- conversation_items_run_item_key_idx is the replay fence: re-appending the
-- same provider item raises rather than showing the answer twice, so the caller
-- probes GetRunItemByKey first.
--
-- The JSON-safe ceiling is refused here, in the statement that allocates the
-- value, exactly as the three user-message inserts refuse it: seq is a number a
-- browser parses, so every writer of the one counter has to stop at the same
-- place, and the database — not one of the two Go paths — is what owns that.
-- Past the ceiling this appends nothing and returns no row. The caller already
-- holds the Run row lock in the same transaction, so `FROM runs WHERE id = ...`
-- cannot be the empty side: no row back means the ceiling and nothing else.
WITH next_item AS MATERIALIZED (
    -- Correlated scalar subquery for the same reason as the user-path insert:
    -- a grouped max() cannot use the index-backward rewrite. The ceiling test
    -- repeats it rather than reading the allocated alias, because a WHERE
    -- cannot see a SELECT-list alias of its own query level.
    SELECT
        runs.company_id,
        runs.conversation_id,
        runs.agent_id,
        (COALESCE((
            SELECT max(conversation_items.seq)
            FROM conversation_items
            WHERE conversation_items.conversation_id = runs.conversation_id
        ), 0) + 1)::bigint AS seq
    FROM runs
    WHERE runs.id = sqlc.arg(run_id)::uuid
      AND COALESCE((
          SELECT max(conversation_items.seq)
          FROM conversation_items
          WHERE conversation_items.conversation_id = runs.conversation_id
      ), 0) < 9007199254740991
)
INSERT INTO conversation_items (
    company_id, conversation_id, run_id, seq, type, author_kind, author_id,
    item_key, content, parts, content_sha256
)
SELECT
    next_item.company_id,
    next_item.conversation_id,
    sqlc.arg(run_id)::uuid,
    next_item.seq,
    sqlc.arg(type)::text,
    'agent',
    next_item.agent_id,
    sqlc.arg(item_key)::text,
    sqlc.arg(content)::text,
    sqlc.arg(parts)::jsonb,
    sqlc.arg(content_sha256)::bytea
FROM next_item
RETURNING conversation_items.*;

-- name: GetRunItemByKey :one
SELECT *
FROM conversation_items
WHERE run_id = sqlc.arg(run_id)::uuid
  AND item_key = sqlc.arg(item_key)::text;

-- name: GetFinalAgentTextItem :one
-- A terminal completion may be followed by tool or reasoning items, so the
-- selected final answer is the newest 'agent_text' item of the Run, not
-- necessarily the transcript head. The type test replaces the old scan through
-- a parts array looking for a text part: visibility is the type now.
SELECT candidate.*
FROM conversation_items AS candidate
WHERE candidate.run_id = sqlc.arg(run_id)::uuid
  AND candidate.item_key = sqlc.arg(item_key)::text
  AND candidate.type = 'agent_text'
  AND NOT EXISTS (
      SELECT 1
      FROM conversation_items AS later
      WHERE later.run_id = candidate.run_id
        AND later.seq > candidate.seq
        AND later.type = 'agent_text'
  );

-- name: ListRunItemTailsBulk :many
-- The newest N items for each of several Runs, in one round trip. The task
-- workspace embeds a timeline per Run referenced by the page it returns; asking
-- per Run is the N+1 this composite exists to remove.
--
-- A LATERAL over unnest keeps it one ordered index scan per Run against
-- conversation_items_run_seq_idx, rather than a window function over every item
-- of every Run in the set.
--
-- Same 'user_message' exclusion as ListConversationTranscriptItems, and a tail
-- is where it matters most: the Run's own words are its LOWEST seq, so
-- they fall inside the newest-N window precisely when the Run has said least —
-- a young Run spent one of its preview slots on a row nothing renders, and then
-- reported more to come because the discarded row made the count overflow.
SELECT tail.*
FROM unnest(sqlc.arg(run_ids)::uuid[]) AS requested(run_id)
JOIN LATERAL (
    SELECT conversation_items.*
    FROM conversation_items
    WHERE conversation_items.run_id = requested.run_id
      AND conversation_items.type <> 'user_message'
    ORDER BY conversation_items.seq DESC
    LIMIT sqlc.arg(per_run_limit)::integer
) AS tail ON TRUE
ORDER BY tail.run_id, tail.seq ASC;

-- name: GetRunTriggerMessageContent :one
-- The user's words a Run was started on: the Run's own first 'user_message'
-- item, reached through conversation_items_run_seq_idx. The company and
-- conversation stay in the predicate because that scoping is what makes
-- ErrNoRows here mean "out of scope" rather than merely "missing" — the same
-- guarantee the retired composite foreign key on runs.trigger_message_id gave.
SELECT conversation_items.content
FROM conversation_items
WHERE conversation_items.run_id = sqlc.arg(run_id)::uuid
  AND conversation_items.company_id = sqlc.arg(company_id)::uuid
  AND conversation_items.conversation_id = sqlc.arg(conversation_id)::uuid
  AND conversation_items.type = 'user_message'
ORDER BY conversation_items.seq
LIMIT 1;

-- name: LinkMessageAttachment :one
WITH locked_attachment AS MATERIALIZED (
    SELECT attachments.id, attachments.company_id
    FROM attachments
    WHERE attachments.id = sqlc.arg(attachment_id)::uuid
      AND attachments.company_id = sqlc.arg(company_id)::uuid
      AND attachments.uploaded_by_user_id = sqlc.arg(actor_user_id)::uuid
      AND attachments.status = 'ready'
      AND NOT EXISTS (
          SELECT 1
          FROM message_attachments
          WHERE message_attachments.attachment_id = attachments.id
      )
      AND NOT EXISTS (
          SELECT 1
          FROM conversation_run_queue_attachments
          WHERE conversation_run_queue_attachments.attachment_id = attachments.id
            AND conversation_run_queue_attachments.released_at IS NULL
      )
    FOR UPDATE OF attachments
)
INSERT INTO message_attachments (company_id, message_id, attachment_id, "position")
SELECT
    locked_attachment.company_id,
    conversation_items.id,
    locked_attachment.id,
    sqlc.arg(position)::integer
FROM locked_attachment
JOIN conversation_items
  ON conversation_items.id = sqlc.arg(message_id)::uuid
 AND conversation_items.company_id = locked_attachment.company_id
 AND conversation_items.type = 'user_message'
 AND conversation_items.author_id = sqlc.arg(actor_user_id)::uuid
RETURNING message_attachments.*;

-- name: GetConversationMessageWatermark :one
-- The watermark is the whole stream's head, not the timeline's: seq is the
-- ordering authority, and a page bounded by it must not move when a Run writes
-- items the chat does not render.
--
-- Correlated scalar subquery rather than a grouped max(): this runs on every
-- list read, and the grouped shape scanned the whole conversation.
SELECT COALESCE((
    SELECT max(conversation_items.seq)
    FROM conversation_items
    WHERE conversation_items.conversation_id = conversations.id
), 0)::bigint AS through_seq
FROM conversations
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND (
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind IN ('direct','schedule') AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
  );

-- name: GetConversationTranscriptWatermark :one
-- The newest durable provider item in one authorized Conversation. The user's
-- own words have no provider parts and therefore do not advance this surface's
-- cursor. A correlated max reaches the unique (conversation_id, seq) index
-- from its newest end instead of grouping the full stream.
SELECT COALESCE((
    SELECT max(conversation_items.seq)
    FROM conversation_items
    WHERE conversation_items.conversation_id = conversations.id
      AND conversation_items.type <> 'user_message'
), 0)::bigint AS through_seq
FROM conversations
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND (
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind IN ('direct','schedule') AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
  );

-- name: ListConversationTranscriptItems :many
-- All durable provider items in one authorized Conversation, regardless of
-- which Run produced them. seq is allocated under the Conversation lock and is
-- therefore the ordering and reconnect authority; run_id and created_at are
-- returned attributes, not a compound cursor.
SELECT conversation_items.*
FROM conversation_items
JOIN conversations ON conversations.id = conversation_items.conversation_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
WHERE conversation_items.conversation_id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND (
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind IN ('direct','schedule') AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
  )
  AND conversation_items.type <> 'user_message'
  AND conversation_items.seq > sqlc.arg(after_seq)::bigint
  AND conversation_items.seq <= sqlc.arg(through_seq)::bigint
ORDER BY conversation_items.seq
LIMIT sqlc.arg(page_size)::integer;

-- name: GetAuthorizedMessageContextTarget :one
SELECT
    target_items.seq AS target_seq,
    COALESCE(max(conversation_stream.seq), 0)::bigint AS through_seq,
    conversations.company_id
FROM conversation_items AS target_items
JOIN conversations ON conversations.id = target_items.conversation_id
JOIN companies
  ON companies.id = conversations.company_id
 AND companies.deleting_at IS NULL
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
LEFT JOIN conversation_items AS conversation_stream
  ON conversation_stream.conversation_id = conversations.id
WHERE target_items.id = sqlc.arg(message_id)::uuid
  AND target_items.conversation_id = sqlc.arg(conversation_id)::uuid
  AND (
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind IN ('direct','schedule') AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
  )
GROUP BY target_items.seq, conversations.company_id;

-- name: ListConversationMessages :many
-- The chat page from a cursor: the two visible types, in seq order, served by
-- conversation_items_timeline_idx. Everything else a Run produced is reachable
-- through ListConversationTranscriptItems; ListRunItemTailsBulk keeps the
-- bounded per-Run previews embedded in a Task workspace. The surfaces are not
-- disjoint and are not meant to be: an 'agent_text' row is both this
-- Conversation's message and one provider transcript item, read twice rather
-- than stored twice. The chat drops working items; transcript reads drop the
-- 'user_message' that started each Run.
SELECT conversation_items.*
FROM conversation_items
JOIN conversations ON conversations.id = conversation_items.conversation_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
WHERE conversation_items.conversation_id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND (
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind IN ('direct','schedule') AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
  )
  AND conversation_items.type IN ('user_message', 'agent_text')
  AND conversation_items.seq > sqlc.arg(after_seq)::bigint
  AND conversation_items.seq <= sqlc.arg(through_seq)::bigint
ORDER BY conversation_items.seq
LIMIT sqlc.arg(page_size)::integer;

-- name: ListConversationMessagesBackward :many
-- The page a chat panel opens on: the newest page_size visible items at or
-- below through_seq, newest first, for the caller to reverse into display
-- order.
--
-- It walks conversation_items_timeline_idx backwards from the bound instead of
-- forwards from a start seq because a page of chat is a COUNT of messages while
-- seq counts the whole stream. One busy Run puts dozens of reasoning, tool_call
-- and tool_result items between two messages, so no seq range chosen in advance
-- reliably holds page_size of them — least of all the obvious one, which is why
-- (through_seq - page_size, through_seq] can select nothing at all.
SELECT conversation_items.*
FROM conversation_items
JOIN conversations ON conversations.id = conversation_items.conversation_id
JOIN company_members AS actor_membership
  ON actor_membership.company_id = conversations.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
WHERE conversation_items.conversation_id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND (
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind IN ('direct','schedule') AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
  )
  AND conversation_items.type IN ('user_message', 'agent_text')
  AND conversation_items.seq <= sqlc.arg(through_seq)::bigint
ORDER BY conversation_items.seq DESC
LIMIT sqlc.arg(page_size)::integer;

-- name: GetMessageActorRef :one
SELECT
    conversation_items.author_kind,
    conversation_items.author_id,
    COALESCE(author_users.display_name, author_agents.name, 'Unknown')::text
        AS display_name,
    author_users.avatar_url
FROM conversation_items
LEFT JOIN company_members AS author_membership
  ON conversation_items.author_kind = 'user'
 AND author_membership.company_id = conversation_items.company_id
 AND author_membership.user_id = conversation_items.author_id
LEFT JOIN users AS author_users
  ON author_users.id = author_membership.user_id
LEFT JOIN agents AS author_agents
  ON conversation_items.author_kind = 'agent'
 AND author_agents.id = conversation_items.author_id
 AND author_agents.company_id = conversation_items.company_id
WHERE conversation_items.id = sqlc.arg(message_id)::uuid
  AND conversation_items.company_id = sqlc.arg(company_id)::uuid;

-- name: ListMessageActorRefsBulk :many
SELECT
    conversation_items.id AS message_id,
    conversation_items.author_kind,
    conversation_items.author_id,
    COALESCE(author_users.display_name, author_agents.name, 'Unknown')::text
        AS display_name,
    author_users.avatar_url
FROM conversation_items
LEFT JOIN company_members AS author_membership
  ON conversation_items.author_kind = 'user'
 AND author_membership.company_id = conversation_items.company_id
 AND author_membership.user_id = conversation_items.author_id
LEFT JOIN users AS author_users
  ON author_users.id = author_membership.user_id
LEFT JOIN agents AS author_agents
  ON conversation_items.author_kind = 'agent'
 AND author_agents.id = conversation_items.author_id
 AND author_agents.company_id = conversation_items.company_id
WHERE conversation_items.id = ANY(sqlc.arg(message_ids)::uuid[])
  AND conversation_items.company_id = sqlc.arg(company_id)::uuid
ORDER BY conversation_items.id;

-- name: GetConversationContextSnapshotHead :one
-- The watermark a Run's context snapshot freezes as throughSeq: the whole
-- stream's newest seq, every item type, so it names the last item before the
-- trigger rather than a chat count. The correlated max keeps the ORDER BY seq
-- DESC LIMIT 1 rewrite against conversation_items_conversation_id_seq_key (see
-- InsertUserMessageAfterConversationLock). The caller holds the Conversation
-- lock, so nothing is allocated between this read and the Run row.
SELECT conversations.kind,
       COALESCE((
           SELECT max(conversation_items.seq)
           FROM conversation_items
           WHERE conversation_items.conversation_id = conversations.id
       ), 0)::bigint AS through_seq
FROM conversations
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.kind IN ('task','direct','schedule','channel_agent');

-- name: ListChatItemsForContextSnapshot :many
-- The newest chat items for a Run's context snapshot, newest first so the
-- LIMIT drops the oldest; the producer reverses them. Author names come from
-- the same joins as ListMessageActorRefsBulk, so the agent reads the names the
-- chat shows. Chat-only by the timeline index predicate: provider detail never
-- reaches a snapshot.
SELECT conversation_items.id,
       conversation_items.seq,
       conversation_items.run_id,
       conversation_items.author_kind,
       conversation_items.author_id,
       COALESCE(author_users.display_name, author_agents.name, 'Unknown')::text
           AS display_name,
       conversation_items.content,
       conversation_items.created_at
FROM conversation_items
LEFT JOIN company_members AS author_membership
  ON conversation_items.author_kind = 'user'
 AND author_membership.company_id = conversation_items.company_id
 AND author_membership.user_id = conversation_items.author_id
LEFT JOIN users AS author_users
  ON author_users.id = author_membership.user_id
LEFT JOIN agents AS author_agents
  ON conversation_items.author_kind = 'agent'
 AND author_agents.id = conversation_items.author_id
 AND author_agents.company_id = conversation_items.company_id
WHERE conversation_items.conversation_id = sqlc.arg(conversation_id)::uuid
  AND conversation_items.company_id = sqlc.arg(company_id)::uuid
  AND EXISTS(SELECT 1 FROM conversations c WHERE c.id=conversation_items.conversation_id AND c.kind IN ('task','direct','schedule'))
  AND conversation_items.type IN ('user_message', 'agent_text')
ORDER BY conversation_items.seq DESC
LIMIT sqlc.arg(item_limit)::integer;

-- name: GetRunTriggerAuthor :one
-- The member whose words start a Run, frozen beside the prompt as
-- context.trigger.author. Membership proves the person belongs to the Company;
-- status is not checked because this is attribution, not authorization, and
-- the caller has already rechecked authority under its own locks. now() is the
-- enqueue transaction's timestamp, the same clock the Run row and the trigger
-- item take by default in this transaction.
SELECT users.display_name,
       now()::timestamptz AS enqueued_at
FROM company_members
JOIN users ON users.id = company_members.user_id
WHERE company_members.company_id = sqlc.arg(company_id)::uuid
  AND company_members.user_id = sqlc.arg(user_id)::uuid;

-- name: ListMessageAttachments :many
-- One message's files the person may open (attachment_access, row by row),
-- ready bytes only. A file they may not open is absent from the message
-- rather than refused on download: a Project file shows only to that
-- Project's readers (ADR 0017). One statement, so the list and the rows it
-- returns are read from one snapshot.
SELECT attachments.*
FROM message_attachments
JOIN attachments
  ON attachments.id = message_attachments.attachment_id
 AND attachments.company_id = message_attachments.company_id
 AND attachments.status = 'ready'
CROSS JOIN LATERAL (
    SELECT 1 FROM attachment_access AS access
    WHERE access.company_id = message_attachments.company_id
      AND access.attachment_id = message_attachments.attachment_id
      AND access.user_id = sqlc.arg(actor_user_id)::uuid
    LIMIT 1
) AS readable
WHERE message_attachments.message_id = sqlc.arg(message_id)::uuid
  AND message_attachments.company_id = sqlc.arg(company_id)::uuid
ORDER BY message_attachments.position;

-- name: MessageCarriesProjectFile :one
-- Whether any file on the message is an active Project file. Such a file
-- follows its Projects' readers, not the chat's audience, so a frame that
-- would carry the message's files to that audience carries none (ADR 0017).
SELECT EXISTS (
    SELECT 1
    FROM message_attachments AS link
    JOIN project_files AS file
      ON file.company_id = link.company_id
     AND file.attachment_id = link.attachment_id
     AND file.removed_at IS NULL
    WHERE link.company_id = sqlc.arg(company_id)::uuid
      AND link.message_id = sqlc.arg(message_id)::uuid
) AS carries_project_file;

-- name: ListMessagesWithWithheldAttachments :many
-- The messages of a page on which a ready file was left out for the person:
-- an active Project file of a Project they do not read (ADR 0017). The
-- message reads say so with attachmentsWithheld, so a client can show that
-- a file is there without naming it. One attachment_access probe per file,
-- as the file lists make. A deleted message never says a file was withheld:
-- that would tell a non-reader the removed message held a Project file, where
-- a plain chat file on it just stops showing.
SELECT DISTINCT link.message_id
FROM message_attachments AS link
JOIN attachments
  ON attachments.id = link.attachment_id
 AND attachments.company_id = link.company_id
 AND attachments.status = 'ready'
JOIN conversation_items AS item
  ON item.company_id = link.company_id
 AND item.id = link.message_id
 AND item.deleted_at IS NULL
WHERE link.message_id = ANY(sqlc.arg(message_ids)::uuid[])
  AND link.company_id = sqlc.arg(company_id)::uuid
  AND EXISTS (
      SELECT 1
      FROM project_files AS file
      WHERE file.company_id = link.company_id
        AND file.attachment_id = link.attachment_id
        AND file.removed_at IS NULL
  )
  AND (
      SELECT 1
      FROM attachment_access AS access
      WHERE access.company_id = link.company_id
        AND access.attachment_id = link.attachment_id
        AND access.user_id = sqlc.arg(actor_user_id)::uuid
      LIMIT 1
  ) IS NULL;

-- name: ListMessageAttachmentsBulk :many
-- A page of messages' files the person may open (attachment_access, row by
-- row), ready bytes only.
SELECT
    message_attachments.message_id,
    message_attachments.position,
    attachments.*
FROM message_attachments
JOIN attachments
  ON attachments.id = message_attachments.attachment_id
 AND attachments.company_id = message_attachments.company_id
 AND attachments.status = 'ready'
CROSS JOIN LATERAL (
    SELECT 1 FROM attachment_access AS access
    WHERE access.company_id = message_attachments.company_id
      AND access.attachment_id = message_attachments.attachment_id
      AND access.user_id = sqlc.arg(actor_user_id)::uuid
    LIMIT 1
) AS readable
WHERE message_attachments.message_id = ANY(sqlc.arg(message_ids)::uuid[])
  AND message_attachments.company_id = sqlc.arg(company_id)::uuid
ORDER BY message_attachments.message_id, message_attachments.position;
