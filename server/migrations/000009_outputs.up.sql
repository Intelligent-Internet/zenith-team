-- 000009_outputs: immutable Run Outputs, Asset Versions, founder review,
-- and durable publication jobs. Extend existing platform tables in place.

ALTER TABLE inbox_items
    ADD CONSTRAINT inbox_items_company_id_id_key UNIQUE (company_id, id),
    DROP CONSTRAINT inbox_items_entity_type_check,
    ADD CONSTRAINT inbox_items_entity_type_check CHECK (entity_type IN (
        'company', 'user', 'agent', 'goal', 'task', 'run', 'conversation',
        'message', 'inbox', 'resource', 'runtime', 'approval', 'question',
        'deliverable'
    ));

CREATE TABLE assets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    title text NOT NULL,
    media_class text NOT NULL,
    status text DEFAULT 'active' NOT NULL,
    created_by_user_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT assets_pkey PRIMARY KEY (id),
    CONSTRAINT assets_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT assets_status_check CHECK (status IN ('active', 'archived'))
);

CREATE INDEX assets_company_created_idx
    ON assets (company_id, created_at DESC, id DESC);

CREATE TABLE asset_versions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    version_number integer NOT NULL,
    attachment_id uuid NOT NULL,
    source_run_id bigint NOT NULL,
    filename text NOT NULL,
    content_type text NOT NULL,
    size_bytes bigint NOT NULL,
    integrity_status text NOT NULL,
    created_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT asset_versions_pkey PRIMARY KEY (id),
    CONSTRAINT asset_versions_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT asset_versions_asset_version_key UNIQUE (asset_id, version_number),
    CONSTRAINT asset_versions_attachment_key UNIQUE (attachment_id),
    CONSTRAINT asset_versions_asset_fkey
        FOREIGN KEY (company_id, asset_id)
        REFERENCES assets(company_id, id) ON DELETE CASCADE,
    CONSTRAINT asset_versions_attachment_fkey
        FOREIGN KEY (company_id, attachment_id)
        REFERENCES attachments(company_id, id)
        ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT asset_versions_source_run_fkey
        FOREIGN KEY (company_id, source_run_id)
        REFERENCES runs(company_id, id) ON DELETE CASCADE,
    CONSTRAINT asset_versions_integrity_status_check CHECK (integrity_status IN ('verified'))
);

CREATE INDEX asset_versions_company_asset_version_idx
    ON asset_versions (company_id, asset_id, version_number DESC);
CREATE INDEX asset_versions_source_run_idx
    ON asset_versions (company_id, source_run_id);

CREATE TABLE outputs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    run_id bigint NOT NULL,
    title text NOT NULL,
    summary text NOT NULL,
    submission_notes text,
    presentation_fingerprint text NOT NULL,
    created_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT outputs_pkey PRIMARY KEY (id),
    CONSTRAINT outputs_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT outputs_run_id_key UNIQUE (run_id),
    CONSTRAINT outputs_run_fkey
        FOREIGN KEY (company_id, run_id)
        REFERENCES runs(company_id, id) ON DELETE CASCADE
);

CREATE INDEX outputs_company_created_idx
    ON outputs (company_id, created_at DESC, id DESC);

CREATE TABLE output_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    output_id uuid NOT NULL,
    ordinal integer NOT NULL,
    role text NOT NULL,
    label text NOT NULL,
    asset_version_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT output_items_pkey PRIMARY KEY (id),
    CONSTRAINT output_items_output_id_id_key UNIQUE (output_id, id),
    CONSTRAINT output_items_output_ordinal_key UNIQUE (output_id, ordinal),
    CONSTRAINT output_items_output_fkey
        FOREIGN KEY (company_id, output_id)
        REFERENCES outputs(company_id, id) ON DELETE CASCADE,
    CONSTRAINT output_items_asset_version_fkey
        FOREIGN KEY (company_id, asset_version_id)
        REFERENCES asset_versions(company_id, id)
        ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT output_items_role_check CHECK (role IN ('primary', 'supporting'))
);

