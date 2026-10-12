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
DROP VIEW board_access;
DROP TABLE board_changes;
DROP TABLE board_operations;
DROP TABLE board_edges;
DROP TABLE board_strokes;
DROP TABLE board_notes;
DROP TABLE board_items;
DROP TABLE board_files;
DROP TABLE board_create_receipts;
DROP TABLE board_favorites;
DROP TABLE board_agent_access;
DROP TABLE board_member_access;
DROP TABLE boards;
