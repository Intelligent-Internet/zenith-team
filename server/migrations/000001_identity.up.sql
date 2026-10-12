-- 000001_identity: user identity and credentials — users, auth sessions, oauth
-- accounts, login/verification/reset flows, and desktop handoff codes.
-- Conventions (whole schema): DB-generated ids via
-- gen_random_uuid(); FOREIGN KEY columns on structural ownership edges
-- (ON DELETE CASCADE for owned children, SET NULL for optional pointers);
-- attribution and polymorphic actor columns carry no FK; text + CHECK enums;
-- jsonb object checks; created_at on every table, updated_at wherever rows
-- mutate.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email text NOT NULL,
    password_hash text,
    display_name text NOT NULL,
    avatar_url text,
    disabled_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    email_verified_at timestamp with time zone,
    deleted_at timestamp with time zone,
    biography text DEFAULT ''::text NOT NULL,
    phone_country_iso2 text,
    phone_number text,
    CONSTRAINT users_pkey PRIMARY KEY (id)
);

CREATE UNIQUE INDEX users_email_ci_key ON users USING btree (lower(email));

CREATE TABLE auth_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_prefix text NOT NULL,
    token_hash bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    last_used_at timestamp with time zone,
    revoked_at timestamp with time zone,
    revocation_reason text,
    revoked_by_user_id uuid,
    rotated_from_session_id uuid,
    client_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT auth_sessions_pkey PRIMARY KEY (id),
    CONSTRAINT auth_sessions_token_hash_key UNIQUE (token_hash),
    CONSTRAINT auth_sessions_rotated_from_session_id_key UNIQUE (rotated_from_session_id),
    CONSTRAINT auth_sessions_user_id_id_key UNIQUE (user_id, id),
    CONSTRAINT auth_sessions_rotation_fkey FOREIGN KEY (user_id, rotated_from_session_id)
        REFERENCES auth_sessions(user_id, id)
);

CREATE INDEX auth_sessions_user_active_idx ON auth_sessions USING btree (user_id, expires_at DESC) WHERE revoked_at IS NULL;

CREATE TABLE oauth_accounts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider text NOT NULL,
    subject text NOT NULL,
    -- Every writer supplies it: CreateOAuthAccount and
    -- UpdateOAuthAccountSnapshot both bind a non-null text, so the column
    -- never held NULL. An absent provider email arrives as '', not NULL.
    email text NOT NULL,
    profile jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT oauth_accounts_pkey PRIMARY KEY (id),
    CONSTRAINT oauth_accounts_provider_subject_key UNIQUE (provider, subject),
    CONSTRAINT oauth_accounts_user_id_provider_key UNIQUE (user_id, provider),
    CONSTRAINT oauth_accounts_provider_check CHECK (provider = 'google')
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
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT oauth_login_flows_pkey PRIMARY KEY (id),
    CONSTRAINT oauth_login_flows_provider_state_hash_key UNIQUE (provider, state_hash),
    CONSTRAINT oauth_login_flows_provider_check CHECK (provider = 'google'),
    CONSTRAINT oauth_login_flows_client_check CHECK (client IN ('web', 'desktop'))
);

CREATE INDEX oauth_login_flows_expiry_idx ON oauth_login_flows USING btree (expires_at);

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
    -- No max_attempts column: the ceiling is auth.MaxVerificationAttempts, a
    -- constant. Storing it per flow meant a row could outlive the value it
    -- was written with and be judged by a number no longer in force.
    expires_at timestamp with time zone NOT NULL,
    resend_available_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    user_id uuid REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT email_verification_flows_pkey PRIMARY KEY (id),
    CONSTRAINT email_verification_flows_client_check CHECK (client IN ('web', 'desktop'))
);

CREATE UNIQUE INDEX email_verification_flows_email_ci_key ON email_verification_flows USING btree (lower(email));
CREATE INDEX email_verification_flows_expiry_idx ON email_verification_flows USING btree (expires_at, id);
CREATE UNIQUE INDEX email_verification_flows_user_id_key ON email_verification_flows USING btree (user_id) WHERE user_id IS NOT NULL;

CREATE TABLE password_reset_flows (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL,
    generation integer DEFAULT 1 NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    resend_available_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT password_reset_flows_pkey PRIMARY KEY (id),
    CONSTRAINT password_reset_flows_token_hash_key UNIQUE (token_hash),
    CONSTRAINT password_reset_flows_user_id_key UNIQUE (user_id)
);


CREATE TABLE desktop_handoff_codes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT desktop_handoff_codes_pkey PRIMARY KEY (id),
    CONSTRAINT desktop_handoff_codes_code_hash_key UNIQUE (code_hash),
    CONSTRAINT desktop_handoff_codes_user_id_key UNIQUE (user_id)
);

