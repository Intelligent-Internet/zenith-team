-- Account deletion uses the same User -> auth-session lock order as refresh,
-- logout, and daemon registration. NO KEY UPDATE still serializes every
-- soft-delete/auth mutation of this User while remaining compatible with the
-- KEY SHARE lock a Company-first producer takes when it inserts an Inbox row
-- through recipient_user_id's foreign key. The authenticated session is
-- revalidated inside the transaction that makes the User unavailable.
-- name: LockAccountDeletionAuthority :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
), locked_user AS MATERIALIZED (
    SELECT users.*
    FROM users
    WHERE users.id = sqlc.arg(user_id)::uuid
      AND users.disabled_at IS NULL
      AND users.deleted_at IS NULL
    FOR NO KEY UPDATE OF users
)
SELECT locked_user.*
FROM locked_user
JOIN auth_sessions
  ON auth_sessions.id = sqlc.arg(auth_session_id)::uuid
 AND auth_sessions.user_id = locked_user.id
CROSS JOIN statement_clock
WHERE auth_sessions.revoked_at IS NULL
  AND auth_sessions.expires_at > statement_clock.now_at
FOR UPDATE OF auth_sessions;

-- Account deletion must fence both current memberships and any stale
-- product/runtime footprint retained after a membership change. A Run belongs
-- to the account when its own user_message names the user, when it lives in the
-- user's private direct Conversation, or when an Approval/Question receipt says
-- the user's decision started that human-input continuation. Retries carry the
-- claim transitively through parent_run_id.
-- name: LockAccountCompaniesForDeletion :many
WITH RECURSIVE owned_runtimes AS MATERIALIZED (
    SELECT runtimes.company_id, runtimes.daemon_id
    FROM runtimes
    WHERE runtimes.owner_user_id = sqlc.arg(user_id)::uuid
), authored_run_lineages AS (
    SELECT runs.company_id, runs.id, runs.status
    FROM runs
    WHERE (
        EXISTS (
            SELECT 1
            FROM conversation_items
            WHERE conversation_items.run_id = runs.id
              AND conversation_items.company_id = runs.company_id
              AND conversation_items.type = 'user_message'
              AND conversation_items.author_id = sqlc.arg(user_id)::uuid
        )
        OR EXISTS (
            SELECT 1
            FROM conversations
            WHERE conversations.id = runs.conversation_id
              AND conversations.company_id = runs.company_id
              AND conversations.kind = 'direct'
              AND conversations.created_by_user_id = sqlc.arg(user_id)::uuid
        )
        OR EXISTS (
            SELECT 1
            FROM approvals
            WHERE approvals.company_id = runs.company_id
              AND approvals.continuation_run_id = runs.id
              AND approvals.decided_by_user_id = sqlc.arg(user_id)::uuid
        )
        OR EXISTS (
            SELECT 1
            FROM questions
            WHERE questions.company_id = runs.company_id
              AND questions.continuation_run_id = runs.id
              AND questions.resolved_by_user_id = sqlc.arg(user_id)::uuid
        )
    )
    UNION
    SELECT descendants.company_id, descendants.id, descendants.status
    FROM runs AS descendants
    JOIN authored_run_lineages AS ancestors
      ON ancestors.company_id = descendants.company_id
     AND ancestors.id = descendants.parent_run_id
), affected_company_ids AS MATERIALIZED (
    SELECT company_members.company_id
    FROM company_members
    WHERE company_members.user_id = sqlc.arg(user_id)::uuid
      AND company_members.status = 'active'
    UNION
    SELECT owned_runtimes.company_id
    FROM owned_runtimes
    UNION
    SELECT company_resources.company_id
    FROM company_resources
    JOIN owned_runtimes
      ON owned_runtimes.daemon_id = company_resources.daemon_id
    UNION
    SELECT conversation_run_queue_items.company_id
    FROM conversation_run_queue_items
    WHERE conversation_run_queue_items.initiated_by_user_id = sqlc.arg(user_id)::uuid
      AND conversation_run_queue_items.state = 'queued'
    UNION
    SELECT approvals.company_id
    FROM approvals
    WHERE (
          approvals.status = 'pending'
          OR approvals.continuation_status = 'pending'
      )
      AND (
          approvals.origin_user_id = sqlc.arg(user_id)::uuid
          OR approvals.decided_by_user_id = sqlc.arg(user_id)::uuid
      )
    UNION
    SELECT questions.company_id
    FROM questions
    WHERE (
          questions.status = 'pending'
          OR questions.continuation_status = 'pending'
      )
      AND (
          questions.origin_user_id = sqlc.arg(user_id)::uuid
          OR questions.resolved_by_user_id = sqlc.arg(user_id)::uuid
      )
    UNION
    SELECT authored_run_lineages.company_id
    FROM authored_run_lineages
    WHERE authored_run_lineages.status IN (
        'queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred'
    )
)
SELECT companies.id AS company_id
FROM companies
JOIN affected_company_ids ON affected_company_ids.company_id = companies.id
WHERE companies.deleting_at IS NULL
ORDER BY companies.id
FOR UPDATE OF companies;

