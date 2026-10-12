-- Huddles are bounded call sessions rooted in a human discussion. The chat
-- Conversation is separate from the source DM/group and starts with no
-- members; admission grants its members after a confirmed media Join.
ALTER TABLE conversations
    DROP CONSTRAINT conversations_kind_check,
    DROP CONSTRAINT conversations_request_key_kind_check,
    DROP CONSTRAINT conversations_discussion_revision_check,
    DROP CONSTRAINT conversations_shape_check,
    ADD CONSTRAINT conversations_kind_check CHECK (kind IN ('task','direct','schedule','human_direct','channel','channel_agent','huddle')),
    ADD CONSTRAINT conversations_request_key_kind_check CHECK (request_key IS NULL OR kind IN ('direct','channel')),
    ADD CONSTRAINT conversations_discussion_revision_check CHECK (kind NOT IN ('human_direct','channel','huddle') OR version BETWEEN 1 AND 9007199254740991),
    ADD CONSTRAINT conversations_shape_check CHECK (
        (kind='task' AND schedule_id IS NULL AND agent_id IS NULL AND created_by_user_id IS NULL)
        OR (kind='channel_agent' AND channel_id IS NOT NULL AND task_id IS NULL AND schedule_id IS NULL AND agent_id IS NOT NULL AND created_by_user_id IS NOT NULL)
        OR (kind='direct' AND task_id IS NULL AND schedule_id IS NULL AND created_by_user_id IS NOT NULL)
        OR (kind='schedule' AND task_id IS NULL AND schedule_id IS NOT NULL AND agent_id IS NOT NULL AND created_by_user_id IS NOT NULL)
        OR (kind IN ('human_direct','channel','huddle') AND task_id IS NULL AND schedule_id IS NULL AND agent_id IS NULL
            AND (created_by_user_id IS NOT NULL OR is_general) AND pinned_at IS NULL
            AND (kind <> 'human_direct' OR (archived_at IS NULL AND title IS NULL))
            AND (kind <> 'channel' OR (request_key IS NOT NULL AND title IS NOT NULL
                AND title=btrim(title) AND char_length(title) BETWEEN 1 AND 100 AND octet_length(title)<=400))
            AND (kind <> 'huddle' OR (archived_at IS NULL AND title IS NULL AND request_key IS NULL)))
    );

ALTER TABLE conversation_members DROP CONSTRAINT conversation_members_source_type_check,
    ADD CONSTRAINT conversation_members_source_type_check CHECK(source_type IN ('human_direct','channel','huddle'));

CREATE TABLE huddles (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL,
    source_conversation_id uuid NOT NULL,
    chat_conversation_id uuid NOT NULL,
    started_by_user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    state text NOT NULL DEFAULT 'starting' CHECK(state IN ('starting','open','ending','ended')),
    revision bigint NOT NULL DEFAULT 1 CHECK(revision BETWEEN 1 AND 9007199254740991),
    deadline_at timestamptz NOT NULL,
    room_name text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    ended_at timestamptz,
    PRIMARY KEY(id),
    UNIQUE(company_id,id),
    UNIQUE(company_id,chat_conversation_id),
    UNIQUE(company_id,started_by_user_id,request_key),
    FOREIGN KEY(company_id,source_conversation_id) REFERENCES conversations(company_id,id) ON DELETE RESTRICT,
    FOREIGN KEY(company_id,chat_conversation_id) REFERENCES conversations(company_id,id) ON DELETE RESTRICT,
    FOREIGN KEY(company_id,started_by_user_id) REFERENCES company_members(company_id,user_id) ON DELETE RESTRICT,
    CHECK((state='ended' AND ended_at IS NOT NULL) OR (state<>'ended' AND ended_at IS NULL)),
    CHECK(room_name IS NULL OR (room_name=btrim(room_name) AND char_length(room_name) BETWEEN 1 AND 128))
);
CREATE UNIQUE INDEX huddles_open_source_idx ON huddles(company_id,source_conversation_id)
    WHERE state IN ('starting','open','ending');
CREATE INDEX huddles_company_created_idx ON huddles(company_id,created_at DESC,id DESC);

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
    ELSIF conversation_type IN ('human_direct','channel','huddle') THEN
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
            IF conversation_type NOT IN ('channel','human_direct') OR NEW.author_kind<>'agent' OR NEW.author_id IS NULL
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

