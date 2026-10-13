-- A Whiteboard lives in the chat it was made in, so a Board has no chat of
-- its own any more. 000108's Board channels go, and the discussion shape,
-- identity guard and conversation_readers return to their 000107 form.
-- Board channels have no meaning without their Board column; drop them so
-- they do not linger as stray private channels.
DELETE FROM conversations WHERE board_id IS NOT NULL;
-- The deferred discussion checks of those rows run now, so the tables they
-- touched can be altered in this transaction.
SET CONSTRAINTS ALL IMMEDIATE;

CREATE OR REPLACE VIEW conversation_readers AS
-- Task chats follow their Project.
SELECT conversation.company_id, conversation.id AS conversation_id, access.user_id
FROM conversations AS conversation
JOIN project_access AS access
  ON access.company_id = conversation.company_id AND access.project_id = conversation.project_id
WHERE conversation.kind = 'task'
UNION ALL
-- Task chats whose Task was deleted before this migration recorded no
-- Project; they stay readable by every active member (a known pocket, ADR
-- 0017). conversations_task_project_check admits a NULL Project only on such a
-- chat, and task_id IS NULL repeats that here so a live Task's chat never
-- falls into this arm.
SELECT conversation.company_id, conversation.id, member.user_id
FROM conversations AS conversation
JOIN companies AS company
  ON company.id = conversation.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = conversation.company_id AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE conversation.kind = 'task' AND conversation.project_id IS NULL AND conversation.task_id IS NULL
UNION ALL
-- Channels, human direct messages and Huddle chats follow their active
-- participants, as ListDiscussionAccess does.
SELECT conversation.company_id, conversation.id, participant.user_id
FROM conversations AS conversation
JOIN companies AS company
  ON company.id = conversation.company_id AND company.deleting_at IS NULL
JOIN conversation_members AS participant
  ON participant.company_id = conversation.company_id
 AND participant.conversation_id = conversation.id
 AND participant.status = 'active'
JOIN company_members AS member
  ON member.company_id = conversation.company_id
 AND member.user_id = participant.user_id
 AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE conversation.kind IN ('channel', 'human_direct', 'huddle')
UNION ALL
-- A channel Agent's execution chat is read by the callers of its live
-- invocations: the live-caller arm of LockHumanInputOriginConversation. The
-- member who created the channel Agent chat is deliberately not a reader
-- through that role alone (ADR 0017).
SELECT conversation.company_id, conversation.id, member.user_id
FROM conversations AS conversation
JOIN companies AS company
  ON company.id = conversation.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = conversation.company_id AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE conversation.kind = 'channel_agent'
  AND EXISTS (
      SELECT 1
      FROM channel_invocations AS invocation
      WHERE invocation.company_id = conversation.company_id
        AND invocation.conversation_id = conversation.id
        AND invocation.caller_user_id = member.user_id
        AND invocation.status IN ('active', 'waiting_input')
  )
UNION ALL
-- Direct and Schedule chats are their creator's alone.
SELECT conversation.company_id, conversation.id, member.user_id
FROM conversations AS conversation
JOIN companies AS company
  ON company.id = conversation.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = conversation.company_id
 AND member.user_id = conversation.created_by_user_id
 AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE conversation.kind IN ('direct', 'schedule')
 AND (conversation.document_thread_id IS NULL OR EXISTS (SELECT 1 FROM project_access access WHERE access.company_id=conversation.company_id AND access.project_id=conversation.project_id AND access.user_id=member.user_id));

