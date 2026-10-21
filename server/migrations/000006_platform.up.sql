-- 000006_platform: platform services — the activity feed, the user inbox,
-- and crash reports.
--
-- Reshape in this migration: inbox_user_state is gone. Unread
-- counts are derived by COUNT(*) over the partial unread index, and the
-- realtime counts ordering token is max(inbox_items.updated_at) per
-- recipient. There is no realtime_outbox: wire events publish post-commit on
-- the in-process Bus.

-- The activity feed stores each domain event's wire name as `action` and its
-- domain-owned topic as `entity_type`, plus the redacted payload in `details`.
-- Rows are written by the Bus sink after commit, never inline by request
-- handlers. The person-facing feed is the narrower read projection described
-- in internal/activity; task_id is NULL for off-task events.
-- activity_events is the company activity feed. Every entry names the entity
-- it is about; entity_id is text so non-UUID entity keys stay representable.
-- actor_id is a free-form actor label or id;
-- agent_id is attribution only, so it deliberately has no foreign key and
-- feed history survives agent deletion. details is sanitized (pkg/redact)
-- before storage.
CREATE TABLE activity_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    -- Append-only history: no key into companies. A cascade here is a delete
    -- nobody can size, and the feed is evidence that should outlive the rows
    -- it describes. See data.md.
    company_id uuid NOT NULL,
    actor_type text DEFAULT 'system' NOT NULL,
    actor_id text,
    action text NOT NULL,
    entity_type text NOT NULL,
    entity_id text NOT NULL,
    agent_id uuid,
    -- The rows the fact was about, off the event envelope. Null when it was
    -- about none: an agent rename names no run, task or conversation. No
    -- foreign keys — this is append-only evidence that outlives what it
    -- describes, same reason company_id has none.
    run_id bigint,
    task_id uuid,
    conversation_id uuid,
    details jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT activity_events_pkey PRIMARY KEY (id),
    CONSTRAINT activity_events_actor_type_check CHECK (actor_type IN ('user', 'agent', 'system'))
    -- action and entity_type have no CHECK: the audit log takes every event
    -- the bus carries, so action is the event's wire name and entity_type is
    -- its topic. Both vocabularies are declared in Go, in the domain packages
    -- that publish them, and validated at the event Bus seam. Restating them
    -- here would mean a migration for every new event, and a vocabulary
    -- written in two places is one that drifts.
);

CREATE INDEX activity_events_company_created_idx
    ON activity_events (company_id, created_at DESC, id DESC);
CREATE INDEX activity_events_entity_created_idx
    ON activity_events (company_id, entity_type, entity_id, created_at DESC, id DESC);
CREATE INDEX activity_events_agent_created_idx
    ON activity_events (agent_id, created_at DESC) WHERE agent_id IS NOT NULL;

CREATE TABLE inbox_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    recipient_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    conversation_id uuid,
    kind text NOT NULL,
    -- What the notification is about, in the same shape and the same closed
    -- vocabulary activity_events uses. Before this the subject was reachable
    -- only by parsing deep_link, so a client had to read an id out of a URL
    -- and every new notification kind needed a new column to be addressable.
    entity_type text NOT NULL,
    entity_id text NOT NULL,
    title text NOT NULL,
    body text NOT NULL,
    deep_link text,
    dedupe_key text NOT NULL,
    read_at timestamp with time zone,
    archived_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT statement_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT inbox_items_pkey PRIMARY KEY (id),
    CONSTRAINT inbox_items_company_id_recipient_user_id_dedupe_key_key
        UNIQUE (company_id, recipient_user_id, dedupe_key),
    CONSTRAINT inbox_items_conversation_fkey
        FOREIGN KEY (company_id, conversation_id)
        REFERENCES conversations(company_id, id)
        ON DELETE SET NULL (conversation_id),
    CONSTRAINT inbox_items_kind_check CHECK (kind IN (
        'task_assigned', 'blocker', 'run_completed', 'run_failed',
        'approval_request', 'question_request'
    )),
    CONSTRAINT inbox_items_entity_type_check CHECK (entity_type IN (
        'company', 'user', 'agent', 'goal', 'task', 'run', 'conversation',
        'message', 'inbox', 'resource', 'runtime', 'approval', 'question'
    ))
);

-- The archived view is a filtered walk of inbox_items_recipient_created_idx.
-- One recipient's inbox is small enough that a second ordering is not worth
-- the write on every insert.
CREATE INDEX inbox_items_recipient_created_idx
    ON inbox_items (company_id, recipient_user_id, created_at DESC, id DESC);
-- Serves both the unread listing and the derived COUNT(*) unread counter.
CREATE INDEX inbox_items_recipient_unread_idx
    ON inbox_items (company_id, recipient_user_id, created_at DESC, id DESC)
    WHERE read_at IS NULL AND archived_at IS NULL;
-- Serves the counts ordering token: max(updated_at) per recipient.
CREATE INDEX inbox_items_recipient_updated_idx
    ON inbox_items (company_id, recipient_user_id, updated_at DESC);
-- Serves the scoped bulk read: every item this recipient holds about one
-- entity. Without it that write scans the recipient's whole inbox to find the
-- rows for one conversation.
CREATE INDEX inbox_items_recipient_entity_idx
    ON inbox_items (company_id, recipient_user_id, entity_type, entity_id);
-- Opens one conversation without scanning unrelated notifications. The write
-- only targets unread, unarchived rows, so the partial predicate matches it.
CREATE INDEX inbox_items_recipient_conversation_unread_idx
    ON inbox_items (company_id, recipient_user_id, conversation_id, created_at DESC, id DESC)
    WHERE conversation_id IS NOT NULL AND read_at IS NULL AND archived_at IS NULL;

CREATE TABLE crash_reports (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    kind text NOT NULL,
    platform text NOT NULL,
    architecture text NOT NULL,
    app_version text NOT NULL,
    reason text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT crash_reports_pkey PRIMARY KEY (id),
    CONSTRAINT crash_reports_kind_check CHECK (kind IN ('crash', 'freeze'))
);

CREATE INDEX crash_reports_retention_idx
    ON crash_reports (created_at, id);

-- One shared inbound address per Company and its short-lived metadata/read
-- projection. Message bodies, raw email, attachment bytes, and signed download
-- URLs remain in the provider adapter, outside PostgreSQL.
CREATE TABLE company_mailboxes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    local_part text NOT NULL,
    created_by_user_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT company_mailboxes_pkey PRIMARY KEY (id),
    CONSTRAINT company_mailboxes_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT company_mailboxes_company_key UNIQUE (company_id),
    CONSTRAINT company_mailboxes_local_part_key UNIQUE (local_part)
);

