-- Mail and social: company mailboxes and social publishing.

CREATE TABLE company_mailboxes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    local_part text NOT NULL,
    created_by_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT company_mailboxes_pkey PRIMARY KEY (id),
    CONSTRAINT company_mailboxes_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT company_mailboxes_company_key UNIQUE (company_id),
    CONSTRAINT company_mailboxes_local_part_key UNIQUE (local_part),
    CONSTRAINT company_mailboxes_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);

CREATE TABLE mailbox_messages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    mailbox_id uuid NOT NULL,
    provider_email_id text NOT NULL,
    sender text NOT NULL,
    subject text NOT NULL,
    recipients text[] NOT NULL,
    received_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    read_at timestamptz,
    has_attachments boolean DEFAULT false NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT mailbox_messages_pkey PRIMARY KEY (id),
    CONSTRAINT mailbox_messages_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT mailbox_messages_mailbox_provider_key UNIQUE (mailbox_id, provider_email_id),
    CONSTRAINT mailbox_messages_mailbox_fkey FOREIGN KEY (company_id, mailbox_id) REFERENCES company_mailboxes(company_id, id) ON DELETE CASCADE
);
CREATE INDEX mailbox_messages_company_list_idx ON mailbox_messages (company_id, received_at DESC, id DESC);
CREATE INDEX mailbox_messages_company_unread_idx ON mailbox_messages (company_id, received_at DESC, id DESC) WHERE (read_at IS NULL);
CREATE INDEX mailbox_messages_provider_email_idx ON mailbox_messages (provider_email_id, id);
CREATE INDEX mailbox_messages_retention_idx ON mailbox_messages (expires_at, id);

CREATE TABLE mailbox_message_attachments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    message_id uuid NOT NULL,
    company_id uuid NOT NULL,
    provider_attachment_id text NOT NULL,
    filename text NOT NULL,
    mime_type text NOT NULL,
    size_bytes bigint,
    disposition text,
    content_id text,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT mailbox_message_attachments_pkey PRIMARY KEY (id),
    CONSTRAINT mailbox_message_attachments_message_provider_key UNIQUE (message_id, provider_attachment_id),
    CONSTRAINT mailbox_message_attachments_disposition_check CHECK ((disposition IS NULL) OR (disposition IN ('attachment', 'inline'))),
    CONSTRAINT mailbox_message_attachments_message_fkey FOREIGN KEY (company_id, message_id) REFERENCES mailbox_messages(company_id, id) ON DELETE CASCADE
);
CREATE INDEX mailbox_message_attachments_message_idx ON mailbox_message_attachments (company_id, message_id, id);

CREATE TABLE mailbox_received_receipts (
    provider_event_id text NOT NULL,
    provider_email_id text NOT NULL,
    outcome text NOT NULL,
    received_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT mailbox_received_receipts_pkey PRIMARY KEY (provider_event_id),
    CONSTRAINT mailbox_received_receipts_provider_email_key UNIQUE (provider_email_id),
    CONSTRAINT mailbox_received_receipts_outcome_check CHECK (outcome IN ('delivered', 'ignored'))
);
CREATE INDEX mailbox_received_receipts_retention_idx ON mailbox_received_receipts (expires_at, provider_event_id);

CREATE TABLE mailbox_draft_groups (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    mailbox_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    source_run_id uuid NOT NULL,
    request_key uuid NOT NULL,
    agent_id uuid NOT NULL,
    created_by_user_id uuid NOT NULL,
    payload_sha256 bytea NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT mailbox_draft_groups_pkey PRIMARY KEY (id),
    CONSTRAINT mailbox_draft_groups_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT mailbox_draft_groups_company_run_key UNIQUE (company_id, source_run_id),
    CONSTRAINT mailbox_draft_groups_agent_fkey FOREIGN KEY (company_id, agent_id) REFERENCES agents(company_id, id),
    CONSTRAINT mailbox_draft_groups_conversation_fkey FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT mailbox_draft_groups_mailbox_fkey FOREIGN KEY (company_id, mailbox_id) REFERENCES company_mailboxes(company_id, id) ON DELETE CASCADE,
    CONSTRAINT mailbox_draft_groups_run_fkey FOREIGN KEY (company_id, source_run_id) REFERENCES runs(company_id, id) ON DELETE CASCADE
);
CREATE INDEX mailbox_draft_groups_conversation_created_idx ON mailbox_draft_groups (company_id, conversation_id, created_at, id);

CREATE TABLE mailbox_drafts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    group_id uuid NOT NULL,
    ordinal integer NOT NULL,
    subject text NOT NULL,
    body_text text NOT NULL,
    from_name text NOT NULL,
    from_address text NOT NULL,
    status text DEFAULT 'draft' NOT NULL,
    send_generation bigint DEFAULT 0 NOT NULL,
    send_requested_at timestamptz,
    send_lease_id uuid,
    send_lease_expires_at timestamptz,
    send_attempts integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamptz,
    sent_by_user_id uuid,
    sent_at timestamptz,
    failed_at timestamptz,
    failure_reason text,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT mailbox_drafts_pkey PRIMARY KEY (id),
    CONSTRAINT mailbox_drafts_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT mailbox_drafts_group_ordinal_key UNIQUE (group_id, ordinal),
    CONSTRAINT mailbox_drafts_status_check CHECK (status IN ('draft', 'sending', 'sent', 'failed')),
    CONSTRAINT mailbox_drafts_group_fkey FOREIGN KEY (company_id, group_id) REFERENCES mailbox_draft_groups(company_id, id) ON DELETE CASCADE
);
CREATE INDEX mailbox_drafts_company_send_quota_idx ON mailbox_drafts (company_id, send_requested_at, id) WHERE (status IN ('sending', 'sent'));
CREATE INDEX mailbox_drafts_group_idx ON mailbox_drafts (company_id, group_id, ordinal, id);
CREATE INDEX mailbox_drafts_send_queue_idx ON mailbox_drafts (next_attempt_at, created_at, id) WHERE (status = 'sending');

