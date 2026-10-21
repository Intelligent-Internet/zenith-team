-- name: LockRunForOutputPresentation :one
SELECT runs.*
FROM runs
WHERE runs.id = sqlc.arg(run_id)::uuid
  AND runs.company_id = sqlc.arg(company_id)::uuid
FOR UPDATE OF runs;

-- name: GetOutputByRunFingerprint :one
SELECT *
FROM outputs
WHERE company_id = sqlc.arg(company_id)::uuid
  AND run_id = sqlc.arg(run_id)::uuid
  AND presentation_fingerprint = sqlc.arg(presentation_fingerprint)::text;

-- name: GetLatestOutputByRunForUpdate :one
SELECT *
FROM outputs
WHERE company_id = sqlc.arg(company_id)::uuid
  AND run_id = sqlc.arg(run_id)::uuid
ORDER BY presentation_number DESC
LIMIT 1
FOR UPDATE;

-- name: InsertOutput :one
-- The owner is the Task's creator, the Authorizer of a tagged Run (the
-- person who tagged the Agent), or the Direct or Schedule
-- Conversation's creator. A tagged Run's Output starts private to that
-- person, who shares it into the chat if they choose; the Agent's creator
-- gets nothing. A Task Run executes on its authorizer's own computer (ADR
-- 0014), so that member can always view what it presented: when they are
-- not the owner they get a view grant in the same statement, and the Output
-- starts shared rather than private. The owner keeps review and sharing.
WITH source AS (
    SELECT source_run.conversation_id, source_run.task_id, conversation.project_id, source_run.task_execution_generation, source_run.agent_id,
           source_run.execution_authorized_by_user_id, source_run.status,
           CASE WHEN conversation.kind = 'task' THEN task.creator_user_id
                WHEN conversation.kind = 'channel_agent' THEN source_run.execution_authorized_by_user_id
                ELSE conversation.created_by_user_id END AS owner_user_id,
           CASE WHEN conversation.kind = 'task'
                THEN source_run.execution_authorized_by_user_id END AS authorizer_user_id
    FROM runs AS source_run
    JOIN conversations AS conversation
      ON conversation.id = source_run.conversation_id
     AND conversation.company_id = source_run.company_id
    LEFT JOIN tasks AS task
      ON task.id = source_run.task_id AND task.company_id = source_run.company_id
    WHERE source_run.id = sqlc.arg(run_id)::uuid
      AND source_run.company_id = sqlc.arg(company_id)::uuid
), authorizer AS (
    SELECT member.user_id
    FROM source
    JOIN company_members AS member
      ON member.company_id = sqlc.arg(company_id)::uuid
     AND member.user_id = source.authorizer_user_id
     AND member.status = 'active'
    WHERE source.authorizer_user_id IS DISTINCT FROM source.owner_user_id
), inserted AS (
    INSERT INTO outputs (
        company_id, run_id, presentation_number, title, summary, submission_notes,
        presentation_fingerprint, type, preview_url, payload, owner_user_id, visibility,
        conversation_id, task_id, project_id, task_execution_generation, actor_agent_id,
        source_run_id, on_behalf_of_user_id, source_run_completed
    )
    SELECT
        sqlc.arg(company_id)::uuid,
        sqlc.arg(run_id)::uuid,
        sqlc.arg(presentation_number)::bigint,
        sqlc.arg(title)::text,
        sqlc.arg(summary)::text,
        sqlc.narg(submission_notes)::text,
        sqlc.arg(presentation_fingerprint)::text,
        sqlc.arg(type)::text,
        sqlc.narg(preview_url)::text,
        sqlc.arg(payload)::jsonb,
        source.owner_user_id,
        CASE WHEN EXISTS (SELECT 1 FROM authorizer) THEN 'shared' ELSE 'private' END,
        source.conversation_id, source.task_id, source.project_id, source.task_execution_generation, source.agent_id,
        sqlc.arg(run_id)::uuid, source.execution_authorized_by_user_id, source.status='completed'
    FROM source
    RETURNING *
), authorizer_grant AS (
    INSERT INTO output_grants (company_id, output_id, user_id, permission, granted_by_user_id)
    SELECT inserted.company_id, inserted.id, authorizer.user_id, 'view', authorizer.user_id
    FROM inserted
    CROSS JOIN authorizer
)
SELECT * FROM inserted;