CREATE TABLE mailbox_messages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    mailbox_id uuid NOT NULL,
    provider_email_id text NOT NULL,
    sender text NOT NULL,
    subject text NOT NULL,
    recipients text[] NOT NULL,
    received_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    read_at timestamp with time zone,
    has_attachments boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT mailbox_messages_pkey PRIMARY KEY (id),
    CONSTRAINT mailbox_messages_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT mailbox_messages_mailbox_provider_key UNIQUE (mailbox_id, provider_email_id),
    CONSTRAINT mailbox_messages_mailbox_fkey
        FOREIGN KEY (company_id, mailbox_id)
        REFERENCES company_mailboxes(company_id, id) ON DELETE CASCADE
);

CREATE INDEX mailbox_messages_company_list_idx
    ON mailbox_messages (company_id, received_at DESC, id DESC);
CREATE INDEX mailbox_messages_company_unread_idx
    ON mailbox_messages (company_id, received_at DESC, id DESC)
    WHERE read_at IS NULL;
CREATE INDEX mailbox_messages_retention_idx
    ON mailbox_messages (expires_at, id);
CREATE INDEX mailbox_messages_provider_email_idx
    ON mailbox_messages (provider_email_id, id);

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
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT mailbox_message_attachments_pkey PRIMARY KEY (id),
    CONSTRAINT mailbox_message_attachments_message_provider_key UNIQUE (
        message_id, provider_attachment_id
    ),
    CONSTRAINT mailbox_message_attachments_message_fkey
        FOREIGN KEY (company_id, message_id)
        REFERENCES mailbox_messages(company_id, id) ON DELETE CASCADE,
    CONSTRAINT mailbox_message_attachments_disposition_check CHECK (
        disposition IS NULL OR disposition IN ('attachment', 'inline')
    )
);

CREATE INDEX mailbox_message_attachments_message_idx
    ON mailbox_message_attachments (company_id, message_id, id);

CREATE TABLE mailbox_received_receipts (
    provider_event_id text NOT NULL,
    provider_email_id text NOT NULL,
    outcome text NOT NULL,
    received_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT mailbox_received_receipts_pkey PRIMARY KEY (provider_event_id),
    CONSTRAINT mailbox_received_receipts_provider_email_key UNIQUE (provider_email_id),
    CONSTRAINT mailbox_received_receipts_outcome_check CHECK (
        outcome IN ('delivered', 'ignored')
    )
);

CREATE INDEX mailbox_received_receipts_retention_idx
    ON mailbox_received_receipts (expires_at, provider_event_id);

-- Reviewable outbound drafts created by an Agent Run. Draft content follows
-- the Conversation lifecycle rather than inbound-message retention.
CREATE TABLE mailbox_draft_groups (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    mailbox_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    source_run_id bigint NOT NULL,
    request_key uuid NOT NULL,
    agent_id uuid NOT NULL,
    created_by_user_id uuid NOT NULL,
    payload_sha256 bytea NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT mailbox_draft_groups_pkey PRIMARY KEY (id),
    CONSTRAINT mailbox_draft_groups_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT mailbox_draft_groups_company_run_key UNIQUE (company_id, source_run_id),
    CONSTRAINT mailbox_draft_groups_mailbox_fkey
        FOREIGN KEY (company_id, mailbox_id)
        REFERENCES company_mailboxes(company_id, id) ON DELETE CASCADE,
    CONSTRAINT mailbox_draft_groups_conversation_fkey
        FOREIGN KEY (company_id, conversation_id)
        REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    CONSTRAINT mailbox_draft_groups_run_fkey
        FOREIGN KEY (company_id, source_run_id)
        REFERENCES runs(company_id, id) ON DELETE CASCADE,
    CONSTRAINT mailbox_draft_groups_agent_fkey
        FOREIGN KEY (company_id, agent_id)
        REFERENCES agents(company_id, id)
);

CREATE INDEX mailbox_draft_groups_conversation_created_idx
    ON mailbox_draft_groups (company_id, conversation_id, created_at, id);

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
    send_requested_at timestamp with time zone,
    send_lease_id uuid,
    send_lease_expires_at timestamp with time zone,
    send_attempts integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone,
    sent_by_user_id uuid,
    sent_at timestamp with time zone,
    failed_at timestamp with time zone,
    failure_reason text,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT mailbox_drafts_pkey PRIMARY KEY (id),
    CONSTRAINT mailbox_drafts_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT mailbox_drafts_group_ordinal_key UNIQUE (group_id, ordinal),
    CONSTRAINT mailbox_drafts_group_fkey
        FOREIGN KEY (company_id, group_id)
        REFERENCES mailbox_draft_groups(company_id, id) ON DELETE CASCADE,
    CONSTRAINT mailbox_drafts_status_check CHECK (
        status IN ('draft', 'sending', 'sent', 'failed')
    )
);

CREATE INDEX mailbox_drafts_group_idx
    ON mailbox_drafts (company_id, group_id, ordinal, id);
CREATE INDEX mailbox_drafts_send_queue_idx
    ON mailbox_drafts (next_attempt_at, created_at, id)
    WHERE status = 'sending';
CREATE INDEX mailbox_drafts_company_send_quota_idx
    ON mailbox_drafts (company_id, send_requested_at, id)
    WHERE status IN ('sending', 'sent');

CREATE TABLE mailbox_draft_deliveries (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    draft_id uuid NOT NULL,
    ordinal integer NOT NULL,
    to_addresses text[] NOT NULL,
    cc_addresses text[] NOT NULL,
    provider_email_id text,
    accepted_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT mailbox_draft_deliveries_pkey PRIMARY KEY (id),
    CONSTRAINT mailbox_draft_deliveries_draft_ordinal_key UNIQUE (draft_id, ordinal),
    CONSTRAINT mailbox_draft_deliveries_draft_fkey
        FOREIGN KEY (company_id, draft_id)
        REFERENCES mailbox_drafts(company_id, id) ON DELETE CASCADE
);

CREATE INDEX mailbox_draft_deliveries_draft_idx
    ON mailbox_draft_deliveries (company_id, draft_id, ordinal, id);

-- Company Commerce: Company-owned Stripe Connect account projections,
-- durable provider-operation recovery, an append-only cluster terms catalog,
-- and append-only acceptance receipts for the Zenith Team Commerce Addendum.
-- Provider secrets, AccountSession client secrets, bank details, raw KYC data,
-- and raw webhook payloads never enter PostgreSQL.

