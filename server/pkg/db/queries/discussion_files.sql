-- name: LinkDiscussionAttachment :one
WITH locked_attachment AS MATERIALIZED (
    SELECT attachments.id, attachments.company_id
    FROM attachments
    WHERE attachments.id = sqlc.arg(attachment_id)::uuid
      AND attachments.company_id = sqlc.arg(company_id)::uuid
      AND attachments.uploaded_by_user_id = sqlc.arg(actor_user_id)::uuid
      AND NOT EXISTS (SELECT 1 FROM asset_versions WHERE asset_versions.attachment_id=attachments.id)
      AND attachments.status = 'ready'
      AND attachments.size_bytes <= 26214400
      AND NOT EXISTS (
          SELECT 1
          FROM message_attachments
          WHERE message_attachments.attachment_id = attachments.id
      )
      AND NOT EXISTS (
          SELECT 1
          FROM conversation_run_queue_attachments
          WHERE conversation_run_queue_attachments.attachment_id = attachments.id
            AND conversation_run_queue_attachments.released_at IS NULL
      )
    FOR UPDATE OF attachments
)
INSERT INTO message_attachments (company_id, message_id, attachment_id, "position")
SELECT
    locked_attachment.company_id,
    conversation_items.id,
    locked_attachment.id,
    sqlc.arg(position)::integer
FROM locked_attachment
JOIN conversation_items
  ON conversation_items.id = sqlc.arg(message_id)::uuid
 AND conversation_items.company_id = locked_attachment.company_id
 AND conversation_items.type = 'user_message'
 AND conversation_items.author_id = sqlc.arg(actor_user_id)::uuid
RETURNING message_attachments.*;


-- name: ListDiscussionAttachments :many
-- A file the person may not open is absent from its message rather than
-- refused on download: a Project file shared into a chat shows only to that
-- Project's readers (ADR 0017). attachment_access is checked row by row.
SELECT link.message_id, sqlc.embed(attachments) FROM attachments
JOIN message_attachments link ON link.attachment_id=attachments.id AND link.company_id=attachments.company_id
CROSS JOIN LATERAL (
    SELECT 1 FROM attachment_access AS access
    WHERE access.company_id = link.company_id
      AND access.attachment_id = link.attachment_id
      AND access.user_id = sqlc.arg(actor_user_id)::uuid
    LIMIT 1
) AS readable
WHERE link.company_id=sqlc.arg(company_id)::uuid AND link.message_id=ANY(sqlc.arg(message_ids)::uuid[]) AND attachments.status='ready'
ORDER BY link.message_id, link.position;

-- name: ListRunChannelAttachments :many
-- The chat files a channel or group-DM Run freezes: those its caller, the
-- Run's Authorizer, may open (attachment_access, row by row). That already
-- hides a Project file from someone who reads none of its Projects (ADR 0017
-- decision 8). The Run uses its Authorizer's access and nothing else narrows
-- it: the person who tagged the Agent answers for what it shares.
SELECT link.message_id, sqlc.embed(attachments) FROM attachments
JOIN message_attachments link ON link.attachment_id=attachments.id AND link.company_id=attachments.company_id
CROSS JOIN LATERAL (
    SELECT 1 FROM attachment_access AS access
    WHERE access.company_id = link.company_id
      AND access.attachment_id = link.attachment_id
      AND access.user_id = sqlc.arg(actor_user_id)::uuid
    LIMIT 1
) AS readable
WHERE link.company_id=sqlc.arg(company_id)::uuid AND link.message_id=ANY(sqlc.arg(message_ids)::uuid[]) AND attachments.status='ready'
ORDER BY link.message_id, link.position;

-- name: LinkDiscussionArtifact :exec
INSERT INTO discussion_artifacts(company_id,message_id,output_id) VALUES($1,$2,$3);

-- name: ListDiscussionArtifacts :many
-- Published Agent replies refer to their exact final Run in server-owned metadata.
-- Derive these references on read so historical replies work without a backfill.
-- A reference never grants access: both sources use the current output_access view.
WITH artifact_references AS (
    SELECT link.company_id, link.message_id, link.output_id
    FROM discussion_artifacts link
    WHERE link.company_id=sqlc.arg(company_id)::uuid
      AND link.message_id=ANY(sqlc.arg(message_ids)::uuid[])
    UNION
    SELECT message.company_id, message.id AS message_id, output.id AS output_id
    FROM conversation_items message
    JOIN runs run ON run.company_id=message.company_id
      AND run.invocation_id=message.invocation_id
      AND run.id::text=message.metadata->>'runId'
    JOIN outputs output ON output.company_id=run.company_id AND output.run_id=run.id
    WHERE message.company_id=sqlc.arg(company_id)::uuid
      AND message.id=ANY(sqlc.arg(message_ids)::uuid[])
      AND message.type='agent_text' AND message.run_id IS NULL
)
SELECT reference.message_id, output.id, output.title, output.type, output.created_at,
  (SELECT count(*) FROM output_items item WHERE item.company_id=output.company_id AND item.output_id=output.id AND item.role <> 'supporting') AS file_count,
  COALESCE(file.filename, '')::text AS file_name, COALESCE(file.content_type, '')::text AS content_type,
  COALESCE((SELECT jsonb_agg(jsonb_build_object('fileName', version.filename, 'contentType', version.content_type) ORDER BY item.ordinal)
    FROM output_items item
    JOIN asset_versions version ON version.company_id=item.company_id AND version.id=item.asset_version_id
    WHERE item.company_id=output.company_id AND item.output_id=output.id AND item.role IN ('primary', 'final')), '[]'::jsonb)::jsonb AS files
FROM artifact_references reference
JOIN conversation_items message ON message.company_id=reference.company_id AND message.id=reference.message_id
JOIN outputs output ON output.id=reference.output_id AND output.company_id=reference.company_id
JOIN output_access access ON access.output_id=output.id AND access.company_id=output.company_id
  AND access.user_id=sqlc.arg(actor_user_id)::uuid
LEFT JOIN LATERAL (
  SELECT version.filename, version.content_type
  FROM output_items item
  JOIN asset_versions version ON version.company_id=item.company_id AND version.id=item.asset_version_id
  WHERE item.company_id=output.company_id AND item.output_id=output.id AND item.role <> 'supporting'
  ORDER BY (item.role='final') DESC, item.ordinal
  LIMIT 1
) file ON true
WHERE message.deleted_at IS NULL
UNION ALL
SELECT link.message_id, artifact.id, artifact.title, artifact.type, artifact.created_at,
 0::bigint AS file_count, ''::text AS file_name, ''::text AS content_type, '[]'::jsonb AS files
FROM discussion_artifacts link
JOIN conversation_items message ON message.company_id=link.company_id AND message.id=link.message_id
JOIN artifacts artifact ON artifact.company_id=link.company_id AND artifact.id=link.artifact_id
JOIN artifact_access access ON access.company_id=artifact.company_id AND access.artifact_id=artifact.id
 AND access.user_id=sqlc.arg(actor_user_id)::uuid
WHERE link.company_id=sqlc.arg(company_id)::uuid AND link.message_id=ANY(sqlc.arg(message_ids)::uuid[])
 AND message.deleted_at IS NULL AND artifact.archived_at IS NULL
ORDER BY message_id, created_at, id;
