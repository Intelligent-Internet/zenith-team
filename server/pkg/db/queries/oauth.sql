-- name: CreateOAuthLoginFlow :one
INSERT INTO oauth_login_flows (
    provider,
    client,
    state_hash,
    nonce_hash,
    browser_binding_hash,
    redirect_uri,
    return_path,
    code_challenge,
    client_metadata,
    expires_at
)
VALUES (
    sqlc.arg(provider)::text,
    sqlc.arg(client)::text,
    sqlc.arg(state_hash)::bytea,
    sqlc.arg(nonce_hash)::bytea,
    sqlc.narg(browser_binding_hash)::bytea,
    sqlc.arg(redirect_uri)::text,
    sqlc.narg(return_path)::text,
    sqlc.narg(code_challenge)::text,
    sqlc.arg(client_metadata)::jsonb,
    sqlc.arg(expires_at)::timestamptz
)
RETURNING *;

-- name: LockOAuthIdentity :one
WITH identity_lock AS MATERIALIZED (
    SELECT pg_advisory_xact_lock(
        hashtextextended(
            octet_length(sqlc.arg(provider)::text)::text || ':' ||
                sqlc.arg(provider)::text || sqlc.arg(subject)::text,
            0
        )
    )
)
SELECT true::boolean AS acquired
FROM identity_lock;

-- name: GetOAuthAccountByIdentity :one
SELECT *
FROM oauth_accounts
WHERE provider = sqlc.arg(provider)::text
  AND subject = sqlc.arg(subject)::text
FOR UPDATE;

-- name: GetOAuthUserForUpdate :one
SELECT *
FROM users
WHERE id = sqlc.arg(id)::uuid
FOR UPDATE;

-- name: CreateOAuthUser :one
INSERT INTO users (email, password_hash, display_name, avatar_url, email_verified_at)
VALUES (
    sqlc.arg(email)::text,
    NULL,
    sqlc.arg(display_name)::text,
    sqlc.narg(avatar_url)::text,
    clock_timestamp()
)
RETURNING *;

-- name: CreateOAuthAccount :one
INSERT INTO oauth_accounts (user_id, provider, subject, email, profile)
VALUES (
    sqlc.arg(user_id)::uuid,
    sqlc.arg(provider)::text,
    sqlc.arg(subject)::text,
    sqlc.arg(email)::text,
    sqlc.arg(profile)::jsonb
)
RETURNING *;

-- name: UpdateOAuthAccountSnapshot :one
UPDATE oauth_accounts
SET email = sqlc.arg(email)::text,
    profile = sqlc.arg(profile)::jsonb,
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(id)::uuid
RETURNING *;

-- name: ConsumeWebOAuthLoginFlow :one
DELETE FROM oauth_login_flows
WHERE provider = sqlc.arg(provider)::text
  AND client = 'web'
  AND state_hash = sqlc.arg(state_hash)::bytea
  AND browser_binding_hash = sqlc.arg(browser_binding_hash)::bytea
  AND redirect_uri = sqlc.arg(redirect_uri)::text
  AND expires_at > clock_timestamp()
RETURNING *;

-- name: ConsumeDesktopOAuthLoginFlow :one
DELETE FROM oauth_login_flows
WHERE id = sqlc.arg(id)::uuid
  AND provider = sqlc.arg(provider)::text
  AND client = 'desktop'
  AND state_hash = sqlc.arg(state_hash)::bytea
  AND redirect_uri = sqlc.arg(redirect_uri)::text
  AND code_challenge = sqlc.arg(code_challenge)::text
  AND expires_at > clock_timestamp()
RETURNING *;