CREATE TABLE commerce_terms_catalog (
    terms_version text NOT NULL,
    terms_sha256 bytea NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT commerce_terms_catalog_pkey PRIMARY KEY (terms_version),
    CONSTRAINT commerce_terms_catalog_version_sha256_key UNIQUE (
        terms_version, terms_sha256
    ),
    CONSTRAINT commerce_terms_catalog_sha256_check CHECK (
        octet_length(terms_sha256) = 32
    )
);

CREATE TABLE commerce_terms_acceptances (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    terms_version text NOT NULL,
    terms_sha256 bytea NOT NULL,
    accepted_by_user_id uuid NOT NULL,
    accepted_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT commerce_terms_acceptances_pkey PRIMARY KEY (id),
    CONSTRAINT commerce_terms_acceptances_company_version_key UNIQUE (
        company_id, terms_version
    ),
    CONSTRAINT commerce_terms_acceptances_company_fkey
        FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT commerce_terms_acceptances_terms_fkey
        FOREIGN KEY (terms_version, terms_sha256)
        REFERENCES commerce_terms_catalog(terms_version, terms_sha256),
    CONSTRAINT commerce_terms_acceptances_sha256_check CHECK (
        octet_length(terms_sha256) = 32
    )
);

CREATE INDEX commerce_terms_acceptances_company_time_idx
    ON commerce_terms_acceptances (company_id, accepted_at DESC, id DESC);

CREATE INDEX commerce_terms_acceptances_terms_idx
    ON commerce_terms_acceptances (terms_version, terms_sha256);

CREATE TABLE commerce_accounts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    environment text NOT NULL,
    provider_account_id text,
    status text DEFAULT 'provisioning' NOT NULL,
    country text,
    default_currency text,
    details_submitted boolean DEFAULT false NOT NULL,
    charges_enabled boolean DEFAULT false NOT NULL,
    payouts_enabled boolean DEFAULT false NOT NULL,
    card_payments_status text,
    transfers_status text,
    requirements_currently_due_count integer DEFAULT 0 NOT NULL,
    requirements_eventually_due_count integer DEFAULT 0 NOT NULL,
    requirements_past_due_count integer DEFAULT 0 NOT NULL,
    requirements_deadline timestamp with time zone,
    disabled_reason text,
    controller_losses_payments text,
    controller_fees_payer text,
    controller_requirement_collection text,
    controller_dashboard_type text,
    controller_template_version integer,
    created_by_user_id uuid NOT NULL,
    reconciliation_generation bigint DEFAULT 0 NOT NULL,
    last_reconciled_at timestamp with time zone,
    disconnected_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT commerce_accounts_pkey PRIMARY KEY (id),
    CONSTRAINT commerce_accounts_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT commerce_accounts_company_id_environment_id_key UNIQUE (
        company_id, id, environment
    ),
    CONSTRAINT commerce_accounts_company_environment_key UNIQUE (
        company_id, environment
    ),
    CONSTRAINT commerce_accounts_company_environment_provider_key UNIQUE (
        company_id, environment, provider_account_id
    ),
    CONSTRAINT commerce_accounts_company_fkey
        FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT commerce_accounts_environment_check CHECK (
        environment IN ('sandbox', 'live')
    ),
    CONSTRAINT commerce_accounts_status_check CHECK (
        status IN (
            'provisioning', 'onboarding', 'pending_review', 'ready',
            'action_required', 'restricted', 'draining', 'disconnected'
        )
    ),
    CONSTRAINT commerce_accounts_card_payments_status_check CHECK (
        card_payments_status IS NULL
        OR card_payments_status IN ('active', 'inactive', 'pending', 'unrequested')
    ),
    CONSTRAINT commerce_accounts_transfers_status_check CHECK (
        transfers_status IS NULL
        OR transfers_status IN ('active', 'inactive', 'pending', 'unrequested')
    ),
    CONSTRAINT commerce_accounts_controller_losses_check CHECK (
        controller_losses_payments IS NULL OR controller_losses_payments = 'stripe'
    ),
    CONSTRAINT commerce_accounts_controller_fees_check CHECK (
        controller_fees_payer IS NULL OR controller_fees_payer = 'account'
    ),
    CONSTRAINT commerce_accounts_controller_requirements_check CHECK (
        controller_requirement_collection IS NULL
        OR controller_requirement_collection = 'stripe'
    ),
    CONSTRAINT commerce_accounts_controller_dashboard_check CHECK (
        controller_dashboard_type IS NULL OR controller_dashboard_type = 'full'
    )
);

CREATE UNIQUE INDEX commerce_accounts_provider_key
    ON commerce_accounts (environment, provider_account_id)
    WHERE provider_account_id IS NOT NULL;

CREATE INDEX commerce_accounts_company_status_idx
    ON commerce_accounts (company_id, status, environment);

CREATE TABLE commerce_operations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    commerce_account_id uuid NOT NULL,
    environment text NOT NULL,
    operation_type text NOT NULL,
    metadata_schema_version integer NOT NULL,
    state text DEFAULT 'pending' NOT NULL,
    request_sha256 bytea NOT NULL,
    idempotency_key text NOT NULL,
    provider_retry_deadline_at timestamp with time zone NOT NULL,
    lease_id uuid,
    lease_expires_at timestamp with time zone,
    attempt_count integer DEFAULT 0 NOT NULL,
    provider_attempt_count integer DEFAULT 0 NOT NULL,
    provider_first_attempted_at timestamp with time zone,
    provider_last_attempted_at timestamp with time zone,
    recovery_cursor text,
    recovery_candidate_provider_id text,
    provider_resource_id text,
    provider_confirmed_at timestamp with time zone,
    recovery_required_at timestamp with time zone,
    completed_at timestamp with time zone,
    last_error_code text,
    created_by_user_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT commerce_operations_pkey PRIMARY KEY (id),
    CONSTRAINT commerce_operations_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT commerce_operations_account_type_key UNIQUE (
        commerce_account_id, operation_type
    ),
    CONSTRAINT commerce_operations_idempotency_key UNIQUE (
        environment, idempotency_key
    ),
    CONSTRAINT commerce_operations_account_fkey
        FOREIGN KEY (company_id, commerce_account_id, environment)
        REFERENCES commerce_accounts(company_id, id, environment)
        ON DELETE CASCADE,
    CONSTRAINT commerce_operations_environment_check CHECK (
        environment IN ('sandbox', 'live')
    ),
    CONSTRAINT commerce_operations_type_check CHECK (
        operation_type = 'account_provisioning'
    ),
    CONSTRAINT commerce_operations_metadata_schema_check CHECK (
        metadata_schema_version = 1
    ),
    CONSTRAINT commerce_operations_state_check CHECK (
        state IN (
            'pending', 'leased', 'provider_confirmed', 'succeeded',
            'recovery_required'
        )
    ),
    CONSTRAINT commerce_operations_request_sha256_check CHECK (
        octet_length(request_sha256) = 32
    )
);

