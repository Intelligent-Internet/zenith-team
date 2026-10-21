-- name: CreateUser :one
INSERT INTO users (email, password_hash, display_name, email_verified_at)
VALUES (
    sqlc.arg(email)::text,
    sqlc.narg(password_hash)::text,
    sqlc.arg(display_name)::text,
    sqlc.narg(email_verified_at)::timestamptz
)
RETURNING *;

-- name: MarkUserEmailVerified :one
UPDATE users
SET email_verified_at = COALESCE(email_verified_at, sqlc.arg(verified_at)::timestamptz),
    updated_at = CASE
        WHEN email_verified_at IS NULL THEN sqlc.arg(verified_at)::timestamptz
        ELSE updated_at
    END,
    version = version + CASE WHEN email_verified_at IS NULL THEN 1 ELSE 0 END
WHERE id = sqlc.arg(id)::uuid
  AND lower(email) = lower(sqlc.arg(email)::text)
  AND disabled_at IS NULL
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = sqlc.arg(id)::uuid;

-- GetUserProjection is the canonical single-User read for HTTP responses and
-- personal user.updated events. The aggregate stays nullable here; the wire
-- projector owns the exact [] representation for an account with no links.
-- name: GetUserProjection :one
SELECT
    sqlc.embed(users),
    (array_agg(oauth_accounts.provider ORDER BY oauth_accounts.provider)
        FILTER (WHERE oauth_accounts.provider IS NOT NULL))::text[] AS linked_providers
FROM users
LEFT JOIN oauth_accounts ON oauth_accounts.user_id = users.id
WHERE users.id = sqlc.arg(id)::uuid
GROUP BY users.id;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE lower(email) = lower(sqlc.arg(email)::text);

-- name: LockUserForProfileUpdate :one
SELECT *
FROM users
WHERE id = sqlc.arg(id)::uuid
FOR UPDATE;

-- name: UpdateUserProfile :one
UPDATE users
SET display_name = sqlc.arg(display_name)::text,
    avatar_url = sqlc.narg(avatar_url)::text,
    biography = sqlc.arg(biography)::text,
    phone_country_iso2 = sqlc.narg(phone_country_iso2)::text,
    phone_number = sqlc.narg(phone_number)::text,
    updated_at = clock_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
  AND disabled_at IS NULL
  AND deleted_at IS NULL
RETURNING *;

-- name: CreateAuthSession :one
INSERT INTO auth_sessions (
    user_id, token_prefix, token_hash, expires_at,
    rotated_from_session_id, rotation_id, client_metadata, client
)
VALUES (
    sqlc.arg(user_id)::uuid,
    sqlc.arg(token_prefix)::text,
    sqlc.arg(token_hash)::bytea,
    sqlc.arg(expires_at)::timestamptz,
    sqlc.narg(rotated_from_session_id)::uuid,
    sqlc.narg(rotation_id)::uuid,
    sqlc.arg(client_metadata)::jsonb,
    sqlc.arg(client)::text
)
RETURNING *;

-- Password login reads and verifies the password before it creates a session.
-- Re-lock the User at the persistence boundary so account deletion cannot
-- commit between those two steps and leave a new live session behind.
-- name: CreateAuthSessionForEnabledUser :one
WITH locked_user AS MATERIALIZED (
    SELECT users.id
    FROM users
    WHERE users.id = sqlc.arg(user_id)::uuid
      AND users.disabled_at IS NULL
      AND users.deleted_at IS NULL
    FOR SHARE OF users
)
INSERT INTO auth_sessions (
    user_id, token_prefix, token_hash, expires_at,
    rotated_from_session_id, rotation_id, client_metadata, client
)
SELECT
    locked_user.id,
    sqlc.arg(token_prefix)::text,
    sqlc.arg(token_hash)::bytea,
    sqlc.arg(expires_at)::timestamptz,
    sqlc.narg(rotated_from_session_id)::uuid,
    sqlc.narg(rotation_id)::uuid,
    sqlc.arg(client_metadata)::jsonb,
    sqlc.arg(client)::text
FROM locked_user
RETURNING auth_sessions.*;

-- name: GetAuthSessionOwnerByTokenHash :one
SELECT user_id
FROM auth_sessions
WHERE token_hash = sqlc.arg(token_hash)::bytea;

-- name: LockUserForAuthSessionMutation :one
SELECT *
FROM users
WHERE id = sqlc.arg(id)::uuid
FOR UPDATE;

-- name: LockAuthSessionByTokenHash :one
SELECT *
FROM auth_sessions
WHERE token_hash = sqlc.arg(token_hash)::bytea
FOR UPDATE;

