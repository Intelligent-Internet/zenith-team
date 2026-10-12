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
ORDER BY skill_versions.created_at LIMIT 100;

-- name: LockSkillImportForCleanup :one
SELECT id FROM skill_imports WHERE id = $1 AND expires_at < now() FOR UPDATE;

-- name: SkillVersionUnused :one
SELECT NOT EXISTS (SELECT 1 FROM agent_skills WHERE version_id = sqlc.arg(version_id)::uuid)
AND NOT EXISTS (SELECT 1 FROM run_skills WHERE version_id = sqlc.arg(version_id)::uuid) AS unused;

-- name: DeleteUnusedSkillVersion :exec
DELETE FROM skill_versions WHERE skill_versions.id = $1
AND NOT EXISTS (SELECT 1 FROM agent_skills WHERE version_id = skill_versions.id)
AND NOT EXISTS (SELECT 1 FROM run_skills WHERE version_id = skill_versions.id);

-- name: DeleteEmptyExpiredSkillImports :exec
DELETE FROM skill_imports WHERE expires_at < now()
AND NOT EXISTS (SELECT 1 FROM skill_versions WHERE import_id = skill_imports.id);

-- name: LockSkillVersionForCleanup :one
SELECT id FROM skill_versions WHERE id = $1 FOR UPDATE;
