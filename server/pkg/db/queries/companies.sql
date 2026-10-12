-- name: CreateCompany :one
INSERT INTO companies (
    name, slug, task_prefix, business_context, website_url
)
VALUES (
    sqlc.arg(name)::text,
    sqlc.arg(slug)::text,
    sqlc.arg(task_prefix)::text,
    sqlc.arg(business_context)::text,
    sqlc.narg(website_url)::text
)
RETURNING *;

-- name: CreateCompanyMember :one
INSERT INTO company_members (company_id, user_id, role)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(user_id)::uuid,
    sqlc.arg(role)::text
)
ON CONFLICT (company_id, user_id) DO UPDATE SET company_id = EXCLUDED.company_id
WHERE company_members.role = EXCLUDED.role
  AND company_members.status = 'active'
RETURNING *;

-- name: AllocateTaskNumber :one
-- Hands out the next Task number and the prefix it is displayed under, so a
-- create that carries no key can mint {task_prefix}-{number} from one row.
UPDATE companies
SET next_task_number = next_task_number + 1
WHERE id = sqlc.arg(company_id)::uuid
  AND deleting_at IS NULL
RETURNING (next_task_number - 1)::bigint AS number, task_prefix;

-- name: GetCompanyByID :one
SELECT * FROM companies
WHERE id = sqlc.arg(id)::uuid
  AND deleting_at IS NULL;

-- name: LockCompanyForProfileUpdate :one
SELECT * FROM companies
WHERE id = sqlc.arg(id)::uuid
  AND deleting_at IS NULL
FOR UPDATE;

-- name: UpdateCompanyProfile :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
)
UPDATE companies
SET name = sqlc.arg(name)::text,
    avatar_attachment_id = sqlc.narg(avatar_attachment_id)::uuid,
    contact_email = sqlc.narg(contact_email)::text,
    phone_country_iso2 = sqlc.narg(phone_country_iso2)::text,
    phone_number = sqlc.narg(phone_number)::text,
    updated_at = statement_clock.now_at,
    version = companies.version + 1
FROM statement_clock
WHERE companies.id = sqlc.arg(id)::uuid
  AND companies.deleting_at IS NULL
  AND companies.version = sqlc.arg(expected_version)::bigint
RETURNING companies.*;

-- name: LockCompanyAvatarUpload :one
-- The new Company avatar: the actor's own ready upload that nothing else
-- holds. Every member opens the avatar (AuthorizeAttachmentDownload), so a
-- file a chat, a queued prompt, a Project or an Artifact holds keeps its own
-- rule and cannot be published to the whole Company this way (ADR 0017).
SELECT a.* FROM attachments a
WHERE a.company_id = sqlc.arg(company_id)::uuid
  AND a.id = sqlc.arg(attachment_id)::uuid
  AND a.uploaded_by_user_id = sqlc.arg(actor_user_id)::uuid
  AND a.status = 'ready'
  AND NOT EXISTS (SELECT 1 FROM asset_versions v WHERE v.attachment_id = a.id)
  AND NOT EXISTS (
      SELECT 1 FROM message_attachments AS link
      WHERE link.company_id = a.company_id AND link.attachment_id = a.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM conversation_run_queue_attachments AS queued
      WHERE queued.attachment_id = a.id AND queued.released_at IS NULL
  )
  AND NOT EXISTS (
      SELECT 1 FROM project_files AS file
      WHERE file.company_id = a.company_id AND file.attachment_id = a.id
  )
FOR UPDATE OF a;

-- name: GetActiveCompanyMembership :one
SELECT company_members.*
FROM company_members
JOIN companies ON companies.id = company_members.company_id
WHERE company_members.company_id = sqlc.arg(company_id)::uuid
  AND company_members.user_id = sqlc.arg(user_id)::uuid
  AND company_members.status = 'active'
  AND companies.deleting_at IS NULL;

