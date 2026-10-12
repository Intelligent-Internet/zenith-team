-- Project Pages, collaborative documents, Canvas, Wiki drafts and Page comments.
-- Upgrades the main schema at version 000088 in one atomic migration.

-- Project canvas
-- Project canvas owns arrangement and review; source owners retain their content.
CREATE TABLE project_canvases (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), company_id uuid NOT NULL, project_id uuid NOT NULL,
 title text NOT NULL DEFAULT 'Project canvas', head_seq bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(project_id), UNIQUE(company_id,project_id,id),
 FOREIGN KEY(company_id,project_id) REFERENCES projects(company_id,id) ON DELETE CASCADE
);
CREATE TABLE project_artifacts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), company_id uuid NOT NULL, project_id uuid NOT NULL,
 title text NOT NULL, accepted_version_id uuid,
 next_number bigint NOT NULL DEFAULT 1, version bigint NOT NULL DEFAULT 1,
 created_by_user_id uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), archived_at timestamptz,
 UNIQUE(company_id,project_id,id), FOREIGN KEY(company_id,project_id) REFERENCES projects(company_id,id) ON DELETE CASCADE
);
ALTER TABLE asset_versions ALTER COLUMN source_run_id DROP NOT NULL;
ALTER TABLE asset_versions ADD COLUMN source_type text NOT NULL DEFAULT 'run' CHECK(source_type IN ('run','human')),
 ADD COLUMN created_by_user_id uuid,
 ADD CONSTRAINT asset_versions_origin_check CHECK((source_type='run' AND source_run_id IS NOT NULL) OR (source_type='human' AND source_run_id IS NULL AND created_by_user_id IS NOT NULL));
CREATE TABLE project_file_assets (
 company_id uuid NOT NULL, project_id uuid NOT NULL, asset_id uuid PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(company_id,project_id) REFERENCES projects(company_id,id) ON DELETE CASCADE,
 FOREIGN KEY(company_id,asset_id) REFERENCES assets(company_id,id) ON DELETE CASCADE
);
CREATE TABLE project_artifact_versions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), company_id uuid NOT NULL, project_id uuid NOT NULL, artifact_id uuid NOT NULL,
 number bigint NOT NULL, source_type text NOT NULL CHECK(source_type IN ('wiki','file','output')),
 wiki_page_id uuid, wiki_revision_id uuid, asset_version_id uuid, output_id uuid,
 base_version_id uuid, created_by_user_id uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(artifact_id,number), UNIQUE(company_id,project_id,artifact_id,id), UNIQUE(company_id,project_id,id),
 FOREIGN KEY(company_id,project_id,artifact_id) REFERENCES project_artifacts(company_id,project_id,id) ON DELETE CASCADE,
 FOREIGN KEY(company_id,wiki_page_id,wiki_revision_id) REFERENCES wiki_revisions(company_id,page_id,id) ON DELETE SET NULL(wiki_page_id,wiki_revision_id),
 FOREIGN KEY(company_id,asset_version_id) REFERENCES asset_versions(company_id,id) ON DELETE SET NULL(asset_version_id),
 FOREIGN KEY(company_id,output_id) REFERENCES outputs(company_id,id) ON DELETE SET NULL(output_id),
 FOREIGN KEY(company_id,project_id,artifact_id,base_version_id) REFERENCES project_artifact_versions(company_id,project_id,artifact_id,id) ON DELETE SET NULL(base_version_id),
 CHECK((source_type='wiki' AND asset_version_id IS NULL AND output_id IS NULL AND ((wiki_page_id IS NULL)=(wiki_revision_id IS NULL))) OR
 (source_type='file' AND wiki_page_id IS NULL AND wiki_revision_id IS NULL AND output_id IS NULL) OR
 (source_type='output' AND wiki_page_id IS NULL AND wiki_revision_id IS NULL AND asset_version_id IS NULL))
);
ALTER TABLE project_artifacts ADD CONSTRAINT project_artifacts_accepted_fkey FOREIGN KEY(company_id,project_id,id,accepted_version_id)
 REFERENCES project_artifact_versions(company_id,project_id,artifact_id,id) ON DELETE SET NULL(accepted_version_id) DEFERRABLE INITIALLY DEFERRED;
