-- name: AuthorizeCommercePrincipal :one
SELECT company_members.role
FROM companies
JOIN company_members
  ON company_members.company_id = companies.id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
JOIN users
  ON users.id = company_members.user_id
 AND users.disabled_at IS NULL
 AND users.deleted_at IS NULL
JOIN auth_sessions
  ON auth_sessions.id = sqlc.arg(session_id)::uuid
 AND auth_sessions.user_id = users.id
 AND auth_sessions.revoked_at IS NULL
 AND auth_sessions.expires_at > clock_timestamp()
WHERE companies.id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL;

-- name: ListCommerceAccountsForOwner :many
SELECT commerce_accounts.*
FROM commerce_accounts
JOIN companies
  ON companies.id = commerce_accounts.company_id
 AND companies.deleting_at IS NULL
JOIN company_members AS owner
  ON owner.company_id = commerce_accounts.company_id
 AND owner.user_id = sqlc.arg(actor_user_id)::uuid
 AND owner.status = 'active'
 AND owner.role = ANY(sqlc.arg(actor_roles)::text[])
JOIN users
  ON users.id = owner.user_id
 AND users.disabled_at IS NULL
 AND users.deleted_at IS NULL
JOIN auth_sessions
  ON auth_sessions.id = sqlc.arg(session_id)::uuid
 AND auth_sessions.user_id = users.id
 AND auth_sessions.revoked_at IS NULL
 AND auth_sessions.expires_at > clock_timestamp()
WHERE commerce_accounts.company_id = sqlc.arg(company_id)::uuid
ORDER BY commerce_accounts.environment;

-- name: InsertCommerceTermsCatalog :one
INSERT INTO commerce_terms_catalog (terms_version, terms_sha256)
VALUES (
    sqlc.arg(terms_version)::text,
    sqlc.arg(terms_sha256)::bytea
)
ON CONFLICT (terms_version) DO NOTHING
RETURNING *;

-- name: GetCommerceTermsCatalog :one
SELECT *
FROM commerce_terms_catalog
WHERE terms_version = sqlc.arg(terms_version)::text;

-- name: InsertCommerceTermsAcceptance :one
INSERT INTO commerce_terms_acceptances (
    company_id, terms_version, terms_sha256, accepted_by_user_id
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(terms_version)::text,
    sqlc.arg(terms_sha256)::bytea,
    sqlc.arg(accepted_by_user_id)::uuid
)
ON CONFLICT (company_id, terms_version) DO NOTHING
RETURNING *;

-- name: GetCommerceTermsAcceptance :one
SELECT *
FROM commerce_terms_acceptances
WHERE company_id = sqlc.arg(company_id)::uuid
  AND terms_version = sqlc.arg(terms_version)::text;

-- name: GetCommerceAccountForUpdate :one
SELECT *
FROM commerce_accounts
WHERE company_id = sqlc.arg(company_id)::uuid
  AND environment = sqlc.arg(environment)::text
FOR UPDATE;

-- name: GetCommerceAccountByCompanyEnvironment :one
SELECT *
FROM commerce_accounts
WHERE company_id = sqlc.arg(company_id)::uuid
  AND environment = sqlc.arg(environment)::text;

-- name: GetCommerceAccountByIdentity :one
SELECT *
FROM commerce_accounts
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(account_id)::uuid
  AND environment = sqlc.arg(environment)::text;

-- name: InsertCommerceAccountProvisioning :one
INSERT INTO commerce_accounts (
    company_id, environment, created_by_user_id
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(environment)::text,
    sqlc.arg(created_by_user_id)::uuid
)
ON CONFLICT (company_id, environment) DO NOTHING
RETURNING *;

-- name: InsertCommerceAccountProvisioningOperation :one
WITH operation_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
),
operation_identity AS MATERIALIZED (
    SELECT gen_random_uuid() AS id
)
INSERT INTO commerce_operations (
    id, company_id, commerce_account_id, environment, operation_type,
    metadata_schema_version, request_sha256, idempotency_key,
    provider_retry_deadline_at, created_by_user_id, created_at, updated_at
)
SELECT
    operation_identity.id,
    sqlc.arg(company_id)::uuid,
    sqlc.arg(account_id)::uuid,
    sqlc.arg(environment)::text,
    'account_provisioning',
    sqlc.arg(metadata_schema_version)::integer,
    digest(
        concat_ws(
            E'\n',
            'boardly-commerce-account-provisioning-v1',
            'operation_id=' || operation_identity.id::text,
            'company_id=' || sqlc.arg(company_id)::uuid::text,
            'commerce_account_id=' || sqlc.arg(account_id)::uuid::text,
            'environment=' || sqlc.arg(environment)::text,
            'metadata_schema_version=1',
            'controller_template_version=1',
            'controller_losses_payments=stripe',
            'controller_fees_payer=account',
            'controller_requirement_collection=stripe',
            'controller_dashboard_type=full'
        ),
        'sha256'
    ),
    'boardly:connect-account:v1:' || operation_identity.id::text,
    operation_clock.now_at + interval '23 hours',
    sqlc.arg(created_by_user_id)::uuid,
    operation_clock.now_at,
    operation_clock.now_at
FROM operation_clock
CROSS JOIN operation_identity
ON CONFLICT (commerce_account_id, operation_type) DO NOTHING
RETURNING *;

