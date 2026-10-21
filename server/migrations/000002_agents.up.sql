-- Agents: agents, runtimes, skills, plugins and connectors.

CREATE TABLE agents (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    name text NOT NULL,
    title text NOT NULL,
    instructions text DEFAULT '' NOT NULL,
    reports_to_agent_id uuid,
    model text,
    thinking_level text,
    max_concurrent_runs integer DEFAULT 3 NOT NULL,
    settings jsonb DEFAULT '{}'::jsonb NOT NULL,
    archived_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    avatar_key text,
    accent_color text,
    created_by_user_id uuid,
    disabled_plugin_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    provider text NOT NULL,
    CONSTRAINT agents_pkey PRIMARY KEY (id),
    CONSTRAINT agents_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT agents_company_id_id_provider_key UNIQUE (company_id, id, provider),
    CONSTRAINT agents_provider_check CHECK (provider IN ('claude', 'codex')),
    CONSTRAINT agents_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT agents_company_reports_to_fkey FOREIGN KEY (company_id, reports_to_agent_id) REFERENCES agents(company_id, id) ON DELETE SET NULL (reports_to_agent_id) DEFERRABLE
);
CREATE INDEX agents_company_reports_to_idx ON agents (company_id, reports_to_agent_id) WHERE (archived_at IS NULL);
CREATE UNIQUE INDEX agents_one_root_per_company_idx ON agents (company_id) WHERE ((reports_to_agent_id IS NULL) AND (archived_at IS NULL));

CREATE TABLE runtimes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    daemon_id text NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    status text DEFAULT 'offline' NOT NULL,
    device_info text DEFAULT '' NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    last_seen_at timestamptz,
    owner_user_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    unusable_reason_code text,
    unusable_reason_message text,
    custom_name text,
    removed_at timestamptz,
    provider_sign_in_state text,
    provider_sign_in_detail text,
    provider_sign_in_checked_at timestamptz,
    device_name text,
    handed_over_at timestamptz,
    CONSTRAINT runtimes_pkey PRIMARY KEY (id),
    CONSTRAINT runtimes_company_daemon_provider_owner_key UNIQUE (company_id, daemon_id, provider, owner_user_id),
    CONSTRAINT runtimes_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT runtimes_company_id_id_provider_key UNIQUE (company_id, id, provider),
    CONSTRAINT runtimes_provider_check CHECK (provider IN ('claude', 'codex')),
    CONSTRAINT runtimes_provider_sign_in_check CHECK (((provider_sign_in_state IS NULL) AND (provider_sign_in_detail IS NULL) AND (provider_sign_in_checked_at IS NULL)) OR ((provider_sign_in_state IN ('signed_in', 'signed_out', 'unknown')) AND (provider_sign_in_checked_at IS NOT NULL))),
    CONSTRAINT runtimes_status_check CHECK (status IN ('online', 'offline')),
    CONSTRAINT runtimes_unusable_reason_check CHECK (((unusable_reason_code IS NULL) AND (unusable_reason_message IS NULL)) OR ((unusable_reason_code IN ('below_minimum_version', 'not_executable')) AND (unusable_reason_message IS NOT NULL) AND (status = 'offline'))),
    CONSTRAINT runtimes_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT runtimes_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX runtimes_company_status_idx ON runtimes (company_id, status);
CREATE INDEX runtimes_last_seen_idx ON runtimes (last_seen_at);
CREATE INDEX runtimes_owner_company_idx ON runtimes (owner_user_id, company_id) WHERE (owner_user_id IS NOT NULL);

CREATE TABLE connector_catalog_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    provider text NOT NULL,
    source text NOT NULL,
    native_id text NOT NULL,
    name text NOT NULL,
    description text,
    category text,
    icon_url text,
    install_count bigint,
    status text NOT NULL,
    action text NOT NULL,
    can_execute boolean DEFAULT false NOT NULL,
    contract_digest text,
    tool_namespace jsonb,
    observed_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT connector_catalog_items_pkey PRIMARY KEY (id),
    CONSTRAINT connector_catalog_items_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT connector_catalog_items_company_provider_source_native_key UNIQUE (company_id, provider, source, native_id),
    CONSTRAINT connector_catalog_items_action_check CHECK (action IN ('install_and_auth', 'authenticate', 'manage_in_provider', 'none')),
    CONSTRAINT connector_catalog_items_category_check CHECK ((category IS NULL) OR (category IN ('Productivity', 'Creativity', 'Developer Tools', 'Business & Operations', 'Data & Analytics', 'Communication', 'Education & Research', 'Security', 'Finance', 'Healthcare', 'Travel', 'Entertainment', 'Other'))),
    CONSTRAINT connector_catalog_items_contract_check CHECK ((can_execute AND (contract_digest IS NOT NULL) AND (tool_namespace IS NOT NULL) AND (jsonb_typeof(tool_namespace) = 'array')) OR ((NOT can_execute) AND (contract_digest IS NULL) AND (tool_namespace IS NULL))),
    CONSTRAINT connector_catalog_items_provider_check CHECK (provider IN ('codex', 'claude')),
    CONSTRAINT connector_catalog_items_source_check CHECK (source IN ('codex_app', 'codex_mcp', 'claude_plugin', 'claude_mcp', 'claude_cloud')),
    CONSTRAINT connector_catalog_items_status_check CHECK (status IN ('available', 'installed_not_ready', 'needs_auth', 'connected', 'managed_in_provider', 'unavailable', 'error')),
    CONSTRAINT connector_catalog_items_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);
