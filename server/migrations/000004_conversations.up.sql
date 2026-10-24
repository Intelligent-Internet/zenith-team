-- Conversations: conversations, channels, messages and huddles.

CREATE TABLE conversations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    kind text NOT NULL,
    task_id uuid,
    schedule_id uuid,
    agent_id uuid,
    created_by_user_id uuid,
    title text,
    request_key uuid,
    pinned_at timestamptz,
    archived_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    head_seq bigint DEFAULT 0 NOT NULL,
    is_general boolean DEFAULT false NOT NULL,
    channel_id uuid,
    channel_execution_generation bigint DEFAULT 1 NOT NULL,
    channel_context_revision bigint DEFAULT 1 NOT NULL,
    project_id uuid,
    document_thread_id uuid,
    CONSTRAINT conversations_pkey PRIMARY KEY (id),
    CONSTRAINT conversations_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT conversations_channel_execution_shape CHECK ((kind = 'channel_agent') = (channel_id IS NOT NULL)),
    CONSTRAINT conversations_discussion_revision_check CHECK ((kind <> ALL (ARRAY['human_direct', 'channel', 'huddle'])) OR version >= 1 AND version <= '9007199254740991'::bigint),
    CONSTRAINT conversations_document_thread_check CHECK (document_thread_id IS NULL OR kind = 'direct'),
    CONSTRAINT conversations_general_shape_check CHECK (NOT is_general OR kind = 'channel' AND title = 'general' AND archived_at IS NULL),
    CONSTRAINT conversations_head_seq_check CHECK (head_seq >= 0 AND head_seq <= '9007199254740991'::bigint),
    CONSTRAINT conversations_kind_check CHECK (kind IN ('task', 'direct', 'schedule', 'human_direct', 'channel', 'channel_agent', 'huddle')),
    CONSTRAINT conversations_request_key_kind_check CHECK (request_key IS NULL OR (kind IN ('direct', 'channel'))),
    CONSTRAINT conversations_shape_check CHECK (kind = 'task' AND schedule_id IS NULL AND agent_id IS NULL AND created_by_user_id IS NULL OR kind = 'channel_agent' AND channel_id IS NOT NULL AND task_id IS NULL AND schedule_id IS NULL AND agent_id IS NOT NULL AND created_by_user_id IS NOT NULL OR kind = 'direct' AND task_id IS NULL AND schedule_id IS NULL AND created_by_user_id IS NOT NULL OR kind = 'schedule' AND task_id IS NULL AND schedule_id IS NOT NULL AND agent_id IS NOT NULL AND created_by_user_id IS NOT NULL OR (kind IN ('human_direct', 'channel', 'huddle')) AND task_id IS NULL AND schedule_id IS NULL AND agent_id IS NULL AND (created_by_user_id IS NOT NULL OR is_general) AND pinned_at IS NULL AND (kind <> 'human_direct' OR archived_at IS NULL AND title IS NULL) AND (kind <> 'channel' OR request_key IS NOT NULL AND title IS NOT NULL AND title = btrim(title) AND char_length(title) >= 1 AND char_length(title) <= 100 AND octet_length(title) <= 400) AND (kind <> 'huddle' OR archived_at IS NULL AND title IS NULL AND request_key IS NULL)),
    CONSTRAINT conversations_task_project_check CHECK (kind <> 'task' OR project_id IS NOT NULL OR task_id IS NULL AND created_at < '2026-10-21 00:00:00+00'::timestamptz),
    CONSTRAINT conversations_agent_fkey FOREIGN KEY (company_id, agent_id) REFERENCES agents(company_id, id) ON DELETE SET NULL (agent_id),
    CONSTRAINT conversations_channel_fkey FOREIGN KEY (company_id, channel_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT conversations_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT conversations_schedule_id_fkey FOREIGN KEY (company_id, schedule_id) REFERENCES schedules(company_id, id) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT conversations_task_id_fkey FOREIGN KEY (company_id, task_id) REFERENCES tasks(company_id, id) ON DELETE SET NULL (task_id) DEFERRABLE INITIALLY DEFERRED
);
CREATE UNIQUE INDEX conversations_request_key_unique_idx ON conversations (company_id, created_by_user_id, request_key) WHERE (request_key IS NOT NULL);
CREATE INDEX conversations_task_list_idx ON conversations (company_id, updated_at DESC, id DESC) WHERE (kind = 'task');
CREATE INDEX conversations_project_idx ON conversations (company_id, project_id) WHERE (project_id IS NOT NULL);
CREATE INDEX conversations_discussion_list_idx ON conversations (company_id, kind, updated_at DESC, id DESC) WHERE (kind IN ('human_direct', 'channel'));
CREATE UNIQUE INDEX conversations_general_company_idx ON conversations (company_id) WHERE is_general;
CREATE UNIQUE INDEX conversations_channel_agent_unique ON conversations (channel_id, agent_id) WHERE (kind = 'channel_agent');
CREATE UNIQUE INDEX conversations_task_unique_idx ON conversations (task_id) WHERE (task_id IS NOT NULL);
CREATE UNIQUE INDEX conversations_document_agent_unique ON conversations (document_thread_id, agent_id, created_by_user_id) WHERE (document_thread_id IS NOT NULL);
CREATE INDEX conversations_schedule_creator_list_idx ON conversations (company_id, created_by_user_id, updated_at DESC, id DESC) WHERE (kind = 'schedule');
CREATE UNIQUE INDEX conversations_schedule_unique_idx ON conversations (schedule_id) WHERE (schedule_id IS NOT NULL);
CREATE INDEX conversations_direct_creator_list_idx ON conversations (company_id, created_by_user_id, updated_at DESC, id DESC) WHERE (kind = 'direct');
CREATE UNIQUE INDEX conversations_active_direct_unique_idx ON conversations (company_id, created_by_user_id, agent_id) WHERE ((kind = 'direct') AND (archived_at IS NULL) AND (agent_id IS NOT NULL) AND (request_key IS NULL));

CREATE TABLE conversation_user_states (
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    user_id uuid NOT NULL,
    pinned_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 0 NOT NULL,
    grant_generation bigint,
    hidden_at timestamptz,
    hidden_through_seq bigint DEFAULT 0 NOT NULL,
    notification_level text DEFAULT 'all' NOT NULL,
    main_read_seq bigint DEFAULT 0 NOT NULL,
    manual_unread_seq bigint,
    unread_count bigint DEFAULT 0 NOT NULL,
    archived_at timestamptz,
    CONSTRAINT conversation_user_states_pkey PRIMARY KEY (conversation_id, user_id),
    CONSTRAINT conversation_user_states_discussion_revision_check CHECK ((grant_generation IS NULL) OR ((version >= 0) AND (version <= '9007199254740991'::bigint))),
    CONSTRAINT conversation_user_states_grant_generation_check CHECK ((grant_generation >= 1) AND (grant_generation <= '9007199254740991'::bigint)),
    CONSTRAINT conversation_user_states_hidden_through_seq_check CHECK ((hidden_through_seq >= 0) AND (hidden_through_seq <= '9007199254740991'::bigint)),
    CONSTRAINT conversation_user_states_main_read_seq_check CHECK ((main_read_seq >= 0) AND (main_read_seq <= '9007199254740991'::bigint)),
    CONSTRAINT conversation_user_states_manual_unread_seq_check CHECK ((manual_unread_seq >= 1) AND (manual_unread_seq <= '9007199254740991'::bigint)),
    CONSTRAINT conversation_user_states_notification_level_check CHECK (notification_level IN ('all', 'mentions', 'muted')),
    CONSTRAINT conversation_user_states_unread_count_check CHECK (unread_count >= 0),
    CONSTRAINT conversation_user_states_company_id_conversation_id_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT conversation_user_states_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT conversation_user_states_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX conversation_user_states_archives_idx ON conversation_user_states (company_id, user_id, archived_at DESC) WHERE (archived_at IS NOT NULL);
CREATE INDEX conversation_user_states_pins_idx ON conversation_user_states (company_id, user_id, pinned_at DESC) WHERE (pinned_at IS NOT NULL);

CREATE TABLE conversation_run_queue_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    message_id uuid DEFAULT gen_random_uuid() NOT NULL,
    initiated_by_user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    content text NOT NULL,
    "position" integer NOT NULL,
    state text DEFAULT 'queued' NOT NULL,
    materialized_at timestamptz,
    removed_at timestamptz,
    removal_reason text,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT conversation_run_queue_items_pkey PRIMARY KEY (id),
    CONSTRAINT conversation_run_queue_items_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT conversation_run_queue_items_message_id_key UNIQUE (message_id),
    CONSTRAINT conversation_run_queue_items_request_key_key UNIQUE (company_id, conversation_id, initiated_by_user_id, request_key),
    CONSTRAINT conversation_run_queue_items_state_check CHECK (state IN ('queued', 'materialized', 'removed')),
    CONSTRAINT conversation_run_queue_items_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT conversation_run_queue_items_conversation_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE
);
CREATE INDEX conversation_run_queue_items_actor_idx ON conversation_run_queue_items (company_id, initiated_by_user_id, state, created_at);
CREATE INDEX conversation_run_queue_items_queued_idx ON conversation_run_queue_items (conversation_id, "position", created_at, id) WHERE (state = 'queued');