CREATE INDEX project_artifacts_list ON project_artifacts(project_id,updated_at DESC,id) WHERE archived_at IS NULL;
CREATE TABLE canvas_items (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), company_id uuid NOT NULL, project_id uuid NOT NULL, canvas_id uuid NOT NULL,
 item_type text NOT NULL CHECK(item_type IN ('artifact','frame')), artifact_id uuid, pinned_version_id uuid,
 version_policy text NOT NULL DEFAULT 'pinned' CHECK(version_policy IN ('pinned','accepted')),
 parent_frame_id uuid, title text NOT NULL DEFAULT '',
 x double precision NOT NULL DEFAULT 0, y double precision NOT NULL DEFAULT 0,
 width double precision NOT NULL DEFAULT 320, height double precision NOT NULL DEFAULT 260,
 stack_order bigint NOT NULL DEFAULT 0, representation text NOT NULL DEFAULT 'preview' CHECK(representation IN ('compact','excerpt','preview')),
 view_config jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(view_config)='object'),
 geometry_revision bigint NOT NULL DEFAULT 1, content_revision bigint NOT NULL DEFAULT 1,
 created_by_user_id uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), deleted_at timestamptz,
 UNIQUE(company_id,project_id,canvas_id,id),
 FOREIGN KEY(company_id,project_id,canvas_id) REFERENCES project_canvases(company_id,project_id,id) ON DELETE CASCADE,
 FOREIGN KEY(company_id,project_id,artifact_id) REFERENCES project_artifacts(company_id,project_id,id) ON DELETE CASCADE,
 FOREIGN KEY(company_id,project_id,artifact_id,pinned_version_id) REFERENCES project_artifact_versions(company_id,project_id,artifact_id,id) ON DELETE SET NULL(pinned_version_id),
 FOREIGN KEY(company_id,project_id,canvas_id,parent_frame_id) REFERENCES canvas_items(company_id,project_id,canvas_id,id) ON DELETE SET NULL(parent_frame_id),
 CHECK((item_type='frame' AND artifact_id IS NULL AND pinned_version_id IS NULL) OR (item_type='artifact' AND artifact_id IS NOT NULL)),
 CHECK(parent_frame_id IS DISTINCT FROM id), CHECK(version_policy='pinned' OR pinned_version_id IS NULL)
);
CREATE INDEX canvas_items_board ON canvas_items(canvas_id,id) WHERE deleted_at IS NULL;
CREATE INDEX canvas_items_artifact ON canvas_items(artifact_id) WHERE deleted_at IS NULL;
CREATE TABLE canvas_edges (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), company_id uuid NOT NULL, project_id uuid NOT NULL, canvas_id uuid NOT NULL,
 source_item_id uuid NOT NULL, target_item_id uuid NOT NULL, label text NOT NULL DEFAULT '',
 version bigint NOT NULL DEFAULT 1, deleted_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(company_id,project_id,canvas_id) REFERENCES project_canvases(company_id,project_id,id) ON DELETE CASCADE,
 FOREIGN KEY(company_id,project_id,canvas_id,source_item_id) REFERENCES canvas_items(company_id,project_id,canvas_id,id) ON DELETE CASCADE,
 FOREIGN KEY(company_id,project_id,canvas_id,target_item_id) REFERENCES canvas_items(company_id,project_id,canvas_id,id) ON DELETE CASCADE,
 CHECK(source_item_id<>target_item_id)
);
CREATE UNIQUE INDEX canvas_edges_pair ON canvas_edges(canvas_id,source_item_id,target_item_id) WHERE deleted_at IS NULL;
CREATE TABLE review_threads (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), company_id uuid NOT NULL, project_id uuid NOT NULL, artifact_version_id uuid NOT NULL,
 anchor jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(anchor)='object'),
 status text NOT NULL DEFAULT 'open' CHECK(status IN ('open','resolved')), version bigint NOT NULL DEFAULT 1,
 created_by_user_id uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), resolved_by_user_id uuid, resolved_at timestamptz,
 UNIQUE(company_id,project_id,id),
 FOREIGN KEY(company_id,project_id,artifact_version_id) REFERENCES project_artifact_versions(company_id,project_id,id) ON DELETE CASCADE,
 CHECK((status='resolved')=(resolved_at IS NOT NULL AND resolved_by_user_id IS NOT NULL))
);
CREATE INDEX review_threads_version ON review_threads(artifact_version_id,created_at,id);
CREATE TABLE review_comments (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), company_id uuid NOT NULL, project_id uuid NOT NULL, thread_id uuid NOT NULL,
 body text NOT NULL, version bigint NOT NULL DEFAULT 1, author_user_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), edited_at timestamptz, deleted_at timestamptz,
 UNIQUE(company_id,project_id,id), FOREIGN KEY(company_id,project_id,thread_id) REFERENCES review_threads(company_id,project_id,id) ON DELETE CASCADE
);
CREATE INDEX review_comments_thread ON review_comments(thread_id,created_at,id);
CREATE TABLE review_comment_revisions (
 comment_id uuid NOT NULL REFERENCES review_comments(id) ON DELETE CASCADE, revision bigint NOT NULL,
 body text NOT NULL, author_user_id uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(comment_id,revision)
);
CREATE TABLE review_thread_events (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), thread_id uuid NOT NULL REFERENCES review_threads(id) ON DELETE CASCADE,
 action text NOT NULL CHECK(action IN ('resolved','reopened')), actor_user_id uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE artifact_review_decisions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), company_id uuid NOT NULL, project_id uuid NOT NULL, artifact_id uuid NOT NULL,
 version_id uuid NOT NULL, action text NOT NULL CHECK(action IN ('accept','reject','request_changes','keep_variant')),
 feedback text NOT NULL DEFAULT '', actor_user_id uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(company_id,project_id,artifact_id,version_id) REFERENCES project_artifact_versions(company_id,project_id,artifact_id,id) ON DELETE CASCADE
);
CREATE TABLE project_canvas_operations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), company_id uuid NOT NULL, project_id uuid NOT NULL, seq bigint NOT NULL,
 actor_user_id uuid NOT NULL, request_key uuid NOT NULL, request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 changes jsonb NOT NULL, result jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(company_id,project_id) REFERENCES projects(company_id,id) ON DELETE CASCADE,
 UNIQUE(project_id,seq), UNIQUE(project_id,actor_user_id,request_key)
);
ALTER TABLE wiki_pages ADD COLUMN next_revision_number bigint NOT NULL DEFAULT 1;
UPDATE wiki_pages p SET next_revision_number=COALESCE((SELECT max(r.number)+1 FROM wiki_revisions r WHERE r.page_id=p.id),1);
CREATE TABLE wiki_canvas_drafts (
 company_id uuid NOT NULL, page_id uuid PRIMARY KEY, state bytea NOT NULL,
 markdown text NOT NULL, head_seq bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(company_id,page_id) REFERENCES wiki_pages(company_id,id) ON DELETE CASCADE
);
CREATE TABLE wiki_canvas_checkpoints (
 company_id uuid NOT NULL, page_id uuid NOT NULL, revision_id uuid PRIMARY KEY, through_seq bigint NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(company_id,page_id,revision_id) REFERENCES wiki_revisions(company_id,page_id,id) ON DELETE CASCADE
);