-- name: LockCompanyForMembershipRead :one
SELECT * FROM companies
WHERE id = sqlc.arg(company_id)::uuid
  AND deleting_at IS NULL
FOR SHARE;

-- name: GetCompanyMemberForShare :one
SELECT * FROM company_members
WHERE company_id = sqlc.arg(company_id)::uuid
  AND user_id = sqlc.arg(user_id)::uuid
FOR SHARE;

-- name: LockCompanyForMembershipMutation :one
SELECT * FROM companies
WHERE id = sqlc.arg(company_id)::uuid
  AND deleting_at IS NULL
FOR UPDATE;

-- name: ListActiveCompanyMembershipsForDeletion :many
SELECT * FROM company_members
WHERE company_id = sqlc.arg(company_id)::uuid
  AND status = 'active'
ORDER BY user_id
FOR UPDATE;

-- name: DeleteCompany :one
DELETE FROM companies
WHERE id = sqlc.arg(company_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
  AND deleting_at IS NULL
RETURNING *;

-- Remove dependents with immediate references to other branches of the Company
-- cascade before deleting their parents. Huddles have restrictive references
-- rather than an owning cascade, and Board items place files that the
-- Attachment branch also owns. Capture Run ids for FK-free records before
-- the Company cascade removes them, and erase independently retained Activity.
-- name: DeleteCompanyDependentRecords :exec
WITH company_runs AS MATERIALIZED (
    SELECT id
    FROM runs
    WHERE company_id = sqlc.arg(company_id)::uuid
), deleted_boards AS (
    DELETE FROM boards
    WHERE company_id = sqlc.arg(company_id)::uuid
), deleted_task_report_batches AS (
    DELETE FROM task_report_batches
    WHERE company_id = sqlc.arg(company_id)::uuid
), deleted_mailbox_draft_groups AS (
    DELETE FROM mailbox_draft_groups
    WHERE company_id = sqlc.arg(company_id)::uuid
), deleted_project_presentations AS (
    DELETE FROM project_presentations
    WHERE company_id = sqlc.arg(company_id)::uuid
), deleted_huddles AS (
    DELETE FROM huddles
    WHERE company_id = sqlc.arg(company_id)::uuid
), deleted_run_tokens AS (
    DELETE FROM run_tokens
    WHERE run_id IN (SELECT id FROM company_runs)
), deleted_run_usage AS (
    DELETE FROM run_usage
    WHERE run_id IN (SELECT id FROM company_runs)
), deleted_run_identity_mappings AS (
    DELETE FROM run_identity_mappings
    WHERE run_id IN (SELECT id FROM company_runs)
)
DELETE FROM activity_events
WHERE company_id = sqlc.arg(company_id)::uuid;

-- name: GetCompanyMemberForUpdate :one
SELECT * FROM company_members
WHERE company_id = sqlc.arg(company_id)::uuid
  AND user_id = sqlc.arg(user_id)::uuid
FOR UPDATE;

-- Company creation takes this as its first transactional lock because its new
-- Company row does not exist for account deletion to discover yet. SHARE
-- conflicts with account deletion's User NO KEY UPDATE lock: whichever wins
-- determines whether deletion discovers the committed founder membership or
-- creation rechecks and rejects the now-disabled founder.
-- name: LockEnabledUserForMembershipMutation :one
SELECT * FROM users
WHERE id = sqlc.arg(user_id)::uuid
  AND disabled_at IS NULL
FOR SHARE;

-- name: InsertCompanyMember :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
)
INSERT INTO company_members (
    company_id, user_id, role, status, created_at, updated_at, version
)
SELECT
    sqlc.arg(company_id)::uuid,
    sqlc.arg(user_id)::uuid,
    sqlc.arg(role)::text,
    'active',
    statement_clock.now_at,
    statement_clock.now_at,
    1
FROM statement_clock
WHERE EXISTS (
    SELECT 1 FROM users
    WHERE users.id = sqlc.arg(user_id)::uuid
      AND users.disabled_at IS NULL
)
ON CONFLICT (company_id, user_id) DO NOTHING
RETURNING company_members.*;

