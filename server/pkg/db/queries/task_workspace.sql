-- name: GetTaskWorkspaceAnchor :one
-- The Task row, its exact Conversation, and the head of the conversation
-- stream are the anchor for every remaining workspace query in the caller's
-- one REPEATABLE READ transaction. The watermark spans every item, not only
-- the visible ones: seq is the ordering authority, so a page bounded by it must
-- not move when a Run writes items the chat does not render.
SELECT
    sqlc.embed(tasks),
    sqlc.embed(conversations),
    COALESCE((
        SELECT max(conversation_items.seq)
        FROM conversation_items
        WHERE conversation_items.conversation_id = conversations.id
    ), 0)::bigint AS through_seq
FROM tasks
JOIN conversations
  ON conversations.company_id = tasks.company_id
 AND conversations.id = tasks.conversation_id
-- The caller reads the Task's Project (ADR 0017); project_access also
-- asserts the active membership and a Company that is not being deleted.
JOIN project_access AS access
  ON access.company_id = tasks.company_id
 AND access.project_id = tasks.project_id
 AND access.user_id = sqlc.arg(actor_user_id)::uuid
WHERE tasks.id = sqlc.arg(task_id)::uuid
  AND tasks.company_id = sqlc.arg(company_id)::uuid
  AND (
      conversations.kind = 'task'
      OR conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
  );
