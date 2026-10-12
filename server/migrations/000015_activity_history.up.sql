BEGIN;

ALTER TABLE activity_events ADD COLUMN revision bigint;
ALTER TABLE activity_events ADD COLUMN visibility text;
ALTER TABLE activity_events ADD COLUMN visibility_user_id uuid;
ALTER TABLE activity_events ADD CONSTRAINT activity_events_revision_check CHECK (
    revision IS NULL OR (entity_type IN ('task', 'conversation') AND revision BETWEEN 1 AND 9007199254740991)
);
ALTER TABLE activity_events ADD CONSTRAINT activity_events_visibility_check CHECK (
    (visibility IS NULL AND visibility_user_id IS NULL)
    OR (visibility IS NOT NULL AND (
        (visibility = 'company' AND visibility_user_id IS NULL)
        OR (visibility = 'user' AND visibility_user_id IS NOT NULL)))
);
CREATE UNIQUE INDEX activity_events_entity_revision_idx
    ON activity_events (company_id, entity_type, entity_id, revision DESC)
    WHERE entity_type IN ('task', 'conversation') AND revision IS NOT NULL;
CREATE INDEX activity_events_actor_history_idx
    ON activity_events (company_id, actor_type, actor_id, created_at DESC, id DESC);

-- Keep the original redacted payload, including historical numeric IDs.
-- The retired attribution remains evidence inside the legacy envelope only.
UPDATE activity_events SET details = jsonb_build_object(
    'schemaVersion', 0, 'payload', details,
    'context', jsonb_build_object('legacy', jsonb_build_object(
        'runId', run_id, 'taskId', task_id, 'conversationId', conversation_id, 'agentId', agent_id
    ))
);
ALTER TABLE activity_events DROP COLUMN run_id;

-- The old sink used the Bus topic even when the payload identified a Message.
UPDATE activity_events SET entity_type = 'message'
WHERE action = 'run.message_created' AND entity_type = 'run';
UPDATE activity_events SET entity_id = company_id::text
WHERE action = 'mailbox.changed' AND entity_id = '';
UPDATE activity_events SET entity_id = details->'payload'->>'draftId'
WHERE action = 'mailbox.draft_changed' AND entity_id = ''
    AND details->'payload'->>'draftId' IS NOT NULL;

-- These subjects have Company scope independently of their continued existence.
UPDATE activity_events SET visibility = 'company'
WHERE conversation_id IS NULL
  AND entity_type IN ('company', 'agent', 'goal', 'task', 'runtime', 'resource', 'deliverable', 'publication', 'commerce', 'mailbox');
-- A known Conversation takes precedence over a convenience Task attribution.
UPDATE activity_events AS activity
SET visibility = CASE conversation.kind WHEN 'task' THEN 'company' ELSE 'user' END,
    visibility_user_id = CASE WHEN conversation.kind <> 'task' THEN conversation.created_by_user_id END
FROM conversations AS conversation
WHERE conversation.company_id = activity.company_id
  AND (conversation.id = activity.conversation_id
       OR (activity.entity_type = 'conversation' AND conversation.id::text = activity.entity_id));
UPDATE activity_events AS activity
SET visibility = CASE conversation.kind WHEN 'task' THEN 'company' ELSE 'user' END,
    visibility_user_id = CASE WHEN conversation.kind <> 'task' THEN conversation.created_by_user_id END
FROM runs JOIN conversations AS conversation ON conversation.id = runs.conversation_id
WHERE activity.entity_type = 'run' AND activity.entity_id = runs.id::text
  AND activity.company_id = runs.company_id;
UPDATE activity_events AS activity
SET visibility = CASE WHEN approval.origin_user_id IS NULL THEN 'company' ELSE 'user' END,
    visibility_user_id = approval.origin_user_id
FROM approvals AS approval
WHERE activity.entity_type = 'approval' AND activity.entity_id = approval.id::text
  AND activity.company_id = approval.company_id;
UPDATE activity_events AS activity
SET visibility = CASE WHEN question.origin_user_id IS NULL THEN 'company' ELSE 'user' END,
    visibility_user_id = question.origin_user_id
FROM questions AS question
WHERE activity.entity_type = 'question' AND activity.entity_id = question.id::text
  AND activity.company_id = question.company_id;
UPDATE activity_events AS activity SET visibility = 'user', visibility_user_id = item.recipient_user_id
FROM inbox_items AS item
WHERE activity.entity_type = 'inbox' AND activity.entity_id = item.id::text
  AND activity.company_id = item.company_id;

-- Full allowlists for the three captured owners. This view is read inside the
-- owner's mutation transaction, under its existing locks, before and after the
-- write. It contains no provider errors, input prompts, credentials or leases.
CREATE VIEW activity_subject_states AS
SELECT 'task'::text AS entity_type, task.id AS entity_id, task.company_id,
    task.version AS revision, 'company'::text AS visibility, NULL::uuid AS visibility_user_id,
    jsonb_build_object(
        'id', task.id, 'companyId', task.company_id, 'key', task.key, 'number', task.number,
        'title', task.title, 'description', task.description, 'status', task.status,
        'priority', task.priority, 'labels', task.labels, 'startDate', task.start_date, 'dueDate', task.due_date,
        'goalId', task.goal_id, 'departmentAgentId', task.department_agent_id,
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

COMMIT;
