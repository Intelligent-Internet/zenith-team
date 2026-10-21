-- Collaboration extends the zero-Run discussion lane only.
ALTER TABLE conversation_items
    ADD COLUMN thread_root_id uuid,
    ADD COLUMN edited_at timestamptz,
    ADD COLUMN deleted_at timestamptz,
    ADD CONSTRAINT conversation_items_scope_id_key UNIQUE(company_id,conversation_id,id),
    ADD CONSTRAINT conversation_items_thread_root_fkey FOREIGN KEY(company_id,conversation_id,thread_root_id)
        REFERENCES conversation_items(company_id,conversation_id,id) ON DELETE CASCADE;
DROP INDEX conversation_items_discussion_roots_idx;
CREATE INDEX conversation_items_discussion_roots_idx ON conversation_items(conversation_id,seq DESC)
    WHERE run_id IS NULL AND thread_root_id IS NULL;
CREATE INDEX conversation_items_thread_replies_idx ON conversation_items(conversation_id,thread_root_id,seq DESC)
    WHERE thread_root_id IS NOT NULL;

CREATE TABLE conversation_message_pins (
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    message_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    pinned_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    pinned_by_user_id uuid NOT NULL,
    PRIMARY KEY(conversation_id,message_id),
    FOREIGN KEY(company_id,conversation_id,message_id) REFERENCES conversation_items(company_id,conversation_id,id) ON DELETE CASCADE
);
CREATE INDEX conversation_message_pins_page_idx ON conversation_message_pins(conversation_id,pinned_at DESC,message_id DESC);

CREATE TABLE conversation_message_reactions (
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    message_id uuid NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    emoji text NOT NULL CHECK(octet_length(emoji) BETWEEN 1 AND 64),
    PRIMARY KEY(message_id,user_id,emoji),
    FOREIGN KEY(company_id,conversation_id,message_id) REFERENCES conversation_items(company_id,conversation_id,id) ON DELETE CASCADE,
    FOREIGN KEY(company_id,user_id) REFERENCES company_members(company_id,user_id) ON DELETE CASCADE
);
CREATE INDEX conversation_message_reactions_people_idx ON conversation_message_reactions(message_id,emoji,user_id);

CREATE TABLE conversation_thread_states (
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    root_id uuid NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    grant_generation bigint NOT NULL CHECK(grant_generation BETWEEN 1 AND 9007199254740991),
    follow text NOT NULL CHECK(follow IN ('auto','following','unfollowed')),
    read_seq bigint NOT NULL DEFAULT 0 CHECK(read_seq BETWEEN 0 AND 9007199254740991),
    version bigint NOT NULL DEFAULT 1 CHECK(version BETWEEN 1 AND 9007199254740991),
    PRIMARY KEY(root_id,user_id),
    FOREIGN KEY(company_id,conversation_id,root_id) REFERENCES conversation_items(company_id,conversation_id,id) ON DELETE CASCADE,
    FOREIGN KEY(conversation_id,user_id) REFERENCES conversation_members(conversation_id,user_id) ON DELETE CASCADE
);
CREATE INDEX conversation_thread_states_user_idx ON conversation_thread_states(conversation_id,user_id);

