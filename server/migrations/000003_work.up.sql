-- Work: projects, tasks, approvals, questions, schedules and calendars.

CREATE TABLE approvals (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    type text DEFAULT 'company_plan_apply' NOT NULL,
    status text DEFAULT 'pending' NOT NULL,
    rule_key text NOT NULL,
    title text NOT NULL,
    summary text,
    preview_markdown text NOT NULL,
    presentation jsonb,
    requested_by_agent_id uuid NOT NULL,
    origin_run_id uuid NOT NULL,
    request_key uuid NOT NULL,
    task_id uuid,
    conversation_id uuid NOT NULL,
    origin_user_id uuid,
    canonical_action jsonb NOT NULL,
    request_sha256 bytea NOT NULL,
    action_sha256 bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    decision_note text,
    decided_by_user_id uuid,
    execution_status text,
    execution_result jsonb,
    skipped_reason text,
    continuation_run_id uuid,
    continuation_prompt text,
    continuation_status text,
    continuation_next_attempt_at timestamptz,
    continuation_abandon_reason text,
    continuation_attempts integer DEFAULT 0 NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT approvals_pkey PRIMARY KEY (id),
    CONSTRAINT approvals_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT approvals_origin_run_id_request_key_key UNIQUE (origin_run_id, request_key),
    CONSTRAINT approvals_continuation_presence_check CHECK ((status = 'pending') = (continuation_status IS NULL)),
    CONSTRAINT approvals_continuation_status_check CHECK ((continuation_status IS NULL) OR (continuation_status IN ('pending', 'delivered', 'abandoned', 'not_required'))),
    CONSTRAINT approvals_execution_status_check CHECK ((execution_status IS NULL) OR (execution_status IN ('executed', 'skipped'))),
    CONSTRAINT approvals_origin_scope_check CHECK (((task_id IS NULL) AND (origin_user_id IS NOT NULL)) OR ((task_id IS NOT NULL) AND (origin_user_id IS NULL))),
    CONSTRAINT approvals_presentation_shape_check CHECK ((presentation IS NULL) OR (jsonb_typeof(presentation) = 'object')),
    CONSTRAINT approvals_status_check CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled', 'expired')),
    CONSTRAINT approvals_type_check CHECK (type = 'company_plan_apply'),
    CONSTRAINT approvals_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);
CREATE INDEX approvals_company_created_idx ON approvals (company_id, created_at DESC, id DESC);
CREATE INDEX approvals_company_task_created_idx ON approvals (company_id, task_id, created_at DESC, id DESC) WHERE (task_id IS NOT NULL);
CREATE INDEX approvals_continuation_pending_idx ON approvals (continuation_next_attempt_at, id) WHERE (continuation_status = 'pending');
CREATE UNIQUE INDEX approvals_continuation_run_key ON approvals (continuation_run_id) WHERE (continuation_run_id IS NOT NULL);
CREATE INDEX approvals_expiry_idx ON approvals (expires_at, id) WHERE (status = 'pending');
CREATE INDEX approvals_pending_agent_idx ON approvals (requested_by_agent_id) WHERE (status = 'pending');

CREATE TABLE questions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    status text DEFAULT 'pending' NOT NULL,
    title text NOT NULL,
    asked_by_agent_id uuid NOT NULL,
    origin_run_id uuid NOT NULL,
    request_key uuid NOT NULL,
    task_id uuid,
    conversation_id uuid NOT NULL,
    origin_user_id uuid,
    prompts jsonb NOT NULL,
    request_sha256 bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    answers jsonb,
    resolved_by_user_id uuid,
    continuation_run_id uuid,
    continuation_prompt text,
    continuation_status text,
    continuation_next_attempt_at timestamptz,
    continuation_abandon_reason text,
    continuation_attempts integer DEFAULT 0 NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT questions_pkey PRIMARY KEY (id),
    CONSTRAINT questions_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT questions_origin_run_id_request_key_key UNIQUE (origin_run_id, request_key),
    CONSTRAINT questions_continuation_presence_check CHECK ((status = 'pending') = (continuation_status IS NULL)),
    CONSTRAINT questions_continuation_status_check CHECK ((continuation_status IS NULL) OR (continuation_status IN ('pending', 'delivered', 'abandoned'))),
    CONSTRAINT questions_origin_scope_check CHECK ((task_id IS NULL) = (origin_user_id IS NOT NULL)),
    CONSTRAINT questions_status_check CHECK (status IN ('pending', 'answered', 'withdrawn', 'expired')),
    CONSTRAINT questions_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);
CREATE INDEX questions_company_created_idx ON questions (company_id, created_at DESC, id DESC);
CREATE INDEX questions_company_origin_user_created_idx ON questions (company_id, origin_user_id, created_at DESC, id DESC) WHERE (origin_user_id IS NOT NULL);
CREATE INDEX questions_company_task_created_idx ON questions (company_id, task_id, created_at DESC, id DESC) WHERE (task_id IS NOT NULL);
CREATE INDEX questions_continuation_pending_idx ON questions (continuation_next_attempt_at, id) WHERE (continuation_status = 'pending');
CREATE UNIQUE INDEX questions_continuation_run_key ON questions (continuation_run_id) WHERE (continuation_run_id IS NOT NULL);
CREATE INDEX questions_expiry_idx ON questions (expires_at, id) WHERE (status = 'pending');

CREATE TABLE schedules (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    creator_user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    agent_id uuid NOT NULL,
    reviewed_provider text NOT NULL,
    name text NOT NULL,
    instructions text NOT NULL,
    preset_key text,
    trigger_type text NOT NULL,
    run_at timestamptz,
    cron_expression text,
    timezone text,
    next_occurrence_at timestamptz,
    last_occurrence_at timestamptz,
    status text DEFAULT 'draft' NOT NULL,
    pause_reason text,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT schedules_pkey PRIMARY KEY (id),
    CONSTRAINT schedules_company_creator_request_key UNIQUE (company_id, creator_user_id, request_key),
    CONSTRAINT schedules_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT schedules_conversation_key UNIQUE (conversation_id),
    CONSTRAINT schedules_occurrence_state_check CHECK (((status IN ('draft', 'completed', 'cancelled')) AND (next_occurrence_at IS NULL)) OR ((status IN ('active', 'paused')) AND (next_occurrence_at IS NOT NULL))),
    CONSTRAINT schedules_pause_reason_check CHECK ((status = 'paused') = (pause_reason IS NOT NULL)),
    CONSTRAINT schedules_provider_check CHECK (reviewed_provider IN ('codex', 'claude')),
    CONSTRAINT schedules_status_check CHECK (status IN ('draft', 'active', 'paused', 'completed', 'cancelled')),
    CONSTRAINT schedules_trigger_shape_check CHECK (((trigger_type = 'once') AND (run_at IS NOT NULL) AND (cron_expression IS NULL) AND (timezone IS NULL)) OR ((trigger_type = 'cron') AND (run_at IS NULL) AND (cron_expression IS NOT NULL) AND (timezone IS NOT NULL))),
    CONSTRAINT schedules_trigger_type_check CHECK (trigger_type IN ('once', 'cron')),
    CONSTRAINT schedules_agent_fkey FOREIGN KEY (company_id, agent_id) REFERENCES agents(company_id, id),
    CONSTRAINT schedules_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);