-- Canvas output lookup
CREATE INDEX project_artifact_versions_output ON project_artifact_versions
(company_id, output_id, project_id, created_at, id) WHERE output_id IS NOT NULL;

-- Canvas views
-- Latest review state and project timeline are projections, not new state stores.
CREATE INDEX artifact_review_decisions_version_time ON artifact_review_decisions(version_id,created_at DESC,id DESC);
CREATE INDEX project_artifact_versions_project_time ON project_artifact_versions(project_id,created_at DESC,id DESC);
CREATE INDEX artifact_review_decisions_project_time ON artifact_review_decisions(project_id,created_at DESC,id DESC);

-- Canvas task artifacts
ALTER TABLE project_artifacts ADD COLUMN task_id uuid,
    ADD CONSTRAINT project_artifacts_task_fkey
    FOREIGN KEY(company_id,project_id,task_id) REFERENCES tasks(company_id,project_id,id)
    ON DELETE SET NULL(task_id);
CREATE INDEX project_artifacts_task ON project_artifacts(task_id) WHERE task_id IS NOT NULL;

-- Block documents
ALTER TABLE wiki_canvas_drafts
 ADD COLUMN schema_version integer NOT NULL DEFAULT 0 CHECK(schema_version IN (0,1)),
 ADD COLUMN content jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(content)='object'),
 ADD COLUMN legacy_state bytea,
 ADD COLUMN legacy_markdown text;
ALTER TABLE wiki_revisions
 ADD COLUMN block_content jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(block_content)='object');

