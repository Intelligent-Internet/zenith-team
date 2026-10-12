CREATE OR REPLACE FUNCTION check_discussion_collaboration_shape() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE item_id uuid; item conversation_items;
BEGIN
    IF TG_TABLE_NAME='conversation_thread_states' THEN item_id:=NEW.root_id;
    ELSE item_id:=NEW.message_id;
    END IF;
    SELECT * INTO item FROM conversation_items
    WHERE company_id=NEW.company_id AND conversation_id=NEW.conversation_id AND id=item_id FOR SHARE;
    IF NOT FOUND OR item.run_id IS NOT NULL OR item.type NOT IN ('user_message','agent_text') THEN
        RAISE EXCEPTION 'collaboration requires a discussion message' USING ERRCODE='23514';
    END IF;
    IF TG_TABLE_NAME='conversation_thread_states' THEN
        IF item.thread_root_id IS NOT NULL OR NOT EXISTS(
            SELECT 1 FROM conversation_members WHERE company_id=NEW.company_id AND conversation_id=NEW.conversation_id
                AND user_id=NEW.user_id AND generation=NEW.grant_generation AND status='active') THEN
            RAISE EXCEPTION 'thread state requires a root and current grant' USING ERRCODE='23514';
        END IF;
    ELSIF item.deleted_at IS NOT NULL THEN
        RAISE EXCEPTION 'deleted messages cannot be pinned or reacted to' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
