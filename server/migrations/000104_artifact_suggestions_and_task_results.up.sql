-- Several immutable MCP deliveries may refer to one shared native Page.
-- This relation supports delivery freshness; it does not restore human resubmission.
ALTER TABLE output_documents DROP CONSTRAINT output_documents_pkey,
 DROP CONSTRAINT output_documents_company_id_output_id_page_id_fkey,
 DROP CONSTRAINT output_documents_output_item_id_key;
ALTER TABLE output_documents ADD PRIMARY KEY(output_item_id),
 ADD FOREIGN KEY(company_id,page_id) REFERENCES wiki_pages(company_id,id) ON DELETE CASCADE;
CREATE INDEX output_documents_page_idx ON output_documents(company_id,page_id);

-- Suggestion decisions are separate from canonical document state.
ALTER TABLE project_page_changes DROP CONSTRAINT project_page_changes_status_check;
ALTER TABLE project_page_changes ADD CONSTRAINT project_page_changes_status_check
 CHECK(status IN ('applied','proposed','rejected','undone','partially_applied','needs_update','superseded','resolved'));
ALTER TABLE project_page_changes ADD COLUMN decisions jsonb NOT NULL DEFAULT '{}'::jsonb,
 ADD COLUMN supersedes_id uuid,
 ADD CONSTRAINT project_page_changes_company_key UNIQUE(company_id,id),
 ADD CONSTRAINT project_page_changes_supersedes_fkey FOREIGN KEY(company_id,supersedes_id)
 REFERENCES project_page_changes(company_id,id);
CREATE UNIQUE INDEX project_page_change_successor ON project_page_changes(supersedes_id) WHERE supersedes_id IS NOT NULL;
CREATE TABLE page_suggestion_reviews (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 company_id uuid NOT NULL,
 change_id uuid NOT NULL,
 actor_user_id uuid NOT NULL,
 request_key uuid NOT NULL,
 fingerprint text NOT NULL,
 action text NOT NULL CHECK(action IN ('apply','reject','undo','request_changes','resolve')),
 selection jsonb NOT NULL,
 feedback text NOT NULL DEFAULT '',
 before_sequence bigint NOT NULL CHECK(before_sequence>=0),
 after_sequence bigint NOT NULL CHECK(after_sequence>=0),
 result jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(company_id,actor_user_id,request_key),
 FOREIGN KEY(company_id,change_id) REFERENCES project_page_changes(company_id,id) ON DELETE CASCADE
);
CREATE INDEX page_suggestion_review_history ON page_suggestion_reviews(company_id,change_id,created_at,id);

ALTER TABLE project_presentations ADD COLUMN shared_content jsonb;
ALTER TABLE output_documents ADD COLUMN submitted_sequence bigint NOT NULL DEFAULT 0 CHECK(submitted_sequence>=0), ADD COLUMN submitted_content jsonb;

ALTER TABLE project_presentations ADD COLUMN change_id uuid,
 ADD COLUMN change_snapshot jsonb,
 ADD CONSTRAINT project_presentations_change_fkey FOREIGN KEY(company_id,change_id)
 REFERENCES project_page_changes(company_id,id);

ALTER TABLE project_artifacts ADD COLUMN pending_creation boolean NOT NULL DEFAULT false,
 ADD COLUMN creation_change_id uuid,
 ADD CONSTRAINT project_artifacts_creation_change_fkey FOREIGN KEY(company_id,creation_change_id)
 REFERENCES project_page_changes(company_id,id);

ALTER TABLE tasks ADD COLUMN result_proposal_only boolean NOT NULL DEFAULT false,
 ADD COLUMN result_review_required boolean NOT NULL DEFAULT true;

CREATE TABLE task_result_sets (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), company_id uuid NOT NULL,
 task_id uuid NOT NULL, task_revision bigint NOT NULL,
 execution_generation bigint NOT NULL, actor_user_id uuid NOT NULL,
 request_key uuid NOT NULL, request_hash text NOT NULL,
 approved_at timestamptz, approved_by_user_id uuid,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(company_id,id), UNIQUE(task_id,task_revision),
 UNIQUE(company_id,actor_user_id,request_key),
 FOREIGN KEY(company_id,task_id) REFERENCES tasks(company_id,id) ON DELETE CASCADE
);
CREATE TABLE task_result_items (
 company_id uuid NOT NULL, result_set_id uuid NOT NULL, ordinal integer NOT NULL,
 output_id uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(result_set_id,ordinal), UNIQUE(result_set_id,output_id),
 FOREIGN KEY(company_id,result_set_id) REFERENCES task_result_sets(company_id,id) ON DELETE CASCADE,
 FOREIGN KEY(company_id,output_id) REFERENCES outputs(company_id,id)
);

