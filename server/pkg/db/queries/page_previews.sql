-- name: CreatePageSourcePreview :one
-- Caller holds the Run admission locks. Copy its exact conversation identity;
-- at most eight retained generations can coexist for one Run.
INSERT INTO project_page_source_previews (
 company_id,project_id,page_id,run_id,conversation_id,created_by_user_id,request_key
)
SELECT r.company_id,NULLIF(sqlc.arg(project_id)::uuid,'00000000-0000-0000-0000-000000000000'::uuid),sqlc.arg(page_id)::uuid,r.id,r.conversation_id,
 sqlc.arg(actor_user_id)::uuid,sqlc.arg(request_key)::uuid
FROM runs r
WHERE r.company_id=sqlc.arg(company_id)::uuid AND r.id=sqlc.arg(run_id)::uuid AND r.status='running'
 AND (SELECT count(*) FROM project_page_source_previews p
      WHERE p.company_id=r.company_id AND p.run_id=r.id AND p.expires_at>clock_timestamp()) < 8
RETURNING *;

-- name: GetPageSourcePreviewReceipt :one
SELECT * FROM project_page_source_previews
WHERE company_id=$1 AND run_id=$2 AND request_key=$3 AND expires_at>clock_timestamp();

-- name: LockPageSourcePreview :one
SELECT * FROM project_page_source_previews
WHERE company_id=$1 AND run_id=$2 AND id=$3 AND expires_at>clock_timestamp()
FOR UPDATE;

-- name: UpdatePageSourcePreview :one
UPDATE project_page_source_previews
SET source=sqlc.arg(source)::text,revision=revision+1,discarded=sqlc.arg(discarded)::boolean,
 last_request_key=sqlc.arg(request_key)::uuid,last_request_hash=sqlc.arg(request_hash)::text,
 updated_at=clock_timestamp(),
 expires_at=LEAST(created_at+interval '1 hour',clock_timestamp()+interval '15 minutes')
WHERE company_id=sqlc.arg(company_id)::uuid AND run_id=sqlc.arg(run_id)::uuid AND id=sqlc.arg(id)::uuid
 AND revision=sqlc.arg(expected_revision)::bigint AND NOT discarded AND expires_at>clock_timestamp()
RETURNING *;

-- name: GetPageSourcePreview :one
-- The host must separately prove Page and originating Conversation read access.
SELECT * FROM project_page_source_previews
WHERE company_id=$1 AND project_id IS NOT DISTINCT FROM $2 AND page_id=$3 AND id=$4 AND expires_at>clock_timestamp();

-- name: DeleteExpiredPageSourcePreviews :execrows
WITH expired AS (
 SELECT id FROM project_page_source_previews
 WHERE expires_at<=clock_timestamp()
 ORDER BY expires_at,id LIMIT 100 FOR UPDATE SKIP LOCKED
)
DELETE FROM project_page_source_previews p USING expired e WHERE p.id=e.id;

-- name: GetPageSourcePreviewForMember :one
-- Project/Page admission is held by the caller. Conversation readers restrict
-- private document-thread Runs independently of the destination Page's readers.
SELECT p.* FROM project_page_source_previews p
WHERE p.company_id=sqlc.arg(company_id)::uuid
 AND p.project_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(project_id)::uuid,'00000000-0000-0000-0000-000000000000'::uuid) AND p.page_id=sqlc.arg(page_id)::uuid
 AND p.id=sqlc.arg(id)::uuid AND p.expires_at>clock_timestamp()
 AND EXISTS (
  SELECT 1 FROM conversation_readers r
  WHERE r.company_id=p.company_id AND r.conversation_id=p.conversation_id
   AND r.user_id=sqlc.arg(actor_user_id)::uuid
 );

-- name: ListPageSourcePreviewsForMember :many
-- One Run has at most eight retained generations. Return metadata only; clients
-- fetch source for the generation they actually display.
SELECT p.id,p.project_id,p.page_id,p.run_id,p.conversation_id,p.revision,p.discarded,p.expires_at
FROM project_page_source_previews p
WHERE p.company_id=sqlc.arg(company_id)::uuid
 AND p.project_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(project_id)::uuid,'00000000-0000-0000-0000-000000000000'::uuid) AND p.page_id=sqlc.arg(page_id)::uuid
 AND p.run_id=sqlc.arg(run_id)::uuid AND p.expires_at>clock_timestamp()
 AND EXISTS (
  SELECT 1 FROM conversation_readers r
  WHERE r.company_id=p.company_id AND r.conversation_id=p.conversation_id
   AND r.user_id=sqlc.arg(actor_user_id)::uuid
 )
ORDER BY p.created_at,p.id LIMIT 8;

-- name: ListPageSourcePreviewReaders :many
-- Always resolve the intersection explicitly, including for Company-visible
-- Projects: the originating document-thread conversation can still be private.
SELECT DISTINCT access.user_id
FROM project_page_source_previews p
JOIN artifact_access access ON access.company_id=p.company_id AND access.artifact_id=p.page_id
JOIN conversation_readers reader ON reader.company_id=p.company_id
 AND reader.conversation_id=p.conversation_id AND reader.user_id=access.user_id
WHERE p.company_id=$1 AND p.id=$2
ORDER BY access.user_id;
