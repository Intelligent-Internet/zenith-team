-- name: GetCompanyResourceAccess :one
SELECT companies.id AS company_id, company_members.role
FROM companies
JOIN company_members
  ON company_members.company_id = companies.id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
WHERE companies.id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL;

-- name: ListCompanyResourcesForOwner :many
SELECT
  sqlc.embed(company_resources),
  daemon_projection.daemon_name,
  daemon_projection.daemon_status,
  daemon_projection.daemon_last_seen_at,
  COALESCE(creator_users.display_name, 'Unknown')::text AS created_by_display_name,
  creator_users.avatar_url AS created_by_avatar_url
FROM company_resources
JOIN companies ON companies.id = company_resources.company_id
JOIN company_members
  ON company_members.company_id = company_resources.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
 AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
LEFT JOIN users AS creator_users
  ON creator_users.id = company_resources.created_by_user_id
LEFT JOIN LATERAL (
  SELECT
    COALESCE(
      (array_agg(
        runtimes.device_name
        ORDER BY runtimes.updated_at DESC, runtimes.id DESC
      ) FILTER (
        WHERE runtimes.device_name IS NOT NULL
      ))[1],
      company_resources.daemon_id
    )::text AS daemon_name,
    CASE
      WHEN count(*) = 0 THEN 'unregistered'
      WHEN bool_or(runtimes.status = 'online') THEN 'online'
      ELSE 'offline'
    END::text AS daemon_status,
    max(runtimes.last_seen_at)::timestamptz AS daemon_last_seen_at
  FROM runtimes
  WHERE runtimes.company_id = company_resources.company_id
    AND runtimes.daemon_id = company_resources.daemon_id
) AS daemon_projection ON true
WHERE company_resources.company_id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL
  AND (
      sqlc.narg(daemon_id)::text IS NULL
      OR company_resources.daemon_id = sqlc.narg(daemon_id)::text
  )
ORDER BY company_resources.position, company_resources.created_at, company_resources.id;

-- name: GetCompanyResourceForOwner :one
SELECT
  sqlc.embed(company_resources),
  daemon_projection.daemon_name,
  daemon_projection.daemon_status,
  daemon_projection.daemon_last_seen_at,
  COALESCE(creator_users.display_name, 'Unknown')::text AS created_by_display_name,
  creator_users.avatar_url AS created_by_avatar_url
FROM company_resources
JOIN companies ON companies.id = company_resources.company_id
JOIN company_members
  ON company_members.company_id = company_resources.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
 AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
LEFT JOIN users AS creator_users
  ON creator_users.id = company_resources.created_by_user_id
LEFT JOIN LATERAL (
  SELECT
    COALESCE(
      (array_agg(
        runtimes.device_name
        ORDER BY runtimes.updated_at DESC, runtimes.id DESC
      ) FILTER (
        WHERE runtimes.device_name IS NOT NULL
      ))[1],
      company_resources.daemon_id
    )::text AS daemon_name,
    CASE
      WHEN count(*) = 0 THEN 'unregistered'
      WHEN bool_or(runtimes.status = 'online') THEN 'online'
      ELSE 'offline'
    END::text AS daemon_status,
    max(runtimes.last_seen_at)::timestamptz AS daemon_last_seen_at
  FROM runtimes
  WHERE runtimes.company_id = company_resources.company_id
    AND runtimes.daemon_id = company_resources.daemon_id
) AS daemon_projection ON true
WHERE company_resources.id = sqlc.arg(company_resource_id)::uuid
  AND company_resources.company_id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL;

-- GetCompanyResourceProjection is the transaction-local canonical resource
-- read used after insert/update and for replay receipts. Authorization and row
-- locking stay with the caller that already owns the transaction.
-- name: GetCompanyResourceProjection :one
SELECT
  sqlc.embed(company_resources),
  daemon_projection.daemon_name,
  daemon_projection.daemon_status,
  daemon_projection.daemon_last_seen_at,
  COALESCE(creator_users.display_name, 'Unknown')::text AS created_by_display_name,
  creator_users.avatar_url AS created_by_avatar_url
