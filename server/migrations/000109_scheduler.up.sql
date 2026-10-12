-- One forward upgrade from version 108. Definitions, receipts, messages and
-- Runs are retained; retry lineage is recovered from existing parent links.
DROP INDEX runs_schedule_firing_unique_idx;
CREATE INDEX runs_schedule_firing_idx
    ON runs (schedule_firing_id, attempt DESC)
    WHERE schedule_firing_id IS NOT NULL;

ALTER TABLE schedule_firings
    DROP CONSTRAINT schedule_firings_disposition_check,
    ADD CONSTRAINT schedule_firings_disposition_check CHECK (
        disposition IN ('materialized', 'superseded', 'skipped', 'needs_attention', 'cancelled')
    ),
    ADD CONSTRAINT schedule_firings_cancelled_run_check CHECK (
        disposition <> 'cancelled' OR run_id IS NOT NULL
    ),
    ADD COLUMN missed_count integer NOT NULL DEFAULT 0 CHECK (missed_count BETWEEN 0 AND 1000);


DROP INDEX schedules_creator_preset_key;
CREATE UNIQUE INDEX schedules_creator_preset_key
    ON schedules (company_id, creator_user_id, preset_key)
    WHERE preset_key IS NOT NULL AND status NOT IN ('completed', 'cancelled');

-- Additive cutover: existing Tasks remain accepted and are never scheduled
-- from their date-only planning fields.
ALTER TABLE tasks
    ADD COLUMN assignment_status text NOT NULL DEFAULT 'accepted'
        CHECK (assignment_status IN ('accepted', 'pending', 'declined')),
    ADD COLUMN scheduled_at timestamptz,
    ADD COLUMN schedule_fired_at timestamptz,
    ADD COLUMN task_schedule_id uuid,
    ADD COLUMN task_schedule_generation bigint NOT NULL DEFAULT 1 CHECK (task_schedule_generation > 0),
    ADD COLUMN schedule_occurrence_at timestamptz,
    ADD COLUMN schedule_disposition text NOT NULL DEFAULT ''
        CHECK (schedule_disposition IN ('', 'scheduled', 'started', 'skipped', 'missed', 'cancelled'));

CREATE TABLE task_schedules (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    template_task_id uuid NOT NULL UNIQUE,
    trigger jsonb NOT NULL,
    template_title text NOT NULL,
    template_description text NOT NULL,
    next_occurrence_at timestamptz,
    paused boolean NOT NULL DEFAULT false,
    version bigint NOT NULL DEFAULT 1,
    generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (company_id, project_id, id),
    FOREIGN KEY (company_id, project_id, template_task_id)
        REFERENCES tasks(company_id, project_id, id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE
);

ALTER TABLE tasks ADD CONSTRAINT tasks_calendar_schedule_fk
    FOREIGN KEY (company_id, project_id, task_schedule_id) REFERENCES task_schedules(company_id, project_id, id)
    ON DELETE SET NULL (task_schedule_id) DEFERRABLE INITIALLY DEFERRED;
CREATE UNIQUE INDEX tasks_calendar_occurrence_idx ON tasks(task_schedule_id, task_schedule_generation, schedule_occurrence_at)
    WHERE task_schedule_id IS NOT NULL;
CREATE INDEX tasks_calendar_window_idx ON tasks(company_id, scheduled_at, id)
    WHERE scheduled_at IS NOT NULL;
CREATE INDEX tasks_calendar_due_idx ON tasks(scheduled_at, id)
    WHERE schedule_disposition = 'scheduled' AND assignment_status = 'accepted';
CREATE INDEX task_schedules_due_idx ON task_schedules(next_occurrence_at, id)
    WHERE NOT paused AND next_occurrence_at IS NOT NULL;
CREATE INDEX task_schedules_project_idx ON task_schedules(company_id, project_id);

CREATE INDEX tasks_calendar_series_due_idx ON tasks(task_schedule_id, task_schedule_generation, scheduled_at DESC)
    WHERE schedule_disposition IN ('scheduled', 'started');

-- First scheduled attempts retain Calendar consent/pause fences while waiting
-- for a computer. Retries and manual Runs use the ordinary Task lifecycle.
ALTER TABLE runs ADD COLUMN task_scheduled boolean NOT NULL DEFAULT false;

CREATE INDEX runs_task_calendar_active_idx ON runs(company_id, task_id)
    WHERE task_id IS NOT NULL AND status NOT IN ('completed', 'failed', 'cancelled');

-- Calendar changes retain the same owner Activity snapshots as ordinary Task edits.
CREATE OR REPLACE VIEW activity_subject_states AS
SELECT 'task'::text AS entity_type, task.id AS entity_id, task.company_id,
    task.version AS revision, 'company'::text AS visibility, NULL::uuid AS visibility_user_id,
    jsonb_build_object(
        'id', task.id, 'companyId', task.company_id, 'key', task.key, 'number', task.number,
        'title', task.title, 'description', task.description, 'status', task.status,
        'priority', task.priority, 'labels', task.labels, 'startDate', task.start_date, 'dueDate', task.due_date,
        'assignmentStatus', task.assignment_status, 'scheduledAt', task.scheduled_at,
        'scheduleOccurrenceAt', task.schedule_occurrence_at, 'scheduleFiredAt', task.schedule_fired_at,
        'scheduleDisposition', task.schedule_disposition,
        'schedule', (SELECT jsonb_build_object('id', s.id, 'trigger', s.trigger, 'paused', s.paused)
            FROM task_schedules s WHERE s.id = task.task_schedule_id),
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

-- Backfill after DDL: deferred Run foreign keys must not leave pending
-- triggers before ALTER TABLE in this single migration transaction.
WITH RECURSIVE lineage AS (
    SELECT id, company_id, conversation_id, schedule_firing_id
    FROM runs WHERE schedule_firing_id IS NOT NULL
    UNION ALL
    SELECT child.id, child.company_id, child.conversation_id, lineage.schedule_firing_id
    FROM runs AS child
    JOIN lineage ON child.parent_run_id = lineage.id
        AND child.company_id = lineage.company_id
        AND child.conversation_id = lineage.conversation_id
    WHERE child.schedule_firing_id IS NULL
)
UPDATE runs SET schedule_firing_id = lineage.schedule_firing_id
FROM lineage WHERE runs.id = lineage.id AND runs.schedule_firing_id IS NULL;
