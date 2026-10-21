-- Identity: people, sessions, companies, membership and groups.

CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;

CREATE TABLE users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email text NOT NULL,
    password_hash text,
    display_name text NOT NULL,
    avatar_url text,
    disabled_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    email_verified_at timestamptz,
    deleted_at timestamptz,
    biography text DEFAULT '' NOT NULL,
    phone_country_iso2 text,
    phone_number text,
    CONSTRAINT users_pkey PRIMARY KEY (id)
);
CREATE UNIQUE INDEX users_email_ci_key ON users (lower(email));

CREATE TABLE auth_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    token_prefix text NOT NULL,
    token_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    last_used_at timestamptz,
    revoked_at timestamptz,
    revocation_reason text,
    revoked_by_user_id uuid,
    rotated_from_session_id uuid,
    client_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    rotation_id uuid,
    client text DEFAULT 'person' NOT NULL,
    CONSTRAINT auth_sessions_pkey PRIMARY KEY (id),
    CONSTRAINT auth_sessions_rotated_from_session_id_key UNIQUE (rotated_from_session_id),
    CONSTRAINT auth_sessions_token_hash_key UNIQUE (token_hash),
    CONSTRAINT auth_sessions_user_id_id_key UNIQUE (user_id, id),
    CONSTRAINT auth_sessions_client_check CHECK (client IN ('person', 'daemon')),
    CONSTRAINT auth_sessions_rotation_fkey FOREIGN KEY (user_id, rotated_from_session_id) REFERENCES auth_sessions(user_id, id),
    CONSTRAINT auth_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX auth_sessions_user_active_idx ON auth_sessions (user_id, expires_at DESC) WHERE (revoked_at IS NULL);

CREATE TABLE oauth_accounts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    provider text NOT NULL,
    subject text NOT NULL,
    email text NOT NULL,
    profile jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT oauth_accounts_pkey PRIMARY KEY (id),
    CONSTRAINT oauth_accounts_provider_subject_key UNIQUE (provider, subject),
    CONSTRAINT oauth_accounts_user_id_provider_key UNIQUE (user_id, provider),
    CONSTRAINT oauth_accounts_provider_check CHECK (provider = 'google'),
    CONSTRAINT oauth_accounts_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE oauth_login_flows (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider text NOT NULL,
    client text NOT NULL,
    state_hash bytea NOT NULL,
    nonce_hash bytea NOT NULL,
    browser_binding_hash bytea,
    redirect_uri text NOT NULL,
    return_path text,
    code_challenge text,
    client_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT oauth_login_flows_pkey PRIMARY KEY (id),
    CONSTRAINT oauth_login_flows_provider_state_hash_key UNIQUE (provider, state_hash),
    CONSTRAINT oauth_login_flows_client_check CHECK (client IN ('web', 'desktop')),
    CONSTRAINT oauth_login_flows_provider_check CHECK (provider = 'google')
);
CREATE INDEX oauth_login_flows_expiry_idx ON oauth_login_flows (expires_at);

