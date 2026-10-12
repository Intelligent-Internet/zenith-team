-- Removes Groups. Every Group membership and Group grant goes with its table,
-- so a person keeps only what their own grant, the Company-visible floor and
-- the management bridge give. The Inbox items only Groups write go first, so
-- the CHECKs can be restored; then the function and the views get back the
-- bodies the previous release reads (000086 for the function and
-- project_access, 000096 for output_access) before the tables they name go.
DELETE FROM inbox_items WHERE kind = 'project_access_granted' OR entity_type = 'project';
ALTER TABLE inbox_items
    DROP CONSTRAINT inbox_items_kind_check,
    ADD CONSTRAINT inbox_items_kind_check CHECK (kind IN (
        'task_assigned','blocker','run_completed','run_failed','approval_request',
        'question_request','runtime_offline','invitation_renewal',
        'calendar_reminder','calendar_update','discussion_mention',
        'task_run_requested','task_handoff','analytics_budget','huddle_missed')),
    DROP CONSTRAINT inbox_items_entity_type_check,
    ADD CONSTRAINT inbox_items_entity_type_check CHECK (entity_type IN (
        'company','user','agent','goal','task','run','conversation','message',
        'inbox','resource','runtime','approval','question','deliverable','calendar'));

DROP VIEW project_access_paths;

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

CREATE OR REPLACE VIEW project_access AS
SELECT project.company_id, project.id AS project_id, member.user_id,
       CASE WHEN read_row.user_id IS NOT NULL THEN 'grant' ELSE 'company' END::text AS via
FROM projects AS project
JOIN companies AS company
  ON company.id = project.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = project.company_id AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
LEFT JOIN project_grants AS read_row
  ON read_row.company_id = project.company_id
 AND read_row.project_id = project.id
 AND read_row.user_id = member.user_id
 AND read_row.permission = read_row.read_permission
 AND read_row.updated_at >= COALESCE(member.reactivated_at, member.created_at)
WHERE read_row.user_id IS NOT NULL OR project.visibility = 'company';

CREATE OR REPLACE FUNCTION project_permission_held(
    company_id uuid,
    project_id uuid,
    user_id uuid,
    permission text,
    company_default_permissions text[],
    management_permissions text[],
    management_roles text[]
) RETURNS boolean
LANGUAGE sql STABLE
AS $$
SELECT EXISTS (
    SELECT 1
    FROM company_members AS member
    JOIN users AS account
      ON account.id = member.user_id
     AND account.disabled_at IS NULL
     AND account.deleted_at IS NULL
    JOIN companies AS company
      ON company.id = member.company_id
     AND company.deleting_at IS NULL
    JOIN projects AS project
      ON project.company_id = member.company_id
     AND project.id = $2
    WHERE member.company_id = $1
      AND member.user_id = $3
      AND member.status = 'active'
      AND (
          EXISTS (
              SELECT 1
              FROM project_grants AS grant_row
              WHERE grant_row.company_id = $1
                AND grant_row.project_id = $2
                AND grant_row.user_id = $3
                AND grant_row.permission = $4
                AND grant_row.updated_at >= COALESCE(member.reactivated_at, member.created_at)
          )
          OR (project.visibility = 'company' AND $4 = ANY($5))
          OR ($4 = ANY($6) AND member.role = ANY($7))
      )
)
$$;

DROP TABLE project_group_grants;
DROP TABLE company_group_members;
DROP TABLE company_groups;