CREATE INDEX connector_catalog_items_company_provider_category_idx ON connector_catalog_items (company_id, provider, category) WHERE (category IS NOT NULL);
CREATE INDEX connector_catalog_items_company_provider_name_idx ON connector_catalog_items (company_id, provider, lower(name), id);
CREATE INDEX connector_catalog_items_company_provider_status_idx ON connector_catalog_items (company_id, provider, status);

CREATE TABLE connector_policies (
    connector_id uuid NOT NULL,
    company_id uuid NOT NULL,
    desired_enabled boolean DEFAULT false NOT NULL,
    reviewed_contract_digest text,
    review_required boolean DEFAULT false NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT connector_policies_pkey PRIMARY KEY (connector_id),
    CONSTRAINT connector_policies_connector_fkey FOREIGN KEY (company_id, connector_id) REFERENCES connector_catalog_items(company_id, id) ON DELETE CASCADE
);
CREATE INDEX connector_policies_company_idx ON connector_policies (company_id, connector_id);

CREATE TABLE connector_discovery_generations (
    runtime_id uuid NOT NULL,
    company_id uuid NOT NULL,
    provider text NOT NULL,
    generation uuid NOT NULL,
    manifest_digest text NOT NULL,
    observed_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT connector_discovery_generations_pkey PRIMARY KEY (runtime_id),
    CONSTRAINT connector_discovery_generations_provider_check CHECK (provider IN ('codex', 'claude')),
    CONSTRAINT connector_discovery_generations_runtime_fkey FOREIGN KEY (company_id, runtime_id) REFERENCES runtimes(company_id, id) ON DELETE CASCADE
);
CREATE INDEX connector_discovery_generations_company_provider_idx ON connector_discovery_generations (company_id, provider, observed_at DESC);

CREATE TABLE connector_runtime_observations (
    runtime_id uuid NOT NULL,
    connector_id uuid NOT NULL,
    company_id uuid NOT NULL,
    provider text NOT NULL,
    generation uuid NOT NULL,
    name text NOT NULL,
    description text,
    category text,
    icon_url text,
    install_count bigint,
    status text NOT NULL,
    action text NOT NULL,
    can_execute boolean DEFAULT false NOT NULL,
    contract_digest text,
    tool_namespace jsonb,
    observed_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT connector_runtime_observations_pkey PRIMARY KEY (runtime_id, connector_id),
    CONSTRAINT connector_runtime_observations_action_check CHECK (action IN ('install_and_auth', 'authenticate', 'manage_in_provider', 'none')),
    CONSTRAINT connector_runtime_observations_category_check CHECK ((category IS NULL) OR (category IN ('Productivity', 'Creativity', 'Developer Tools', 'Business & Operations', 'Data & Analytics', 'Communication', 'Education & Research', 'Security', 'Finance', 'Healthcare', 'Travel', 'Entertainment', 'Other'))),
    CONSTRAINT connector_runtime_observations_contract_check CHECK ((can_execute AND (contract_digest IS NOT NULL) AND (tool_namespace IS NOT NULL) AND (jsonb_typeof(tool_namespace) = 'array')) OR ((NOT can_execute) AND (contract_digest IS NULL) AND (tool_namespace IS NULL))),
    CONSTRAINT connector_runtime_observations_provider_check CHECK (provider IN ('codex', 'claude')),
    CONSTRAINT connector_runtime_observations_status_check CHECK (status IN ('available', 'installed_not_ready', 'needs_auth', 'connected', 'managed_in_provider', 'unavailable', 'error')),
    CONSTRAINT connector_runtime_observations_connector_fkey FOREIGN KEY (company_id, connector_id) REFERENCES connector_catalog_items(company_id, id) ON DELETE CASCADE,
    CONSTRAINT connector_runtime_observations_runtime_fkey FOREIGN KEY (company_id, runtime_id) REFERENCES runtimes(company_id, id) ON DELETE CASCADE
);
CREATE INDEX connector_runtime_observations_company_provider_idx ON connector_runtime_observations (company_id, provider, runtime_id);
CREATE INDEX connector_runtime_observations_connector_idx ON connector_runtime_observations (connector_id, observed_at DESC, runtime_id);

