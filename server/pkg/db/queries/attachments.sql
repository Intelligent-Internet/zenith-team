-- name: LockAttachmentCompanyAuthority :one
SELECT companies.id
FROM companies
WHERE companies.id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL
FOR SHARE OF companies;

-- name: LockAttachmentActiveMember :one
SELECT company_members.user_id
FROM company_members
JOIN users
  ON users.id = company_members.user_id
WHERE company_members.company_id = sqlc.arg(company_id)::uuid
  AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
  AND company_members.status = 'active'
  AND users.disabled_at IS NULL
FOR SHARE OF company_members, users;

-- name: GetAttachmentClock :one
SELECT clock_timestamp()::timestamptz AS now_at;

-- name: InsertPendingAttachmentIfAbsent :one
WITH insert_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
INSERT INTO attachments (
    request_key, company_id, uploaded_by_user_id,
    filename, content_type, size_bytes, status, upload_lease_expires_at,
    created_at, updated_at, version
)
SELECT
    sqlc.arg(request_key)::uuid,
    sqlc.arg(company_id)::uuid,
    sqlc.arg(uploaded_by_user_id)::uuid,
    sqlc.arg(filename)::text,
    sqlc.arg(content_type)::text,
    sqlc.arg(size_bytes)::bigint,
    'pending',
    insert_clock.now_at
        + sqlc.arg(pending_ttl_milliseconds)::bigint * interval '1 millisecond',
    insert_clock.now_at,
    insert_clock.now_at,
    1
FROM insert_clock
ON CONFLICT (company_id, uploaded_by_user_id, request_key) DO NOTHING
RETURNING *;

-- name: GetAttachmentByRequestKeyForUpdate :one
SELECT *
FROM attachments
WHERE company_id = sqlc.arg(company_id)::uuid
  AND uploaded_by_user_id = sqlc.arg(uploaded_by_user_id)::uuid
  AND request_key = sqlc.arg(request_key)::uuid
FOR UPDATE;

-- name: GetAttachmentByIDForUpdate :one
SELECT *
FROM attachments
WHERE id = sqlc.arg(attachment_id)::uuid
FOR UPDATE;

-- name: RenewPendingAttachmentLease :one
WITH lease_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE attachments
SET upload_lease_expires_at = (SELECT now_at FROM lease_clock)
        + sqlc.arg(pending_ttl_milliseconds)::bigint * interval '1 millisecond'