-- name: InsertOutputItem :one
INSERT INTO output_items (
    company_id, output_id, ordinal, role, label, asset_version_id
) VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(output_id)::uuid,
    sqlc.arg(ordinal)::integer,
    sqlc.arg(role)::text,
    sqlc.arg(label)::text,
    sqlc.arg(asset_version_id)::uuid
)
RETURNING *;

-- name: ListOutputItems :many
SELECT
    output_items.id AS output_item_id,
    output_items.output_id,
    output_items.ordinal,
    output_items.role,
    output_items.label,
    asset_versions.id AS asset_version_id,
    asset_versions.asset_id,
    asset_versions.filename,
    asset_versions.content_type,
    asset_versions.size_bytes
FROM output_items
JOIN asset_versions
  ON asset_versions.id = output_items.asset_version_id
 AND asset_versions.company_id = output_items.company_id
JOIN assets
  ON assets.id = asset_versions.asset_id
 AND assets.company_id = asset_versions.company_id
WHERE output_items.company_id = sqlc.arg(company_id)::uuid
  AND output_items.output_id = ANY(sqlc.arg(output_ids)::uuid[])
ORDER BY output_items.output_id, output_items.ordinal;

-- name: GetReadyAssetVersionForOutputForShare :one
SELECT asset_versions.*
FROM asset_versions
JOIN assets
  ON assets.id = asset_versions.asset_id
 AND assets.company_id = asset_versions.company_id
WHERE asset_versions.id = sqlc.arg(asset_version_id)::uuid
  AND asset_versions.company_id = sqlc.arg(company_id)::uuid
  AND asset_versions.integrity_status = 'verified'
  AND assets.status = 'active'
FOR SHARE OF asset_versions, assets;

-- name: GetReadyAttachmentForOutputForShare :one
SELECT attachments.*
FROM attachments
WHERE attachments.id = sqlc.arg(attachment_id)::uuid
  AND attachments.company_id = sqlc.arg(company_id)::uuid
  AND attachments.uploaded_by_user_id = sqlc.arg(user_id)::uuid
  AND attachments.status = 'ready'
FOR SHARE;

-- name: InsertGeneratedAsset :one
INSERT INTO assets (
    company_id, title, media_class, created_by_user_id
) VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(title)::text,
    sqlc.arg(media_class)::text,
    sqlc.arg(created_by_user_id)::uuid
)
RETURNING *;

-- name: InsertGeneratedAssetVersion :one
INSERT INTO asset_versions (
    company_id, asset_id, version_number, attachment_id, source_run_id,
    filename, content_type, size_bytes, integrity_status
) VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(asset_id)::uuid,
    1,
    sqlc.arg(attachment_id)::uuid,
    sqlc.arg(source_run_id)::uuid,
    sqlc.arg(filename)::text,
    sqlc.arg(content_type)::text,
    sqlc.arg(size_bytes)::bigint,
    'verified'
)
ON CONFLICT (attachment_id) DO NOTHING
RETURNING *;

-- name: GetAssetVersionByAttachment :one
SELECT *
FROM asset_versions
WHERE company_id = sqlc.arg(company_id)::uuid
  AND attachment_id = sqlc.arg(attachment_id)::uuid;

-- name: FetchRunDeliverable :one
SELECT
    runs.status AS run_status,
    runs.runtime_id,
    runtimes.owner_user_id AS runtime_owner_user_id,
    company_members.role AS caller_role,
    conversations.kind AS conversation_kind,
    conversations.project_id AS conversation_project_id,
    outputs.id AS output_id,
    outputs.title,
    outputs.type AS output_type,
    outputs.preview_url,
    outputs.summary,
    outputs.submission_notes,
    outputs.created_at AS output_created_at,
    deliverables.id AS deliverable_id,
    deliverables.status AS deliverable_status,
    deliverables.submitted_at,
    deliverables.reviewed_at,
    deliverables.review_feedback,
    deliverables.version AS deliverable_version
FROM runs
JOIN runtimes ON runtimes.id = runs.runtime_id
JOIN conversations
  ON conversations.id = runs.conversation_id
 AND conversations.company_id = runs.company_id
