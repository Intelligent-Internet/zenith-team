-- Refuse destructive downgrades before changing the schema.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM project_presentations) OR EXISTS(SELECT 1 FROM project_page_changes)
 OR EXISTS(SELECT 1 FROM wiki_pages WHERE project_id IS NOT NULL)
 OR EXISTS(SELECT 1 FROM project_canvases WHERE overview_page_id IS NOT NULL)
 OR EXISTS(SELECT 1 FROM project_artifacts WHERE source_run_id IS NOT NULL) THEN
  RAISE EXCEPTION 'Project overview data exists; restore a verified backup rather than discard content';
 END IF;
END $$;

DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM company_wiki_pages WHERE current_revision_id IS NULL)
 OR EXISTS(SELECT 1 FROM wiki_canvas_drafts d JOIN company_wiki_pages p ON p.id=d.page_id
   WHERE d.title<>p.title OR d.summary<>p.summary OR d.collection_id IS DISTINCT FROM p.collection_id) THEN
   RAISE EXCEPTION 'publish or preserve Wiki drafts before downgrading';
 END IF;
END $$;

DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM wiki_pages WHERE source_output_id IS NOT NULL) THEN
  RAISE EXCEPTION 'export and remove Output documents before downgrading Project Pages';
 END IF;
END $$;

DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM wiki_canvas_drafts WHERE schema_version=1)
 OR EXISTS(SELECT 1 FROM wiki_revisions WHERE block_content<>'{}'::jsonb)
 OR EXISTS(SELECT 1 FROM canvas_templates)
 OR EXISTS(SELECT 1 FROM canvas_personal_views)
 OR EXISTS(SELECT 1 FROM canvas_items WHERE version_policy='draft') THEN
  RAISE EXCEPTION 'Block document downgrade requires restoring a pre-migration backup';
 END IF;
END $$;

ALTER TABLE review_threads DROP COLUMN page_id;

CREATE OR REPLACE VIEW output_access AS
SELECT output.company_id, output.id AS output_id, member.user_id,
       CASE WHEN output.owner_user_id = member.user_id OR grant_row.permission = 'manage'
            THEN 3 ELSE 1 END::integer AS access_level
FROM outputs AS output
JOIN company_members AS member
  ON member.company_id = output.company_id AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
JOIN companies AS company ON company.id = output.company_id AND company.deleting_at IS NULL
LEFT JOIN output_grants AS grant_row
  ON grant_row.company_id = output.company_id AND grant_row.output_id = output.id
 AND grant_row.user_id = member.user_id
 AND grant_row.updated_at >= COALESCE(member.reactivated_at, member.created_at)
LEFT JOIN runs AS source_run
  ON source_run.company_id = output.company_id AND source_run.id = output.run_id
LEFT JOIN conversations AS source_conversation
  ON source_conversation.company_id = source_run.company_id
 AND source_conversation.id = source_run.conversation_id
LEFT JOIN projects AS source_project
  ON source_project.company_id = source_conversation.company_id
 AND source_project.id = source_conversation.project_id
WHERE output.owner_user_id = member.user_id OR grant_row.permission IS NOT NULL
   OR (output.visibility = 'company' AND (
       source_conversation.project_id IS NULL
       OR source_project.visibility = 'company'
       OR EXISTS (
           SELECT 1 FROM project_grants AS project_grant
           WHERE project_grant.company_id = source_project.company_id
             AND project_grant.project_id = source_project.id
             AND project_grant.user_id = member.user_id
             AND project_grant.permission = project_grant.read_permission
             AND project_grant.updated_at >= COALESCE(member.reactivated_at, member.created_at)
       )
   ))
   OR EXISTS (
       SELECT 1 FROM output_conversation_grants AS conversation_grant
       JOIN conversation_members AS participant
         ON participant.company_id = conversation_grant.company_id
        AND participant.conversation_id = conversation_grant.conversation_id
        AND participant.user_id = member.user_id AND participant.status = 'active'
       JOIN conversations AS conversation
         ON conversation.id = conversation_grant.conversation_id
        AND conversation.company_id = conversation_grant.company_id
        AND conversation.kind IN ('channel', 'human_direct')
       WHERE conversation_grant.company_id = output.company_id
         AND conversation_grant.output_id = output.id
   );

DROP TABLE project_page_managed_blocks;
ALTER TABLE wiki_block_ids DROP CONSTRAINT wiki_block_ids_scope_key;
DROP TABLE project_page_changes;
DROP TABLE project_presentation_sources;
DROP TABLE project_presentations;
ALTER TABLE project_canvases DROP COLUMN overview_page_id;

ALTER TABLE project_artifacts DROP COLUMN source_run_id;

DROP VIEW company_wiki_pages;
ALTER TABLE wiki_pages DROP COLUMN project_id, DROP COLUMN theme;
CREATE VIEW company_wiki_pages AS SELECT * FROM wiki_pages WHERE source_output_id IS NULL;

ALTER TABLE wiki_revisions DROP COLUMN published_at;
ALTER TABLE wiki_canvas_drafts DROP COLUMN title,DROP COLUMN summary,DROP COLUMN collection_id,DROP COLUMN restored_revision_id;