WHERE id = sqlc.arg(attachment_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND uploaded_by_user_id = sqlc.arg(uploaded_by_user_id)::uuid
  AND status = 'pending'
  AND version = 1
  AND upload_lease_expires_at = sqlc.arg(expected_upload_lease_expires_at)::timestamptz
  AND upload_lease_expires_at > (SELECT now_at FROM lease_clock)
RETURNING attachments.*;

-- name: GetAttachmentByLocator :one
SELECT *
FROM attachments
WHERE id = sqlc.arg(attachment_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid;

-- name: HasInaccessibleOutputAttachment :one
SELECT EXISTS (SELECT 1 WHERE EXISTS (
    SELECT 1 FROM asset_versions AS version
    JOIN output_items AS item
      ON item.asset_version_id = version.id
     AND item.company_id = version.company_id
    WHERE version.attachment_id = sqlc.arg(attachment_id)::uuid
      AND version.company_id = sqlc.arg(company_id)::uuid
      AND NOT EXISTS (
          SELECT 1 FROM output_access AS access
          WHERE access.company_id = item.company_id
            AND access.output_id = item.output_id
            AND access.user_id = sqlc.arg(actor_user_id)::uuid
      )
) OR EXISTS (
    SELECT 1 FROM project_presentations AS presentation
    JOIN project_presentation_sources AS source ON source.presentation_id=presentation.id
    WHERE presentation.company_id=sqlc.arg(company_id)::uuid
      AND presentation.export_attachment_id=sqlc.arg(attachment_id)::uuid
      AND source.output_id IS NOT NULL
      AND NOT EXISTS (SELECT 1 FROM output_access AS access
          WHERE access.company_id=source.company_id AND access.output_id=source.output_id
            AND access.user_id=sqlc.arg(actor_user_id)::uuid)
)) AS inaccessible;

-- name: AuthorizeAttachmentCompletion :one
SELECT attachments.*
FROM attachments
JOIN companies
  ON companies.id = attachments.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
  ON company_members.company_id = attachments.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
JOIN users
  ON users.id = company_members.user_id
 AND users.disabled_at IS NULL
WHERE attachments.id = sqlc.arg(attachment_id)::uuid
  AND attachments.company_id = sqlc.arg(company_id)::uuid
  AND attachments.uploaded_by_user_id = sqlc.arg(actor_user_id)::uuid
  AND attachments.status IN ('pending', 'ready');

-- name: AuthorizeAttachmentDownload :one
-- A person opens a file when attachment_access says so (ADR 0017):
-- an active Project file follows its Projects' readers, a message file its
-- chat's readers, an unlinked upload its uploader. The view says who; the
-- bytes must also be ready. The Company avatar also opens to every active
-- member, but only while nothing else holds it: no chat message, queued
-- prompt, Project or Artifact (LockCompanyAvatarUpload sets only such a
-- file; one linked somewhere later keeps that place's rule).
SELECT attachments.*
FROM attachments
WHERE attachments.id = sqlc.arg(attachment_id)::uuid
  AND attachments.company_id = sqlc.arg(company_id)::uuid
  AND attachments.status = 'ready'
  AND (
      COALESCE((
          SELECT true FROM attachment_access AS access
          WHERE access.company_id = attachments.company_id
            AND access.attachment_id = attachments.id
            AND access.user_id = sqlc.arg(actor_user_id)::uuid
          LIMIT 1
      ), false)
      OR (
          COALESCE((
              SELECT true
              FROM companies AS company
              JOIN company_members AS member
                ON member.company_id = company.id
               AND member.user_id = sqlc.arg(actor_user_id)::uuid
               AND member.status = 'active'
              JOIN users AS account
                ON account.id = member.user_id
               AND account.disabled_at IS NULL
               AND account.deleted_at IS NULL
              WHERE company.id = attachments.company_id
                AND company.deleting_at IS NULL
                AND company.avatar_attachment_id = attachments.id
              LIMIT 1
          ), false)
          AND (
              SELECT true FROM message_attachments AS link
              WHERE link.company_id = attachments.company_id
                AND link.attachment_id = attachments.id
              LIMIT 1
          ) IS NULL
          AND (
              SELECT true FROM conversation_run_queue_attachments AS queued
              WHERE queued.attachment_id = attachments.id
                AND queued.released_at IS NULL
              LIMIT 1
          ) IS NULL
          AND (
              SELECT true FROM project_attachment_scopes AS file
              WHERE file.company_id = attachments.company_id
                AND file.attachment_id = attachments.id
              LIMIT 1
          ) IS NULL
          AND (
              SELECT true FROM asset_versions AS artifact_version
              WHERE artifact_version.company_id = attachments.company_id
                AND artifact_version.attachment_id = attachments.id
              LIMIT 1
          ) IS NULL
      )
  );

-- name: AuthorizeRunAttachmentDownload :one
-- A Run's daemon download of a file its context froze, for the Run's
-- authorizer (under ADR 0014 also the machine owner, so one person check
-- covers both). The caller has already found the id in the Run's frozen
-- context; frozen_project_document says it was in the frozen Project
-- documents, which the freeze read as active files that person could open.
-- Such a file stays open to the Run after it is retired (retired rows keep
-- their bytes for frozen Runs and their retries, 000057) while the person
-- still reads one of its Projects. Every other file follows
-- attachment_access, the rule a person's own download uses. A Company
-- reference document, which every Run of the Company receives, is open to
-- any active member only while it is nothing else: no chat message carries
-- it and no Project has ever held it, so a chat file or a Project file
-- registered as a reference keeps its own rule. The Artifact exclusion
-- applies to every arm. Only a Task Run is narrowed further (below); a Run
-- tagged in a channel or group DM opens what its Authorizer opens (D31).
SELECT attachments.*
FROM attachments
JOIN runs AS run
  ON run.company_id = attachments.company_id
 AND run.id = sqlc.arg(run_id)::uuid
 AND run.execution_authorized_by_user_id = sqlc.arg(authorizer_user_id)::uuid
JOIN companies AS company
  ON company.id = attachments.company_id
 AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = attachments.company_id
 AND member.user_id = sqlc.arg(authorizer_user_id)::uuid
 AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id
 AND account.disabled_at IS NULL
 AND account.deleted_at IS NULL
LEFT JOIN conversations AS task_chat
  ON task_chat.company_id = run.company_id
 AND task_chat.id = run.conversation_id
 AND task_chat.kind = 'task'
WHERE attachments.id = sqlc.arg(attachment_id)::uuid
  AND attachments.company_id = sqlc.arg(company_id)::uuid
  AND attachments.status = 'ready'
  AND NOT EXISTS (
      SELECT 1 FROM asset_versions AS artifact_version
      JOIN output_items AS artifact_item
        ON artifact_item.asset_version_id = artifact_version.id
       AND artifact_item.company_id = artifact_version.company_id
      WHERE artifact_version.attachment_id = attachments.id
        AND artifact_version.company_id = attachments.company_id
        AND (SELECT true FROM output_access AS access
            WHERE access.company_id = artifact_item.company_id
              AND access.output_id = artifact_item.output_id
              AND access.user_id = sqlc.arg(authorizer_user_id)::uuid
            LIMIT 1) IS NULL
  )
  AND (
    SELECT true FROM project_presentations AS presentation
    JOIN project_presentation_sources AS source ON source.presentation_id=presentation.id
    WHERE presentation.company_id=sqlc.arg(company_id)::uuid
      AND presentation.export_attachment_id=sqlc.arg(attachment_id)::uuid
      AND source.output_id IS NOT NULL
      AND (SELECT true FROM output_access AS access
          WHERE access.company_id=source.company_id AND access.output_id=source.output_id
            AND access.user_id=sqlc.arg(authorizer_user_id)::uuid
          LIMIT 1) IS NULL
    LIMIT 1
) IS NULL
  AND (
      EXISTS (
          SELECT 1
          FROM project_attachment_scopes AS file
          JOIN project_access AS access
            ON access.company_id = file.company_id
           AND access.project_id = file.project_id
           AND access.user_id = sqlc.arg(authorizer_user_id)::uuid
          WHERE file.company_id = attachments.company_id
            AND file.attachment_id = attachments.id
            AND (file.removed_at IS NULL OR sqlc.arg(frozen_project_document)::boolean)
      )
      OR COALESCE((
          SELECT true FROM attachment_access AS access
          WHERE access.company_id = attachments.company_id
            AND access.attachment_id = attachments.id
            AND access.user_id = sqlc.arg(authorizer_user_id)::uuid
          LIMIT 1
      ), false)
      OR (
          EXISTS (
              SELECT 1 FROM company_reference_documents AS reference
              WHERE reference.company_id = attachments.company_id
                AND reference.attachment_id = attachments.id
          )
          AND NOT EXISTS (
              SELECT 1 FROM message_attachments AS link
              WHERE link.company_id = attachments.company_id
                AND link.attachment_id = attachments.id
          )
          AND NOT EXISTS (
              SELECT 1 FROM project_attachment_scopes AS file
              WHERE file.company_id = attachments.company_id
                AND file.attachment_id = attachments.id
          )
      )
  )
  -- A Task Run answers into its Task chat, which only the Task's Project
  -- readers see (ADR 0017): a file a Project holds opens to it only through
  -- the Task's own Project, so a file of another Project linked into the
  -- chat stays closed even when the authorizer reads that Project too.
  AND (
      task_chat.id IS NULL
      OR NOT EXISTS (
          SELECT 1 FROM project_attachment_scopes AS held
          WHERE held.company_id = attachments.company_id
            AND held.attachment_id = attachments.id
            AND (held.removed_at IS NULL OR sqlc.arg(frozen_project_document)::boolean)
      )
      OR EXISTS (
          SELECT 1 FROM project_attachment_scopes AS held
          WHERE held.company_id = attachments.company_id
            AND held.attachment_id = attachments.id
            AND held.project_id = task_chat.project_id
            AND (held.removed_at IS NULL OR sqlc.arg(frozen_project_document)::boolean)
      )
  );

-- name: ListProjectFileMessageLinks :many
-- The messages carrying any of these files, so a Project file change can tell
-- each chat to refetch that message's attachments (ADR 0017): whether a file
-- is a Project file decides who sees it in the chat.
SELECT link.message_id, item.conversation_id, conversation.kind AS conversation_kind,
       conversation.created_by_user_id AS conversation_created_by_user_id
FROM message_attachments AS link
JOIN conversation_items AS item
  ON item.company_id = link.company_id
 AND item.id = link.message_id
 AND item.deleted_at IS NULL
JOIN conversations AS conversation
  ON conversation.company_id = item.company_id
 AND conversation.id = item.conversation_id
WHERE link.company_id = sqlc.arg(company_id)::uuid
  AND link.attachment_id = ANY(sqlc.arg(attachment_ids)::uuid[])
ORDER BY link.message_id;

-- name: MarkAttachmentReady :one
UPDATE attachments
SET status = 'ready',
    upload_lease_expires_at = NULL,
    updated_at = sqlc.arg(now_at)::timestamptz,
    version = version + 1
WHERE id = sqlc.arg(attachment_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND uploaded_by_user_id = sqlc.arg(uploaded_by_user_id)::uuid
  AND object_key = sqlc.arg(object_key)::text
  AND size_bytes = sqlc.arg(size_bytes)::bigint
  AND status = 'pending'
  AND version = sqlc.arg(expected_version)::bigint
  AND upload_lease_expires_at > sqlc.arg(now_at)::timestamptz
RETURNING *;

-- name: MarkAttachmentDeleting :one
UPDATE attachments
SET status = 'deleting',
    updated_at = sqlc.arg(now_at)::timestamptz,
    version = version + 1
WHERE id = sqlc.arg(attachment_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND object_key = sqlc.arg(object_key)::text
  AND status IN ('pending', 'ready')
  AND version = sqlc.arg(expected_version)::bigint
  AND NOT EXISTS (
      SELECT 1
      FROM conversation_run_queue_attachments
      WHERE conversation_run_queue_attachments.attachment_id = attachments.id
        AND conversation_run_queue_attachments.released_at IS NULL
  )
  AND NOT EXISTS (SELECT 1 FROM board_files WHERE board_files.company_id=attachments.company_id AND (board_files.attachment_id=attachments.id OR board_files.preview_attachment_id=attachments.id))
  AND NOT EXISTS (SELECT 1 FROM project_presentations WHERE project_presentations.export_attachment_id=attachments.id)
  AND NOT EXISTS (SELECT 1 FROM project_files WHERE project_files.attachment_id=attachments.id)
  AND NOT EXISTS (SELECT 1 FROM companies WHERE companies.avatar_attachment_id = attachments.id)
  AND NOT EXISTS (
      SELECT 1
      FROM asset_versions
      WHERE asset_versions.attachment_id = attachments.id
  )
RETURNING *;

-- name: MarkAttachmentDeleted :one
UPDATE attachments
SET status = 'deleted',
    updated_at = sqlc.arg(now_at)::timestamptz,
    version = version + 1
WHERE id = sqlc.arg(attachment_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND object_key = sqlc.arg(object_key)::text
  AND status = 'deleting'
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: TransitionExpiredPendingAttachmentsForCleanup :many
WITH cleanup_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
), expired_pending AS MATERIALIZED (
    SELECT attachments.id
    FROM attachments
    CROSS JOIN cleanup_clock
    WHERE attachments.status = 'pending'
      AND attachments.version = 1
      AND attachments.upload_lease_expires_at <= cleanup_clock.now_at
      AND NOT EXISTS (SELECT 1 FROM board_files WHERE board_files.company_id=attachments.company_id AND (board_files.attachment_id=attachments.id OR board_files.preview_attachment_id=attachments.id))
      AND NOT EXISTS (
          SELECT 1
          FROM asset_versions
          WHERE asset_versions.attachment_id = attachments.id
      )
    ORDER BY attachments.upload_lease_expires_at, attachments.id
    FOR UPDATE OF attachments SKIP LOCKED
    LIMIT LEAST(GREATEST(sqlc.arg(batch_size)::integer, 0), 1000)
)
UPDATE attachments
SET status = 'deleting',
    updated_at = cleanup_clock.now_at,
    version = 2
FROM expired_pending
CROSS JOIN cleanup_clock
WHERE attachments.id = expired_pending.id
  AND attachments.status = 'pending'
  AND attachments.version = 1
  AND attachments.upload_lease_expires_at <= cleanup_clock.now_at
  AND NOT EXISTS (SELECT 1 FROM board_files WHERE board_files.company_id=attachments.company_id AND (board_files.attachment_id=attachments.id OR board_files.preview_attachment_id=attachments.id))
  AND NOT EXISTS (SELECT 1 FROM project_presentations WHERE project_presentations.export_attachment_id=attachments.id)
  AND NOT EXISTS (SELECT 1 FROM project_files WHERE project_files.attachment_id=attachments.id)
  AND NOT EXISTS (SELECT 1 FROM companies WHERE companies.avatar_attachment_id = attachments.id)
  AND NOT EXISTS (
      SELECT 1
      FROM asset_versions
      WHERE asset_versions.attachment_id = attachments.id
  )
RETURNING attachments.id, attachments.company_id;

-- name: ClaimDeletingAttachmentCleanupCandidates :many
WITH cleanup_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
), deleting_candidates AS MATERIALIZED (
    SELECT attachments.id
    FROM attachments
    WHERE attachments.status = 'deleting'
      AND NOT EXISTS (SELECT 1 FROM board_files WHERE board_files.company_id=attachments.company_id AND (board_files.attachment_id=attachments.id OR board_files.preview_attachment_id=attachments.id))
      AND NOT EXISTS (
          SELECT 1
          FROM asset_versions
          WHERE asset_versions.attachment_id = attachments.id
      )
    ORDER BY attachments.updated_at, attachments.id
    FOR UPDATE OF attachments SKIP LOCKED
    LIMIT LEAST(GREATEST(sqlc.arg(batch_size)::integer, 0), 1000)
)
UPDATE attachments
SET updated_at = GREATEST(attachments.updated_at, cleanup_clock.now_at)
FROM deleting_candidates
CROSS JOIN cleanup_clock
WHERE attachments.id = deleting_candidates.id
  AND attachments.status = 'deleting'
  AND NOT EXISTS (SELECT 1 FROM board_files WHERE board_files.company_id=attachments.company_id AND (board_files.attachment_id=attachments.id OR board_files.preview_attachment_id=attachments.id))
  AND NOT EXISTS (SELECT 1 FROM project_presentations WHERE project_presentations.export_attachment_id=attachments.id)
  AND NOT EXISTS (SELECT 1 FROM project_files WHERE project_files.attachment_id=attachments.id)
  AND NOT EXISTS (SELECT 1 FROM companies WHERE companies.avatar_attachment_id = attachments.id)
  AND NOT EXISTS (
      SELECT 1
      FROM asset_versions
      WHERE asset_versions.attachment_id = attachments.id
  )
RETURNING attachments.id, attachments.company_id;

-- This is statement one of the READ COMMITTED abandoned-ready transaction.
-- It only claims rows; the following statement repeats every predicate using
-- a fresh snapshot before making the lifecycle transition.
-- name: ClaimAbandonedReadyAttachmentCandidates :many
WITH cleanup_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
SELECT attachments.id
FROM attachments
CROSS JOIN cleanup_clock
WHERE attachments.status = 'ready'
  AND attachments.version = 2
  AND attachments.updated_at <= cleanup_clock.now_at
      - sqlc.arg(abandoned_grace_milliseconds)::bigint * interval '1 millisecond'
  AND NOT EXISTS (
      SELECT 1 FROM message_attachments
      WHERE message_attachments.attachment_id = attachments.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM conversation_run_queue_attachments
      WHERE conversation_run_queue_attachments.attachment_id = attachments.id
        AND conversation_run_queue_attachments.released_at IS NULL
  )
  AND NOT EXISTS (SELECT 1 FROM board_files WHERE board_files.company_id=attachments.company_id AND (board_files.attachment_id=attachments.id OR board_files.preview_attachment_id=attachments.id))
  AND NOT EXISTS (SELECT 1 FROM project_presentations WHERE project_presentations.export_attachment_id=attachments.id)
  AND NOT EXISTS (SELECT 1 FROM project_files WHERE project_files.attachment_id=attachments.id)
  AND NOT EXISTS (SELECT 1 FROM companies WHERE companies.avatar_attachment_id = attachments.id)
  AND NOT EXISTS (
      SELECT 1 FROM asset_versions
      WHERE asset_versions.attachment_id = attachments.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM company_reference_documents
      WHERE company_reference_documents.attachment_id = attachments.id
  )
ORDER BY attachments.updated_at, attachments.id
FOR UPDATE OF attachments SKIP LOCKED
LIMIT LEAST(GREATEST(sqlc.arg(batch_size)::integer, 0), 1000);

-- name: TransitionClaimedAbandonedReadyAttachments :many
WITH cleanup_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE attachments
SET status = 'deleting',
    updated_at = cleanup_clock.now_at,
    version = 3
FROM cleanup_clock
WHERE attachments.id = ANY(sqlc.arg(attachment_ids)::uuid[])
  AND attachments.status = 'ready'
  AND attachments.version = 2
  AND attachments.updated_at <= cleanup_clock.now_at
      - sqlc.arg(abandoned_grace_milliseconds)::bigint * interval '1 millisecond'
  AND NOT EXISTS (
      SELECT 1 FROM message_attachments
      WHERE message_attachments.attachment_id = attachments.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM conversation_run_queue_attachments
      WHERE conversation_run_queue_attachments.attachment_id = attachments.id
        AND conversation_run_queue_attachments.released_at IS NULL
  )
  AND NOT EXISTS (SELECT 1 FROM board_files WHERE board_files.company_id=attachments.company_id AND (board_files.attachment_id=attachments.id OR board_files.preview_attachment_id=attachments.id))
  AND NOT EXISTS (SELECT 1 FROM project_presentations WHERE project_presentations.export_attachment_id=attachments.id)
  AND NOT EXISTS (SELECT 1 FROM project_files WHERE project_files.attachment_id=attachments.id)
  AND NOT EXISTS (SELECT 1 FROM companies WHERE companies.avatar_attachment_id = attachments.id)
  AND NOT EXISTS (
      SELECT 1 FROM asset_versions
      WHERE asset_versions.attachment_id = attachments.id
  )
RETURNING attachments.id, attachments.company_id;

-- name: GetAttachmentRealtimeRoute :one
SELECT
    message_attachments.message_id,
    conversations.id AS conversation_id,
    conversations.kind AS conversation_kind,
    conversations.created_by_user_id AS conversation_creator_user_id,
    attachments.uploaded_by_user_id
FROM attachments
JOIN company_members
  ON company_members.company_id = attachments.company_id
 AND company_members.user_id = attachments.uploaded_by_user_id
LEFT JOIN message_attachments
  ON message_attachments.attachment_id = attachments.id
LEFT JOIN conversation_items
  ON conversation_items.id = message_attachments.message_id
LEFT JOIN conversations
  ON conversations.id = conversation_items.conversation_id
 AND conversations.company_id = attachments.company_id
WHERE attachments.id = sqlc.arg(attachment_id)::uuid
  AND attachments.company_id = sqlc.arg(company_id)::uuid;

-- name: ListAttachmentBriefs :many
-- The metadata the enqueue kernel freezes for the files a founder attached to
-- the words that started a Run. Ready attachments only: a pending or deleting
-- upload has no content an Agent could read.
SELECT id, filename, content_type, size_bytes
FROM attachments
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = ANY(sqlc.arg(attachment_ids)::uuid[])
  AND status = 'ready'
ORDER BY filename;

-- name: RegisterCompanyReferenceDocument :one
-- Idempotent: the founder re-registering the same upload restates one fact.
-- The insert is gated on a ready Attachment the same Company owns that the
-- registrant uploaded and that is nothing else yet: no chat message carries
-- it, no queued message holds it and no Project has held it. Every Run of the
-- Company receives the references, so registering someone else's file, a
-- chat file or a Project file would open it past its own rule (ADR 0017):
-- managing the Company is not reading its content.
INSERT INTO company_reference_documents (company_id, attachment_id)
SELECT attachments.company_id, attachments.id
FROM attachments
WHERE attachments.id = sqlc.arg(attachment_id)::uuid
  AND attachments.company_id = sqlc.arg(company_id)::uuid
  AND attachments.uploaded_by_user_id = sqlc.arg(actor_user_id)::uuid
  AND attachments.status = 'ready'
  AND NOT EXISTS (
      SELECT 1 FROM message_attachments AS link
      WHERE link.company_id = attachments.company_id
        AND link.attachment_id = attachments.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM conversation_run_queue_attachments AS queued
      WHERE queued.attachment_id = attachments.id
        AND queued.released_at IS NULL
  )
  AND NOT EXISTS (
      SELECT 1 FROM project_attachment_scopes AS file
      WHERE file.company_id = attachments.company_id
        AND file.attachment_id = attachments.id
  )
ON CONFLICT (attachment_id) DO UPDATE
    SET attachment_id = company_reference_documents.attachment_id
RETURNING *;

-- name: ListCompanyReferenceDocuments :many
-- The founder-supplied source material an Agent may read, oldest first so the
-- order the founder uploaded them survives into the Run. A reference that has
-- since become a chat file or a Project file follows that rule instead and is
-- left out, so no Run context names a file its reader may not open (ADR 0017).
SELECT attachments.id, attachments.filename, attachments.content_type,
    attachments.size_bytes
FROM company_reference_documents
JOIN attachments
  ON attachments.id = company_reference_documents.attachment_id
 AND attachments.company_id = company_reference_documents.company_id
WHERE company_reference_documents.company_id = sqlc.arg(company_id)::uuid
  AND attachments.status = 'ready'
  AND NOT EXISTS (
      SELECT 1 FROM message_attachments AS link
      WHERE link.company_id = attachments.company_id
        AND link.attachment_id = attachments.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM project_attachment_scopes AS file
      WHERE file.company_id = attachments.company_id
        AND file.attachment_id = attachments.id
  )
ORDER BY company_reference_documents.created_at, company_reference_documents.attachment_id;