-- name: GetCommerceAccountProvisioningOperation :one
SELECT *
FROM commerce_operations
WHERE company_id = sqlc.arg(company_id)::uuid
  AND commerce_account_id = sqlc.arg(account_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND operation_type = 'account_provisioning';

-- name: GetCommerceAccountProvisioningOperationForUpdate :one
SELECT *
FROM commerce_operations
WHERE company_id = sqlc.arg(company_id)::uuid
  AND commerce_account_id = sqlc.arg(account_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND operation_type = 'account_provisioning'
FOR UPDATE;

-- name: GetCommerceOperationByIdentity :one
SELECT *
FROM commerce_operations
WHERE id = sqlc.arg(operation_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND commerce_account_id = sqlc.arg(account_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND operation_type = 'account_provisioning';

-- name: ClaimCommerceAccountProvisioningOperation :one
WITH lease_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE commerce_operations
SET state = 'leased',
    lease_id = sqlc.arg(lease_id)::uuid,
    lease_expires_at = (SELECT now_at FROM lease_clock)
        + sqlc.arg(lease_duration_milliseconds)::bigint * interval '1 millisecond',
    attempt_count = commerce_operations.attempt_count + 1,
    last_error_code = NULL,
    updated_at = (SELECT now_at FROM lease_clock),
    version = commerce_operations.version + 1
WHERE id = sqlc.arg(operation_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND commerce_account_id = sqlc.arg(account_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND operation_type = 'account_provisioning'
  AND state IN ('pending', 'leased', 'recovery_required')
  AND (
      lease_expires_at IS NULL
      OR lease_expires_at <= (SELECT now_at FROM lease_clock)
  )
  AND sqlc.arg(lease_duration_milliseconds)::bigint BETWEEN 1 AND 600000
  AND attempt_count < 1024
RETURNING commerce_operations.*;

-- name: AdvanceCommerceAccountRecoveryScan :one
WITH recovery_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE commerce_operations
SET state = CASE
        WHEN provider_retry_deadline_at > (SELECT now_at FROM recovery_clock)
         AND provider_attempt_count < 8
        THEN 'pending'
        ELSE 'recovery_required'
    END,
    lease_id = NULL,
    lease_expires_at = NULL,
    recovery_cursor = sqlc.narg(recovery_cursor)::text,
    recovery_candidate_provider_id = sqlc.narg(recovery_candidate_provider_id)::text,
    recovery_required_at = CASE
        WHEN provider_retry_deadline_at > (SELECT now_at FROM recovery_clock)
         AND provider_attempt_count < 8
        THEN NULL
        ELSE COALESCE(
            recovery_required_at,
            (SELECT now_at FROM recovery_clock)
        )
    END,
    last_error_code = 'recovery_scan_incomplete',
    updated_at = (SELECT now_at FROM recovery_clock),
    version = commerce_operations.version + 1
WHERE id = sqlc.arg(operation_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND commerce_account_id = sqlc.arg(account_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND operation_type = 'account_provisioning'
  AND request_sha256 = sqlc.arg(request_sha256)::bytea
  AND idempotency_key = sqlc.arg(idempotency_key)::text
  AND state = 'leased'
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND lease_expires_at > (SELECT now_at FROM recovery_clock)
  AND version = sqlc.arg(expected_version)::bigint
RETURNING commerce_operations.*;

-- name: RecordCommerceAccountProviderAttempt :one
WITH attempt_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE commerce_operations
SET provider_attempt_count = commerce_operations.provider_attempt_count + 1,
    provider_first_attempted_at = COALESCE(
        commerce_operations.provider_first_attempted_at,
        (SELECT now_at FROM attempt_clock)
    ),
    provider_last_attempted_at = (SELECT now_at FROM attempt_clock),
    lease_expires_at = (SELECT now_at FROM attempt_clock)
        + sqlc.arg(provider_call_budget_milliseconds)::bigint * interval '1 millisecond',
    recovery_cursor = NULL,
    recovery_candidate_provider_id = NULL,
    last_error_code = NULL,
    updated_at = (SELECT now_at FROM attempt_clock),
    version = commerce_operations.version + 1
WHERE id = sqlc.arg(operation_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND commerce_account_id = sqlc.arg(account_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND operation_type = 'account_provisioning'
  AND request_sha256 = sqlc.arg(request_sha256)::bytea
  AND idempotency_key = sqlc.arg(idempotency_key)::text
  AND state = 'leased'
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND lease_expires_at > (SELECT now_at FROM attempt_clock)
  AND provider_retry_deadline_at > (SELECT now_at FROM attempt_clock)
        + sqlc.arg(provider_call_budget_milliseconds)::bigint * interval '1 millisecond'
  AND version = sqlc.arg(expected_version)::bigint
  AND provider_attempt_count < 8
  AND sqlc.arg(provider_call_budget_milliseconds)::bigint BETWEEN 1000 AND 30000
RETURNING commerce_operations.*;

-- name: ReleaseCommerceAccountProvisioningLease :one
WITH release_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE commerce_operations
SET state = CASE
        WHEN provider_retry_deadline_at > (SELECT now_at FROM release_clock)
         AND provider_attempt_count < 8
        THEN 'pending'
        ELSE 'recovery_required'
    END,
    lease_id = NULL,
    lease_expires_at = NULL,
    recovery_cursor = NULL,
    recovery_candidate_provider_id = NULL,
    recovery_required_at = CASE
        WHEN provider_retry_deadline_at > (SELECT now_at FROM release_clock)
         AND provider_attempt_count < 8
        THEN NULL
        ELSE COALESCE(
            recovery_required_at,
            (SELECT now_at FROM release_clock)
        )
    END,
    last_error_code = sqlc.arg(last_error_code)::text,
    updated_at = (SELECT now_at FROM release_clock),
    version = commerce_operations.version + 1
WHERE id = sqlc.arg(operation_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND commerce_account_id = sqlc.arg(account_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND operation_type = 'account_provisioning'
  AND request_sha256 = sqlc.arg(request_sha256)::bytea
  AND idempotency_key = sqlc.arg(idempotency_key)::text
  AND state = 'leased'
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
RETURNING commerce_operations.*;

-- name: RotateCommerceAccountProvisioningIdempotency :one
WITH rejection_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE commerce_operations
SET state = CASE
        WHEN provider_retry_deadline_at > (SELECT now_at FROM rejection_clock)
         AND provider_attempt_count < 8
        THEN 'pending'
        ELSE 'recovery_required'
    END,
    idempotency_key = sqlc.arg(new_idempotency_key)::text,
    lease_id = NULL,
    lease_expires_at = NULL,
    recovery_cursor = NULL,
    recovery_candidate_provider_id = NULL,
    recovery_required_at = CASE
        WHEN provider_retry_deadline_at > (SELECT now_at FROM rejection_clock)
         AND provider_attempt_count < 8
        THEN NULL
        ELSE COALESCE(
            recovery_required_at,
            (SELECT now_at FROM rejection_clock)
        )
    END,
    last_error_code = 'provider_setup_required',
    updated_at = (SELECT now_at FROM rejection_clock),
    version = commerce_operations.version + 1
WHERE id = sqlc.arg(operation_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND commerce_account_id = sqlc.arg(account_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND operation_type = 'account_provisioning'
  AND request_sha256 = sqlc.arg(request_sha256)::bytea
  AND idempotency_key = sqlc.arg(idempotency_key)::text
  AND state = 'leased'
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND lease_expires_at > (SELECT now_at FROM rejection_clock)
  AND provider_attempt_count BETWEEN 1 AND 8
  AND length(sqlc.arg(new_idempotency_key)::text) BETWEEN 1 AND 255
  AND sqlc.arg(new_idempotency_key)::text <> idempotency_key
  AND version = sqlc.arg(expected_version)::bigint
RETURNING commerce_operations.*;

-- name: RecordCommerceAccountProviderConfirmation :one
WITH confirmation_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE commerce_operations
SET state = 'provider_confirmed',
    provider_resource_id = sqlc.arg(provider_resource_id)::text,
    provider_confirmed_at = (SELECT now_at FROM confirmation_clock),
    lease_id = NULL,
    lease_expires_at = NULL,
    recovery_cursor = NULL,
    recovery_candidate_provider_id = NULL,
    recovery_required_at = NULL,
    last_error_code = NULL,
    updated_at = (SELECT now_at FROM confirmation_clock),
    version = commerce_operations.version + 1
WHERE id = sqlc.arg(operation_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND commerce_account_id = sqlc.arg(account_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND operation_type = 'account_provisioning'
  AND request_sha256 = sqlc.arg(request_sha256)::bytea
  AND idempotency_key = sqlc.arg(idempotency_key)::text
  AND state = 'leased'
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
RETURNING commerce_operations.*;

-- name: CompleteCommerceAccountProvisioningOperation :one
WITH completion_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE commerce_operations
SET state = 'succeeded',
    completed_at = (SELECT now_at FROM completion_clock),
    lease_id = NULL,
    lease_expires_at = NULL,
    recovery_cursor = NULL,
    recovery_candidate_provider_id = NULL,
    recovery_required_at = NULL,
    last_error_code = NULL,
    updated_at = (SELECT now_at FROM completion_clock),
    version = commerce_operations.version + 1
WHERE id = sqlc.arg(operation_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND commerce_account_id = sqlc.arg(account_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND operation_type = 'account_provisioning'
  AND request_sha256 = sqlc.arg(request_sha256)::bytea
  AND idempotency_key = sqlc.arg(idempotency_key)::text
  AND state = 'provider_confirmed'
  AND provider_resource_id = sqlc.arg(provider_resource_id)::text
  AND version = sqlc.arg(expected_version)::bigint
RETURNING commerce_operations.*;

-- name: GetCommerceAccountForOwner :one
SELECT commerce_accounts.*
FROM commerce_accounts
JOIN companies
  ON companies.id = commerce_accounts.company_id
 AND companies.deleting_at IS NULL
JOIN company_members AS owner
  ON owner.company_id = commerce_accounts.company_id
 AND owner.user_id = sqlc.arg(actor_user_id)::uuid
 AND owner.status = 'active'
 AND owner.role = 'owner'
JOIN users
  ON users.id = owner.user_id
 AND users.disabled_at IS NULL
 AND users.deleted_at IS NULL
JOIN auth_sessions
  ON auth_sessions.id = sqlc.arg(session_id)::uuid
 AND auth_sessions.user_id = users.id
 AND auth_sessions.revoked_at IS NULL
 AND auth_sessions.expires_at > clock_timestamp()
WHERE commerce_accounts.company_id = sqlc.arg(company_id)::uuid
  AND commerce_accounts.environment = sqlc.arg(environment)::text;

-- name: FinalizeCommerceAccountProvisioning :one
WITH projection_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE commerce_accounts
SET provider_account_id = sqlc.arg(provider_account_id)::text,
    status = sqlc.arg(status)::text,
    country = sqlc.narg(country)::text,
    default_currency = sqlc.narg(default_currency)::text,
    details_submitted = sqlc.arg(details_submitted)::boolean,
    charges_enabled = sqlc.arg(charges_enabled)::boolean,
    payouts_enabled = sqlc.arg(payouts_enabled)::boolean,
    card_payments_status = sqlc.narg(card_payments_status)::text,
    transfers_status = sqlc.narg(transfers_status)::text,
    requirements_currently_due_count = sqlc.arg(requirements_currently_due_count)::integer,
    requirements_eventually_due_count = sqlc.arg(requirements_eventually_due_count)::integer,
    requirements_past_due_count = sqlc.arg(requirements_past_due_count)::integer,
    requirements_deadline = sqlc.narg(requirements_deadline)::timestamptz,
    disabled_reason = sqlc.narg(disabled_reason)::text,
    controller_losses_payments = 'stripe',
    controller_fees_payer = 'account',
    controller_requirement_collection = 'stripe',
    controller_dashboard_type = 'full',
    controller_template_version = 1,
    reconciliation_generation = commerce_accounts.reconciliation_generation + 1,
    last_reconciled_at = (SELECT now_at FROM projection_clock),
    updated_at = (SELECT now_at FROM projection_clock),
    version = commerce_accounts.version + 1
WHERE commerce_accounts.company_id = sqlc.arg(company_id)::uuid
  AND commerce_accounts.id = sqlc.arg(account_id)::uuid
  AND commerce_accounts.environment = sqlc.arg(environment)::text
  AND commerce_accounts.version = sqlc.arg(expected_version)::bigint
  AND commerce_accounts.provider_account_id IS NULL
RETURNING commerce_accounts.*;

-- name: UpdateCommerceAccountProjection :one
WITH projection_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE commerce_accounts
SET status = sqlc.arg(status)::text,
    country = sqlc.narg(country)::text,
    default_currency = sqlc.narg(default_currency)::text,
    details_submitted = sqlc.arg(details_submitted)::boolean,
    charges_enabled = sqlc.arg(charges_enabled)::boolean,
    payouts_enabled = sqlc.arg(payouts_enabled)::boolean,
    card_payments_status = sqlc.narg(card_payments_status)::text,
    transfers_status = sqlc.narg(transfers_status)::text,
    requirements_currently_due_count = sqlc.arg(requirements_currently_due_count)::integer,
    requirements_eventually_due_count = sqlc.arg(requirements_eventually_due_count)::integer,
    requirements_past_due_count = sqlc.arg(requirements_past_due_count)::integer,
    requirements_deadline = sqlc.narg(requirements_deadline)::timestamptz,
    disabled_reason = sqlc.narg(disabled_reason)::text,
    reconciliation_generation = commerce_accounts.reconciliation_generation + 1,
    last_reconciled_at = (SELECT now_at FROM projection_clock),
    updated_at = (SELECT now_at FROM projection_clock),
    version = commerce_accounts.version + 1
WHERE commerce_accounts.company_id = sqlc.arg(company_id)::uuid
  AND commerce_accounts.id = sqlc.arg(account_id)::uuid
  AND commerce_accounts.environment = sqlc.arg(environment)::text
  AND commerce_accounts.provider_account_id = sqlc.arg(provider_account_id)::text
  AND commerce_accounts.version = sqlc.arg(expected_version)::bigint
  AND commerce_accounts.status NOT IN ('draining', 'disconnected')
RETURNING commerce_accounts.*;

-- name: LockCommerceManifestConversation :one
SELECT id
FROM conversations
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(conversation_id)::uuid
  AND kind = 'task'
FOR UPDATE;

-- name: LockCommerceManifestRun :one
SELECT runs.*
FROM runs
WHERE runs.company_id = sqlc.arg(company_id)::uuid
  AND runs.id = sqlc.arg(run_id)::uuid
  AND runs.task_id = sqlc.arg(task_id)::uuid
  AND runs.conversation_id = sqlc.arg(conversation_id)::uuid
  AND runs.agent_id = sqlc.arg(agent_id)::uuid
FOR UPDATE;

-- name: InsertCommerceDraftSite :one
INSERT INTO commerce_sites (
    company_id, commerce_account_id, environment, site_alias, origin,
    verification_token_sha256,
    portal_return_path, created_by_user_id
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(commerce_account_id)::uuid,
    sqlc.arg(environment)::text,
    sqlc.arg(site_alias)::text,
    sqlc.arg(origin)::text,
    sqlc.arg(verification_token_sha256)::bytea,
    sqlc.arg(portal_return_path)::text,
    sqlc.arg(created_by_user_id)::uuid
)
ON CONFLICT (company_id, environment, site_alias) DO NOTHING
RETURNING *;

-- name: GetCommerceSiteByAlias :one
SELECT *
FROM commerce_sites
WHERE company_id = sqlc.arg(company_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND site_alias = sqlc.arg(site_alias)::text;

-- name: GetCommerceSiteByID :one
SELECT *
FROM commerce_sites
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(site_id)::uuid;

-- name: GetCommerceSiteForUpdate :one
SELECT *
FROM commerce_sites
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(site_id)::uuid
FOR UPDATE;

-- name: LockActiveCommerceManifestForSite :many
SELECT id
FROM commerce_manifests
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND id <> sqlc.arg(manifest_id)::uuid
  AND state = 'active'
ORDER BY id
FOR UPDATE;

-- name: GetActiveCommerceSiteByID :one
SELECT
    commerce_sites.*,
    commerce_accounts.provider_account_id,
    commerce_accounts.charges_enabled,
    commerce_accounts.status AS account_status
FROM commerce_sites
JOIN companies
  ON companies.id = commerce_sites.company_id
 AND companies.deleting_at IS NULL
JOIN commerce_accounts
  ON commerce_accounts.id = commerce_sites.commerce_account_id
 AND commerce_accounts.company_id = commerce_sites.company_id
 AND commerce_accounts.environment = commerce_sites.environment
WHERE commerce_sites.id = sqlc.arg(site_id)::uuid
  AND commerce_sites.status = 'active';

-- name: GetDaemonCommerceSiteByTask :one
SELECT commerce_sites.*
FROM commerce_sites
JOIN companies
  ON companies.id = commerce_sites.company_id
 AND companies.deleting_at IS NULL
WHERE commerce_sites.company_id = sqlc.arg(company_id)::uuid
  AND commerce_sites.site_alias = sqlc.arg(site_alias)::text
  AND commerce_sites.origin = sqlc.arg(origin)::text
  AND commerce_sites.environment = 'sandbox'
  AND commerce_sites.status IN ('draft', 'active')
  AND EXISTS (
      SELECT 1
      FROM runs
      WHERE runs.id = sqlc.arg(run_id)::uuid
        AND runs.company_id = commerce_sites.company_id
        AND runs.task_id = sqlc.arg(task_id)::uuid
        AND runs.status = 'running'
  )
  AND EXISTS (
      SELECT 1
      FROM commerce_manifests
      WHERE commerce_manifests.company_id = commerce_sites.company_id
        AND commerce_manifests.site_id = commerce_sites.id
        AND commerce_manifests.task_id = sqlc.arg(task_id)::uuid
        AND commerce_manifests.state IN (
            'draft', 'publishing', 'active', 'apply_failed'
        )
  );

-- name: InsertPendingCommerceSiteSigningKey :one
INSERT INTO commerce_site_signing_keys (
    company_id, site_id, environment, key_id, fingerprint, public_jwk
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(site_id)::uuid,
    sqlc.arg(environment)::text,
    sqlc.arg(key_id)::text,
    sqlc.arg(fingerprint)::text,
    sqlc.arg(public_jwk)::jsonb
)
ON CONFLICT (site_id, fingerprint) DO NOTHING
RETURNING *;

-- name: GetCommerceSiteSigningKeyByFingerprint :one
SELECT *
FROM commerce_site_signing_keys
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND fingerprint = sqlc.arg(fingerprint)::text;

-- name: GetPendingCommerceSiteSigningKey :one
SELECT *
FROM commerce_site_signing_keys
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND state = 'pending';

-- name: GetCommerceSiteRequestSigningKey :one
SELECT
    commerce_site_signing_keys.*,
    commerce_sites.origin,
    commerce_sites.portal_return_path,
    commerce_sites.provider_portal_configuration_id,
    commerce_sites.active_manifest_id,
    commerce_accounts.provider_account_id,
    commerce_accounts.charges_enabled,
    commerce_accounts.status AS account_status
FROM commerce_site_signing_keys
JOIN commerce_sites
  ON commerce_sites.company_id = commerce_site_signing_keys.company_id
 AND commerce_sites.id = commerce_site_signing_keys.site_id
 AND commerce_sites.environment = commerce_site_signing_keys.environment
 AND commerce_sites.status = 'active'
JOIN companies
  ON companies.id = commerce_sites.company_id
 AND companies.deleting_at IS NULL
JOIN commerce_accounts
  ON commerce_accounts.id = commerce_sites.commerce_account_id
 AND commerce_accounts.company_id = commerce_sites.company_id
 AND commerce_accounts.environment = commerce_sites.environment
WHERE commerce_site_signing_keys.site_id = sqlc.arg(site_id)::uuid
  AND commerce_site_signing_keys.key_id = sqlc.arg(key_id)::text
  AND (
      commerce_site_signing_keys.state = 'active'
      OR (
          commerce_site_signing_keys.state = 'retiring'
          AND commerce_site_signing_keys.retire_at > clock_timestamp()
      )
  );

-- name: ListCommerceSiteSigningKeys :many
SELECT *
FROM commerce_site_signing_keys
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
ORDER BY created_at DESC, id DESC
LIMIT 100;

-- name: GetActiveCommerceManifestForSite :one
SELECT commerce_manifests.*
FROM commerce_manifests
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND state = 'active';

-- name: RevokePendingCommerceSiteSigningKeys :execrows
UPDATE commerce_site_signing_keys
SET state = 'revoked',
    revoked_at = clock_timestamp(),
    updated_at = clock_timestamp(),
    version = version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND fingerprint <> sqlc.arg(fingerprint)::text
  AND state = 'pending';

-- name: RevokeRetiringCommerceSiteSigningKeys :execrows
UPDATE commerce_site_signing_keys
SET state = 'revoked',
    revoked_at = clock_timestamp(),
    updated_at = clock_timestamp(),
    version = version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND fingerprint <> sqlc.arg(fingerprint)::text
  AND state = 'retiring';

-- name: RetireActiveCommerceSiteSigningKeys :execrows
UPDATE commerce_site_signing_keys
SET state = 'retiring',
    retire_at = clock_timestamp() + interval '24 hours',
    updated_at = clock_timestamp(),
    version = version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND fingerprint <> sqlc.arg(fingerprint)::text
  AND state = 'active';

-- name: ActivateCommerceSiteSigningKey :one
UPDATE commerce_site_signing_keys
SET state = 'active',
    activated_at = COALESCE(activated_at, clock_timestamp()),
    retire_at = NULL,
    revoked_at = NULL,
    updated_at = clock_timestamp(),
    version = version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND fingerprint = sqlc.arg(fingerprint)::text
  AND state IN ('pending', 'active')
RETURNING *;

-- name: RevokeCommerceSiteSigningKey :one
UPDATE commerce_site_signing_keys
SET state = 'revoked',
    revoked_at = clock_timestamp(),
    updated_at = clock_timestamp(),
    version = version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND key_id = sqlc.arg(key_id)::text
  AND state <> 'revoked'
RETURNING *;

-- name: InsertCommerceSiteChallenge :one
INSERT INTO commerce_site_challenges (
    company_id, site_id, environment, action, operation_id,
    nonce_sha256, expires_at
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(site_id)::uuid,
    sqlc.arg(environment)::text,
    sqlc.arg(action)::text,
    sqlc.arg(operation_id)::uuid,
    sqlc.arg(nonce_sha256)::bytea,
    sqlc.arg(expires_at)::timestamptz
)
ON CONFLICT (site_id, action, operation_id)
DO UPDATE SET
    nonce_sha256 = EXCLUDED.nonce_sha256,
    expires_at = EXCLUDED.expires_at,
    consumed_at = NULL,
    created_at = clock_timestamp()
RETURNING *;

-- name: PruneCommerceSiteChallenges :execrows
WITH stale AS (
    SELECT id
    FROM commerce_site_challenges
    WHERE expires_at <= sqlc.arg(now)::timestamptz
       OR consumed_at IS NOT NULL
    ORDER BY expires_at, id
    LIMIT 1000
    FOR UPDATE SKIP LOCKED
)
DELETE FROM commerce_site_challenges AS challenges
USING stale
WHERE challenges.id = stale.id;

-- name: ConsumeCommerceSiteChallenge :one
UPDATE commerce_site_challenges
SET consumed_at = clock_timestamp()
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND action = sqlc.arg(action)::text
  AND operation_id = sqlc.arg(operation_id)::uuid
  AND nonce_sha256 = sqlc.arg(nonce_sha256)::bytea
  AND consumed_at IS NULL
  AND expires_at > sqlc.arg(now)::timestamptz
RETURNING *;

-- name: ListSandboxCommerceAccountsForEventPolling :many
SELECT *
FROM commerce_accounts
WHERE environment = 'sandbox'
  AND provider_account_id IS NOT NULL
  AND status <> 'disconnected'
  AND id > sqlc.arg(after_id)::uuid
ORDER BY id
LIMIT sqlc.arg(batch_size)::integer;

-- name: InsertCommerceManifest :one
INSERT INTO commerce_manifests (
    company_id, site_id, task_id, run_id, request_key, schema_version,
    canonical_json, digest
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(site_id)::uuid,
    sqlc.arg(task_id)::uuid,
    sqlc.arg(run_id)::uuid,
    sqlc.arg(request_key)::uuid,
    2,
    sqlc.arg(canonical_json)::jsonb,
    sqlc.arg(digest)::bytea
)
ON CONFLICT (site_id, run_id, digest) DO NOTHING
RETURNING *;

-- name: GetCommerceManifestByRunRequestKey :one
SELECT sqlc.embed(commerce_manifests), commerce_sites.environment
FROM commerce_manifests
JOIN commerce_sites
  ON commerce_sites.company_id = commerce_manifests.company_id
 AND commerce_sites.id = commerce_manifests.site_id
WHERE commerce_manifests.company_id = sqlc.arg(company_id)::uuid
  AND commerce_manifests.run_id = sqlc.arg(run_id)::uuid
  AND commerce_manifests.request_key = sqlc.arg(request_key)::uuid;

-- name: GetCommerceManifestBySiteRunDigest :one
SELECT *
FROM commerce_manifests
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND run_id = sqlc.arg(run_id)::uuid
  AND digest = sqlc.arg(digest)::bytea;

-- name: GetCommerceManifestByTask :one
SELECT sqlc.embed(commerce_manifests), commerce_sites.environment
FROM commerce_manifests
JOIN commerce_sites
  ON commerce_sites.company_id = commerce_manifests.company_id
 AND commerce_sites.id = commerce_manifests.site_id
JOIN tasks
  ON tasks.company_id = commerce_manifests.company_id
 AND tasks.id = commerce_manifests.task_id
 AND tasks.conversation_id = sqlc.arg(conversation_id)::uuid
WHERE commerce_manifests.company_id = sqlc.arg(company_id)::uuid
  AND commerce_manifests.task_id = sqlc.arg(task_id)::uuid
  AND commerce_manifests.id = sqlc.arg(manifest_id)::uuid;

-- name: GetCommerceManifestForUpdate :one
SELECT *
FROM commerce_manifests
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(manifest_id)::uuid
FOR UPDATE;

-- name: ListCommerceManifestsForOwner :many
SELECT sqlc.embed(commerce_manifests), commerce_sites.environment
FROM commerce_manifests
JOIN commerce_sites
  ON commerce_sites.id = commerce_manifests.site_id
 AND commerce_sites.company_id = commerce_manifests.company_id
WHERE commerce_manifests.company_id = sqlc.arg(company_id)::uuid
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (commerce_manifests.created_at, commerce_manifests.id) < (
          sqlc.arg(before_created_at)::timestamptz,
          sqlc.arg(before_id)::uuid
      )
  )
ORDER BY commerce_manifests.created_at DESC, commerce_manifests.id DESC
LIMIT sqlc.arg(page_size)::integer;

-- name: AuthorizeRecentCommerceApprovalSession :one
WITH RECURSIVE session_lineage AS (
    SELECT current_session.id, current_session.user_id,
        current_session.rotated_from_session_id, current_session.created_at
    FROM auth_sessions AS current_session
    WHERE current_session.id = sqlc.arg(session_id)::uuid
      AND current_session.user_id = sqlc.arg(actor_user_id)::uuid
    UNION ALL
    SELECT parent.id, parent.user_id, parent.rotated_from_session_id,
        parent.created_at
    FROM auth_sessions AS parent
    JOIN session_lineage AS child
      ON child.rotated_from_session_id = parent.id
     AND parent.user_id = child.user_id
)
SELECT auth_sessions.id
FROM auth_sessions
JOIN users
  ON users.id = auth_sessions.user_id
 AND users.disabled_at IS NULL
 AND users.deleted_at IS NULL
WHERE auth_sessions.id = sqlc.arg(session_id)::uuid
  AND auth_sessions.user_id = sqlc.arg(actor_user_id)::uuid
  AND auth_sessions.revoked_at IS NULL
  AND auth_sessions.expires_at > clock_timestamp()
  AND EXISTS (
      SELECT 1
      FROM session_lineage AS authentication_root
      WHERE authentication_root.rotated_from_session_id IS NULL
        AND authentication_root.created_at >= clock_timestamp()
            - sqlc.arg(maximum_age_seconds)::bigint * interval '1 second'
  );

-- name: MarkCommerceManifestPublishing :one
UPDATE commerce_manifests
SET state = 'publishing',
    approved_by_user_id = sqlc.arg(approved_by_user_id)::uuid,
    approval_session_id = sqlc.arg(approval_session_id)::uuid,
    approved_at = clock_timestamp(),
    apply_error_code = NULL,
    updated_at = clock_timestamp(),
    version = commerce_manifests.version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(manifest_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND digest = sqlc.arg(digest)::bytea
  AND state IN ('draft', 'apply_failed')
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: MarkCommerceManifestApplyFailed :one
UPDATE commerce_manifests
SET state = 'apply_failed',
    apply_error_code = sqlc.arg(apply_error_code)::text,
    updated_at = clock_timestamp(),
    version = commerce_manifests.version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND id = sqlc.arg(manifest_id)::uuid
  AND digest = sqlc.arg(digest)::bytea
  AND state = 'publishing'
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: InsertCommercePublishingOffer :one
INSERT INTO commerce_offers (
    company_id, site_id, manifest_id, environment, alias, display_name,
    offer_type, plan_group_alias, entitlement_key, amount, currency,
    recurring_interval, automatic_tax, success_path, cancel_path
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(site_id)::uuid,
    sqlc.arg(manifest_id)::uuid,
    sqlc.arg(environment)::text,
    sqlc.arg(alias)::text,
    sqlc.arg(display_name)::text,
    sqlc.arg(offer_type)::text,
    sqlc.narg(plan_group_alias)::text,
    sqlc.arg(entitlement_key)::text,
    sqlc.arg(amount)::bigint,
    sqlc.arg(currency)::text,
    sqlc.narg(recurring_interval)::text,
    sqlc.arg(automatic_tax)::boolean,
    sqlc.arg(success_path)::text,
    sqlc.arg(cancel_path)::text
)
ON CONFLICT (manifest_id, alias) DO NOTHING
RETURNING *;

-- name: GetCommerceOfferByManifestAlias :one
SELECT *
FROM commerce_offers
WHERE company_id = sqlc.arg(company_id)::uuid
  AND manifest_id = sqlc.arg(manifest_id)::uuid
  AND alias = sqlc.arg(alias)::text;

-- name: ListCommerceOffersByManifest :many
SELECT *
FROM commerce_offers
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND manifest_id = sqlc.arg(manifest_id)::uuid
ORDER BY alias;

-- name: SetCommerceOfferProviderResources :one
UPDATE commerce_offers
SET provider_product_id = sqlc.arg(provider_product_id)::text,
    provider_price_id = sqlc.arg(provider_price_id)::text,
    updated_at = clock_timestamp(),
    version = commerce_offers.version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(offer_id)::uuid
  AND manifest_id = sqlc.arg(manifest_id)::uuid
  AND status = 'publishing'
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: DisableActiveCommerceOffers :execrows
UPDATE commerce_offers
SET status = 'disabled',
    updated_at = clock_timestamp(),
    version = commerce_offers.version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND manifest_id <> sqlc.arg(manifest_id)::uuid
  AND status = 'active';

-- name: SupersedeActiveCommerceManifests :execrows
UPDATE commerce_manifests
SET state = 'superseded',
    updated_at = clock_timestamp(),
    version = commerce_manifests.version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND id <> sqlc.arg(manifest_id)::uuid
  AND state = 'active';

-- name: ActivateCommerceManifest :one
WITH activated_offers AS (
    UPDATE commerce_offers
    SET status = 'active',
        updated_at = clock_timestamp(),
        version = commerce_offers.version + 1
    WHERE company_id = sqlc.arg(company_id)::uuid
      AND site_id = sqlc.arg(site_id)::uuid
      AND manifest_id = sqlc.arg(manifest_id)::uuid
      AND status = 'publishing'
      AND provider_product_id IS NOT NULL
      AND provider_price_id IS NOT NULL
    RETURNING id
), activated_site AS (
    UPDATE commerce_sites
    SET commerce_account_id = sqlc.arg(commerce_account_id)::uuid,
        origin = sqlc.arg(origin)::text,
        verification_token_sha256 = sqlc.arg(verification_token_sha256)::bytea,
        status = 'active',
        verified_at = COALESCE(verified_at, clock_timestamp()),
        portal_return_path = sqlc.arg(portal_return_path)::text,
        provider_portal_configuration_id = sqlc.arg(provider_portal_configuration_id)::text,
        active_manifest_id = sqlc.arg(manifest_id)::uuid,
        updated_at = clock_timestamp(),
        version = commerce_sites.version + 1
    WHERE company_id = sqlc.arg(company_id)::uuid
      AND id = sqlc.arg(site_id)::uuid
      AND version = sqlc.arg(expected_site_version)::bigint
      AND (
          SELECT count(*) FROM activated_offers
      ) = sqlc.arg(expected_offer_count)::integer
    RETURNING id
)
UPDATE commerce_manifests
SET state = 'active',
    applied_at = clock_timestamp(),
    apply_error_code = NULL,
    updated_at = clock_timestamp(),
    version = commerce_manifests.version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(manifest_id)::uuid
  AND site_id = (SELECT id FROM activated_site)
  AND state = 'publishing'
  AND version = sqlc.arg(expected_manifest_version)::bigint
RETURNING commerce_manifests.*;

-- name: GetActiveCommerceOfferByAlias :one
SELECT
    commerce_offers.*,
    commerce_sites.origin,
    commerce_sites.portal_return_path,
    commerce_sites.provider_portal_configuration_id,
    commerce_accounts.provider_account_id
FROM commerce_offers
JOIN commerce_sites
  ON commerce_sites.id = commerce_offers.site_id
 AND commerce_sites.company_id = commerce_offers.company_id
 AND commerce_sites.environment = commerce_offers.environment
 AND commerce_sites.status = 'active'
JOIN commerce_accounts
  ON commerce_accounts.id = commerce_sites.commerce_account_id
 AND commerce_accounts.company_id = commerce_sites.company_id
 AND commerce_accounts.environment = commerce_sites.environment
WHERE commerce_offers.company_id = sqlc.arg(company_id)::uuid
  AND commerce_offers.site_id = sqlc.arg(site_id)::uuid
  AND commerce_offers.alias = sqlc.arg(alias)::text
  AND commerce_offers.status = 'active'
  AND commerce_accounts.status = 'ready'
  AND commerce_accounts.charges_enabled = true;

-- name: InsertCommerceCustomer :one
INSERT INTO commerce_customers (
    company_id, site_id, environment, subject_hmac
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(site_id)::uuid,
    sqlc.arg(environment)::text,
    sqlc.arg(subject_hmac)::bytea
)
ON CONFLICT (site_id, subject_hmac) DO NOTHING
RETURNING *;

-- name: GetCommerceCustomerBySubject :one
SELECT *
FROM commerce_customers
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND subject_hmac = sqlc.arg(subject_hmac)::bytea;

-- name: SetCommerceCustomerProviderID :one
UPDATE commerce_customers
SET provider_customer_id = sqlc.arg(provider_customer_id)::text,
    updated_at = clock_timestamp(),
    version = commerce_customers.version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND id = sqlc.arg(customer_id)::uuid
  AND provider_customer_id IS NULL
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: RecordCommerceCustomerProviderAttempt :one
UPDATE commerce_customers
SET provider_first_attempted_at = COALESCE(
        commerce_customers.provider_first_attempted_at,
        clock_timestamp()
    ),
    updated_at = clock_timestamp(),
    version = commerce_customers.version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND id = sqlc.arg(customer_id)::uuid
  AND provider_customer_id IS NULL
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: InsertCommerceGatewayOperation :one
INSERT INTO commerce_gateway_operations (
    company_id, site_id, customer_id, manifest_id, offer_id,
    environment, operation_type, operation_id, request_sha256,
    idempotency_key
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(site_id)::uuid,
    sqlc.narg(customer_id)::uuid,
    sqlc.narg(manifest_id)::uuid,
    sqlc.narg(offer_id)::uuid,
    sqlc.arg(environment)::text,
    sqlc.arg(operation_type)::text,
    sqlc.arg(operation_id)::uuid,
    sqlc.arg(request_sha256)::bytea,
    sqlc.arg(idempotency_key)::text
)
ON CONFLICT (site_id, operation_type, operation_id) DO NOTHING
RETURNING *;

-- name: GetCommerceGatewayOperation :one
SELECT *
FROM commerce_gateway_operations
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND operation_type = sqlc.arg(operation_type)::text
  AND operation_id = sqlc.arg(operation_id)::uuid;

-- name: GetCommerceGatewayOperationForUpdate :one
SELECT *
FROM commerce_gateway_operations
WHERE id = sqlc.arg(id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND operation_type = sqlc.arg(operation_type)::text
  AND operation_id = sqlc.arg(operation_id)::uuid
FOR UPDATE;

-- name: ClaimCommerceGatewayOperation :one
WITH claim_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE commerce_gateway_operations
SET state = 'leased',
    lease_id = sqlc.arg(lease_id)::uuid,
    lease_expires_at = (SELECT now_at FROM claim_clock)
        + sqlc.arg(lease_duration_milliseconds)::bigint * interval '1 millisecond',
    attempt_count = commerce_gateway_operations.attempt_count + 1,
    last_error_code = NULL,
    updated_at = (SELECT now_at FROM claim_clock),
    version = commerce_gateway_operations.version + 1
WHERE id = sqlc.arg(id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND state IN ('pending', 'leased', 'recovery_required')
  AND (
      lease_expires_at IS NULL
      OR lease_expires_at <= (SELECT now_at FROM claim_clock)
  )
  AND request_sha256 = sqlc.arg(request_sha256)::bytea
  AND sqlc.arg(lease_duration_milliseconds)::bigint BETWEEN 1 AND 600000
  AND attempt_count < 1024
RETURNING *;

-- name: RecordCommerceGatewayProviderAttempt :one
UPDATE commerce_gateway_operations
SET provider_attempt_count = commerce_gateway_operations.provider_attempt_count + 1,
    updated_at = clock_timestamp(),
    version = commerce_gateway_operations.version + 1
WHERE id = sqlc.arg(id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND state = 'leased'
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND lease_expires_at > clock_timestamp()
  AND provider_attempt_count < 64
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: CompleteCommerceGatewayOperation :one
UPDATE commerce_gateway_operations
SET state = 'succeeded',
    lease_id = NULL,
    lease_expires_at = NULL,
	provider_resource_id = COALESCE(
		commerce_gateway_operations.provider_resource_id,
		sqlc.arg(provider_resource_id)::text
	),
    provider_confirmed_at = COALESCE(provider_confirmed_at, clock_timestamp()),
    completed_at = COALESCE(completed_at, clock_timestamp()),
    last_error_code = NULL,
    updated_at = clock_timestamp(),
    version = commerce_gateway_operations.version + 1
WHERE id = sqlc.arg(id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND state = 'leased'
	AND (
		provider_resource_id IS NULL
		OR provider_resource_id = sqlc.arg(provider_resource_id)::text
	)
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: ReleaseCommerceGatewayOperationForRetry :one
UPDATE commerce_gateway_operations
SET state = CASE
        WHEN provider_attempt_count < 64 THEN 'pending'
        ELSE 'recovery_required'
    END,
    lease_id = NULL,
    lease_expires_at = NULL,
    last_error_code = sqlc.arg(last_error_code)::text,
    updated_at = clock_timestamp(),
    version = commerce_gateway_operations.version + 1
WHERE id = sqlc.arg(id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND state = 'leased'
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: ListCommerceEntitlements :many
SELECT entitlement_key, status, starts_at, expires_at, revoked_reason, updated_at
FROM (
    SELECT entitlement_key, status, starts_at, expires_at, revoked_reason,
        updated_at,
        row_number() OVER (
            PARTITION BY entitlement_key
            ORDER BY
                CASE
                    WHEN status = 'active'
                     AND (expires_at IS NULL OR expires_at > clock_timestamp())
                        THEN 0
                    WHEN status = 'processing' THEN 1
                    WHEN status = 'denied' THEN 2
                    ELSE 3
                END,
                provider_event_at DESC,
                id DESC
        ) AS grant_priority
    FROM commerce_entitlements
    WHERE company_id = sqlc.arg(company_id)::uuid
      AND site_id = sqlc.arg(site_id)::uuid
      AND customer_id = sqlc.arg(customer_id)::uuid
      AND (
          (
              source_type = 'one_time'
              AND payment_id = grant_id
              AND subscription_id IS NULL
          ) OR (
              source_type = 'subscription'
              AND subscription_id = grant_id
          )
      )
) AS effective_entitlements
WHERE grant_priority = 1
ORDER BY entitlement_key;

-- name: GetCommerceEntitlementGrant :one
SELECT *
FROM commerce_entitlements
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND customer_id = sqlc.arg(customer_id)::uuid
  AND source_type = sqlc.arg(source_type)::text
  AND grant_id = sqlc.arg(grant_id)::uuid
  AND entitlement_key = sqlc.arg(entitlement_key)::text;

-- name: ConsumeCommerceRateLimit :one
WITH limit_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
INSERT INTO commerce_rate_limits (
    company_id, site_id, subject_hmac, action, window_started_at,
    request_count, updated_at
)
SELECT
    sqlc.arg(company_id)::uuid,
    sqlc.arg(site_id)::uuid,
    sqlc.arg(subject_hmac)::bytea,
    sqlc.arg(action)::text,
    limit_clock.now_at,
    1,
    limit_clock.now_at
FROM limit_clock
WHERE sqlc.arg(window_milliseconds)::bigint BETWEEN 1000 AND 3600000
  AND sqlc.arg(maximum_requests)::integer BETWEEN 1 AND 1000000
ON CONFLICT (company_id, site_id, subject_hmac, action)
DO UPDATE SET
    window_started_at = CASE
        WHEN commerce_rate_limits.window_started_at
            + sqlc.arg(window_milliseconds)::bigint * interval '1 millisecond'
            <= (SELECT now_at FROM limit_clock)
            THEN (SELECT now_at FROM limit_clock)
        ELSE commerce_rate_limits.window_started_at
    END,
    request_count = CASE
        WHEN commerce_rate_limits.window_started_at
            + sqlc.arg(window_milliseconds)::bigint * interval '1 millisecond'
            <= (SELECT now_at FROM limit_clock)
            THEN 1
        ELSE commerce_rate_limits.request_count + 1
    END,
    updated_at = (SELECT now_at FROM limit_clock)
WHERE commerce_rate_limits.window_started_at
        + sqlc.arg(window_milliseconds)::bigint * interval '1 millisecond'
        <= (SELECT now_at FROM limit_clock)
   OR commerce_rate_limits.request_count < sqlc.arg(maximum_requests)::integer
RETURNING commerce_rate_limits.*;

-- name: PruneCommerceRateLimits :execrows
WITH stale AS (
    SELECT company_id, site_id, subject_hmac, action
    FROM commerce_rate_limits
    WHERE updated_at <= clock_timestamp()
        - sqlc.arg(retention_milliseconds)::bigint * interval '1 millisecond'
      AND sqlc.arg(retention_milliseconds)::bigint BETWEEN 2000 AND 7200000
    ORDER BY updated_at, company_id, site_id, action
    LIMIT 1000
    FOR UPDATE SKIP LOCKED
)
DELETE FROM commerce_rate_limits AS limits
USING stale
WHERE limits.company_id = stale.company_id
  AND limits.site_id = stale.site_id
  AND limits.subject_hmac = stale.subject_hmac
  AND limits.action = stale.action;

-- name: InsertCommerceWebhookReceipt :one
INSERT INTO commerce_webhook_receipts (
    company_id, environment, provider_event_id, provider_account_id,
    event_type, provider_object_id, payload_sha256, livemode,
    provider_created_at
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(environment)::text,
    sqlc.arg(provider_event_id)::text,
    sqlc.arg(provider_account_id)::text,
    sqlc.arg(event_type)::text,
    sqlc.narg(provider_object_id)::text,
    sqlc.arg(payload_sha256)::bytea,
    sqlc.arg(livemode)::boolean,
    sqlc.arg(provider_created_at)::timestamptz
)
ON CONFLICT (environment, provider_event_id) DO NOTHING
RETURNING *;

-- Claim provider fetch + projection authority without keeping a transaction
-- or connection open during Stripe I/O. The lease row is shared by every API
-- replica; an expired worker is fenced again under row lock before projection.
-- name: ClaimCommerceWebhookAccountLease :one
WITH lease_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
INSERT INTO commerce_webhook_account_leases (
    company_id, environment, provider_account_id, lease_id,
    lease_expires_at, acquired_at, updated_at
)
SELECT
    sqlc.arg(company_id)::uuid,
    sqlc.arg(environment)::text,
    sqlc.arg(provider_account_id)::text,
    sqlc.arg(lease_id)::uuid,
    (SELECT now_at FROM lease_clock)
        + sqlc.arg(lease_duration_milliseconds)::bigint * interval '1 millisecond',
    (SELECT now_at FROM lease_clock),
    (SELECT now_at FROM lease_clock)
WHERE sqlc.arg(lease_duration_milliseconds)::bigint BETWEEN 1 AND 600000
ON CONFLICT (company_id, environment, provider_account_id)
DO UPDATE SET
    lease_id = EXCLUDED.lease_id,
    lease_expires_at = EXCLUDED.lease_expires_at,
    acquired_at = EXCLUDED.acquired_at,
    updated_at = EXCLUDED.updated_at
WHERE commerce_webhook_account_leases.lease_expires_at
    <= (SELECT now_at FROM lease_clock)
RETURNING *;

-- name: LockCommerceWebhookAccountLease :one
SELECT sqlc.embed(commerce_accounts)
FROM commerce_webhook_account_leases
JOIN commerce_accounts
  ON commerce_accounts.company_id = commerce_webhook_account_leases.company_id
 AND commerce_accounts.environment = commerce_webhook_account_leases.environment
 AND commerce_accounts.provider_account_id
     = commerce_webhook_account_leases.provider_account_id
WHERE commerce_webhook_account_leases.company_id = sqlc.arg(company_id)::uuid
  AND commerce_webhook_account_leases.environment = sqlc.arg(environment)::text
  AND commerce_webhook_account_leases.provider_account_id
      = sqlc.arg(provider_account_id)::text
  AND commerce_webhook_account_leases.lease_id = sqlc.arg(lease_id)::uuid
  AND commerce_webhook_account_leases.lease_expires_at > clock_timestamp()
FOR UPDATE OF commerce_webhook_account_leases, commerce_accounts;

-- name: ReleaseCommerceWebhookAccountLease :one
DELETE FROM commerce_webhook_account_leases
WHERE company_id = sqlc.arg(company_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND provider_account_id = sqlc.arg(provider_account_id)::text
  AND lease_id = sqlc.arg(lease_id)::uuid
RETURNING lease_id;

-- name: GetCommerceWebhookReceipt :one
SELECT *
FROM commerce_webhook_receipts
WHERE environment = sqlc.arg(environment)::text
  AND provider_event_id = sqlc.arg(provider_event_id)::text;

-- name: CompleteCommerceWebhookReceipt :one
UPDATE commerce_webhook_receipts
SET state = 'succeeded',
    lease_id = NULL,
    lease_expires_at = NULL,
    completed_at = COALESCE(completed_at, clock_timestamp()),
    last_error_code = NULL,
    updated_at = clock_timestamp(),
    version = commerce_webhook_receipts.version + 1
WHERE id = sqlc.arg(id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND payload_sha256 = sqlc.arg(payload_sha256)::bytea
  AND state = 'processing'
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: ClaimCommerceWebhookReceipt :one
WITH claim_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE commerce_webhook_receipts
SET state = 'processing',
    lease_id = sqlc.arg(lease_id)::uuid,
    lease_expires_at = (SELECT now_at FROM claim_clock)
        + sqlc.arg(lease_duration_milliseconds)::bigint * interval '1 millisecond',
    attempt_count = commerce_webhook_receipts.attempt_count + 1,
    last_error_code = NULL,
    updated_at = (SELECT now_at FROM claim_clock),
    version = commerce_webhook_receipts.version + 1
WHERE id = sqlc.arg(id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND payload_sha256 = sqlc.arg(payload_sha256)::bytea
  AND (
      state = 'received'
      OR (state = 'failed' AND next_attempt_at <= (SELECT now_at FROM claim_clock))
      OR (state = 'processing' AND lease_expires_at <= (SELECT now_at FROM claim_clock))
  )
  AND sqlc.arg(lease_duration_milliseconds)::bigint BETWEEN 1 AND 600000
  AND attempt_count < 32
  AND created_at > (SELECT now_at FROM claim_clock) - interval '23 hours'
RETURNING *;

-- name: ClaimNextCommerceWebhookReceipt :one
WITH claim_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
), candidate AS MATERIALIZED (
    SELECT receipts.id
    FROM commerce_webhook_receipts AS receipts
    WHERE (
        receipts.state = 'received'
        OR (
            receipts.state = 'failed'
            AND receipts.next_attempt_at <= (SELECT now_at FROM claim_clock)
        )
        OR (
            receipts.state = 'processing'
            AND receipts.lease_expires_at <= (SELECT now_at FROM claim_clock)
        )
    )
      AND receipts.attempt_count < 32
      AND receipts.created_at
          > (SELECT now_at FROM claim_clock) - interval '23 hours'
    ORDER BY receipts.next_attempt_at, receipts.provider_created_at, receipts.id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE commerce_webhook_receipts AS receipts
SET state = 'processing',
    lease_id = sqlc.arg(lease_id)::uuid,
    lease_expires_at = (SELECT now_at FROM claim_clock)
        + sqlc.arg(lease_duration_milliseconds)::bigint * interval '1 millisecond',
    attempt_count = receipts.attempt_count + 1,
    last_error_code = NULL,
    updated_at = (SELECT now_at FROM claim_clock),
    version = receipts.version + 1
FROM candidate
WHERE receipts.id = candidate.id
  AND sqlc.arg(lease_duration_milliseconds)::bigint BETWEEN 1 AND 600000
RETURNING receipts.*;

-- name: ReleaseCommerceWebhookReceiptForRetry :one
UPDATE commerce_webhook_receipts
SET state = 'failed',
    lease_id = NULL,
    lease_expires_at = NULL,
    next_attempt_at = clock_timestamp()
        + LEAST(
            3600::double precision,
            30::double precision * power(
                2::double precision,
                LEAST(GREATEST(attempt_count - 1, 0), 7)::double precision
            )
        ) * interval '1 second',
    last_error_code = sqlc.arg(last_error_code)::text,
    updated_at = clock_timestamp(),
    version = commerce_webhook_receipts.version + 1
WHERE id = sqlc.arg(id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND state = 'processing'
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: MarkCommerceWebhookReceiptDeadLetter :one
UPDATE commerce_webhook_receipts
SET state = 'dead_letter',
    lease_id = NULL,
    lease_expires_at = NULL,
    last_error_code = sqlc.arg(last_error_code)::text,
    completed_at = COALESCE(completed_at, clock_timestamp()),
    updated_at = clock_timestamp(),
    version = commerce_webhook_receipts.version + 1
WHERE id = sqlc.arg(id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND state = 'processing'
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: DeadLetterExhaustedCommerceWebhookReceipts :many
WITH terminal_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
), candidates AS MATERIALIZED (
    SELECT receipts.id
    FROM commerce_webhook_receipts AS receipts
    WHERE (
        receipts.state IN ('received', 'failed')
        OR (
            receipts.state = 'processing'
            AND receipts.lease_expires_at <= (SELECT now_at FROM terminal_clock)
        )
    )
      AND (
          receipts.attempt_count >= 32
          OR receipts.created_at
              <= (SELECT now_at FROM terminal_clock) - interval '23 hours'
      )
    ORDER BY receipts.provider_created_at, receipts.id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(page_limit)::int
)
UPDATE commerce_webhook_receipts AS receipts
SET state = 'dead_letter',
    lease_id = NULL,
    lease_expires_at = NULL,
    last_error_code = CASE
        WHEN receipts.attempt_count >= 32 THEN 'attempts_exhausted'
        ELSE 'receipt_expired'
    END,
    completed_at = COALESCE(receipts.completed_at, (SELECT now_at FROM terminal_clock)),
    updated_at = (SELECT now_at FROM terminal_clock),
    version = receipts.version + 1
FROM candidates
WHERE receipts.id = candidates.id
RETURNING receipts.*;

-- name: GetCommerceAccountByProviderID :one
SELECT *
FROM commerce_accounts
WHERE environment = sqlc.arg(environment)::text
  AND provider_account_id = sqlc.arg(provider_account_id)::text;

-- name: UpsertCommercePaymentProjection :one
INSERT INTO commerce_payments (
    company_id, site_id, customer_id, offer_id, subscription_id, environment,
    provider_payment_intent_id, provider_checkout_session_id, status,
    amount, currency, paid_at, refunded_at, disputed_at, provider_event_at
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(site_id)::uuid,
    sqlc.arg(customer_id)::uuid,
    sqlc.arg(offer_id)::uuid,
    NULL,
    sqlc.arg(environment)::text,
    sqlc.narg(provider_payment_intent_id)::text,
    sqlc.narg(provider_checkout_session_id)::text,
    sqlc.arg(status)::text,
    sqlc.arg(amount)::bigint,
    sqlc.arg(currency)::text,
    sqlc.narg(paid_at)::timestamptz,
    sqlc.narg(refunded_at)::timestamptz,
    sqlc.narg(disputed_at)::timestamptz,
	sqlc.arg(provider_event_at)::timestamptz
)
ON CONFLICT (environment, provider_checkout_session_id)
    WHERE provider_checkout_session_id IS NOT NULL
DO UPDATE SET
	provider_payment_intent_id = CASE
		WHEN commerce_payments.provider_event_at > EXCLUDED.provider_event_at
			THEN commerce_payments.provider_payment_intent_id
		ELSE COALESCE(
			commerce_payments.provider_payment_intent_id,
			EXCLUDED.provider_payment_intent_id
		)
	END,
	status = CASE
		WHEN commerce_payments.provider_event_at > EXCLUDED.provider_event_at
			THEN commerce_payments.status
		WHEN commerce_payments.status IN ('refunded', 'disputed')
			THEN commerce_payments.status
		WHEN commerce_payments.status = 'paid' AND EXCLUDED.status <> 'paid'
			THEN 'paid'
		WHEN commerce_payments.status = 'failed' AND EXCLUDED.status = 'processing'
			THEN 'failed'
		ELSE EXCLUDED.status
	END,
	paid_at = CASE
		WHEN commerce_payments.provider_event_at > EXCLUDED.provider_event_at
			THEN commerce_payments.paid_at
		ELSE COALESCE(EXCLUDED.paid_at, commerce_payments.paid_at)
	END,
	refunded_at = CASE
		WHEN commerce_payments.provider_event_at > EXCLUDED.provider_event_at
			THEN commerce_payments.refunded_at
		ELSE COALESCE(EXCLUDED.refunded_at, commerce_payments.refunded_at)
	END,
	disputed_at = CASE
		WHEN commerce_payments.provider_event_at > EXCLUDED.provider_event_at
			THEN commerce_payments.disputed_at
		ELSE COALESCE(EXCLUDED.disputed_at, commerce_payments.disputed_at)
	END,
	provider_event_at = GREATEST(
		commerce_payments.provider_event_at,
		EXCLUDED.provider_event_at
	),
	updated_at = CASE
		WHEN commerce_payments.provider_event_at <= EXCLUDED.provider_event_at
			THEN clock_timestamp()
		ELSE commerce_payments.updated_at
	END,
	version = CASE
		WHEN commerce_payments.provider_event_at <= EXCLUDED.provider_event_at
			THEN commerce_payments.version + 1
		ELSE commerce_payments.version
	END
WHERE commerce_payments.company_id = EXCLUDED.company_id
  AND commerce_payments.site_id = EXCLUDED.site_id
  AND commerce_payments.customer_id = EXCLUDED.customer_id
  AND commerce_payments.offer_id = EXCLUDED.offer_id
	AND (
		commerce_payments.provider_payment_intent_id IS NULL
		OR EXCLUDED.provider_payment_intent_id IS NULL
		OR commerce_payments.provider_payment_intent_id = EXCLUDED.provider_payment_intent_id
	)
  AND commerce_payments.amount = EXCLUDED.amount
  AND commerce_payments.currency = EXCLUDED.currency
RETURNING *;

-- name: UpsertCommerceInvoicePaymentProjection :one
WITH terminal_payment_intent AS MATERIALIZED (
	SELECT status, refunded_at, disputed_at, provider_event_at
	FROM commerce_payments
	WHERE environment = sqlc.arg(environment)::text
	  AND provider_payment_intent_id = sqlc.arg(provider_payment_intent_id)::text
	  AND status IN ('refunded', 'disputed')
	ORDER BY provider_event_at DESC, id DESC
	LIMIT 1
)
INSERT INTO commerce_payments (
    company_id, site_id, customer_id, offer_id, subscription_id, environment,
    provider_payment_intent_id, provider_invoice_payment_id, provider_invoice_id,
    provider_checkout_session_id, status,
    amount, currency, paid_at, refunded_at, disputed_at, provider_event_at
)
SELECT
    sqlc.arg(company_id)::uuid,
    sqlc.arg(site_id)::uuid,
    sqlc.arg(customer_id)::uuid,
    sqlc.arg(offer_id)::uuid,
    sqlc.arg(subscription_id)::uuid,
    sqlc.arg(environment)::text,
    sqlc.arg(provider_payment_intent_id)::text,
    sqlc.arg(provider_invoice_payment_id)::text,
    sqlc.arg(provider_invoice_id)::text,
    NULL,
    COALESCE(
		(SELECT terminal_payment_intent.status FROM terminal_payment_intent),
		sqlc.arg(status)::text
	),
    sqlc.arg(amount)::bigint,
    sqlc.arg(currency)::text,
    sqlc.narg(paid_at)::timestamptz,
    (SELECT terminal_payment_intent.refunded_at FROM terminal_payment_intent),
    (SELECT terminal_payment_intent.disputed_at FROM terminal_payment_intent),
	GREATEST(
		sqlc.arg(provider_event_at)::timestamptz,
		COALESCE(
			(SELECT terminal_payment_intent.provider_event_at FROM terminal_payment_intent),
			sqlc.arg(provider_event_at)::timestamptz
		)
	)
ON CONFLICT (environment, provider_invoice_payment_id)
    WHERE provider_invoice_payment_id IS NOT NULL
DO UPDATE SET
	status = CASE
		WHEN commerce_payments.status IN ('refunded', 'disputed')
			THEN commerce_payments.status
		WHEN EXCLUDED.status IN ('refunded', 'disputed')
			THEN EXCLUDED.status
		WHEN commerce_payments.provider_event_at > EXCLUDED.provider_event_at
			THEN commerce_payments.status
		ELSE EXCLUDED.status
	END,
	paid_at = CASE
		WHEN commerce_payments.provider_event_at > EXCLUDED.provider_event_at
			THEN commerce_payments.paid_at
		ELSE COALESCE(EXCLUDED.paid_at, commerce_payments.paid_at)
	END,
	refunded_at = COALESCE(EXCLUDED.refunded_at, commerce_payments.refunded_at),
	disputed_at = COALESCE(EXCLUDED.disputed_at, commerce_payments.disputed_at),
	provider_event_at = GREATEST(
		commerce_payments.provider_event_at,
		EXCLUDED.provider_event_at
	),
	updated_at = CASE
		WHEN commerce_payments.provider_event_at <= EXCLUDED.provider_event_at
			THEN clock_timestamp()
		ELSE commerce_payments.updated_at
	END,
	version = CASE
		WHEN commerce_payments.provider_event_at <= EXCLUDED.provider_event_at
			THEN commerce_payments.version + 1
		ELSE commerce_payments.version
	END
WHERE commerce_payments.company_id = EXCLUDED.company_id
  AND commerce_payments.site_id = EXCLUDED.site_id
  AND commerce_payments.customer_id = EXCLUDED.customer_id
  AND commerce_payments.offer_id = EXCLUDED.offer_id
  AND commerce_payments.subscription_id = EXCLUDED.subscription_id
  AND commerce_payments.provider_invoice_id = EXCLUDED.provider_invoice_id
  AND commerce_payments.provider_payment_intent_id = EXCLUDED.provider_payment_intent_id
  AND commerce_payments.amount = EXCLUDED.amount
  AND commerce_payments.currency = EXCLUDED.currency
RETURNING *;

-- name: GetCommerceSubscriptionInvoiceRevocationState :one
SELECT
	subscriptions.provider_latest_invoice_id IS NOT DISTINCT FROM
		sqlc.arg(provider_invoice_id)::text AS is_latest_invoice,
	EXISTS (
		SELECT 1
		FROM commerce_payments AS payments
		WHERE payments.company_id = subscriptions.company_id
		  AND payments.site_id = subscriptions.site_id
		  AND payments.customer_id = subscriptions.customer_id
		  AND payments.offer_id = subscriptions.offer_id
		  AND payments.subscription_id = subscriptions.id
		  AND payments.provider_invoice_id = sqlc.arg(provider_invoice_id)::text
		  AND payments.status IN ('processing', 'paid')
	) AS has_remaining_payments,
	(
		SELECT count(*)
		FROM commerce_payments AS payments
		WHERE payments.company_id = subscriptions.company_id
		  AND payments.site_id = subscriptions.site_id
		  AND payments.customer_id = subscriptions.customer_id
		  AND payments.offer_id = subscriptions.offer_id
		  AND payments.subscription_id = subscriptions.id
		  AND payments.provider_invoice_id = sqlc.arg(provider_invoice_id)::text
	) AS payment_count
FROM commerce_subscriptions AS subscriptions
WHERE subscriptions.id = sqlc.arg(subscription_id)::uuid
  AND subscriptions.company_id = sqlc.arg(company_id)::uuid
  AND subscriptions.site_id = sqlc.arg(site_id)::uuid
  AND subscriptions.customer_id = sqlc.arg(customer_id)::uuid
  AND subscriptions.offer_id = sqlc.arg(offer_id)::uuid;

-- name: UpdateCommercePaymentTerminalProjection :one
UPDATE commerce_payments
SET status = CASE
		WHEN provider_event_at <= sqlc.arg(provider_event_at)::timestamptz
			THEN sqlc.arg(status)::text
		ELSE status
	END,
    refunded_at = CASE
		WHEN provider_event_at <= sqlc.arg(provider_event_at)::timestamptz
			AND sqlc.arg(status)::text = 'refunded'
			THEN sqlc.arg(provider_event_at)::timestamptz
        ELSE refunded_at
    END,
    disputed_at = CASE
		WHEN provider_event_at <= sqlc.arg(provider_event_at)::timestamptz
			AND sqlc.arg(status)::text = 'disputed'
			THEN sqlc.arg(provider_event_at)::timestamptz
        ELSE disputed_at
    END,
	provider_event_at = GREATEST(
		provider_event_at,
		sqlc.arg(provider_event_at)::timestamptz
	),
	updated_at = CASE
		WHEN provider_event_at <= sqlc.arg(provider_event_at)::timestamptz
			THEN clock_timestamp()
		ELSE updated_at
	END,
	version = CASE
		WHEN provider_event_at <= sqlc.arg(provider_event_at)::timestamptz
			THEN commerce_payments.version + 1
		ELSE commerce_payments.version
	END
WHERE id = sqlc.arg(id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND environment = sqlc.arg(environment)::text
  AND provider_payment_intent_id = sqlc.arg(provider_payment_intent_id)::text
  AND status IN ('processing', 'paid', 'failed', 'refunded', 'disputed')
  AND sqlc.arg(status)::text IN ('refunded', 'disputed')
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: UpsertCommerceSubscriptionProjection :one
INSERT INTO commerce_subscriptions (
    company_id, site_id, customer_id, offer_id, environment,
    provider_subscription_id, provider_latest_invoice_id, status,
    cancel_at_period_end, current_period_start, current_period_end,
	provider_event_at
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(site_id)::uuid,
    sqlc.arg(customer_id)::uuid,
    sqlc.arg(offer_id)::uuid,
    sqlc.arg(environment)::text,
    sqlc.arg(provider_subscription_id)::text,
    sqlc.narg(provider_latest_invoice_id)::text,
    sqlc.arg(status)::text,
    sqlc.arg(cancel_at_period_end)::boolean,
    sqlc.narg(current_period_start)::timestamptz,
    sqlc.narg(current_period_end)::timestamptz,
	sqlc.arg(provider_event_at)::timestamptz
)
ON CONFLICT (environment, provider_subscription_id)
DO UPDATE SET
	offer_id = CASE
		WHEN commerce_subscriptions.provider_event_at <= EXCLUDED.provider_event_at
			THEN EXCLUDED.offer_id
		ELSE commerce_subscriptions.offer_id
	END,
	provider_latest_invoice_id = CASE
		WHEN commerce_subscriptions.provider_event_at <= EXCLUDED.provider_event_at
			THEN EXCLUDED.provider_latest_invoice_id
		ELSE commerce_subscriptions.provider_latest_invoice_id
	END,
	status = CASE
		WHEN commerce_subscriptions.provider_event_at <= EXCLUDED.provider_event_at
			THEN EXCLUDED.status
		ELSE commerce_subscriptions.status
	END,
	cancel_at_period_end = CASE
		WHEN commerce_subscriptions.provider_event_at <= EXCLUDED.provider_event_at
			THEN EXCLUDED.cancel_at_period_end
		ELSE commerce_subscriptions.cancel_at_period_end
	END,
	current_period_start = CASE
		WHEN commerce_subscriptions.provider_event_at <= EXCLUDED.provider_event_at
			THEN EXCLUDED.current_period_start
		ELSE commerce_subscriptions.current_period_start
	END,
	current_period_end = CASE
		WHEN commerce_subscriptions.provider_event_at <= EXCLUDED.provider_event_at
			THEN EXCLUDED.current_period_end
		ELSE commerce_subscriptions.current_period_end
	END,
	provider_event_at = GREATEST(
		commerce_subscriptions.provider_event_at,
		EXCLUDED.provider_event_at
	),
	updated_at = CASE
		WHEN commerce_subscriptions.provider_event_at <= EXCLUDED.provider_event_at
			THEN clock_timestamp()
		ELSE commerce_subscriptions.updated_at
	END,
	version = CASE
		WHEN commerce_subscriptions.provider_event_at <= EXCLUDED.provider_event_at
			THEN commerce_subscriptions.version + 1
		ELSE commerce_subscriptions.version
	END
WHERE commerce_subscriptions.company_id = EXCLUDED.company_id
  AND commerce_subscriptions.site_id = EXCLUDED.site_id
  AND commerce_subscriptions.customer_id = EXCLUDED.customer_id
RETURNING *;

-- name: UpsertCommerceEntitlementProjection :one
INSERT INTO commerce_entitlements (
    company_id, site_id, customer_id, offer_id, payment_id,
    subscription_id, grant_id, entitlement_key, source_type, status, starts_at,
    expires_at, revoked_reason, provider_event_at
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(site_id)::uuid,
    sqlc.arg(customer_id)::uuid,
    sqlc.arg(offer_id)::uuid,
    sqlc.narg(payment_id)::uuid,
    sqlc.narg(subscription_id)::uuid,
    sqlc.arg(grant_id)::uuid,
    sqlc.arg(entitlement_key)::text,
    sqlc.arg(source_type)::text,
    sqlc.arg(status)::text,
    sqlc.narg(starts_at)::timestamptz,
    sqlc.narg(expires_at)::timestamptz,
    sqlc.narg(revoked_reason)::text,
	sqlc.arg(provider_event_at)::timestamptz
)
ON CONFLICT (site_id, customer_id, source_type, grant_id, entitlement_key)
DO UPDATE SET
	offer_id = CASE
		WHEN commerce_entitlements.provider_event_at <= EXCLUDED.provider_event_at
			THEN EXCLUDED.offer_id
		ELSE commerce_entitlements.offer_id
	END,
	payment_id = CASE
		WHEN commerce_entitlements.source_type = 'subscription'
			AND commerce_entitlements.provider_event_at <= EXCLUDED.provider_event_at
			THEN EXCLUDED.payment_id
		WHEN commerce_entitlements.provider_event_at <= EXCLUDED.provider_event_at
			AND EXCLUDED.payment_id IS NOT NULL
			THEN EXCLUDED.payment_id
		ELSE commerce_entitlements.payment_id
	END,
    status = CASE
		WHEN commerce_entitlements.provider_event_at > EXCLUDED.provider_event_at
			THEN commerce_entitlements.status
        WHEN commerce_entitlements.status = 'denied'
            AND commerce_entitlements.revoked_reason IN ('refunded', 'dispute_lost')
            AND commerce_entitlements.payment_id IS NOT DISTINCT FROM EXCLUDED.payment_id
            THEN commerce_entitlements.status
        ELSE EXCLUDED.status
    END,
	starts_at = CASE
		WHEN commerce_entitlements.provider_event_at <= EXCLUDED.provider_event_at
			THEN EXCLUDED.starts_at
		ELSE commerce_entitlements.starts_at
	END,
	expires_at = CASE
		WHEN commerce_entitlements.provider_event_at <= EXCLUDED.provider_event_at
			THEN EXCLUDED.expires_at
		ELSE commerce_entitlements.expires_at
	END,
    revoked_reason = CASE
		WHEN commerce_entitlements.provider_event_at > EXCLUDED.provider_event_at
			THEN commerce_entitlements.revoked_reason
        WHEN commerce_entitlements.status = 'denied'
            AND commerce_entitlements.revoked_reason IN ('refunded', 'dispute_lost')
            AND commerce_entitlements.payment_id IS NOT DISTINCT FROM EXCLUDED.payment_id
            THEN commerce_entitlements.revoked_reason
        ELSE EXCLUDED.revoked_reason
    END,
	provider_event_at = GREATEST(
		commerce_entitlements.provider_event_at,
		EXCLUDED.provider_event_at
	),
	updated_at = CASE
		WHEN commerce_entitlements.provider_event_at <= EXCLUDED.provider_event_at
			THEN clock_timestamp()
		ELSE commerce_entitlements.updated_at
	END,
	version = CASE
		WHEN commerce_entitlements.provider_event_at <= EXCLUDED.provider_event_at
			THEN commerce_entitlements.version + 1
		ELSE commerce_entitlements.version
	END
WHERE commerce_entitlements.company_id = EXCLUDED.company_id
  AND commerce_entitlements.site_id = EXCLUDED.site_id
  AND commerce_entitlements.customer_id = EXCLUDED.customer_id
  AND commerce_entitlements.subscription_id IS NOT DISTINCT FROM EXCLUDED.subscription_id
RETURNING *;

-- name: DenySupersededCommerceSubscriptionEntitlements :execrows
UPDATE commerce_entitlements
SET status = 'denied',
    expires_at = sqlc.arg(provider_event_at)::timestamptz,
    revoked_reason = 'plan_changed',
    provider_event_at = sqlc.arg(provider_event_at)::timestamptz,
    updated_at = clock_timestamp(),
    version = commerce_entitlements.version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid
  AND customer_id = sqlc.arg(customer_id)::uuid
  AND subscription_id = sqlc.arg(subscription_id)::uuid
  AND offer_id <> sqlc.arg(active_offer_id)::uuid
  AND source_type = 'subscription'
  AND status IN ('processing', 'active')
  AND provider_event_at <= sqlc.arg(provider_event_at)::timestamptz;

-- name: GetCommerceOfferByProviderPrice :one
SELECT *
FROM commerce_offers
WHERE environment = sqlc.arg(environment)::text
  AND provider_price_id = sqlc.arg(provider_price_id)::text;

-- name: GetCommerceOfferByID :one
SELECT *
FROM commerce_offers
WHERE id = sqlc.arg(id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND site_id = sqlc.arg(site_id)::uuid;

-- name: GetCommerceCustomerByProviderID :one
SELECT *
FROM commerce_customers
WHERE environment = sqlc.arg(environment)::text
  AND provider_customer_id = sqlc.arg(provider_customer_id)::text;

-- name: ListCommercePaymentsByProviderIntent :many
SELECT *
FROM commerce_payments
WHERE environment = sqlc.arg(environment)::text
  AND provider_payment_intent_id = sqlc.arg(provider_payment_intent_id)::text
ORDER BY id
FOR UPDATE;

-- name: GetCommerceSubscriptionByProviderID :one
SELECT *
FROM commerce_subscriptions
WHERE environment = sqlc.arg(environment)::text
  AND provider_subscription_id = sqlc.arg(provider_subscription_id)::text;
