-- Runs: runs, outputs, deliverables, assets and reviews.

CREATE TABLE attachments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    uploaded_by_user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    object_key text GENERATED ALWAYS AS (((('companies/' || (company_id)::text) || '/attachments/') || (id)::text)) STORED NOT NULL,
    filename text NOT NULL,
    content_type text NOT NULL,
    size_bytes bigint NOT NULL,
    status text DEFAULT 'pending' NOT NULL,
    upload_lease_expires_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT attachments_pkey PRIMARY KEY (id),
    CONSTRAINT attachments_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT attachments_create_request_key UNIQUE (company_id, uploaded_by_user_id, request_key),
    CONSTRAINT attachments_object_key_key UNIQUE (object_key),
    CONSTRAINT attachments_status_check CHECK (status IN ('pending', 'ready', 'deleting', 'deleted')),
    CONSTRAINT attachments_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);
CREATE INDEX attachments_deleting_retry_cleanup_idx ON attachments (updated_at, id) WHERE (status = 'deleting');
CREATE INDEX attachments_pending_expiry_cleanup_idx ON attachments (upload_lease_expires_at, id) WHERE (status = 'pending');
CREATE INDEX attachments_ready_abandonment_cleanup_idx ON attachments (updated_at, id) WHERE (status = 'ready');

CREATE TABLE runs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    task_id uuid,
    conversation_id uuid NOT NULL,
    runtime_id uuid NOT NULL,
    status text DEFAULT 'queued' NOT NULL,
    priority integer DEFAULT 0 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    error text,
    failure_reason text,
    context jsonb,
    session_id text,
    session_rollout_missing boolean DEFAULT false NOT NULL,
    retired_session_id text,
    attempt integer DEFAULT 1 NOT NULL,
    parent_run_id uuid,
    force_fresh_session boolean DEFAULT false NOT NULL,
    wait_reason text,
    prepare_lease_expires_at timestamptz,
    fire_at timestamptz,
    task_execution_generation bigint,
    schedule_firing_id uuid,
    transcript_through_seq bigint DEFAULT 0 NOT NULL,
    run_type text NOT NULL,
    revision bigint,
    execution_authorized_by_user_id uuid,
    invocation_id uuid,
    source_task_id uuid,
    final_answer_item_key text,
    studio_enabled boolean DEFAULT false NOT NULL,
    connector_selection_mode text DEFAULT 'all' NOT NULL,
    connector_snapshot_frozen_at timestamptz,
    queued_at timestamptz,
    claimed_at timestamptz,
    started_at timestamptz,
    finished_at timestamptz,
    task_scheduled boolean DEFAULT false NOT NULL,
    CONSTRAINT runs_pkey PRIMARY KEY (id),
    CONSTRAINT runs_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT runs_connector_selection_mode_check CHECK (connector_selection_mode IN ('all', 'exact')),
    CONSTRAINT runs_revision_check CHECK ((revision IS NULL) OR ((revision >= 1) AND (revision <= '9007199254740991'::bigint))),
    CONSTRAINT runs_run_type_check CHECK (run_type IN ('task', 'direct_chat', 'schedule', 'channel_agent')),
    CONSTRAINT runs_schedule_connector_selection_check CHECK ((schedule_firing_id IS NULL) OR (connector_selection_mode = 'exact')),
    CONSTRAINT runs_status_check CHECK (status IN ('queued', 'dispatched', 'running', 'completed', 'failed', 'cancelled', 'waiting_local_directory', 'deferred')),
    CONSTRAINT runs_company_agent_fkey FOREIGN KEY (company_id, agent_id) REFERENCES agents(company_id, id) ON DELETE CASCADE,
    CONSTRAINT runs_company_conversation_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT runs_company_runtime_fkey FOREIGN KEY (company_id, runtime_id) REFERENCES runtimes(company_id, id) ON DELETE CASCADE,
    CONSTRAINT runs_company_task_fkey FOREIGN KEY (company_id, task_id) REFERENCES tasks(company_id, id) ON DELETE SET NULL (task_id),
    CONSTRAINT runs_invocation_fkey FOREIGN KEY (company_id, invocation_id) REFERENCES channel_invocations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT runs_schedule_firing_fkey FOREIGN KEY (company_id, schedule_firing_id) REFERENCES schedule_firings(company_id, id) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX runs_agent_status_idx ON runs (agent_id, status);
