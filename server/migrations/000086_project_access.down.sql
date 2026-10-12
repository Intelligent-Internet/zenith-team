DROP FUNCTION conversation_audience_reads(uuid, uuid, uuid);

-- Restore the 000036 output_access body first: it reads the columns dropped below.
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
WHERE output.owner_user_id = member.user_id OR grant_row.permission IS NOT NULL
   OR output.visibility = 'company'
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

DROP VIEW attachment_access;
DROP VIEW conversation_readers;
DROP VIEW task_access;
DROP VIEW project_access;

DROP FUNCTION project_permission_held(uuid, uuid, uuid, text, text[], text[], text[]);

ALTER TABLE conversations DROP CONSTRAINT conversations_task_project_check;
DROP INDEX activity_events_project_created_idx;
DROP INDEX inbox_items_recipient_project_idx;
DROP INDEX conversations_project_idx;
ALTER TABLE task_report_events DROP COLUMN project_id;
ALTER TABLE activity_events DROP COLUMN project_id;
ALTER TABLE inbox_items DROP COLUMN project_id;
ALTER TABLE conversations DROP COLUMN project_id;

DROP TABLE project_grants;

ALTER TABLE projects
    DROP COLUMN access_version,
    DROP COLUMN visibility;
