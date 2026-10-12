-- name: ListSocialConnections :many
SELECT * FROM social_connections WHERE user_id = sqlc.arg(user_id) ORDER BY connected_at DESC;

-- name: GetSocialConnection :one
SELECT * FROM social_connections WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id);

-- name: UpsertSocialConnection :one
INSERT INTO social_connections (user_id, platform, external_id, display_name, username, avatar_url, credentials, expires_at)
VALUES (sqlc.arg(user_id), sqlc.arg(platform), sqlc.arg(external_id), sqlc.arg(display_name), sqlc.arg(username), sqlc.arg(avatar_url), sqlc.arg(credentials), sqlc.narg(expires_at))
ON CONFLICT (user_id, platform, external_id) DO UPDATE SET display_name = EXCLUDED.display_name, username = EXCLUDED.username, avatar_url = EXCLUDED.avatar_url, credentials = EXCLUDED.credentials, expires_at = EXCLUDED.expires_at, connected_at = statement_timestamp(), updated_at = statement_timestamp(), version = social_connections.version + 1, reconnect_required = false, maintenance_at = statement_timestamp(), operation_token = NULL, operation_until = NULL
RETURNING *;

-- name: DeleteSocialConnection :execrows
DELETE FROM social_connections WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id) AND version = sqlc.arg(version) AND operation_token = sqlc.arg(operation_token) AND operation_until > statement_timestamp();

-- name: ListSocialTargets :many
SELECT t.* FROM social_targets t JOIN social_connections c ON c.id = t.connection_id WHERE c.user_id = sqlc.arg(user_id) ORDER BY t.display_name, t.id;

-- name: DeleteSocialTargets :exec
DELETE FROM social_targets WHERE connection_id = sqlc.arg(connection_id);

-- name: InsertSocialTarget :exec
INSERT INTO social_targets (connection_id, external_id, display_name, username, type, credentials)
VALUES (sqlc.arg(connection_id), sqlc.arg(external_id), sqlc.arg(display_name), sqlc.arg(username), sqlc.arg(type), sqlc.arg(credentials));

-- name: InsertSocialOAuthFlow :exec
INSERT INTO social_oauth_flows (user_id, session_id, platform, state_hash, verifier, binding_hash, expires_at)
VALUES (sqlc.arg(user_id), sqlc.arg(session_id), sqlc.arg(platform), sqlc.arg(state_hash), sqlc.arg(verifier), sqlc.arg(binding_hash), statement_timestamp() + interval '10 minutes');

-- name: ConsumeSocialOAuthFlow :one
WITH RECURSIVE session_family AS (
    SELECT s.id, s.user_id, s.revoked_at, s.revocation_reason, s.expires_at
    FROM auth_sessions s JOIN social_oauth_flows f ON s.id = f.session_id AND s.user_id = f.user_id
    WHERE f.state_hash = sqlc.arg(state_hash)
    UNION ALL
    SELECT child.id, child.user_id, child.revoked_at, child.revocation_reason, child.expires_at
    FROM auth_sessions child JOIN session_family parent
      ON child.rotated_from_session_id = parent.id AND child.user_id = parent.user_id
    WHERE parent.revoked_at IS NOT NULL AND parent.revocation_reason = 'refresh_rotated'
)
UPDATE social_oauth_flows flow SET consumed_at = statement_timestamp()
WHERE flow.state_hash = sqlc.arg(state_hash) AND flow.binding_hash = sqlc.arg(binding_hash) AND flow.platform = sqlc.arg(platform) AND flow.consumed_at IS NULL AND flow.expires_at > statement_timestamp()
AND EXISTS (
    SELECT 1 FROM session_family s JOIN users u ON u.id = s.user_id
    WHERE s.revoked_at IS NULL AND s.expires_at > statement_timestamp() AND u.disabled_at IS NULL
)
RETURNING flow.*;

-- name: DeleteExpiredSocialOAuthFlows :exec
DELETE FROM social_oauth_flows WHERE expires_at < statement_timestamp() - interval '1 day';

-- name: LockSocialAuthorizationSession :one
WITH RECURSIVE session_family AS (
    SELECT root.id, root.user_id, root.revoked_at, root.revocation_reason FROM auth_sessions root
    WHERE root.id = sqlc.arg(session_id) AND root.user_id = sqlc.arg(user_id)
    UNION ALL
    SELECT child.id, child.user_id, child.revoked_at, child.revocation_reason
    FROM auth_sessions child JOIN session_family parent
      ON child.rotated_from_session_id = parent.id AND child.user_id = parent.user_id
    WHERE parent.revoked_at IS NOT NULL AND parent.revocation_reason = 'refresh_rotated'
)
SELECT s.id FROM auth_sessions s JOIN session_family f ON f.id = s.id
WHERE s.revoked_at IS NULL AND s.expires_at > statement_timestamp() FOR SHARE OF s;

-- name: SetSocialDesktopFlow :exec
UPDATE social_oauth_flows SET desktop_redirect_uri=$2, desktop_state=$3 WHERE state_hash=$1;

