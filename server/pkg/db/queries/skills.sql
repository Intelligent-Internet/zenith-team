-- name: GetSkillAgent :one
SELECT * FROM agents WHERE company_id = $1 AND id = $2;

-- name: LockSkillAgent :one
SELECT * FROM agents WHERE company_id = $1 AND id = $2 FOR UPDATE;

-- name: CreateSkillImport :one
INSERT INTO skill_imports (company_id, agent_id, created_by_user_id, source_type, source_url, source_commit)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING *;

-- name: CreateSkillEdit :one
INSERT INTO skill_imports (company_id, agent_id, created_by_user_id, source_type, source_url, source_commit, edited)
VALUES ($1, $2, $3, $4, $5, $6, true) RETURNING *;

-- name: GetSkillEditForPublish :one
SELECT id FROM skill_imports
WHERE company_id = $1 AND agent_id = $2 AND id = $3 AND created_by_user_id = $4
  AND edited AND NOT ready AND expires_at > now() FOR UPDATE;

-- name: CountRecentSkillImports :one
SELECT count(*) FROM skill_imports WHERE company_id = $1 AND agent_id = $2 AND NOT edited AND created_at > now() - interval '24 hours';

-- name: CreateSkillVersion :one
INSERT INTO skill_versions (company_id, import_id, name, description, source_path, content_hash, size_bytes, file_count, manifest)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING *;

-- name: MarkSkillImportReady :exec
UPDATE skill_imports SET ready = true WHERE company_id = $1 AND id = $2;

-- name: GetSkillImportForInstall :one
SELECT * FROM skill_imports
WHERE company_id = $1 AND agent_id = $2 AND id = $3 AND created_by_user_id = $4
  AND ready AND NOT edited AND expires_at > now() FOR UPDATE;

-- name: ListSkillImportVersions :many
SELECT * FROM skill_versions WHERE company_id = $1 AND import_id = $2 ORDER BY name;

-- name: GetSkillVersion :one
SELECT skill_versions.*, skill_imports.source_type, skill_imports.source_url, skill_imports.source_commit
FROM skill_versions JOIN skill_imports ON skill_imports.id = skill_versions.import_id
WHERE skill_versions.company_id = $1 AND skill_versions.id = $2 AND skill_imports.ready;

-- name: ListAgentSkills :many
SELECT agent_skills.*, skill_versions.description, skill_versions.content_hash, skill_versions.size_bytes,
       skill_versions.file_count, skill_versions.manifest, skill_versions.source_path,
       skill_imports.source_type, skill_imports.source_url, skill_imports.source_commit, skill_imports.edited
FROM agent_skills JOIN skill_versions ON skill_versions.id = agent_skills.version_id
JOIN skill_imports ON skill_imports.id = skill_versions.import_id
WHERE agent_skills.company_id = $1 AND agent_skills.agent_id = $2 ORDER BY agent_skills.name;

-- name: InsertAgentSkill :one
INSERT INTO agent_skills (company_id, agent_id, version_id, name) VALUES ($1, $2, $3, $4) RETURNING *;

-- name: UpdateAgentSkill :execrows
UPDATE agent_skills SET enabled = $1, version_id = $2, revision = revision + 1, updated_at = now()
WHERE company_id = $3 AND agent_id = $4 AND id = $5 AND revision = $6;

-- name: DeleteAgentSkill :execrows
DELETE FROM agent_skills WHERE company_id = $1 AND agent_id = $2 AND id = $3 AND revision = $4;

-- name: TouchAgentSkills :exec
UPDATE agents SET version = version + 1, updated_at = clock_timestamp() WHERE company_id = $1 AND id = $2;

-- name: ListRunAgentSkills :many
SELECT skill_versions.* FROM agent_skills
JOIN skill_versions ON skill_versions.id = agent_skills.version_id
WHERE agent_skills.company_id = $1 AND agent_skills.agent_id = $2 AND agent_skills.enabled
ORDER BY agent_skills.name FOR KEY SHARE OF skill_versions;

-- name: FreezeRunSkill :exec
INSERT INTO run_skills (company_id, run_id, version_id) VALUES ($1, $2, $3);

-- name: GetFrozenRunSkill :one
SELECT skill_versions.* FROM run_skills JOIN skill_versions ON skill_versions.id = run_skills.version_id
WHERE run_skills.company_id = $1 AND run_skills.run_id = $2 AND run_skills.version_id = $3;

-- name: ListExpiredUnusedSkillVersions :many
SELECT skill_versions.* FROM skill_versions JOIN skill_imports ON skill_imports.id = skill_versions.import_id
WHERE skill_imports.expires_at < now()
AND NOT EXISTS (SELECT 1 FROM agent_skills WHERE version_id = skill_versions.id)
AND NOT EXISTS (SELECT 1 FROM run_skills WHERE version_id = skill_versions.id)
AND NOT EXISTS (SELECT 1 FROM message_skills WHERE version_id = skill_versions.id)
ORDER BY skill_versions.created_at LIMIT 100;

-- name: LockSkillImportForCleanup :one
SELECT id FROM skill_imports WHERE id = $1 AND expires_at < now() FOR UPDATE;

-- name: SkillVersionUnused :one
SELECT NOT EXISTS (SELECT 1 FROM agent_skills WHERE version_id = sqlc.arg(version_id)::uuid)
AND NOT EXISTS (SELECT 1 FROM run_skills WHERE version_id = sqlc.arg(version_id)::uuid)
AND NOT EXISTS (SELECT 1 FROM message_skills WHERE version_id = sqlc.arg(version_id)::uuid) AS unused;