-- name: ReactivateCompanyMember :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
)
UPDATE company_members
SET role = sqlc.arg(role)::text,
    status = 'active',
    reactivated_at = statement_clock.now_at,
    reactivated_by_user_id = sqlc.arg(actor_user_id)::uuid,
    updated_at = statement_clock.now_at,
    version = company_members.version + 1
FROM statement_clock
WHERE company_members.company_id = sqlc.arg(company_id)::uuid
  AND company_members.user_id = sqlc.arg(user_id)::uuid
  AND company_members.status = 'revoked'
  AND company_members.version = sqlc.arg(expected_version)::bigint
  AND EXISTS (
      SELECT 1 FROM users
      WHERE users.id = company_members.user_id
        AND users.disabled_at IS NULL
  )
RETURNING company_members.*;

-- name: ChangeCompanyMemberRole :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
)
UPDATE company_members
SET role = sqlc.arg(role)::text,
    updated_at = statement_clock.now_at,
    version = company_members.version + 1
FROM statement_clock
WHERE company_members.company_id = sqlc.arg(company_id)::uuid
  AND company_members.user_id = sqlc.arg(user_id)::uuid
  AND company_members.status = 'active'
  AND company_members.role <> sqlc.arg(role)::text
  AND company_members.version = sqlc.arg(expected_version)::bigint
  AND EXISTS (
      SELECT 1 FROM users
      WHERE users.id = company_members.user_id
        AND users.disabled_at IS NULL
  )
RETURNING company_members.*;

-- name: RevokeCompanyMember :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
)
UPDATE company_members
SET status = 'revoked',
    revoked_at = statement_clock.now_at,
    revoked_by_user_id = sqlc.arg(actor_user_id)::uuid,
    updated_at = statement_clock.now_at,
    version = company_members.version + 1
FROM statement_clock
WHERE company_members.company_id = sqlc.arg(company_id)::uuid
  AND company_members.user_id = sqlc.arg(user_id)::uuid
  AND company_members.status = 'active'
  AND company_members.version = sqlc.arg(expected_version)::bigint
RETURNING company_members.*;

-- name: CountActiveCompanyOwners :one
SELECT count(*)::bigint
FROM company_members
WHERE company_id = sqlc.arg(company_id)::uuid
  AND status = 'active'
  AND role = 'owner';

-- name: ListActiveCompanyMemberIDsByRoles :many
-- The active members holding one of the roles — the audience for a fact
-- that only a permission's holders should receive. Callers pass
-- auth.RolesWith(permission).
SELECT user_id
FROM company_members
WHERE company_id = sqlc.arg(company_id)::uuid
  AND status = 'active'
  AND role = ANY(sqlc.arg(roles)::text[])
ORDER BY user_id;

-- name: ListActiveCompanyMembers :many
SELECT
    sqlc.embed(members),
    users.email AS member_email,
    users.display_name AS member_display_name,
    users.avatar_url AS member_avatar_url,
    (users.disabled_at IS NOT NULL)::boolean AS account_disabled
FROM company_members AS members
JOIN users ON users.id = members.user_id
WHERE members.company_id = sqlc.arg(company_id)::uuid
  AND members.status = 'active'
  AND EXISTS (
      SELECT 1
      FROM company_members AS actor
      JOIN companies ON companies.id = actor.company_id
      WHERE actor.company_id = members.company_id
        AND actor.user_id = sqlc.arg(actor_user_id)::uuid
        AND actor.status = 'active'
        AND companies.deleting_at IS NULL
  )
ORDER BY members.created_at, members.user_id;

-- name: GetActiveCompanyMemberProjection :one
SELECT
    sqlc.embed(members),
    users.email AS member_email,
    users.display_name AS member_display_name,
    users.avatar_url AS member_avatar_url,
    (users.disabled_at IS NOT NULL)::boolean AS account_disabled