CREATE TABLE mailbox_draft_deliveries (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    draft_id uuid NOT NULL,
    ordinal integer NOT NULL,
    to_addresses text[] NOT NULL,
    cc_addresses text[] NOT NULL,
    provider_email_id text,
    accepted_at timestamptz,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT mailbox_draft_deliveries_pkey PRIMARY KEY (id),
    CONSTRAINT mailbox_draft_deliveries_draft_ordinal_key UNIQUE (draft_id, ordinal),
    CONSTRAINT mailbox_draft_deliveries_draft_fkey FOREIGN KEY (company_id, draft_id) REFERENCES mailbox_drafts(company_id, id) ON DELETE CASCADE
);
CREATE INDEX mailbox_draft_deliveries_draft_idx ON mailbox_draft_deliveries (company_id, draft_id, ordinal, id);

CREATE TABLE social_connections (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    platform text NOT NULL,
    external_id text NOT NULL,
    display_name text NOT NULL,
    username text DEFAULT '' NOT NULL,
    avatar_url text DEFAULT '' NOT NULL,
    credentials bytea NOT NULL,
    expires_at timestamptz,
    reconnect_required boolean DEFAULT false NOT NULL,
    maintenance_at timestamptz DEFAULT statement_timestamp(),
    operation_token uuid,
    operation_until timestamptz,
    connected_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT social_connections_pkey PRIMARY KEY (id),
    CONSTRAINT social_connections_user_id_platform_external_id_key UNIQUE (user_id, platform, external_id),
    CONSTRAINT social_connections_platform_check CHECK (platform IN ('x', 'facebook', 'instagram', 'linkedin')),
    CONSTRAINT social_connections_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX social_connections_maintenance_idx ON social_connections (maintenance_at) WHERE (maintenance_at IS NOT NULL);

CREATE TABLE social_targets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    connection_id uuid NOT NULL,
    external_id text NOT NULL,
    display_name text NOT NULL,
    username text DEFAULT '' NOT NULL,
    type text NOT NULL,
    credentials bytea NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT social_targets_pkey PRIMARY KEY (id),
    CONSTRAINT social_targets_connection_id_external_id_key UNIQUE (connection_id, external_id),
    CONSTRAINT social_targets_connection_id_fkey FOREIGN KEY (connection_id) REFERENCES social_connections(id) ON DELETE CASCADE
);

CREATE TABLE social_oauth_flows (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    session_id uuid NOT NULL,
    platform text NOT NULL,
    state_hash bytea NOT NULL,
    verifier bytea NOT NULL,
    binding_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    desktop_redirect_uri text DEFAULT '' NOT NULL,
    desktop_state text DEFAULT '' NOT NULL,
    launched_at timestamptz,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT social_oauth_flows_pkey PRIMARY KEY (id),
    CONSTRAINT social_oauth_flows_state_hash_key UNIQUE (state_hash),
    CONSTRAINT social_oauth_flows_platform_check CHECK (platform IN ('x', 'facebook', 'instagram', 'linkedin')),
    CONSTRAINT social_oauth_flows_session_id_fkey FOREIGN KEY (session_id) REFERENCES auth_sessions(id) ON DELETE CASCADE,
    CONSTRAINT social_oauth_flows_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX social_oauth_flows_expiry_idx ON social_oauth_flows (expires_at);

CREATE TABLE social_posts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    run_id uuid,
    connection_id uuid,
    request_key uuid NOT NULL,
    payload_hash bytea NOT NULL,
    platform text NOT NULL,
    target_name text NOT NULL,
    text text NOT NULL,
    status text DEFAULT 'publishing' NOT NULL,
    external_id text DEFAULT '' NOT NULL,
    url text DEFAULT '' NOT NULL,
    error_code text DEFAULT '' NOT NULL,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT social_posts_pkey PRIMARY KEY (id),
    CONSTRAINT social_posts_user_id_request_key_key UNIQUE (user_id, request_key),
    CONSTRAINT social_posts_platform_check CHECK (platform IN ('x', 'facebook', 'instagram', 'linkedin')),
    CONSTRAINT social_posts_status_check CHECK (status IN ('publishing', 'published', 'failed', 'unknown')),
    CONSTRAINT social_posts_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT social_posts_connection_id_fkey FOREIGN KEY (connection_id) REFERENCES social_connections(id) ON DELETE SET NULL,
    CONSTRAINT social_posts_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE,
    CONSTRAINT social_posts_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE SET NULL,
    CONSTRAINT social_posts_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX social_posts_user_created_idx ON social_posts (user_id, created_at DESC);
