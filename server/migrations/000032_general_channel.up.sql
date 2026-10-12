-- General is a system-owned Company channel, never an existing private group.
ALTER TABLE conversations ADD COLUMN is_general boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT conversations_general_shape_check CHECK (
        NOT is_general OR (kind='channel' AND title='general' AND archived_at IS NULL));
CREATE UNIQUE INDEX conversations_general_company_idx ON conversations(company_id) WHERE is_general;
ALTER TABLE conversations DROP CONSTRAINT conversations_shape_check,
    ADD CONSTRAINT conversations_shape_check CHECK (
        (kind='task' AND schedule_id IS NULL AND agent_id IS NULL AND created_by_user_id IS NULL)
        OR (kind='direct' AND task_id IS NULL AND schedule_id IS NULL AND created_by_user_id IS NOT NULL)
        OR (kind='schedule' AND task_id IS NULL AND schedule_id IS NOT NULL AND agent_id IS NOT NULL AND created_by_user_id IS NOT NULL)
        OR (kind IN ('human_direct','channel') AND task_id IS NULL AND schedule_id IS NULL AND agent_id IS NULL
            AND (created_by_user_id IS NOT NULL OR is_general) AND pinned_at IS NULL
            AND (kind <> 'human_direct' OR (archived_at IS NULL AND title IS NULL))
            AND (kind <> 'channel' OR (request_key IS NOT NULL AND title IS NOT NULL
                AND title=btrim(title) AND char_length(title) BETWEEN 1 AND 100 AND octet_length(title)<=400)))
    );

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
            OR EXISTS(SELECT 1 FROM conversation_agents WHERE conversation_id=cid)
            OR (SELECT count(*) FROM conversation_members WHERE conversation_id=cid)<>2
            OR EXISTS(SELECT 1 FROM conversation_members WHERE conversation_id=cid
                AND (source_type<>'human_direct' OR user_id NOT IN (pair.user_low_id,pair.user_high_id))) THEN
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
        IF (OLD.kind IN ('human_direct','channel') OR NEW.kind IN ('human_direct','channel')) AND
            (NEW.is_general IS DISTINCT FROM OLD.is_general OR NEW.kind IS DISTINCT FROM OLD.kind OR NEW.company_id IS DISTINCT FROM OLD.company_id OR NEW.id IS DISTINCT FROM OLD.id
            OR NEW.created_by_user_id IS DISTINCT FROM OLD.created_by_user_id OR NEW.request_key IS DISTINCT FROM OLD.request_key) THEN
            RAISE EXCEPTION 'immutable conversation identity' USING ERRCODE='23514';
        END IF;
    ELSIF TG_TABLE_NAME='conversation_human_directs' THEN
        IF NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'immutable human direct pair' USING ERRCODE='23514'; END IF;
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

-- Create a separate channel even if a private channel is already named general.
-- System ownership also permits Companies without an active human during cleanup.
INSERT INTO conversations(company_id,kind,title,request_key,is_general)
SELECT id,'channel','general',gen_random_uuid(),true FROM companies WHERE deleting_at IS NULL;
INSERT INTO conversation_channels(company_id,conversation_id,create_fingerprint)
SELECT company_id,id,decode(repeat('00',32),'hex') FROM conversations WHERE is_general;
INSERT INTO conversation_members(company_id,conversation_id,user_id,source_type)
SELECT c.company_id,c.id,m.user_id,'channel'
FROM conversations c JOIN company_members m ON m.company_id=c.company_id AND m.status='active'
JOIN users u ON u.id=m.user_id AND u.disabled_at IS NULL AND u.deleted_at IS NULL
WHERE c.is_general;
INSERT INTO conversation_user_states(company_id,conversation_id,user_id,grant_generation)
SELECT m.company_id,m.conversation_id,m.user_id,m.generation
FROM conversation_members m JOIN conversations c ON c.id=m.conversation_id WHERE c.is_general;
