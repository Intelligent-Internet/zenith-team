-- The source Conversation lock serializes Start requests for one discussion.
-- Huddle identity and chat identity are returned together for one projection.

-- name: GetHuddleByRequestKey :one
SELECT h.*
FROM huddles h
WHERE h.company_id=sqlc.arg(company_id)::uuid
  AND h.started_by_user_id=sqlc.arg(started_by_user_id)::uuid
  AND h.request_key=sqlc.arg(request_key)::uuid;

-- name: GetActiveHuddleForSource :one
SELECT h.*
FROM huddles h
WHERE h.company_id=sqlc.arg(company_id)::uuid
  AND h.source_conversation_id=sqlc.arg(source_conversation_id)::uuid
  AND h.state IN ('starting','open','ending')
ORDER BY h.created_at DESC, h.id DESC
LIMIT 1;

-- name: InsertHuddle :one
INSERT INTO huddles(
    company_id,source_conversation_id,chat_conversation_id,started_by_user_id,
    request_key,deadline_at
)
VALUES(
    sqlc.arg(company_id)::uuid,sqlc.arg(source_conversation_id)::uuid,
    sqlc.arg(chat_conversation_id)::uuid,sqlc.arg(started_by_user_id)::uuid,
    sqlc.arg(request_key)::uuid,sqlc.arg(deadline_at)::timestamptz
)
RETURNING *;

-- name: GetHuddleWithChat :one
SELECT h.*, chat.kind AS chat_kind
FROM huddles h
JOIN conversations chat ON chat.company_id=h.company_id AND chat.id=h.chat_conversation_id
WHERE h.company_id=sqlc.arg(company_id)::uuid AND h.id=sqlc.arg(huddle_id)::uuid;

-- name: GetHuddle :one
SELECT * FROM huddles
WHERE company_id=sqlc.arg(company_id)::uuid AND id=sqlc.arg(huddle_id)::uuid;