-- Reserved identities belong to the source document; clients never mint entity IDs.
CREATE TABLE wiki_block_ids (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), company_id uuid NOT NULL, page_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(company_id,page_id) REFERENCES wiki_pages(company_id,id) ON DELETE CASCADE
);
CREATE INDEX wiki_block_ids_page ON wiki_block_ids(page_id);

ALTER TABLE project_artifact_versions ADD COLUMN branch_title text NOT NULL DEFAULT 'Main';
ALTER TABLE canvas_items DROP CONSTRAINT canvas_items_version_policy_check;
ALTER TABLE canvas_items ADD CONSTRAINT canvas_items_version_policy_check CHECK(version_policy IN ('pinned','accepted','draft'));
ALTER TABLE canvas_items DROP CONSTRAINT canvas_items_check2;
ALTER TABLE canvas_items ADD CONSTRAINT canvas_items_version_selection_check CHECK(version_policy<>'accepted' OR pinned_version_id IS NULL);

CREATE TABLE canvas_templates (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), company_id uuid NOT NULL, project_id uuid NOT NULL,
 title text NOT NULL, content jsonb NOT NULL CHECK(jsonb_typeof(content)='object'),
 created_by_user_id uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(company_id,project_id) REFERENCES projects(company_id,id) ON DELETE CASCADE
);
CREATE TABLE canvas_personal_views (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), company_id uuid NOT NULL, project_id uuid NOT NULL, user_id uuid NOT NULL,
 title text NOT NULL, config jsonb NOT NULL CHECK(jsonb_typeof(config)='object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(company_id,project_id) REFERENCES projects(company_id,id) ON DELETE CASCADE,
 FOREIGN KEY(company_id,user_id) REFERENCES company_members(company_id,user_id) ON DELETE CASCADE
);

-- Document sync
-- A structural revision fences text written against XML that was split or moved.
ALTER TABLE wiki_canvas_drafts ADD COLUMN structure_seq bigint NOT NULL DEFAULT 0;
UPDATE wiki_canvas_drafts SET structure_seq=head_seq;
ALTER TABLE wiki_canvas_drafts ADD CONSTRAINT wiki_canvas_drafts_structure_seq_check
 CHECK (structure_seq >= 0 AND structure_seq <= head_seq);

-- Output documents
-- Output-derived documents remain under the revocable Output ACL. They are
-- document-engine pages, never implicitly published into the Company Wiki.
ALTER TABLE wiki_pages ADD COLUMN source_output_id uuid,
 ADD CONSTRAINT wiki_pages_source_output_fkey FOREIGN KEY(company_id,source_output_id)
 REFERENCES outputs(company_id,id) ON DELETE CASCADE;
CREATE INDEX wiki_pages_source_output_idx ON wiki_pages(source_output_id) WHERE source_output_id IS NOT NULL;

CREATE VIEW company_wiki_pages AS SELECT * FROM wiki_pages WHERE source_output_id IS NULL;
CREATE VIEW canvas_version_sources AS
 SELECT v.*, COALESCE(v.output_id,p.source_output_id) AS access_output_id
 FROM project_artifact_versions v LEFT JOIN wiki_pages p ON p.company_id=v.company_id AND p.id=v.wiki_page_id;

CREATE TABLE canvas_document_imports (
 company_id uuid NOT NULL, project_id uuid NOT NULL, artifact_id uuid NOT NULL,
 source_version_id uuid NOT NULL, output_id uuid NOT NULL, output_item_id uuid NOT NULL,
 page_id uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(artifact_id,output_item_id),
 FOREIGN KEY(company_id,project_id,artifact_id,source_version_id)
 REFERENCES project_artifact_versions(company_id,project_id,artifact_id,id) ON DELETE CASCADE,
 FOREIGN KEY(company_id,output_id) REFERENCES outputs(company_id,id) ON DELETE CASCADE,
 FOREIGN KEY(output_id,output_item_id) REFERENCES output_items(output_id,id) ON DELETE CASCADE,
 FOREIGN KEY(company_id,page_id) REFERENCES wiki_pages(company_id,id) ON DELETE CASCADE
);

-- Both Project file representations share the same download and chat boundary.
CREATE VIEW project_attachment_scopes AS
SELECT company_id,project_id,attachment_id,removed_at FROM project_files
UNION
SELECT file.company_id,file.project_id,version.attachment_id,NULL::timestamptz AS removed_at
FROM project_file_assets file JOIN asset_versions version
ON version.company_id=file.company_id AND version.asset_id=file.asset_id;

