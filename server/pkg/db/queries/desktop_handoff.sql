-- name: DeleteDesktopHandoffCodesForUser :exec
DELETE FROM desktop_handoff_codes WHERE user_id = sqlc.arg(user_id)::uuid;

-- name: CreateDesktopHandoffCode :exec
INSERT INTO desktop_handoff_codes (
    user_id,
    code_hash,
    expires_at
)
VALUES (
    sqlc.arg(user_id)::uuid,
    sqlc.arg(code_hash)::bytea,
    sqlc.arg(expires_at)::timestamptz
);

-- name: ConsumeDesktopHandoffCode :one
DELETE FROM desktop_handoff_codes WHERE code_hash = sqlc.arg(code_hash)::bytea RETURNING *;