CREATE UNIQUE INDEX commerce_operations_provider_resource_key
    ON commerce_operations (environment, provider_resource_id)
    WHERE provider_resource_id IS NOT NULL;

CREATE INDEX commerce_operations_recovery_idx
    ON commerce_operations (state, lease_expires_at, created_at, id)
    WHERE state IN ('pending', 'leased', 'provider_confirmed', 'recovery_required');

CREATE INDEX commerce_operations_company_idx
    ON commerce_operations (company_id, environment, created_at, id);

-- Thin Commerce Gateway: the Company-owned Site authority, immutable
-- manifests and Offers, opaque customer mappings, durable Stripe operations,
-- payment/subscription/entitlement projections, and replay-safe webhook
-- receipts used by the thin Zenith Team Commerce Gateway.
--
-- Raw customer subjects, private Site signing keys, Stripe keys,
-- hosted-session URLs, raw webhook payloads, bank details, and KYC data are
-- deliberately not persisted.

CREATE TABLE commerce_sites (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    commerce_account_id uuid NOT NULL,
    environment text NOT NULL,
    site_alias text NOT NULL,
    origin text NOT NULL,
    verification_token_sha256 bytea NOT NULL,
    status text DEFAULT 'draft' NOT NULL,
    verified_at timestamp with time zone,
    portal_return_path text NOT NULL,
    provider_portal_configuration_id text,
    active_manifest_id uuid,
    created_by_user_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT commerce_sites_pkey PRIMARY KEY (id),
    CONSTRAINT commerce_sites_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT commerce_sites_company_id_environment_id_key UNIQUE (
        company_id, id, environment
    ),
    CONSTRAINT commerce_sites_company_alias_key UNIQUE (
        company_id, environment, site_alias
    ),
    CONSTRAINT commerce_sites_account_fkey
        FOREIGN KEY (company_id, commerce_account_id, environment)
        REFERENCES commerce_accounts(company_id, id, environment)
        ON DELETE CASCADE,
    CONSTRAINT commerce_sites_environment_check CHECK (
        environment IN ('sandbox', 'live')
    ),
    CONSTRAINT commerce_sites_verification_hash_check CHECK (
        octet_length(verification_token_sha256) = 32
    ),
    CONSTRAINT commerce_sites_status_check CHECK (
        status IN ('draft', 'active', 'disabled')
    )
);

CREATE INDEX commerce_sites_company_status_idx
    ON commerce_sites (company_id, environment, status, created_at, id);

CREATE INDEX commerce_sites_account_idx
    ON commerce_sites (company_id, commerce_account_id, environment);

-- Drafts have no authority and therefore cannot reserve another tenant's
-- public origin. Sandbox loopback origins are local to each developer and
-- intentionally do not reserve localhost across Companies.
CREATE UNIQUE INDEX commerce_sites_active_origin_key
	ON commerce_sites (environment, origin)
	WHERE status = 'active'
	  AND (environment <> 'sandbox' OR origin LIKE 'https://%');

