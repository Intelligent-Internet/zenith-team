-- A rollback must preserve authored discussion. Disable new writes and keep the
-- upgraded reader until retained discussions have an explicitly approved disposition.
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM conversations WHERE kind IN ('human_direct','channel')) THEN
        RAISE EXCEPTION 'cannot roll back retained discussions';
    END IF;
END $$;
DROP TRIGGER discussion_run_guard ON runs;
DROP FUNCTION reject_discussion_run();
DROP TRIGGER discussion_item_shape ON conversation_items;
DROP FUNCTION check_discussion_item_shape();
DROP TRIGGER discussion_identity ON conversations;
DROP TRIGGER discussion_identity ON conversation_human_directs;
DROP TRIGGER discussion_identity ON conversation_channels;
DROP TRIGGER discussion_identity ON conversation_members;
DROP TRIGGER discussion_identity ON conversation_agents;
DROP FUNCTION preserve_discussion_identity();
DROP TRIGGER discussion_shape ON conversations;
DROP TRIGGER discussion_shape ON conversation_human_directs;
DROP TRIGGER discussion_shape ON conversation_channels;
DROP TRIGGER discussion_shape ON conversation_members;
DROP TRIGGER discussion_shape ON conversation_agents;
DROP FUNCTION check_discussion_shape();
DROP TABLE conversation_agents, conversation_members, conversation_channels, conversation_human_directs;
ALTER TABLE conversation_user_states DROP CONSTRAINT conversation_user_states_discussion_revision_check,
    DROP COLUMN grant_generation, DROP COLUMN hidden_at, DROP COLUMN hidden_through_seq;
DROP INDEX conversation_items_discussion_roots_idx;
ALTER TABLE conversation_items DROP CONSTRAINT conversation_items_type_check, DROP CONSTRAINT conversation_items_author_kind_check,
    DROP COLUMN body, DROP COLUMN revision, DROP COLUMN send_fingerprint,
    ALTER COLUMN run_id SET NOT NULL, ALTER COLUMN author_id SET NOT NULL,
    ADD CONSTRAINT conversation_items_type_check CHECK(type IN ('user_message','agent_text','reasoning','tool_call','tool_result','error')),
    ADD CONSTRAINT conversation_items_author_kind_check CHECK(author_kind IN ('user','agent'));
DROP INDEX conversations_discussion_list_idx;
ALTER TABLE conversations DROP CONSTRAINT conversations_kind_check, DROP CONSTRAINT conversations_shape_check,
    DROP CONSTRAINT conversations_request_key_kind_check, DROP CONSTRAINT conversations_discussion_revision_check,
    DROP COLUMN head_seq,
    ADD CONSTRAINT conversations_kind_check CHECK(kind IN ('task','direct','schedule')),
    ADD CONSTRAINT conversations_request_key_kind_check CHECK(request_key IS NULL OR kind='direct'),
    ADD CONSTRAINT conversations_shape_check CHECK (
        (kind='task' AND schedule_id IS NULL AND agent_id IS NULL AND created_by_user_id IS NULL)
        OR (kind='direct' AND task_id IS NULL AND schedule_id IS NULL AND created_by_user_id IS NOT NULL)
        OR (kind='schedule' AND task_id IS NULL AND schedule_id IS NOT NULL AND agent_id IS NOT NULL AND created_by_user_id IS NOT NULL));
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
    CASE conversation.kind WHEN 'task' THEN 'company' ELSE 'user' END,
    CASE WHEN conversation.kind <> 'task' THEN conversation.created_by_user_id END,
    jsonb_build_object(
        'id', conversation.id, 'companyId', conversation.company_id, 'kind', conversation.kind,
        'title', conversation.title, 'taskId', conversation.task_id, 'scheduleId', conversation.schedule_id,
        'agentId', conversation.agent_id, 'createdByUserId', conversation.created_by_user_id,
        'pinnedAt', conversation.pinned_at, 'archivedAt', conversation.archived_at,
        'revision', conversation.version, 'createdAt', conversation.created_at, 'updatedAt', conversation.updated_at
    ), jsonb_build_object('entityKey', NULL, 'entityTitle', conversation.title)
FROM conversations AS conversation
UNION ALL
SELECT 'run', run.id, run.company_id, NULL::bigint,
    CASE conversation.kind WHEN 'task' THEN 'company' ELSE 'user' END,
    CASE WHEN conversation.kind <> 'task' THEN conversation.created_by_user_id END,
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