CREATE TABLE plugin_catalog_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    provider text NOT NULL,
    native_id text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT plugin_catalog_items_pkey PRIMARY KEY (id),
    CONSTRAINT plugin_catalog_items_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT plugin_catalog_items_company_provider_native_key UNIQUE (company_id, provider, native_id),
    CONSTRAINT plugin_catalog_items_provider_check CHECK (provider IN ('codex', 'claude')),
    CONSTRAINT plugin_catalog_items_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);
CREATE INDEX plugin_catalog_items_company_provider_idx ON plugin_catalog_items (company_id, provider, id);

CREATE TABLE plugin_discovery_generations (
    runtime_id uuid NOT NULL,
    company_id uuid NOT NULL,
    provider text NOT NULL,
    generation uuid NOT NULL,
    manifest_digest text NOT NULL,
    observed_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT plugin_discovery_generations_pkey PRIMARY KEY (runtime_id),
    CONSTRAINT plugin_discovery_generations_provider_check CHECK (provider IN ('codex', 'claude')),
    CONSTRAINT plugin_discovery_generations_runtime_fkey FOREIGN KEY (company_id, runtime_id) REFERENCES runtimes(company_id, id) ON DELETE CASCADE
);
CREATE INDEX plugin_discovery_generations_company_provider_idx ON plugin_discovery_generations (company_id, provider, observed_at DESC);

CREATE TABLE plugin_runtime_observations (
    runtime_id uuid NOT NULL,
    plugin_id uuid NOT NULL,
    company_id uuid NOT NULL,
    provider text NOT NULL,
    generation uuid NOT NULL,
    name text NOT NULL,
    description text,
    category text NOT NULL,
    icon_url text,
    share_url text,
    status text NOT NULL,
    installed boolean DEFAULT false NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    featured boolean DEFAULT false NOT NULL,
    action text NOT NULL,
    catalog_rank integer NOT NULL,
    app_count integer DEFAULT 0 NOT NULL,
    mcp_server_count integer DEFAULT 0 NOT NULL,
    skill_count integer DEFAULT 0 NOT NULL,
    install_count bigint,
    connector_identities jsonb DEFAULT '[]'::jsonb NOT NULL,
    observed_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    long_description text,
    default_prompts jsonb DEFAULT '[]'::jsonb NOT NULL,
    brand_color text,
    developer_name text,
    website_url text,
    privacy_policy_url text,
    terms_of_service_url text,
    name_key text GENERATED ALWAYS AS (regexp_replace(lower(name), '[[:space:][:punct:]]+', '', 'g')) STORED NOT NULL,
    website_host text GENERATED ALWAYS AS (regexp_replace("substring"(lower(website_url), '^[a-z][a-z0-9+.-]*://([^/?#:]+)'), '^www\.', '')) STORED,
    curated_duplicate boolean DEFAULT false NOT NULL,
    CONSTRAINT plugin_runtime_observations_pkey PRIMARY KEY (runtime_id, plugin_id),
    CONSTRAINT plugin_runtime_observations_runtime_rank_key UNIQUE (runtime_id, catalog_rank) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT plugin_runtime_observations_action_check CHECK (action IN ('install', 'uninstall', 'none')),
    CONSTRAINT plugin_runtime_observations_brand_color_check CHECK ((brand_color IS NULL) OR (brand_color ~ '^#[0-9A-Fa-f]{6}$')),
    CONSTRAINT plugin_runtime_observations_category_check CHECK (category IN ('Productivity', 'Creativity', 'Developer Tools', 'Business & Operations', 'Data & Analytics', 'Communication', 'Education & Research', 'Security', 'Finance', 'Healthcare', 'Travel', 'Entertainment', 'Other')),
    CONSTRAINT plugin_runtime_observations_connector_identities_check CHECK (jsonb_typeof(connector_identities) = 'array'),
    CONSTRAINT plugin_runtime_observations_default_prompts_check CHECK (
    CASE
        WHEN (jsonb_typeof(default_prompts) = 'array') THEN (jsonb_array_length(default_prompts) <= 3)
        ELSE false
    END),
    CONSTRAINT plugin_runtime_observations_provider_check CHECK (provider IN ('codex', 'claude')),
    CONSTRAINT plugin_runtime_observations_status_check CHECK (status IN ('available', 'installed', 'disabled', 'error')),
    CONSTRAINT plugin_runtime_observations_plugin_fkey FOREIGN KEY (company_id, plugin_id) REFERENCES plugin_catalog_items(company_id, id) ON DELETE CASCADE,
    CONSTRAINT plugin_runtime_observations_runtime_fkey FOREIGN KEY (company_id, runtime_id) REFERENCES runtimes(company_id, id) ON DELETE CASCADE
);
CREATE INDEX plugin_runtime_observations_company_provider_developer_idx ON plugin_runtime_observations (company_id, provider, lower(developer_name)) WHERE (icon_url IS NOT NULL);
CREATE INDEX plugin_runtime_observations_company_provider_name_key_idx ON plugin_runtime_observations (company_id, provider, name_key) WHERE (icon_url IS NOT NULL);
CREATE INDEX plugin_runtime_observations_company_provider_runtime_idx ON plugin_runtime_observations (company_id, provider, runtime_id, catalog_rank, plugin_id);
CREATE INDEX plugin_runtime_observations_company_provider_website_host_idx ON plugin_runtime_observations (company_id, provider, website_host) WHERE (icon_url IS NOT NULL);
CREATE INDEX plugin_runtime_observations_plugin_idx ON plugin_runtime_observations (plugin_id, observed_at DESC, runtime_id);

