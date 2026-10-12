-- Social connections, OAuth flows and publishing. Kept separate from Calendar.
CREATE TABLE social_connections (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    platform text NOT NULL CHECK (platform IN ('x', 'facebook', 'instagram', 'linkedin')),
    external_id text NOT NULL,
    display_name text NOT NULL,
    username text NOT NULL DEFAULT '',
    avatar_url text NOT NULL DEFAULT '',
    credentials bytea NOT NULL,
    expires_at timestamptz,
    reconnect_required boolean NOT NULL DEFAULT false,
    maintenance_at timestamptz DEFAULT statement_timestamp(),
    operation_token uuid,
    operation_until timestamptz,
    connected_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    version bigint NOT NULL DEFAULT 1,
    UNIQUE (user_id, platform, external_id)
);

CREATE INDEX social_connections_maintenance_idx ON social_connections(maintenance_at) WHERE maintenance_at IS NOT NULL;

CREATE TABLE social_targets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id uuid NOT NULL REFERENCES social_connections(id) ON DELETE CASCADE,
    external_id text NOT NULL,
    display_name text NOT NULL,
    username text NOT NULL DEFAULT '',
    type text NOT NULL,
    credentials bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    UNIQUE (connection_id, external_id)
);

CREATE TABLE social_oauth_flows (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES auth_sessions(id) ON DELETE CASCADE,
    platform text NOT NULL CHECK (platform IN ('x', 'facebook', 'instagram', 'linkedin')),
    state_hash bytea NOT NULL UNIQUE,
    verifier bytea NOT NULL,
    binding_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    desktop_redirect_uri text NOT NULL DEFAULT '',
    desktop_state text NOT NULL DEFAULT '',
    launched_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp()
);
CREATE INDEX social_oauth_flows_expiry_idx ON social_oauth_flows (expires_at);

CREATE TABLE social_posts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    conversation_id uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    run_id uuid REFERENCES runs(id) ON DELETE SET NULL,
    connection_id uuid REFERENCES social_connections(id) ON DELETE SET NULL,
    request_key uuid NOT NULL,
    payload_hash bytea NOT NULL,
    platform text NOT NULL CHECK (platform IN ('x', 'facebook', 'instagram', 'linkedin')),
    target_name text NOT NULL,
    text text NOT NULL,
    status text NOT NULL DEFAULT 'publishing' CHECK (status IN ('publishing', 'published', 'failed', 'unknown')),
    external_id text NOT NULL DEFAULT '',
    url text NOT NULL DEFAULT '',
    error_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    UNIQUE(user_id, request_key)
);
CREATE INDEX social_posts_user_created_idx ON social_posts(user_id, created_at DESC);