JOIN companies
  ON companies.id = runs.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
  ON company_members.company_id = runs.company_id
 AND company_members.user_id = sqlc.arg(user_id)::uuid
 AND company_members.status = 'active'
LEFT JOIN outputs
  ON outputs.id = (
    SELECT output_rows.id
    FROM outputs AS output_rows
    WHERE output_rows.run_id = runs.id
      AND output_rows.company_id = runs.company_id
      AND (
        sqlc.narg(output_id)::uuid IS NULL
        OR output_rows.id = sqlc.narg(output_id)::uuid
      )
      AND EXISTS (SELECT 1 FROM output_access AS access
          WHERE access.company_id = output_rows.company_id
            AND access.output_id = output_rows.id
            AND access.user_id = sqlc.arg(user_id)::uuid)
    ORDER BY output_rows.presentation_number DESC
    LIMIT 1
  )
 AND outputs.company_id = runs.company_id
LEFT JOIN deliverables
  ON deliverables.output_id = outputs.id
 AND deliverables.company_id = runs.company_id
WHERE runs.id = sqlc.arg(run_id)::uuid
  AND runs.company_id = sqlc.arg(company_id)::uuid
  AND (
    outputs.id IS NOT NULL
    OR (sqlc.narg(output_id)::uuid IS NULL AND (
      -- A Task chat's Run is read by its Project's readers (ADR 0017).
      (conversations.kind = 'task' AND EXISTS (
        SELECT 1 FROM conversation_readers AS reader
        WHERE reader.company_id = conversations.company_id
          AND reader.conversation_id = conversations.id
          AND reader.user_id = sqlc.arg(user_id)::uuid
      ))
      OR (conversations.kind <> 'task' AND conversations.created_by_user_id = sqlc.arg(user_id)::uuid)
    ))
  );

-- name: ResolveOutputFileAttachment :one
SELECT attachments.id
FROM outputs
JOIN companies ON companies.id=outputs.company_id AND companies.deleting_at IS NULL
JOIN company_members ON company_members.company_id=outputs.company_id AND company_members.user_id=sqlc.arg(user_id)::uuid AND company_members.status='active'
JOIN output_items
  ON output_items.output_id = outputs.id
 AND output_items.company_id = outputs.company_id
 AND output_items.ordinal = sqlc.arg(ordinal)::integer
JOIN asset_versions
  ON asset_versions.id = output_items.asset_version_id
 AND asset_versions.company_id = output_items.company_id
 AND asset_versions.integrity_status = 'verified'
JOIN assets
  ON assets.id = asset_versions.asset_id
 AND assets.company_id = asset_versions.company_id
JOIN attachments
  ON attachments.id = asset_versions.attachment_id
 AND attachments.company_id = asset_versions.company_id
 AND attachments.status = 'ready'
WHERE outputs.id=sqlc.arg(output_id)::uuid
 AND outputs.company_id=sqlc.arg(company_id)::uuid
 AND outputs.run_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(run_id)::uuid,'00000000-0000-0000-0000-000000000000'::uuid)
  AND EXISTS (SELECT 1 FROM output_access AS access
      WHERE access.company_id = outputs.company_id
        AND access.output_id = outputs.id
        AND access.user_id = sqlc.arg(user_id)::uuid);

-- name: GetOutputStudioCanvas :one
SELECT * FROM output_studio_canvases
WHERE output_id = sqlc.arg(output_id)::uuid AND company_id = sqlc.arg(company_id)::uuid;

-- name: InsertOutputStudioCanvas :one
INSERT INTO output_studio_canvases (output_id, company_id, canvas, added_by_user_id)
VALUES (sqlc.arg(output_id)::uuid, sqlc.arg(company_id)::uuid, sqlc.arg(canvas)::jsonb, sqlc.arg(user_id)::uuid)
RETURNING *;

-- name: RetainOutputRunCompletion :exec
-- The completion receipt commits with the terminal Run, before Task review.
UPDATE outputs SET source_run_completed=true
FROM runs
WHERE outputs.company_id=runs.company_id AND outputs.run_id=runs.id
  AND runs.id=$1 AND runs.status='completed';
