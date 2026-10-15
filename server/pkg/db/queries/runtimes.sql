-- Runtimes: one provider instance registered by one daemon in one company
--.

-- Registration is an upsert on the (company, daemon, provider, owner)
-- identity: a runtime is one person's use of one machine (D33), so a second
-- person signing in on the same computer inserts rows of their own and never
-- moves anyone else's. The unusable-reason columns are replaced wholesale
-- like metadata: a registration without a reason clears it. custom_name is
-- deliberately absent from the DO UPDATE list: the user's machine name
-- survives every re-registration. removed_at is reset: the same machine
-- announcing the same identity again is the user restarting it on purpose,
-- so a removed row comes back as a live one. handed_over_at is reset too:
-- the person is back on the machine (HandOverMachineRuntimes set it).
-- The sign-in columns follow the announcement: a registration that carries
-- no verdict (an older daemon, an unusable provider) keeps the last one.
-- name: UpsertRuntime :one
INSERT INTO runtimes (
    company_id, daemon_id, name, provider,
    status, device_info, device_name, metadata, last_seen_at, owner_user_id,
    unusable_reason_code, unusable_reason_message,
    provider_sign_in_state, provider_sign_in_detail, provider_sign_in_checked_at
) VALUES (
    $1, $2, $3, $4,
    $5, $6, sqlc.narg(device_name)::text, $7, now(), $8,
    sqlc.narg(unusable_reason_code)::text, sqlc.narg(unusable_reason_message)::text,
    sqlc.narg(provider_sign_in_state)::text, sqlc.narg(provider_sign_in_detail)::text,
    sqlc.narg(provider_sign_in_checked_at)::timestamptz
)
ON CONFLICT (company_id, daemon_id, provider, owner_user_id) DO UPDATE SET
    name = EXCLUDED.name,
    status = EXCLUDED.status,
    device_info = EXCLUDED.device_info,
    device_name = EXCLUDED.device_name,
    metadata = EXCLUDED.metadata,
    last_seen_at = now(),
    unusable_reason_code = EXCLUDED.unusable_reason_code,
    unusable_reason_message = EXCLUDED.unusable_reason_message,
    provider_sign_in_state = COALESCE(EXCLUDED.provider_sign_in_state, runtimes.provider_sign_in_state),
    provider_sign_in_detail = CASE
        WHEN EXCLUDED.provider_sign_in_state IS NULL THEN runtimes.provider_sign_in_detail
        ELSE EXCLUDED.provider_sign_in_detail
    END,
    provider_sign_in_checked_at = COALESCE(EXCLUDED.provider_sign_in_checked_at, runtimes.provider_sign_in_checked_at),
    removed_at = NULL,
    handed_over_at = NULL,
    updated_at = now()
RETURNING *;

-- The people with live rows on one machine in one company, read before any
-- lock so the registering service can take their Company then membership
-- locks first (the canonical ladder: company/member, then runtime, then
-- Inbox) before HandOverMachineRuntimes locks their rows.
-- name: ListMachineRuntimeOwners :many
SELECT DISTINCT owner_user_id::uuid AS owner_user_id
FROM runtimes
WHERE company_id = sqlc.arg(company_id)::uuid
  AND daemon_id = sqlc.arg(daemon_id)::text
  AND owner_user_id IS NOT NULL
  AND owner_user_id <> sqlc.arg(registrant_user_id)::uuid
  AND handed_over_at IS NULL
  AND removed_at IS NULL
ORDER BY owner_user_id;

-- One person at a time (D33): a registration takes every other person's live
-- rows on the same machine offline and marks them handed over, in the
-- registration's own transaction. Their bindings, queued Runs, names and
-- discovery stay on their rows for when they sign back in. A row already
-- handed over is not returned again, so its owner is told once.
-- name: HandOverMachineRuntimes :many
UPDATE runtimes
SET status = 'offline', handed_over_at = now(), updated_at = now()
WHERE company_id = sqlc.arg(company_id)::uuid
  AND daemon_id = sqlc.arg(daemon_id)::text
  AND owner_user_id IS NOT NULL
  AND owner_user_id <> sqlc.arg(registrant_user_id)::uuid
  AND handed_over_at IS NULL
  AND removed_at IS NULL
RETURNING *;

-- The registrant's own rows on the machine that someone else took over since
-- they last registered: the registration is their return, and closes the
-- notice the handover raised.
-- name: ListHandedOverRuntimesForOwner :many
SELECT id FROM runtimes
WHERE company_id = sqlc.arg(company_id)::uuid
  AND daemon_id = sqlc.arg(daemon_id)::text
  AND owner_user_id = sqlc.arg(owner_user_id)::uuid
  AND handed_over_at IS NOT NULL