CREATE TABLE agent_runtime_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    user_id uuid NOT NULL,
    runtime_id uuid NOT NULL,
    provider text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT agent_runtime_bindings_pkey PRIMARY KEY (id),
    CONSTRAINT agent_runtime_bindings_agent_user_key UNIQUE (agent_id, user_id),
    CONSTRAINT agent_runtime_bindings_agent_fkey FOREIGN KEY (company_id, agent_id, provider) REFERENCES agents(company_id, id, provider) ON DELETE CASCADE,
    CONSTRAINT agent_runtime_bindings_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT agent_runtime_bindings_runtime_fkey FOREIGN KEY (company_id, runtime_id, provider) REFERENCES runtimes(company_id, id, provider) ON DELETE CASCADE,
    CONSTRAINT agent_runtime_bindings_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX agent_runtime_bindings_company_user_idx ON agent_runtime_bindings (company_id, user_id);
CREATE INDEX agent_runtime_bindings_runtime_idx ON agent_runtime_bindings (runtime_id);

CREATE TABLE skill_imports (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    created_by_user_id uuid NOT NULL,
    source_type text NOT NULL,
    source_url text DEFAULT '' NOT NULL,
    source_commit text DEFAULT '' NOT NULL,
    ready boolean DEFAULT false NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    expires_at timestamptz DEFAULT (now() + '24:00:00'::interval) NOT NULL,
    edited boolean DEFAULT false NOT NULL,
    CONSTRAINT skill_imports_pkey PRIMARY KEY (id),
    CONSTRAINT skill_imports_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT skill_imports_source_type_check CHECK (source_type IN ('github', 'zip', 'markdown')),
    CONSTRAINT skill_imports_company_id_agent_id_fkey FOREIGN KEY (company_id, agent_id) REFERENCES agents(company_id, id) ON DELETE CASCADE,
    CONSTRAINT skill_imports_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);
CREATE INDEX skill_imports_agent ON skill_imports (company_id, agent_id, created_at);

CREATE TABLE skill_versions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    import_id uuid NOT NULL,
    name text NOT NULL,
    description text NOT NULL,
    source_path text NOT NULL,
    content_hash text NOT NULL,
    size_bytes bigint NOT NULL,
    file_count integer NOT NULL,
    manifest jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT skill_versions_pkey PRIMARY KEY (id),
    CONSTRAINT skill_versions_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT skill_versions_import_id_name_key UNIQUE (import_id, name),
    CONSTRAINT skill_versions_content_hash_check CHECK (content_hash ~ '^[a-f0-9]{64}$'),
    CONSTRAINT skill_versions_manifest_check CHECK (jsonb_typeof(manifest) = 'array'),
    CONSTRAINT skill_versions_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT skill_versions_company_id_import_id_fkey FOREIGN KEY (company_id, import_id) REFERENCES skill_imports(company_id, id) ON DELETE CASCADE
);

CREATE TABLE agent_skills (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    version_id uuid NOT NULL,
    name text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT agent_skills_pkey PRIMARY KEY (id),
    CONSTRAINT agent_skills_company_id_agent_id_name_key UNIQUE (company_id, agent_id, name),
    CONSTRAINT agent_skills_revision_check CHECK (revision > 0),
    CONSTRAINT agent_skills_company_id_agent_id_fkey FOREIGN KEY (company_id, agent_id) REFERENCES agents(company_id, id) ON DELETE CASCADE,
    CONSTRAINT agent_skills_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT agent_skills_company_id_version_id_fkey FOREIGN KEY (company_id, version_id) REFERENCES skill_versions(company_id, id)
);
CREATE INDEX agent_skills_version ON agent_skills (version_id);
