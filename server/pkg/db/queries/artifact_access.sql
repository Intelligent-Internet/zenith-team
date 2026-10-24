-- name: ReadArtifactAccess :one
SELECT sqlc.embed(artifact), access.access_level, member.role,
 (project.archived_at IS NOT NULL)::boolean AS project_archived
FROM artifacts artifact
JOIN artifact_access access ON access.company_id=artifact.company_id AND access.artifact_id=artifact.id
 AND access.user_id=sqlc.arg(actor_user_id)::uuid
JOIN company_members member ON member.company_id=access.company_id AND member.user_id=access.user_id
LEFT JOIN projects project ON project.company_id=artifact.company_id AND project.id=artifact.project_id
WHERE artifact.company_id=sqlc.arg(company_id)::uuid AND artifact.id=sqlc.arg(artifact_id)::uuid;

-- name: LockArtifactIdentity :one
SELECT * FROM artifacts WHERE company_id=$1 AND id=$2 FOR UPDATE;

-- name: ListArtifactReaders :many
SELECT user_id FROM artifact_access WHERE company_id=$1 AND artifact_id=$2 ORDER BY user_id;

-- name: UpsertArtifactGrant :one
INSERT INTO artifact_grants(company_id,artifact_id,user_id,permission,granted_by_user_id)
SELECT sqlc.arg(company_id)::uuid, sqlc.arg(artifact_id)::uuid, sqlc.arg(user_id)::uuid,
 sqlc.arg(permission)::text, sqlc.arg(actor_user_id)::uuid
FROM company_members member JOIN users account ON account.id=member.user_id
WHERE member.company_id=sqlc.arg(company_id)::uuid AND member.user_id=sqlc.arg(user_id)::uuid
 AND member.status='active' AND account.disabled_at IS NULL AND account.deleted_at IS NULL
ON CONFLICT(company_id,artifact_id,user_id) DO UPDATE
SET permission=EXCLUDED.permission,granted_by_user_id=EXCLUDED.granted_by_user_id,updated_at=statement_timestamp()
RETURNING user_id;

-- name: DeleteArtifactGrant :execrows
DELETE FROM artifact_grants WHERE company_id=$1 AND artifact_id=$2 AND user_id=$3;

-- name: ListArtifactGrants :many
SELECT grant_row.user_id,grant_row.permission,grant_row.created_at,account.display_name
FROM artifact_grants grant_row JOIN company_members member
 ON member.company_id=grant_row.company_id AND member.user_id=grant_row.user_id
 AND member.status='active' AND grant_row.updated_at>=COALESCE(member.reactivated_at,member.created_at)
JOIN users account ON account.id=member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE grant_row.company_id=$1 AND grant_row.artifact_id=$2 ORDER BY account.display_name,grant_row.user_id;

-- name: UpdateArtifactVisibility :exec
UPDATE artifacts SET visibility=$3 WHERE company_id=$1 AND id=$2;

-- name: AdvanceArtifactAccess :one
UPDATE artifacts SET access_version=access_version+1,updated_at=clock_timestamp()
WHERE company_id=$1 AND id=$2 RETURNING access_version;

-- name: ListReadablePages :many
SELECT artifact.id,artifact.title,artifact.project_id,artifact.owner_user_id,artifact.updated_at
FROM artifacts artifact
JOIN artifact_access access ON access.company_id=artifact.company_id AND access.artifact_id=artifact.id
 AND access.user_id=sqlc.arg(actor_user_id)::uuid
WHERE artifact.company_id=sqlc.arg(company_id)::uuid AND artifact.type='page' AND artifact.archived_at IS NULL AND NOT artifact.pending_creation
ORDER BY artifact.updated_at DESC,artifact.id;

-- name: ShareArtifactWithConversation :exec
INSERT INTO artifact_conversation_grants(company_id,artifact_id,conversation_id,granted_by_user_id)
VALUES($1,$2,$3,$4) ON CONFLICT(company_id,artifact_id,conversation_id) DO NOTHING;

-- name: LinkDiscussionWork :exec
INSERT INTO discussion_artifacts(company_id,message_id,artifact_id) VALUES($1,$2,$3);

-- name: ListLiveArtifacts :many
SELECT artifact.id,artifact.title,artifact.type,artifact.created_at,artifact.created_by_user_id,
 COALESCE((SELECT content.summary FROM retained_snapshots content JOIN artifact_snapshots snapshot ON snapshot.id=content.id
   WHERE content.company_id=artifact.company_id AND content.artifact_id=artifact.id ORDER BY snapshot.number DESC LIMIT 1),'')::text AS summary,
 COALESCE(owner_account.display_name,'Former member')::text AS owner_name,
 COALESCE(artifact.owner_user_id=sqlc.arg(actor_user_id)::uuid,false)::boolean AS owned,
 (access.access_level>=3)::boolean AS can_share,
 CASE WHEN source.id IS NULL THEN 'restricted' WHEN source.kind='human_direct' THEN 'direct' ELSE source.kind END::text AS source_type,
 COALESCE(task.title,source.title,agent.name,'')::text AS source_title,
 COALESCE(project.title,'')::text AS source_detail
FROM artifacts artifact
JOIN artifact_access access ON access.company_id=artifact.company_id AND access.artifact_id=artifact.id
 AND access.user_id=sqlc.arg(actor_user_id)::uuid
