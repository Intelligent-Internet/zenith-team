ALTER TABLE project_page_changes DROP COLUMN actor_agent_id;

ALTER TABLE task_result_sets DROP COLUMN source_run_id;

DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM outputs WHERE run_id IS NULL) THEN
  RAISE EXCEPTION 'cannot remove independent deliveries while retained deliveries without Runs exist';
 END IF;
END $$;
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
       OR EXISTS (
           SELECT 1 FROM company_group_members AS group_member
           JOIN project_group_grants AS group_grant
             ON group_grant.company_id = group_member.company_id
            AND group_grant.project_id = source_project.id
            AND group_grant.group_id = group_member.group_id
            AND group_grant.permission = group_grant.read_permission
           WHERE group_member.company_id = source_project.company_id
             AND group_member.user_id = member.user_id
             AND group_member.created_at >= COALESCE(member.reactivated_at, member.created_at)
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
ALTER TABLE outputs DROP CONSTRAINT outputs_run_fkey;
ALTER TABLE outputs ADD CONSTRAINT outputs_run_fkey FOREIGN KEY(company_id,run_id) REFERENCES runs(company_id,id) ON DELETE CASCADE;
ALTER TABLE outputs ALTER COLUMN run_id SET NOT NULL;
ALTER TABLE outputs DROP CONSTRAINT outputs_company_fkey,
 DROP COLUMN conversation_id,DROP COLUMN task_id,DROP COLUMN project_id,DROP COLUMN task_execution_generation,
 DROP COLUMN actor_type,DROP COLUMN actor_user_id,DROP COLUMN actor_agent_id,
 DROP COLUMN source_run_id,DROP COLUMN on_behalf_of_user_id,DROP COLUMN source_run_completed;

ALTER TABLE asset_versions ADD CONSTRAINT asset_versions_source_run_fkey FOREIGN KEY(company_id,source_run_id) REFERENCES runs(company_id,id) ON DELETE CASCADE;

ALTER TABLE page_suggestion_reviews DROP COLUMN before_revision_id, DROP COLUMN result_revision_id,
 DROP COLUMN before_theme, DROP COLUMN result_theme;

DROP TABLE task_result_items;
DROP TABLE task_result_sets;
ALTER TABLE tasks DROP COLUMN result_proposal_only, DROP COLUMN result_review_required;

ALTER TABLE project_artifacts DROP CONSTRAINT project_artifacts_creation_change_fkey,
 DROP COLUMN creation_change_id, DROP COLUMN pending_creation;

ALTER TABLE project_presentations DROP CONSTRAINT project_presentations_change_fkey,
 DROP COLUMN change_snapshot, DROP COLUMN change_id;

ALTER TABLE output_documents DROP COLUMN submitted_content, DROP COLUMN submitted_sequence;
ALTER TABLE project_presentations DROP COLUMN shared_content;

DROP TABLE page_suggestion_reviews;
DROP INDEX project_page_change_successor;
ALTER TABLE project_page_changes DROP CONSTRAINT project_page_changes_supersedes_fkey,
 DROP CONSTRAINT project_page_changes_company_key, DROP COLUMN supersedes_id, DROP COLUMN decisions;
ALTER TABLE project_page_changes DROP CONSTRAINT project_page_changes_status_check;
ALTER TABLE project_page_changes ADD CONSTRAINT project_page_changes_status_check
 CHECK(status IN ('applied','proposed','rejected','undone'));

DROP INDEX output_documents_page_idx;
ALTER TABLE output_documents DROP CONSTRAINT output_documents_company_id_page_id_fkey,
 DROP CONSTRAINT output_documents_pkey;
ALTER TABLE output_documents ADD PRIMARY KEY(page_id),
 ADD UNIQUE(output_item_id),
 ADD FOREIGN KEY(company_id,output_id,page_id) REFERENCES wiki_pages(company_id,source_output_id,id) ON DELETE CASCADE;