CREATE INDEX runs_claim_candidates_idx ON runs (agent_id, runtime_id, priority DESC, created_at, id) WHERE (status = 'queued');
CREATE INDEX runs_company_authorizer_finished_idx ON runs (company_id, execution_authorized_by_user_id, finished_at) WHERE (finished_at IS NOT NULL);
CREATE INDEX runs_company_created_idx ON runs (company_id, created_at DESC, id DESC);
CREATE INDEX runs_company_finished_idx ON runs (company_id, finished_at) WHERE (finished_at IS NOT NULL);
CREATE INDEX runs_company_live_idx ON runs (company_id, status) WHERE (status IN ('queued', 'dispatched', 'waiting_local_directory', 'running'));
CREATE INDEX runs_conversation_created_idx ON runs (conversation_id, created_at DESC, id DESC);
CREATE INDEX runs_conversation_live_idx ON runs (conversation_id, status) WHERE (status IN ('queued', 'deferred', 'dispatched', 'waiting_local_directory', 'running'));
CREATE INDEX runs_deferred_fire_idx ON runs (runtime_id, fire_at) WHERE (status = 'deferred');
CREATE INDEX runs_dispatched_prepare_idx ON runs (runtime_id, priority DESC, updated_at) WHERE (status = 'dispatched');
CREATE INDEX runs_finished_idx ON runs (finished_at DESC) WHERE (finished_at IS NOT NULL);
CREATE INDEX runs_invocation_idx ON runs (invocation_id, created_at DESC) WHERE (invocation_id IS NOT NULL);
CREATE UNIQUE INDEX runs_one_pending_per_task_agent_idx ON runs (task_id, agent_id) WHERE (status IN ('queued', 'deferred', 'dispatched'));
CREATE INDEX runs_parent_retry_idx ON runs (parent_run_id) WHERE (parent_run_id IS NOT NULL);
CREATE INDEX runs_runtime_keyset_idx ON runs (runtime_id, id);
CREATE INDEX runs_schedule_firing_idx ON runs (schedule_firing_id, attempt DESC) WHERE (schedule_firing_id IS NOT NULL);
CREATE INDEX runs_source_task_created_idx ON runs (source_task_id, created_at DESC, id DESC) WHERE (source_task_id IS NOT NULL);
CREATE INDEX runs_task_calendar_active_idx ON runs (company_id, task_id) WHERE ((task_id IS NOT NULL) AND (status <> ALL (ARRAY['completed', 'failed', 'cancelled'])));
CREATE INDEX runs_task_created_idx ON runs (task_id, created_at DESC) WHERE (task_id IS NOT NULL);

CREATE TABLE run_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT run_tokens_pkey PRIMARY KEY (id),
    CONSTRAINT run_tokens_token_hash_key UNIQUE (token_hash)
);
CREATE INDEX run_tokens_expiry_idx ON run_tokens (expires_at);
CREATE INDEX run_tokens_run_idx ON run_tokens (run_id);

CREATE TABLE run_usage (
    run_id uuid NOT NULL,
    provider text NOT NULL,
    model text NOT NULL,
    input_tokens bigint DEFAULT 0 NOT NULL,
    output_tokens bigint DEFAULT 0 NOT NULL,
    cache_read_tokens bigint DEFAULT 0 NOT NULL,
    cache_write_tokens bigint DEFAULT 0 NOT NULL,
    provider_cost_usd numeric(30,12),
    raw_json jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    usage_basis text DEFAULT 'per_run' NOT NULL,
    reasoning_tokens bigint,
    turns integer,
    provider_duration_ms bigint,
    billing_mode text DEFAULT 'unknown' NOT NULL,
    CONSTRAINT run_usage_pkey PRIMARY KEY (run_id, provider, model),
    CONSTRAINT run_usage_billing_mode_check CHECK (billing_mode IN ('api', 'subscription', 'unknown')),
    CONSTRAINT run_usage_provider_check CHECK (provider IN ('claude', 'codex')),
    CONSTRAINT run_usage_usage_basis_check CHECK (usage_basis IN ('per_run', 'session_cumulative'))
);

CREATE TABLE assets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    title text NOT NULL,
    media_class text NOT NULL,
    status text DEFAULT 'active' NOT NULL,
    created_by_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT assets_pkey PRIMARY KEY (id),
    CONSTRAINT assets_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT assets_status_check CHECK (status IN ('active', 'archived')),
    CONSTRAINT assets_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);
CREATE INDEX assets_company_created_idx ON assets (company_id, created_at DESC, id DESC);

CREATE TABLE asset_versions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    version_number integer NOT NULL,
    attachment_id uuid NOT NULL,
    source_run_id uuid,
    filename text NOT NULL,
    content_type text NOT NULL,
    size_bytes bigint NOT NULL,
    integrity_status text NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    source_type text DEFAULT 'run' NOT NULL,
    created_by_user_id uuid,
    CONSTRAINT asset_versions_pkey PRIMARY KEY (id),
    CONSTRAINT asset_versions_asset_version_key UNIQUE (asset_id, version_number),
    CONSTRAINT asset_versions_attachment_key UNIQUE (attachment_id),
    CONSTRAINT asset_versions_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT asset_versions_integrity_status_check CHECK (integrity_status = 'verified'),
    CONSTRAINT asset_versions_origin_check CHECK (((source_type = 'run') AND (source_run_id IS NOT NULL)) OR ((source_type = 'human') AND (source_run_id IS NULL) AND (created_by_user_id IS NOT NULL))),
    CONSTRAINT asset_versions_source_type_check CHECK (source_type IN ('run', 'human')),
    CONSTRAINT asset_versions_asset_fkey FOREIGN KEY (company_id, asset_id) REFERENCES assets(company_id, id) ON DELETE CASCADE,
    CONSTRAINT asset_versions_attachment_fkey FOREIGN KEY (company_id, attachment_id) REFERENCES attachments(company_id, id) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX asset_versions_company_asset_version_idx ON asset_versions (company_id, asset_id, version_number DESC);
