-- name: GetOutputAccess :one
SELECT outputs.id, outputs.company_id, outputs.run_id, outputs.conversation_id, outputs.owner_user_id,
       outputs.access_version, outputs.visibility, outputs.public_token, owner_account.display_name AS owner_display_name,
       EXISTS (SELECT 1 FROM company_members AS owner_membership
           WHERE owner_membership.company_id = outputs.company_id
             AND owner_membership.user_id = outputs.owner_user_id
             AND owner_membership.status = 'active'
             AND owner_account.disabled_at IS NULL) AS owner_active,
       access.access_level
FROM outputs
JOIN output_access AS access
  ON access.company_id = outputs.company_id AND access.output_id = outputs.id
 AND access.user_id = sqlc.arg(actor_user_id)::uuid
LEFT JOIN users AS owner_account ON owner_account.id = outputs.owner_user_id
WHERE outputs.company_id = sqlc.arg(company_id)::uuid
  AND outputs.id = sqlc.arg(output_id)::uuid;

-- name: LockOutputForAccessChange :one
SELECT outputs.id, outputs.owner_user_id, outputs.access_version, outputs.visibility
FROM outputs
WHERE outputs.company_id = sqlc.arg(company_id)::uuid
  AND outputs.id = sqlc.arg(output_id)::uuid
FOR UPDATE;

-- name: GetOutputAccessForManage :one
SELECT COALESCE((SELECT access_level FROM output_access
    WHERE company_id = sqlc.arg(company_id)::uuid
      AND output_id = sqlc.arg(output_id)::uuid
      AND user_id = sqlc.arg(actor_user_id)::uuid), 0)::integer;

-- name: ListOutputGrants :many
SELECT output_grants.user_id, output_grants.permission, output_grants.created_at,
       users.display_name
FROM output_grants
JOIN users ON users.id = output_grants.user_id AND users.disabled_at IS NULL
JOIN company_members AS member
  ON member.company_id = output_grants.company_id
 AND member.user_id = output_grants.user_id
 AND member.status = 'active'
 AND output_grants.updated_at >= COALESCE(member.reactivated_at, member.created_at)
WHERE output_grants.company_id = sqlc.arg(company_id)::uuid
  AND output_grants.output_id = sqlc.arg(output_id)::uuid
ORDER BY users.display_name, output_grants.user_id;

-- name: UpsertOutputGrant :one
INSERT INTO output_grants (company_id, output_id, user_id, permission, granted_by_user_id)
SELECT sqlc.arg(company_id)::uuid, sqlc.arg(output_id)::uuid,
       sqlc.arg(user_id)::uuid, sqlc.arg(permission)::text,
       sqlc.arg(actor_user_id)::uuid
FROM company_members
JOIN users ON users.id = company_members.user_id AND users.disabled_at IS NULL
WHERE company_members.company_id = sqlc.arg(company_id)::uuid
  AND company_members.user_id = sqlc.arg(user_id)::uuid
  AND company_members.status = 'active'
ON CONFLICT (company_id, output_id, user_id) DO UPDATE
SET permission = EXCLUDED.permission, granted_by_user_id = EXCLUDED.granted_by_user_id,
    updated_at = statement_timestamp()
RETURNING user_id;

-- name: DeleteOutputGrant :execrows
DELETE FROM output_grants
WHERE company_id = sqlc.arg(company_id)::uuid
  AND output_id = sqlc.arg(output_id)::uuid
  AND user_id = sqlc.arg(user_id)::uuid;

-- name: BumpOutputAccessVersion :one
UPDATE outputs SET access_version = access_version + 1,
    visibility = CASE WHEN visibility = 'private' THEN 'shared' ELSE visibility END
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(output_id)::uuid
  AND access_version < 9007199254740991
RETURNING access_version;

-- name: ListUnownedOutputs :many
SELECT outputs.id, outputs.run_id, outputs.created_at, outputs.access_version, outputs.task_id
FROM outputs
WHERE outputs.company_id = sqlc.arg(company_id)::uuid
  AND (outputs.owner_user_id IS NULL OR NOT EXISTS (
      SELECT 1 FROM company_members AS member JOIN users ON users.id = member.user_id
      WHERE member.company_id = outputs.company_id
        AND member.user_id = outputs.owner_user_id
        AND member.status = 'active' AND users.disabled_at IS NULL
  ))
ORDER BY outputs.created_at, outputs.id
LIMIT 200;

-- name: ClaimUnownedOutput :one
UPDATE outputs SET owner_user_id = sqlc.arg(user_id)::uuid,
                   access_version = access_version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(output_id)::uuid
  AND (owner_user_id IS NULL OR NOT EXISTS (
      SELECT 1 FROM company_members AS member JOIN users ON users.id = member.user_id
      WHERE member.company_id = outputs.company_id
        AND member.user_id = outputs.owner_user_id
        AND member.status = 'active' AND users.disabled_at IS NULL
  ))
  AND access_version = sqlc.arg(expected_revision)::bigint
  AND access_version < 9007199254740991
RETURNING access_version;

