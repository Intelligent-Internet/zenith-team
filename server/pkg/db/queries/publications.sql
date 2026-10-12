-- name: GetPublicationAuthority :one
SELECT
    outputs.id AS output_id,
    outputs.type AS output_type,
    output_items.id AS output_item_id,
    runs.status AS run_status,
    runs.task_id,
    conversations.kind AS conversation_kind,
    conversations.created_by_user_id AS conversation_created_by_user_id,
    deliverables.status AS deliverable_status,
    company_members.role AS caller_role,
    access.access_level AS caller_access_level,
    attachments.object_key,
    attachments.filename,
    attachments.content_type,
    attachments.size_bytes,
    attachments.status AS attachment_status
FROM runs
JOIN conversations
  ON conversations.id = runs.conversation_id
 AND conversations.company_id = runs.company_id
JOIN companies
  ON companies.id = runs.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
  ON company_members.company_id = runs.company_id
 AND company_members.user_id = sqlc.arg(user_id)::uuid
 AND company_members.status = 'active'
JOIN outputs
  ON outputs.id = (
    SELECT output_rows.id
    FROM outputs AS output_rows
    WHERE output_rows.run_id = runs.id
      AND output_rows.company_id = runs.company_id
    ORDER BY output_rows.presentation_number DESC
    LIMIT 1
  )
 AND outputs.company_id = runs.company_id
JOIN output_access AS access
  ON access.company_id = outputs.company_id
 AND access.output_id = outputs.id
 AND access.user_id = sqlc.arg(user_id)::uuid
JOIN output_items
  ON output_items.output_id = outputs.id
 AND output_items.company_id = outputs.company_id
 AND output_items.role = 'primary'
JOIN asset_versions
  ON asset_versions.id = output_items.asset_version_id
 AND asset_versions.company_id = output_items.company_id
 AND asset_versions.integrity_status = 'verified'
JOIN attachments
  ON attachments.id = asset_versions.attachment_id
 AND attachments.company_id = asset_versions.company_id
LEFT JOIN deliverables
  ON deliverables.output_id = outputs.id
 AND deliverables.company_id = outputs.company_id
WHERE runs.id = sqlc.arg(run_id)::uuid
  AND runs.company_id = sqlc.arg(company_id)::uuid;

-- name: GetOutputPublicationByOutput :one
SELECT * FROM output_publications
WHERE company_id = sqlc.arg(company_id)::uuid
  AND output_id = sqlc.arg(output_id)::uuid;

-- name: GetOutputPublicationByOutputForUpdate :one
SELECT * FROM output_publications
WHERE company_id = sqlc.arg(company_id)::uuid
  AND output_id = sqlc.arg(output_id)::uuid
FOR UPDATE;

-- name: InsertOutputPublication :one
INSERT INTO output_publications (
    company_id, output_id, output_item_id, requested_by_user_id, service_name,
    provider_type, provider_binding_id
) VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(output_id)::uuid,
    sqlc.arg(output_item_id)::uuid,
    sqlc.arg(requested_by_user_id)::uuid,
    sqlc.arg(service_name)::text,
    sqlc.arg(provider_type)::text,
    sqlc.arg(provider_binding_id)::text
)
ON CONFLICT (output_id) DO NOTHING
RETURNING *;

