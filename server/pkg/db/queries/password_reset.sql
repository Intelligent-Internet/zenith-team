-- name: UpsertPasswordResetFlow :one
INSERT INTO password_reset_flows (
    user_id, token_hash, expires_at, resend_available_at
)
VALUES (
    sqlc.arg(user_id)::uuid,
    sqlc.arg(token_hash)::bytea,
    sqlc.arg(expires_at)::timestamptz,
    sqlc.arg(resend_available_at)::timestamptz
)
ON CONFLICT (user_id) DO UPDATE
SET id = EXCLUDED.id,
    token_hash = EXCLUDED.token_hash,
    generation = password_reset_flows.generation + 1,
    expires_at = EXCLUDED.expires_at,
    resend_available_at = EXCLUDED.resend_available_at,
    updated_at = sqlc.arg(now_at)::timestamptz
WHERE password_reset_flows.resend_available_at <= sqlc.arg(now_at)::timestamptz
RETURNING *;

-- name: GetPasswordResetFlowByUser :one
SELECT * FROM password_reset_flows
WHERE user_id = sqlc.arg(user_id)::uuid;

-- name: MarkPasswordResetDeliveryFailed :exec
UPDATE password_reset_flows
SET resend_available_at = GREATEST(sqlc.arg(retry_at)::timestamptz, created_at),
    updated_at = GREATEST(sqlc.arg(retry_at)::timestamptz, created_at)
WHERE id = sqlc.arg(id)::uuid
  AND generation = sqlc.arg(generation)::integer;

-- name: ConsumePasswordResetFlow :one
DELETE FROM password_reset_flows
WHERE token_hash = sqlc.arg(token_hash)::bytea
  AND expires_at > sqlc.arg(now_at)::timestamptz
RETURNING *;

-- name: SetUserPasswordFromReset :one
UPDATE users
SET password_hash = sqlc.arg(password_hash)::text,
    email_verified_at = COALESCE(email_verified_at, sqlc.arg(now_at)::timestamptz),
    updated_at = sqlc.arg(now_at)::timestamptz,
    version = version + 1
WHERE id = sqlc.arg(id)::uuid
  AND disabled_at IS NULL
RETURNING *;

-- name: RevokeAllLiveAuthSessionsForPasswordReset :many
WITH statement_clock AS MATERIALIZED (
    SELECT sqlc.arg(now_at)::timestamptz AS now_at
)
UPDATE auth_sessions
SET revoked_at = statement_clock.now_at,
    revocation_reason = 'password_reset',
    revoked_by_user_id = NULL,
    updated_at = statement_clock.now_at
FROM statement_clock
WHERE auth_sessions.user_id = sqlc.arg(user_id)::uuid
  AND auth_sessions.revoked_at IS NULL
  AND auth_sessions.expires_at > statement_clock.now_at
RETURNING auth_sessions.id, auth_sessions.user_id, auth_sessions.revoked_at,
          auth_sessions.revocation_reason;
