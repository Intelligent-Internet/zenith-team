DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM output_documents) OR EXISTS(SELECT 1 FROM review_threads WHERE output_id IS NOT NULL) OR EXISTS(SELECT 1 FROM output_grants WHERE permission='edit') THEN
 RAISE EXCEPTION 'Artifact documents or Editor grants exist; restore a database backup';
 END IF;
END $$;
ALTER TABLE wiki_pages DROP CONSTRAINT wiki_pages_document_owner_check;
ALTER TABLE review_threads DROP CONSTRAINT review_threads_document_subject_check;
DROP TABLE output_documents;
ALTER TABLE wiki_pages DROP CONSTRAINT wiki_pages_output_document_key;
ALTER TABLE review_threads DROP COLUMN document_revision_id;
ALTER TABLE wiki_revisions DROP CONSTRAINT wiki_revisions_company_key;
ALTER TABLE review_comments DROP CONSTRAINT review_comments_subject_fkey;
ALTER TABLE review_comments ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE review_threads DROP CONSTRAINT review_threads_subject_check;
ALTER TABLE review_threads DROP CONSTRAINT review_threads_resolution_check;
ALTER TABLE review_threads DROP CONSTRAINT review_threads_document_fkey;
ALTER TABLE review_threads DROP CONSTRAINT review_threads_company_key;
ALTER TABLE review_threads DROP COLUMN output_id;
ALTER TABLE review_threads ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE review_threads ALTER COLUMN artifact_version_id SET NOT NULL;
ALTER TABLE review_threads ADD CONSTRAINT review_threads_check CHECK((status='resolved')=(resolved_at IS NOT NULL AND resolved_by_user_id IS NOT NULL));
ALTER TABLE output_grants DROP CONSTRAINT output_grants_permission_check;
ALTER TABLE output_grants ADD CONSTRAINT output_grants_permission_check CHECK(permission IN ('view','manage'));
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

-- Immutable Outputs can be reviewed without a Project. Access remains output_access.
CREATE TABLE output_review_threads (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 company_id uuid NOT NULL, output_id uuid NOT NULL,
 anchor jsonb NOT NULL CHECK (jsonb_typeof(anchor) = 'object'),
 status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
 revision bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (company_id, output_id, id),
 FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE
);
CREATE INDEX output_review_threads_output ON output_review_threads(output_id, created_at, id);
CREATE TABLE output_review_comments (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 company_id uuid NOT NULL, output_id uuid NOT NULL, thread_id uuid NOT NULL,
 author_user_id uuid NOT NULL, body text NOT NULL,
 revision bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), edited_at timestamptz, deleted_at timestamptz,
 FOREIGN KEY (company_id, output_id, thread_id) REFERENCES output_review_threads(company_id, output_id, id) ON DELETE CASCADE
);
CREATE INDEX output_review_comments_thread ON output_review_comments(thread_id, created_at, id);
