-- Whiteboard Artifacts and their scenes go; 000107's Board columns, access
-- tables and views return empty.
DELETE FROM outputs WHERE type = 'whiteboard';
-- The deferred checks of the deleted scenes run now, so their tables can be
-- altered in this transaction.
SET CONSTRAINTS ALL IMMEDIATE;
ALTER TABLE outputs
    DROP CONSTRAINT outputs_type_check,
    ADD CONSTRAINT outputs_type_check CHECK (type IN ('app', 'media', 'text', 'file'));

ALTER TABLE boards
    DROP CONSTRAINT boards_output_fkey,
    ALTER COLUMN id SET DEFAULT gen_random_uuid(),
    ADD COLUMN title text NOT NULL,
    ADD COLUMN purpose text NOT NULL DEFAULT '',
    ADD COLUMN visibility text NOT NULL DEFAULT 'private' CHECK (visibility IN ('private','company','public')),
    ADD COLUMN company_access text NOT NULL DEFAULT 'read' CHECK (company_access IN ('read','edit')),
    ADD COLUMN public_token uuid UNIQUE,
    ADD COLUMN revision bigint NOT NULL DEFAULT 1,
    ADD COLUMN created_by uuid NOT NULL,
    ADD COLUMN archived_at timestamptz,
    ADD CONSTRAINT boards_visibility_token_check CHECK ((visibility='public') = (public_token IS NOT NULL));
CREATE INDEX boards_library_idx ON boards(company_id, updated_at DESC, id);
CREATE INDEX board_notes_search_idx ON board_notes USING gin(to_tsvector('simple', plain_text));

CREATE TABLE board_member_access (
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    user_id uuid NOT NULL,
    access text NOT NULL CHECK (access IN ('read','edit','manage')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (board_id,user_id),
    FOREIGN KEY (company_id,board_id) REFERENCES boards(company_id,id) ON DELETE CASCADE,
    FOREIGN KEY (company_id,user_id) REFERENCES company_members(company_id,user_id) ON DELETE CASCADE
);

-- Agent access intersects the live Run author's own Board access.
CREATE TABLE board_agent_access (
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    access text NOT NULL CHECK (access IN ('read','edit')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (board_id,agent_id),
    FOREIGN KEY (company_id,board_id) REFERENCES boards(company_id,id) ON DELETE CASCADE,
    FOREIGN KEY (company_id,agent_id) REFERENCES agents(company_id,id) ON DELETE CASCADE
);

-- One resource access projection serves HTTP, libraries, media and audiences.
-- Company permissions remain in Go; no role grants are inferred here.
CREATE VIEW board_access AS
SELECT b.company_id,b.id AS board_id,m.user_id,
 CASE WHEN b.created_by=m.user_id OR g.access='manage' THEN 'manage'
      WHEN g.access='edit' OR (b.visibility='company' AND b.company_access='edit') THEN 'edit'
      ELSE 'read' END::text AS access
FROM boards b
JOIN companies c ON c.id=b.company_id AND c.deleting_at IS NULL
JOIN company_members m ON m.company_id=b.company_id AND m.status='active'
JOIN users u ON u.id=m.user_id AND u.disabled_at IS NULL AND u.deleted_at IS NULL
LEFT JOIN board_member_access g ON g.company_id=b.company_id AND g.board_id=b.id AND g.user_id=m.user_id
WHERE b.created_by=m.user_id OR g.user_id IS NOT NULL OR b.visibility IN ('company','public');

CREATE TABLE board_favorites (
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (board_id, user_id),
    FOREIGN KEY (company_id, board_id) REFERENCES boards(company_id, id) ON DELETE CASCADE
);

CREATE TABLE board_create_receipts (
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    actor_user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    board_id uuid NOT NULL,
    request_hash bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (company_id, actor_user_id, request_key),
    FOREIGN KEY (company_id, board_id) REFERENCES boards(company_id, id) ON DELETE CASCADE
);

CREATE INDEX board_create_receipts_created_idx ON board_create_receipts(created_at);

CREATE OR REPLACE VIEW attachment_access AS
SELECT arm.company_id, arm.attachment_id, arm.user_id
FROM (
    SELECT file.company_id, file.attachment_id, access.user_id
    FROM board_files AS file
    JOIN board_access AS access ON access.company_id = file.company_id AND access.board_id = file.board_id
    UNION ALL
    SELECT file.company_id, file.preview_attachment_id, access.user_id
    FROM board_files AS file
    JOIN board_access AS access ON access.company_id = file.company_id AND access.board_id = file.board_id
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

-- Restores 000108's Board channel column, shape rules and reader arm.
ALTER TABLE conversations ADD COLUMN board_id uuid,
 ADD CONSTRAINT conversations_board_fkey FOREIGN KEY (company_id, board_id) REFERENCES boards(company_id, id) ON DELETE CASCADE,
 ADD CONSTRAINT conversations_board_check
  CHECK (board_id IS NULL OR (kind = 'channel' AND NOT is_general AND archived_at IS NULL));
CREATE UNIQUE INDEX conversations_board_channel_unique ON conversations(board_id) WHERE board_id IS NOT NULL;

-- 000070's shape check, except that a Board's channel may have no one or
-- more than 50 people; it keeps the 20-Agent limit.
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
            OR (NOT c.is_general AND c.board_id IS NULL AND ((c.archived_at IS NULL AND humans=0) OR humans>50))
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
-- 000070's identity guard, plus a channel's Board.
CREATE OR REPLACE FUNCTION preserve_discussion_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME='conversations' THEN
        IF (OLD.kind IN ('human_direct','channel','huddle') OR NEW.kind IN ('human_direct','channel','huddle')) AND
            (NEW.is_general IS DISTINCT FROM OLD.is_general OR NEW.kind IS DISTINCT FROM OLD.kind OR NEW.company_id IS DISTINCT FROM OLD.company_id OR NEW.id IS DISTINCT FROM OLD.id
            OR NEW.created_by_user_id IS DISTINCT FROM OLD.created_by_user_id OR NEW.request_key IS DISTINCT FROM OLD.request_key
            OR NEW.board_id IS DISTINCT FROM OLD.board_id) THEN
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
-- participants, as ListDiscussionAccess does; a Board's channel has its own arm.
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
WHERE conversation.kind IN ('channel', 'human_direct', 'huddle') AND conversation.board_id IS NULL
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

 AND (conversation.document_thread_id IS NULL OR EXISTS (SELECT 1 FROM project_access access WHERE access.company_id=conversation.company_id AND access.project_id=conversation.project_id AND access.user_id=member.user_id))
UNION ALL
-- A Board's channel is read by its joined people while they can still read
-- the Board: membership follows Board access after commit, so the live join
-- keeps someone who just lost the Board out in between. board_access already
-- requires an active member with an enabled account in a live Company.
SELECT conversation.company_id, conversation.id, participant.user_id
FROM conversations AS conversation
JOIN conversation_members AS participant
  ON participant.company_id = conversation.company_id
 AND participant.conversation_id = conversation.id
 AND participant.status = 'active'
JOIN board_access AS access
  ON access.company_id = conversation.company_id
 AND access.board_id = conversation.board_id
 AND access.user_id = participant.user_id
WHERE conversation.kind = 'channel' AND conversation.board_id IS NOT NULL;