-- Zenith Team stores public P-256 keys only. Partial unique indexes enforce the
-- one-active/one-retiring rotation window for each Site.
CREATE TABLE commerce_site_signing_keys (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    site_id uuid NOT NULL,
    environment text NOT NULL,
    key_id text NOT NULL,
    fingerprint text NOT NULL,
    public_jwk jsonb NOT NULL,
    state text DEFAULT 'pending' NOT NULL,
    activated_at timestamp with time zone,
    retire_at timestamp with time zone,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT commerce_site_signing_keys_pkey PRIMARY KEY (id),
    CONSTRAINT commerce_site_signing_keys_site_fingerprint_key UNIQUE (
        site_id, fingerprint
    ),
    CONSTRAINT commerce_site_signing_keys_site_key_id_key UNIQUE (
        site_id, key_id
    ),
    CONSTRAINT commerce_site_signing_keys_site_fkey
        FOREIGN KEY (company_id, site_id, environment)
        REFERENCES commerce_sites(company_id, id, environment) ON DELETE CASCADE,
    CONSTRAINT commerce_site_signing_keys_environment_check CHECK (
        environment IN ('sandbox', 'live')
    ),
    CONSTRAINT commerce_site_signing_keys_identity_check CHECK (
        key_id = fingerprint
        AND fingerprint ~ '^[A-Za-z0-9_-]{43}$'
    ),
    CONSTRAINT commerce_site_signing_keys_jwk_check CHECK (
        jsonb_typeof(public_jwk) = 'object'
    ),
    CONSTRAINT commerce_site_signing_keys_state_check CHECK (
        (state = 'pending'
            AND activated_at IS NULL AND retire_at IS NULL AND revoked_at IS NULL)
        OR (state = 'active'
            AND activated_at IS NOT NULL AND retire_at IS NULL AND revoked_at IS NULL)
        OR (state = 'retiring'
            AND activated_at IS NOT NULL AND retire_at IS NOT NULL AND revoked_at IS NULL)
        OR (state = 'revoked'
            AND revoked_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX commerce_site_signing_keys_active_site_key
    ON commerce_site_signing_keys (site_id)
    WHERE state = 'active';

CREATE UNIQUE INDEX commerce_site_signing_keys_pending_site_key
    ON commerce_site_signing_keys (site_id)
    WHERE state = 'pending';

CREATE UNIQUE INDEX commerce_site_signing_keys_retiring_site_key
    ON commerce_site_signing_keys (site_id)
    WHERE state = 'retiring';

CREATE INDEX commerce_site_signing_keys_verification_idx
    ON commerce_site_signing_keys (site_id, key_id, state, retire_at);

-- Checkout and Portal challenges are one-time proof-of-possession nonces.
-- Reissuing the same logical operation atomically invalidates its old nonce.
CREATE TABLE commerce_site_challenges (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    site_id uuid NOT NULL,
    environment text NOT NULL,
    action text NOT NULL,
    operation_id uuid NOT NULL,
    nonce_sha256 bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT commerce_site_challenges_pkey PRIMARY KEY (id),
    CONSTRAINT commerce_site_challenges_nonce_key UNIQUE (nonce_sha256),
    CONSTRAINT commerce_site_challenges_operation_key UNIQUE (
        site_id, action, operation_id
    ),
    CONSTRAINT commerce_site_challenges_site_fkey
        FOREIGN KEY (company_id, site_id, environment)
        REFERENCES commerce_sites(company_id, id, environment) ON DELETE CASCADE,
    CONSTRAINT commerce_site_challenges_environment_check CHECK (
        environment IN ('sandbox', 'live')
    ),
    CONSTRAINT commerce_site_challenges_action_check CHECK (
        action IN ('checkout', 'portal')
    ),
    CONSTRAINT commerce_site_challenges_nonce_check CHECK (
        octet_length(nonce_sha256) = 32
    )
);

CREATE INDEX commerce_site_challenges_expiry_idx
    ON commerce_site_challenges (expires_at, id)
    WHERE consumed_at IS NULL;

CREATE INDEX commerce_site_challenges_consumed_idx
    ON commerce_site_challenges (consumed_at, id)
    WHERE consumed_at IS NOT NULL;

CREATE TABLE commerce_manifests (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    site_id uuid NOT NULL,
    task_id uuid NOT NULL,
    run_id bigint NOT NULL,
    request_key uuid NOT NULL,
    schema_version integer NOT NULL,
    canonical_json jsonb NOT NULL,
    digest bytea NOT NULL,
    state text DEFAULT 'draft' NOT NULL,
    approved_by_user_id uuid,
    approval_session_id uuid,
    approved_at timestamp with time zone,
    applied_at timestamp with time zone,
    apply_error_code text,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT commerce_manifests_pkey PRIMARY KEY (id),
    CONSTRAINT commerce_manifests_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT commerce_manifests_company_site_id_key UNIQUE (
        company_id, site_id, id
    ),
    CONSTRAINT commerce_manifests_site_run_digest_key UNIQUE (site_id, run_id, digest),
    CONSTRAINT commerce_manifests_run_request_key UNIQUE (
        company_id, run_id, request_key
    ),
    CONSTRAINT commerce_manifests_site_fkey
        FOREIGN KEY (company_id, site_id)
        REFERENCES commerce_sites(company_id, id) ON DELETE CASCADE,
    CONSTRAINT commerce_manifests_schema_version_check CHECK (
        schema_version = 2
    ),
    CONSTRAINT commerce_manifests_document_check CHECK (
        jsonb_typeof(canonical_json) = 'object'
    ),
    CONSTRAINT commerce_manifests_digest_check CHECK (
        octet_length(digest) = 32
    ),
    CONSTRAINT commerce_manifests_state_check CHECK (
        state IN (
            'draft', 'publishing', 'active', 'rejected', 'apply_failed',
            'superseded'
        )
    )
);

CREATE INDEX commerce_manifests_site_state_idx
    ON commerce_manifests (company_id, site_id, state, created_at DESC, id DESC);

CREATE UNIQUE INDEX commerce_manifests_active_site_key
    ON commerce_manifests (company_id, site_id)
    WHERE state = 'active';

CREATE INDEX commerce_manifests_task_run_idx
    ON commerce_manifests (company_id, task_id, run_id, created_at DESC, id DESC);

CREATE INDEX commerce_manifests_company_created_idx
    ON commerce_manifests (company_id, created_at DESC, id DESC);

-- Commerce owns the immutable manifest; approval owns the human decision and
-- continuation receipt. This typed edge keeps one exact approval companion per
-- manifest without introducing a polymorphic, unenforceable subject pointer.
ALTER TABLE approvals
    ADD COLUMN commerce_manifest_id uuid,
    ADD CONSTRAINT approvals_commerce_manifest_fkey
        FOREIGN KEY (company_id, commerce_manifest_id)
        REFERENCES commerce_manifests(company_id, id) ON DELETE CASCADE,
    ADD CONSTRAINT approvals_commerce_manifest_key
        UNIQUE (company_id, commerce_manifest_id),
    ADD CONSTRAINT approvals_commerce_manifest_shape_check CHECK (
        (type = 'company_plan_apply' AND commerce_manifest_id IS NULL)
        OR (type = 'commerce_manifest_publish' AND commerce_manifest_id IS NOT NULL)
    );

CREATE TABLE commerce_offers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    site_id uuid NOT NULL,
    manifest_id uuid NOT NULL,
    environment text NOT NULL,
    alias text NOT NULL,
    display_name text NOT NULL,
    offer_type text NOT NULL,
    plan_group_alias text,
    entitlement_key text NOT NULL,
    amount bigint NOT NULL,
    currency text NOT NULL,
    recurring_interval text,
    automatic_tax boolean DEFAULT false NOT NULL,
    success_path text NOT NULL,
    cancel_path text NOT NULL,
    provider_product_id text,
    provider_price_id text,
    status text DEFAULT 'publishing' NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT commerce_offers_pkey PRIMARY KEY (id),
    CONSTRAINT commerce_offers_company_site_id_key UNIQUE (
        company_id, site_id, id
    ),
    CONSTRAINT commerce_offers_company_site_environment_id_key UNIQUE (
        company_id, site_id, id, environment
    ),
    CONSTRAINT commerce_offers_entitlement_source_key UNIQUE (
        company_id, site_id, id, offer_type, entitlement_key
    ),
    CONSTRAINT commerce_offers_manifest_alias_key UNIQUE (manifest_id, alias),
    CONSTRAINT commerce_offers_site_fkey
        FOREIGN KEY (company_id, site_id, environment)
        REFERENCES commerce_sites(company_id, id, environment)
        ON DELETE CASCADE,
    CONSTRAINT commerce_offers_manifest_fkey
        FOREIGN KEY (company_id, site_id, manifest_id)
        REFERENCES commerce_manifests(company_id, site_id, id)
        ON DELETE CASCADE,
    CONSTRAINT commerce_offers_environment_check CHECK (
        environment IN ('sandbox', 'live')
    ),
    CONSTRAINT commerce_offers_type_check CHECK (
        offer_type IN ('one_time', 'subscription')
    ),
    CONSTRAINT commerce_offers_billing_shape_check CHECK (
        (
            offer_type = 'one_time'
            AND plan_group_alias IS NULL
            AND recurring_interval IS NULL
        ) OR (
            offer_type = 'subscription'
            AND plan_group_alias IS NOT NULL
            AND recurring_interval IS NOT NULL
            AND recurring_interval IN ('month', 'year')
        )
    ),
    CONSTRAINT commerce_offers_status_check CHECK (
        status IN ('publishing', 'active', 'disabled')
    )
);

CREATE UNIQUE INDEX commerce_offers_active_alias_key
    ON commerce_offers (site_id, alias)
    WHERE status = 'active';

CREATE UNIQUE INDEX commerce_offers_provider_price_key
    ON commerce_offers (environment, provider_price_id)
    WHERE provider_price_id IS NOT NULL;

CREATE INDEX commerce_offers_site_status_idx
    ON commerce_offers (company_id, site_id, status, alias);

CREATE TABLE commerce_customers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    site_id uuid NOT NULL,
    environment text NOT NULL,
    subject_hmac bytea NOT NULL,
    provider_customer_id text,
    provider_first_attempted_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT commerce_customers_pkey PRIMARY KEY (id),
    CONSTRAINT commerce_customers_company_site_id_key UNIQUE (
        company_id, site_id, id
    ),
    CONSTRAINT commerce_customers_company_site_environment_id_key UNIQUE (
        company_id, site_id, id, environment
    ),
    CONSTRAINT commerce_customers_site_subject_key UNIQUE (site_id, subject_hmac),
    CONSTRAINT commerce_customers_site_fkey
        FOREIGN KEY (company_id, site_id, environment)
        REFERENCES commerce_sites(company_id, id, environment)
        ON DELETE CASCADE,
    CONSTRAINT commerce_customers_environment_check CHECK (
        environment IN ('sandbox', 'live')
    ),
    CONSTRAINT commerce_customers_subject_hmac_check CHECK (
        octet_length(subject_hmac) = 32
    )
);

CREATE UNIQUE INDEX commerce_customers_provider_key
    ON commerce_customers (environment, provider_customer_id)
    WHERE provider_customer_id IS NOT NULL;

CREATE TABLE commerce_rate_limits (
    company_id uuid NOT NULL,
    site_id uuid NOT NULL,
    subject_hmac bytea NOT NULL,
    action text NOT NULL,
    window_started_at timestamp with time zone NOT NULL,
    request_count integer NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT commerce_rate_limits_pkey PRIMARY KEY (
        company_id, site_id, subject_hmac, action
    ),
    CONSTRAINT commerce_rate_limits_site_fkey
        FOREIGN KEY (company_id, site_id)
        REFERENCES commerce_sites(company_id, id) ON DELETE CASCADE,
    CONSTRAINT commerce_rate_limits_subject_hmac_check CHECK (
        octet_length(subject_hmac) = 32
    ),
    CONSTRAINT commerce_rate_limits_action_check CHECK (
        action IN (
            'checkout', 'portal', 'entitlements',
            'site_checkout', 'site_portal', 'site_entitlements',
            'challenge_site', 'challenge_ip'
        )
    )
);

CREATE INDEX commerce_rate_limits_updated_idx
    ON commerce_rate_limits (updated_at, company_id, site_id, action);

CREATE TABLE commerce_gateway_operations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    site_id uuid NOT NULL,
    customer_id uuid,
    manifest_id uuid,
    offer_id uuid,
    environment text NOT NULL,
    operation_type text NOT NULL,
    operation_id uuid NOT NULL,
    request_sha256 bytea NOT NULL,
    idempotency_key text NOT NULL,
    state text DEFAULT 'pending' NOT NULL,
    lease_id uuid,
    lease_expires_at timestamp with time zone,
    attempt_count integer DEFAULT 0 NOT NULL,
    provider_attempt_count integer DEFAULT 0 NOT NULL,
    provider_resource_id text,
    provider_confirmed_at timestamp with time zone,
    completed_at timestamp with time zone,
    last_error_code text,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT commerce_gateway_operations_pkey PRIMARY KEY (id),
    CONSTRAINT commerce_gateway_operations_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT commerce_gateway_operations_request_key UNIQUE (
        site_id, operation_type, operation_id
    ),
    CONSTRAINT commerce_gateway_operations_idempotency_key UNIQUE (
        environment, idempotency_key
    ),
    CONSTRAINT commerce_gateway_operations_site_fkey
        FOREIGN KEY (company_id, site_id, environment)
        REFERENCES commerce_sites(company_id, id, environment)
        ON DELETE CASCADE,
    CONSTRAINT commerce_gateway_operations_customer_fkey
        FOREIGN KEY (company_id, site_id, customer_id, environment)
        REFERENCES commerce_customers(company_id, site_id, id, environment)
        ON DELETE SET NULL (customer_id),
    CONSTRAINT commerce_gateway_operations_manifest_fkey
        FOREIGN KEY (company_id, site_id, manifest_id)
        REFERENCES commerce_manifests(company_id, site_id, id)
        ON DELETE SET NULL (manifest_id),
    CONSTRAINT commerce_gateway_operations_offer_fkey
        FOREIGN KEY (company_id, site_id, offer_id, environment)
        REFERENCES commerce_offers(company_id, site_id, id, environment)
        ON DELETE SET NULL (offer_id),
    CONSTRAINT commerce_gateway_operations_environment_check CHECK (
        environment IN ('sandbox', 'live')
    ),
    CONSTRAINT commerce_gateway_operations_type_check CHECK (
        operation_type IN ('catalog_publish', 'checkout', 'portal')
    ),
    CONSTRAINT commerce_gateway_operations_request_sha_check CHECK (
        octet_length(request_sha256) = 32
    ),
    CONSTRAINT commerce_gateway_operations_state_check CHECK (
        state IN (
            'pending', 'leased', 'provider_confirmed', 'succeeded',
            'recovery_required', 'failed'
        )
    )
);