-- The Company rows returned above are already locked in id order. Revoking
-- their active memberships now makes every mutation that waits behind those
-- locks recheck a revoked row instead of relying on the pre-deletion principal
-- it entered with.
-- name: RevokeActiveMembershipsForAccountDeletion :many
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
), revoked AS (
    UPDATE company_members
    SET status = 'revoked',
        revoked_at = statement_clock.now_at,
        revoked_by_user_id = sqlc.arg(user_id)::uuid,
        updated_at = statement_clock.now_at,
        version = company_members.version + 1
    FROM statement_clock
    WHERE company_members.company_id = ANY(sqlc.arg(company_ids)::uuid[])
      AND company_members.user_id = sqlc.arg(user_id)::uuid
      AND company_members.status = 'active'
    RETURNING company_members.*
)
SELECT *
FROM revoked
ORDER BY company_id, user_id;

-- Stop both forms of post-deletion human-input work. An unresolved request in
-- the user's private direct Conversation becomes terminal before ExpireDue can
-- open a continuation, while an already-terminal pending receipt is abandoned
-- before the lifecycle reconciler can materialize its Run. If the reconciler
-- already holds the receipt lock, this statement waits; after it commits a
-- delivered receipt the caller's later Run-candidate read sees and cancels the
-- Run under a fresh READ COMMITTED snapshot.
-- name: AbandonPendingContinuationsForAccountDeletion :many
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
), abandoned_approvals AS (
    UPDATE approvals
    SET status = CASE WHEN approvals.status = 'pending' THEN 'cancelled'
                      ELSE approvals.status END,
        continuation_prompt = COALESCE(
            approvals.continuation_prompt,
            'The account was deleted before this request could continue.'
        ),
        continuation_status = 'abandoned',
        continuation_next_attempt_at = NULL,
        continuation_abandon_reason = sqlc.arg(reason)::text,
        continuation_attempts = approvals.continuation_attempts + 1,
        updated_at = statement_clock.now_at,
        version = approvals.version + 1
    FROM statement_clock
    WHERE approvals.company_id = sqlc.arg(company_id)::uuid
      AND approvals.continuation_status IS DISTINCT FROM 'delivered'
      AND (
          approvals.status = 'pending'
          OR approvals.continuation_status = 'pending'
      )
      AND (
          approvals.origin_user_id = sqlc.arg(user_id)::uuid
          OR approvals.decided_by_user_id = sqlc.arg(user_id)::uuid
      )
    RETURNING
        'approval'::text AS entity_type,
        approvals.id AS entity_id,
        approvals.task_id AS task_id,
        approvals.origin_user_id AS origin_user_id
), abandoned_questions AS (
    UPDATE questions
    SET status = CASE WHEN questions.status = 'pending' THEN 'withdrawn'
                      ELSE questions.status END,
        continuation_prompt = COALESCE(
            questions.continuation_prompt,
            'The account was deleted before this request could continue.'
        ),
        continuation_status = 'abandoned',
        continuation_next_attempt_at = NULL,
        continuation_abandon_reason = sqlc.arg(reason)::text,
        continuation_attempts = questions.continuation_attempts + 1,
        updated_at = statement_clock.now_at,
        version = questions.version + 1
    FROM statement_clock
    WHERE questions.company_id = sqlc.arg(company_id)::uuid
      AND questions.continuation_status IS DISTINCT FROM 'delivered'
      AND (
          questions.status = 'pending'
          OR questions.continuation_status = 'pending'
      )
      AND (
          questions.origin_user_id = sqlc.arg(user_id)::uuid
          OR questions.resolved_by_user_id = sqlc.arg(user_id)::uuid
      )
    RETURNING
        'question'::text AS entity_type,
        questions.id AS entity_id,
        questions.task_id AS task_id,
        questions.origin_user_id AS origin_user_id
)
SELECT entity_type, entity_id, task_id, origin_user_id FROM abandoned_approvals
UNION ALL
SELECT entity_type, entity_id, task_id, origin_user_id FROM abandoned_questions
ORDER BY entity_type, entity_id;

-- name: MarkAccountDeleted :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
)
UPDATE users
SET deleted_at = statement_clock.now_at,
    disabled_at = statement_clock.now_at,
    updated_at = statement_clock.now_at,
    version = users.version + 1
FROM statement_clock
WHERE users.id = sqlc.arg(user_id)::uuid
  AND users.disabled_at IS NULL
  AND users.deleted_at IS NULL
  AND users.version = sqlc.arg(expected_version)::bigint
RETURNING users.*;

-- name: RevokeAllLiveAuthSessionsForAccountDeletion :many
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
)
UPDATE auth_sessions
SET revoked_at = statement_clock.now_at,
    revocation_reason = 'user_disabled',
    revoked_by_user_id = sqlc.arg(user_id)::uuid,
    updated_at = statement_clock.now_at
FROM statement_clock
WHERE auth_sessions.user_id = sqlc.arg(user_id)::uuid
  AND auth_sessions.revoked_at IS NULL
  AND auth_sessions.expires_at > statement_clock.now_at
RETURNING auth_sessions.id, auth_sessions.user_id, auth_sessions.revoked_at,
          auth_sessions.revocation_reason;
