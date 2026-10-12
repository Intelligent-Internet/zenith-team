-- Channel execution has a private transcript and a durable logical request.
ALTER TABLE conversations
    ADD COLUMN channel_id uuid,
    ADD COLUMN channel_execution_generation bigint NOT NULL DEFAULT 1,
    ADD COLUMN channel_context_revision bigint NOT NULL DEFAULT 1,
    ADD CONSTRAINT conversations_channel_fkey FOREIGN KEY(company_id,channel_id)
        REFERENCES conversations(company_id,id) ON DELETE CASCADE,
    DROP CONSTRAINT conversations_kind_check,
    ADD CONSTRAINT conversations_kind_check CHECK(kind IN ('task','direct','schedule','human_direct','channel','channel_agent')),
    ADD CONSTRAINT conversations_channel_execution_shape CHECK((kind='channel_agent')=(channel_id IS NOT NULL)),
    DROP CONSTRAINT conversations_shape_check,

    ADD CONSTRAINT conversations_shape_check CHECK (
        (kind='task' AND schedule_id IS NULL AND agent_id IS NULL AND created_by_user_id IS NULL)
        OR (kind='channel_agent' AND channel_id IS NOT NULL AND task_id IS NULL AND schedule_id IS NULL AND agent_id IS NOT NULL AND created_by_user_id IS NOT NULL)
        OR (kind='direct' AND task_id IS NULL AND schedule_id IS NULL AND created_by_user_id IS NOT NULL)
        OR (kind='schedule' AND task_id IS NULL AND schedule_id IS NOT NULL AND agent_id IS NOT NULL AND created_by_user_id IS NOT NULL)
        OR (kind IN ('human_direct','channel') AND task_id IS NULL AND schedule_id IS NULL AND agent_id IS NULL
            AND (created_by_user_id IS NOT NULL OR is_general) AND pinned_at IS NULL
            AND (kind <> 'human_direct' OR (archived_at IS NULL AND title IS NULL))
            AND (kind <> 'channel' OR (request_key IS NOT NULL AND title IS NOT NULL
                AND title=btrim(title) AND char_length(title) BETWEEN 1 AND 100 AND octet_length(title)<=400)))
    );


CREATE UNIQUE INDEX conversations_channel_agent_unique ON conversations(channel_id,agent_id) WHERE kind='channel_agent';
ALTER TABLE runs DROP CONSTRAINT runs_run_type_check,
    ADD CONSTRAINT runs_run_type_check CHECK(run_type IN ('task','direct_chat','schedule','channel_agent'));

CREATE TABLE channel_invocations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL,
    channel_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    trigger_message_id uuid NOT NULL,
    thread_root_id uuid NOT NULL,
    caller_user_id uuid NOT NULL,
    agent_generation bigint NOT NULL,
    member_generation bigint NOT NULL,
    channel_generation bigint NOT NULL,
    trigger_content text NOT NULL,
    trigger_revision bigint NOT NULL,
    status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','active','waiting_input','completed','failed','cancelled')),
    publication_status text NOT NULL DEFAULT 'pending' CHECK(publication_status IN ('pending','published','no_output','blocked')),
    failure_reason text,
    request_key uuid,
    queued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(trigger_message_id,agent_id),
    UNIQUE(company_id,id),
    FOREIGN KEY(company_id,channel_id) REFERENCES conversations(company_id,id) ON DELETE CASCADE,
    FOREIGN KEY(company_id,conversation_id) REFERENCES conversations(company_id,id) ON DELETE CASCADE,
    FOREIGN KEY(company_id,agent_id) REFERENCES agents(company_id,id) ON DELETE CASCADE,
    FOREIGN KEY(company_id,channel_id,trigger_message_id) REFERENCES conversation_items(company_id,conversation_id,id) ON DELETE CASCADE,
    FOREIGN KEY(company_id,channel_id,thread_root_id) REFERENCES conversation_items(company_id,conversation_id,id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX channel_invocations_one_active ON channel_invocations(conversation_id) WHERE status IN ('active','waiting_input');
CREATE INDEX channel_invocations_queue ON channel_invocations(conversation_id,queued_at,id) WHERE status='queued';
ALTER TABLE runs ADD COLUMN invocation_id uuid,
    ADD CONSTRAINT runs_invocation_fkey FOREIGN KEY(company_id,invocation_id) REFERENCES channel_invocations(company_id,id) ON DELETE CASCADE;
CREATE INDEX runs_invocation_idx ON runs(invocation_id,created_at DESC) WHERE invocation_id IS NOT NULL;
CREATE UNIQUE INDEX channel_answer_invocation_unique ON conversation_items((metadata->>'invocationId'))
    WHERE run_id IS NULL AND type='agent_text';

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
    IF conversation_type IN ('task','direct','schedule','channel_agent') THEN
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
        ELSIF NEW.type='agent_text' THEN
            IF conversation_type<>'channel' OR NEW.author_kind<>'agent' OR NEW.author_id IS NULL
                OR NEW.thread_root_id IS NULL OR NEW.request_key IS NOT NULL
                OR NEW.send_fingerprint IS NOT NULL OR NEW.body IS NULL
                OR NEW.edited_at IS NOT NULL OR NEW.deleted_at IS NOT NULL
                OR NOT EXISTS (SELECT 1 FROM channel_invocations v
                    WHERE v.id=(NEW.metadata->>'invocationId')::uuid
                      AND v.company_id=NEW.company_id AND v.channel_id=NEW.conversation_id
                      AND v.agent_id=NEW.author_id AND v.thread_root_id=NEW.thread_root_id) THEN
                RAISE EXCEPTION 'invalid channel answer' USING ERRCODE='23514';
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

-- Revisions invalidate resumable context; archive/restore never restores old authority.
CREATE FUNCTION advance_channel_execution_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME='conversations' THEN
        IF OLD.kind='channel' AND NEW.archived_at IS DISTINCT FROM OLD.archived_at THEN
            NEW.channel_execution_generation := OLD.channel_execution_generation + 1;
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.run_id IS NULL AND (NEW.revision IS DISTINCT FROM OLD.revision) THEN
        UPDATE conversations SET channel_context_revision=channel_context_revision+1
        WHERE id=NEW.conversation_id AND kind='channel';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER channel_archive_generation BEFORE UPDATE OF archived_at ON conversations
    FOR EACH ROW EXECUTE FUNCTION advance_channel_execution_revision();
CREATE TRIGGER channel_context_revision AFTER UPDATE OF revision ON conversation_items
    FOR EACH ROW EXECUTE FUNCTION advance_channel_execution_revision();
