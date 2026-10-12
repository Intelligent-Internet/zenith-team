-- Editing is explicitly granted; Company visibility and conversation grants remain read-only.
ALTER TABLE output_grants DROP CONSTRAINT output_grants_permission_check;
ALTER TABLE output_grants ADD CONSTRAINT output_grants_permission_check CHECK(permission IN ('view','edit','manage'));
CREATE OR REPLACE VIEW output_access AS
WITH direct_access AS NOT MATERIALIZED (
SELECT output.company_id, output.id AS output_id, member.user_id,
       CASE WHEN output.owner_user_id = member.user_id OR grant_row.permission = 'manage'
            THEN 3 WHEN grant_row.permission = 'edit' THEN 2 ELSE 1 END::integer AS access_level
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


-- A document belongs to an Output item. Project artifact versions are placements,
-- not its identity. This schema targets a fresh database.
ALTER TABLE wiki_pages ADD CONSTRAINT wiki_pages_output_document_key UNIQUE(company_id,source_output_id,id);
CREATE TABLE output_documents (
 company_id uuid NOT NULL, output_id uuid NOT NULL, output_item_id uuid NOT NULL,
 page_id uuid PRIMARY KEY, UNIQUE(output_item_id),
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(company_id,output_id) REFERENCES outputs(company_id,id) ON DELETE CASCADE,
 FOREIGN KEY(output_id,output_item_id) REFERENCES output_items(output_id,id) ON DELETE CASCADE,
 FOREIGN KEY(company_id,output_id,page_id) REFERENCES wiki_pages(company_id,source_output_id,id) ON DELETE CASCADE
);
DROP TABLE canvas_document_imports;

-- One discussion store for immutable review versions and live documents.
ALTER TABLE review_threads ALTER COLUMN project_id DROP NOT NULL;
ALTER TABLE review_threads ALTER COLUMN artifact_version_id DROP NOT NULL;
ALTER TABLE review_threads ADD COLUMN output_id uuid,
 ADD CONSTRAINT review_threads_output_fkey FOREIGN KEY(company_id,output_id) REFERENCES outputs(company_id,id) ON DELETE CASCADE,
 ADD CONSTRAINT review_threads_company_key UNIQUE(company_id,id),
 ADD CONSTRAINT review_threads_document_fkey FOREIGN KEY(company_id,page_id) REFERENCES wiki_pages(company_id,id) ON DELETE CASCADE,
 ADD CONSTRAINT review_threads_subject_check CHECK(
 (project_id IS NOT NULL AND artifact_version_id IS NOT NULL AND output_id IS NULL) OR
 (project_id IS NULL AND artifact_version_id IS NULL AND output_id IS NOT NULL));
-- Resolutions retain the actor and timestamp for every review surface.
ALTER TABLE review_threads DROP CONSTRAINT review_threads_check;
ALTER TABLE review_threads ADD CONSTRAINT review_threads_resolution_check CHECK(
 (status='open' AND resolved_at IS NULL AND resolved_by_user_id IS NULL) OR
 (status='resolved' AND resolved_at IS NOT NULL AND resolved_by_user_id IS NOT NULL));
ALTER TABLE review_comments ALTER COLUMN project_id DROP NOT NULL;
ALTER TABLE review_comments ADD CONSTRAINT review_comments_subject_fkey FOREIGN KEY(company_id,thread_id) REFERENCES review_threads(company_id,id) ON DELETE CASCADE;
DROP TABLE output_review_comments;
DROP TABLE output_review_threads;
CREATE INDEX review_threads_output ON review_threads(output_id,created_at,id) WHERE output_id IS NOT NULL;

ALTER TABLE wiki_revisions ADD CONSTRAINT wiki_revisions_company_key UNIQUE(company_id,id);
ALTER TABLE review_threads ADD COLUMN document_revision_id uuid,
 ADD CONSTRAINT review_threads_document_revision_fkey FOREIGN KEY(company_id,document_revision_id) REFERENCES wiki_revisions(company_id,id) ON DELETE CASCADE;

ALTER TABLE wiki_pages ADD CONSTRAINT wiki_pages_document_owner_check CHECK(source_output_id IS NULL OR project_id IS NULL);
ALTER TABLE review_threads ADD CONSTRAINT review_threads_document_subject_check CHECK(document_revision_id IS NULL OR (output_id IS NOT NULL AND page_id IS NULL));
