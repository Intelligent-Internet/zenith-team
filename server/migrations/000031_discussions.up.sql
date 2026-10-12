-- C1 human discussion. Legacy execution history keeps its attribution and
-- allocator. Only discussion writers use head_seq; no later chat feature tables.
ALTER TABLE conversations
    ADD COLUMN head_seq bigint NOT NULL DEFAULT 0 CHECK (head_seq BETWEEN 0 AND 9007199254740991),
    DROP CONSTRAINT conversations_kind_check,
    DROP CONSTRAINT conversations_shape_check,
    DROP CONSTRAINT conversations_request_key_kind_check,
    ADD CONSTRAINT conversations_kind_check CHECK (kind IN ('task','direct','schedule','human_direct','channel')),
    ADD CONSTRAINT conversations_request_key_kind_check CHECK (request_key IS NULL OR kind IN ('direct','channel')),
    ADD CONSTRAINT conversations_discussion_revision_check CHECK (kind NOT IN ('human_direct','channel') OR version BETWEEN 1 AND 9007199254740991),
    ADD CONSTRAINT conversations_shape_check CHECK (
        (kind='task' AND schedule_id IS NULL AND agent_id IS NULL AND created_by_user_id IS NULL)
        OR (kind='direct' AND task_id IS NULL AND schedule_id IS NULL AND created_by_user_id IS NOT NULL)
        OR (kind='schedule' AND task_id IS NULL AND schedule_id IS NOT NULL AND agent_id IS NOT NULL AND created_by_user_id IS NOT NULL)
        OR (kind IN ('human_direct','channel') AND task_id IS NULL AND schedule_id IS NULL AND agent_id IS NULL
            AND created_by_user_id IS NOT NULL AND pinned_at IS NULL
            AND (kind <> 'human_direct' OR (archived_at IS NULL AND title IS NULL))
            AND (kind <> 'channel' OR (request_key IS NOT NULL AND title IS NOT NULL
                AND title=btrim(title) AND char_length(title) BETWEEN 1 AND 100 AND octet_length(title)<=400)))
    );
CREATE INDEX conversations_discussion_list_idx ON conversations(company_id,kind,updated_at DESC,id DESC)
    WHERE kind IN ('human_direct','channel');

CREATE TABLE conversation_human_directs (
    created_at timestamptz NOT NULL DEFAULT now(),
    company_id uuid NOT NULL,
    conversation_id uuid PRIMARY KEY,
    user_low_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    user_high_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    CHECK (user_low_id < user_high_id),
    UNIQUE(company_id,user_low_id,user_high_id),
    FOREIGN KEY(company_id,conversation_id) REFERENCES conversations(company_id,id) ON DELETE CASCADE
);
CREATE TABLE conversation_channels (
    created_at timestamptz NOT NULL DEFAULT now(),
    company_id uuid NOT NULL,
    conversation_id uuid PRIMARY KEY,
    topic text NOT NULL DEFAULT '' CHECK (octet_length(topic)<=2048),
    configuration_revision bigint NOT NULL DEFAULT 1 CHECK (configuration_revision BETWEEN 1 AND 9007199254740991),
    create_fingerprint bytea NOT NULL CHECK (octet_length(create_fingerprint)=32),
    FOREIGN KEY(company_id,conversation_id) REFERENCES conversations(company_id,id) ON DELETE CASCADE
);
CREATE TABLE conversation_members (
    created_at timestamptz NOT NULL DEFAULT now(),
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    user_id uuid NOT NULL,
    source_type text NOT NULL CHECK(source_type IN ('human_direct','channel')),
    status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','revoked')),
    generation bigint NOT NULL DEFAULT 1 CHECK(generation BETWEEN 1 AND 9007199254740991),
    version bigint NOT NULL DEFAULT 1 CHECK(version BETWEEN 1 AND 9007199254740991),
    joined_at timestamptz NOT NULL DEFAULT now(),
    joined_seq bigint NOT NULL DEFAULT 0 CHECK(joined_seq BETWEEN 0 AND 9007199254740991),
    joined_by_user_id uuid,
    revoked_at timestamptz,
    revoked_by_user_id uuid,
    PRIMARY KEY(conversation_id,user_id),
    CHECK((status='active' AND revoked_at IS NULL) OR (status='revoked' AND revoked_at IS NOT NULL)),
    FOREIGN KEY(company_id,conversation_id) REFERENCES conversations(company_id,id) ON DELETE CASCADE,
    FOREIGN KEY(company_id,user_id) REFERENCES company_members(company_id,user_id) ON DELETE CASCADE
);
CREATE INDEX conversation_members_active_user_idx ON conversation_members(company_id,user_id,conversation_id) WHERE status='active';
CREATE INDEX conversation_members_roster_idx ON conversation_members(conversation_id,status,user_id);
CREATE TABLE conversation_agents (
    created_at timestamptz NOT NULL DEFAULT now(),
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','revoked')),
    generation bigint NOT NULL DEFAULT 1 CHECK(generation BETWEEN 1 AND 9007199254740991),
    version bigint NOT NULL DEFAULT 1 CHECK(version BETWEEN 1 AND 9007199254740991),
    joined_at timestamptz NOT NULL DEFAULT now(),
    joined_by_user_id uuid,
    revoked_at timestamptz,
    revoked_by_user_id uuid,
    PRIMARY KEY(conversation_id,agent_id),
    CHECK((status='active' AND revoked_at IS NULL) OR (status='revoked' AND revoked_at IS NOT NULL)),
    FOREIGN KEY(company_id,conversation_id) REFERENCES conversations(company_id,id) ON DELETE CASCADE,
    FOREIGN KEY(company_id,agent_id) REFERENCES agents(company_id,id) ON DELETE CASCADE
);
CREATE INDEX conversation_agents_active_agent_idx ON conversation_agents(company_id,agent_id,conversation_id) WHERE status='active';