CREATE OR REPLACE FUNCTION check_discussion_shape() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE cid uuid; c conversations; pair conversation_human_directs; humans bigint;
BEGIN
    IF TG_TABLE_NAME='conversations' THEN cid:=COALESCE(NEW.id,OLD.id);
    ELSE cid:=COALESCE(NEW.conversation_id,OLD.conversation_id); END IF;
    SELECT * INTO c FROM conversations WHERE id=cid;
    IF NOT FOUND THEN RETURN NULL; END IF;
    IF c.kind='human_direct' THEN
        SELECT * INTO pair FROM conversation_human_directs WHERE conversation_id=cid;
        IF NOT FOUND OR EXISTS(SELECT 1 FROM conversation_channels WHERE conversation_id=cid)
            OR (pair.participant_ids IS NULL AND (pair.user_low_id IS NULL OR pair.user_high_id IS NULL OR cardinality(pair.agent_ids)<>0
                OR (SELECT count(*) FROM conversation_members WHERE conversation_id=cid)<>2))
            OR (pair.participant_ids IS NOT NULL AND (pair.user_low_id IS NOT NULL OR pair.user_high_id IS NOT NULL
                OR cardinality(pair.participant_ids) NOT BETWEEN 1 AND 50
                OR cardinality(pair.participant_ids)+cardinality(pair.agent_ids)<2
                OR (cardinality(pair.agent_ids)=0 AND cardinality(pair.participant_ids)<3)
                OR pair.participant_ids IS DISTINCT FROM (SELECT array_agg(user_id ORDER BY user_id)
                    FROM conversation_members WHERE conversation_id=cid)))
            OR cardinality(pair.agent_ids)>20
            OR pair.agent_ids IS DISTINCT FROM COALESCE((SELECT array_agg(agent_id ORDER BY agent_id)
                FROM conversation_agents WHERE conversation_id=cid),'{}'::uuid[])
            OR EXISTS(SELECT 1 FROM conversation_members WHERE conversation_id=cid
                AND (source_type<>'human_direct' OR (pair.participant_ids IS NULL AND user_id NOT IN (pair.user_low_id,pair.user_high_id)))) THEN
            RAISE EXCEPTION 'invalid human direct shape' USING ERRCODE='23514';
        END IF;
    ELSIF c.kind='channel' THEN
        SELECT count(*) INTO humans FROM conversation_members WHERE conversation_id=cid AND status='active';
        IF NOT EXISTS(SELECT 1 FROM conversation_channels WHERE conversation_id=cid)
            OR EXISTS(SELECT 1 FROM conversation_human_directs WHERE conversation_id=cid)
            OR EXISTS(SELECT 1 FROM conversation_members WHERE conversation_id=cid AND source_type<>'channel')
            OR (NOT c.is_general AND ((c.archived_at IS NULL AND humans=0) OR humans>50))
            OR (SELECT count(*) FROM conversation_agents WHERE conversation_id=cid AND status='active')>20 THEN
            RAISE EXCEPTION 'invalid private channel shape' USING ERRCODE='23514';
        END IF;
    ELSIF c.kind='huddle' THEN
        IF NOT EXISTS(SELECT 1 FROM huddles WHERE company_id=c.company_id AND chat_conversation_id=cid)
            OR EXISTS(SELECT 1 FROM conversation_human_directs WHERE conversation_id=cid)
            OR EXISTS(SELECT 1 FROM conversation_channels WHERE conversation_id=cid)
            OR EXISTS(SELECT 1 FROM conversation_agents WHERE conversation_id=cid)
            OR EXISTS(SELECT 1 FROM conversation_members WHERE conversation_id=cid AND source_type<>'huddle') THEN
            RAISE EXCEPTION 'invalid huddle chat shape' USING ERRCODE='23514';
        END IF;
    ELSIF EXISTS(SELECT 1 FROM conversation_human_directs WHERE conversation_id=cid)
        OR EXISTS(SELECT 1 FROM conversation_channels WHERE conversation_id=cid)
        OR EXISTS(SELECT 1 FROM conversation_members WHERE conversation_id=cid)
        OR EXISTS(SELECT 1 FROM conversation_agents WHERE conversation_id=cid) THEN
        RAISE EXCEPTION 'legacy conversation has discussion grants' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION preserve_discussion_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME='conversations' THEN
        IF (OLD.kind IN ('human_direct','channel','huddle') OR NEW.kind IN ('human_direct','channel','huddle')) AND
            (NEW.is_general IS DISTINCT FROM OLD.is_general OR NEW.kind IS DISTINCT FROM OLD.kind OR NEW.company_id IS DISTINCT FROM OLD.company_id OR NEW.id IS DISTINCT FROM OLD.id
            OR NEW.created_by_user_id IS DISTINCT FROM OLD.created_by_user_id OR NEW.request_key IS DISTINCT FROM OLD.request_key) THEN
            RAISE EXCEPTION 'immutable conversation identity' USING ERRCODE='23514';
        END IF;
    ELSIF TG_TABLE_NAME='conversation_human_directs' THEN
        IF NEW IS DISTINCT FROM OLD AND (
            OLD.participant_ids IS NULL OR NEW.participant_ids IS NULL
            OR NEW.company_id IS DISTINCT FROM OLD.company_id
            OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id
            OR NEW.created_at IS DISTINCT FROM OLD.created_at
            OR NEW.user_low_id IS DISTINCT FROM OLD.user_low_id
            OR NEW.user_high_id IS DISTINCT FROM OLD.user_high_id
            OR NOT NEW.membership_expanded
            OR NOT NEW.participant_ids @> OLD.participant_ids
            OR NOT NEW.agent_ids @> OLD.agent_ids) THEN
            RAISE EXCEPTION 'immutable human direct identity or removed participant' USING ERRCODE='23514';
        END IF;
    ELSE
        IF NEW.conversation_id IS DISTINCT FROM OLD.conversation_id OR NEW.company_id IS DISTINCT FROM OLD.company_id
            OR to_jsonb(NEW)->'created_at' IS DISTINCT FROM to_jsonb(OLD)->'created_at'
            OR to_jsonb(NEW)->'user_id' IS DISTINCT FROM to_jsonb(OLD)->'user_id'
            OR to_jsonb(NEW)->'agent_id' IS DISTINCT FROM to_jsonb(OLD)->'agent_id'
            OR to_jsonb(NEW)->'source_type' IS DISTINCT FROM to_jsonb(OLD)->'source_type'
            OR (TG_TABLE_NAME='conversation_channels' AND to_jsonb(NEW)->'create_fingerprint' IS DISTINCT FROM to_jsonb(OLD)->'create_fingerprint') THEN
            RAISE EXCEPTION 'immutable discussion scope or receipt' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END $$;

