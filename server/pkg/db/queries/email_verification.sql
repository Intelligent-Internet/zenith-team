-- name: UpsertEmailVerificationFlow :one
INSERT INTO email_verification_flows (
    user_id, email, password_hash, display_name, client, client_metadata,
    code_hash, expires_at, resend_available_at
)
VALUES (
    sqlc.narg(user_id)::uuid,
    sqlc.arg(email)::text,
    sqlc.arg(password_hash)::text,
    sqlc.arg(display_name)::text,
    sqlc.arg(client)::text,
    sqlc.arg(client_metadata)::jsonb,
    sqlc.arg(code_hash)::bytea,
    sqlc.arg(expires_at)::timestamptz,
    sqlc.arg(resend_available_at)::timestamptz
)
ON CONFLICT (lower(email)) DO UPDATE
SET id = EXCLUDED.id,
    user_id = EXCLUDED.user_id,
    password_hash = EXCLUDED.password_hash,
    display_name = EXCLUDED.display_name,
    client = EXCLUDED.client,
    client_metadata = EXCLUDED.client_metadata,
    code_hash = EXCLUDED.code_hash,
    generation = email_verification_flows.generation + 1,
    failed_attempts = 0,
    expires_at = EXCLUDED.expires_at,
    resend_available_at = EXCLUDED.resend_available_at,
    updated_at = sqlc.arg(now_at)::timestamptz
WHERE email_verification_flows.resend_available_at <= sqlc.arg(now_at)::timestamptz
RETURNING email_verification_flows.*;

-- name: GetEmailVerificationFlowByEmail :one
SELECT *
FROM email_verification_flows
WHERE lower(email) = lower(sqlc.arg(email)::text);

-- name: GetEmailVerificationFlow :one
SELECT *
FROM email_verification_flows
WHERE id = sqlc.arg(id)::uuid
  AND lower(email) = lower(sqlc.arg(email)::text)
  AND client = sqlc.arg(client)::text;

-- name: UpdateEmailVerificationCode :one
UPDATE email_verification_flows
SET code_hash = sqlc.arg(code_hash)::bytea,
    generation = generation + 1,
    failed_attempts = 0,
    expires_at = sqlc.arg(expires_at)::timestamptz,
    resend_available_at = sqlc.arg(resend_available_at)::timestamptz,
    updated_at = sqlc.arg(now_at)::timestamptz
WHERE id = sqlc.arg(id)::uuid
  AND lower(email) = lower(sqlc.arg(email)::text)
  AND client = sqlc.arg(client)::text
  AND resend_available_at <= sqlc.arg(now_at)::timestamptz
  AND expires_at > sqlc.arg(now_at)::timestamptz
  AND failed_attempts < sqlc.arg(max_attempts)::integer
RETURNING *;

-- name: RecordEmailVerificationFailure :one
UPDATE email_verification_flows
SET failed_attempts = failed_attempts + 1,
    updated_at = sqlc.arg(now_at)::timestamptz
WHERE id = sqlc.arg(id)::uuid
  AND lower(email) = lower(sqlc.arg(email)::text)
  AND client = sqlc.arg(client)::text
  AND generation = sqlc.arg(generation)::integer
  AND expires_at > sqlc.arg(now_at)::timestamptz
  AND failed_attempts < sqlc.arg(max_attempts)::integer
RETURNING *;

-- name: MarkEmailVerificationDeliveryFailed :exec
UPDATE email_verification_flows
SET resend_available_at = GREATEST(sqlc.arg(retry_at)::timestamptz, created_at),
    updated_at = GREATEST(sqlc.arg(retry_at)::timestamptz, created_at)
WHERE id = sqlc.arg(id)::uuid
  AND generation = sqlc.arg(generation)::integer;

-- name: ConsumeEmailVerificationFlow :one
DELETE FROM email_verification_flows
WHERE id = sqlc.arg(id)::uuid
  AND lower(email) = lower(sqlc.arg(email)::text)
  AND client = sqlc.arg(client)::text
  AND generation = sqlc.arg(generation)::integer
  AND code_hash = sqlc.arg(code_hash)::bytea
  AND expires_at > sqlc.arg(now_at)::timestamptz
  AND failed_attempts < sqlc.arg(max_attempts)::integer
RETURNING *;