ALTER TABLE page_suggestion_reviews
 ADD COLUMN before_revision_id uuid,
 ADD COLUMN result_revision_id uuid,
 ADD COLUMN before_theme text CHECK(before_theme IN ('default','editorial','compact')),
 ADD COLUMN result_theme text CHECK(result_theme IN ('default','editorial','compact')),
 ADD CONSTRAINT page_suggestion_reviews_before_revision_fkey FOREIGN KEY(company_id,before_revision_id) REFERENCES wiki_revisions(company_id,id),
 ADD CONSTRAINT page_suggestion_reviews_result_revision_fkey FOREIGN KEY(company_id,result_revision_id) REFERENCES wiki_revisions(company_id,id);

-- Deliveries own their retained evidence independently of a Run. Frozen scope
-- preserves access when the originating Run or Task is removed.
-- File provenance is attribution. Retained file versions must not be deleted
-- with execution history while a delivery still names them.
ALTER TABLE asset_versions DROP CONSTRAINT asset_versions_source_run_fkey;
ALTER TABLE outputs ALTER COLUMN run_id DROP NOT NULL;
ALTER TABLE outputs DROP CONSTRAINT outputs_run_fkey;
ALTER TABLE outputs
 ADD COLUMN conversation_id uuid,
 ADD COLUMN task_id uuid,
 ADD COLUMN project_id uuid,
 ADD COLUMN task_execution_generation bigint,
 ADD COLUMN actor_type text NOT NULL DEFAULT 'agent' CHECK(actor_type IN ('user','agent')),
 ADD COLUMN actor_user_id uuid,
 ADD COLUMN actor_agent_id uuid,
 ADD COLUMN source_run_id uuid,
 ADD COLUMN on_behalf_of_user_id uuid,
 ADD COLUMN source_run_completed boolean NOT NULL DEFAULT false;

-- Source Run and authorizer IDs are historical attribution, without foreign keys.
-- Completion is retained evidence; deleting a failed Run never grants eligibility.
-- Existing deliveries inherit the same frozen scope and Agent attribution as
-- InsertOutput. Use the Run's generation, not the Task's current generation:
-- a later handoff must not make an older delivery eligible as current work.
UPDATE outputs AS output
SET conversation_id = source_run.conversation_id,
    task_id = source_run.task_id,
    project_id = conversation.project_id,
    task_execution_generation = source_run.task_execution_generation,
    actor_agent_id = source_run.agent_id,
    source_run_id = source_run.id,
    on_behalf_of_user_id = source_run.execution_authorized_by_user_id,
    source_run_completed = source_run.status = 'completed'
FROM runs AS source_run
JOIN conversations AS conversation
  ON conversation.company_id = source_run.company_id
 AND conversation.id = source_run.conversation_id
WHERE output.company_id = source_run.company_id
  AND output.run_id = source_run.id;

ALTER TABLE outputs
 ADD CONSTRAINT outputs_company_fkey FOREIGN KEY(company_id) REFERENCES companies(id) ON DELETE CASCADE,
 ADD CONSTRAINT outputs_run_fkey FOREIGN KEY(company_id,run_id) REFERENCES runs(company_id,id) ON DELETE SET NULL(run_id),
 ADD CONSTRAINT outputs_conversation_fkey FOREIGN KEY(company_id,conversation_id) REFERENCES conversations(company_id,id) ON DELETE SET NULL(conversation_id),
 ADD CONSTRAINT outputs_task_fkey FOREIGN KEY(company_id,task_id) REFERENCES tasks(company_id,id) ON DELETE SET NULL(task_id),
 ADD CONSTRAINT outputs_project_fkey FOREIGN KEY(company_id,project_id) REFERENCES projects(company_id,id),
 ADD CONSTRAINT outputs_actor_check CHECK((actor_type='user' AND actor_user_id IS NOT NULL AND run_id IS NULL) OR (actor_type='agent' AND actor_agent_id IS NOT NULL));

CREATE INDEX outputs_task_created ON outputs(company_id,task_id,created_at DESC,id DESC) WHERE task_id IS NOT NULL;
CREATE INDEX outputs_conversation_created ON outputs(company_id,conversation_id,created_at DESC,id DESC) WHERE conversation_id IS NOT NULL;

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
LEFT JOIN projects AS source_project
  ON source_project.company_id = output.company_id
 AND source_project.id = output.project_id
WHERE output.owner_user_id = member.user_id OR grant_row.permission IS NOT NULL
   OR (output.visibility = 'company' AND (
       output.project_id IS NULL
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

-- Run identity is retained attribution; removing execution history cannot
-- erase a result selection or make it belong to another Run.
ALTER TABLE task_result_sets ADD COLUMN source_run_id uuid;

-- Attribution survives the originating Run and Agent being removed.
ALTER TABLE project_page_changes ADD COLUMN actor_agent_id uuid;