LEFT JOIN users owner_account ON owner_account.id=artifact.owner_user_id
LEFT JOIN runs origin_run ON origin_run.company_id=artifact.company_id AND origin_run.id=artifact.source_run_id
LEFT JOIN conversations source ON source.company_id=artifact.company_id AND source.id=COALESCE(artifact.conversation_id,origin_run.conversation_id)
 AND EXISTS(SELECT 1 FROM conversation_readers reader WHERE reader.company_id=source.company_id
   AND reader.conversation_id=source.id AND reader.user_id=sqlc.arg(actor_user_id)::uuid)
LEFT JOIN tasks task ON task.company_id=source.company_id AND task.id=source.task_id
LEFT JOIN agents agent ON agent.company_id=source.company_id AND agent.id=source.agent_id
LEFT JOIN projects project ON project.company_id=source.company_id AND project.id=source.project_id
WHERE artifact.company_id=sqlc.arg(company_id)::uuid AND artifact.type IN ('whiteboard','file','media','app','page') AND artifact.archived_at IS NULL AND NOT artifact.pending_creation
 AND (cardinality(sqlc.arg(ids)::uuid[])=0 OR artifact.id=ANY(sqlc.arg(ids)::uuid[]))
 AND (sqlc.narg(conversation_id)::uuid IS NULL OR artifact.conversation_id=sqlc.narg(conversation_id)::uuid
   OR EXISTS(SELECT 1 FROM artifact_conversation_grants shared WHERE shared.company_id=artifact.company_id
     AND shared.artifact_id=artifact.id AND shared.conversation_id=sqlc.narg(conversation_id)::uuid)
   OR EXISTS(SELECT 1 FROM delivery_snapshots delivery
     JOIN artifact_snapshots snapshot ON snapshot.company_id=delivery.company_id AND snapshot.id=delivery.snapshot_id AND snapshot.artifact_id=artifact.id
     JOIN outputs output ON output.company_id=delivery.company_id AND output.id=delivery.output_id
     LEFT JOIN runs run ON run.company_id=output.company_id AND run.id=output.run_id
     WHERE delivery.company_id=artifact.company_id
      AND EXISTS(SELECT 1 FROM output_access access WHERE access.company_id=output.company_id AND access.output_id=output.id AND access.user_id=sqlc.arg(actor_user_id)::uuid)
      AND (output.conversation_id=sqlc.narg(conversation_id)::uuid
       OR EXISTS(SELECT 1 FROM channel_invocations invocation WHERE invocation.company_id=run.company_id AND invocation.id=run.invocation_id AND invocation.channel_id=sqlc.narg(conversation_id)::uuid)
       OR EXISTS(SELECT 1 FROM output_conversation_grants shared WHERE shared.company_id=output.company_id AND shared.output_id=output.id AND shared.conversation_id=sqlc.narg(conversation_id)::uuid)
       OR EXISTS(SELECT 1 FROM discussion_artifacts link JOIN conversation_items message ON message.company_id=link.company_id AND message.id=link.message_id
         WHERE link.company_id=output.company_id AND link.output_id=output.id AND message.conversation_id=sqlc.narg(conversation_id)::uuid AND message.run_id IS NULL AND message.deleted_at IS NULL))))
 AND (sqlc.arg(ownership)::text='' OR (sqlc.arg(ownership)::text='owned' AND artifact.owner_user_id=sqlc.arg(actor_user_id)::uuid)
   OR (sqlc.arg(ownership)::text='shared' AND artifact.owner_user_id IS DISTINCT FROM sqlc.arg(actor_user_id)::uuid))
 AND (sqlc.arg(source_type)::text='' OR CASE WHEN source.id IS NULL THEN 'restricted' WHEN source.kind='human_direct' THEN 'direct' ELSE source.kind END=sqlc.arg(source_type)::text)
 AND (sqlc.arg(title_pattern)::text='' OR artifact.title ILIKE sqlc.arg(title_pattern)::text ESCAPE '\')
 AND (sqlc.narg(before_created_at)::timestamptz IS NULL OR (artifact.created_at,artifact.id)<(sqlc.narg(before_created_at)::timestamptz,sqlc.arg(before_id)::uuid))
ORDER BY artifact.created_at DESC,artifact.id DESC LIMIT sqlc.arg(row_limit)::integer;

-- name: ListArtifactConversationGrants :many
SELECT shared.conversation_id,shared.created_at,
 CASE WHEN EXISTS(SELECT 1 FROM conversation_readers reader WHERE reader.company_id=shared.company_id
   AND reader.conversation_id=shared.conversation_id AND reader.user_id=sqlc.arg(actor_user_id)::uuid)
 THEN COALESCE(conversation.title,'Conversation') ELSE 'Private conversation' END::text AS title
FROM artifact_conversation_grants shared
JOIN conversations conversation ON conversation.company_id=shared.company_id AND conversation.id=shared.conversation_id
WHERE shared.company_id=sqlc.arg(company_id)::uuid AND shared.artifact_id=sqlc.arg(artifact_id)::uuid
ORDER BY shared.created_at,shared.conversation_id;

-- name: DeleteArtifactConversationGrant :execrows
DELETE FROM artifact_conversation_grants WHERE company_id=$1 AND artifact_id=$2 AND conversation_id=$3;
