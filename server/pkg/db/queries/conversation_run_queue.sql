-- name: GetConversationCurrentRun :one
SELECT
    runs.id,
    runs.status,
    runs.created_at,
    runs.runtime_id,
    runtimes.owner_user_id AS runtime_owner_user_id,
    runs.wait_reason
FROM runs
JOIN runtimes ON runtimes.id = runs.runtime_id
WHERE runs.conversation_id = sqlc.arg(conversation_id)::uuid
  AND runs.company_id = sqlc.arg(company_id)::uuid
  AND runs.status IN ('queued', 'deferred', 'dispatched', 'waiting_local_directory', 'running')
ORDER BY runs.created_at, runs.id
LIMIT 1;

-- name: LockConversationForSystemRunQueueMutation :one
WITH parent_lock AS MATERIALIZED (
    SELECT parent.id FROM conversations child JOIN conversations parent ON parent.id=child.channel_id
    WHERE child.id=sqlc.arg(conversation_id)::uuid AND child.company_id=sqlc.arg(company_id)::uuid
    FOR NO KEY UPDATE OF parent
)
SELECT conversations.*
FROM conversations
WHERE (SELECT count(*) FROM parent_lock)>=0 AND conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
-- This is a writer mutex, not a key-change lock. Keeping KEY SHARE compatible
-- lets a concurrent Run update validate its Conversation foreign key instead
-- of queueing behind a terminal writer that is itself waiting for this mutex.
FOR NO KEY UPDATE OF conversations;

-- name: ListQueuedRunQueueConversationIDsForUser :many
-- The chats where the person has items queued, for a revocation to lock and
-- clean. @project_id narrows it to that Project's Task chats: a Project
-- access change removes only what the person may no longer post there
-- (ADR 0017). NULL means every chat in the Company.
SELECT DISTINCT conversation_run_queue_items.conversation_id
FROM conversation_run_queue_items
WHERE conversation_run_queue_items.company_id = sqlc.arg(company_id)::uuid
  AND conversation_run_queue_items.initiated_by_user_id = sqlc.arg(actor_user_id)::uuid
  AND conversation_run_queue_items.state = 'queued'
  AND (
      sqlc.narg(project_id)::uuid IS NULL
      OR conversation_run_queue_items.conversation_id IN (
          SELECT conversations.id FROM conversations
          WHERE conversations.company_id = sqlc.arg(company_id)::uuid
            AND conversations.kind = 'task'
            AND conversations.project_id = sqlc.narg(project_id)::uuid
      )
  )
ORDER BY conversation_run_queue_items.conversation_id;

-- name: RemoveQueuedRunQueueItemsForUser :many
-- @conversation_ids narrows the removal to chats the caller locked (a
-- Project access change); NULL removes the person's items everywhere.
WITH removal_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE conversation_run_queue_items
SET state = 'removed',
    removed_at = removal_clock.now_at,
    removal_reason = sqlc.arg(removal_reason)::text,
    updated_at = removal_clock.now_at,
    version = conversation_run_queue_items.version + 1
FROM removal_clock
WHERE conversation_run_queue_items.company_id = sqlc.arg(company_id)::uuid
  AND conversation_run_queue_items.initiated_by_user_id = sqlc.arg(actor_user_id)::uuid
  AND conversation_run_queue_items.state = 'queued'
  AND (
      sqlc.narg(conversation_ids)::uuid[] IS NULL
      OR conversation_run_queue_items.conversation_id = ANY(sqlc.narg(conversation_ids)::uuid[])
  )
RETURNING conversation_run_queue_items.id,
          conversation_run_queue_items.conversation_id,
          conversation_run_queue_items.message_id;

-- name: ReleaseRunQueueItemAttachmentsBulk :exec
WITH release_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE conversation_run_queue_attachments
SET released_at = release_clock.now_at,
    updated_at = release_clock.now_at
FROM release_clock
WHERE conversation_run_queue_attachments.queue_item_id = ANY(sqlc.arg(queue_item_ids)::uuid[])
  AND conversation_run_queue_attachments.released_at IS NULL;

-- name: GetActiveAgentForRunQueuePromotion :one
SELECT agents.*
FROM agents
WHERE agents.id = sqlc.arg(agent_id)::uuid
  AND agents.company_id = sqlc.arg(company_id)::uuid
  AND agents.archived_at IS NULL;