CREATE OR REPLACE FUNCTION check_discussion_item_shape() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE conversation_type text; action text; parent conversation_items;
BEGIN
    IF TG_OP='UPDATE' AND (OLD.run_id IS NULL OR NEW.run_id IS NULL) AND
        (NEW.id IS DISTINCT FROM OLD.id OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id
        OR NEW.company_id IS DISTINCT FROM OLD.company_id OR NEW.seq IS DISTINCT FROM OLD.seq) THEN
        RAISE EXCEPTION 'immutable item identity' USING ERRCODE='23514';
    END IF;
    IF TG_OP='UPDATE' AND OLD.run_id IS NULL AND
        (NEW.author_id IS DISTINCT FROM OLD.author_id OR NEW.author_kind IS DISTINCT FROM OLD.author_kind
        OR NEW.request_key IS DISTINCT FROM OLD.request_key OR NEW.send_fingerprint IS DISTINCT FROM OLD.send_fingerprint
        OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.type IS DISTINCT FROM OLD.type
        OR NEW.thread_root_id IS DISTINCT FROM OLD.thread_root_id
        OR (OLD.deleted_at IS NOT NULL AND NEW IS DISTINCT FROM OLD)) THEN
        RAISE EXCEPTION 'immutable discussion receipt' USING ERRCODE='23514';
    END IF;
    SELECT kind INTO conversation_type FROM conversations WHERE id=NEW.conversation_id AND company_id=NEW.company_id;
    IF conversation_type IN ('task','direct','schedule') THEN
        IF NEW.run_id IS NULL OR NEW.author_id IS NULL OR NEW.author_kind='system' OR NEW.type='discussion_system'
            OR NEW.body IS NOT NULL OR NEW.send_fingerprint IS NOT NULL OR NEW.revision<>1
            OR NEW.thread_root_id IS NOT NULL OR NEW.edited_at IS NOT NULL OR NEW.deleted_at IS NOT NULL THEN
            RAISE EXCEPTION 'invalid legacy item shape' USING ERRCODE='23514';
        END IF;
    ELSIF conversation_type IN ('human_direct','channel') THEN
        IF NEW.seq NOT BETWEEN 1 AND 9007199254740991 OR NEW.run_id IS NOT NULL OR NEW.parts IS NOT NULL
            OR NEW.item_key IS NOT NULL OR NEW.content_sha256 IS NOT NULL THEN
            RAISE EXCEPTION 'invalid discussion item shape' USING ERRCODE='23514';
        END IF;
        IF NEW.type='user_message' THEN
            IF NEW.author_kind<>'user' OR NEW.author_id IS NULL OR NEW.request_key IS NULL
                OR NEW.send_fingerprint IS NULL OR octet_length(NEW.send_fingerprint)<>32
                OR (NEW.deleted_at IS NULL AND (NEW.body IS NULL OR jsonb_typeof(NEW.body)<>'object'))
                OR (NEW.deleted_at IS NOT NULL AND (NEW.body IS NOT NULL OR NEW.content<>''))
                OR NEW.metadata<>'{}'::jsonb THEN
                RAISE EXCEPTION 'invalid discussion message receipt' USING ERRCODE='23514';
            END IF;
            IF NEW.deleted_at IS NOT NULL AND (
                EXISTS(SELECT 1 FROM conversation_message_pins WHERE message_id=NEW.id) OR
                EXISTS(SELECT 1 FROM conversation_message_reactions WHERE message_id=NEW.id)) THEN
                RAISE EXCEPTION 'deleted messages cannot retain pins or reactions' USING ERRCODE='23514';
            END IF;
            IF NEW.thread_root_id IS NOT NULL THEN
                SELECT * INTO parent FROM conversation_items WHERE company_id=NEW.company_id
                    AND conversation_id=NEW.conversation_id AND id=NEW.thread_root_id;
                IF NOT FOUND OR parent.run_id IS NOT NULL OR parent.type<>'user_message'
                    OR parent.thread_root_id IS NOT NULL OR parent.seq>=NEW.seq THEN
                    RAISE EXCEPTION 'invalid discussion thread root' USING ERRCODE='23514';
                END IF;
            END IF;
        ELSIF NEW.type='discussion_system' THEN
            action:=NEW.metadata->>'action';
            IF NEW.thread_root_id IS NOT NULL OR NEW.edited_at IS NOT NULL OR NEW.deleted_at IS NOT NULL
                OR conversation_type<>'channel' OR NEW.author_kind<>'system' OR NEW.author_id IS NOT NULL
                OR NEW.request_key IS NOT NULL OR NEW.send_fingerprint IS NOT NULL OR NEW.body IS NOT NULL OR NEW.content<>''
                OR action IS NULL OR action NOT IN ('member_added','member_removed','agent_added','agent_removed','channel_updated','channel_archived','channel_restored')
                OR jsonb_typeof(NEW.metadata)<>'object'
                OR NOT (NEW.metadata ? 'actorUserId')
                OR jsonb_typeof(NEW.metadata->'actorUserId') NOT IN ('string','null')
                OR (NEW.metadata ? 'subjectUserId' AND jsonb_typeof(NEW.metadata->'subjectUserId')<>'string')
                OR (NEW.metadata ? 'subjectAgentId' AND jsonb_typeof(NEW.metadata->'subjectAgentId')<>'string')
                OR (NEW.metadata - ARRAY['action','actorUserId','subjectUserId','subjectAgentId'])<>'{}'::jsonb
                OR ((action IN ('member_added','member_removed')) IS DISTINCT FROM (NEW.metadata ? 'subjectUserId'))
                OR ((action IN ('agent_added','agent_removed')) IS DISTINCT FROM (NEW.metadata ? 'subjectAgentId')) THEN
                RAISE EXCEPTION 'invalid discussion system item' USING ERRCODE='23514';
            END IF;
            -- UUID casts validate typed, non-spoofable identifiers; labels/body are absent.
            PERFORM (NEW.metadata->>'actorUserId')::uuid;
            IF NEW.metadata ? 'subjectUserId' THEN PERFORM (NEW.metadata->>'subjectUserId')::uuid; END IF;
            IF NEW.metadata ? 'subjectAgentId' THEN PERFORM (NEW.metadata->>'subjectAgentId')::uuid; END IF;
        ELSE RAISE EXCEPTION 'unsupported discussion item' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END $$;

-- Cross-row shape checks only. Authorization and mutations stay in the service.
CREATE FUNCTION check_discussion_collaboration_shape() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE item_id uuid; item conversation_items;
BEGIN
    IF TG_TABLE_NAME='conversation_thread_states' THEN item_id:=NEW.root_id;
    ELSE item_id:=NEW.message_id;
    END IF;
    SELECT * INTO item FROM conversation_items
    WHERE company_id=NEW.company_id AND conversation_id=NEW.conversation_id AND id=item_id FOR SHARE;
    IF NOT FOUND OR item.run_id IS NOT NULL OR item.type<>'user_message' THEN
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
CREATE TRIGGER discussion_collaboration_shape BEFORE INSERT OR UPDATE ON conversation_message_pins
    FOR EACH ROW EXECUTE FUNCTION check_discussion_collaboration_shape();
CREATE TRIGGER discussion_collaboration_shape BEFORE INSERT OR UPDATE ON conversation_message_reactions
    FOR EACH ROW EXECUTE FUNCTION check_discussion_collaboration_shape();
CREATE TRIGGER discussion_collaboration_shape BEFORE INSERT OR UPDATE ON conversation_thread_states
    FOR EACH ROW EXECUTE FUNCTION check_discussion_collaboration_shape();
