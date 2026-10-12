-- name: InitializeConversationModelSettings :exec
-- Caller owns the Conversation lock (or just created it).
INSERT INTO conversation_model_settings (company_id, conversation_id, agent_id, model, thinking_level)
SELECT c.company_id, c.id, a.id, a.model, a.thinking_level
FROM conversations c
JOIN agents a ON a.company_id = c.company_id AND a.id = sqlc.arg(agent_id)::uuid
WHERE c.id = sqlc.arg(conversation_id)::uuid AND c.kind IN ('direct', 'task')
ON CONFLICT DO NOTHING;

-- name: GetConversationModelSettings :one
SELECT a.id AS agent_id, a.provider,
       COALESCE(CASE WHEN s.agent_id IS NULL THEN a.model ELSE s.model END, '')::text AS model,
       COALESCE(CASE WHEN s.agent_id IS NULL THEN a.thinking_level ELSE s.thinking_level END, '')::text AS thinking_level,
       COALESCE(s.revision, 1)::bigint AS revision
FROM agents a
LEFT JOIN conversation_model_settings s
  ON s.agent_id = a.id AND s.conversation_id = sqlc.arg(conversation_id)::uuid
WHERE a.id = sqlc.arg(agent_id)::uuid AND a.company_id = sqlc.arg(company_id)::uuid
  AND a.archived_at IS NULL;

-- name: UpdateConversationModelSettings :execrows
UPDATE conversation_model_settings
SET model = sqlc.narg(model)::text, thinking_level = sqlc.narg(thinking_level)::text,
    revision = revision + 1, updated_at = now()
WHERE conversation_id = sqlc.arg(conversation_id)::uuid
  AND agent_id = sqlc.arg(agent_id)::uuid AND revision < 9007199254740991;

-- name: InsertConversationModelChange :execrows
-- The caller owns the Conversation lock, shared with message sequence allocation.
INSERT INTO conversation_model_changes (
    company_id, conversation_id, agent_id, changed_by_user_id, previous_model, model, after_seq
) SELECT
    sqlc.arg(company_id)::uuid, sqlc.arg(conversation_id)::uuid, sqlc.arg(agent_id)::uuid,
    sqlc.arg(changed_by_user_id)::uuid, sqlc.narg(previous_model)::text, sqlc.narg(model)::text,
    COALESCE((SELECT max(seq) FROM conversation_items WHERE conversation_id = sqlc.arg(conversation_id)::uuid), 0)
WHERE EXISTS (
    SELECT 1 FROM conversation_readers
    WHERE company_id = sqlc.arg(company_id)::uuid
      AND conversation_id = sqlc.arg(conversation_id)::uuid
      AND user_id = sqlc.arg(changed_by_user_id)::uuid
);

-- name: ListConversationModelChanges :many
SELECT id, agent_id, previous_model, model, after_seq, created_at
FROM conversation_model_changes
WHERE company_id = sqlc.arg(company_id)::uuid AND conversation_id = sqlc.arg(conversation_id)::uuid
ORDER BY created_at, id;