ORDER BY id;

-- The by-id reads hide removed rows: every caller — the daemon-plane access
-- gate, the heartbeat, deregister, a new agent binding — then treats a
-- removed runtime exactly like one that never existed. GetRuntimes is the
-- claim path's authorization input, so a removed runtime is never
-- dispatched to.
-- name: GetRuntime :one
SELECT * FROM runtimes WHERE id = $1 AND removed_at IS NULL;

-- name: GetRuntimes :many
SELECT * FROM runtimes WHERE id = ANY(@ids::uuid[]) AND removed_at IS NULL;

-- The one by-id read that keeps a removed row visible: a Run keeps its
-- runtime through a removal, and the terminal Inbox item and the metrics
-- observation name its provider. Never the read for a runtime a member may
-- bind TO — that is GetRuntime.
-- name: GetRunRuntime :one
SELECT * FROM runtimes
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(id)::uuid;

-- The user-plane detail read: the company fence is part of the predicate so
-- another company's id and an unknown id are the same no-row answer, and so
-- is another member's runtime (ADR 0014): a member reads their own machines
-- and nobody else's — whatever their Company role — so a known id is not an
-- oracle.
-- name: GetRuntimeForCompany :one
SELECT
    sqlc.embed(runtimes),
    runtime_owners.id AS owner_id,
    runtime_owners.display_name AS owner_display_name,
    runtime_owners.avatar_url AS owner_avatar_url
FROM runtimes
LEFT JOIN users AS runtime_owners ON runtime_owners.id = runtimes.owner_user_id
WHERE runtimes.company_id = sqlc.arg(company_id)::uuid
  AND runtimes.id = sqlc.arg(id)::uuid
  AND runtimes.removed_at IS NULL
  AND runtimes.owner_user_id = sqlc.arg(viewer_user_id)::uuid;

-- The rename gate reads the row it is about to write under lock, fenced to
-- the company like the detail read, so the owner it judges is the owner the
-- write sees. A removed row is not there to rename.
-- name: GetRuntimeForUpdate :one
SELECT * FROM runtimes
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(id)::uuid
  AND removed_at IS NULL
FOR UPDATE;

-- Removal locks the runtime being removed and, when the plan names one, the
-- target, in id order in one statement. Removed rows are excluded, so a
-- source already removed and a target already removed are both the no-row
-- answer the caller reads as not found.
-- name: LockRuntimesForRemoval :many
SELECT * FROM runtimes
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = ANY(sqlc.arg(ids)::uuid[])
  AND removed_at IS NULL
ORDER BY id
FOR UPDATE;