CREATE INDEX output_items_asset_version_idx ON output_items (asset_version_id);
CREATE UNIQUE INDEX output_items_one_primary_idx
    ON output_items (output_id) WHERE role = 'primary';

CREATE TABLE deliverables (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    output_id uuid NOT NULL,
    status text DEFAULT 'ready_for_review' NOT NULL,
    submitted_at timestamp with time zone NOT NULL,
    reviewed_by_user_id uuid,
    reviewed_at timestamp with time zone,
    review_feedback text,
    created_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT deliverables_pkey PRIMARY KEY (id),
    CONSTRAINT deliverables_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT deliverables_output_id_key UNIQUE (output_id),
    CONSTRAINT deliverables_output_fkey
        FOREIGN KEY (company_id, output_id)
        REFERENCES outputs(company_id, id) ON DELETE CASCADE,
    CONSTRAINT deliverables_status_check CHECK (
        status IN ('ready_for_review', 'approved', 'changes_requested', 'superseded')
    )
);

CREATE INDEX deliverables_company_status_submitted_idx
    ON deliverables (company_id, status, submitted_at DESC, id DESC);

CREATE TABLE deliverable_review_actions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    deliverable_id uuid NOT NULL,
    inbox_item_id uuid NOT NULL,
    recipient_user_id uuid NOT NULL,
    status text DEFAULT 'open' NOT NULL,
    resolved_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT deliverable_review_actions_pkey PRIMARY KEY (id),
    CONSTRAINT deliverable_review_actions_deliverable_id_key UNIQUE (deliverable_id),
    CONSTRAINT deliverable_review_actions_inbox_item_id_key UNIQUE (inbox_item_id),
    CONSTRAINT deliverable_review_actions_deliverable_fkey
        FOREIGN KEY (company_id, deliverable_id)
        REFERENCES deliverables(company_id, id) ON DELETE CASCADE,
    CONSTRAINT deliverable_review_actions_inbox_item_fkey
        FOREIGN KEY (company_id, inbox_item_id)
        REFERENCES inbox_items(company_id, id) ON DELETE CASCADE,
    CONSTRAINT deliverable_review_actions_status_check CHECK (status IN ('open', 'resolved'))
);

CREATE INDEX deliverable_review_actions_company_status_idx
    ON deliverable_review_actions (company_id, status, created_at DESC, id DESC);

-- Owner-authorized, provider-neutral durable publication state for one
-- immutable primary ZIP item per Output. The provider key and opaque resource
-- references let cleanup resolve the exact adapter binding that created them.
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
    next_attempt_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    lease_id uuid,
    lease_expires_at timestamp with time zone,
    published_at timestamp with time zone,
    unpublished_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT output_publications_pkey PRIMARY KEY (id),
    CONSTRAINT output_publications_output_key UNIQUE (output_id),
    CONSTRAINT output_publications_output_fkey
        FOREIGN KEY (company_id, output_id)
        REFERENCES outputs(company_id, id) ON DELETE CASCADE,
    CONSTRAINT output_publications_output_item_fkey
        FOREIGN KEY (output_id, output_item_id)
        REFERENCES output_items(output_id, id)
        ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT output_publications_status_check CHECK (
        status IN (
            'queued', 'uploading', 'building', 'deploying', 'published',
            'unpublishing', 'unpublished', 'failed'
        )
    ),
    CONSTRAINT output_publications_failure_code_check CHECK (
        failure_code IS NULL OR failure_code IN (
            'source_invalid', 'build_failed', 'deploy_failed', 'provider_unavailable'
        )
    )
);

CREATE INDEX output_publications_company_created_idx
    ON output_publications (company_id, created_at DESC, id DESC);
CREATE INDEX output_publications_work_idx
    ON output_publications (next_attempt_at, created_at, id)
    WHERE status IN ('queued', 'uploading', 'building', 'deploying', 'unpublishing');