CREATE INDEX asset_versions_source_run_idx ON asset_versions (company_id, source_run_id);

CREATE TABLE outputs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    run_id uuid,
    title text NOT NULL,
    summary text NOT NULL,
    submission_notes text,
    presentation_fingerprint text NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    type text DEFAULT 'file' NOT NULL,
    preview_url text,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    presentation_number bigint DEFAULT 1 NOT NULL,
    owner_user_id uuid,
    access_version bigint DEFAULT 1 NOT NULL,
    visibility text DEFAULT 'private' NOT NULL,
    public_token uuid,
    conversation_id uuid,
    task_id uuid,
    project_id uuid,
    task_execution_generation bigint,
    actor_type text DEFAULT 'agent' NOT NULL,
    actor_user_id uuid,
    actor_agent_id uuid,
    source_run_id uuid,
    on_behalf_of_user_id uuid,
    source_run_completed boolean DEFAULT false NOT NULL,
    CONSTRAINT outputs_pkey PRIMARY KEY (id),
    CONSTRAINT outputs_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT outputs_public_token_key UNIQUE (public_token),
    CONSTRAINT outputs_run_presentation_fingerprint_key UNIQUE (run_id, presentation_fingerprint),
    CONSTRAINT outputs_run_presentation_number_key UNIQUE (run_id, presentation_number),
    CONSTRAINT outputs_actor_check CHECK (((actor_type = 'user') AND (actor_user_id IS NOT NULL) AND (run_id IS NULL)) OR ((actor_type = 'agent') AND (actor_agent_id IS NOT NULL))),
    CONSTRAINT outputs_actor_type_check CHECK (actor_type IN ('user', 'agent')),
    CONSTRAINT outputs_payload_object_check CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT outputs_presentation_number_check CHECK ((presentation_number >= 1) AND (presentation_number <= '9007199254740991'::bigint)),
    CONSTRAINT outputs_preview_url_type_check CHECK ((preview_url IS NULL) OR (type = 'app')),
    CONSTRAINT outputs_type_check CHECK (type IN ('app', 'media', 'text', 'file', 'whiteboard')),
    CONSTRAINT outputs_visibility_check CHECK (visibility IN ('private', 'shared', 'company', 'public')),
    CONSTRAINT outputs_company_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT outputs_conversation_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE SET NULL (conversation_id),
    CONSTRAINT outputs_project_fkey FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id),
    CONSTRAINT outputs_run_fkey FOREIGN KEY (company_id, run_id) REFERENCES runs(company_id, id) ON DELETE SET NULL (run_id),
    CONSTRAINT outputs_task_fkey FOREIGN KEY (company_id, task_id) REFERENCES tasks(company_id, id) ON DELETE SET NULL (task_id)
);
CREATE INDEX outputs_company_created_idx ON outputs (company_id, created_at DESC, id DESC);
CREATE INDEX outputs_company_visible_created_idx ON outputs (company_id, created_at DESC, id DESC) WHERE (visibility = 'company');
CREATE INDEX outputs_conversation_created ON outputs (company_id, conversation_id, created_at DESC, id DESC) WHERE (conversation_id IS NOT NULL);
CREATE INDEX outputs_owner_created_idx ON outputs (company_id, owner_user_id, created_at DESC, id DESC) WHERE (owner_user_id IS NOT NULL);
CREATE INDEX outputs_task_created ON outputs (company_id, task_id, created_at DESC, id DESC) WHERE (task_id IS NOT NULL);

CREATE TABLE output_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    output_id uuid NOT NULL,
    ordinal integer NOT NULL,
    role text NOT NULL,
    label text NOT NULL,
    asset_version_id uuid NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT output_items_pkey PRIMARY KEY (id),
    CONSTRAINT output_items_output_id_id_key UNIQUE (output_id, id),
    CONSTRAINT output_items_output_ordinal_key UNIQUE (output_id, ordinal),
    CONSTRAINT output_items_role_check CHECK (role IN ('primary', 'final', 'supporting')),
    CONSTRAINT output_items_asset_version_fkey FOREIGN KEY (company_id, asset_version_id) REFERENCES asset_versions(company_id, id) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT output_items_output_fkey FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE
);
CREATE INDEX output_items_asset_version_idx ON output_items (asset_version_id);
CREATE UNIQUE INDEX output_items_one_primary_idx ON output_items (output_id) WHERE (role = 'primary');

