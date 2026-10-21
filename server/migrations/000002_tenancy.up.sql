-- 000002_tenancy: the Company tenant root — companies, memberships, typed
-- company resources, and the Work Plan (goals).
-- Structural edges carry FKs; attribution columns do not.

CREATE TABLE companies (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    slug text NOT NULL,
    task_prefix text NOT NULL,
    next_task_number bigint DEFAULT 1 NOT NULL,
    deleting_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    business_context text DEFAULT ''::text NOT NULL,
    contact_email text,
    phone_country_iso2 text,
    phone_number text,
    proactive_mode_enabled boolean DEFAULT false NOT NULL,
    proactive_mode_version bigint DEFAULT 1 NOT NULL,
    proactive_mode_enabled_by_user_id uuid,
    proactive_mode_conversation_id uuid,
    agent_setup_completed_at timestamp with time zone,
    first_plan_applied_at timestamp with time zone,
    website_url text,
    CONSTRAINT companies_pkey PRIMARY KEY (id),
    CONSTRAINT companies_slug_key UNIQUE (slug)
);

CREATE TABLE company_members (
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    revoked_at timestamp with time zone,
    revoked_by_user_id uuid,
    reactivated_at timestamp with time zone,
    reactivated_by_user_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT company_members_pkey PRIMARY KEY (company_id, user_id),
    CONSTRAINT company_members_role_check CHECK (role = ANY (ARRAY['owner'::text, 'admin'::text, 'member'::text])),
    CONSTRAINT company_members_status_check CHECK (status = ANY (ARRAY['active'::text, 'revoked'::text]))
);

CREATE INDEX company_members_active_user_idx ON company_members USING btree (user_id, company_id) WHERE status = 'active'::text;

-- company_resources: external things a company's runs work against. One
-- concrete type today — local_directory: a folder on one machine that runs
-- execute inside — stored as flat, typed columns (no
-- polymorphic ref payload; a future type widens this table in a migration).
--
-- daemon_id is the registering machine's persistent identity
-- (~/.zenith-team/daemon.id). Machines are deliberately not an entity: the same
-- daemon_id appears in runtimes (000005_execution) up to once per provider
-- per company, and in every company its owner registered it in, so there is
-- nothing for a FOREIGN KEY to point at — consistency is enforced in Go.
-- The unique index below owns the resource-level rule: one local directory
-- per daemon per company, whatever the path spelling.
CREATE TABLE company_resources (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    resource_type text NOT NULL,
    daemon_id text NOT NULL,
    local_path text NOT NULL,
    label text NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    metadata jsonb DEFAULT '{}' NOT NULL,
    created_by_user_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT company_resources_pkey PRIMARY KEY (id),
    CONSTRAINT company_resources_resource_type_check CHECK (resource_type = 'local_directory')
);

CREATE UNIQUE INDEX company_resources_company_daemon_key ON company_resources USING btree (company_id, daemon_id);

CREATE INDEX company_resources_tenant_order_idx ON company_resources USING btree (company_id, "position", created_at, id);

CREATE TABLE goals (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    title text NOT NULL,
    description text DEFAULT '' NOT NULL,
    status text DEFAULT 'active' NOT NULL,
    creator_user_id uuid,
    creator_agent_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    key text NOT NULL,
    CONSTRAINT goals_pkey PRIMARY KEY (id),
    CONSTRAINT goals_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT goals_company_key_key UNIQUE (company_id, key),
    CONSTRAINT goals_status_check CHECK (status IN ('draft', 'active', 'completed', 'cancelled'))
);

CREATE INDEX goals_company_created_idx ON goals USING btree (company_id, created_at DESC, id DESC);

CREATE INDEX goals_company_status_created_idx ON goals USING btree (company_id, status, created_at DESC, id DESC);