CREATE INDEX schedules_agent_idx ON schedules (company_id, agent_id) WHERE (status IN ('draft', 'active', 'paused'));
CREATE INDEX schedules_creator_list_idx ON schedules (company_id, creator_user_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX schedules_creator_preset_key ON schedules (company_id, creator_user_id, preset_key) WHERE ((preset_key IS NOT NULL) AND (status <> ALL (ARRAY['completed', 'cancelled'])));
CREATE INDEX schedules_due_idx ON schedules (next_occurrence_at, id) WHERE (status = 'active');

CREATE TABLE schedule_sources (
    schedule_id uuid NOT NULL,
    connector_id uuid NOT NULL,
    company_id uuid NOT NULL,
    provider text NOT NULL,
    source text NOT NULL,
    native_id text NOT NULL,
    reviewed_contract_digest text NOT NULL,
    policy_revision bigint NOT NULL,
    "position" integer NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT schedule_sources_pkey PRIMARY KEY (schedule_id, connector_id),
    CONSTRAINT schedule_sources_schedule_position_key UNIQUE (schedule_id, "position"),
    CONSTRAINT schedule_sources_provider_check CHECK (provider IN ('codex', 'claude')),
    CONSTRAINT schedule_sources_provider_source_check CHECK (((provider = 'codex') AND (source = 'codex_app')) OR ((provider = 'claude') AND (source IN ('claude_plugin', 'claude_mcp')))),
    CONSTRAINT schedule_sources_source_check CHECK (source IN ('codex_app', 'claude_plugin', 'claude_mcp')),
    CONSTRAINT schedule_sources_connector_fkey FOREIGN KEY (company_id, connector_id) REFERENCES connector_catalog_items(company_id, id),
    CONSTRAINT schedule_sources_schedule_fkey FOREIGN KEY (company_id, schedule_id) REFERENCES schedules(company_id, id) ON DELETE CASCADE
);
CREATE INDEX schedule_sources_company_connector_idx ON schedule_sources (company_id, connector_id);

CREATE TABLE schedule_firings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    schedule_id uuid NOT NULL,
    schedule_version bigint NOT NULL,
    conversation_id uuid NOT NULL,
    message_id uuid,
    run_id uuid,
    occurrence_at timestamptz NOT NULL,
    trigger text NOT NULL,
    request_key uuid,
    disposition text NOT NULL,
    reason text,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    missed_count integer DEFAULT 0 NOT NULL,
    CONSTRAINT schedule_firings_pkey PRIMARY KEY (id),
    CONSTRAINT schedule_firings_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT schedule_firings_message_key UNIQUE (message_id),
    CONSTRAINT schedule_firings_run_key UNIQUE (run_id),
    CONSTRAINT schedule_firings_cancelled_run_check CHECK ((disposition <> 'cancelled') OR (run_id IS NOT NULL)),
    CONSTRAINT schedule_firings_disposition_check CHECK (disposition IN ('materialized', 'superseded', 'skipped', 'needs_attention', 'cancelled')),
    CONSTRAINT schedule_firings_materialization_check CHECK (((message_id IS NULL) = (run_id IS NULL)) AND ((disposition <> 'materialized') OR (run_id IS NOT NULL)) AND ((disposition <> 'superseded') OR (run_id IS NOT NULL))),
    CONSTRAINT schedule_firings_missed_count_check CHECK ((missed_count >= 0) AND (missed_count <= 1000)),
    CONSTRAINT schedule_firings_reason_check CHECK ((disposition = 'materialized') = (reason IS NULL)),
    CONSTRAINT schedule_firings_request_key_check CHECK (((trigger = 'scheduled') AND (request_key IS NULL)) OR ((trigger = 'manual') AND (request_key IS NOT NULL))),
    CONSTRAINT schedule_firings_trigger_check CHECK (trigger IN ('scheduled', 'manual')),
    CONSTRAINT schedule_firings_schedule_fkey FOREIGN KEY (company_id, schedule_id) REFERENCES schedules(company_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX schedule_firings_manual_request_key ON schedule_firings (schedule_id, request_key) WHERE (request_key IS NOT NULL);
CREATE INDEX schedule_firings_schedule_history_idx ON schedule_firings (schedule_id, occurrence_at DESC, id DESC);
CREATE UNIQUE INDEX schedule_firings_scheduled_occurrence_key ON schedule_firings (schedule_id, occurrence_at) WHERE (trigger = 'scheduled');

CREATE TABLE task_report_batches (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    recipient_agent_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    confirmed_by_user_id uuid,
    delivery_type text DEFAULT 'manual' NOT NULL,
    automation_user_id uuid,
    request_key uuid NOT NULL,
    message_id uuid NOT NULL,
    run_id uuid NOT NULL,
    event_count integer NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT task_report_batches_pkey PRIMARY KEY (id),
    CONSTRAINT task_report_batches_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT task_report_batches_message_id_key UNIQUE (message_id),
    CONSTRAINT task_report_batches_run_id_key UNIQUE (run_id),
    CONSTRAINT task_report_batches_delivery_authority_check CHECK (((delivery_type = 'manual') AND (confirmed_by_user_id IS NOT NULL) AND (automation_user_id IS NULL)) OR ((delivery_type = 'proactive') AND (confirmed_by_user_id IS NULL) AND (automation_user_id IS NOT NULL))),
    CONSTRAINT task_report_batches_delivery_type_check CHECK (delivery_type IN ('manual', 'proactive')),
    CONSTRAINT task_report_batches_recipient_agent_fkey FOREIGN KEY (company_id, recipient_agent_id) REFERENCES agents(company_id, id)
);
CREATE INDEX task_report_batches_company_created_idx ON task_report_batches (company_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX task_report_batches_manual_request_key ON task_report_batches (company_id, confirmed_by_user_id, request_key) WHERE (delivery_type = 'manual');
CREATE UNIQUE INDEX task_report_batches_proactive_request_key ON task_report_batches (company_id, automation_user_id, request_key) WHERE (delivery_type = 'proactive');

CREATE TABLE projects (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    key text NOT NULL,
    title text NOT NULL,
    description text DEFAULT '' NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    visibility text DEFAULT 'restricted' NOT NULL,
    access_version bigint DEFAULT 1 NOT NULL,
    access_changed_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT projects_pkey PRIMARY KEY (id),
    CONSTRAINT projects_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT projects_company_key_key UNIQUE (company_id, key),
    CONSTRAINT projects_visibility_check CHECK (visibility IN ('company', 'restricted')),
    CONSTRAINT projects_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);

CREATE TABLE calendars (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    type text NOT NULL,
    user_id uuid,
    company_id uuid,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT calendars_pkey PRIMARY KEY (id),
    CONSTRAINT calendars_company_id_key UNIQUE (company_id),
    CONSTRAINT calendars_user_id_key UNIQUE (user_id),
    CONSTRAINT calendars_check CHECK (((type = 'personal') AND (user_id IS NOT NULL) AND (company_id IS NULL)) OR ((type = 'company') AND (company_id IS NOT NULL) AND (user_id IS NULL))),
    CONSTRAINT calendars_type_check CHECK (type IN ('personal', 'company')),
    CONSTRAINT calendars_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT calendars_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE calendar_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    calendar_id uuid NOT NULL,
    sharing_company_id uuid,
    type text NOT NULL,
    title text NOT NULL,
    description text DEFAULT '' NOT NULL,
    location text DEFAULT '' NOT NULL,
    color text NOT NULL,
    all_day boolean DEFAULT false NOT NULL,
    start_local timestamp NOT NULL,
    end_local timestamp NOT NULL,
    time_zone text NOT NULL,
    recurrence text DEFAULT 'none' NOT NULL,
    recurrence_until date,
    reminder_minutes integer,
    reminder_next_at timestamptz,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    recurrence_interval integer DEFAULT 1 NOT NULL,
    recurrence_weekdays integer[] DEFAULT '{}'::integer[] NOT NULL,
    recurrence_monthly_mode text DEFAULT 'day_of_month' NOT NULL,
    recurrence_count integer,
    CONSTRAINT calendar_events_pkey PRIMARY KEY (id),
    CONSTRAINT calendar_events_user_id_id_key UNIQUE (user_id, id),
    CONSTRAINT calendar_events_color_check CHECK (color IN ('sage', 'sky', 'lavender', 'rose', 'sand')),
    CONSTRAINT calendar_events_recurrence_check CHECK (recurrence IN ('none', 'daily', 'weekly', 'monthly', 'yearly')),
    CONSTRAINT calendar_events_recurrence_count_check CHECK ((recurrence_count >= 1) AND (recurrence_count <= 1000)),
    CONSTRAINT calendar_events_recurrence_end_check CHECK ((recurrence_count IS NULL) OR (recurrence_until IS NULL)),
    CONSTRAINT calendar_events_recurrence_interval_check CHECK ((recurrence_interval >= 1) AND (recurrence_interval <= 999)),
    CONSTRAINT calendar_events_recurrence_monthly_mode_check CHECK (recurrence_monthly_mode IN ('day_of_month', 'day_of_week')),
    CONSTRAINT calendar_events_recurrence_options_check CHECK (((recurrence = 'weekly') OR (cardinality(recurrence_weekdays) = 0)) AND ((recurrence = 'monthly') OR (recurrence_monthly_mode = 'day_of_month')) AND ((recurrence <> 'none') OR ((recurrence_interval = 1) AND (recurrence_count IS NULL)))),
    CONSTRAINT calendar_events_recurrence_weekdays_check CHECK ((cardinality(recurrence_weekdays) <= 7) AND (recurrence_weekdays <@ ARRAY[0, 1, 2, 3, 4, 5, 6]) AND (array_position(recurrence_weekdays, NULL::integer) IS NULL)),
    CONSTRAINT calendar_events_type_check CHECK (type IN ('event', 'reminder')),
    CONSTRAINT calendar_events_calendar_id_fkey FOREIGN KEY (calendar_id) REFERENCES calendars(id) ON DELETE CASCADE,
    CONSTRAINT calendar_events_sharing_company_id_fkey FOREIGN KEY (sharing_company_id) REFERENCES companies(id) ON DELETE SET NULL
);
CREATE INDEX calendar_events_calendar_start_idx ON calendar_events (calendar_id, start_local);
CREATE INDEX calendar_events_reminder_due_idx ON calendar_events (reminder_next_at, id) WHERE (reminder_next_at IS NOT NULL);
CREATE INDEX calendar_events_user_start_idx ON calendar_events (user_id, start_local);

CREATE TABLE calendar_occurrence_states (
    event_id uuid NOT NULL,
    occurrence_date date NOT NULL,
    cancelled boolean DEFAULT false NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT calendar_occurrence_states_pkey PRIMARY KEY (event_id, occurrence_date),
    CONSTRAINT calendar_occurrence_states_event_id_fkey FOREIGN KEY (event_id) REFERENCES calendar_events(id) ON DELETE CASCADE
);

CREATE TABLE calendar_attendees (
    event_id uuid NOT NULL,
    company_id uuid NOT NULL,
    user_id uuid NOT NULL,
    response_status text DEFAULT 'needs_action' NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT calendar_attendees_pkey PRIMARY KEY (event_id, user_id),
    CONSTRAINT calendar_attendees_response_status_check CHECK (response_status IN ('needs_action', 'accepted', 'tentative', 'declined')),
    CONSTRAINT calendar_attendees_company_id_user_id_fkey FOREIGN KEY (company_id, user_id) REFERENCES company_members(company_id, user_id) ON DELETE CASCADE,
    CONSTRAINT calendar_attendees_event_id_fkey FOREIGN KEY (event_id) REFERENCES calendar_events(id) ON DELETE CASCADE
);
CREATE INDEX calendar_attendees_user_idx ON calendar_attendees (user_id, event_id);

CREATE TABLE calendar_reminder_states (
    event_id uuid NOT NULL,
    user_id uuid NOT NULL,
    occurrence_date date NOT NULL,
    completed boolean DEFAULT false NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT calendar_reminder_states_pkey PRIMARY KEY (event_id, user_id, occurrence_date),
    CONSTRAINT calendar_reminder_states_event_id_fkey FOREIGN KEY (event_id) REFERENCES calendar_events(id) ON DELETE CASCADE,
    CONSTRAINT calendar_reminder_states_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE calendar_tool_receipts (
    run_id uuid NOT NULL,
    request_key uuid NOT NULL,
    payload_hash bytea NOT NULL,
    response jsonb NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT calendar_tool_receipts_pkey PRIMARY KEY (run_id, request_key)
);

CREATE TABLE project_files (
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    file_type text NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    removed_at timestamptz,
    CONSTRAINT project_files_pkey PRIMARY KEY (project_id, attachment_id),
    CONSTRAINT project_files_file_type_check CHECK (file_type IN ('logo', 'document')),
    CONSTRAINT project_files_company_id_project_id_fkey FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE
);
CREATE INDEX project_files_attachment ON project_files (attachment_id);
CREATE UNIQUE INDEX project_files_one_logo ON project_files (project_id) WHERE ((file_type = 'logo') AND (removed_at IS NULL));

CREATE TABLE project_grants (
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    user_id uuid NOT NULL,
    permission text NOT NULL,
    read_permission text GENERATED ALWAYS AS ('project.read') STORED NOT NULL,
    granted_by_user_id uuid,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT project_grants_pkey PRIMARY KEY (company_id, project_id, user_id, permission),
    CONSTRAINT project_grants_permission_check CHECK (permission IN ('project.read', 'project.tasks.write', 'project.tasks.execute', 'project.files.manage', 'project.approvals.resolve', 'project.visibility.update', 'project.settings.update', 'project.grants.manage', 'project.archive')),
    CONSTRAINT project_grants_company_id_project_id_fkey FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE,
    CONSTRAINT project_grants_company_id_user_id_fkey FOREIGN KEY (company_id, user_id) REFERENCES company_members(company_id, user_id) ON DELETE CASCADE,
    CONSTRAINT project_grants_read_fkey FOREIGN KEY (company_id, project_id, user_id, read_permission) REFERENCES project_grants(company_id, project_id, user_id, permission) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX project_grants_user_idx ON project_grants (company_id, user_id, project_id);

CREATE TABLE project_canvases (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    title text DEFAULT 'Project canvas' NOT NULL,
    head_seq bigint DEFAULT 0 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    overview_page_id uuid,
    CONSTRAINT project_canvases_pkey PRIMARY KEY (id),
    CONSTRAINT project_canvases_company_id_project_id_id_key UNIQUE (company_id, project_id, id),
    CONSTRAINT project_canvases_project_id_key UNIQUE (project_id),
    CONSTRAINT project_canvases_company_id_project_id_fkey FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE
);

CREATE TABLE project_file_assets (
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT project_file_assets_pkey PRIMARY KEY (asset_id),
    CONSTRAINT project_file_assets_company_id_project_id_fkey FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE
);

CREATE TABLE project_canvas_operations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    seq bigint NOT NULL,
    actor_user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    request_hash bytea NOT NULL,
    changes jsonb NOT NULL,
    result jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT project_canvas_operations_pkey PRIMARY KEY (id),
    CONSTRAINT project_canvas_operations_project_id_actor_user_id_request__key UNIQUE (project_id, actor_user_id, request_key),
    CONSTRAINT project_canvas_operations_project_id_seq_key UNIQUE (project_id, seq),
    CONSTRAINT project_canvas_operations_request_hash_check CHECK (octet_length(request_hash) = 32),
    CONSTRAINT project_canvas_operations_company_id_project_id_fkey FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE
);

CREATE TABLE project_page_changes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    page_id uuid NOT NULL,
    run_id uuid,
    actor_user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    request_hash text NOT NULL,
    operations jsonb NOT NULL,
    before_content jsonb NOT NULL,
    before_theme text,
    after_theme text,
    result jsonb NOT NULL,
    summary text NOT NULL,
    status text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    decisions jsonb DEFAULT '{}'::jsonb NOT NULL,
    supersedes_id uuid,
    actor_agent_id uuid,
    CONSTRAINT project_page_changes_pkey PRIMARY KEY (id),
    CONSTRAINT project_page_changes_company_key UNIQUE (company_id, id),
    CONSTRAINT project_page_changes_project_id_actor_user_id_request_key_key UNIQUE (project_id, actor_user_id, request_key),
    CONSTRAINT project_page_changes_after_theme_check CHECK (after_theme IN ('default', 'editorial', 'compact')),
    CONSTRAINT project_page_changes_before_theme_check CHECK (before_theme IN ('default', 'editorial', 'compact')),
    CONSTRAINT project_page_changes_status_check CHECK (status IN ('applied', 'proposed', 'rejected', 'undone', 'partially_applied', 'needs_update', 'superseded', 'resolved')),
    CONSTRAINT project_page_changes_company_id_project_id_fkey FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE,
    CONSTRAINT project_page_changes_supersedes_fkey FOREIGN KEY (company_id, supersedes_id) REFERENCES project_page_changes(company_id, id)
);
CREATE UNIQUE INDEX project_page_change_successor ON project_page_changes (supersedes_id) WHERE (supersedes_id IS NOT NULL);
CREATE INDEX project_page_pending_changes ON project_page_changes (project_id, page_id, created_at DESC) WHERE (status = 'proposed');

CREATE TABLE project_page_managed_blocks (
    company_id uuid NOT NULL,
    page_id uuid NOT NULL,
    block_id uuid NOT NULL,
    content_hash text NOT NULL,
    run_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT project_page_managed_blocks_pkey PRIMARY KEY (block_id)
);

CREATE TABLE project_group_grants (
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    group_id uuid NOT NULL,
    permission text NOT NULL,
    read_permission text GENERATED ALWAYS AS ('project.read') STORED NOT NULL,
    granted_by_user_id uuid,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT project_group_grants_pkey PRIMARY KEY (company_id, project_id, group_id, permission),
    CONSTRAINT project_group_grants_permission_check CHECK (permission IN ('project.read', 'project.tasks.write', 'project.tasks.execute', 'project.files.manage', 'project.approvals.resolve', 'project.visibility.update', 'project.settings.update', 'project.grants.manage', 'project.archive')),
    CONSTRAINT project_group_grants_company_id_group_id_fkey FOREIGN KEY (company_id, group_id) REFERENCES company_groups(company_id, id) ON DELETE CASCADE,
    CONSTRAINT project_group_grants_company_id_project_id_fkey FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE,
    CONSTRAINT project_group_grants_read_fkey FOREIGN KEY (company_id, project_id, group_id, read_permission) REFERENCES project_group_grants(company_id, project_id, group_id, permission) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX project_group_grants_group_idx ON project_group_grants (company_id, group_id, project_id);

CREATE TABLE tasks (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    number bigint NOT NULL,
    title text NOT NULL,
    description text DEFAULT '' NOT NULL,
    status text DEFAULT 'backlog' NOT NULL,
    priority text DEFAULT 'medium' NOT NULL,
    labels text[] DEFAULT '{}'::text[] NOT NULL,
    start_date date,
    due_date date,
    assignee_user_id uuid NOT NULL,
    executed_by_agent_id uuid,
    creator_user_id uuid,
    creator_agent_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    completed_at timestamptz,
    version bigint DEFAULT 1 NOT NULL,
    milestone_type text NOT NULL,
    execution_generation bigint DEFAULT 0 NOT NULL,
    key text NOT NULL,
    project_id uuid NOT NULL,
    workspace_generation integer DEFAULT 0 NOT NULL,
    result_proposal_only boolean DEFAULT false NOT NULL,
    result_review_required boolean DEFAULT true NOT NULL,
    assignment_status text DEFAULT 'accepted' NOT NULL,
    scheduled_at timestamptz,
    schedule_fired_at timestamptz,
    task_schedule_id uuid,
    task_schedule_generation bigint DEFAULT 1 NOT NULL,
    schedule_occurrence_at timestamptz,
    schedule_disposition text DEFAULT '' NOT NULL,
    CONSTRAINT tasks_pkey PRIMARY KEY (id),
    CONSTRAINT tasks_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT tasks_company_id_number_key UNIQUE (company_id, number),
    CONSTRAINT tasks_company_key_key UNIQUE (company_id, key),
    CONSTRAINT tasks_company_project_id_key UNIQUE (company_id, project_id, id),
    CONSTRAINT tasks_conversation_id_key UNIQUE (conversation_id),
    CONSTRAINT tasks_assignment_status_check CHECK (assignment_status IN ('accepted', 'pending', 'declined')),
    CONSTRAINT tasks_creator_present_check CHECK (num_nonnulls(creator_user_id, creator_agent_id) >= 1),
    CONSTRAINT tasks_milestone_type_check CHECK (milestone_type IN ('positioning', 'brand', 'presence', 'finance', 'product', 'launch', 'growth', 'sales_outbound', 'operations', 'support')),
    CONSTRAINT tasks_priority_check CHECK (priority IN ('low', 'medium', 'high', 'urgent')),
    CONSTRAINT tasks_schedule_disposition_check CHECK (schedule_disposition IN ('', 'scheduled', 'started', 'skipped', 'missed', 'cancelled')),
    CONSTRAINT tasks_status_check CHECK (status IN ('backlog', 'todo', 'in_progress', 'in_review', 'done', 'blocked', 'failed', 'error', 'cancelled')),
    CONSTRAINT tasks_task_schedule_generation_check CHECK (task_schedule_generation > 0),
    CONSTRAINT tasks_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT tasks_executed_by_agent_fkey FOREIGN KEY (company_id, executed_by_agent_id) REFERENCES agents(company_id, id),
    CONSTRAINT tasks_project_fkey FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE
);
CREATE INDEX tasks_assignee_user_idx ON tasks (company_id, assignee_user_id, number DESC);
CREATE INDEX tasks_calendar_due_idx ON tasks (scheduled_at, id) WHERE ((schedule_disposition = 'scheduled') AND (assignment_status = 'accepted'));
CREATE UNIQUE INDEX tasks_calendar_occurrence_idx ON tasks (task_schedule_id, task_schedule_generation, schedule_occurrence_at) WHERE (task_schedule_id IS NOT NULL);
CREATE INDEX tasks_calendar_series_due_idx ON tasks (task_schedule_id, task_schedule_generation, scheduled_at DESC) WHERE (schedule_disposition IN ('scheduled', 'started'));
CREATE INDEX tasks_calendar_window_idx ON tasks (company_id, scheduled_at, id) WHERE (scheduled_at IS NOT NULL);
CREATE INDEX tasks_company_executed_by_status_idx ON tasks (company_id, executed_by_agent_id, status) WHERE (executed_by_agent_id IS NOT NULL);
CREATE INDEX tasks_company_project_status_idx ON tasks (company_id, project_id, status, number DESC);
CREATE INDEX tasks_company_status_idx ON tasks (company_id, status, number DESC);
CREATE INDEX tasks_executed_by_agent_idx ON tasks (company_id, executed_by_agent_id, number DESC) WHERE (executed_by_agent_id IS NOT NULL);
CREATE INDEX tasks_milestone_idx ON tasks (company_id, milestone_type, number DESC);

CREATE TABLE task_dependencies (
    company_id uuid NOT NULL,
    dependent_task_id uuid NOT NULL,
    prerequisite_task_id uuid NOT NULL,
    created_by_user_id uuid,
    created_by_agent_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    project_id uuid NOT NULL,
    CONSTRAINT task_dependencies_pkey PRIMARY KEY (company_id, dependent_task_id, prerequisite_task_id),
    CONSTRAINT task_dependencies_distinct_tasks_check CHECK (dependent_task_id <> prerequisite_task_id),
    CONSTRAINT task_dependencies_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT task_dependencies_dependent_task_fkey FOREIGN KEY (company_id, project_id, dependent_task_id) REFERENCES tasks(company_id, project_id, id) ON DELETE CASCADE,
    CONSTRAINT task_dependencies_prerequisite_task_fkey FOREIGN KEY (company_id, project_id, prerequisite_task_id) REFERENCES tasks(company_id, project_id, id) ON DELETE CASCADE
);
CREATE INDEX task_dependencies_prerequisite_idx ON task_dependencies (company_id, prerequisite_task_id, dependent_task_id);

CREATE TABLE task_merge_jobs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    task_id uuid NOT NULL,
    run_id uuid NOT NULL,
    task_execution_generation bigint NOT NULL,
    authority_user_id uuid NOT NULL,
    request_type text DEFAULT 'proactive' NOT NULL,
    status text DEFAULT 'pending' NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    lease_expires_at timestamptz,
    result_code text,
    merged_commit text,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    completed_at timestamptz,
    CONSTRAINT task_merge_jobs_pkey PRIMARY KEY (id),
    CONSTRAINT task_merge_jobs_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT task_merge_jobs_run_key UNIQUE (run_id),
    CONSTRAINT task_merge_jobs_task_generation_key UNIQUE (task_id, task_execution_generation),
    CONSTRAINT task_merge_jobs_request_type_check CHECK (request_type IN ('proactive', 'manual')),
    CONSTRAINT task_merge_jobs_status_check CHECK (status IN ('pending', 'executing', 'succeeded', 'blocked')),
    CONSTRAINT task_merge_jobs_task_fkey FOREIGN KEY (company_id, task_id) REFERENCES tasks(company_id, id) ON DELETE CASCADE
);
CREATE INDEX task_merge_jobs_company_task_created_idx ON task_merge_jobs (company_id, task_id, created_at DESC, id DESC);
CREATE INDEX task_merge_jobs_due_idx ON task_merge_jobs (next_attempt_at, created_at, id) WHERE (status IN ('pending', 'executing'));

CREATE TABLE task_report_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    task_id uuid NOT NULL,
    task_version bigint NOT NULL,
    task_key text NOT NULL,
    task_title text NOT NULL,
    source_agent_id uuid NOT NULL,
    recipient_agent_id uuid NOT NULL,
    completion_source text NOT NULL,
    completed_at timestamptz NOT NULL,
    result_summary text DEFAULT '' NOT NULL,
    source_run_id uuid,
    request_key uuid,
    batch_id uuid,
    superseded_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    project_id uuid,
    CONSTRAINT task_report_events_pkey PRIMARY KEY (id),
    CONSTRAINT task_report_events_task_version_key UNIQUE (task_id, task_version),
    CONSTRAINT task_report_events_completion_source_check CHECK (completion_source IN ('manual', 'review_accepted', 'agent_business_failure')),
    CONSTRAINT task_report_events_failure_origin_check CHECK (((completion_source = 'agent_business_failure') AND (source_run_id IS NOT NULL) AND (request_key IS NOT NULL)) OR ((completion_source <> 'agent_business_failure') AND (source_run_id IS NULL) AND (request_key IS NULL))),
    CONSTRAINT task_report_events_batch_fkey FOREIGN KEY (company_id, batch_id) REFERENCES task_report_batches(company_id, id) ON DELETE SET NULL (batch_id),
    CONSTRAINT task_report_events_task_fkey FOREIGN KEY (company_id, task_id) REFERENCES tasks(company_id, id) ON DELETE CASCADE
);
CREATE INDEX task_report_events_batch_idx ON task_report_events (batch_id, created_at, id) WHERE (batch_id IS NOT NULL);
CREATE UNIQUE INDEX task_report_events_failure_request_key ON task_report_events (source_run_id, request_key) WHERE (source_run_id IS NOT NULL);
CREATE INDEX task_report_events_pending_recipient_idx ON task_report_events (company_id, recipient_agent_id, created_at, id) WHERE ((batch_id IS NULL) AND (superseded_at IS NULL));

CREATE TABLE task_result_sets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    task_id uuid NOT NULL,
    task_revision bigint NOT NULL,
    execution_generation bigint NOT NULL,
    actor_user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    request_hash text NOT NULL,
    approved_at timestamptz,
    approved_by_user_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    source_run_id uuid,
    CONSTRAINT task_result_sets_pkey PRIMARY KEY (id),
    CONSTRAINT task_result_sets_company_id_actor_user_id_request_key_key UNIQUE (company_id, actor_user_id, request_key),
    CONSTRAINT task_result_sets_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT task_result_sets_task_id_task_revision_key UNIQUE (task_id, task_revision),
    CONSTRAINT task_result_sets_company_id_task_id_fkey FOREIGN KEY (company_id, task_id) REFERENCES tasks(company_id, id) ON DELETE CASCADE
);

CREATE TABLE task_result_items (
    company_id uuid NOT NULL,
    result_set_id uuid NOT NULL,
    ordinal integer NOT NULL,
    output_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT task_result_items_pkey PRIMARY KEY (result_set_id, ordinal),
    CONSTRAINT task_result_items_result_set_id_output_id_key UNIQUE (result_set_id, output_id),
    CONSTRAINT task_result_items_company_id_result_set_id_fkey FOREIGN KEY (company_id, result_set_id) REFERENCES task_result_sets(company_id, id) ON DELETE CASCADE
);

CREATE TABLE task_schedules (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    template_task_id uuid NOT NULL,
    trigger jsonb NOT NULL,
    template_title text NOT NULL,
    template_description text NOT NULL,
    next_occurrence_at timestamptz,
    paused boolean DEFAULT false NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    generation bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT task_schedules_pkey PRIMARY KEY (id),
    CONSTRAINT task_schedules_company_id_project_id_id_key UNIQUE (company_id, project_id, id),
    CONSTRAINT task_schedules_template_task_id_key UNIQUE (template_task_id),
    CONSTRAINT task_schedules_generation_check CHECK (generation > 0),
    CONSTRAINT task_schedules_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT task_schedules_company_id_project_id_fkey FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE,
    CONSTRAINT task_schedules_company_id_project_id_template_task_id_fkey FOREIGN KEY (company_id, project_id, template_task_id) REFERENCES tasks(company_id, project_id, id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT task_schedules_project_id_fkey FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);
CREATE INDEX task_schedules_due_idx ON task_schedules (next_occurrence_at, id) WHERE ((NOT paused) AND (next_occurrence_at IS NOT NULL));
CREATE INDEX task_schedules_project_idx ON task_schedules (company_id, project_id);

CREATE TABLE project_artifacts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    title text NOT NULL,
    accepted_version_id uuid,
    next_number bigint DEFAULT 1 NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_by_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    task_id uuid,
    source_run_id uuid,
    pending_creation boolean DEFAULT false NOT NULL,
    creation_change_id uuid,
    CONSTRAINT project_artifacts_pkey PRIMARY KEY (id),
    CONSTRAINT project_artifacts_company_id_project_id_id_key UNIQUE (company_id, project_id, id),
    CONSTRAINT project_artifacts_company_id_project_id_fkey FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE,
    CONSTRAINT project_artifacts_creation_change_fkey FOREIGN KEY (company_id, creation_change_id) REFERENCES project_page_changes(company_id, id),
    CONSTRAINT project_artifacts_task_fkey FOREIGN KEY (company_id, project_id, task_id) REFERENCES tasks(company_id, project_id, id) ON DELETE SET NULL (task_id)
);
CREATE INDEX project_artifacts_list ON project_artifacts (project_id, updated_at DESC, id) WHERE (archived_at IS NULL);
CREATE INDEX project_artifacts_task ON project_artifacts (task_id) WHERE (task_id IS NOT NULL);

CREATE TABLE project_artifact_versions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    artifact_id uuid NOT NULL,
    number bigint NOT NULL,
    source_type text NOT NULL,
    wiki_page_id uuid,
    wiki_revision_id uuid,
    asset_version_id uuid,
    output_id uuid,
    base_version_id uuid,
    created_by_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    branch_title text DEFAULT 'Main' NOT NULL,
    CONSTRAINT project_artifact_versions_pkey PRIMARY KEY (id),
    CONSTRAINT project_artifact_versions_artifact_id_number_key UNIQUE (artifact_id, number),
    CONSTRAINT project_artifact_versions_company_id_project_id_artifact_id_key UNIQUE (company_id, project_id, artifact_id, id),
    CONSTRAINT project_artifact_versions_company_id_project_id_id_key UNIQUE (company_id, project_id, id),
    CONSTRAINT project_artifact_versions_check CHECK (((source_type = 'wiki') AND (asset_version_id IS NULL) AND (output_id IS NULL) AND ((wiki_page_id IS NULL) = (wiki_revision_id IS NULL))) OR ((source_type = 'file') AND (wiki_page_id IS NULL) AND (wiki_revision_id IS NULL) AND (output_id IS NULL)) OR ((source_type = 'output') AND (wiki_page_id IS NULL) AND (wiki_revision_id IS NULL) AND (asset_version_id IS NULL))),
    CONSTRAINT project_artifact_versions_source_type_check CHECK (source_type IN ('wiki', 'file', 'output')),
    CONSTRAINT project_artifact_versions_company_id_project_id_artifact__fkey1 FOREIGN KEY (company_id, project_id, artifact_id, base_version_id) REFERENCES project_artifact_versions(company_id, project_id, artifact_id, id) ON DELETE SET NULL (base_version_id),
    CONSTRAINT project_artifact_versions_company_id_project_id_artifact_i_fkey FOREIGN KEY (company_id, project_id, artifact_id) REFERENCES project_artifacts(company_id, project_id, id) ON DELETE CASCADE
);
CREATE INDEX project_artifact_versions_output ON project_artifact_versions (company_id, output_id, project_id, created_at, id) WHERE (output_id IS NOT NULL);
CREATE INDEX project_artifact_versions_project_time ON project_artifact_versions (project_id, created_at DESC, id DESC);