CREATE INDEX commerce_gateway_operations_recovery_idx
    ON commerce_gateway_operations (state, lease_expires_at, created_at, id)
    WHERE state IN ('pending', 'leased', 'provider_confirmed', 'recovery_required');

CREATE INDEX commerce_gateway_operations_customer_idx
    ON commerce_gateway_operations (company_id, site_id, customer_id, created_at DESC, id DESC);

CREATE INDEX commerce_gateway_operations_manifest_idx
    ON commerce_gateway_operations (company_id, site_id, manifest_id)
    WHERE manifest_id IS NOT NULL;

CREATE INDEX commerce_gateway_operations_offer_idx
    ON commerce_gateway_operations (company_id, site_id, offer_id, environment)
    WHERE offer_id IS NOT NULL;

CREATE TABLE commerce_payments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    site_id uuid NOT NULL,
    customer_id uuid NOT NULL,
    offer_id uuid NOT NULL,
    subscription_id uuid,
    environment text NOT NULL,
    provider_payment_intent_id text,
    provider_invoice_payment_id text,
    provider_invoice_id text,
    provider_checkout_session_id text,
    status text NOT NULL,
    amount bigint NOT NULL,
    currency text NOT NULL,
    paid_at timestamp with time zone,
    refunded_at timestamp with time zone,
    disputed_at timestamp with time zone,
	provider_event_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT commerce_payments_pkey PRIMARY KEY (id),
    CONSTRAINT commerce_payments_company_site_id_key UNIQUE (
        company_id, site_id, id
    ),
    CONSTRAINT commerce_payments_source_identity_key UNIQUE (
        company_id, site_id, customer_id, offer_id, id
    ),
    CONSTRAINT commerce_payments_subscription_source_identity_key UNIQUE (
        company_id, site_id, customer_id, offer_id, subscription_id, id
    ),
    CONSTRAINT commerce_payments_customer_fkey
        FOREIGN KEY (company_id, site_id, customer_id, environment)
        REFERENCES commerce_customers(company_id, site_id, id, environment)
        ON DELETE CASCADE,
    CONSTRAINT commerce_payments_offer_fkey
        FOREIGN KEY (company_id, site_id, offer_id, environment)
        REFERENCES commerce_offers(company_id, site_id, id, environment),
    CONSTRAINT commerce_payments_environment_check CHECK (
        environment IN ('sandbox', 'live')
    ),
    CONSTRAINT commerce_payments_status_check CHECK (
        status IN ('processing', 'paid', 'failed', 'refunded', 'disputed')
    ),
    CONSTRAINT commerce_payments_provider_shape_check CHECK (
        (
            subscription_id IS NULL
            AND provider_invoice_payment_id IS NULL
            AND provider_invoice_id IS NULL
        ) OR (
            subscription_id IS NOT NULL
            AND provider_invoice_payment_id IS NOT NULL
            AND provider_invoice_id IS NOT NULL
            AND provider_payment_intent_id IS NOT NULL
        )
    )
);