FROM company_members AS members
JOIN users ON users.id = members.user_id
WHERE members.company_id = sqlc.arg(company_id)::uuid
  AND members.user_id = sqlc.arg(user_id)::uuid
  AND members.status = 'active';

-- name: GetCompanyForMember :one
SELECT
    companies.*,
    company_members.role AS member_role,
    EXISTS (SELECT 1 FROM company_initializations i WHERE i.company_id = companies.id
      AND i.created_by_user_id = company_members.user_id AND i.completed_at IS NULL) AS initialization_pending
FROM companies
JOIN company_members ON company_members.company_id = companies.id
WHERE companies.id = sqlc.arg(company_id)::uuid
  AND company_members.user_id = sqlc.arg(user_id)::uuid
  AND company_members.status = 'active'
  AND companies.deleting_at IS NULL;

-- name: ListCompaniesForUser :many
SELECT
    companies.*,
    company_members.role AS member_role,
    EXISTS (SELECT 1 FROM company_initializations i WHERE i.company_id = companies.id
      AND i.created_by_user_id = company_members.user_id AND i.completed_at IS NULL) AS initialization_pending
FROM companies
JOIN company_members ON company_members.company_id = companies.id
WHERE company_members.user_id = sqlc.arg(user_id)::uuid
  AND company_members.status = 'active'
  AND companies.deleting_at IS NULL
ORDER BY companies.created_at, companies.id;

-- name: GetCompanyStatistics :one
SELECT
    (
        SELECT count(*)::bigint
        FROM company_members
        WHERE company_members.company_id = companies.id
          AND company_members.status = 'active'
    ) AS member_count,
    (
        SELECT count(*)::bigint
        FROM runtimes
        WHERE runtimes.company_id = companies.id
          AND runtimes.removed_at IS NULL
    ) AS runtime_count,
    (
        SELECT count(*)::bigint
        FROM runtimes
        JOIN company_members
          ON company_members.company_id = runtimes.company_id
         AND company_members.user_id = runtimes.owner_user_id
         AND company_members.status = 'active'
        JOIN users ON users.id = runtimes.owner_user_id
        WHERE runtimes.company_id = companies.id
          AND runtimes.removed_at IS NULL
          AND runtimes.status = 'online'
          AND users.disabled_at IS NULL
    ) AS runtime_online_count
FROM companies
WHERE companies.id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL;

-- name: GetAgentForCompany :one
SELECT * FROM agents
WHERE id = sqlc.arg(agent_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND archived_at IS NULL;

-- name: GetCompanyRunBrief :one
-- What the founder told Zenith Team when they created the Company, frozen into
-- a Run's context. The durable record of the business is
-- assets/general/business_context.md, which the CEO Agent authors from this
-- seed plus its own research.
SELECT c.name, c.business_context, c.website_url
FROM companies c
WHERE c.id = sqlc.arg(company_id)::uuid AND c.deleting_at IS NULL;

-- name: CompleteAgentSetup :exec
-- Server-owned onboarding stamp, written once when the Company gains its root
-- Agent. It deliberately leaves `version` and `updated_at` alone: those carry
-- the founder-facing profile revision a client compare-and-swaps on, and a
-- background stamp must not invalidate an in-flight profile edit.
UPDATE companies
SET agent_setup_completed_at = COALESCE(
        companies.agent_setup_completed_at,
        clock_timestamp()
    )
WHERE companies.id = sqlc.arg(company_id)::uuid;

-- name: CompleteFirstPlanApplied :exec
-- Server-owned onboarding stamp, written once when the first Company Plan
-- transaction applies. Same revision rule as CompleteAgentSetup.
UPDATE companies
SET first_plan_applied_at = COALESCE(
        companies.first_plan_applied_at,
        clock_timestamp()
    )
WHERE companies.id = sqlc.arg(company_id)::uuid;
