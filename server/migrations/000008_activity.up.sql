-- Activity: activity feed, inbox and analytics.

CREATE TABLE activity_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    actor_type text DEFAULT 'system' NOT NULL,
    actor_id text,
    action text NOT NULL,
    entity_type text NOT NULL,
    entity_id text NOT NULL,
    agent_id uuid,
    task_id uuid,
    conversation_id uuid,
    details jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    revision bigint,
    visibility text,
    visibility_user_id uuid,
    schema_version integer DEFAULT 0 NOT NULL,
    project_id uuid,
    on_behalf_of_user_id uuid,
    run_id uuid,
    CONSTRAINT activity_events_pkey PRIMARY KEY (id),
    CONSTRAINT activity_events_actor_type_check CHECK (actor_type IN ('user', 'agent', 'system')),
    CONSTRAINT activity_events_on_behalf_of_check CHECK ((on_behalf_of_user_id IS NULL) OR (actor_type = 'agent')),
    CONSTRAINT activity_events_revision_check CHECK ((revision IS NULL) OR ((entity_type IN ('task', 'conversation')) AND ((revision >= 1) AND (revision <= '9007199254740991'::bigint)))),
    CONSTRAINT activity_events_run_check CHECK ((run_id IS NULL) OR (on_behalf_of_user_id IS NOT NULL)),
    CONSTRAINT activity_events_schema_version_check CHECK (schema_version = ANY (ARRAY[0, 1])),
    CONSTRAINT activity_events_visibility_check CHECK (((visibility IS NULL) AND (visibility_user_id IS NULL)) OR ((visibility IS NOT NULL) AND (((visibility = 'company') AND (visibility_user_id IS NULL)) OR ((visibility = 'user') AND (visibility_user_id IS NOT NULL)))))
);
CREATE INDEX activity_events_actor_history_idx ON activity_events (company_id, actor_type, actor_id, created_at DESC, id DESC);
CREATE INDEX activity_events_agent_created_idx ON activity_events (agent_id, created_at DESC) WHERE (agent_id IS NOT NULL);
CREATE INDEX activity_events_company_created_idx ON activity_events (company_id, created_at DESC, id DESC);
CREATE INDEX activity_events_entity_created_idx ON activity_events (company_id, entity_type, entity_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX activity_events_entity_revision_idx ON activity_events (company_id, entity_type, entity_id, revision DESC) WHERE ((entity_type IN ('task', 'conversation')) AND (revision IS NOT NULL));
CREATE INDEX activity_events_project_created_idx ON activity_events (company_id, project_id, created_at DESC, id DESC) WHERE (project_id IS NOT NULL);
CREATE INDEX activity_events_snapshot_subject_idx ON activity_events (company_id, entity_type, entity_id) WHERE (schema_version = 1);

CREATE TABLE inbox_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid,
    recipient_user_id uuid NOT NULL,
    conversation_id uuid,
    kind text NOT NULL,
    entity_type text NOT NULL,
    entity_id text NOT NULL,
    title text NOT NULL,
    body text NOT NULL,
    deep_link text,
    dedupe_key text NOT NULL,
    read_at timestamptz,
    archived_at timestamptz,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    discussion_grant_generation bigint,
    project_id uuid,
    CONSTRAINT inbox_items_pkey PRIMARY KEY (id),
    CONSTRAINT inbox_items_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT inbox_items_company_id_recipient_user_id_dedupe_key_key UNIQUE NULLS NOT DISTINCT (company_id, recipient_user_id, dedupe_key),
    CONSTRAINT inbox_items_entity_type_check CHECK (entity_type IN ('company', 'user', 'agent', 'goal', 'task', 'run', 'conversation', 'message', 'inbox', 'resource', 'runtime', 'approval', 'question', 'deliverable', 'calendar', 'project')),
    CONSTRAINT inbox_items_kind_check CHECK (kind IN ('task_assigned', 'blocker', 'run_completed', 'run_failed', 'approval_request', 'question_request', 'runtime_offline', 'invitation_renewal', 'calendar_reminder', 'calendar_update', 'discussion_mention', 'task_run_requested', 'task_handoff', 'analytics_budget', 'huddle_missed', 'project_access_granted')),
    CONSTRAINT inbox_items_scope_check CHECK ((company_id IS NOT NULL) OR ((kind = 'calendar_reminder') AND (conversation_id IS NULL))),
    CONSTRAINT inbox_items_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT inbox_items_conversation_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE SET NULL (conversation_id),
    CONSTRAINT inbox_items_recipient_user_id_fkey FOREIGN KEY (recipient_user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX inbox_items_account_created_idx ON inbox_items (recipient_user_id, created_at DESC, id DESC);
CREATE INDEX inbox_items_account_unread_idx ON inbox_items (recipient_user_id) WHERE ((read_at IS NULL) AND (archived_at IS NULL));
CREATE INDEX inbox_items_recipient_conversation_unread_idx ON inbox_items (company_id, recipient_user_id, conversation_id, created_at DESC, id DESC) WHERE ((conversation_id IS NOT NULL) AND (read_at IS NULL) AND (archived_at IS NULL));
CREATE INDEX inbox_items_recipient_created_idx ON inbox_items (company_id, recipient_user_id, created_at DESC, id DESC);
CREATE INDEX inbox_items_recipient_entity_idx ON inbox_items (company_id, recipient_user_id, entity_type, entity_id);
CREATE INDEX inbox_items_recipient_project_idx ON inbox_items (company_id, recipient_user_id, project_id) WHERE (project_id IS NOT NULL);
CREATE INDEX inbox_items_recipient_unread_idx ON inbox_items (company_id, recipient_user_id, created_at DESC, id DESC) WHERE ((read_at IS NULL) AND (archived_at IS NULL));
CREATE INDEX inbox_items_recipient_updated_idx ON inbox_items (company_id, recipient_user_id, updated_at DESC);

CREATE TABLE crash_reports (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    kind text NOT NULL,
    platform text NOT NULL,
    architecture text NOT NULL,
    app_version text NOT NULL,
    reason text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT crash_reports_pkey PRIMARY KEY (id),
    CONSTRAINT crash_reports_kind_check CHECK (kind IN ('crash', 'freeze'))
);
CREATE INDEX crash_reports_retention_idx ON crash_reports (created_at, id);

CREATE TABLE model_prices (
    provider text NOT NULL,
    model text NOT NULL,
    effective_from timestamptz NOT NULL,
    input_usd_per_mtok numeric(20,10) NOT NULL,
    output_usd_per_mtok numeric(20,10) NOT NULL,
    cache_read_usd_per_mtok numeric(20,10) NOT NULL,
    cache_write_usd_per_mtok numeric(20,10) NOT NULL,
    source text NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT model_prices_pkey PRIMARY KEY (provider, model, effective_from),
    CONSTRAINT model_prices_model_check CHECK (model = lower(model)),
    CONSTRAINT model_prices_nonnegative_check CHECK ((input_usd_per_mtok >= (0)::numeric) AND (output_usd_per_mtok >= (0)::numeric) AND (cache_read_usd_per_mtok >= (0)::numeric) AND (cache_write_usd_per_mtok >= (0)::numeric)),
    CONSTRAINT model_prices_provider_check CHECK (provider IN ('claude', 'codex'))
);

-- Seed: verified standard API rates as of this snapshot; never apply today's
-- price retroactively to historical facts. Missing models retain unknown cost.
INSERT INTO model_prices (provider, model, effective_from, input_usd_per_mtok,
    output_usd_per_mtok, cache_read_usd_per_mtok, cache_write_usd_per_mtok, source)
VALUES
    ('codex', 'gpt-5', '2026-09-30T00:00:00Z', 1.25, 10, 0.125, 0,
     'https://developers.openai.com/api/docs/models/gpt-5'),
    ('codex', 'gpt-5.3-codex', '2026-09-30T00:00:00Z', 1.75, 14, 0.175, 0,
     'https://developers.openai.com/api/docs/models/gpt-5.3-codex');

CREATE TABLE analytics_budgets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    owner_kind text NOT NULL,
    agent_id uuid,
    monthly_limit_usd_ticks bigint NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_by_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT analytics_budgets_pkey PRIMARY KEY (id),
    CONSTRAINT analytics_budgets_company_id_owner_kind_agent_id_key UNIQUE NULLS NOT DISTINCT (company_id, owner_kind, agent_id),
    CONSTRAINT analytics_budgets_agent_shape_check CHECK (((owner_kind = 'company') AND (agent_id IS NULL)) OR ((owner_kind = 'agent') AND (agent_id IS NOT NULL))),
    CONSTRAINT analytics_budgets_monthly_limit_usd_ticks_check CHECK (monthly_limit_usd_ticks > 0),
    CONSTRAINT analytics_budgets_owner_kind_check CHECK (owner_kind IN ('company', 'agent')),
    CONSTRAINT analytics_budgets_agent_company_fkey FOREIGN KEY (company_id, agent_id) REFERENCES agents(company_id, id) ON DELETE CASCADE,
    CONSTRAINT analytics_budgets_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);
CREATE INDEX analytics_budgets_company_enabled_idx ON analytics_budgets (company_id, enabled);

CREATE TABLE analytics_budget_alerts (
    budget_id uuid NOT NULL,
    period_start date NOT NULL,
    threshold_percent smallint NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    crossed_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT analytics_budget_alerts_pkey PRIMARY KEY (budget_id, period_start, threshold_percent),
    CONSTRAINT analytics_budget_alerts_threshold_percent_check CHECK (threshold_percent = ANY (ARRAY[80, 100])),
    CONSTRAINT analytics_budget_alerts_budget_id_fkey FOREIGN KEY (budget_id) REFERENCES analytics_budgets(id) ON DELETE CASCADE
);

-- activity_subject_states: full allowlists for the captured owners. This view
-- is read inside the owner's mutation transaction, under its existing locks,
-- before and after the write. It contains no provider errors, input prompts,
-- credentials or leases, and a discussion exposes no metadata. The Task, Task
-- chat and Run snapshots name the subject's Project, so activity.RecordInTx
-- stores activity_events.project_id from the same observation, including for
-- a deletion, whose only observation is the one taken before it.
CREATE VIEW activity_subject_states AS
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