DROP INDEX IF EXISTS conversations_board_channel_unique;
ALTER TABLE conversations DROP COLUMN board_id;

-- A Whiteboard is an Artifact: its Output owns the title, the owner, every
-- grant, the chat it is shared into and the public link, and output_access is
-- the one access rule. The Board keeps only the live scene, under the
-- Output's ID. Boards made before had no Output to belong to, so they go.
DELETE FROM boards;
SET CONSTRAINTS ALL IMMEDIATE;

-- Who reads a Board's files follows the Whiteboard Artifact.
CREATE OR REPLACE VIEW attachment_access AS
SELECT arm.company_id, arm.attachment_id, arm.user_id
FROM (
    -- A Board's file and its preview: each person is probed against the
    -- Whiteboard Artifact, as a message file is against its chat.
    SELECT file.company_id, file.attachment_id, candidate.user_id
    FROM board_files AS file
    JOIN company_members AS candidate
      ON candidate.company_id = file.company_id
    CROSS JOIN LATERAL (
        SELECT 1
        FROM output_access AS access
        WHERE access.company_id = file.company_id
          AND access.output_id = file.board_id
          AND access.user_id = candidate.user_id
        LIMIT 1
    ) AS reads_board
    UNION ALL
    SELECT file.company_id, file.preview_attachment_id, candidate.user_id
    FROM board_files AS file
    JOIN company_members AS candidate
      ON candidate.company_id = file.company_id
    CROSS JOIN LATERAL (
        SELECT 1
        FROM output_access AS access
        WHERE access.company_id = file.company_id
          AND access.output_id = file.board_id
          AND access.user_id = candidate.user_id
        LIMIT 1
    ) AS reads_board
    WHERE file.preview_attachment_id IS NOT NULL
    UNION ALL
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
DROP TABLE board_favorites, board_create_receipts, board_member_access, board_agent_access;
-- The library searched note words; nothing does now.
DROP INDEX boards_library_idx, board_notes_search_idx;
ALTER TABLE boards
    DROP CONSTRAINT boards_visibility_token_check,
    DROP COLUMN title,
    DROP COLUMN purpose,
    DROP COLUMN visibility,
    DROP COLUMN company_access,
    DROP COLUMN public_token,
    DROP COLUMN revision,
    DROP COLUMN created_by,
    DROP COLUMN archived_at,
    ALTER COLUMN id DROP DEFAULT,
    ADD CONSTRAINT boards_output_fkey FOREIGN KEY (company_id, id) REFERENCES outputs(company_id, id) ON DELETE CASCADE;

ALTER TABLE outputs
    DROP CONSTRAINT outputs_type_check,
    ADD CONSTRAINT outputs_type_check CHECK (type IN ('app', 'media', 'text', 'file', 'whiteboard'));
