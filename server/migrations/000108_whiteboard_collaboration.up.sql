-- A Board's chat: one channel per Board that the people who can read the
-- Board share, where they @mention each other and the Agents invited to the
-- Board. Membership follows Board access (joined on open or on a mention,
-- removed after an access change), so the channel never lists in Chat, never
-- archives, and holds as many people as the Board has readers.
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

-- Connector notation for software diagrams: a dashed line, a marker at each
-- end (arrows, UML triangles and diamonds, crow's feet), a route, and labels
-- at each end. The defaults draw every existing connector exactly as before:
-- a solid line from the source's right to the target's left with an arrow.
-- A message runs straight across at anchor_y below its source's top, and is
-- the one route that may leave and return to the same item (a self message).
ALTER TABLE board_edges
    ADD COLUMN line text NOT NULL DEFAULT 'solid',
    ADD COLUMN start_marker text NOT NULL DEFAULT 'none',
    ADD COLUMN end_marker text NOT NULL DEFAULT 'arrow',
    ADD COLUMN route text NOT NULL DEFAULT 'flow',
    ADD COLUMN anchor_y double precision,
    ADD COLUMN source_label text NOT NULL DEFAULT '',
    ADD COLUMN target_label text NOT NULL DEFAULT '',
    ADD CONSTRAINT board_edges_line_check CHECK (line IN ('solid', 'dashed')),
    ADD CONSTRAINT board_edges_start_marker_check CHECK (start_marker IN
        ('none', 'arrow', 'open', 'triangle', 'diamond', 'diamond_filled', 'one', 'zero_one', 'many', 'zero_many')),
    ADD CONSTRAINT board_edges_end_marker_check CHECK (end_marker IN
        ('none', 'arrow', 'open', 'triangle', 'diamond', 'diamond_filled', 'one', 'zero_one', 'many', 'zero_many')),
    ADD CONSTRAINT board_edges_route_check CHECK (route IN ('flow', 'straight', 'elbow', 'message')),
    DROP CONSTRAINT board_edges_check,
    ADD CONSTRAINT board_edges_check CHECK (source_id <> target_id OR route = 'message');
