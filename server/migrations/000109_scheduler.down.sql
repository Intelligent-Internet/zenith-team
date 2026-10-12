-- Refuse a lossy downgrade. Old binaries cannot represent linked retries,
-- cancellation receipts or missed counts. Restore a pre-upgrade backup instead
-- of silently erasing that history after the new scheduler has been used.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM runs WHERE schedule_firing_id IS NOT NULL GROUP BY schedule_firing_id HAVING count(*) > 1)
       OR EXISTS (SELECT 1 FROM schedule_firings WHERE disposition = 'cancelled' OR missed_count <> 0)
       OR EXISTS (SELECT 1 FROM schedules WHERE preset_key IS NOT NULL AND status <> 'cancelled'
           GROUP BY company_id, creator_user_id, preset_key HAVING count(*) > 1)
       OR EXISTS (SELECT 1 FROM runs WHERE task_scheduled)
       OR EXISTS (SELECT 1 FROM task_schedules)
       OR EXISTS (SELECT 1 FROM tasks WHERE assignment_status <> 'accepted' OR scheduled_at IS NOT NULL)
    THEN
        RAISE EXCEPTION 'scheduler downgrade would discard history; restore a pre-upgrade backup';
    END IF;
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
    jsonb_build_object('entityKey', task.key, 'entityTitle', task.title) AS context,
    task.project_id AS project_id
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
        ELSE jsonb_build_object('entityKey', NULL, 'entityTitle', conversation.title) END,
    conversation.project_id
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
    ), jsonb_build_object('entityKey', run.context->'source'->'key', 'entityTitle', run.context->'source'->'title'),
    conversation.project_id
FROM runs AS run JOIN conversations AS conversation ON conversation.id = run.conversation_id;

DROP INDEX runs_task_calendar_active_idx;
ALTER TABLE runs DROP COLUMN task_scheduled;
ALTER TABLE tasks DROP CONSTRAINT tasks_calendar_schedule_fk;
DROP TABLE task_schedules;
ALTER TABLE tasks DROP COLUMN assignment_status, DROP COLUMN scheduled_at,
    DROP COLUMN schedule_fired_at, DROP COLUMN task_schedule_id,
    DROP COLUMN task_schedule_generation,
    DROP COLUMN schedule_occurrence_at, DROP COLUMN schedule_disposition;

DROP INDEX schedules_creator_preset_key;
CREATE UNIQUE INDEX schedules_creator_preset_key
    ON schedules (company_id, creator_user_id, preset_key)
    WHERE preset_key IS NOT NULL AND status <> 'cancelled';
ALTER TABLE schedule_firings
    DROP CONSTRAINT schedule_firings_cancelled_run_check,
    DROP CONSTRAINT schedule_firings_disposition_check,
    ADD CONSTRAINT schedule_firings_disposition_check CHECK (
        disposition IN ('materialized', 'superseded', 'skipped', 'needs_attention')
    ),
    DROP COLUMN missed_count;
DROP INDEX runs_schedule_firing_idx;
CREATE UNIQUE INDEX runs_schedule_firing_unique_idx
    ON runs (schedule_firing_id) WHERE schedule_firing_id IS NOT NULL;