-- name: GetRecentDirectRotationChild :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
)
SELECT child.*
FROM auth_sessions AS child
INNER JOIN auth_sessions AS root
    ON root.id = child.rotated_from_session_id
   AND root.user_id = child.user_id
CROSS JOIN statement_clock
WHERE child.rotated_from_session_id = sqlc.arg(root_session_id)::uuid
  AND child.revoked_at IS NULL
  AND child.expires_at > statement_clock.now_at
  AND child.client_metadata = sqlc.arg(client_metadata)::jsonb
  AND root.revocation_reason = 'refresh_rotated'
  AND root.revoked_at <= statement_clock.now_at
  AND root.revoked_at >= statement_clock.now_at - INTERVAL '5 seconds'
FOR UPDATE OF child;

-- name: GetDirectRotationChildByRotationID :one
SELECT child.*
FROM auth_sessions AS child
INNER JOIN auth_sessions AS root
    ON root.id = child.rotated_from_session_id
   AND root.user_id = child.user_id
WHERE child.rotated_from_session_id = sqlc.arg(root_session_id)::uuid
  AND child.rotation_id = sqlc.arg(rotation_id)::uuid
  AND child.revoked_at IS NULL
  AND child.expires_at > clock_timestamp()
  AND root.revocation_reason = 'refresh_rotated'
FOR UPDATE OF child;

-- name: LockAuthSessionFamily :many
WITH RECURSIVE session_family AS (
    SELECT id, user_id
    FROM auth_sessions
    WHERE id = sqlc.arg(root_session_id)::uuid

    UNION ALL

    SELECT child.id, child.user_id
    FROM auth_sessions child
    INNER JOIN session_family parent
        ON child.rotated_from_session_id = parent.id
       AND child.user_id = parent.user_id
)
SELECT session.id
FROM auth_sessions AS session
INNER JOIN session_family
    ON session.id = session_family.id
   AND session.user_id = session_family.user_id
ORDER BY session.id
FOR UPDATE OF session;

-- name: RevokeLiveAuthSession :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
)
UPDATE auth_sessions
SET revoked_at = statement_clock.now_at,
    revocation_reason = sqlc.arg(revocation_reason)::text,
    revoked_by_user_id = sqlc.narg(revoked_by_user_id)::uuid,
    updated_at = statement_clock.now_at
FROM statement_clock
WHERE auth_sessions.id = sqlc.arg(id)::uuid
  AND auth_sessions.revoked_at IS NULL
  AND auth_sessions.expires_at > statement_clock.now_at
RETURNING auth_sessions.id, auth_sessions.user_id, auth_sessions.revoked_at,
          auth_sessions.revocation_reason, auth_sessions.revoked_by_user_id;

-- name: RevokeLiveAuthSessionFamily :many
WITH RECURSIVE statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
),
session_family AS (
    SELECT id, user_id
    FROM auth_sessions
    WHERE id = sqlc.arg(root_session_id)::uuid

    UNION ALL

    SELECT child.id, child.user_id
    FROM auth_sessions child
    INNER JOIN session_family parent
        ON child.rotated_from_session_id = parent.id
       AND child.user_id = parent.user_id
)
UPDATE auth_sessions
SET revoked_at = statement_clock.now_at,
    revocation_reason = sqlc.arg(revocation_reason)::text,
    revoked_by_user_id = sqlc.narg(revoked_by_user_id)::uuid,
    updated_at = statement_clock.now_at
FROM statement_clock
WHERE auth_sessions.id IN (SELECT id FROM session_family)
  AND auth_sessions.revoked_at IS NULL
  AND auth_sessions.expires_at > statement_clock.now_at
RETURNING auth_sessions.id, auth_sessions.user_id, auth_sessions.revoked_at,
          auth_sessions.revocation_reason, auth_sessions.revoked_by_user_id;

-- GetActiveSessionUser also reads who holds the session (client): a person
-- or a daemon, which the server admits only on the daemon plane and beside a
-- live Run proof.
-- name: GetActiveSessionUser :one
SELECT sqlc.embed(users), auth_sessions.client
FROM auth_sessions
INNER JOIN users ON users.id = auth_sessions.user_id
WHERE auth_sessions.id = sqlc.arg(session_id)::uuid
  AND auth_sessions.user_id = sqlc.arg(user_id)::uuid
  AND auth_sessions.revoked_at IS NULL
  AND auth_sessions.expires_at > clock_timestamp()
  AND users.disabled_at IS NULL;