CREATE TABLE project_presentations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    page_id uuid NOT NULL,
    page_version_id uuid NOT NULL,
    theme text NOT NULL,
    title text NOT NULL,
    content jsonb NOT NULL,
    documents jsonb DEFAULT '{}'::jsonb NOT NULL,
    export_markdown text NOT NULL,
    source_sequence bigint NOT NULL,
    fingerprint text NOT NULL,
    request_key uuid NOT NULL,
    created_by_user_id uuid NOT NULL,
    source_run_id uuid,
    output_id uuid,
    export_attachment_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    shared_content jsonb,
    change_id uuid,
    change_snapshot jsonb,
    CONSTRAINT project_presentations_pkey PRIMARY KEY (id),
    CONSTRAINT project_presentations_company_id_project_id_id_key UNIQUE (company_id, project_id, id),
    CONSTRAINT project_presentations_output_id_key UNIQUE (output_id),
    CONSTRAINT project_presentations_project_id_created_by_user_id_request_key UNIQUE (project_id, created_by_user_id, request_key),
    CONSTRAINT project_presentations_theme_check CHECK (theme IN ('default', 'editorial', 'compact')),
    CONSTRAINT project_presentations_change_fkey FOREIGN KEY (company_id, change_id) REFERENCES project_page_changes(company_id, id),
    CONSTRAINT project_presentations_company_id_project_id_fkey FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE,
    CONSTRAINT project_presentations_company_id_project_id_page_version_i_fkey FOREIGN KEY (company_id, project_id, page_version_id) REFERENCES project_artifact_versions(company_id, project_id, id)
);
CREATE INDEX project_presentations_history ON project_presentations (project_id, created_at DESC, id DESC);

