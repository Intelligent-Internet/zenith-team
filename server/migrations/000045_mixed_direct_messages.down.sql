DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM conversation_human_directs WHERE cardinality(agent_ids)>0) THEN
        RAISE EXCEPTION 'cannot downgrade while mixed direct messages exist';
    END IF;
END $$;
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
            OR (pair.participant_ids IS NULL AND (pair.user_low_id IS NULL OR pair.user_high_id IS NULL
                OR (SELECT count(*) FROM conversation_members WHERE conversation_id=cid)<>2))
            OR (pair.participant_ids IS NOT NULL AND (pair.user_low_id IS NOT NULL OR pair.user_high_id IS NOT NULL
                OR cardinality(pair.participant_ids) NOT BETWEEN 3 AND 50
                OR pair.participant_ids IS DISTINCT FROM (SELECT array_agg(user_id ORDER BY user_id)
                    FROM conversation_members WHERE conversation_id=cid)))
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
    ELSIF EXISTS(SELECT 1 FROM conversation_human_directs WHERE conversation_id=cid)
        OR EXISTS(SELECT 1 FROM conversation_channels WHERE conversation_id=cid)
        OR EXISTS(SELECT 1 FROM conversation_members WHERE conversation_id=cid)
        OR EXISTS(SELECT 1 FROM conversation_agents WHERE conversation_id=cid) THEN
        RAISE EXCEPTION 'legacy conversation has discussion grants' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END $$;

DROP INDEX conversation_human_directs_participants_idx;
ALTER TABLE conversation_human_directs DROP COLUMN agent_ids;
CREATE UNIQUE INDEX conversation_human_directs_participants_idx
    ON conversation_human_directs(company_id,participant_ids) WHERE participant_ids IS NOT NULL;