CREATE OR REPLACE VIEW attachment_access AS
SELECT arm.company_id, arm.attachment_id, arm.user_id
FROM (
    SELECT DISTINCT file.company_id, file.attachment_id, access.user_id
    FROM project_attachment_scopes AS file
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
        SELECT 1 FROM project_attachment_scopes AS file
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
        SELECT 1 FROM project_attachment_scopes AS file
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

-- Task origin is attribution and survives a Run being removed.
ALTER TABLE project_artifacts ADD COLUMN source_run_id uuid;

ALTER TABLE wiki_pages ADD COLUMN project_id uuid,
 ADD COLUMN theme text NOT NULL DEFAULT 'default' CHECK(theme IN ('default','editorial','compact')),
 ADD CONSTRAINT wiki_project_fkey FOREIGN KEY(company_id,project_id) REFERENCES projects(company_id,id) ON DELETE CASCADE,
 ADD CONSTRAINT wiki_project_page_key UNIQUE(company_id,project_id,id);
CREATE OR REPLACE VIEW company_wiki_pages AS SELECT * FROM wiki_pages WHERE source_output_id IS NULL AND project_id IS NULL;
ALTER TABLE project_canvases ADD COLUMN overview_page_id uuid,
 ADD CONSTRAINT project_overview_page_fkey FOREIGN KEY(company_id,project_id,overview_page_id)
 REFERENCES wiki_pages(company_id,project_id,id) ON DELETE SET NULL(overview_page_id);

-- A presentation owns capture evidence, not another editable document tree.
CREATE TABLE project_presentations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), company_id uuid NOT NULL, project_id uuid NOT NULL,
 page_id uuid NOT NULL, page_version_id uuid NOT NULL, theme text NOT NULL CHECK(theme IN ('default','editorial','compact')), title text NOT NULL, content jsonb NOT NULL,
 documents jsonb NOT NULL DEFAULT '{}', export_markdown text NOT NULL,
 source_sequence bigint NOT NULL, fingerprint text NOT NULL, request_key uuid NOT NULL,
 created_by_user_id uuid NOT NULL, source_run_id uuid, output_id uuid, export_attachment_id uuid,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(company_id,project_id,id), UNIQUE(project_id,created_by_user_id,request_key), UNIQUE(output_id),
 FOREIGN KEY(company_id,project_id) REFERENCES projects(company_id,id) ON DELETE CASCADE,
 FOREIGN KEY(company_id,project_id,page_version_id) REFERENCES project_artifact_versions(company_id,project_id,id),
 FOREIGN KEY(company_id,project_id,page_id) REFERENCES wiki_pages(company_id,project_id,id),
 FOREIGN KEY(company_id,output_id) REFERENCES outputs(company_id,id) ON DELETE SET NULL(output_id),
 FOREIGN KEY(company_id,export_attachment_id) REFERENCES attachments(company_id,id)
);
CREATE INDEX project_presentations_history ON project_presentations(project_id,created_at DESC,id DESC);
CREATE TABLE project_presentation_sources (
 company_id uuid NOT NULL, project_id uuid NOT NULL, presentation_id uuid NOT NULL, version_id uuid NOT NULL,
 wiki_page_id uuid, wiki_revision_id uuid, asset_version_id uuid, output_id uuid,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(presentation_id,version_id),
 FOREIGN KEY(company_id,project_id,presentation_id) REFERENCES project_presentations(company_id,project_id,id) ON DELETE CASCADE,
 FOREIGN KEY(company_id,project_id,version_id) REFERENCES project_artifact_versions(company_id,project_id,id),
 FOREIGN KEY(company_id,wiki_page_id,wiki_revision_id) REFERENCES wiki_revisions(company_id,page_id,id),
 FOREIGN KEY(company_id,asset_version_id) REFERENCES asset_versions(company_id,id),
 FOREIGN KEY(company_id,output_id) REFERENCES outputs(company_id,id)
);
CREATE TABLE project_page_changes (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), company_id uuid NOT NULL, project_id uuid NOT NULL,
 page_id uuid NOT NULL, run_id uuid, actor_user_id uuid NOT NULL,
 request_key uuid NOT NULL, request_hash text NOT NULL, operations jsonb NOT NULL, before_content jsonb NOT NULL,
 before_theme text CHECK(before_theme IN ('default','editorial','compact')), after_theme text CHECK(after_theme IN ('default','editorial','compact')), result jsonb NOT NULL, summary text NOT NULL, status text NOT NULL CHECK(status IN ('applied','proposed','rejected','undone')),
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(project_id,actor_user_id,request_key),
 FOREIGN KEY(company_id,project_id) REFERENCES projects(company_id,id) ON DELETE CASCADE,
 FOREIGN KEY(company_id,project_id,page_id) REFERENCES wiki_pages(company_id,project_id,id)
);
CREATE INDEX project_page_pending_changes ON project_page_changes(project_id,page_id,created_at DESC) WHERE status='proposed';
ALTER TABLE wiki_block_ids ADD CONSTRAINT wiki_block_ids_scope_key UNIQUE(company_id,page_id,id);
CREATE TABLE project_page_managed_blocks (
 company_id uuid NOT NULL, page_id uuid NOT NULL, block_id uuid PRIMARY KEY, content_hash text NOT NULL,
 run_id uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(company_id,page_id,block_id) REFERENCES wiki_block_ids(company_id,page_id,id) ON DELETE CASCADE
);