-- name: DeleteUnusedSkillVersion :exec
DELETE FROM skill_versions WHERE skill_versions.id = $1
AND NOT EXISTS (SELECT 1 FROM agent_skills WHERE version_id = skill_versions.id)
AND NOT EXISTS (SELECT 1 FROM run_skills WHERE version_id = skill_versions.id)
AND NOT EXISTS (SELECT 1 FROM message_skills WHERE version_id = skill_versions.id);

-- name: DeleteEmptyExpiredSkillImports :exec
DELETE FROM skill_imports WHERE expires_at < now()
AND NOT EXISTS (SELECT 1 FROM skill_versions WHERE import_id = skill_imports.id);

-- name: LockSkillVersionForCleanup :one
SELECT id FROM skill_versions WHERE id = $1 FOR UPDATE;

-- name: ListMessageSkillCatalog :many
SELECT agent_skills.id, agent_skills.agent_id, agent_skills.version_id,
       agent_skills.name, skill_versions.description, agents.name AS agent_name
FROM agent_skills
JOIN agents ON agents.id = agent_skills.agent_id
JOIN skill_versions ON skill_versions.id = agent_skills.version_id
WHERE agent_skills.company_id = sqlc.arg(company_id)::uuid AND agent_skills.enabled AND agents.archived_at IS NULL
AND (
  (sqlc.arg(conversation_id)::uuid = '00000000-0000-0000-0000-000000000000' AND agents.id = sqlc.arg(agent_id)::uuid)
  OR EXISTS (
    SELECT 1 FROM conversations c
    WHERE c.company_id = agent_skills.company_id AND c.id = sqlc.arg(conversation_id)::uuid
      AND c.archived_at IS NULL AND c.document_thread_id IS NULL
      AND EXISTS (SELECT 1 FROM conversation_readers reader WHERE reader.company_id = c.company_id AND reader.conversation_id = c.id AND reader.user_id = sqlc.arg(actor_user_id)::uuid)
      AND (
        (c.kind = 'direct' AND c.agent_id = agents.id AND c.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
        OR (c.kind IN ('channel', 'human_direct')
          AND EXISTS (SELECT 1 FROM conversation_members m WHERE m.conversation_id = c.id AND m.user_id = sqlc.arg(actor_user_id)::uuid AND m.status = 'active')
          AND EXISTS (SELECT 1 FROM conversation_agents g WHERE g.conversation_id = c.id AND g.agent_id = agents.id AND g.status = 'active'))
      )
  )
)
ORDER BY agent_skills.name, agents.name, agent_skills.id;

-- name: GetSelectableMessageSkill :one
SELECT skill_versions.* FROM agent_skills
JOIN agents ON agents.id = agent_skills.agent_id
JOIN skill_versions ON skill_versions.id = agent_skills.version_id
WHERE agent_skills.company_id = $1 AND agent_skills.id = $2 AND agent_skills.version_id = $3
  AND agent_skills.enabled AND agents.archived_at IS NULL
  AND EXISTS (
    SELECT 1 FROM conversations c
    WHERE c.company_id = agent_skills.company_id AND c.id = sqlc.arg(conversation_id)::uuid
      AND ((c.kind = 'direct' AND c.agent_id = agents.id)
        OR (c.kind IN ('channel', 'human_direct') AND EXISTS (
          SELECT 1 FROM conversation_agents g
          WHERE g.conversation_id = c.id AND g.agent_id = agents.id AND g.status = 'active'
        )))
  )
FOR SHARE OF agent_skills, agents, skill_versions;

-- name: InsertMessageSkill :exec
INSERT INTO message_skills (company_id, conversation_id, request_key, selected_by_user_id,
 position, skill_id, version_id, start_offset, end_offset)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9);

-- name: ListMessageSkills :many
SELECT * FROM message_skills WHERE company_id=$1 AND conversation_id=$2
 AND request_key=$3 AND selected_by_user_id=$4 ORDER BY position;

-- name: ListMessageSkillVersions :many
SELECT DISTINCT skill_versions.* FROM message_skills
JOIN skill_versions ON skill_versions.id=message_skills.version_id
WHERE message_skills.company_id=$1 AND message_skills.conversation_id=$2
 AND message_skills.request_key=$3 AND message_skills.selected_by_user_id=$4
ORDER BY skill_versions.name;

-- name: ListInvocationSkillVersions :many
SELECT DISTINCT skill_versions.* FROM channel_invocations
JOIN conversation_items ON conversation_items.id=channel_invocations.trigger_message_id
JOIN message_skills ON message_skills.conversation_id=conversation_items.conversation_id
 AND message_skills.request_key=conversation_items.request_key
 AND message_skills.selected_by_user_id=conversation_items.author_id
JOIN skill_versions ON skill_versions.id=message_skills.version_id
WHERE channel_invocations.company_id=$1 AND channel_invocations.id=$2
ORDER BY skill_versions.name;

-- name: ListMessageSkillReferencesBulk :many
SELECT conversation_items.id AS message_id, message_skills.* FROM conversation_items
JOIN message_skills ON message_skills.conversation_id=conversation_items.conversation_id
 AND message_skills.request_key=conversation_items.request_key
 AND message_skills.selected_by_user_id=conversation_items.author_id
WHERE conversation_items.company_id=$1 AND conversation_items.id=ANY(sqlc.arg(message_ids)::uuid[])
ORDER BY conversation_items.id, message_skills.position;