CREATE UNIQUE INDEX commerce_payments_intent_key
    ON commerce_payments (environment, provider_payment_intent_id)
    WHERE provider_payment_intent_id IS NOT NULL
      AND provider_invoice_payment_id IS NULL;

CREATE UNIQUE INDEX commerce_payments_invoice_payment_key
    ON commerce_payments (environment, provider_invoice_payment_id)
    WHERE provider_invoice_payment_id IS NOT NULL;

CREATE UNIQUE INDEX commerce_payments_checkout_key
    ON commerce_payments (environment, provider_checkout_session_id)
    WHERE provider_checkout_session_id IS NOT NULL;

CREATE INDEX commerce_payments_customer_idx
    ON commerce_payments (company_id, site_id, customer_id, created_at DESC, id DESC);

CREATE INDEX commerce_payments_offer_idx
    ON commerce_payments (company_id, site_id, offer_id, environment);

CREATE INDEX commerce_payments_subscription_idx
    ON commerce_payments (
        company_id, site_id, customer_id, subscription_id
    )
    WHERE subscription_id IS NOT NULL;

CREATE INDEX commerce_payments_subscription_invoice_idx
    ON commerce_payments (
        company_id, site_id, customer_id, subscription_id,
        provider_invoice_id, status
    )
    WHERE subscription_id IS NOT NULL
      AND provider_invoice_id IS NOT NULL;

CREATE TABLE commerce_subscriptions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    site_id uuid NOT NULL,
    customer_id uuid NOT NULL,
    offer_id uuid NOT NULL,
    environment text NOT NULL,
    provider_subscription_id text NOT NULL,
    provider_latest_invoice_id text,
    status text NOT NULL,
    cancel_at_period_end boolean DEFAULT false NOT NULL,
    current_period_start timestamp with time zone,
    current_period_end timestamp with time zone,
	provider_event_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT commerce_subscriptions_pkey PRIMARY KEY (id),
    CONSTRAINT commerce_subscriptions_company_site_id_key UNIQUE (
        company_id, site_id, id
    ),
    CONSTRAINT commerce_subscriptions_customer_identity_key UNIQUE (
        company_id, site_id, customer_id, id
    ),
    CONSTRAINT commerce_subscriptions_provider_key UNIQUE (
        environment, provider_subscription_id
    ),
    CONSTRAINT commerce_subscriptions_customer_fkey
        FOREIGN KEY (company_id, site_id, customer_id, environment)
        REFERENCES commerce_customers(company_id, site_id, id, environment)
        ON DELETE CASCADE,
    CONSTRAINT commerce_subscriptions_offer_fkey
        FOREIGN KEY (company_id, site_id, offer_id, environment)
        REFERENCES commerce_offers(company_id, site_id, id, environment),
    CONSTRAINT commerce_subscriptions_environment_check CHECK (
        environment IN ('sandbox', 'live')
    ),
    CONSTRAINT commerce_subscriptions_status_check CHECK (
        status IN (
            'incomplete', 'incomplete_expired', 'trialing', 'active',
            'past_due', 'canceled', 'unpaid', 'paused'
        )
    )
);

CREATE INDEX commerce_subscriptions_customer_idx
    ON commerce_subscriptions (company_id, site_id, customer_id, status, updated_at DESC, id DESC);

CREATE INDEX commerce_subscriptions_offer_idx
    ON commerce_subscriptions (company_id, site_id, offer_id, environment);

