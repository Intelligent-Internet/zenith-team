-- Restores the shape, not the facts. Sharing decisions and binders are gone
-- for good: every runtime returns as private at revision 1, and an Agent's
-- runtime_id is rebuilt from its creator's binding, else its oldest binding,
-- else any live runtime of its provider in the Company — and comes back
-- NULLABLE, weaker than the original NOT NULL, because an Agent with no
-- binding and no such runtime has nothing to return to. Every other member's
-- binding is lost. To satisfy the exactly-one CHECK, the human assignee of
-- every Agent-executed Task is cleared. The Runs the up file cancelled stay
-- cancelled. Roll back only as part of a reviewed downgrade; a backup is the
-- real rollback.

DROP VIEW activity_subject_states;

DELETE FROM inbox_items WHERE kind IN ('task_run_requested', 'task_handoff');
ALTER TABLE inbox_items
    DROP CONSTRAINT inbox_items_kind_check,
    ADD CONSTRAINT inbox_items_kind_check CHECK (kind IN (
        'task_assigned', 'blocker', 'run_completed', 'run_failed',
        'approval_request', 'question_request', 'runtime_offline',
        'invitation_renewal', 'calendar_reminder', 'calendar_update',
        'discussion_mention'
    ));

ALTER TABLE runtimes
    ADD COLUMN visibility text DEFAULT 'private' NOT NULL,
    ADD COLUMN visibility_revision bigint DEFAULT 1 NOT NULL,
    ADD CONSTRAINT runtimes_visibility_check CHECK (visibility IN ('private', 'public'));

ALTER TABLE agents
    ADD COLUMN runtime_id uuid,
    ADD COLUMN runtime_bound_by_user_id uuid;

UPDATE agents
SET runtime_id = chosen.runtime_id,
    runtime_bound_by_user_id = chosen.user_id
FROM (
    SELECT DISTINCT ON (binding.agent_id)
        binding.agent_id, binding.runtime_id, binding.user_id
    FROM agent_runtime_bindings AS binding
    JOIN agents AS agent ON agent.id = binding.agent_id
    ORDER BY binding.agent_id,
        (binding.user_id = agent.created_by_user_id) DESC NULLS LAST,
        binding.created_at, binding.id
) AS chosen
WHERE agents.id = chosen.agent_id;

UPDATE agents
SET runtime_id = (
    SELECT runtime.id
    FROM runtimes AS runtime
    WHERE runtime.company_id = agents.company_id
      AND runtime.provider = agents.provider
      AND runtime.removed_at IS NULL
    ORDER BY runtime.created_at, runtime.id
    LIMIT 1
)
WHERE agents.runtime_id IS NULL;

ALTER TABLE agents
    ADD CONSTRAINT agents_company_runtime_fkey
        FOREIGN KEY (company_id, runtime_id)
        REFERENCES runtimes(company_id, id) ON DELETE RESTRICT;
CREATE INDEX agents_runtime_idx ON agents (runtime_id);

ALTER TABLE tasks
    DROP COLUMN workspace_generation;
DROP INDEX tasks_assignee_user_idx;
ALTER TABLE tasks
    ALTER COLUMN assignee_user_id DROP NOT NULL;
ALTER INDEX tasks_executed_by_agent_idx RENAME TO tasks_assignee_agent_idx;
ALTER INDEX tasks_company_executed_by_status_idx RENAME TO tasks_company_agent_status_idx;
ALTER TABLE tasks
    RENAME CONSTRAINT tasks_executed_by_agent_fkey TO tasks_assignee_agent_fkey;
ALTER TABLE tasks
    RENAME COLUMN executed_by_agent_id TO assignee_agent_id;
UPDATE tasks
SET assignee_user_id = NULL
WHERE assignee_agent_id IS NOT NULL;
ALTER TABLE tasks
    ADD CONSTRAINT tasks_assignee_exactly_one_check
        CHECK (num_nonnulls(assignee_user_id, assignee_agent_id) = 1);
CREATE INDEX tasks_assignee_user_idx
    ON tasks USING btree (company_id, assignee_user_id, number DESC)
    WHERE (assignee_user_id IS NOT NULL);

DROP TABLE agent_runtime_bindings;
ALTER TABLE runtimes
    DROP CONSTRAINT runtimes_company_id_id_provider_key;
ALTER TABLE agents
    DROP CONSTRAINT agents_company_id_id_provider_key,
    DROP CONSTRAINT agents_provider_check,
    DROP COLUMN provider;

CREATE VIEW activity_subject_states AS
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