CREATE TABLE deliverables (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    output_id uuid NOT NULL,
    status text DEFAULT 'ready_for_review' NOT NULL,
    submitted_at timestamptz NOT NULL,
    reviewed_by_user_id uuid,
    reviewed_at timestamptz,
    review_feedback text,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT deliverables_pkey PRIMARY KEY (id),
    CONSTRAINT deliverables_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT deliverables_output_id_key UNIQUE (output_id),
    CONSTRAINT deliverables_status_check CHECK (status IN ('ready_for_review', 'approved', 'changes_requested', 'superseded')),
    CONSTRAINT deliverables_output_fkey FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE
);
CREATE INDEX deliverables_company_status_submitted_idx ON deliverables (company_id, status, submitted_at DESC, id DESC);

CREATE TABLE output_publications (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    output_id uuid NOT NULL,
    output_item_id uuid NOT NULL,
    requested_by_user_id uuid NOT NULL,
    status text DEFAULT 'queued' NOT NULL,
    service_name text NOT NULL,
    provider_type text NOT NULL,
    provider_binding_id text NOT NULL,
    provider_resources_may_exist boolean DEFAULT false NOT NULL,
    source_ref text,
    source_template text,
    build_ref text,
    artifact_ref text,
    artifact_digest text,
    service_ref text,
    public_url text,
    failure_code text,
    failure_detail text,
    attempt_count integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    lease_id uuid,
    lease_expires_at timestamptz,
    published_at timestamptz,
    unpublished_at timestamptz,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    custom_hostname text,
    route_binding_id text,
    route_pending boolean DEFAULT false NOT NULL,
    CONSTRAINT output_publications_pkey PRIMARY KEY (id),
    CONSTRAINT output_publications_custom_hostname_key UNIQUE (custom_hostname),
    CONSTRAINT output_publications_output_key UNIQUE (output_id),
    CONSTRAINT output_publications_failure_code_check CHECK ((failure_code IS NULL) OR (failure_code IN ('source_invalid', 'build_failed', 'deploy_failed', 'provider_unavailable'))),
    CONSTRAINT output_publications_route_check CHECK (((custom_hostname IS NULL) AND (route_binding_id IS NULL) AND (route_pending = false)) OR ((custom_hostname IS NOT NULL) AND (route_binding_id IS NOT NULL))),
    CONSTRAINT output_publications_route_pending_check CHECK (((NOT route_pending) OR ((public_url IS NOT NULL) AND (service_ref IS NOT NULL))) AND ((status <> 'routing') OR route_pending)),
    CONSTRAINT output_publications_status_check CHECK (status IN ('queued', 'uploading', 'building', 'deploying', 'routing', 'published', 'unpublishing', 'unpublished', 'failed')),
    CONSTRAINT output_publications_output_fkey FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE,
    CONSTRAINT output_publications_output_item_fkey FOREIGN KEY (output_id, output_item_id) REFERENCES output_items(output_id, id) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX output_publications_company_created_idx ON output_publications (company_id, created_at DESC, id DESC);
CREATE INDEX output_publications_work_idx ON output_publications (next_attempt_at, created_at, id) WHERE (status IN ('queued', 'uploading', 'building', 'deploying', 'routing', 'unpublishing'));

CREATE TABLE output_studio_canvases (
    output_id uuid NOT NULL,
    company_id uuid NOT NULL,
    canvas jsonb NOT NULL,
    added_by_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT output_studio_canvases_pkey PRIMARY KEY (output_id),
    CONSTRAINT output_studio_canvases_canvas_check CHECK ((jsonb_typeof(canvas) = 'object') AND (octet_length((canvas)::text) <= 2097152)),
    CONSTRAINT output_studio_canvases_company_id_output_id_fkey FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE
);

CREATE TABLE output_grants (
    company_id uuid NOT NULL,
    output_id uuid NOT NULL,
    user_id uuid NOT NULL,
    permission text NOT NULL,
    granted_by_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT output_grants_pkey PRIMARY KEY (company_id, output_id, user_id),
    CONSTRAINT output_grants_permission_check CHECK (permission IN ('view', 'edit', 'manage')),
    CONSTRAINT output_grants_company_id_output_id_fkey FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE,
    CONSTRAINT output_grants_company_id_user_id_fkey FOREIGN KEY (company_id, user_id) REFERENCES company_members(company_id, user_id) ON DELETE CASCADE
);
CREATE INDEX output_grants_recipient_idx ON output_grants (company_id, user_id, output_id);

CREATE TABLE output_conversation_grants (
    company_id uuid NOT NULL,
    output_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    granted_by_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT output_conversation_grants_pkey PRIMARY KEY (company_id, output_id, conversation_id),
    CONSTRAINT output_conversation_grants_company_id_conversation_id_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT output_conversation_grants_company_id_output_id_fkey FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE
);
CREATE INDEX output_conversation_grants_conversation_idx ON output_conversation_grants (company_id, conversation_id, output_id);

CREATE TABLE run_connector_selections (
    run_id uuid NOT NULL,
    connector_id uuid NOT NULL,
    policy_revision bigint NOT NULL,
    source text NOT NULL,
    native_id text NOT NULL,
    contract_digest text NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT run_connector_selections_pkey PRIMARY KEY (run_id, connector_id),
    CONSTRAINT run_connector_selections_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE run_connector_snapshots (
    run_id uuid NOT NULL,
    connector_id uuid NOT NULL,
    policy_revision bigint NOT NULL,
    source text NOT NULL,
    native_id text NOT NULL,
    contract_digest text NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT run_connector_snapshots_pkey PRIMARY KEY (run_id, connector_id),
    CONSTRAINT run_connector_snapshots_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE run_wiki_reads (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    run_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    kind text NOT NULL,
    query text DEFAULT '' NOT NULL,
    result_count integer,
    page_id uuid,
    revision_id uuid,
    revision_number integer,
    title text DEFAULT '' NOT NULL,
    char_offset integer DEFAULT 0 NOT NULL,
    chars_returned integer DEFAULT 0 NOT NULL,
    truncated boolean DEFAULT false NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT run_wiki_reads_pkey PRIMARY KEY (id),
    CONSTRAINT run_wiki_reads_kind_check CHECK (kind IN ('search', 'read', 'list')),
    CONSTRAINT run_wiki_reads_shape_check CHECK (((kind = 'read') AND (page_id IS NOT NULL) AND (revision_id IS NOT NULL) AND (revision_number IS NOT NULL) AND (result_count IS NULL)) OR ((kind IN ('search', 'list')) AND (page_id IS NULL) AND (revision_id IS NULL) AND (revision_number IS NULL) AND (result_count IS NOT NULL))),
    CONSTRAINT run_wiki_reads_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX run_wiki_reads_page_idx ON run_wiki_reads (company_id, page_id, created_at DESC) WHERE (kind = 'read');
CREATE INDEX run_wiki_reads_revision_idx ON run_wiki_reads (revision_id) WHERE (revision_id IS NOT NULL);
CREATE INDEX run_wiki_reads_run_idx ON run_wiki_reads (run_id, created_at, id);

CREATE TABLE review_threads (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    project_id uuid,
    artifact_version_id uuid,
    anchor jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'open' NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_by_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    resolved_by_user_id uuid,
    resolved_at timestamptz,
    page_id uuid,
    output_id uuid,
    document_revision_id uuid,
    CONSTRAINT review_threads_pkey PRIMARY KEY (id),
    CONSTRAINT review_threads_company_id_project_id_id_key UNIQUE (company_id, project_id, id),
    CONSTRAINT review_threads_company_key UNIQUE (company_id, id),
    CONSTRAINT review_threads_anchor_check CHECK (jsonb_typeof(anchor) = 'object'),
    CONSTRAINT review_threads_document_subject_check CHECK ((document_revision_id IS NULL) OR ((output_id IS NOT NULL) AND (page_id IS NULL))),
    CONSTRAINT review_threads_resolution_check CHECK (((status = 'open') AND (resolved_at IS NULL) AND (resolved_by_user_id IS NULL)) OR ((status = 'resolved') AND (resolved_at IS NOT NULL) AND (resolved_by_user_id IS NOT NULL))),
    CONSTRAINT review_threads_status_check CHECK (status IN ('open', 'resolved')),
    CONSTRAINT review_threads_subject_check CHECK (((project_id IS NOT NULL) AND (artifact_version_id IS NOT NULL) AND (output_id IS NULL)) OR ((project_id IS NULL) AND (artifact_version_id IS NULL) AND (output_id IS NOT NULL))),
    CONSTRAINT review_threads_company_id_project_id_artifact_version_id_fkey FOREIGN KEY (company_id, project_id, artifact_version_id) REFERENCES project_artifact_versions(company_id, project_id, id) ON DELETE CASCADE,
    CONSTRAINT review_threads_output_fkey FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE
);
CREATE INDEX review_threads_output ON review_threads (output_id, created_at, id) WHERE (output_id IS NOT NULL);
CREATE INDEX review_threads_page ON review_threads (page_id, created_at, id) WHERE (page_id IS NOT NULL);
CREATE INDEX review_threads_version ON review_threads (artifact_version_id, created_at, id);

CREATE TABLE review_comments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    project_id uuid,
    thread_id uuid NOT NULL,
    body text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    author_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    edited_at timestamptz,
    deleted_at timestamptz,
    author_agent_id uuid,
    agent_run_id uuid,
    CONSTRAINT review_comments_pkey PRIMARY KEY (id),
    CONSTRAINT review_comments_company_id_project_id_id_key UNIQUE (company_id, project_id, id),
    CONSTRAINT review_comments_agent_run_id_fkey FOREIGN KEY (agent_run_id) REFERENCES runs(id) ON DELETE SET NULL,
    CONSTRAINT review_comments_company_id_project_id_thread_id_fkey FOREIGN KEY (company_id, project_id, thread_id) REFERENCES review_threads(company_id, project_id, id) ON DELETE CASCADE,
    CONSTRAINT review_comments_subject_fkey FOREIGN KEY (company_id, thread_id) REFERENCES review_threads(company_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX review_comments_agent_run_unique ON review_comments (agent_run_id) WHERE (agent_run_id IS NOT NULL);
CREATE INDEX review_comments_thread ON review_comments (thread_id, created_at, id);

CREATE TABLE review_comment_revisions (
    comment_id uuid NOT NULL,
    revision bigint NOT NULL,
    body text NOT NULL,
    author_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT review_comment_revisions_pkey PRIMARY KEY (comment_id, revision),
    CONSTRAINT review_comment_revisions_comment_id_fkey FOREIGN KEY (comment_id) REFERENCES review_comments(id) ON DELETE CASCADE
);

CREATE TABLE review_thread_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    thread_id uuid NOT NULL,
    action text NOT NULL,
    actor_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT review_thread_events_pkey PRIMARY KEY (id),
    CONSTRAINT review_thread_events_action_check CHECK (action IN ('resolved', 'reopened')),
    CONSTRAINT review_thread_events_thread_id_fkey FOREIGN KEY (thread_id) REFERENCES review_threads(id) ON DELETE CASCADE
);

CREATE TABLE artifact_review_decisions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    artifact_id uuid NOT NULL,
    version_id uuid NOT NULL,
    action text NOT NULL,
    feedback text DEFAULT '' NOT NULL,
    actor_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT artifact_review_decisions_pkey PRIMARY KEY (id),
    CONSTRAINT artifact_review_decisions_action_check CHECK (action IN ('accept', 'reject', 'request_changes', 'keep_variant')),
    CONSTRAINT artifact_review_decisions_company_id_project_id_artifact_i_fkey FOREIGN KEY (company_id, project_id, artifact_id, version_id) REFERENCES project_artifact_versions(company_id, project_id, artifact_id, id) ON DELETE CASCADE
);
CREATE INDEX artifact_review_decisions_project_time ON artifact_review_decisions (project_id, created_at DESC, id DESC);
CREATE INDEX artifact_review_decisions_version_time ON artifact_review_decisions (version_id, created_at DESC, id DESC);

CREATE TABLE run_skills (
    company_id uuid NOT NULL,
    run_id uuid NOT NULL,
    version_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT run_skills_pkey PRIMARY KEY (run_id, version_id),
    CONSTRAINT run_skills_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT run_skills_company_id_run_id_fkey FOREIGN KEY (company_id, run_id) REFERENCES runs(company_id, id) ON DELETE CASCADE,
    CONSTRAINT run_skills_company_id_version_id_fkey FOREIGN KEY (company_id, version_id) REFERENCES skill_versions(company_id, id)
);
CREATE INDEX run_skills_version ON run_skills (version_id);

CREATE TABLE output_review_operations (
    company_id uuid NOT NULL,
    output_id uuid NOT NULL,
    actor_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    request_key uuid NOT NULL,
    fingerprint bytea NOT NULL,
    result_id uuid NOT NULL,
    CONSTRAINT output_review_operations_pkey PRIMARY KEY (output_id, actor_user_id, request_key),
    CONSTRAINT output_review_operations_company_id_output_id_fkey FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE
);

CREATE TABLE output_documents (
    company_id uuid NOT NULL,
    output_id uuid NOT NULL,
    output_item_id uuid NOT NULL,
    page_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    submitted_sequence bigint DEFAULT 0 NOT NULL,
    submitted_content jsonb,
    CONSTRAINT output_documents_pkey PRIMARY KEY (output_item_id),
    CONSTRAINT output_documents_submitted_sequence_check CHECK (submitted_sequence >= 0),
    CONSTRAINT output_documents_company_id_output_id_fkey FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE,
    CONSTRAINT output_documents_output_id_output_item_id_fkey FOREIGN KEY (output_id, output_item_id) REFERENCES output_items(output_id, id) ON DELETE CASCADE
);
CREATE INDEX output_documents_page_idx ON output_documents (company_id, page_id);

CREATE TABLE run_wiki_writes (
    run_id uuid NOT NULL,
    company_id uuid NOT NULL,
    key text NOT NULL,
    page_id uuid NOT NULL,
    source_markdown text DEFAULT '' NOT NULL,
    last_revision_id uuid,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    last_draft_seq bigint,
    CONSTRAINT run_wiki_writes_pkey PRIMARY KEY (run_id, key),
    CONSTRAINT run_wiki_writes_page_key UNIQUE (page_id),
    CONSTRAINT run_wiki_writes_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX run_wiki_writes_company_created_idx ON run_wiki_writes (company_id, created_at, key);

-- Links to these tables from tables created earlier.
ALTER TABLE companies ADD CONSTRAINT companies_avatar_attachment_fkey FOREIGN KEY (id, avatar_attachment_id) REFERENCES attachments(company_id, id) ON DELETE SET NULL (avatar_attachment_id);
ALTER TABLE company_reference_documents ADD CONSTRAINT company_reference_documents_attachment_fkey FOREIGN KEY (company_id, attachment_id) REFERENCES attachments(company_id, id) ON DELETE CASCADE;
ALTER TABLE company_initializations ADD CONSTRAINT company_initializations_company_id_research_run_id_fkey FOREIGN KEY (company_id, research_run_id) REFERENCES runs(company_id, id) ON DELETE SET NULL (research_run_id);
ALTER TABLE approvals ADD CONSTRAINT approvals_origin_run_fkey FOREIGN KEY (company_id, origin_run_id) REFERENCES runs(company_id, id) ON DELETE CASCADE;
ALTER TABLE questions ADD CONSTRAINT questions_origin_run_fkey FOREIGN KEY (company_id, origin_run_id) REFERENCES runs(company_id, id) ON DELETE CASCADE;
ALTER TABLE schedule_firings ADD CONSTRAINT schedule_firings_run_fkey FOREIGN KEY (company_id, run_id) REFERENCES runs(company_id, id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE task_report_batches ADD CONSTRAINT task_report_batches_run_fkey FOREIGN KEY (company_id, run_id) REFERENCES runs(company_id, id);
ALTER TABLE calendar_tool_receipts ADD CONSTRAINT calendar_tool_receipts_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE project_files ADD CONSTRAINT project_files_company_id_attachment_id_fkey FOREIGN KEY (company_id, attachment_id) REFERENCES attachments(company_id, id) ON DELETE CASCADE;
ALTER TABLE project_file_assets ADD CONSTRAINT project_file_assets_company_id_asset_id_fkey FOREIGN KEY (company_id, asset_id) REFERENCES assets(company_id, id) ON DELETE CASCADE;
ALTER TABLE task_merge_jobs ADD CONSTRAINT task_merge_jobs_run_fkey FOREIGN KEY (company_id, run_id) REFERENCES runs(company_id, id) ON DELETE CASCADE;
ALTER TABLE task_result_items ADD CONSTRAINT task_result_items_company_id_output_id_fkey FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id);
ALTER TABLE project_artifact_versions ADD CONSTRAINT project_artifact_versions_company_id_asset_version_id_fkey FOREIGN KEY (company_id, asset_version_id) REFERENCES asset_versions(company_id, id) ON DELETE SET NULL (asset_version_id);
ALTER TABLE project_artifact_versions ADD CONSTRAINT project_artifact_versions_company_id_output_id_fkey FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE SET NULL (output_id);
ALTER TABLE project_presentations ADD CONSTRAINT project_presentations_company_id_export_attachment_id_fkey FOREIGN KEY (company_id, export_attachment_id) REFERENCES attachments(company_id, id);
ALTER TABLE project_presentations ADD CONSTRAINT project_presentations_company_id_output_id_fkey FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE SET NULL (output_id);
ALTER TABLE project_presentation_sources ADD CONSTRAINT project_presentation_sources_company_id_asset_version_id_fkey FOREIGN KEY (company_id, asset_version_id) REFERENCES asset_versions(company_id, id);
ALTER TABLE project_presentation_sources ADD CONSTRAINT project_presentation_sources_company_id_output_id_fkey FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id);
ALTER TABLE conversations ADD CONSTRAINT conversations_document_thread_fkey FOREIGN KEY (company_id, document_thread_id) REFERENCES review_threads(company_id, id) ON DELETE CASCADE;
ALTER TABLE conversation_run_queue_attachments ADD CONSTRAINT conversation_run_queue_attachments_attachment_fkey FOREIGN KEY (company_id, attachment_id) REFERENCES attachments(company_id, id) ON DELETE CASCADE;
ALTER TABLE message_attachments ADD CONSTRAINT message_attachments_attachment_fkey FOREIGN KEY (company_id, attachment_id) REFERENCES attachments(company_id, id) ON DELETE CASCADE;
ALTER TABLE discussion_artifacts ADD CONSTRAINT discussion_artifacts_company_id_output_id_fkey FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE;
ALTER TABLE huddle_work_proposals ADD CONSTRAINT huddle_work_proposals_company_id_submitted_run_id_fkey FOREIGN KEY (company_id, submitted_run_id) REFERENCES runs(company_id, id) ON DELETE SET NULL (submitted_run_id);

-- The same rule for one Run, from its own row: its Authorizer and the chat
-- it answers into. A Run that is not in the Company, or has no Authorizer,
-- may do nothing.
CREATE FUNCTION run_permission_held(
    company_id uuid,
    run_id uuid,
    project_id uuid,
    permission text,
    read_permission text,
    company_default_permissions text[]
) RETURNS boolean
LANGUAGE sql STABLE
AS $$
SELECT COALESCE((
    SELECT delegate_permission_held(
        run.company_id, run.execution_authorized_by_user_id, run.conversation_id,
        $3, $4, $5, $6)
    FROM runs AS run
    WHERE run.company_id = $1 AND run.id = $2
), false)
$$;

CREATE FUNCTION reject_discussion_run() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS(SELECT 1 FROM conversations WHERE id=NEW.conversation_id AND kind IN ('human_direct','channel','huddle')) THEN
        RAISE EXCEPTION 'discussion cannot own Runs' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;

-- output_access: who may open an Artifact, and at which level (1 view, 2 edit,
-- 3 manage). The union of current grants is evaluated on every protected
-- read. A Company-visible Artifact from a restricted Project's Task chat
-- reaches only that Project's readers, directly or through a Group. A public
-- token is a separate anonymous entry point, not company-wide discovery. An
-- aggregate export keeps the access of every source it flattens.
CREATE VIEW output_access AS
WITH direct_access AS NOT MATERIALIZED (
SELECT output.company_id, output.id AS output_id, member.user_id,
       CASE WHEN output.owner_user_id = member.user_id OR grant_row.permission = 'manage'
            THEN 3 WHEN grant_row.permission = 'edit' THEN 2 ELSE 1 END::integer AS access_level
FROM outputs AS output
JOIN company_members AS member
  ON member.company_id = output.company_id AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
JOIN companies AS company ON company.id = output.company_id AND company.deleting_at IS NULL
LEFT JOIN output_grants AS grant_row
  ON grant_row.company_id = output.company_id AND grant_row.output_id = output.id
 AND grant_row.user_id = member.user_id
 AND grant_row.updated_at >= COALESCE(member.reactivated_at, member.created_at)
LEFT JOIN projects AS source_project
  ON source_project.company_id = output.company_id
 AND source_project.id = output.project_id
WHERE output.owner_user_id = member.user_id OR grant_row.permission IS NOT NULL
   OR (output.visibility = 'company' AND (
       output.project_id IS NULL
       OR source_project.visibility = 'company'
       OR EXISTS (
           SELECT 1 FROM project_grants AS project_grant
           WHERE project_grant.company_id = source_project.company_id
             AND project_grant.project_id = source_project.id
             AND project_grant.user_id = member.user_id
             AND project_grant.permission = project_grant.read_permission
             AND project_grant.updated_at >= COALESCE(member.reactivated_at, member.created_at)
       )
       OR EXISTS (
           SELECT 1 FROM company_group_members AS group_member
           JOIN project_group_grants AS group_grant
             ON group_grant.company_id = group_member.company_id
            AND group_grant.project_id = source_project.id
            AND group_grant.group_id = group_member.group_id
            AND group_grant.permission = group_grant.read_permission
           WHERE group_member.company_id = source_project.company_id
             AND group_member.user_id = member.user_id
             AND group_member.created_at >= COALESCE(member.reactivated_at, member.created_at)
       )
   ))
   OR EXISTS (
       SELECT 1 FROM output_conversation_grants AS conversation_grant
       JOIN conversation_members AS participant
         ON participant.company_id = conversation_grant.company_id
        AND participant.conversation_id = conversation_grant.conversation_id
        AND participant.user_id = member.user_id AND participant.status = 'active'
       JOIN conversations AS conversation
         ON conversation.id = conversation_grant.conversation_id
        AND conversation.company_id = conversation_grant.company_id
        AND conversation.kind IN ('channel', 'human_direct')
       WHERE conversation_grant.company_id = output.company_id
         AND conversation_grant.output_id = output.id
   )
)
SELECT access.company_id,access.output_id,access.user_id,access.access_level
FROM direct_access access
WHERE NOT EXISTS (
 SELECT 1 FROM project_presentations presentation
 JOIN project_presentation_sources source ON source.presentation_id=presentation.id
 WHERE presentation.company_id=access.company_id AND presentation.output_id=access.output_id
 -- Sharing the result authorizes its primary snapshot, not other retained Pages.
 AND source.version_id <> presentation.page_version_id
 AND ((source.output_id IS NOT NULL
 AND NOT EXISTS (SELECT 1 FROM direct_access evidence
     WHERE evidence.company_id=source.company_id AND evidence.output_id=source.output_id AND evidence.user_id=access.user_id))
 OR (source.output_id IS NULL AND NOT EXISTS(SELECT 1 FROM project_access reader
     WHERE reader.company_id=source.company_id AND reader.project_id=source.project_id AND reader.user_id=access.user_id)))
);

CREATE VIEW deliverable_inbox_access AS
SELECT deliverable.company_id, deliverable.id::text AS entity_id, access.user_id
FROM deliverables AS deliverable
JOIN output_access AS access
  ON access.company_id = deliverable.company_id
 AND access.output_id = deliverable.output_id;

-- Both Project file representations share the same download and chat boundary.
CREATE VIEW project_attachment_scopes AS
SELECT company_id,project_id,attachment_id,removed_at FROM project_files
UNION
SELECT file.company_id,file.project_id,version.attachment_id,NULL::timestamptz AS removed_at
FROM project_file_assets file JOIN asset_versions version
ON version.company_id=file.company_id AND version.asset_id=file.asset_id;

CREATE TRIGGER discussion_run_guard BEFORE INSERT OR UPDATE OF conversation_id ON runs FOR EACH ROW EXECUTE FUNCTION reject_discussion_run();