CREATE OR REPLACE FUNCTION reject_discussion_run() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS(SELECT 1 FROM conversations WHERE id=NEW.conversation_id AND kind IN ('human_direct','channel','huddle')) THEN
        RAISE EXCEPTION 'discussion cannot own Runs' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;

CREATE OR REPLACE VIEW activity_subject_states AS
SELECT 'task'::text AS entity_type, task.id AS entity_id, task.company_id,
    task.version AS revision, 'company'::text AS visibility, NULL::uuid AS visibility_user_id,
    jsonb_build_object(
        'id', task.id, 'companyId', task.company_id, 'key', task.key, 'number', task.number,
        'title', task.title, 'description', task.description, 'status', task.status,
        'priority', task.priority, 'labels', task.labels, 'startDate', task.start_date, 'dueDate', task.due_date,
        'assigneeUserId', task.assignee_user_id, 'executedByAgentId', task.executed_by_agent_id,
        'creatorUserId', task.creator_user_id, 'creatorAgentId', task.creator_agent_id,
        'projectId', task.project_id, 'milestoneType', task.milestone_type,
        'conversationId', task.conversation_id, 'completedAt', task.completed_at,
        'prerequisiteIds', COALESCE((SELECT jsonb_agg(dependency.prerequisite_task_id ORDER BY dependency.prerequisite_task_id)
            FROM task_dependencies AS dependency WHERE dependency.dependent_task_id = task.id), '[]'::jsonb),
        'revision', task.version, 'createdAt', task.created_at, 'updatedAt', task.updated_at
    ) AS state,
    jsonb_build_object('entityKey', task.key, 'entityTitle', task.title) AS context
FROM tasks AS task
UNION ALL
SELECT 'conversation', conversation.id, conversation.company_id, conversation.version,
    CASE conversation.kind WHEN 'task' THEN 'company' WHEN 'direct' THEN 'user' WHEN 'schedule' THEN 'user' END,
    CASE WHEN conversation.kind IN ('direct','schedule') THEN conversation.created_by_user_id END,
    CASE WHEN conversation.kind IN ('human_direct','channel','huddle') THEN jsonb_build_object(
        'id', conversation.id, 'companyId', conversation.company_id, 'kind', conversation.kind,
        'revision', conversation.version, 'createdAt', conversation.created_at, 'updatedAt', conversation.updated_at
    ) ELSE jsonb_build_object(
        'id', conversation.id, 'companyId', conversation.company_id, 'kind', conversation.kind,
        'title', conversation.title, 'taskId', conversation.task_id, 'scheduleId', conversation.schedule_id,
        'agentId', conversation.agent_id, 'createdByUserId', conversation.created_by_user_id,
        'pinnedAt', conversation.pinned_at, 'archivedAt', conversation.archived_at,
        'revision', conversation.version, 'createdAt', conversation.created_at, 'updatedAt', conversation.updated_at) END,
    CASE WHEN conversation.kind IN ('human_direct','channel','huddle') THEN '{}'::jsonb
        ELSE jsonb_build_object('entityKey', NULL, 'entityTitle', conversation.title) END
FROM conversations AS conversation
UNION ALL
SELECT 'run', run.id, run.company_id, NULL::bigint,
    CASE conversation.kind WHEN 'task' THEN 'company' WHEN 'direct' THEN 'user' WHEN 'schedule' THEN 'user' END,
    CASE WHEN conversation.kind IN ('direct','schedule') THEN conversation.created_by_user_id END,
    jsonb_build_object(
        'id', run.id, 'companyId', run.company_id, 'runType', run.run_type, 'revision', run.revision,
        'conversationId', run.conversation_id, 'taskId', run.task_id, 'agentId', run.agent_id, 'runtimeId', run.runtime_id,
        'status', run.status, 'failureReason', run.failure_reason, 'waitReason', run.wait_reason,
        'attempt', run.attempt, 'parentRunId', run.parent_run_id, 'taskExecutionGeneration', run.task_execution_generation,
        'createdAt', run.created_at, 'fireAt', run.fire_at,
        'source', CASE WHEN run.revision IS NOT NULL THEN jsonb_build_object(
            'id', run.context->'source'->'entityId', 'type', run.context->'source'->'entityType',
            'key', run.context->'source'->'key', 'title', run.context->'source'->'title') END
    ), jsonb_build_object('entityKey', run.context->'source'->'key', 'entityTitle', run.context->'source'->'title')
FROM runs AS run JOIN conversations AS conversation ON conversation.id = run.conversation_id;