-- name: LaunchSocialDesktopFlow :one
WITH RECURSIVE session_family AS (
    SELECT s.id, s.user_id, s.revoked_at, s.revocation_reason, s.expires_at
    FROM auth_sessions s JOIN social_oauth_flows f ON s.id = f.session_id AND s.user_id = f.user_id
    WHERE f.state_hash = sqlc.arg(state_hash)
    UNION ALL
    SELECT child.id, child.user_id, child.revoked_at, child.revocation_reason, child.expires_at
    FROM auth_sessions child JOIN session_family parent
      ON child.rotated_from_session_id = parent.id AND child.user_id = parent.user_id
    WHERE parent.revoked_at IS NOT NULL AND parent.revocation_reason = 'refresh_rotated'
)
UPDATE social_oauth_flows flow SET launched_at=statement_timestamp()
WHERE flow.state_hash=sqlc.arg(state_hash) AND flow.binding_hash=sqlc.arg(binding_hash) AND flow.platform=sqlc.arg(platform) AND flow.desktop_redirect_uri<>''
 AND flow.launched_at IS NULL AND flow.consumed_at IS NULL AND flow.expires_at>statement_timestamp()
 AND EXISTS (
    SELECT 1 FROM session_family s JOIN users u ON u.id = s.user_id
    WHERE s.revoked_at IS NULL AND s.expires_at > statement_timestamp() AND u.disabled_at IS NULL
 )
RETURNING flow.*;

-- name: LockSocialConnection :one
SELECT * FROM social_connections WHERE id=$1 AND user_id=$2 FOR UPDATE;

-- name: ClaimSocialOperation :one
UPDATE social_connections SET operation_token=gen_random_uuid(), operation_until=statement_timestamp()+interval '3 minutes'
WHERE id=$1 AND user_id=$2 AND (operation_until IS NULL OR operation_until<statement_timestamp())
RETURNING *;

-- name: SaveSocialCredentials :execrows
UPDATE social_connections SET credentials=sqlc.arg(credentials), expires_at=sqlc.narg(expires_at),
 reconnect_required=sqlc.arg(reconnect_required), maintenance_at=sqlc.narg(maintenance_at), updated_at=statement_timestamp()
WHERE id=sqlc.arg(id) AND operation_token=sqlc.arg(operation_token) AND operation_until>statement_timestamp();

-- name: ReleaseSocialOperation :exec
UPDATE social_connections SET operation_token=NULL, operation_until=NULL WHERE id=$1 AND operation_token=$2;

-- name: ListSocialMaintenance :many
SELECT id,user_id FROM social_connections c WHERE maintenance_at<=statement_timestamp()
 AND NOT reconnect_required AND (operation_until IS NULL OR operation_until<statement_timestamp())
 AND EXISTS (SELECT 1 FROM users u WHERE u.id=c.user_id AND u.disabled_at IS NULL)
ORDER BY maintenance_at LIMIT 25;

-- name: GetSocialTarget :one
SELECT * FROM social_targets WHERE id=$1 AND connection_id=$2;

-- name: LockSocialConversation :one
SELECT id FROM conversations WHERE id=$1 AND company_id=$2 AND kind='direct' AND created_by_user_id=$3 FOR UPDATE;

-- name: LockSocialRun :one
SELECT id FROM runs WHERE id=$1 AND company_id=$2 AND conversation_id=$3 AND agent_id=$4 AND status='running' FOR UPDATE;

-- name: GetSocialPostReceipt :one
SELECT * FROM social_posts WHERE user_id=$1 AND request_key=$2;

-- name: CreateSocialPost :one
INSERT INTO social_posts(user_id,company_id,conversation_id,run_id,connection_id,request_key,payload_hash,platform,target_name,text)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING *;

-- name: FinishSocialPost :one
UPDATE social_posts SET status=$2,external_id=$3,url=$4,error_code=$5,updated_at=statement_timestamp()
WHERE id=$1 AND status='publishing' RETURNING *;

-- name: ListSocialPosts :many
SELECT * FROM social_posts WHERE user_id=$1 AND company_id=$2 ORDER BY created_at DESC LIMIT 20;

-- name: ListSocialMedia :many
-- The chat's media the person may open (attachment_access, one probe per
-- row): a Project file publishes only for a reader of its Projects (ADR 0017).
SELECT DISTINCT a.id,a.filename,a.content_type,a.size_bytes FROM attachments a
JOIN message_attachments ma ON ma.attachment_id=a.id AND ma.company_id=a.company_id
JOIN conversation_items i ON i.id=ma.message_id AND i.company_id=ma.company_id
CROSS JOIN LATERAL (
    SELECT 1 FROM attachment_access AS access
    WHERE access.company_id = a.company_id
      AND access.attachment_id = a.id
      AND access.user_id = sqlc.arg(actor_user_id)::uuid
    LIMIT 1
) AS readable
WHERE a.company_id=sqlc.arg(company_id) AND i.conversation_id=sqlc.arg(conversation_id) AND a.status='ready'
 AND ((a.content_type IN ('image/jpeg','image/png') AND a.size_bytes<=5242880)
   OR (a.content_type='video/mp4' AND a.size_bytes<=sqlc.arg(max_video_bytes)::bigint))
ORDER BY a.id LIMIT 100;

-- name: MarkSocialReconnect :exec
UPDATE social_connections SET reconnect_required=true, maintenance_at=NULL
WHERE id=$1 AND operation_token=$2;