-- Native aggregate exports retain the current access of every flattened source.
-- The direct grant calculation is unchanged for ordinary submitted Artifacts.
CREATE OR REPLACE VIEW output_access AS
WITH direct_access AS NOT MATERIALIZED (
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
   )
)
SELECT access.company_id,access.output_id,access.user_id,access.access_level
FROM direct_access access
WHERE NOT EXISTS (
 SELECT 1 FROM project_presentations presentation
 JOIN project_presentation_sources source ON source.presentation_id=presentation.id
 WHERE presentation.company_id=access.company_id AND presentation.output_id=access.output_id
 -- Sharing the result authorizes its primary snapshot, not other retained Pages.
 AND source.version_id <> presentation.page_version_id
 AND ((source.output_id IS NOT NULL
 AND NOT EXISTS (SELECT 1 FROM direct_access evidence
     WHERE evidence.company_id=source.company_id AND evidence.output_id=source.output_id AND evidence.user_id=access.user_id))
 OR (source.output_id IS NULL AND NOT EXISTS(SELECT 1 FROM project_access reader
     WHERE reader.company_id=source.company_id AND reader.project_id=source.project_id AND reader.user_id=access.user_id)))
);

-- Working metadata belongs to the same draft as the block document. Published
-- page metadata changes only with its selected immutable revision.
ALTER TABLE wiki_canvas_drafts
 ADD COLUMN title text NOT NULL DEFAULT '',
 ADD COLUMN summary text NOT NULL DEFAULT '',
 ADD COLUMN collection_id uuid,
 ADD COLUMN restored_revision_id uuid,
 ADD CONSTRAINT wiki_canvas_drafts_restored_revision_fkey FOREIGN KEY(company_id,page_id,restored_revision_id)
 REFERENCES wiki_revisions(company_id,page_id,id),
 ADD CONSTRAINT wiki_canvas_drafts_collection_fkey FOREIGN KEY(company_id,collection_id)
 REFERENCES wiki_collections(company_id,id) ON DELETE SET NULL(collection_id);
UPDATE wiki_canvas_drafts d SET title=p.title,summary=p.summary,collection_id=p.collection_id
 FROM wiki_pages p WHERE p.company_id=d.company_id AND p.id=d.page_id;

-- Historical Wiki saves were publications, except saved Canvas checkpoints
-- that never became the Wiki page's current revision.
ALTER TABLE wiki_revisions ADD COLUMN published_at timestamptz;
UPDATE wiki_revisions r SET published_at=r.created_at
 FROM company_wiki_pages p WHERE p.company_id=r.company_id AND p.id=r.page_id
 AND (p.current_revision_id=r.id OR NOT EXISTS(
   SELECT 1 FROM wiki_canvas_checkpoints c WHERE c.revision_id=r.id));

-- Working Page discussions survive checkpoints. The version retains the origin
-- of the thread; a null page_id continues to mean exact-version feedback.
ALTER TABLE review_threads ADD COLUMN page_id uuid,
 ADD CONSTRAINT review_threads_page_fkey FOREIGN KEY(company_id,project_id,page_id)
 REFERENCES wiki_pages(company_id,project_id,id) ON DELETE CASCADE;
CREATE INDEX review_threads_page ON review_threads(page_id,created_at,id) WHERE page_id IS NOT NULL;