CREATE OR REPLACE VIEW attachment_access AS
SELECT arm.company_id, arm.attachment_id, arm.user_id
FROM (
    SELECT DISTINCT file.company_id, file.attachment_id, access.user_id
    FROM project_files AS file
    JOIN project_access AS access
      ON access.company_id = file.company_id AND access.project_id = file.project_id
    WHERE file.removed_at IS NULL
    UNION ALL
    SELECT link.company_id, link.attachment_id, candidate.user_id
    FROM message_attachments AS link
    JOIN conversation_items AS item
      ON item.company_id = link.company_id
     AND item.id = link.message_id
     AND item.deleted_at IS NULL
    JOIN company_members AS candidate
      ON candidate.company_id = link.company_id
    CROSS JOIN LATERAL (
        SELECT 1
        FROM conversation_readers AS reader
        WHERE reader.company_id = item.company_id
          AND reader.conversation_id = item.conversation_id
          AND reader.user_id = candidate.user_id
        LIMIT 1
    ) AS reads_chat
    WHERE NOT EXISTS (
        SELECT 1 FROM project_files AS file
        WHERE file.company_id = link.company_id
          AND file.attachment_id = link.attachment_id
          AND file.removed_at IS NULL
    )
    UNION ALL
    SELECT attachment.company_id, attachment.id, member.user_id
    FROM attachments AS attachment
    JOIN companies AS company
      ON company.id = attachment.company_id AND company.deleting_at IS NULL
    JOIN company_members AS member
      ON member.company_id = attachment.company_id
     AND member.user_id = attachment.uploaded_by_user_id
     AND member.status = 'active'
    JOIN users AS account
      ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
    WHERE NOT EXISTS (
        SELECT 1 FROM message_attachments AS link
        WHERE link.company_id = attachment.company_id AND link.attachment_id = attachment.id
    )
      AND NOT EXISTS (
        SELECT 1 FROM project_files AS file
        WHERE file.company_id = attachment.company_id
          AND file.attachment_id = attachment.id
          AND file.removed_at IS NULL
    )
) AS arm
WHERE NOT EXISTS (
    SELECT 1
    FROM asset_versions AS artifact_version
    JOIN output_items AS artifact_item
      ON artifact_item.asset_version_id = artifact_version.id
     AND artifact_item.company_id = artifact_version.company_id
    WHERE artifact_version.attachment_id = arm.attachment_id
      AND artifact_version.company_id = arm.company_id
      AND NOT EXISTS (
          SELECT 1 FROM output_access AS access
          WHERE access.company_id = artifact_item.company_id
            AND access.output_id = artifact_item.output_id
            AND access.user_id = arm.user_id
      )
);
DROP VIEW project_attachment_scopes;

-- Project canvas and collaborative document schema.

-- Output documents
DROP TABLE canvas_document_imports;
DROP VIEW canvas_version_sources;
DROP VIEW company_wiki_pages;
ALTER TABLE wiki_pages DROP COLUMN source_output_id;

-- Document sync
ALTER TABLE wiki_canvas_drafts DROP COLUMN structure_seq;

-- Block documents
DROP TABLE canvas_personal_views,canvas_templates,wiki_block_ids;
ALTER TABLE canvas_items DROP CONSTRAINT canvas_items_version_selection_check;
ALTER TABLE canvas_items ADD CONSTRAINT canvas_items_check2 CHECK(version_policy='pinned' OR pinned_version_id IS NULL);
ALTER TABLE canvas_items DROP CONSTRAINT canvas_items_version_policy_check;
ALTER TABLE canvas_items ADD CONSTRAINT canvas_items_version_policy_check CHECK(version_policy IN ('pinned','accepted'));
ALTER TABLE project_artifact_versions DROP COLUMN branch_title;
ALTER TABLE wiki_revisions DROP COLUMN block_content;
ALTER TABLE wiki_canvas_drafts DROP COLUMN schema_version,DROP COLUMN content,DROP COLUMN legacy_state,DROP COLUMN legacy_markdown;

-- Canvas task artifacts
ALTER TABLE project_artifacts DROP COLUMN task_id;

-- Canvas views
DROP INDEX artifact_review_decisions_project_time;
DROP INDEX project_artifact_versions_project_time;
DROP INDEX artifact_review_decisions_version_time;

-- Canvas output lookup
DROP INDEX project_artifact_versions_output;

-- Project canvas
DROP TABLE wiki_canvas_checkpoints, wiki_canvas_drafts;
ALTER TABLE wiki_pages DROP COLUMN next_revision_number;
DROP TABLE project_canvas_operations, artifact_review_decisions, review_thread_events, review_comment_revisions, review_comments, review_threads, canvas_edges, canvas_items;
ALTER TABLE project_artifacts DROP CONSTRAINT project_artifacts_accepted_fkey;
DROP TABLE project_artifact_versions, project_artifacts, project_file_assets, project_canvases;
-- Human versions cannot be represented by the older schema; reject rollback until explicitly disposed.
ALTER TABLE asset_versions ALTER COLUMN source_run_id SET NOT NULL;
ALTER TABLE asset_versions DROP CONSTRAINT asset_versions_origin_check, DROP COLUMN source_type, DROP COLUMN created_by_user_id;
