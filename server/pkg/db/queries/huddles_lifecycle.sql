-- name: OpenHuddle :one
UPDATE huddles
SET state='open', updated_at=now(), revision=revision+1
WHERE company_id=sqlc.arg(company_id)::uuid AND id=sqlc.arg(huddle_id)::uuid
  AND state='starting'
RETURNING *;

-- name: SetHuddleRoomName :one
UPDATE huddles
SET room_name=sqlc.arg(room_name)::text, updated_at=now(), revision=revision+1
WHERE company_id=sqlc.arg(company_id)::uuid AND id=sqlc.arg(huddle_id)::uuid
  AND room_name IS NULL AND state IN ('starting','open')
RETURNING *;

-- name: EndHuddleByRoom :one
UPDATE huddles
SET state='ended', ended_at=COALESCE(ended_at, now()), updated_at=now(), revision=revision+1
WHERE company_id=sqlc.arg(company_id)::uuid AND room_name=sqlc.arg(room_name)::text
  AND state IN ('starting','open','ending')
RETURNING *;

-- name: EndHuddleByRoomAnyCompany :one
UPDATE huddles
SET state='ended', ended_at=COALESCE(ended_at, now()), revision=revision+1, updated_at=now()
WHERE room_name=sqlc.arg(room_name)::text
  AND state IN ('starting','open','ending')
RETURNING *;

-- A Huddle whose media room was never created cannot be reached by the
-- room reconciliation below. Ending it after a grace period (or its deadline)
-- releases the source discussion for a fresh Start.
-- name: EndStaleRoomlessHuddles :many
UPDATE huddles
SET state='ended', ended_at=COALESCE(ended_at, now()), updated_at=now(), revision=revision+1
WHERE state='starting' AND room_name IS NULL
  AND (created_at<=now()-sqlc.arg(grace)::interval OR deadline_at<=now())
RETURNING *;

-- name: ListLiveHuddles :many
SELECT * FROM huddles
WHERE state IN ('starting','open','ending')
  AND room_name IS NOT NULL
ORDER BY created_at ASC, id ASC;

-- name: LockHuddle :one
SELECT * FROM huddles
WHERE company_id=sqlc.arg(company_id)::uuid AND id=sqlc.arg(huddle_id)::uuid
FOR UPDATE;

-- A confirmed human connection alone cancels empty-room closure. Reservations
-- and an AI participant cannot keep a room alive. A Huddle still starting
-- after the unjoined grace never had anyone connect (its starter's Join
-- failed or was abandoned), so it closes too.
-- name: BeginExpiredHuddleClosure :one
UPDATE huddles h
SET state='ending', updated_at=now(), revision=revision+1
WHERE h.company_id=sqlc.arg(company_id)::uuid AND h.id=sqlc.arg(huddle_id)::uuid
  AND h.state IN ('starting','open')
  AND (h.deadline_at<=now()
    OR (h.state='starting' AND h.created_at<=now()-sqlc.arg(unjoined_grace)::interval)
    OR (
    NOT EXISTS (SELECT 1 FROM huddle_connections c
      WHERE c.company_id=h.company_id AND c.huddle_id=h.id AND c.status='connected')
    AND (SELECT max(c.ended_at) FROM huddle_connections c
      WHERE c.company_id=h.company_id AND c.huddle_id=h.id AND c.connected_at IS NOT NULL)
      <=now()-interval '10 seconds'))
RETURNING h.*;