ALTER TABLE conversation_user_states
    ADD COLUMN grant_generation bigint CHECK(grant_generation BETWEEN 1 AND 9007199254740991),
    ADD COLUMN hidden_at timestamptz,
    ADD COLUMN hidden_through_seq bigint NOT NULL DEFAULT 0 CHECK(hidden_through_seq BETWEEN 0 AND 9007199254740991),
    ADD CONSTRAINT conversation_user_states_discussion_revision_check CHECK(grant_generation IS NULL OR version BETWEEN 0 AND 9007199254740991);
ALTER TABLE conversation_items
    ALTER COLUMN run_id DROP NOT NULL,
    ALTER COLUMN author_id DROP NOT NULL,
    ADD COLUMN body jsonb,
    ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK(revision BETWEEN 1 AND 9007199254740991),
    ADD COLUMN send_fingerprint bytea,
    DROP CONSTRAINT conversation_items_type_check,
    DROP CONSTRAINT conversation_items_author_kind_check,
    ADD CONSTRAINT conversation_items_type_check CHECK(type IN ('user_message','agent_text','reasoning','tool_call','tool_result','error','discussion_system')),
    ADD CONSTRAINT conversation_items_author_kind_check CHECK(author_kind IN ('user','agent','system'));
CREATE INDEX conversation_items_discussion_roots_idx ON conversation_items(conversation_id,seq DESC) WHERE run_id IS NULL;

-- Deferred shape validation permits atomic creation and lifecycle cleanup.
-- It checks final rows, not the intermediate NEW image of a deferred trigger.
CREATE FUNCTION check_discussion_shape() RETURNS trigger LANGUAGE plpgsql AS $$
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
            OR (c.archived_at IS NULL AND humans=0) OR humans>50
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
CREATE CONSTRAINT TRIGGER discussion_shape AFTER INSERT OR UPDATE ON conversations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_discussion_shape();
CREATE CONSTRAINT TRIGGER discussion_shape AFTER INSERT OR UPDATE OR DELETE ON conversation_human_directs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_discussion_shape();
CREATE CONSTRAINT TRIGGER discussion_shape AFTER INSERT OR UPDATE OR DELETE ON conversation_channels DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_discussion_shape();
CREATE CONSTRAINT TRIGGER discussion_shape AFTER INSERT OR UPDATE OR DELETE ON conversation_members DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_discussion_shape();
CREATE CONSTRAINT TRIGGER discussion_shape AFTER INSERT OR UPDATE OR DELETE ON conversation_agents DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_discussion_shape();

