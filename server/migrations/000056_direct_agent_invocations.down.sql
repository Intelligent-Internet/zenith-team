-- Restore the channel-only answer and context-revision rules.
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
                    WHERE v.id=NEW.invocation_id
                      AND v.company_id=NEW.company_id AND v.channel_id=NEW.conversation_id
                      AND v.agent_id=NEW.author_id AND v.thread_root_id=NEW.thread_root_id) THEN
                RAISE EXCEPTION 'invalid channel answer' USING ERRCODE='23514';
            END IF;
        ELSIF NEW.type='discussion_system' THEN
            action:=NEW.metadata->>'action';
            IF NEW.thread_root_id IS NOT NULL OR NEW.edited_at IS NOT NULL OR NEW.deleted_at IS NOT NULL
                OR (conversation_type='human_direct' AND (action IS DISTINCT FROM 'members_added'
                    OR NOT EXISTS(SELECT 1 FROM conversation_human_directs WHERE conversation_id=NEW.conversation_id AND participant_ids IS NOT NULL)))
                OR NEW.author_kind<>'system' OR NEW.author_id IS NOT NULL
                OR NEW.request_key IS NOT NULL OR NEW.send_fingerprint IS NOT NULL OR NEW.body IS NOT NULL OR NEW.content<>''
                OR action IS NULL OR action NOT IN ('members_added','member_added','member_removed','agent_added','agent_removed','channel_updated','channel_archived','channel_restored')
                OR jsonb_typeof(NEW.metadata)<>'object'
                OR NOT (NEW.metadata ? 'actorUserId')
                OR jsonb_typeof(NEW.metadata->'actorUserId') NOT IN ('string','null')
                OR (NEW.metadata ? 'subjectUserId' AND jsonb_typeof(NEW.metadata->'subjectUserId')<>'string')
                OR (NEW.metadata ? 'subjectAgentId' AND jsonb_typeof(NEW.metadata->'subjectAgentId')<>'string')
                OR (NEW.metadata - ARRAY['action','actorUserId','subjectUserId','subjectAgentId','subjectUserIds','subjectAgentIds'])<>'{}'::jsonb
                OR ((action IN ('member_added','member_removed')) IS DISTINCT FROM (NEW.metadata ? 'subjectUserId'))
                OR ((action IN ('agent_added','agent_removed')) IS DISTINCT FROM (NEW.metadata ? 'subjectAgentId')) THEN
                RAISE EXCEPTION 'invalid discussion system item' USING ERRCODE='23514';
            END IF;
            IF action='members_added' THEN
                IF jsonb_typeof(NEW.metadata->'actorUserId') IS DISTINCT FROM 'string'
                    OR (NOT (NEW.metadata ? 'subjectUserIds') AND NOT (NEW.metadata ? 'subjectAgentIds'))
                    OR (NEW.metadata ? 'subjectUserIds' AND jsonb_typeof(NEW.metadata->'subjectUserIds')<>'array')
                    OR (NEW.metadata ? 'subjectAgentIds' AND jsonb_typeof(NEW.metadata->'subjectAgentIds')<>'array') THEN
                    RAISE EXCEPTION 'invalid invitation subjects' USING ERRCODE='23514';
                END IF;
                IF (NEW.metadata ? 'subjectUserIds' AND jsonb_array_length(NEW.metadata->'subjectUserIds') NOT BETWEEN 1 AND 50)
                    OR (NEW.metadata ? 'subjectAgentIds' AND jsonb_array_length(NEW.metadata->'subjectAgentIds') NOT BETWEEN 1 AND 20)
                    OR EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(NEW.metadata->'subjectUserIds','[]'::jsonb)) item WHERE jsonb_typeof(item)<>'string')
                    OR EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(NEW.metadata->'subjectAgentIds','[]'::jsonb)) item WHERE jsonb_typeof(item)<>'string') THEN
                    RAISE EXCEPTION 'invalid invitation subjects' USING ERRCODE='23514';
                END IF;
                PERFORM value::uuid FROM jsonb_array_elements_text(COALESCE(NEW.metadata->'subjectUserIds','[]'::jsonb));
                PERFORM value::uuid FROM jsonb_array_elements_text(COALESCE(NEW.metadata->'subjectAgentIds','[]'::jsonb));
            ELSIF NEW.metadata ? 'subjectUserIds' OR NEW.metadata ? 'subjectAgentIds' THEN
                RAISE EXCEPTION 'unexpected invitation subjects' USING ERRCODE='23514';
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

CREATE OR REPLACE FUNCTION advance_channel_execution_revision() RETURNS trigger LANGUAGE plpgsql AS $$
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