CREATE TABLE conversation_run_queue_attachments (
    company_id uuid NOT NULL,
    queue_item_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    "position" integer NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    released_at timestamptz,
    CONSTRAINT conversation_run_queue_attachments_pkey PRIMARY KEY (queue_item_id, "position"),
    CONSTRAINT conversation_run_queue_attachments_item_attachment_key UNIQUE (queue_item_id, attachment_id),
    CONSTRAINT conversation_run_queue_attachments_queue_item_fkey FOREIGN KEY (company_id, queue_item_id) REFERENCES conversation_run_queue_items(company_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX conversation_run_queue_attachments_active_attachment_idx ON conversation_run_queue_attachments (attachment_id) WHERE (released_at IS NULL);

CREATE TABLE conversation_studio_projects (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    project_id uuid,
    title text NOT NULL,
    description text DEFAULT '' NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    api_origin text NOT NULL,
    binding_revision bigint DEFAULT 1 NOT NULL,
    creation_protocol text DEFAULT 'legacy_marker' NOT NULL,
    creation_account_id uuid,
    creation_namespace uuid,
    creation_request_hash text,
    created_by_user_id uuid,
    CONSTRAINT conversation_studio_projects_pkey PRIMARY KEY (id),
    CONSTRAINT conversation_studio_projects_conversation_id_key UNIQUE (conversation_id),
    CONSTRAINT conversation_studio_projects_binding_revision_check CHECK (binding_revision > 0),
    CONSTRAINT conversation_studio_projects_creation_protocol_check CHECK (creation_protocol IN ('legacy_marker', 'receipt_v1')),
    CONSTRAINT conversation_studio_projects_description_check CHECK (length(description) <= 4000),
    CONSTRAINT conversation_studio_projects_title_check CHECK ((length(title) >= 1) AND (length(title) <= 200)),
    CONSTRAINT studio_receipt_identity CHECK ((creation_protocol = 'legacy_marker') OR ((creation_account_id IS NOT NULL) AND (creation_namespace IS NOT NULL) AND (creation_request_hash IS NOT NULL) AND (creation_request_hash ~ '^[0-9a-f]{64}$') AND (created_by_user_id IS NOT NULL) AND (length(description) <= 2000))),
    CONSTRAINT conversation_studio_projects_company_id_conversation_id_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX conversation_studio_projects_origin_project_key ON conversation_studio_projects (api_origin, project_id) WHERE (project_id IS NOT NULL);

CREATE TABLE conversation_human_directs (
    created_at timestamptz DEFAULT now() NOT NULL,
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    user_low_id uuid,
    user_high_id uuid,
    participant_ids uuid[],
    agent_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    membership_expanded boolean DEFAULT false NOT NULL,
    CONSTRAINT conversation_human_directs_pkey PRIMARY KEY (conversation_id),
    CONSTRAINT conversation_human_directs_company_id_user_low_id_user_high_key UNIQUE (company_id, user_low_id, user_high_id),
    CONSTRAINT conversation_human_directs_check CHECK (user_low_id < user_high_id),
    CONSTRAINT conversation_human_directs_company_id_conversation_id_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT conversation_human_directs_user_high_id_fkey FOREIGN KEY (user_high_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT conversation_human_directs_user_low_id_fkey FOREIGN KEY (user_low_id) REFERENCES users(id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX conversation_human_directs_participants_idx ON conversation_human_directs (company_id, participant_ids, agent_ids) WHERE ((participant_ids IS NOT NULL) AND (NOT membership_expanded));

CREATE TABLE conversation_channels (
    created_at timestamptz DEFAULT now() NOT NULL,
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    topic text DEFAULT '' NOT NULL,
    configuration_revision bigint DEFAULT 1 NOT NULL,
    create_fingerprint bytea NOT NULL,
    CONSTRAINT conversation_channels_pkey PRIMARY KEY (conversation_id),
    CONSTRAINT conversation_channels_configuration_revision_check CHECK ((configuration_revision >= 1) AND (configuration_revision <= '9007199254740991'::bigint)),
    CONSTRAINT conversation_channels_create_fingerprint_check CHECK (octet_length(create_fingerprint) = 32),
    CONSTRAINT conversation_channels_topic_check CHECK (octet_length(topic) <= 2048),
    CONSTRAINT conversation_channels_company_id_conversation_id_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE
);

CREATE TABLE conversation_members (
    created_at timestamptz DEFAULT now() NOT NULL,
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    user_id uuid NOT NULL,
    source_type text NOT NULL,
    status text DEFAULT 'active' NOT NULL,
    generation bigint DEFAULT 1 NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    joined_at timestamptz DEFAULT now() NOT NULL,
    joined_seq bigint DEFAULT 0 NOT NULL,
    joined_by_user_id uuid,
    revoked_at timestamptz,
    revoked_by_user_id uuid,
    CONSTRAINT conversation_members_pkey PRIMARY KEY (conversation_id, user_id),
    CONSTRAINT conversation_members_check CHECK (((status = 'active') AND (revoked_at IS NULL)) OR ((status = 'revoked') AND (revoked_at IS NOT NULL))),
    CONSTRAINT conversation_members_generation_check CHECK ((generation >= 1) AND (generation <= '9007199254740991'::bigint)),
    CONSTRAINT conversation_members_joined_seq_check CHECK ((joined_seq >= 0) AND (joined_seq <= '9007199254740991'::bigint)),
    CONSTRAINT conversation_members_source_type_check CHECK (source_type IN ('human_direct', 'channel', 'huddle')),
    CONSTRAINT conversation_members_status_check CHECK (status IN ('active', 'revoked')),
    CONSTRAINT conversation_members_version_check CHECK ((version >= 1) AND (version <= '9007199254740991'::bigint)),
    CONSTRAINT conversation_members_company_id_conversation_id_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT conversation_members_company_id_user_id_fkey FOREIGN KEY (company_id, user_id) REFERENCES company_members(company_id, user_id) ON DELETE CASCADE
);
CREATE INDEX conversation_members_active_user_idx ON conversation_members (company_id, user_id, conversation_id) WHERE (status = 'active');
CREATE INDEX conversation_members_roster_idx ON conversation_members (conversation_id, status, user_id);

CREATE TABLE conversation_agents (
    created_at timestamptz DEFAULT now() NOT NULL,
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    status text DEFAULT 'active' NOT NULL,
    generation bigint DEFAULT 1 NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    joined_at timestamptz DEFAULT now() NOT NULL,
    joined_by_user_id uuid,
    revoked_at timestamptz,
    revoked_by_user_id uuid,
    CONSTRAINT conversation_agents_pkey PRIMARY KEY (conversation_id, agent_id),
    CONSTRAINT conversation_agents_check CHECK (((status = 'active') AND (revoked_at IS NULL)) OR ((status = 'revoked') AND (revoked_at IS NOT NULL))),
    CONSTRAINT conversation_agents_generation_check CHECK ((generation >= 1) AND (generation <= '9007199254740991'::bigint)),
    CONSTRAINT conversation_agents_status_check CHECK (status IN ('active', 'revoked')),
    CONSTRAINT conversation_agents_version_check CHECK ((version >= 1) AND (version <= '9007199254740991'::bigint)),
    CONSTRAINT conversation_agents_company_id_agent_id_fkey FOREIGN KEY (company_id, agent_id) REFERENCES agents(company_id, id) ON DELETE CASCADE,
    CONSTRAINT conversation_agents_company_id_conversation_id_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE
);
CREATE INDEX conversation_agents_active_agent_idx ON conversation_agents (company_id, agent_id, conversation_id) WHERE (status = 'active');

CREATE TABLE huddle_ai_budgets (
    company_id uuid NOT NULL,
    monthly_limit_usd_ticks bigint NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT huddle_ai_budgets_pkey PRIMARY KEY (company_id),
    CONSTRAINT huddle_ai_budgets_monthly_limit_usd_ticks_check CHECK (monthly_limit_usd_ticks >= 0),
    CONSTRAINT huddle_ai_budgets_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);

CREATE TABLE huddle_ai_usage_receipts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    huddle_id uuid NOT NULL,
    attempt_id uuid NOT NULL,
    model_session_id text NOT NULL,
    model_seconds double precision NOT NULL,
    input_tokens bigint NOT NULL,
    cached_input_tokens bigint NOT NULL,
    output_tokens bigint NOT NULL,
    final boolean DEFAULT false NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT huddle_ai_usage_receipts_pkey PRIMARY KEY (id),
    CONSTRAINT huddle_ai_usage_receipts_attempt_id_model_session_id_key UNIQUE (attempt_id, model_session_id),
    CONSTRAINT huddle_ai_usage_receipts_check CHECK ((cached_input_tokens >= 0) AND (cached_input_tokens <= input_tokens)),
    CONSTRAINT huddle_ai_usage_receipts_input_tokens_check CHECK (input_tokens >= 0),
    CONSTRAINT huddle_ai_usage_receipts_model_seconds_check CHECK (model_seconds >= (0)::double precision),
    CONSTRAINT huddle_ai_usage_receipts_output_tokens_check CHECK (output_tokens >= 0)
);

CREATE TABLE conversation_model_settings (
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    model text,
    thinking_level text,
    revision bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT conversation_model_settings_pkey PRIMARY KEY (conversation_id, agent_id),
    CONSTRAINT conversation_model_settings_company_id_agent_id_fkey FOREIGN KEY (company_id, agent_id) REFERENCES agents(company_id, id) ON DELETE CASCADE,
    CONSTRAINT conversation_model_settings_company_id_conversation_id_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE
);

CREATE TABLE conversation_model_changes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    changed_by_user_id uuid NOT NULL,
    previous_model text,
    model text,
    after_seq bigint NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT conversation_model_changes_pkey PRIMARY KEY (id),
    CONSTRAINT conversation_model_changes_company_id_conversation_id_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE
);
CREATE INDEX conversation_model_changes_history_idx ON conversation_model_changes (conversation_id, created_at, id);

CREATE TABLE message_skills (
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    request_key uuid NOT NULL,
    selected_by_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    "position" integer NOT NULL,
    skill_id uuid NOT NULL,
    version_id uuid NOT NULL,
    start_offset integer NOT NULL,
    end_offset integer NOT NULL,
    CONSTRAINT message_skills_pkey PRIMARY KEY (conversation_id, selected_by_user_id, request_key, "position"),
    CONSTRAINT message_skills_check CHECK (end_offset > start_offset),
    CONSTRAINT message_skills_position_check CHECK ("position" >= 0),
    CONSTRAINT message_skills_start_offset_check CHECK (start_offset >= 0),
    CONSTRAINT message_skills_company_id_conversation_id_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT message_skills_company_id_version_id_fkey FOREIGN KEY (company_id, version_id) REFERENCES skill_versions(company_id, id)
);
CREATE INDEX message_skills_version ON message_skills (version_id);

CREATE TABLE message_connectors (
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    request_key uuid NOT NULL,
    selected_by_user_id uuid NOT NULL,
    "position" integer NOT NULL,
    connector_id uuid NOT NULL,
    name text NOT NULL,
    policy_revision bigint NOT NULL,
    source text NOT NULL,
    native_id text NOT NULL,
    contract_digest text NOT NULL,
    start_offset integer NOT NULL,
    end_offset integer NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT message_connectors_pkey PRIMARY KEY (conversation_id, selected_by_user_id, request_key, "position"),
    CONSTRAINT message_connectors_check CHECK (end_offset > start_offset),
    CONSTRAINT message_connectors_position_check CHECK ("position" >= 0),
    CONSTRAINT message_connectors_start_offset_check CHECK (start_offset >= 0),
    CONSTRAINT message_connectors_company_id_conversation_id_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT message_connectors_connector_id_fkey FOREIGN KEY (connector_id) REFERENCES connector_catalog_items(id) DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE message_connector_bindings (
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    request_key uuid NOT NULL,
    selected_by_user_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    runtime_id uuid NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT message_connector_bindings_pkey PRIMARY KEY (conversation_id, selected_by_user_id, request_key, agent_id),
    CONSTRAINT message_connector_bindings_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agents(id) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT message_connector_bindings_company_id_conversation_id_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT message_connector_bindings_runtime_id_fkey FOREIGN KEY (runtime_id) REFERENCES runtimes(id) DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE huddle_notes_policies (
    company_id uuid NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    retention_days integer DEFAULT 30 NOT NULL,
    monthly_seconds bigint DEFAULT 360000 NOT NULL,
    reserved_seconds bigint DEFAULT 0 NOT NULL,
    period_start date DEFAULT (date_trunc('month', now()))::date NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    CONSTRAINT huddle_notes_policies_pkey PRIMARY KEY (company_id),
    CONSTRAINT huddle_notes_policies_monthly_seconds_check CHECK (monthly_seconds >= 0),
    CONSTRAINT huddle_notes_policies_reserved_seconds_check CHECK (reserved_seconds >= 0),
    CONSTRAINT huddle_notes_policies_revision_check CHECK (revision > 0),
    CONSTRAINT huddle_notes_policies_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);

CREATE TABLE conversation_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    run_id uuid,
    seq bigint NOT NULL,
    type text NOT NULL,
    author_kind text NOT NULL,
    author_id uuid,
    item_key text,
    request_key uuid,
    content text DEFAULT '' NOT NULL,
    parts jsonb,
    content_sha256 bytea,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    body jsonb,
    revision bigint DEFAULT 1 NOT NULL,
    send_fingerprint bytea,
    thread_root_id uuid,
    edited_at timestamptz,
    deleted_at timestamptz,
    invocation_id uuid,
    CONSTRAINT conversation_items_pkey PRIMARY KEY (id),
    CONSTRAINT conversation_items_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT conversation_items_conversation_id_seq_key UNIQUE (conversation_id, seq),
    CONSTRAINT conversation_items_scope_id_key UNIQUE (company_id, conversation_id, id),
    CONSTRAINT conversation_items_author_kind_check CHECK (author_kind IN ('user', 'agent', 'system')),
    CONSTRAINT conversation_items_revision_check CHECK ((revision >= 1) AND (revision <= '9007199254740991'::bigint)),
    CONSTRAINT conversation_items_type_check CHECK (type IN ('user_message', 'agent_text', 'reasoning', 'tool_call', 'tool_result', 'error', 'discussion_system')),
    CONSTRAINT conversation_items_conversation_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT conversation_items_thread_root_fkey FOREIGN KEY (company_id, conversation_id, thread_root_id) REFERENCES conversation_items(company_id, conversation_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX channel_answer_invocation_unique ON conversation_items (invocation_id) WHERE ((run_id IS NULL) AND (type = 'agent_text'));
CREATE INDEX conversation_items_content_search_idx ON conversation_items USING gin (to_tsvector('simple'::regconfig, content)) WHERE ((content <> '') AND (type IN ('user_message', 'agent_text')));
CREATE INDEX conversation_items_discussion_reference_idx ON conversation_items (conversation_id, id) WHERE (run_id IS NULL);
CREATE INDEX conversation_items_discussion_roots_idx ON conversation_items (conversation_id, seq DESC) WHERE ((run_id IS NULL) AND (thread_root_id IS NULL));
CREATE INDEX conversation_items_discussion_unread_idx ON conversation_items (conversation_id, thread_root_id, seq) INCLUDE (author_id) WHERE ((run_id IS NULL) AND (type = 'user_message') AND (deleted_at IS NULL));
CREATE UNIQUE INDEX conversation_items_request_key_idx ON conversation_items (company_id, conversation_id, author_id, request_key) WHERE (request_key IS NOT NULL);
CREATE UNIQUE INDEX conversation_items_run_item_key_idx ON conversation_items (run_id, item_key) WHERE (item_key IS NOT NULL);
CREATE INDEX conversation_items_run_seq_idx ON conversation_items (run_id, seq);
CREATE INDEX conversation_items_thread_replies_idx ON conversation_items (conversation_id, thread_root_id, seq DESC) WHERE (thread_root_id IS NOT NULL);
CREATE INDEX conversation_items_timeline_idx ON conversation_items (conversation_id, seq) WHERE (type IN ('user_message', 'agent_text'));

CREATE TABLE message_attachments (
    company_id uuid NOT NULL,
    message_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    "position" integer NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT message_attachments_pkey PRIMARY KEY (attachment_id),
    CONSTRAINT message_attachments_message_id_position_key UNIQUE (message_id, "position"),
    CONSTRAINT message_attachments_message_fkey FOREIGN KEY (company_id, message_id) REFERENCES conversation_items(company_id, id) ON DELETE CASCADE
);

CREATE TABLE conversation_message_pins (
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    message_id uuid NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    pinned_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    pinned_by_user_id uuid NOT NULL,
    CONSTRAINT conversation_message_pins_pkey PRIMARY KEY (conversation_id, message_id),
    CONSTRAINT conversation_message_pins_company_id_conversation_id_messa_fkey FOREIGN KEY (company_id, conversation_id, message_id) REFERENCES conversation_items(company_id, conversation_id, id) ON DELETE CASCADE
);
CREATE INDEX conversation_message_pins_page_idx ON conversation_message_pins (conversation_id, pinned_at DESC, message_id DESC);

CREATE TABLE conversation_message_reactions (
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    message_id uuid NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    emoji text NOT NULL,
    CONSTRAINT conversation_message_reactions_pkey PRIMARY KEY (message_id, user_id, emoji),
    CONSTRAINT conversation_message_reactions_emoji_check CHECK ((octet_length(emoji) >= 1) AND (octet_length(emoji) <= 64)),
    CONSTRAINT conversation_message_reaction_company_id_conversation_id_m_fkey FOREIGN KEY (company_id, conversation_id, message_id) REFERENCES conversation_items(company_id, conversation_id, id) ON DELETE CASCADE,
    CONSTRAINT conversation_message_reactions_company_id_user_id_fkey FOREIGN KEY (company_id, user_id) REFERENCES company_members(company_id, user_id) ON DELETE CASCADE
);
CREATE INDEX conversation_message_reactions_people_idx ON conversation_message_reactions (message_id, emoji, user_id);

CREATE TABLE conversation_thread_states (
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    root_id uuid NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    grant_generation bigint NOT NULL,
    follow text NOT NULL,
    read_seq bigint DEFAULT 0 NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    unread_count bigint DEFAULT 0 NOT NULL,
    manual_unread_seq bigint,
    CONSTRAINT conversation_thread_states_pkey PRIMARY KEY (root_id, user_id),
    CONSTRAINT conversation_thread_states_follow_check CHECK (follow IN ('auto', 'following', 'unfollowed')),
    CONSTRAINT conversation_thread_states_grant_generation_check CHECK ((grant_generation >= 1) AND (grant_generation <= '9007199254740991'::bigint)),
    CONSTRAINT conversation_thread_states_manual_unread_seq_check CHECK ((manual_unread_seq >= 1) AND (manual_unread_seq <= '9007199254740991'::bigint)),
    CONSTRAINT conversation_thread_states_read_seq_check CHECK ((read_seq >= 0) AND (read_seq <= '9007199254740991'::bigint)),
    CONSTRAINT conversation_thread_states_unread_count_check CHECK (unread_count >= 0),
    CONSTRAINT conversation_thread_states_version_check CHECK ((version >= 1) AND (version <= '9007199254740991'::bigint)),
    CONSTRAINT conversation_thread_states_company_id_conversation_id_root_fkey FOREIGN KEY (company_id, conversation_id, root_id) REFERENCES conversation_items(company_id, conversation_id, id) ON DELETE CASCADE,
    CONSTRAINT conversation_thread_states_conversation_id_user_id_fkey FOREIGN KEY (conversation_id, user_id) REFERENCES conversation_members(conversation_id, user_id) ON DELETE CASCADE
);
CREATE INDEX conversation_thread_states_user_idx ON conversation_thread_states (conversation_id, user_id);

CREATE TABLE discussion_artifacts (
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    company_id uuid NOT NULL,
    message_id uuid NOT NULL,
    output_id uuid,
    artifact_id uuid,
    CONSTRAINT discussion_artifacts_artifact_unique UNIQUE (company_id, message_id, artifact_id),
    CONSTRAINT discussion_artifacts_output_unique UNIQUE (company_id, message_id, output_id),
    CONSTRAINT discussion_artifacts_target_check CHECK (num_nonnulls(output_id, artifact_id) = 1),
    CONSTRAINT discussion_artifacts_artifact_fkey FOREIGN KEY (company_id, artifact_id) REFERENCES artifacts(company_id, id) ON DELETE CASCADE
);

CREATE TABLE channel_invocations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    channel_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    trigger_message_id uuid NOT NULL,
    thread_root_id uuid NOT NULL,
    caller_user_id uuid NOT NULL,
    agent_generation bigint NOT NULL,
    member_generation bigint NOT NULL,
    channel_generation bigint NOT NULL,
    trigger_content text NOT NULL,
    trigger_revision bigint NOT NULL,
    status text DEFAULT 'queued' NOT NULL,
    publication_status text DEFAULT 'pending' NOT NULL,
    failure_reason text,
    request_key uuid,
    queued_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT channel_invocations_pkey PRIMARY KEY (id),
    CONSTRAINT channel_invocations_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT channel_invocations_trigger_message_id_agent_id_key UNIQUE (trigger_message_id, agent_id),
    CONSTRAINT channel_invocations_publication_status_check CHECK (publication_status IN ('pending', 'published', 'no_output', 'blocked')),
    CONSTRAINT channel_invocations_status_check CHECK (status IN ('queued', 'active', 'waiting_input', 'completed', 'failed', 'cancelled')),
    CONSTRAINT channel_invocations_company_id_agent_id_fkey FOREIGN KEY (company_id, agent_id) REFERENCES agents(company_id, id) ON DELETE CASCADE,
    CONSTRAINT channel_invocations_company_id_channel_id_fkey FOREIGN KEY (company_id, channel_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT channel_invocations_company_id_channel_id_thread_root_id_fkey FOREIGN KEY (company_id, channel_id, thread_root_id) REFERENCES conversation_items(company_id, conversation_id, id) ON DELETE CASCADE,
    CONSTRAINT channel_invocations_company_id_channel_id_trigger_message__fkey FOREIGN KEY (company_id, channel_id, trigger_message_id) REFERENCES conversation_items(company_id, conversation_id, id) ON DELETE CASCADE,
    CONSTRAINT channel_invocations_company_id_conversation_id_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX channel_invocations_one_active ON channel_invocations (conversation_id) WHERE (status IN ('active', 'waiting_input'));
CREATE INDEX channel_invocations_queue ON channel_invocations (conversation_id, queued_at, id) WHERE (status = 'queued');

CREATE TABLE huddles (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    source_conversation_id uuid NOT NULL,
    chat_conversation_id uuid NOT NULL,
    started_by_user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    state text DEFAULT 'starting' NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    deadline_at timestamptz NOT NULL,
    room_name text,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    ended_at timestamptz,
    source_item_id uuid,
    notes_mode boolean DEFAULT false NOT NULL,
    CONSTRAINT huddles_pkey PRIMARY KEY (id),
    CONSTRAINT huddles_company_id_chat_conversation_id_key UNIQUE (company_id, chat_conversation_id),
    CONSTRAINT huddles_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT huddles_company_id_started_by_user_id_request_key_key UNIQUE (company_id, started_by_user_id, request_key),
    CONSTRAINT huddles_source_item_unique UNIQUE (company_id, source_item_id),
    CONSTRAINT huddles_check CHECK (((state = 'ended') AND (ended_at IS NOT NULL)) OR ((state <> 'ended') AND (ended_at IS NULL))),
    CONSTRAINT huddles_revision_check CHECK ((revision >= 1) AND (revision <= '9007199254740991'::bigint)),
    CONSTRAINT huddles_room_name_check CHECK ((room_name IS NULL) OR ((room_name = btrim(room_name)) AND ((char_length(room_name) >= 1) AND (char_length(room_name) <= 128)))),
    CONSTRAINT huddles_state_check CHECK (state IN ('starting', 'open', 'ending', 'ended')),
    CONSTRAINT huddles_company_id_chat_conversation_id_fkey FOREIGN KEY (company_id, chat_conversation_id) REFERENCES conversations(company_id, id) ON DELETE RESTRICT,
    CONSTRAINT huddles_company_id_source_conversation_id_fkey FOREIGN KEY (company_id, source_conversation_id) REFERENCES conversations(company_id, id) ON DELETE RESTRICT,
    CONSTRAINT huddles_company_id_started_by_user_id_fkey FOREIGN KEY (company_id, started_by_user_id) REFERENCES company_members(company_id, user_id) ON DELETE RESTRICT,
    CONSTRAINT huddles_source_item_fk FOREIGN KEY (company_id, source_item_id) REFERENCES conversation_items(company_id, id) ON DELETE SET NULL (source_item_id)
);
CREATE INDEX huddles_company_created_idx ON huddles (company_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX huddles_open_source_idx ON huddles (company_id, source_conversation_id) WHERE (state IN ('starting', 'open', 'ending'));

CREATE TABLE huddle_connections (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    huddle_id uuid NOT NULL,
    user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    status text DEFAULT 'reserved' NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    connected_at timestamptz,
    ended_at timestamptz,
    reservation_expires_at timestamptz,
    CONSTRAINT huddle_connections_pkey PRIMARY KEY (id),
    CONSTRAINT huddle_connections_company_id_huddle_id_user_id_request_key_key UNIQUE (company_id, huddle_id, user_id, request_key),
    CONSTRAINT huddle_connections_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT huddle_connections_check CHECK (((status = 'connected') AND (connected_at IS NOT NULL)) OR (status <> 'connected')),
    CONSTRAINT huddle_connections_check1 CHECK (((status = 'ended') AND (ended_at IS NOT NULL)) OR (status <> 'ended')),
    CONSTRAINT huddle_connections_reservation_expiry_check CHECK ((status <> 'reserved') OR (reservation_expires_at IS NOT NULL)),
    CONSTRAINT huddle_connections_revision_check CHECK ((revision >= 1) AND (revision <= '9007199254740991'::bigint)),
    CONSTRAINT huddle_connections_status_check CHECK (status IN ('reserved', 'connected', 'ended', 'revoked')),
    CONSTRAINT huddle_connections_company_id_huddle_id_fkey FOREIGN KEY (company_id, huddle_id) REFERENCES huddles(company_id, id) ON DELETE CASCADE,
    CONSTRAINT huddle_connections_company_id_user_id_fkey FOREIGN KEY (company_id, user_id) REFERENCES company_members(company_id, user_id) ON DELETE RESTRICT
);
CREATE INDEX huddle_connections_expiry_idx ON huddle_connections (reservation_expires_at) WHERE (status = 'reserved');
CREATE INDEX huddle_connections_huddle_idx ON huddle_connections (company_id, huddle_id, created_at, id);
CREATE UNIQUE INDEX huddle_connections_open_user_idx ON huddle_connections (company_id, huddle_id, user_id) WHERE (status IN ('reserved', 'connected'));

CREATE TABLE huddle_invitations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    huddle_id uuid NOT NULL,
    inviter_user_id uuid NOT NULL,
    invitee_user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    status text DEFAULT 'pending' NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT huddle_invitations_pkey PRIMARY KEY (id),
    CONSTRAINT huddle_invitations_company_id_huddle_id_invitee_user_id_req_key UNIQUE (company_id, huddle_id, invitee_user_id, request_key),
    CONSTRAINT huddle_invitations_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT huddle_invitations_revision_check CHECK ((revision >= 1) AND (revision <= '9007199254740991'::bigint)),
    CONSTRAINT huddle_invitations_status_check CHECK (status IN ('pending', 'accepted', 'declined', 'expired', 'revoked')),
    CONSTRAINT huddle_invitations_company_id_huddle_id_fkey FOREIGN KEY (company_id, huddle_id) REFERENCES huddles(company_id, id) ON DELETE CASCADE,
    CONSTRAINT huddle_invitations_company_id_invitee_user_id_fkey FOREIGN KEY (company_id, invitee_user_id) REFERENCES company_members(company_id, user_id) ON DELETE RESTRICT,
    CONSTRAINT huddle_invitations_company_id_inviter_user_id_fkey FOREIGN KEY (company_id, inviter_user_id) REFERENCES company_members(company_id, user_id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX huddle_invitations_pending_idx ON huddle_invitations (company_id, huddle_id, invitee_user_id) WHERE (status = 'pending');

CREATE TABLE huddle_ai_budget_reservations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    huddle_id uuid NOT NULL,
    period_start timestamptz NOT NULL,
    reserved_usd_ticks bigint NOT NULL,
    spent_usd_ticks bigint DEFAULT 0 NOT NULL,
    deadline_at timestamptz NOT NULL,
    settled_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT huddle_ai_budget_reservations_pkey PRIMARY KEY (id),
    CONSTRAINT huddle_ai_budget_reservations_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT huddle_ai_budget_reservations_huddle_id_key UNIQUE (huddle_id),
    CONSTRAINT huddle_ai_budget_reservations_reserved_usd_ticks_check CHECK (reserved_usd_ticks >= 0),
    CONSTRAINT huddle_ai_budget_reservations_spent_usd_ticks_check CHECK (spent_usd_ticks >= 0),
    CONSTRAINT huddle_ai_budget_reservations_company_id_fkey FOREIGN KEY (company_id) REFERENCES huddle_ai_budgets(company_id) ON DELETE CASCADE,
    CONSTRAINT huddle_ai_budget_reservations_company_id_huddle_id_fkey FOREIGN KEY (company_id, huddle_id) REFERENCES huddles(company_id, id) ON DELETE CASCADE
);
CREATE INDEX huddle_ai_budget_period_idx ON huddle_ai_budget_reservations (company_id, period_start);

CREATE TABLE huddle_ai_states (
    huddle_id uuid NOT NULL,
    company_id uuid NOT NULL,
    disabled boolean DEFAULT false NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    generation bigint DEFAULT 0 NOT NULL,
    chat_start_seq bigint NOT NULL,
    chat_cursor bigint NOT NULL,
    chat_completed_seq bigint NOT NULL,
    greeted boolean DEFAULT false NOT NULL,
    state text DEFAULT 'pending' NOT NULL,
    retry_at timestamptz DEFAULT now() NOT NULL,
    failures integer DEFAULT 0 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    agent_id uuid,
    agent_profile jsonb DEFAULT '{}'::jsonb NOT NULL,
    allow_company_context boolean DEFAULT false NOT NULL,
    context_granted_by_user_id uuid,
    tool_activity jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT huddle_ai_states_pkey PRIMARY KEY (huddle_id),
    CONSTRAINT huddle_ai_states_company_id_huddle_id_key UNIQUE (company_id, huddle_id),
    CONSTRAINT huddle_ai_states_state_check CHECK (state IN ('pending', 'starting', 'listening', 'speaking', 'unavailable', 'disabled', 'ended')),
    CONSTRAINT huddle_ai_states_agent_fkey FOREIGN KEY (company_id, agent_id) REFERENCES agents(company_id, id) ON DELETE SET NULL (agent_id),
    CONSTRAINT huddle_ai_states_company_id_huddle_id_fkey FOREIGN KEY (company_id, huddle_id) REFERENCES huddles(company_id, id) ON DELETE CASCADE
);
CREATE INDEX huddle_ai_states_agent_idx ON huddle_ai_states (company_id, agent_id) WHERE (agent_id IS NOT NULL);

CREATE TABLE huddle_ai_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    huddle_id uuid NOT NULL,
    generation bigint NOT NULL,
    state text DEFAULT 'pending' NOT NULL,
    capability_hash bytea,
    lease_expires_at timestamptz DEFAULT (now() + '00:00:30'::interval) NOT NULL,
    deadline_at timestamptz NOT NULL,
    dispatch_after timestamptz DEFAULT now() NOT NULL,
    finished_at timestamptz,
    usage_final boolean DEFAULT false NOT NULL,
    model_seconds double precision DEFAULT 0 NOT NULL,
    spent_usd_ticks bigint DEFAULT 0 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    tool_calls integer DEFAULT 0 NOT NULL,
    tool_window_calls integer DEFAULT 0 NOT NULL,
    last_tool_at timestamptz,
    stop_reason text DEFAULT '' NOT NULL,
    invitation_revision bigint DEFAULT 0 NOT NULL,
    transcript_sequence bigint DEFAULT 0 NOT NULL,
    transcript_hash bytea,
    CONSTRAINT huddle_ai_sessions_pkey PRIMARY KEY (id),
    CONSTRAINT huddle_ai_sessions_company_id_huddle_id_generation_key UNIQUE (company_id, huddle_id, generation),
    CONSTRAINT huddle_ai_sessions_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT huddle_ai_sessions_model_seconds_check CHECK (model_seconds >= (0)::double precision),
    CONSTRAINT huddle_ai_sessions_spent_usd_ticks_check CHECK (spent_usd_ticks >= 0),
    CONSTRAINT huddle_ai_sessions_state_check CHECK (state IN ('pending', 'claimed', 'stopping', 'finished')),
    CONSTRAINT huddle_ai_sessions_company_id_huddle_id_fkey FOREIGN KEY (company_id, huddle_id) REFERENCES huddle_ai_states(company_id, huddle_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX huddle_ai_sessions_active_idx ON huddle_ai_sessions (huddle_id) WHERE (state <> 'finished');

CREATE TABLE huddle_ai_operations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    huddle_id uuid NOT NULL,
    user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    enabled boolean NOT NULL,
    expected_revision bigint NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    agent_id uuid,
    allow_company_context boolean DEFAULT false NOT NULL,
    CONSTRAINT huddle_ai_operations_pkey PRIMARY KEY (id),
    CONSTRAINT huddle_ai_operations_company_id_huddle_id_user_id_request_k_key UNIQUE (company_id, huddle_id, user_id, request_key),
    CONSTRAINT huddle_ai_operations_company_id_huddle_id_fkey FOREIGN KEY (company_id, huddle_id) REFERENCES huddle_ai_states(company_id, huddle_id) ON DELETE CASCADE
);

CREATE TABLE huddle_work_proposals (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    huddle_id uuid NOT NULL,
    attempt_id uuid,
    invitation_revision bigint NOT NULL,
    created_by_agent_id uuid NOT NULL,
    agent_name text NOT NULL,
    project_id uuid,
    project_title text NOT NULL,
    task_id uuid,
    task_key text DEFAULT '' NOT NULL,
    request_key text NOT NULL,
    request_hash bytea NOT NULL,
    title text NOT NULL,
    brief text NOT NULL,
    state text DEFAULT 'pending' NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    updated_by_user_id uuid,
    expires_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    submitted_by_user_id uuid,
    submitted_revision bigint,
    submission_hash bytea,
    submitted_task_id uuid,
    submitted_task_key text,
    submitted_run_id uuid,
    submitted_at timestamptz,
    voice_notice_keys text[] DEFAULT '{}'::text[] NOT NULL,
    CONSTRAINT huddle_work_proposals_pkey PRIMARY KEY (id),
    CONSTRAINT huddle_work_proposals_company_id_huddle_id_invitation_revis_key UNIQUE (company_id, huddle_id, invitation_revision, request_key),
    CONSTRAINT huddle_work_proposals_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT huddle_work_proposals_state_check CHECK (state IN ('pending', 'rejected', 'submitted')),
    CONSTRAINT huddle_work_proposals_company_id_attempt_id_fkey FOREIGN KEY (company_id, attempt_id) REFERENCES huddle_ai_sessions(company_id, id) ON DELETE SET NULL (attempt_id),
    CONSTRAINT huddle_work_proposals_company_id_huddle_id_fkey FOREIGN KEY (company_id, huddle_id) REFERENCES huddles(company_id, id) ON DELETE CASCADE,
    CONSTRAINT huddle_work_proposals_company_id_project_id_fkey FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE SET NULL (project_id),
    CONSTRAINT huddle_work_proposals_company_id_submitted_task_id_fkey FOREIGN KEY (company_id, submitted_task_id) REFERENCES tasks(company_id, id) ON DELETE SET NULL (submitted_task_id),
    CONSTRAINT huddle_work_proposals_company_id_task_id_fkey FOREIGN KEY (company_id, task_id) REFERENCES tasks(company_id, id) ON DELETE SET NULL (task_id)
);

CREATE TABLE huddle_transcripts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    huddle_id uuid NOT NULL,
    invitation_revision bigint NOT NULL,
    agent_name text NOT NULL,
    content text DEFAULT '' NOT NULL,
    last_speaker text DEFAULT '' NOT NULL,
    truncated boolean DEFAULT false NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    ended_at timestamptz,
    posted_at timestamptz,
    CONSTRAINT huddle_transcripts_pkey PRIMARY KEY (id),
    CONSTRAINT huddle_transcripts_company_id_huddle_id_invitation_revision_key UNIQUE (company_id, huddle_id, invitation_revision),
    CONSTRAINT huddle_transcripts_company_id_huddle_id_fkey FOREIGN KEY (company_id, huddle_id) REFERENCES huddles(company_id, id) ON DELETE CASCADE
);
CREATE INDEX huddle_transcripts_unposted_idx ON huddle_transcripts (created_at) WHERE ((ended_at IS NOT NULL) AND (posted_at IS NULL));

CREATE TABLE huddle_notes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    huddle_id uuid NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    state text DEFAULT 'off' NOT NULL,
    started_by_user_id uuid,
    stop_reason text DEFAULT '' NOT NULL,
    expires_at timestamptz NOT NULL,
    recap_state text DEFAULT 'pending' NOT NULL,
    recap jsonb DEFAULT '{}'::jsonb NOT NULL,
    recap_revision bigint DEFAULT 0 NOT NULL,
    recap_edited_by_user_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    recap_attempts integer DEFAULT 0 NOT NULL,
    recap_lease_until timestamptz,
    recap_token uuid,
    CONSTRAINT huddle_notes_pkey PRIMARY KEY (id),
    CONSTRAINT huddle_notes_company_id_huddle_id_key UNIQUE (company_id, huddle_id),
    CONSTRAINT huddle_notes_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT huddle_notes_recap_attempts_check CHECK (recap_attempts >= 0),
    CONSTRAINT huddle_notes_recap_revision_check CHECK (recap_revision >= 0),
    CONSTRAINT huddle_notes_recap_state_check CHECK (recap_state IN ('pending', 'processing', 'ready', 'failed')),
    CONSTRAINT huddle_notes_revision_check CHECK (revision > 0),
    CONSTRAINT huddle_notes_state_check CHECK (state IN ('off', 'starting', 'capturing', 'stopping', 'stopped', 'interrupted', 'ended', 'expired')),
    CONSTRAINT huddle_notes_company_id_huddle_id_fkey FOREIGN KEY (company_id, huddle_id) REFERENCES huddles(company_id, id) ON DELETE CASCADE
);
CREATE INDEX huddle_notes_expiry ON huddle_notes (expires_at) WHERE (state <> 'expired');

CREATE TABLE huddle_notes_intervals (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    notes_id uuid NOT NULL,
    request_key uuid NOT NULL,
    capability_hash bytea,
    state text DEFAULT 'pending' NOT NULL,
    lease_expires_at timestamptz DEFAULT (now() + '00:00:30'::interval) NOT NULL,
    deadline_at timestamptz NOT NULL,
    started_at timestamptz,
    ended_at timestamptz,
    sequence bigint DEFAULT 0 NOT NULL,
    batch_hash bytea,
    created_at timestamptz DEFAULT now() NOT NULL,
    stop_requested_at timestamptz,
    drain_until timestamptz,
    CONSTRAINT huddle_notes_intervals_pkey PRIMARY KEY (id),
    CONSTRAINT huddle_notes_intervals_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT huddle_notes_intervals_notes_id_request_key_key UNIQUE (notes_id, request_key),
    CONSTRAINT huddle_notes_intervals_sequence_check CHECK (sequence >= 0),
    CONSTRAINT huddle_notes_intervals_state_check CHECK (state IN ('pending', 'claimed', 'finished')),
    CONSTRAINT huddle_notes_intervals_company_id_notes_id_fkey FOREIGN KEY (company_id, notes_id) REFERENCES huddle_notes(company_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX huddle_notes_one_active ON huddle_notes_intervals (notes_id) WHERE (state <> 'finished');

CREATE TABLE huddle_notes_segments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    interval_id uuid NOT NULL,
    sequence bigint NOT NULL,
    speaker text NOT NULL,
    content text NOT NULL,
    start_ms bigint NOT NULL,
    end_ms bigint NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT huddle_notes_segments_pkey PRIMARY KEY (id),
    CONSTRAINT huddle_notes_segments_interval_id_sequence_key UNIQUE (interval_id, sequence),
    CONSTRAINT huddle_notes_segments_sequence_check CHECK (sequence > 0),
    CONSTRAINT huddle_notes_segments_company_id_interval_id_fkey FOREIGN KEY (company_id, interval_id) REFERENCES huddle_notes_intervals(company_id, id) ON DELETE CASCADE
);

CREATE TABLE huddle_notes_recap_revisions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    notes_id uuid NOT NULL,
    revision bigint NOT NULL,
    content jsonb NOT NULL,
    edited_by_user_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT huddle_notes_recap_revisions_pkey PRIMARY KEY (id),
    CONSTRAINT huddle_notes_recap_revisions_notes_id_revision_key UNIQUE (notes_id, revision),
    CONSTRAINT huddle_notes_recap_revisions_revision_check CHECK (revision > 0),
    CONSTRAINT huddle_notes_recap_revisions_company_id_notes_id_fkey FOREIGN KEY (company_id, notes_id) REFERENCES huddle_notes(company_id, id) ON DELETE CASCADE
);

CREATE TABLE huddle_notes_actions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    notes_id uuid NOT NULL,
    action_index integer NOT NULL,
    recap_revision bigint NOT NULL,
    task_id uuid,
    submitted_by_user_id uuid NOT NULL,
    submission_hash bytea NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT huddle_notes_actions_pkey PRIMARY KEY (id),
    CONSTRAINT huddle_notes_actions_notes_id_action_index_key UNIQUE (notes_id, action_index),
    CONSTRAINT huddle_notes_actions_action_index_check CHECK (action_index >= 0),
    CONSTRAINT huddle_notes_actions_recap_revision_check CHECK (recap_revision > 0),
    CONSTRAINT huddle_notes_actions_company_id_notes_id_fkey FOREIGN KEY (company_id, notes_id) REFERENCES huddle_notes(company_id, id) ON DELETE CASCADE,
    CONSTRAINT huddle_notes_actions_company_id_task_id_fkey FOREIGN KEY (company_id, task_id) REFERENCES tasks(company_id, id) ON DELETE SET NULL (task_id)
);

-- Links to these tables from tables created earlier.
ALTER TABLE artifacts ADD CONSTRAINT artifacts_conversation_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE SET NULL (conversation_id);
ALTER TABLE discussion_artifacts ADD CONSTRAINT discussion_artifacts_company_id_message_id_fkey FOREIGN KEY (company_id, message_id) REFERENCES conversation_items(company_id, id) ON DELETE CASCADE;
ALTER TABLE companies ADD CONSTRAINT companies_proactive_mode_conversation_fkey FOREIGN KEY (id, proactive_mode_conversation_id) REFERENCES conversations(company_id, id) ON DELETE SET NULL (proactive_mode_conversation_id);
ALTER TABLE company_initializations ADD CONSTRAINT company_initializations_company_id_conversation_id_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE;
ALTER TABLE approvals ADD CONSTRAINT approvals_conversation_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE;
ALTER TABLE questions ADD CONSTRAINT questions_conversation_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE;
ALTER TABLE schedules ADD CONSTRAINT schedules_conversation_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE schedule_firings ADD CONSTRAINT schedule_firings_conversation_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE;
ALTER TABLE schedule_firings ADD CONSTRAINT schedule_firings_message_fkey FOREIGN KEY (company_id, message_id) REFERENCES conversation_items(company_id, id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE task_report_batches ADD CONSTRAINT task_report_batches_conversation_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE;
ALTER TABLE task_report_batches ADD CONSTRAINT task_report_batches_message_fkey FOREIGN KEY (company_id, message_id) REFERENCES conversation_items(company_id, id);
ALTER TABLE tasks ADD CONSTRAINT tasks_conversation_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE;
ALTER TABLE conversation_items ADD CONSTRAINT conversation_items_invocation_fkey FOREIGN KEY (company_id, invocation_id) REFERENCES channel_invocations(company_id, id) ON DELETE CASCADE;

-- The delegate rule: what a Run or the voice assistant may do on a Project.
-- Its Authorizer holds the permission and read there (never through the
-- management bridge: a delegate never manages), and a Task chat's answer
-- stays in that chat's Project.
--
-- Arguments are positional because a SQL function resolves a bare name to a
-- column first. Neither function is inlined (each body holds a subquery), so
-- each is one opaque call per candidate Project, never inside a list over a
-- Company's Tasks.
CREATE FUNCTION delegate_permission_held(
    company_id uuid,
    authorizer_user_id uuid,
    answer_conversation_id uuid,
    project_id uuid,
    permission text,
    read_permission text,
    company_default_permissions text[]
) RETURNS boolean
LANGUAGE sql STABLE
AS $$
SELECT $2 IS NOT NULL
   AND project_permission_held($1, $4, $2, $6, $7, '{}', '{}')
   AND project_permission_held($1, $4, $2, $5, $7, '{}', '{}')
   AND NOT EXISTS (
       SELECT 1 FROM conversations AS answer
       WHERE answer.company_id = $1 AND answer.id = $3
         AND answer.kind = 'task' AND answer.project_id IS DISTINCT FROM $4
   )
$$;

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

CREATE FUNCTION check_discussion_item_shape() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE conversation_type text; action text; parent conversation_items;
BEGIN
    IF TG_OP='UPDATE' AND OLD.type='discussion_system' AND OLD.metadata->>'action' IN ('huddle_transcript','huddle_call')
        AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'immutable huddle card' USING ERRCODE='23514';
    END IF;
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
        ELSIF NEW.type='discussion_system' AND NEW.metadata->>'action'='huddle_transcript' THEN
            IF conversation_type<>'huddle' OR NEW.author_kind<>'system' OR NEW.author_id IS NOT NULL
                OR NEW.request_key IS NOT NULL OR NEW.send_fingerprint IS NOT NULL OR NEW.thread_root_id IS NOT NULL
                OR NEW.edited_at IS NOT NULL OR NEW.deleted_at IS NOT NULL OR NEW.content<>''
                OR NEW.body IS NULL OR jsonb_typeof(NEW.body)<>'object'
                OR jsonb_typeof(NEW.metadata->'agentName') IS DISTINCT FROM 'string'
                OR NEW.metadata->'actorUserId' IS DISTINCT FROM 'null'::jsonb
                OR (NEW.metadata - ARRAY['action','actorUserId','agentName'])<>'{}'::jsonb
                OR (TG_OP='UPDATE' AND NEW IS DISTINCT FROM OLD) THEN
                RAISE EXCEPTION 'invalid huddle transcript card' USING ERRCODE='23514';
            END IF;
        ELSIF NEW.type='discussion_system' AND NEW.metadata->>'action'='huddle_call' THEN
            IF conversation_type NOT IN ('human_direct','channel') OR NEW.author_kind<>'system'
                OR NEW.author_id IS NOT NULL OR NEW.request_key IS NOT NULL OR NEW.send_fingerprint IS NOT NULL
                OR NEW.thread_root_id IS NOT NULL OR NEW.edited_at IS NOT NULL OR NEW.deleted_at IS NOT NULL
                OR NEW.content<>'' OR NEW.body IS NOT NULL
                OR NEW.metadata <> '{"action":"huddle_call","actorUserId":null}'::jsonb THEN
                RAISE EXCEPTION 'invalid huddle call card' USING ERRCODE='23514';
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

CREATE FUNCTION check_discussion_collaboration_shape() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE item_id uuid; item conversation_items;
BEGIN
    IF TG_TABLE_NAME='conversation_thread_states' THEN item_id:=NEW.root_id;
    ELSE item_id:=NEW.message_id;
    END IF;
    SELECT * INTO item FROM conversation_items
    WHERE company_id=NEW.company_id AND conversation_id=NEW.conversation_id AND id=item_id FOR SHARE;
    IF NOT FOUND OR item.run_id IS NOT NULL OR item.type NOT IN ('user_message','agent_text') THEN
        RAISE EXCEPTION 'collaboration requires a discussion message' USING ERRCODE='23514';
    END IF;
    IF TG_TABLE_NAME='conversation_thread_states' THEN
        IF item.thread_root_id IS NOT NULL OR NOT EXISTS(
            SELECT 1 FROM conversation_members WHERE company_id=NEW.company_id AND conversation_id=NEW.conversation_id
                AND user_id=NEW.user_id AND generation=NEW.grant_generation AND status='active') THEN
            RAISE EXCEPTION 'thread state requires a root and current grant' USING ERRCODE='23514';
        END IF;
    ELSIF item.deleted_at IS NOT NULL THEN
        RAISE EXCEPTION 'deleted messages cannot be pinned or reacted to' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;

CREATE FUNCTION preserve_discussion_identity() RETURNS trigger LANGUAGE plpgsql AS $$
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

CREATE FUNCTION advance_channel_execution_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME='conversations' THEN
        IF OLD.kind='channel' AND NEW.archived_at IS DISTINCT FROM OLD.archived_at THEN
            NEW.channel_execution_generation := OLD.channel_execution_generation + 1;
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.run_id IS NULL AND (NEW.revision IS DISTINCT FROM OLD.revision) THEN
        UPDATE conversations SET channel_context_revision=channel_context_revision+1
        WHERE id=NEW.conversation_id AND kind IN ('channel','human_direct');
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER channel_archive_generation BEFORE UPDATE OF archived_at ON conversations FOR EACH ROW EXECUTE FUNCTION advance_channel_execution_revision();
CREATE TRIGGER discussion_identity BEFORE UPDATE ON conversations FOR EACH ROW EXECUTE FUNCTION preserve_discussion_identity();
CREATE CONSTRAINT TRIGGER discussion_shape AFTER INSERT OR UPDATE ON conversations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_discussion_shape();
CREATE TRIGGER discussion_identity BEFORE UPDATE ON conversation_human_directs FOR EACH ROW EXECUTE FUNCTION preserve_discussion_identity();
CREATE CONSTRAINT TRIGGER discussion_shape AFTER INSERT OR DELETE OR UPDATE ON conversation_human_directs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_discussion_shape();
CREATE TRIGGER discussion_identity BEFORE UPDATE ON conversation_channels FOR EACH ROW EXECUTE FUNCTION preserve_discussion_identity();
CREATE CONSTRAINT TRIGGER discussion_shape AFTER INSERT OR DELETE OR UPDATE ON conversation_channels DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_discussion_shape();
CREATE TRIGGER discussion_identity BEFORE UPDATE ON conversation_members FOR EACH ROW EXECUTE FUNCTION preserve_discussion_identity();
CREATE CONSTRAINT TRIGGER discussion_shape AFTER INSERT OR DELETE OR UPDATE ON conversation_members DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_discussion_shape();
CREATE TRIGGER discussion_identity BEFORE UPDATE ON conversation_agents FOR EACH ROW EXECUTE FUNCTION preserve_discussion_identity();
CREATE CONSTRAINT TRIGGER discussion_shape AFTER INSERT OR DELETE OR UPDATE ON conversation_agents DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_discussion_shape();
CREATE TRIGGER channel_context_revision AFTER UPDATE OF revision ON conversation_items FOR EACH ROW EXECUTE FUNCTION advance_channel_execution_revision();
CREATE TRIGGER discussion_item_shape BEFORE INSERT OR UPDATE ON conversation_items FOR EACH ROW EXECUTE FUNCTION check_discussion_item_shape();
CREATE TRIGGER discussion_collaboration_shape BEFORE INSERT OR UPDATE ON conversation_message_pins FOR EACH ROW EXECUTE FUNCTION check_discussion_collaboration_shape();
CREATE TRIGGER discussion_collaboration_shape BEFORE INSERT OR UPDATE ON conversation_message_reactions FOR EACH ROW EXECUTE FUNCTION check_discussion_collaboration_shape();
CREATE TRIGGER discussion_collaboration_shape BEFORE INSERT OR UPDATE ON conversation_thread_states FOR EACH ROW EXECUTE FUNCTION check_discussion_collaboration_shape();