-- The soft removal. The status predicate is the precondition restated at
-- the write: a runtime that came online between the lock and this statement
-- (it cannot — the row is locked — but the predicate keeps the invariant
-- local) is not removed, and the caller reads no row as blocked.
-- name: MarkRuntimeRemoved :one
UPDATE runtimes
SET removed_at = now(), updated_at = now()
WHERE id = sqlc.arg(id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND removed_at IS NULL
  AND status = 'offline'
RETURNING *;

-- The two rename writes. custom_name is the user's machine name and NULL
-- clears it; neither statement touches a column registration owns.
-- name: SetRuntimeCustomName :one
UPDATE runtimes
SET custom_name = sqlc.narg(custom_name)::text, updated_at = now()
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(id)::uuid
RETURNING *;

-- applyToMachine: every LIVE runtime the same person registered from the
-- same daemon in the company, in one statement so the siblings flip
-- together. Another person's rows on the same computer are theirs (D33) and
-- keep their own name. Removed rows are excluded like everywhere else:
-- renaming one would store a name no read serves, and its id would ride
-- along in the statement's returned rows and from there into
-- daemon.roster_changed{action:"update"}.
-- name: SetRuntimeCustomNameForDaemon :many
UPDATE runtimes
SET custom_name = sqlc.narg(custom_name)::text, updated_at = now()
WHERE company_id = sqlc.arg(company_id)::uuid
  AND daemon_id = sqlc.arg(daemon_id)::text
  AND owner_user_id = sqlc.arg(owner_user_id)::uuid
  AND removed_at IS NULL
RETURNING *;

-- The roster is filtered for its viewer here, not in Go (ADR 0014), so a
-- hidden row never reaches a count, a picker, a search or an event: a member
-- lists their own machines and nobody else's — whatever their Company role.
-- name: ListRuntimesForCompany :many
SELECT
    sqlc.embed(runtimes),
    runtime_owners.id AS owner_id,
    runtime_owners.display_name AS owner_display_name,
    runtime_owners.avatar_url AS owner_avatar_url
FROM runtimes
LEFT JOIN users AS runtime_owners ON runtime_owners.id = runtimes.owner_user_id
WHERE runtimes.company_id = sqlc.arg(company_id)::uuid
  AND runtimes.removed_at IS NULL
  AND runtimes.owner_user_id = sqlc.arg(viewer_user_id)::uuid
  AND (
      sqlc.narg(status)::text IS NULL
      OR runtimes.status = sqlc.narg(status)::text
  )
  AND (
      sqlc.narg(provider)::text IS NULL
      OR runtimes.provider = sqlc.narg(provider)::text
  )
ORDER BY runtimes.created_at ASC, runtimes.id ASC;


-- A heartbeat refreshes liveness; it brings the runtime online only when no
-- unusable reason is stored (the CHECK would refuse the flip anyway), so an
-- unusable runtime stays offline-but-fresh while its machine is on.
--
-- previous_status is what makes the beat a decision and not just a write: the
-- CTE locks the row and reports the status it held immediately before this
-- statement, so the caller learns "this write is what brought the machine
-- back" from the write itself instead of from an earlier unlocked read that
-- a committing stale sweep can invalidate. FOR UPDATE is what makes the
-- report true — under READ COMMITTED a plain CTE read would answer from the
-- statement's own snapshot, taken before a concurrent sweep committed, while
-- the locking read waits for that sweep and re-reads the row it left behind.
-- The lock is the one this UPDATE takes anyway, so a beat still costs one
-- statement.
--
-- The CTE's removed_at guard is the race fence with removal, and the UPDATE
-- reaches only rows it returned: a heartbeat that read the row before a
-- removal committed touches nothing and cannot resurrect it online. No row
-- means "gone".
-- name: TouchRuntimeHeartbeat :one
WITH previous AS (
    SELECT before.id, before.status
    FROM runtimes AS before
    WHERE before.id = $1
      AND before.removed_at IS NULL
    FOR UPDATE
)
UPDATE runtimes
SET last_seen_at = now(),
    status = CASE
        WHEN runtimes.unusable_reason_code IS NULL THEN 'online'
        ELSE runtimes.status
    END,
    updated_at = now()
FROM previous
WHERE runtimes.id = previous.id
RETURNING runtimes.*, previous.status AS previous_status;

-- name: SetRuntimeOffline :one
UPDATE runtimes
SET status = 'offline', updated_at = now()
WHERE id = $1
RETURNING *;

-- Sweeper: candidates first, so Go can count each one's in-flight runs and
-- stage the offline notices while the rows are still online, then the UPDATE
-- re-checks the stale predicate so a heartbeat landing in between cannot
-- demote a just-refreshed runtime. There is no Redis liveness lane; this
-- table is the only liveness authority.
-- name: SelectStaleOnlineRuntimes :many
SELECT id, company_id, owner_user_id, daemon_id, provider FROM runtimes
WHERE status = 'online'
  AND (last_seen_at IS NULL OR last_seen_at < now() - make_interval(secs => @stale_secs::double precision));

-- name: MarkRuntimesOfflineByIDs :many
UPDATE runtimes
SET status = 'offline', updated_at = now()
WHERE status = 'online'
  AND id = ANY(@ids::uuid[])
  AND (last_seen_at IS NULL OR last_seen_at < now() - make_interval(secs => @stale_secs::double precision))
RETURNING id, company_id;

-- Unchanged by removal on purpose: a removed row with run history is kept
-- by the NOT EXISTS runs guard forever, and a removed row nothing references
-- is garbage after the TTL like any other offline runtime. A row a member
-- still has an Agent bound to is theirs to keep: their own offline laptop is
-- never pruned out from under their setup. Message selections also retain their
-- frozen placement, including queued messages that have not created a Run.
-- name: DeleteStaleOfflineRuntimes :many
DELETE FROM runtimes
WHERE status = 'offline'
  AND (last_seen_at IS NULL OR last_seen_at < now() - make_interval(secs => @ttl_secs::double precision))
  AND NOT EXISTS (
      SELECT 1 FROM agent_runtime_bindings AS binding
      WHERE binding.runtime_id = runtimes.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM runs
      WHERE runs.runtime_id = runtimes.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM message_connector_bindings AS selection
      WHERE selection.runtime_id = runtimes.id
  )
RETURNING id, company_id;

-- name: UpdateRuntimeTools :exec
UPDATE runtimes
SET metadata = jsonb_set(metadata, '{tools}', sqlc.arg(tools)::jsonb), updated_at = clock_timestamp()
WHERE company_id = sqlc.arg(company_id)::uuid AND id = sqlc.arg(runtime_id)::uuid;
