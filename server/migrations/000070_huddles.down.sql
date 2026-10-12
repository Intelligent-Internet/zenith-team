DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM huddles) THEN
        RAISE EXCEPTION 'cannot downgrade while huddles exist';
    END IF;
END $$;
DROP TABLE huddles;
ALTER TABLE conversation_members DROP CONSTRAINT conversation_members_source_type_check,
    ADD CONSTRAINT conversation_members_source_type_check CHECK(source_type IN ('human_direct','channel'));
ALTER TABLE conversations DROP CONSTRAINT conversations_kind_check,
    DROP CONSTRAINT conversations_request_key_kind_check,
    DROP CONSTRAINT conversations_discussion_revision_check,
    DROP CONSTRAINT conversations_shape_check,
    ADD CONSTRAINT conversations_kind_check CHECK(kind IN ('task','direct','schedule','human_direct','channel','channel_agent')),
    ADD CONSTRAINT conversations_request_key_kind_check CHECK (request_key IS NULL OR kind IN ('direct','channel')),
    ADD CONSTRAINT conversations_discussion_revision_check CHECK (kind NOT IN ('human_direct','channel') OR version BETWEEN 1 AND 9007199254740991),
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