FROM company_resources
LEFT JOIN users AS creator_users
  ON creator_users.id = company_resources.created_by_user_id
LEFT JOIN LATERAL (
  SELECT
    COALESCE(
      (array_agg(
        runtimes.device_name
        ORDER BY runtimes.updated_at DESC, runtimes.id DESC
      ) FILTER (
        WHERE runtimes.device_name IS NOT NULL
      ))[1],
      company_resources.daemon_id
    )::text AS daemon_name,
    CASE
      WHEN count(*) = 0 THEN 'unregistered'
      WHEN bool_or(runtimes.status = 'online') THEN 'online'
      ELSE 'offline'
    END::text AS daemon_status,
    max(runtimes.last_seen_at)::timestamptz AS daemon_last_seen_at
  FROM runtimes
  WHERE runtimes.company_id = company_resources.company_id
    AND runtimes.daemon_id = company_resources.daemon_id
) AS daemon_projection ON true
WHERE company_resources.id = sqlc.arg(company_resource_id)::uuid
  AND company_resources.company_id = sqlc.arg(company_id)::uuid;

-- name: GetCompanyResourceByIDForUpdate :one
SELECT company_resources.*
FROM company_resources
WHERE company_resources.id = sqlc.arg(company_resource_id)::uuid
  AND company_resources.company_id = sqlc.arg(company_id)::uuid
FOR UPDATE;

-- name: GetCompanyResourceByDaemonForUpdate :one
SELECT company_resources.*
FROM company_resources
WHERE company_resources.company_id = sqlc.arg(company_id)::uuid
  AND company_resources.daemon_id = sqlc.arg(daemon_id)::text
FOR UPDATE;

-- name: InsertCompanyResource :one
INSERT INTO company_resources (
  company_id, resource_type, daemon_id, local_path, label, position,
  metadata, created_by_user_id
) VALUES (
  sqlc.arg(company_id)::uuid,
  sqlc.arg(resource_type)::text,
  sqlc.arg(daemon_id)::text,
  sqlc.arg(local_path)::text,
  sqlc.arg(label)::text,
  sqlc.arg(position)::integer,
  sqlc.arg(metadata)::jsonb,
  sqlc.arg(created_by_user_id)::uuid
)
ON CONFLICT (company_id, daemon_id) DO NOTHING
RETURNING company_resources.*;

-- name: UpdateCompanyResource :one
WITH statement_clock AS MATERIALIZED (
  SELECT clock_timestamp() AS now_at
)
UPDATE company_resources
SET label = sqlc.arg(label)::text,
    position = sqlc.arg(position)::integer,
    updated_at = statement_clock.now_at,
    version = company_resources.version + 1
FROM statement_clock
WHERE company_resources.id = sqlc.arg(company_resource_id)::uuid
  AND company_resources.company_id = sqlc.arg(company_id)::uuid
  AND company_resources.version = sqlc.arg(expected_version)::bigint
RETURNING company_resources.*;

-- name: DeleteCompanyResource :one
DELETE FROM company_resources
WHERE company_resources.id = sqlc.arg(company_resource_id)::uuid
  AND company_resources.company_id = sqlc.arg(company_id)::uuid
  AND company_resources.version = sqlc.arg(expected_version)::bigint
RETURNING company_resources.*;

-- name: GetCompanyResourceClock :one
SELECT clock_timestamp()::timestamptz AS created_at;

-- ListCompanyResourcesForRun is the enqueue-time read that puts a company's
-- local directories into the run context document. No actor join: the run has
-- already been authorized by the write that triggered it, and the daemon needs
-- every daemon's row so each machine can resolve its own.
-- name: ListCompanyResourcesForRun :many
SELECT company_resources.*
FROM company_resources
WHERE company_resources.company_id = sqlc.arg(company_id)::uuid
ORDER BY company_resources.position, company_resources.created_at, company_resources.id;