-- name: GetRunQueueItemReplayForUpdate :one
SELECT conversation_run_queue_items.*
FROM conversation_run_queue_items
WHERE conversation_run_queue_items.request_key = sqlc.arg(request_key)::uuid
  AND conversation_run_queue_items.conversation_id = sqlc.arg(conversation_id)::uuid
  AND conversation_run_queue_items.company_id = sqlc.arg(company_id)::uuid
  AND conversation_run_queue_items.initiated_by_user_id = sqlc.arg(actor_user_id)::uuid
FOR UPDATE OF conversation_run_queue_items;

-- name: CountQueuedRunQueueItems :one
SELECT count(*)::bigint
FROM conversation_run_queue_items
WHERE conversation_id = sqlc.arg(conversation_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND state = 'queued';

-- name: InsertRunQueueItem :one
WITH next_position AS MATERIALIZED (
    SELECT (COALESCE(max("position"), 0) + 1)::integer AS value
    FROM conversation_run_queue_items
    WHERE conversation_id = sqlc.arg(conversation_id)::uuid
      AND company_id = sqlc.arg(company_id)::uuid
      AND state = 'queued'
)
INSERT INTO conversation_run_queue_items (
    company_id, conversation_id, initiated_by_user_id, request_key,
    content, "position"
)
SELECT
    sqlc.arg(company_id)::uuid,
    sqlc.arg(conversation_id)::uuid,
    sqlc.arg(actor_user_id)::uuid,
    sqlc.arg(request_key)::uuid,
    sqlc.arg(content)::text,
    next_position.value
FROM next_position
RETURNING *;

-- name: ReserveRunQueueAttachment :one
WITH locked_attachment AS MATERIALIZED (
    SELECT attachments.id
    FROM attachments
    WHERE attachments.id = sqlc.arg(attachment_id)::uuid
      AND attachments.company_id = sqlc.arg(company_id)::uuid
      AND attachments.uploaded_by_user_id = sqlc.arg(actor_user_id)::uuid
      AND attachments.status = 'ready'
      AND NOT EXISTS (
          SELECT 1 FROM message_attachments
          WHERE message_attachments.attachment_id = attachments.id
      )
      AND NOT EXISTS (
          SELECT 1 FROM conversation_run_queue_attachments
          WHERE conversation_run_queue_attachments.attachment_id = attachments.id
            AND conversation_run_queue_attachments.released_at IS NULL
      )
    FOR UPDATE OF attachments
)
INSERT INTO conversation_run_queue_attachments (
    company_id, queue_item_id, attachment_id, "position"
)
SELECT
    conversation_run_queue_items.company_id,
    conversation_run_queue_items.id,
    locked_attachment.id,
    sqlc.arg(position)::integer
FROM locked_attachment
JOIN conversation_run_queue_items
  ON conversation_run_queue_items.id = sqlc.arg(queue_item_id)::uuid
 AND conversation_run_queue_items.state = 'queued'
RETURNING conversation_run_queue_attachments.*;

-- name: ListQueuedRunQueueItems :many
SELECT conversation_run_queue_items.*
FROM conversation_run_queue_items
WHERE conversation_id = sqlc.arg(conversation_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND state = 'queued'
ORDER BY "position", created_at, id
LIMIT 20;

-- name: ListRunQueueItemAttachments :many
-- Queued prompts' files as the viewer will see them once the prompt is
-- carried into a message (ADR 0017): the viewer already reads the chat, so a
-- plain upload shows; an active Project file shows only to the readers of its
-- Projects (attachment_access, one probe per row).
SELECT
    conversation_run_queue_attachments.queue_item_id,
    conversation_run_queue_attachments."position",
    attachments.*
FROM conversation_run_queue_attachments
JOIN conversation_run_queue_items
  ON conversation_run_queue_items.id = conversation_run_queue_attachments.queue_item_id
 AND conversation_run_queue_items.state = 'queued'
 AND conversation_run_queue_attachments.released_at IS NULL
JOIN attachments
  ON attachments.id = conversation_run_queue_attachments.attachment_id
 AND attachments.company_id = conversation_run_queue_attachments.company_id
WHERE conversation_run_queue_attachments.queue_item_id = ANY(sqlc.arg(queue_item_ids)::uuid[])
  AND (
      NOT EXISTS (
          SELECT 1 FROM project_files AS file
          WHERE file.company_id = attachments.company_id
            AND file.attachment_id = attachments.id
            AND file.removed_at IS NULL
      )
      OR COALESCE((
          SELECT true FROM attachment_access AS access
          WHERE access.company_id = attachments.company_id
            AND access.attachment_id = attachments.id
            AND access.user_id = sqlc.arg(viewer_user_id)::uuid
          LIMIT 1
      ), false)
  )
ORDER BY conversation_run_queue_attachments.queue_item_id,
         conversation_run_queue_attachments."position";

-- name: ListRunQueueItemInitiators :many
-- Who wrote each queued prompt, named the way a Message author is. A Task
-- queue holds colleagues' saved Messages beside the assignee's own (ADR
-- 0014), so the assignee sees whose words their next Run carries.
SELECT
    conversation_run_queue_items.id AS queue_item_id,
    conversation_run_queue_items.initiated_by_user_id,
    COALESCE(initiator_users.display_name, 'Unknown')::text AS display_name,
    initiator_users.avatar_url
FROM conversation_run_queue_items
LEFT JOIN company_members AS initiator_membership
  ON initiator_membership.company_id = conversation_run_queue_items.company_id
 AND initiator_membership.user_id = conversation_run_queue_items.initiated_by_user_id
LEFT JOIN users AS initiator_users
  ON initiator_users.id = initiator_membership.user_id
WHERE conversation_run_queue_items.id = ANY(sqlc.arg(queue_item_ids)::uuid[])
ORDER BY conversation_run_queue_items.id;

-- name: ListRunQueueAttachmentIDs :many
SELECT attachment_id
FROM conversation_run_queue_attachments
WHERE queue_item_id = sqlc.arg(queue_item_id)::uuid
ORDER BY "position";

-- name: LockQueuedRunQueueItems :many
SELECT conversation_run_queue_items.*
FROM conversation_run_queue_items
WHERE conversation_run_queue_items.conversation_id = sqlc.arg(conversation_id)::uuid
  AND conversation_run_queue_items.company_id = sqlc.arg(company_id)::uuid
  AND conversation_run_queue_items.state = 'queued'
ORDER BY conversation_run_queue_items."position", conversation_run_queue_items.id
FOR UPDATE OF conversation_run_queue_items;

-- name: ListRunQueueItemIDs :many
-- Every state, not only 'queued': a removed or materialized item keeps its
-- row, which is what lets a mutation tell an item this Conversation never
-- held (not found) from one that changed under the write (conflict).
SELECT id
FROM conversation_run_queue_items
WHERE conversation_id = sqlc.arg(conversation_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND id = ANY(sqlc.arg(ids)::uuid[]);

-- name: SetRunQueueItemPosition :one
UPDATE conversation_run_queue_items
SET "position" = sqlc.arg(position)::integer,
    updated_at = clock_timestamp(),
    version = conversation_run_queue_items.version + 1
WHERE conversation_run_queue_items.id = sqlc.arg(queue_item_id)::uuid
  AND conversation_run_queue_items.conversation_id = sqlc.arg(conversation_id)::uuid
  AND conversation_run_queue_items.company_id = sqlc.arg(company_id)::uuid
  AND conversation_run_queue_items.state = 'queued'
  AND conversation_run_queue_items.version = sqlc.arg(expected_version)::bigint
RETURNING conversation_run_queue_items.*;

-- name: RemoveRunQueueItem :one
WITH removal_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE conversation_run_queue_items
SET state = 'removed',
    removed_at = removal_clock.now_at,
    removal_reason = sqlc.arg(removal_reason)::text,
    updated_at = removal_clock.now_at,
    version = version + 1
FROM removal_clock
WHERE id = sqlc.arg(queue_item_id)::uuid
  AND conversation_id = sqlc.arg(conversation_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND state = 'queued'
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: ReleaseRunQueueItemAttachments :exec
WITH release_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE conversation_run_queue_attachments
SET released_at = release_clock.now_at,
    updated_at = release_clock.now_at
FROM release_clock
WHERE queue_item_id = sqlc.arg(queue_item_id)::uuid
  AND released_at IS NULL;

-- name: GetNextRunQueueItemForUpdate :one
-- The promotion head. A Task Conversation promotes only the assignee's own
-- deferred prompts (ADR 0014): a colleague's queued Message is skipped here,
-- never removed, and rides into the transcript with the assignee's next Run
-- (ListQueuedRunQueueItemsForTaskRun). A direct Conversation passes NULL and
-- promotes its head whoever wrote it.
SELECT conversation_run_queue_items.*
FROM conversation_run_queue_items
WHERE conversation_id = sqlc.arg(conversation_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND state = 'queued'
  AND (
      sqlc.narg(initiated_by_user_id)::uuid IS NULL
      OR initiated_by_user_id = sqlc.narg(initiated_by_user_id)::uuid
  )
ORDER BY "position", created_at, id
LIMIT 1
FOR UPDATE OF conversation_run_queue_items;

-- name: ListQueuedRunQueueItemsForTaskRun :many
-- The queued Messages a Task Run started by @authorized_by_user_id (the
-- assignee) carries into the transcript ahead of its trigger (ADR 0014):
-- every queued item written by someone else, in queue order, with the
-- author's display name for the frozen snapshot. The enqueue kernel freezes
-- exactly this list into the Run's conversationSnapshot and the Conversation
-- domain materializes it in the same transaction under the Conversation
-- lock, so the two must read one predicate. The assignee's own deferred
-- prompts are left for promotion: each becomes its own Run. Only the words
-- of someone who still reads the chat are carried, the same rule
-- InsertReservedUserMessageAfterConversationLock applies to the author
-- (ADR 0017): revocation removes a departed member's items and a Project
-- access change removes the items of someone who lost the Project, and an
-- item either missed is skipped here rather than failing the enqueue,
-- because its reserved Message could never be written.
SELECT sqlc.embed(conversation_run_queue_items),
       COALESCE(users.display_name, 'Unknown')::text AS display_name
FROM conversation_run_queue_items
JOIN company_members AS author_membership
  ON author_membership.company_id = conversation_run_queue_items.company_id
 AND author_membership.user_id = conversation_run_queue_items.initiated_by_user_id
 AND author_membership.status = 'active'
LEFT JOIN users ON users.id = conversation_run_queue_items.initiated_by_user_id
WHERE conversation_run_queue_items.conversation_id = sqlc.arg(conversation_id)::uuid
  AND conversation_run_queue_items.company_id = sqlc.arg(company_id)::uuid
  AND conversation_run_queue_items.state = 'queued'
  AND conversation_run_queue_items.initiated_by_user_id <> sqlc.arg(authorized_by_user_id)::uuid
  -- One scalar probe per item: the author differs per row.
  AND COALESCE((
      SELECT true FROM conversation_readers AS reader
      WHERE reader.company_id = conversation_run_queue_items.company_id
        AND reader.conversation_id = conversation_run_queue_items.conversation_id
        AND reader.user_id = conversation_run_queue_items.initiated_by_user_id
      LIMIT 1
  ), false)
ORDER BY conversation_run_queue_items."position",
         conversation_run_queue_items.created_at,
         conversation_run_queue_items.id
LIMIT 20;

-- name: RunQueueReservedIDsAvailable :one
-- Only the item id is reserved now. runs.id is a database-generated UUID the promotion
-- transaction takes by inserting the Run, so there is no run identity a second
-- promotion of the same queue head could collide with.
SELECT NOT EXISTS (
    SELECT 1 FROM conversation_items
    WHERE conversation_items.id = sqlc.arg(message_id)::uuid
) AS available;

-- name: MaterializeRunQueueItem :one
WITH materialization_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE conversation_run_queue_items
SET state = 'materialized',
    materialized_at = materialization_clock.now_at,
    updated_at = materialization_clock.now_at,
    version = version + 1
FROM materialization_clock
WHERE id = sqlc.arg(queue_item_id)::uuid
  AND state = 'queued'
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: MaterializeRunQueueAttachments :many
INSERT INTO message_attachments (company_id, message_id, attachment_id, "position")
SELECT
    conversation_run_queue_attachments.company_id,
    sqlc.arg(message_id)::uuid,
    conversation_run_queue_attachments.attachment_id,
    conversation_run_queue_attachments."position"
FROM conversation_run_queue_attachments
WHERE conversation_run_queue_attachments.queue_item_id = sqlc.arg(queue_item_id)::uuid
  AND conversation_run_queue_attachments.released_at IS NULL
ORDER BY conversation_run_queue_attachments."position"
RETURNING message_attachments.*;