CREATE TABLE project_presentation_sources (
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    presentation_id uuid NOT NULL,
    version_id uuid NOT NULL,
    wiki_page_id uuid,
    wiki_revision_id uuid,
    asset_version_id uuid,
    output_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT project_presentation_sources_pkey PRIMARY KEY (presentation_id, version_id),
    CONSTRAINT project_presentation_sources_company_id_project_id_present_fkey FOREIGN KEY (company_id, project_id, presentation_id) REFERENCES project_presentations(company_id, project_id, id) ON DELETE CASCADE,
    CONSTRAINT project_presentation_sources_company_id_project_id_version_fkey FOREIGN KEY (company_id, project_id, version_id) REFERENCES project_artifact_versions(company_id, project_id, id)
);

-- Links to these tables from tables created earlier.
ALTER TABLE tasks ADD CONSTRAINT tasks_calendar_schedule_fk FOREIGN KEY (company_id, project_id, task_schedule_id) REFERENCES task_schedules(company_id, project_id, id) ON DELETE SET NULL (task_schedule_id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE project_artifacts ADD CONSTRAINT project_artifacts_accepted_fkey FOREIGN KEY (company_id, project_id, id, accepted_version_id) REFERENCES project_artifact_versions(company_id, project_id, artifact_id, id) ON DELETE SET NULL (accepted_version_id) DEFERRABLE INITIALLY DEFERRED;

-- project_permission_held: the one per-permission predicate, for writes and
-- the Run claim fence. A person holds a permission on a Project through their
-- own grant, through a Group holding it there, through the Company default set
-- on a Company-visible Project, or through the management bridge. A grant or
-- Group membership counts only when written since their latest (re)join. Go
-- binds the Company default set, the three management permissions and the
-- roles holding projects.manage, so no permission or role name lives here.
-- Arguments are positional because a SQL function resolves a bare name to a
-- column first.
--
-- Postgres does not inline this function (its body has an EXISTS), so the
-- planner sees one opaque call per row. Use it where the candidate set is
-- already small: one write, or the Run claim fence. List queries join the
-- views below instead.
CREATE FUNCTION project_permission_held(
    company_id uuid,
    project_id uuid,
    user_id uuid,
    permission text,
    company_default_permissions text[],
    management_permissions text[],
    management_roles text[]
) RETURNS boolean
LANGUAGE sql STABLE
AS $$
SELECT EXISTS (
    SELECT 1
    FROM company_members AS member
    JOIN users AS account
      ON account.id = member.user_id
     AND account.disabled_at IS NULL
     AND account.deleted_at IS NULL
    JOIN companies AS company
      ON company.id = member.company_id
     AND company.deleting_at IS NULL
    JOIN projects AS project
      ON project.company_id = member.company_id
     AND project.id = $2
    WHERE member.company_id = $1
      AND member.user_id = $3
      AND member.status = 'active'
      AND (
          EXISTS (
              SELECT 1
              FROM project_grants AS grant_row
              WHERE grant_row.company_id = $1
                AND grant_row.project_id = $2
                AND grant_row.user_id = $3
                AND grant_row.permission = $4
                AND grant_row.updated_at >= COALESCE(member.reactivated_at, member.created_at)
          )
          OR EXISTS (
              SELECT 1
              FROM company_group_members AS group_member
              JOIN project_group_grants AS group_grant
                ON group_grant.company_id = group_member.company_id
               AND group_grant.group_id = group_member.group_id
              WHERE group_member.company_id = $1
                AND group_member.user_id = $3
                AND group_grant.project_id = $2
                AND group_grant.permission = $4
                AND group_member.created_at >= COALESCE(member.reactivated_at, member.created_at)
          )
          OR (project.visibility = 'company' AND $4 = ANY($5))
          OR ($4 = ANY($6) AND member.role = ANY($7))
      )
)
$$;

-- project_access: a row is project.read. via says why the person reads:
-- 'grant' when they hold a fenced project.read row of their own, else 'group'
-- when one of their Groups holds project.read there through a fenced
-- membership, else 'company' for a Company-visible Project. The
-- projects.manage bridge grants no row: management is not content. The Group
-- path is found by a LATERAL ... LIMIT 1 lookup naming the Project and the
-- person, so the view stays one row per person and Project however many
-- Groups lead there, and each lookup probes company_group_members_user_idx and
-- the project_group_grants key. task_access, conversation_readers,
-- attachment_access, the Inbox and Activity reads and every frame audience
-- read this view.
CREATE VIEW project_access AS
SELECT project.company_id, project.id AS project_id, member.user_id,
       CASE WHEN read_row.user_id IS NOT NULL THEN 'grant'
            WHEN group_read.found THEN 'group'
            ELSE 'company' END::text AS via
FROM projects AS project
JOIN companies AS company
  ON company.id = project.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = project.company_id AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
LEFT JOIN project_grants AS read_row
  ON read_row.company_id = project.company_id
 AND read_row.project_id = project.id
 AND read_row.user_id = member.user_id
 AND read_row.permission = read_row.read_permission
 AND read_row.updated_at >= COALESCE(member.reactivated_at, member.created_at)
LEFT JOIN LATERAL (
    SELECT true AS found
    FROM company_group_members AS group_member
    JOIN project_group_grants AS group_grant
      ON group_grant.company_id = group_member.company_id
     AND group_grant.project_id = project.id
     AND group_grant.group_id = group_member.group_id
     AND group_grant.permission = group_grant.read_permission
    WHERE group_member.company_id = member.company_id
      AND group_member.user_id = member.user_id
      AND group_member.created_at >= COALESCE(member.reactivated_at, member.created_at)
    LIMIT 1
) AS group_read ON true
WHERE read_row.user_id IS NOT NULL OR group_read.found OR project.visibility = 'company';

-- project_access_paths: every stored path by which a person holds a
-- permission on a Project, one row per path and permission: their own fenced
-- grant rows (via 'grant', no Group) and each fenced Group membership's Group
-- rows (via 'group', the Group). Only active members with enabled accounts in
-- a Company that is not being deleted. The Company-visible floor and the
-- management bridge are not stored, so they are not here; Go adds them. A
-- UNION ALL view: reach it with a correlated lookup that names the Project and
-- the person (see the note above conversation_readers).
CREATE VIEW project_access_paths AS
SELECT grant_row.company_id, grant_row.project_id, grant_row.user_id, grant_row.permission,
       'grant'::text AS via, NULL::uuid AS group_id
FROM project_grants AS grant_row
JOIN companies AS company
  ON company.id = grant_row.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = grant_row.company_id
 AND member.user_id = grant_row.user_id
 AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE grant_row.updated_at >= COALESCE(member.reactivated_at, member.created_at)
UNION ALL
SELECT group_grant.company_id, group_grant.project_id, group_member.user_id, group_grant.permission,
       'group'::text AS via, group_grant.group_id
FROM company_group_members AS group_member
JOIN project_group_grants AS group_grant
  ON group_grant.company_id = group_member.company_id
 AND group_grant.group_id = group_member.group_id
JOIN companies AS company
  ON company.id = group_member.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = group_member.company_id
 AND member.user_id = group_member.user_id
 AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE group_member.created_at >= COALESCE(member.reactivated_at, member.created_at);

-- task_access: a Task is readable exactly where its Project is.
CREATE VIEW task_access AS
SELECT task.company_id, task.id AS task_id, task.project_id, task.conversation_id, access.user_id
FROM tasks AS task
JOIN project_access AS access
  ON access.company_id = task.company_id AND access.project_id = task.project_id;

-- task_run_agents: the Agent a Task's Runs use. A person's own Task runs with
-- the Company's root Agent; the creating Agent counts only for a Task no
-- person created. Admission, continuation, retries and completion all read
-- this one projection so they agree.
CREATE VIEW task_run_agents AS
SELECT tasks.id AS task_id, tasks.company_id,
       COALESCE(tasks.executed_by_agent_id,
                CASE WHEN tasks.creator_user_id IS NULL THEN creator.id END,
                root.id) AS agent_id
FROM tasks
LEFT JOIN agents AS creator
  ON creator.company_id = tasks.company_id
 AND creator.id = tasks.creator_agent_id
LEFT JOIN agents AS root
  ON root.company_id = tasks.company_id
 AND root.reports_to_agent_id IS NULL
 AND root.archived_at IS NULL;