-- name: ListOutputAccessRecipients :many
SELECT user_id
FROM output_access
WHERE company_id = sqlc.arg(company_id)::uuid
  AND output_id = sqlc.arg(output_id)::uuid
ORDER BY user_id;

-- name: SetOutputVisibility :one
UPDATE outputs SET visibility = sqlc.arg(visibility)::text,
    public_token = CASE WHEN sqlc.arg(visibility)::text = 'public'
        THEN COALESCE(public_token, gen_random_uuid()) ELSE NULL END,
    access_version = access_version + 1
WHERE company_id = sqlc.arg(company_id)::uuid AND id = sqlc.arg(output_id)::uuid
RETURNING *;

-- name: ShareOutputWithConversation :exec
INSERT INTO output_conversation_grants(company_id,output_id,conversation_id,granted_by_user_id)
VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING;

-- name: DeleteOutputConversationGrant :execrows
DELETE FROM output_conversation_grants WHERE company_id=$1 AND output_id=$2 AND conversation_id=$3;

-- name: ListOutputConversationGrants :many
-- A member sees the name the chat list shows: a channel's title, or a direct
-- message's other participants (ListDiscussionAccess's group naming).
SELECT grant_row.conversation_id,
 CASE WHEN EXISTS (SELECT 1 FROM conversation_members member WHERE member.conversation_id=conversation.id
    AND member.company_id=conversation.company_id AND member.user_id=sqlc.arg(actor_user_id)::uuid AND member.status='active')
 THEN COALESCE(NULLIF(conversation.title,''),(
    SELECT string_agg(person.name, ', ' ORDER BY person.kind,person.id) FROM (
        SELECT other.user_id AS id, 'human' AS kind, left(account.display_name,256) AS name
        FROM conversation_members other JOIN users account ON account.id=other.user_id
        JOIN company_members other_member ON other_member.company_id=other.company_id AND other_member.user_id=other.user_id
        WHERE other.conversation_id=conversation.id AND other.company_id=conversation.company_id
            AND other.user_id<>sqlc.arg(actor_user_id)::uuid AND other.status='active' AND other_member.status='active'
            AND account.deleted_at IS NULL AND account.disabled_at IS NULL
        UNION ALL
        SELECT participant.agent_id AS id, 'agent' AS kind, left(agent.name,256) AS name
        FROM conversation_agents participant JOIN agents agent ON agent.id=participant.agent_id AND agent.company_id=participant.company_id
        WHERE participant.conversation_id=conversation.id AND participant.company_id=conversation.company_id
            AND participant.status='active' AND agent.archived_at IS NULL
    ) person),'Direct message') ELSE 'Private conversation' END::text AS title,
 grant_row.created_at
FROM output_conversation_grants grant_row
JOIN conversations conversation ON conversation.id=grant_row.conversation_id AND conversation.company_id=grant_row.company_id
WHERE grant_row.company_id=$1 AND grant_row.output_id=$2
AND (EXISTS (SELECT 1 FROM output_access access WHERE access.company_id=grant_row.company_id
 AND access.output_id=grant_row.output_id AND access.user_id=sqlc.arg(actor_user_id)::uuid AND access.access_level=3)
 OR EXISTS (SELECT 1 FROM conversation_members member WHERE member.conversation_id=conversation.id
    AND member.company_id=conversation.company_id AND member.user_id=sqlc.arg(actor_user_id)::uuid AND member.status='active'))
ORDER BY grant_row.created_at,grant_row.conversation_id;



-- name: GetPublicOutput :one
SELECT outputs.* FROM outputs JOIN companies ON companies.id=outputs.company_id
WHERE outputs.public_token=$1 AND outputs.visibility='public'
AND companies.deleting_at IS NULL;

-- name: ResolvePublicOutputFile :one
SELECT version.attachment_id, outputs.company_id
FROM outputs JOIN companies ON companies.id=outputs.company_id
JOIN output_items item ON item.output_id=outputs.id AND item.company_id=outputs.company_id
JOIN asset_versions version ON version.id=item.asset_version_id AND version.company_id=item.company_id
WHERE outputs.public_token=$1 AND outputs.visibility='public'
AND companies.deleting_at IS NULL
AND item.ordinal=$2 AND (item.role = 'final' OR (item.role = 'primary' AND outputs.type <> 'app'))
AND version.content_type <> 'application/vnd.zenith-studio.canvas+json';


-- name: CanShareOutputToConversation :one
SELECT EXISTS(SELECT 1 FROM conversations conversation
 JOIN conversation_members participant ON participant.company_id=conversation.company_id AND participant.conversation_id=conversation.id
 WHERE conversation.company_id=sqlc.arg(company_id)::uuid AND conversation.id=sqlc.arg(conversation_id)::uuid
 AND conversation.kind IN ('channel','human_direct') AND conversation.archived_at IS NULL
 AND participant.user_id=sqlc.arg(actor_user_id)::uuid AND participant.status='active')::boolean;

-- name: ListArtifactCompanyRecipients :many
SELECT member.user_id FROM company_members member JOIN users ON users.id=member.user_id
WHERE member.company_id=$1 AND member.status='active' AND users.disabled_at IS NULL AND users.deleted_at IS NULL;