CREATE TABLE email_verification_flows (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email text NOT NULL,
    password_hash text NOT NULL,
    display_name text NOT NULL,
    client text NOT NULL,
    client_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    code_hash bytea NOT NULL,
    generation integer DEFAULT 1 NOT NULL,
    failed_attempts integer DEFAULT 0 NOT NULL,
    expires_at timestamptz NOT NULL,
    resend_available_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    user_id uuid,
    CONSTRAINT email_verification_flows_pkey PRIMARY KEY (id),
    CONSTRAINT email_verification_flows_client_check CHECK (client IN ('web', 'desktop')),
    CONSTRAINT email_verification_flows_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX email_verification_flows_email_ci_key ON email_verification_flows (lower(email));
CREATE INDEX email_verification_flows_expiry_idx ON email_verification_flows (expires_at, id);
CREATE UNIQUE INDEX email_verification_flows_user_id_key ON email_verification_flows (user_id) WHERE (user_id IS NOT NULL);

CREATE TABLE password_reset_flows (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    generation integer DEFAULT 1 NOT NULL,
    expires_at timestamptz NOT NULL,
    resend_available_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT password_reset_flows_pkey PRIMARY KEY (id),
    CONSTRAINT password_reset_flows_token_hash_key UNIQUE (token_hash),
    CONSTRAINT password_reset_flows_user_id_key UNIQUE (user_id),
    CONSTRAINT password_reset_flows_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE desktop_handoff_codes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    code_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT desktop_handoff_codes_pkey PRIMARY KEY (id),
    CONSTRAINT desktop_handoff_codes_code_hash_key UNIQUE (code_hash),
    CONSTRAINT desktop_handoff_codes_user_id_key UNIQUE (user_id),
    CONSTRAINT desktop_handoff_codes_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE companies (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    slug text NOT NULL,
    task_prefix text NOT NULL,
    next_task_number bigint DEFAULT 1 NOT NULL,
    deleting_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    business_context text DEFAULT '' NOT NULL,
    contact_email text,
    phone_country_iso2 text,
    phone_number text,
    proactive_mode_enabled boolean DEFAULT false NOT NULL,
    proactive_mode_version bigint DEFAULT 1 NOT NULL,
    proactive_mode_enabled_by_user_id uuid,
    proactive_mode_conversation_id uuid,
    agent_setup_completed_at timestamptz,
    first_plan_applied_at timestamptz,
    website_url text,
    avatar_attachment_id uuid,
    CONSTRAINT companies_pkey PRIMARY KEY (id),
    CONSTRAINT companies_slug_key UNIQUE (slug)
);
CREATE INDEX companies_avatar_attachment_idx ON companies (avatar_attachment_id) WHERE (avatar_attachment_id IS NOT NULL);

CREATE TABLE company_members (
    company_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role text NOT NULL,
    status text DEFAULT 'active' NOT NULL,
    revoked_at timestamptz,
    revoked_by_user_id uuid,
    reactivated_at timestamptz,
    reactivated_by_user_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    presence_active_until timestamptz,
    presence_manual_away boolean DEFAULT false NOT NULL,
    CONSTRAINT company_members_pkey PRIMARY KEY (company_id, user_id),
    CONSTRAINT company_members_role_check CHECK (role IN ('owner', 'admin', 'member')),
    CONSTRAINT company_members_status_check CHECK (status IN ('active', 'revoked')),
    CONSTRAINT company_members_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT company_members_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX company_members_active_user_idx ON company_members (user_id, company_id) WHERE (status = 'active');

CREATE TABLE company_resources (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    resource_type text NOT NULL,
    daemon_id text NOT NULL,
    local_path text NOT NULL,
    label text NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_by_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT company_resources_pkey PRIMARY KEY (id),
    CONSTRAINT company_resources_resource_type_check CHECK (resource_type = 'local_directory'),
    CONSTRAINT company_resources_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX company_resources_company_daemon_key ON company_resources (company_id, daemon_id);
CREATE INDEX company_resources_tenant_order_idx ON company_resources (company_id, "position", created_at, id);

CREATE TABLE company_reference_documents (
    company_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT company_reference_documents_pkey PRIMARY KEY (attachment_id)
);
CREATE INDEX company_reference_documents_company_idx ON company_reference_documents (company_id, created_at, attachment_id);

CREATE TABLE company_milestone_attestations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    milestone_type text NOT NULL,
    catalog_version integer NOT NULL,
    note text DEFAULT '' NOT NULL,
    attested_by_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT company_milestone_attestations_pkey PRIMARY KEY (id),
    CONSTRAINT company_milestone_attestations_company_milestone_key UNIQUE (company_id, milestone_type),
    CONSTRAINT company_milestone_attestations_catalog_version_check CHECK (catalog_version >= 1),
    CONSTRAINT company_milestone_attestations_milestone_type_check CHECK (milestone_type IN ('positioning', 'brand', 'presence', 'finance', 'product', 'launch', 'growth', 'sales_outbound', 'operations', 'support')),
    CONSTRAINT company_milestone_attestations_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);

CREATE TABLE company_invitations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    email text NOT NULL,
    status text DEFAULT 'pending' NOT NULL,
    generation integer DEFAULT 1 NOT NULL,
    invited_by_user_id uuid NOT NULL,
    accepted_user_id uuid,
    expires_at timestamptz NOT NULL,
    renewal_requested_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    role text DEFAULT 'member' NOT NULL,
    CONSTRAINT company_invitations_pkey PRIMARY KEY (id),
    CONSTRAINT company_invitations_role_check CHECK (role IN ('admin', 'member')),
    CONSTRAINT company_invitations_status_check CHECK (status IN ('pending', 'accepted', 'revoked', 'expired')),
    CONSTRAINT company_invitations_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);
CREATE INDEX company_invitations_company_idx ON company_invitations (company_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX company_invitations_pending_email_idx ON company_invitations (company_id, lower(email)) WHERE (status = 'pending');

CREATE TABLE company_invitation_tokens (
    invitation_id uuid NOT NULL,
    generation integer NOT NULL,
    token_hash bytea NOT NULL,
    encrypted_token bytea,
    recipient_email text NOT NULL,
    company_name text NOT NULL,
    inviter_name text NOT NULL,
    expires_at timestamptz NOT NULL,
    delivery_status text DEFAULT 'queued' NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamptz DEFAULT now() NOT NULL,
    lease_until timestamptz,
    provider_id text,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT company_invitation_tokens_pkey PRIMARY KEY (invitation_id, generation),
    CONSTRAINT company_invitation_tokens_token_hash_key UNIQUE (token_hash),
    CONSTRAINT company_invitation_tokens_delivery_status_check CHECK (delivery_status IN ('queued', 'sending', 'sent', 'failed', 'cancelled')),
    CONSTRAINT company_invitation_tokens_invitation_id_fkey FOREIGN KEY (invitation_id) REFERENCES company_invitations(id) ON DELETE CASCADE
);
CREATE INDEX company_invitation_delivery_idx ON company_invitation_tokens (next_attempt_at) WHERE (delivery_status IN ('queued', 'sending'));

CREATE TABLE company_share_links (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    encrypted_token bytea,
    created_by_user_id uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    role text DEFAULT 'member' NOT NULL,
    CONSTRAINT company_share_links_pkey PRIMARY KEY (id),
    CONSTRAINT company_share_links_company_id_key UNIQUE (company_id),
    CONSTRAINT company_share_links_token_hash_key UNIQUE (token_hash),
    CONSTRAINT company_share_links_role_check CHECK (role IN ('admin', 'member')),
    CONSTRAINT company_share_links_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);

CREATE TABLE company_initializations (
    company_id uuid NOT NULL,
    created_by_user_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    research_run_id uuid,
    completed_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT company_initializations_pkey PRIMARY KEY (company_id)
);

CREATE TABLE company_groups (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT '' NOT NULL,
    created_by_user_id uuid,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT company_groups_pkey PRIMARY KEY (id),
    CONSTRAINT company_groups_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT company_groups_description_check CHECK (char_length(description) <= 500),
    CONSTRAINT company_groups_name_check CHECK ((name = btrim(name)) AND ((char_length(name) >= 1) AND (char_length(name) <= 80))),
    CONSTRAINT company_groups_version_check CHECK ((version >= 1) AND (version <= '9007199254740991'::bigint)),
    CONSTRAINT company_groups_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX company_groups_name_key ON company_groups (company_id, lower(name));

CREATE TABLE company_group_members (
    company_id uuid NOT NULL,
    group_id uuid NOT NULL,
    user_id uuid NOT NULL,
    added_by_user_id uuid,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT company_group_members_pkey PRIMARY KEY (company_id, group_id, user_id),
    CONSTRAINT company_group_members_company_id_group_id_fkey FOREIGN KEY (company_id, group_id) REFERENCES company_groups(company_id, id) ON DELETE CASCADE,
    CONSTRAINT company_group_members_company_id_user_id_fkey FOREIGN KEY (company_id, user_id) REFERENCES company_members(company_id, user_id) ON DELETE CASCADE
);
CREATE INDEX company_group_members_user_idx ON company_group_members (company_id, user_id, group_id);
