BEGIN;

-- Agents only. A Task names an assignee — a member or any active Agent — and
-- nothing else; the "department agent" owner pointer, the six-key department
-- vocabulary, and the department configuration tables are retired. Agents
-- keep the appearance that used to hang off the department configuration.

ALTER TABLE agents
    ADD COLUMN avatar_key text,
    ADD COLUMN accent_color text;

UPDATE agents
SET avatar_key = configs.avatar_key,
    accent_color = configs.accent_color
FROM department_configs AS configs
WHERE configs.company_id = agents.company_id
  AND configs.agent_id = agents.id;

-- The shape of an avatar key and an accent colour is a service-layer rule
-- (normalizeAvatarKey, normalizeAccentColor), like every other format rule.

-- Replace the current Task snapshot before dropping its retired owner column.
-- Historical Activity and Inbox records remain evidence and are not rewritten.
CREATE OR REPLACE VIEW activity_subject_states AS
SELECT 'task'::text AS entity_type, task.id AS entity_id, task.company_id,
    task.version AS revision, 'company'::text AS visibility, NULL::uuid AS visibility_user_id,
    jsonb_build_object(
        'id', task.id, 'companyId', task.company_id, 'key', task.key, 'number', task.number,
        'title', task.title, 'description', task.description, 'status', task.status,
        'priority', task.priority, 'labels', task.labels, 'startDate', task.start_date, 'dueDate', task.due_date,
        'assigneeUserId', task.assignee_user_id, 'assigneeAgentId', task.assignee_agent_id,
        'creatorUserId', task.creator_user_id, 'creatorAgentId', task.creator_agent_id,
        'milestoneType', task.milestone_type,
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

ALTER TABLE task_report_events DROP COLUMN department_key;

DROP INDEX tasks_department_agent_idx;
ALTER TABLE tasks DROP CONSTRAINT tasks_department_agent_fkey;
ALTER TABLE tasks DROP COLUMN department_agent_id;

DROP TABLE department_capabilities;
DROP TABLE department_configs;
DROP INDEX agents_company_department_key_idx;
ALTER TABLE agents DROP CONSTRAINT agents_company_id_id_department_key_key;
ALTER TABLE agents DROP COLUMN department_key;

COMMIT;