CREATE TABLE commerce_entitlements (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    site_id uuid NOT NULL,
    customer_id uuid NOT NULL,
    offer_id uuid NOT NULL,
    payment_id uuid,
    subscription_id uuid,
    grant_id uuid NOT NULL,
    entitlement_key text NOT NULL,
    source_type text NOT NULL,
    status text NOT NULL,
    starts_at timestamp with time zone,
    expires_at timestamp with time zone,
    revoked_reason text,
	provider_event_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT commerce_entitlements_pkey PRIMARY KEY (id),
    CONSTRAINT commerce_entitlements_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT commerce_entitlements_grant_key UNIQUE (
        site_id, customer_id, source_type, grant_id, entitlement_key
    ),
    CONSTRAINT commerce_entitlements_customer_fkey
        FOREIGN KEY (company_id, site_id, customer_id)
        REFERENCES commerce_customers(company_id, site_id, id)
        ON DELETE CASCADE,
    CONSTRAINT commerce_entitlements_offer_fkey
        FOREIGN KEY (
            company_id, site_id, offer_id, source_type, entitlement_key
        )
        REFERENCES commerce_offers(
            company_id, site_id, id, offer_type, entitlement_key
        ),
    CONSTRAINT commerce_entitlements_payment_fkey
        FOREIGN KEY (company_id, site_id, customer_id, offer_id, payment_id)
        REFERENCES commerce_payments(
            company_id, site_id, customer_id, offer_id, id
        ),
    CONSTRAINT commerce_entitlements_subscription_fkey
        FOREIGN KEY (company_id, site_id, customer_id, subscription_id)
        REFERENCES commerce_subscriptions(
            company_id, site_id, customer_id, id
        ),
    CONSTRAINT commerce_entitlements_subscription_payment_fkey
        FOREIGN KEY (
            company_id, site_id, customer_id, offer_id,
            subscription_id, payment_id
        )
        REFERENCES commerce_payments(
            company_id, site_id, customer_id, offer_id,
            subscription_id, id
        ),
    CONSTRAINT commerce_entitlements_source_type_check CHECK (
        source_type IN ('one_time', 'subscription')
    ),
    CONSTRAINT commerce_entitlements_source_identity_check CHECK (
        (
            source_type = 'one_time'
            AND payment_id IS NOT NULL
            AND subscription_id IS NULL
            AND grant_id = payment_id
        ) OR (
            source_type = 'subscription'
            AND subscription_id IS NOT NULL
            AND grant_id = subscription_id
        )
    ),
    CONSTRAINT commerce_entitlements_status_check CHECK (
        status IN ('processing', 'active', 'denied')
    ),
    CONSTRAINT commerce_entitlements_revoked_reason_check CHECK (
        revoked_reason IS NULL
        OR revoked_reason IN (
            'payment_failed', 'refunded', 'dispute_lost', 'subscription_ended',
            'offer_disabled', 'plan_changed'
        )
    )
);

CREATE INDEX commerce_entitlements_verify_idx
    ON commerce_entitlements (
        company_id, site_id, customer_id, entitlement_key, status,
        provider_event_at DESC, id DESC
    );

CREATE INDEX commerce_entitlements_offer_idx
    ON commerce_entitlements (
        company_id, site_id, offer_id, source_type, entitlement_key
    );

CREATE INDEX commerce_entitlements_payment_idx
    ON commerce_entitlements (
        company_id, site_id, customer_id, offer_id, payment_id
    )
    WHERE payment_id IS NOT NULL;

CREATE INDEX commerce_entitlements_subscription_idx
    ON commerce_entitlements (
        company_id, site_id, customer_id, subscription_id
    )
    WHERE subscription_id IS NOT NULL;

CREATE INDEX commerce_entitlements_subscription_payment_idx
    ON commerce_entitlements (
        company_id, site_id, customer_id, offer_id,
        subscription_id, payment_id
    )
    WHERE subscription_id IS NOT NULL AND payment_id IS NOT NULL;

CREATE TABLE commerce_webhook_account_leases (
    company_id uuid NOT NULL,
    environment text NOT NULL,
    provider_account_id text NOT NULL,
    lease_id uuid NOT NULL,
    lease_expires_at timestamp with time zone NOT NULL,
    acquired_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT commerce_webhook_account_leases_pkey PRIMARY KEY (
        company_id, environment, provider_account_id
    ),
    CONSTRAINT commerce_webhook_account_leases_account_fkey
        FOREIGN KEY (company_id, environment, provider_account_id)
        REFERENCES commerce_accounts(company_id, environment, provider_account_id)
        ON DELETE CASCADE,
    CONSTRAINT commerce_webhook_account_leases_environment_check CHECK (
        environment IN ('sandbox', 'live')
    )
);

CREATE INDEX commerce_webhook_account_leases_expiry_idx
    ON commerce_webhook_account_leases (lease_expires_at, company_id);

CREATE TABLE commerce_webhook_receipts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    environment text NOT NULL,
    provider_event_id text NOT NULL,
    provider_account_id text NOT NULL,
    event_type text NOT NULL,
    provider_object_id text,
    payload_sha256 bytea NOT NULL,
    livemode boolean NOT NULL,
    provider_created_at timestamp with time zone NOT NULL,
    state text DEFAULT 'received' NOT NULL,
    lease_id uuid,
    lease_expires_at timestamp with time zone,
    attempt_count integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    last_error_code text,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT commerce_webhook_receipts_pkey PRIMARY KEY (id),
    CONSTRAINT commerce_webhook_receipts_account_fkey
        FOREIGN KEY (company_id, environment, provider_account_id)
        REFERENCES commerce_accounts(company_id, environment, provider_account_id)
        ON DELETE CASCADE,
    CONSTRAINT commerce_webhook_receipts_provider_key UNIQUE (
        environment, provider_event_id
    ),
    CONSTRAINT commerce_webhook_receipts_environment_check CHECK (
        environment IN ('sandbox', 'live')
    ),
    CONSTRAINT commerce_webhook_receipts_livemode_check CHECK (
        livemode = (environment = 'live')
    ),
    CONSTRAINT commerce_webhook_receipts_payload_hash_check CHECK (
        octet_length(payload_sha256) = 32
    ),
    CONSTRAINT commerce_webhook_receipts_state_check CHECK (
        state IN ('received', 'processing', 'succeeded', 'failed', 'dead_letter')
    )
);

CREATE INDEX commerce_webhook_receipts_retry_idx
    ON commerce_webhook_receipts (next_attempt_at, provider_created_at, id)
    WHERE state IN ('received', 'failed');

CREATE INDEX commerce_webhook_receipts_expired_lease_idx
    ON commerce_webhook_receipts (lease_expires_at, provider_created_at, id)
    WHERE state = 'processing';

CREATE INDEX commerce_webhook_receipts_company_idx
    ON commerce_webhook_receipts (company_id, environment, provider_created_at DESC, id DESC);

CREATE INDEX commerce_webhook_receipts_account_idx
    ON commerce_webhook_receipts (company_id, environment, provider_account_id);

ALTER TABLE commerce_sites
    ADD CONSTRAINT commerce_sites_active_manifest_fkey
    FOREIGN KEY (company_id, id, active_manifest_id)
    REFERENCES commerce_manifests(company_id, site_id, id)
    ON DELETE SET NULL (active_manifest_id);

ALTER TABLE commerce_payments
    ADD CONSTRAINT commerce_payments_subscription_fkey
    FOREIGN KEY (company_id, site_id, customer_id, subscription_id)
    REFERENCES commerce_subscriptions(
        company_id, site_id, customer_id, id
    )
    ON DELETE SET NULL (subscription_id);