CREATE FUNCTION preserve_discussion_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME='conversations' THEN
        IF (OLD.kind IN ('human_direct','channel') OR NEW.kind IN ('human_direct','channel')) AND
            (NEW.kind IS DISTINCT FROM OLD.kind OR NEW.company_id IS DISTINCT FROM OLD.company_id OR NEW.id IS DISTINCT FROM OLD.id
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
CREATE TRIGGER discussion_identity BEFORE UPDATE ON conversations FOR EACH ROW EXECUTE FUNCTION preserve_discussion_identity();
CREATE TRIGGER discussion_identity BEFORE UPDATE ON conversation_human_directs FOR EACH ROW EXECUTE FUNCTION preserve_discussion_identity();
CREATE TRIGGER discussion_identity BEFORE UPDATE ON conversation_channels FOR EACH ROW EXECUTE FUNCTION preserve_discussion_identity();
CREATE TRIGGER discussion_identity BEFORE UPDATE ON conversation_members FOR EACH ROW EXECUTE FUNCTION preserve_discussion_identity();
CREATE TRIGGER discussion_identity BEFORE UPDATE ON conversation_agents FOR EACH ROW EXECUTE FUNCTION preserve_discussion_identity();

CREATE FUNCTION check_discussion_item_shape() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE conversation_type text; action text;
BEGIN
    IF TG_OP='UPDATE' AND (OLD.run_id IS NULL OR NEW.run_id IS NULL) AND
        (NEW.id IS DISTINCT FROM OLD.id OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id
        OR NEW.company_id IS DISTINCT FROM OLD.company_id OR NEW.seq IS DISTINCT FROM OLD.seq) THEN
        RAISE EXCEPTION 'immutable item identity' USING ERRCODE='23514';
    END IF;
    IF TG_OP='UPDATE' AND OLD.run_id IS NULL AND
        (NEW.author_id IS DISTINCT FROM OLD.author_id OR NEW.author_kind IS DISTINCT FROM OLD.author_kind
        OR NEW.request_key IS DISTINCT FROM OLD.request_key OR NEW.send_fingerprint IS DISTINCT FROM OLD.send_fingerprint
        OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.type IS DISTINCT FROM OLD.type) THEN
        RAISE EXCEPTION 'immutable discussion receipt' USING ERRCODE='23514';
    END IF;
    SELECT kind INTO conversation_type FROM conversations WHERE id=NEW.conversation_id AND company_id=NEW.company_id;
    IF conversation_type IN ('task','direct','schedule') THEN
        IF NEW.run_id IS NULL OR NEW.author_id IS NULL OR NEW.author_kind='system' OR NEW.type='discussion_system'
            OR NEW.body IS NOT NULL OR NEW.send_fingerprint IS NOT NULL OR NEW.revision<>1 THEN
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
                OR NEW.body IS NULL OR jsonb_typeof(NEW.body)<>'object' OR NEW.metadata<>'{}'::jsonb THEN
                RAISE EXCEPTION 'invalid discussion message receipt' USING ERRCODE='23514';
            END IF;
        ELSIF NEW.type='discussion_system' THEN
            action:=NEW.metadata->>'action';
            IF conversation_type<>'channel' OR NEW.author_kind<>'system' OR NEW.author_id IS NOT NULL
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
CREATE TRIGGER discussion_item_shape BEFORE INSERT OR UPDATE ON conversation_items FOR EACH ROW EXECUTE FUNCTION check_discussion_item_shape();

-- C1 discussions have no execution lane, including direct SQL callers.
CREATE FUNCTION reject_discussion_run() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS(SELECT 1 FROM conversations WHERE id=NEW.conversation_id AND kind IN ('human_direct','channel')) THEN
        RAISE EXCEPTION 'discussion cannot own Runs' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER discussion_run_guard BEFORE INSERT OR UPDATE OF conversation_id ON runs FOR EACH ROW EXECUTE FUNCTION reject_discussion_run();

CREATE OR REPLACE VIEW activity_subject_states AS
SELECT 'task'::text AS entity_type, task.id AS entity_id, task.company_id,
    task.version AS revision, 'company'::text AS visibility, NULL::uuid AS visibility_user_id,
    jsonb_build_object(
        'id', task.id, 'companyId', task.company_id, 'key', task.key, 'number', task.number,
        'title', task.title, 'description', task.description, 'status', task.status,
        'priority', task.priority, 'labels', task.labels, 'startDate', task.start_date, 'dueDate', task.due_date,
        'assigneeUserId', task.assignee_user_id, 'assigneeAgentId', task.assignee_agent_id,
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
    CASE WHEN conversation.kind IN ('human_direct','channel') THEN jsonb_build_object(
        'id', conversation.id, 'companyId', conversation.company_id, 'kind', conversation.kind,
        'revision', conversation.version, 'createdAt', conversation.created_at, 'updatedAt', conversation.updated_at
    ) ELSE jsonb_build_object(
        'id', conversation.id, 'companyId', conversation.company_id, 'kind', conversation.kind,
        'title', conversation.title, 'taskId', conversation.task_id, 'scheduleId', conversation.schedule_id,
        'agentId', conversation.agent_id, 'createdByUserId', conversation.created_by_user_id,
        'pinnedAt', conversation.pinned_at, 'archivedAt', conversation.archived_at,
        'revision', conversation.version, 'createdAt', conversation.created_at, 'updatedAt', conversation.updated_at) END,
    CASE WHEN conversation.kind IN ('human_direct','channel') THEN '{}'::jsonb
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