-- name: RequeueOutputPublication :one
UPDATE output_publications
SET status = 'queued',
    route_pending = CASE WHEN provider_resources_may_exist THEN route_pending ELSE false END,
    requested_by_user_id = sqlc.arg(requested_by_user_id)::uuid,
    provider_type = sqlc.arg(provider_type)::text,
    provider_binding_id = sqlc.arg(provider_binding_id)::text,
    source_ref = CASE WHEN provider_resources_may_exist THEN source_ref ELSE NULL END,
    source_template = CASE WHEN provider_resources_may_exist THEN source_template ELSE NULL END,
    build_ref = CASE WHEN provider_resources_may_exist THEN build_ref ELSE NULL END,
    artifact_ref = CASE WHEN provider_resources_may_exist THEN artifact_ref ELSE NULL END,
    artifact_digest = CASE WHEN provider_resources_may_exist THEN artifact_digest ELSE NULL END,
    service_ref = CASE WHEN provider_resources_may_exist THEN service_ref ELSE NULL END,
    public_url = CASE WHEN provider_resources_may_exist THEN public_url ELSE NULL END,
    failure_code = NULL,
    failure_detail = NULL,
    attempt_count = 0,
    next_attempt_at = statement_timestamp(),
    lease_id = NULL,
    lease_expires_at = NULL,
    published_at = CASE WHEN provider_resources_may_exist THEN published_at ELSE NULL END,
    unpublished_at = NULL,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(publication_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND output_id = sqlc.arg(output_id)::uuid
  AND output_item_id = sqlc.arg(output_item_id)::uuid
  AND (
      (status = 'unpublished' AND provider_resources_may_exist = false)
      OR (
          status = 'failed'
          AND failure_code IN ('provider_unavailable', 'deploy_failed')
      )
  )
  AND (
      provider_resources_may_exist = false
      OR (
          provider_type = sqlc.arg(provider_type)::text
          AND provider_binding_id = sqlc.arg(provider_binding_id)::text
      )
  )
  AND version < 9007199254740991
RETURNING *;

-- name: MarkOutputPublicationUnpublishing :one
UPDATE output_publications
SET status = 'unpublishing',
    route_pending = false,
    requested_by_user_id = sqlc.arg(requested_by_user_id)::uuid,
    failure_code = NULL,
    failure_detail = NULL,
    attempt_count = 0,
    next_attempt_at = statement_timestamp(),
    lease_id = NULL,
    lease_expires_at = NULL,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(publication_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND output_id = sqlc.arg(output_id)::uuid
  AND (
      status = 'published'
      OR (
          status = 'failed'
          AND provider_resources_may_exist = true
      )
  )
  AND version = sqlc.arg(expected_version)::bigint
  AND version < 9007199254740991
RETURNING *;

-- name: ClaimOutputPublicationWork :one
WITH candidate AS MATERIALIZED (
    SELECT output_publications.id
    FROM output_publications
    WHERE (
        (
            status IN ('queued', 'routing', 'unpublishing')
            AND lease_id IS NULL
            AND next_attempt_at <= clock_timestamp()
        )
        OR (
            status IN ('uploading', 'building', 'deploying', 'routing', 'unpublishing')
            AND lease_id IS NOT NULL
            AND lease_expires_at <= clock_timestamp()
        )
    )
      AND attempt_count < sqlc.arg(max_attempts)::integer
    ORDER BY next_attempt_at, created_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
), claimed AS MATERIALIZED (
    UPDATE output_publications
    SET status = CASE
            WHEN output_publications.status = 'unpublishing' THEN 'unpublishing'
            WHEN output_publications.route_pending THEN 'routing'
            ELSE 'uploading'
        END,
        attempt_count = attempt_count + 1,
        lease_id = sqlc.arg(lease_id)::uuid,
        lease_expires_at = clock_timestamp()
            + sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond',
        updated_at = statement_timestamp(),
        version = version + 1
    FROM candidate
    WHERE output_publications.id = candidate.id
      AND output_publications.version < 9007199254740991
    RETURNING output_publications.*
)
SELECT claimed.*
FROM claimed;

-- name: GetOutputPublicationSource :one
SELECT
    attachments.object_key,
    attachments.filename,
    attachments.content_type,
    attachments.size_bytes AS attachment_size_bytes,
    attachments.status AS attachment_status
FROM output_publications
JOIN output_items
  ON output_items.id = output_publications.output_item_id
 AND output_items.output_id = output_publications.output_id
 AND output_items.company_id = output_publications.company_id
 AND output_items.role = 'primary'
JOIN asset_versions
  ON asset_versions.id = output_items.asset_version_id
 AND asset_versions.company_id = output_items.company_id
 AND asset_versions.integrity_status = 'verified'
JOIN attachments
  ON attachments.id = asset_versions.attachment_id
 AND attachments.company_id = asset_versions.company_id
WHERE output_publications.id = sqlc.arg(publication_id)::uuid
  AND output_publications.lease_id = sqlc.arg(lease_id)::uuid
  AND output_publications.status = 'uploading';

-- name: FailExpiredOutputPublications :many
UPDATE output_publications
SET status = 'failed',
    failure_code = sqlc.arg(failure_code)::text,
    failure_detail = sqlc.arg(failure_detail)::text,
    lease_id = NULL,
    lease_expires_at = NULL,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE status IN ('uploading', 'building', 'deploying', 'routing', 'unpublishing')
  AND lease_id IS NOT NULL
  AND lease_expires_at <= clock_timestamp()
  AND attempt_count >= sqlc.arg(max_attempts)::integer
  AND version < 9007199254740991
RETURNING *;

-- name: UpdateOutputPublicationProgress :one
UPDATE output_publications
SET status = sqlc.arg(status)::text,
    source_ref = COALESCE(sqlc.narg(source_ref)::text, source_ref),
    source_template = COALESCE(sqlc.narg(source_template)::text, source_template),
    build_ref = COALESCE(sqlc.narg(build_ref)::text, build_ref),
    artifact_ref = COALESCE(sqlc.narg(artifact_ref)::text, artifact_ref),
    artifact_digest = COALESCE(sqlc.narg(artifact_digest)::text, artifact_digest),
    lease_expires_at = clock_timestamp()
        + sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond',
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(publication_id)::uuid
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND status IN ('uploading', 'building', 'deploying')
  AND version < 9007199254740991
RETURNING *;

-- name: MarkOutputPublicationProviderResourcesPossible :one
UPDATE output_publications
SET provider_resources_may_exist = true,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(publication_id)::uuid
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND status = 'uploading'
  AND version < 9007199254740991
RETURNING *;

-- name: CompleteOutputPublication :one
UPDATE output_publications
SET status = CASE WHEN custom_hostname IS NULL THEN 'published' ELSE 'routing' END,
    route_pending = (custom_hostname IS NOT NULL),
    attempt_count = 0,
    next_attempt_at = statement_timestamp(),
    source_ref = sqlc.narg(source_ref)::text,
    source_template = sqlc.narg(source_template)::text,
    build_ref = sqlc.narg(build_ref)::text,
    artifact_ref = sqlc.narg(artifact_ref)::text,
    artifact_digest = sqlc.narg(artifact_digest)::text,
    service_ref = sqlc.arg(service_ref)::text,
    public_url = sqlc.arg(public_url)::text,
    provider_resources_may_exist = true,
    failure_code = NULL,
    failure_detail = NULL,
    lease_id = NULL,
    lease_expires_at = NULL,
    published_at = statement_timestamp(),
    unpublished_at = NULL,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(publication_id)::uuid
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND status = 'deploying'
  AND version < 9007199254740991
RETURNING *;

-- name: CompleteOutputUnpublication :one
UPDATE output_publications
SET status = 'unpublished',
    route_pending = false,
    public_url = NULL,
    provider_resources_may_exist = false,
    failure_code = NULL,
    failure_detail = NULL,
    lease_id = NULL,
    lease_expires_at = NULL,
    unpublished_at = statement_timestamp(),
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(publication_id)::uuid
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND status = 'unpublishing'
  AND version < 9007199254740991
RETURNING *;

-- name: RetryOutputPublication :one
UPDATE output_publications
SET status = sqlc.arg(status)::text,
    next_attempt_at = clock_timestamp()
        + sqlc.arg(retry_milliseconds)::bigint * interval '1 millisecond',
    lease_id = NULL,
    lease_expires_at = NULL,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(publication_id)::uuid
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND output_publications.status IN ('uploading', 'building', 'deploying', 'routing', 'unpublishing')
  AND version < 9007199254740991
RETURNING *;

-- name: ReleaseOutputPublicationLease :one
UPDATE output_publications
SET status = sqlc.arg(status)::text,
    attempt_count = GREATEST(attempt_count - 1, 0),
    next_attempt_at = statement_timestamp(),
    lease_id = NULL,
    lease_expires_at = NULL,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(publication_id)::uuid
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND output_publications.status IN ('uploading', 'building', 'deploying', 'routing', 'unpublishing')
  AND attempt_count > 0
  AND version < 9007199254740991
RETURNING *;

-- name: FailOutputPublication :one
UPDATE output_publications
SET status = 'failed',
    failure_code = sqlc.arg(failure_code)::text,
    failure_detail = sqlc.arg(failure_detail)::text,
    lease_id = NULL,
    lease_expires_at = NULL,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(publication_id)::uuid
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND status IN ('uploading', 'building', 'deploying', 'routing', 'unpublishing')
  AND version < 9007199254740991
RETURNING *;

-- name: SetOutputPublicationSubdomain :one
UPDATE output_publications
SET custom_hostname = sqlc.arg(custom_hostname)::text,
    route_binding_id = sqlc.arg(route_binding_id)::text,
    route_pending = true,
    status = 'routing',
    requested_by_user_id = sqlc.arg(requested_by_user_id)::uuid,
    attempt_count = 0,
    failure_code = NULL,
    failure_detail = NULL,
    next_attempt_at = statement_timestamp(),
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(publication_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
  AND version < 9007199254740991
  AND lease_id IS NULL
  AND (status = 'published' OR (status = 'failed' AND route_pending))
  AND (custom_hostname IS NULL OR custom_hostname = sqlc.arg(custom_hostname)::text)
RETURNING *;

-- name: CompleteOutputPublicationRouting :one
UPDATE output_publications
SET status = 'published',
    route_pending = false,
    failure_code = NULL,
    failure_detail = NULL,
    lease_id = NULL,
    lease_expires_at = NULL,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(publication_id)::uuid
  AND lease_id = sqlc.arg(lease_id)::uuid
  AND status = 'routing'
  AND version < 9007199254740991
RETURNING *;
