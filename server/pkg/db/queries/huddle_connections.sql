-- name: GetHuddleConnectionByRequestKey :one
SELECT * FROM huddle_connections
WHERE company_id=sqlc.arg(company_id)::uuid
  AND huddle_id=sqlc.arg(huddle_id)::uuid
  AND user_id=sqlc.arg(user_id)::uuid
  AND request_key=sqlc.arg(request_key)::uuid;

-- name: ListOpenHuddleConnections :many
SELECT hc.*, COALESCE(u.display_name, 'Former member')::text AS display_name
FROM huddle_connections AS hc
JOIN users AS u ON u.id = hc.user_id
WHERE hc.company_id=sqlc.arg(company_id)::uuid
  AND hc.huddle_id=sqlc.arg(huddle_id)::uuid
  AND hc.status IN ('reserved','connected')
ORDER BY hc.created_at ASC, hc.id ASC;

-- name: GetOpenHuddleConnection :one
SELECT * FROM huddle_connections
WHERE company_id=sqlc.arg(company_id)::uuid
  AND huddle_id=sqlc.arg(huddle_id)::uuid
  AND user_id=sqlc.arg(user_id)::uuid
  AND status IN ('reserved','connected')
ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: InsertHuddleConnection :one
INSERT INTO huddle_connections(company_id,huddle_id,user_id,request_key,reservation_expires_at)
VALUES(sqlc.arg(company_id)::uuid,sqlc.arg(huddle_id)::uuid,sqlc.arg(user_id)::uuid,sqlc.arg(request_key)::uuid,now()+interval '30 seconds')
RETURNING *;

-- name: GetHuddleConnectionForUpdate :one
SELECT * FROM huddle_connections
WHERE company_id=sqlc.arg(company_id)::uuid AND id=sqlc.arg(connection_id)::uuid
FOR UPDATE;

-- name: MarkHuddleConnectionConnected :one
UPDATE huddle_connections
SET status='connected', connected_at=COALESCE(connected_at, now()), updated_at=now(), revision=revision+1
WHERE company_id=sqlc.arg(company_id)::uuid AND id=sqlc.arg(connection_id)::uuid
  AND status IN ('reserved','connected')
RETURNING *;

-- name: MarkHuddleConnectionEnded :one
UPDATE huddle_connections
SET status='ended', ended_at=COALESCE(ended_at, now()), updated_at=now(), revision=revision+1
WHERE company_id=sqlc.arg(company_id)::uuid AND id=sqlc.arg(connection_id)::uuid
  AND user_id=sqlc.arg(user_id)::uuid
  AND status IN ('reserved','connected')
RETURNING *;

-- name: RevokeExpiredHuddleConnections :many
UPDATE huddle_connections
SET status='revoked', updated_at=now(), revision=revision+1
WHERE status='reserved'
  AND reservation_expires_at IS NOT NULL
  AND reservation_expires_at <= sqlc.arg(as_of)::timestamptz
RETURNING *;

-- name: GetHuddleConnectionByRoomIdentity :one
SELECT hc.*
FROM huddle_connections hc
JOIN huddles h ON h.company_id=hc.company_id AND h.id=hc.huddle_id
WHERE hc.company_id=sqlc.arg(company_id)::uuid
  AND h.room_name=sqlc.arg(room_name)::text
  AND hc.user_id=sqlc.arg(user_id)::uuid
ORDER BY hc.created_at DESC, hc.id DESC
LIMIT 1;

-- name: GetHuddleConnectionByRoomIdentityAnyCompany :one
SELECT hc.*
FROM huddle_connections hc
JOIN huddles h ON h.company_id=hc.company_id AND h.id=hc.huddle_id
WHERE h.room_name=sqlc.arg(room_name)::text
  AND hc.user_id=sqlc.arg(user_id)::uuid
ORDER BY hc.created_at DESC, hc.id DESC
LIMIT 1;

-- name: GetHuddleConnection :one
SELECT * FROM huddle_connections WHERE company_id=$1 AND id=$2;

-- name: LockHuddleByChat :one
SELECT * FROM huddles WHERE company_id=$1 AND chat_conversation_id=$2 FOR UPDATE;

-- name: LockHuddleChatWriter :one
SELECT id FROM huddle_connections WHERE company_id=$1 AND huddle_id=$2 AND user_id=$3 AND status='connected' FOR SHARE;
