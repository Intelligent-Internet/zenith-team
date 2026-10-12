-- name: GetScheduleClock :one
SELECT clock_timestamp()::timestamptz AS now_at;

-- A Schedule runs on its creator's binding of the Agent (ADR 0014): the
-- Agent is usable for the creator only while they have set it up on one of
-- their own live runtimes AND they are still an enabled, active member.
-- The provider is the template's. Keep these predicates identical to
-- LockActiveScheduleAgent, and ScheduleAgentNeedsCreatorBinding their
-- binding-only complement: a condition added here but not there gives a
-- creator the wrong refusal.
-- name: GetActiveScheduleAgent :one
SELECT sqlc.embed(agents), agents.provider AS provider
FROM agents
JOIN agent_runtime_bindings AS binding
  ON binding.company_id = agents.company_id
 AND binding.agent_id = agents.id
 AND binding.user_id = sqlc.arg(creator_user_id)::uuid
JOIN users AS binder
  ON binder.id = binding.user_id
 AND binder.disabled_at IS NULL
JOIN company_members AS binder_membership
  ON binder_membership.company_id = binding.company_id
 AND binder_membership.user_id = binding.user_id
 AND binder_membership.status = 'active'
JOIN runtimes
  ON runtimes.id = binding.runtime_id
 AND runtimes.company_id = binding.company_id
 AND runtimes.removed_at IS NULL
 AND runtimes.owner_user_id = binding.user_id
WHERE agents.id = sqlc.arg(agent_id)::uuid
  AND agents.company_id = sqlc.arg(company_id)::uuid
  AND agents.archived_at IS NULL;

-- GetActiveScheduleAgent with the Agent row share-locked; the same
-- predicates, mirrored by ScheduleAgentNeedsCreatorBinding.
-- name: LockActiveScheduleAgent :one
SELECT sqlc.embed(agents), agents.provider AS provider
FROM agents
JOIN agent_runtime_bindings AS binding
  ON binding.company_id = agents.company_id
 AND binding.agent_id = agents.id
 AND binding.user_id = sqlc.arg(creator_user_id)::uuid
JOIN users AS binder
  ON binder.id = binding.user_id
 AND binder.disabled_at IS NULL
JOIN company_members AS binder_membership
  ON binder_membership.company_id = binding.company_id
 AND binder_membership.user_id = binding.user_id
 AND binder_membership.status = 'active'
JOIN runtimes
  ON runtimes.id = binding.runtime_id
 AND runtimes.company_id = binding.company_id
 AND runtimes.removed_at IS NULL
 AND runtimes.owner_user_id = binding.user_id
WHERE agents.id = sqlc.arg(agent_id)::uuid
  AND agents.company_id = sqlc.arg(company_id)::uuid
  AND agents.archived_at IS NULL
FOR SHARE OF agents;

-- Why GetActiveScheduleAgent found nothing, when it is the one reason the
-- creator can fix: the Agent is active and they are an enabled, active
-- member, but they have not set it up on one of their own live runtimes.
-- Every predicate of GetActiveScheduleAgent and LockActiveScheduleAgent
-- appears here: the Agent and creator ones as-is, the binding and its
-- runtime negated.
-- name: ScheduleAgentNeedsCreatorBinding :one
SELECT EXISTS (
    SELECT 1
    FROM agents
    JOIN users AS creator
      ON creator.id = sqlc.arg(creator_user_id)::uuid
     AND creator.disabled_at IS NULL
    JOIN company_members AS creator_membership
      ON creator_membership.company_id = agents.company_id
     AND creator_membership.user_id = creator.id
     AND creator_membership.status = 'active'
    WHERE agents.id = sqlc.arg(agent_id)::uuid
      AND agents.company_id = sqlc.arg(company_id)::uuid
      AND agents.archived_at IS NULL
      AND NOT EXISTS (
          SELECT 1
          FROM agent_runtime_bindings AS binding
          JOIN runtimes
            ON runtimes.id = binding.runtime_id
           AND runtimes.company_id = binding.company_id
           AND runtimes.removed_at IS NULL
           AND runtimes.owner_user_id = binding.user_id
          WHERE binding.company_id = agents.company_id
            AND binding.agent_id = agents.id
            AND binding.user_id = creator.id
      )
)::boolean AS needs_binding;

-- name: ResolveScheduleSources :many
WITH requested AS MATERIALIZED (
    SELECT connector_id, (ordinality - 1)::integer AS position
    FROM unnest(sqlc.arg(connector_ids)::uuid[]) WITH ORDINALITY
        AS input(connector_id, ordinality)
)
SELECT
    connector_catalog_items.id AS connector_id,
    connector_catalog_items.company_id,
    connector_catalog_items.provider,
    connector_catalog_items.source,
    connector_catalog_items.native_id,
    connector_policies.reviewed_contract_digest,
    connector_policies.version AS policy_revision,
    requested.position
FROM requested
JOIN connector_catalog_items
  ON connector_catalog_items.id = requested.connector_id
 AND connector_catalog_items.company_id = sqlc.arg(company_id)::uuid
 AND connector_catalog_items.provider = sqlc.arg(provider)::text
JOIN connector_policies
  ON connector_policies.connector_id = connector_catalog_items.id
 AND connector_policies.company_id = connector_catalog_items.company_id
 AND connector_policies.desired_enabled
 AND NOT connector_policies.review_required
 AND connector_policies.reviewed_contract_digest IS NOT NULL
 AND connector_policies.reviewed_contract_digest = connector_catalog_items.contract_digest
WHERE connector_catalog_items.status = 'connected'
  AND connector_catalog_items.can_execute
ORDER BY requested.position
FOR SHARE OF connector_catalog_items, connector_policies;

-- name: InsertSchedule :one
WITH request_lock AS MATERIALIZED (
    SELECT pg_advisory_xact_lock(hashtextextended(
        'boardly/schedule-create/v1/' ||
        sqlc.arg(company_id)::uuid::text || '/' ||
        sqlc.arg(creator_user_id)::uuid::text || '/' ||
        sqlc.arg(request_key)::uuid::text,
        0
    ))
), identities AS MATERIALIZED (
    SELECT gen_random_uuid()::uuid AS schedule_id,
           gen_random_uuid()::uuid AS conversation_id
), authorized AS MATERIALIZED (
    SELECT
        companies.id AS company_id,
        company_members.user_id AS creator_user_id,
        agents.id AS agent_id
    FROM companies
    JOIN company_members
      ON company_members.company_id = companies.id
     AND company_members.user_id = sqlc.arg(creator_user_id)::uuid
     AND company_members.status = 'active'
     AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
    JOIN agents
      ON agents.company_id = companies.id
     AND agents.id = sqlc.arg(agent_id)::uuid
     AND agents.archived_at IS NULL
     AND agents.provider = sqlc.arg(reviewed_provider)::text
    -- The creator's own binding of the Agent (ADR 0014).
    JOIN agent_runtime_bindings AS binding
      ON binding.company_id = agents.company_id
     AND binding.agent_id = agents.id
     AND binding.user_id = company_members.user_id
    JOIN runtimes
      ON runtimes.company_id = binding.company_id
     AND runtimes.id = binding.runtime_id
     AND runtimes.removed_at IS NULL
     AND runtimes.owner_user_id = binding.user_id
    CROSS JOIN request_lock
    WHERE companies.id = sqlc.arg(company_id)::uuid
      AND companies.deleting_at IS NULL
      AND NOT EXISTS (
          SELECT 1
          FROM schedules
          WHERE schedules.company_id = companies.id
            AND schedules.creator_user_id = company_members.user_id
            AND schedules.request_key = sqlc.arg(request_key)::uuid
      )
), inserted_conversation AS (
    INSERT INTO conversations (
        id, company_id, kind, schedule_id, agent_id, created_by_user_id,
        title
    )
    SELECT
        identities.conversation_id,
        authorized.company_id,
        'schedule',
        identities.schedule_id,
        authorized.agent_id,
        authorized.creator_user_id,
        sqlc.arg(name)::text
    FROM authorized
    CROSS JOIN identities
    RETURNING conversations.*
), inserted_schedule AS (
    INSERT INTO schedules (
        id, company_id, conversation_id, creator_user_id, request_key, agent_id,
        reviewed_provider, name, instructions, preset_key, trigger_type, run_at,
        cron_expression, timezone, next_occurrence_at, status
    )
    SELECT
        identities.schedule_id,
        authorized.company_id,
        identities.conversation_id,
        authorized.creator_user_id,
        sqlc.arg(request_key)::uuid,
        authorized.agent_id,
        sqlc.arg(reviewed_provider)::text,
        sqlc.arg(name)::text,
        sqlc.arg(instructions)::text,
        sqlc.narg(preset_key)::text,
        sqlc.arg(trigger_type)::text,
        sqlc.narg(run_at)::timestamptz,
        sqlc.narg(cron_expression)::text,
        sqlc.narg(timezone)::text,
        sqlc.narg(next_occurrence_at)::timestamptz,
        sqlc.arg(status)::text
    FROM authorized
    CROSS JOIN identities
    JOIN inserted_conversation
      ON inserted_conversation.id = identities.conversation_id
    RETURNING schedules.*
)
SELECT inserted_schedule.*
FROM inserted_schedule;

-- name: GetScheduleCreateReplay :one
SELECT schedules.*
FROM schedules
JOIN company_members
  ON company_members.company_id = schedules.company_id
 AND company_members.user_id = sqlc.arg(creator_user_id)::uuid
 AND company_members.status = 'active'
 AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE schedules.company_id = sqlc.arg(company_id)::uuid
  AND schedules.creator_user_id = sqlc.arg(creator_user_id)::uuid
  AND schedules.request_key = sqlc.arg(request_key)::uuid;
-- name: InsertScheduleSources :execrows
WITH input_sources AS MATERIALIZED (
    SELECT
        input."connectorId"::uuid AS connector_id,
        input."companyId"::uuid AS company_id,
        input.provider::text,
        input.source::text,
        input."nativeId"::text AS native_id,
        input."reviewedContractDigest"::text AS reviewed_contract_digest,
        input."policyRevision"::bigint AS policy_revision,
        input.position::integer
    FROM jsonb_to_recordset(sqlc.arg(sources)::jsonb) AS input(
        "connectorId" uuid,
        "companyId" uuid,
        provider text,
        source text,
        "nativeId" text,
        "reviewedContractDigest" text,
        "policyRevision" bigint,
        position integer
    )
)
INSERT INTO schedule_sources (
    schedule_id, connector_id, company_id, provider, source, native_id,
    reviewed_contract_digest, policy_revision, position
)
SELECT
    sqlc.arg(schedule_id)::uuid,
    input_sources.connector_id,
    input_sources.company_id,
    input_sources.provider,
    input_sources.source,
    input_sources.native_id,
    input_sources.reviewed_contract_digest,
    input_sources.policy_revision,
    input_sources.position
FROM input_sources
ORDER BY input_sources.position;

-- name: DeleteScheduleSources :execrows
DELETE FROM schedule_sources
WHERE schedule_id = sqlc.arg(schedule_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid;

-- name: ListScheduleSources :many
SELECT schedule_sources.*
FROM schedule_sources
WHERE schedule_id = sqlc.arg(schedule_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
ORDER BY position;

-- name: GetScheduleForCreator :one
SELECT schedules.*
FROM schedules
JOIN companies
  ON companies.id = schedules.company_id
 AND companies.deleting_at IS NULL
JOIN users
  ON users.id = schedules.creator_user_id
 AND users.disabled_at IS NULL
JOIN company_members AS actor_membership
  ON actor_membership.company_id = schedules.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
 AND actor_membership.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE schedules.id = sqlc.arg(schedule_id)::uuid
  AND schedules.company_id = sqlc.arg(company_id)::uuid
  AND schedules.creator_user_id = sqlc.arg(actor_user_id)::uuid;

-- name: GetScheduleForCreatorForUpdate :one
SELECT schedules.*
FROM schedules
JOIN companies
  ON companies.id = schedules.company_id
 AND companies.deleting_at IS NULL
JOIN users
  ON users.id = schedules.creator_user_id
 AND users.disabled_at IS NULL
JOIN company_members AS actor_membership
  ON actor_membership.company_id = schedules.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
 AND actor_membership.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE schedules.id = sqlc.arg(schedule_id)::uuid
  AND schedules.company_id = sqlc.arg(company_id)::uuid
  AND schedules.creator_user_id = sqlc.arg(actor_user_id)::uuid
FOR UPDATE OF schedules;

-- name: ListSchedulesForCreator :many
SELECT schedules.*
FROM schedules
JOIN companies
  ON companies.id = schedules.company_id
 AND companies.deleting_at IS NULL
JOIN users
  ON users.id = schedules.creator_user_id
 AND users.disabled_at IS NULL
JOIN company_members AS actor_membership
  ON actor_membership.company_id = schedules.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
 AND actor_membership.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE (NOT sqlc.arg(exclude_terminal)::boolean OR schedules.status NOT IN ('completed', 'cancelled'))
 AND schedules.company_id = sqlc.arg(company_id)::uuid
  AND schedules.creator_user_id = sqlc.arg(actor_user_id)::uuid
  AND (
      sqlc.arg(status)::text = ''
      OR schedules.status = sqlc.arg(status)::text
  )
  AND (
      sqlc.arg(preset_key)::text = ''
      OR schedules.preset_key = sqlc.arg(preset_key)::text
  )
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (schedules.created_at, schedules.id) < (
          sqlc.arg(before_created_at)::timestamptz,
          sqlc.arg(before_id)::uuid
      )
  )
ORDER BY schedules.created_at DESC, schedules.id DESC
LIMIT sqlc.arg(page_size)::integer;

-- name: UpdateScheduleDefinition :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE schedules
SET agent_id = sqlc.arg(agent_id)::uuid,
    reviewed_provider = sqlc.arg(reviewed_provider)::text,
    name = sqlc.arg(name)::text,
    instructions = sqlc.arg(instructions)::text,
    preset_key = sqlc.narg(preset_key)::text,
    trigger_type = sqlc.arg(trigger_type)::text,
    run_at = sqlc.narg(run_at)::timestamptz,
    cron_expression = sqlc.narg(cron_expression)::text,
    timezone = sqlc.narg(timezone)::text,
    next_occurrence_at = sqlc.narg(next_occurrence_at)::timestamptz,
    pause_reason = CASE
        WHEN schedules.status = 'paused' THEN 'definition_updated'
        ELSE NULL
    END,
    updated_at = statement_clock.now_at,
    version = schedules.version + 1
FROM statement_clock
WHERE schedules.id = sqlc.arg(schedule_id)::uuid
  AND schedules.company_id = sqlc.arg(company_id)::uuid
  AND schedules.creator_user_id = sqlc.arg(actor_user_id)::uuid
  AND schedules.version = sqlc.arg(expected_version)::bigint
  AND schedules.version < 9007199254740991
  AND schedules.status IN ('draft', 'active', 'paused')
RETURNING schedules.*;

-- name: PauseSchedule :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE schedules
SET status = 'paused',
    pause_reason = sqlc.arg(reason)::text,
    updated_at = statement_clock.now_at,
    version = schedules.version + 1
FROM statement_clock
WHERE schedules.id = sqlc.arg(schedule_id)::uuid
  AND schedules.company_id = sqlc.arg(company_id)::uuid
  AND schedules.creator_user_id = sqlc.arg(actor_user_id)::uuid
  AND schedules.version = sqlc.arg(expected_version)::bigint
  AND schedules.version < 9007199254740991
  AND schedules.status = 'active'
RETURNING schedules.*;

-- name: ActivateSchedule :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE schedules
SET status = 'active',
    reviewed_provider = sqlc.arg(reviewed_provider)::text,
    next_occurrence_at = sqlc.arg(next_occurrence_at)::timestamptz,
    pause_reason = NULL,
    updated_at = statement_clock.now_at,
    version = schedules.version + 1
FROM statement_clock
WHERE schedules.id = sqlc.arg(schedule_id)::uuid
  AND schedules.company_id = sqlc.arg(company_id)::uuid
  AND schedules.creator_user_id = sqlc.arg(actor_user_id)::uuid
  AND schedules.version = sqlc.arg(expected_version)::bigint
  AND schedules.version < 9007199254740991
  AND schedules.status = 'draft'
RETURNING schedules.*;

-- name: ResumeSchedule :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE schedules
SET status = 'active',
    reviewed_provider = sqlc.arg(reviewed_provider)::text,
    next_occurrence_at = sqlc.arg(next_occurrence_at)::timestamptz,
    pause_reason = NULL,
    updated_at = statement_clock.now_at,
    version = schedules.version + 1
FROM statement_clock
WHERE schedules.id = sqlc.arg(schedule_id)::uuid
  AND schedules.company_id = sqlc.arg(company_id)::uuid
  AND schedules.creator_user_id = sqlc.arg(actor_user_id)::uuid
  AND schedules.version = sqlc.arg(expected_version)::bigint
  AND schedules.version < 9007199254740991
  AND schedules.status = 'paused'
RETURNING schedules.*;

-- name: HasActiveScheduleRun :one
SELECT EXISTS (
    SELECT 1
    FROM schedule_firings
    JOIN runs
      ON runs.schedule_firing_id = schedule_firings.id
     AND runs.company_id = schedule_firings.company_id
    WHERE schedule_firings.schedule_id = sqlc.arg(schedule_id)::uuid
      AND schedule_firings.company_id = sqlc.arg(company_id)::uuid
      AND runs.status IN ('queued', 'deferred', 'dispatched', 'waiting_local_directory', 'running')
)::boolean AS active;

-- name: CancelSchedule :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE schedules
SET status = 'cancelled',
    next_occurrence_at = NULL,
    pause_reason = NULL,
    updated_at = statement_clock.now_at,
    version = schedules.version + 1
FROM statement_clock
WHERE schedules.id = sqlc.arg(schedule_id)::uuid
  AND schedules.company_id = sqlc.arg(company_id)::uuid
  AND schedules.creator_user_id = sqlc.arg(actor_user_id)::uuid
  AND schedules.version = sqlc.arg(expected_version)::bigint
  AND schedules.version < 9007199254740991
  AND schedules.status IN ('draft', 'active', 'paused')
RETURNING schedules.*;

-- Discovery never holds authority locks for other companies.
-- name: ListDueScheduleLockTargets :many
SELECT id AS schedule_id, company_id, creator_user_id, next_occurrence_at,
       statement_timestamp()::timestamptz AS server_time
FROM schedules
WHERE status = 'active'
  AND next_occurrence_at <= statement_timestamp() - make_interval(secs => sqlc.arg(grace_seconds)::double precision)
  AND (sqlc.narg(after_time)::timestamptz IS NULL OR
       (next_occurrence_at, id) > (sqlc.narg(after_time)::timestamptz, sqlc.arg(after_id)::uuid))
ORDER BY next_occurrence_at, id
LIMIT sqlc.arg(page_size)::integer;

-- name: GetScheduleLockTarget :one
SELECT id AS schedule_id, company_id, creator_user_id FROM schedules WHERE id = $1;

-- name: SetScheduleTransactionTimeouts :exec
SELECT set_config('lock_timeout', sqlc.arg(lock_timeout)::text, true),
       set_config('statement_timeout', '10s', true);

-- name: LockNextDueSchedule :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
), candidate AS MATERIALIZED (
    SELECT schedules.id
    FROM schedules
    CROSS JOIN statement_clock
    WHERE schedules.status = 'active'
      AND (sqlc.arg(include_not_due)::boolean OR schedules.next_occurrence_at <= statement_clock.now_at)
      AND schedules.id = sqlc.arg(schedule_id)::uuid
    ORDER BY schedules.next_occurrence_at, schedules.id
    FOR UPDATE OF schedules SKIP LOCKED
    LIMIT 1
)
SELECT
    sqlc.embed(schedules),
    statement_clock.now_at AS server_time,
    (companies.id IS NOT NULL)::boolean AS company_valid,
    (
        users.id IS NOT NULL
        AND creator_membership.user_id IS NOT NULL
    )::boolean AS creator_valid,
    (
        agents.id IS NOT NULL
        AND runtimes.id IS NOT NULL
    )::boolean AS agent_valid,
    COALESCE(agents.provider = schedules.reviewed_provider, false)::boolean AS provider_matches,
    NOT EXISTS (
        SELECT 1
        FROM schedule_sources
        LEFT JOIN connector_catalog_items
          ON connector_catalog_items.id = schedule_sources.connector_id
         AND connector_catalog_items.company_id = schedule_sources.company_id
         AND connector_catalog_items.provider = schedule_sources.provider
         AND connector_catalog_items.source = schedule_sources.source
         AND connector_catalog_items.native_id = schedule_sources.native_id
        LEFT JOIN connector_policies
          ON connector_policies.connector_id = schedule_sources.connector_id
         AND connector_policies.company_id = schedule_sources.company_id
        WHERE schedule_sources.schedule_id = schedules.id
          AND (
              schedule_sources.company_id <> schedules.company_id
              OR schedule_sources.provider <> schedules.reviewed_provider
              OR connector_catalog_items.id IS NULL
              OR NOT connector_catalog_items.can_execute
              OR connector_catalog_items.status <> 'connected'
              OR connector_policies.connector_id IS NULL
              OR NOT connector_policies.desired_enabled
              OR connector_policies.review_required
              OR connector_policies.version <> schedule_sources.policy_revision
              OR connector_policies.reviewed_contract_digest
                    IS DISTINCT FROM schedule_sources.reviewed_contract_digest
              OR connector_catalog_items.contract_digest
                    IS DISTINCT FROM schedule_sources.reviewed_contract_digest
          )
    )::boolean AS sources_match
FROM candidate
JOIN schedules ON schedules.id = candidate.id
CROSS JOIN statement_clock
LEFT JOIN companies
  ON companies.id = schedules.company_id
 AND companies.deleting_at IS NULL
LEFT JOIN users
  ON users.id = schedules.creator_user_id
 AND users.disabled_at IS NULL
LEFT JOIN company_members AS creator_membership
  ON creator_membership.company_id = schedules.company_id
 AND creator_membership.user_id = schedules.creator_user_id
 AND creator_membership.status = 'active'
 AND creator_membership.role = ANY(sqlc.arg(actor_roles)::text[])
LEFT JOIN agents
  ON agents.id = schedules.agent_id
 AND agents.company_id = schedules.company_id
 AND agents.archived_at IS NULL
-- The creator's binding of the Agent (ADR 0014): no binding, no runtime.
LEFT JOIN agent_runtime_bindings AS creator_binding
  ON creator_binding.company_id = agents.company_id
 AND creator_binding.agent_id = agents.id
 AND creator_binding.user_id = schedules.creator_user_id
LEFT JOIN runtimes
  ON runtimes.id = creator_binding.runtime_id
 AND runtimes.company_id = creator_binding.company_id
 AND runtimes.removed_at IS NULL
 AND runtimes.owner_user_id = creator_binding.user_id;

-- name: LockScheduleConversation :exec
SELECT id FROM conversations
WHERE id = sqlc.arg(conversation_id)::uuid AND company_id = sqlc.arg(company_id)::uuid
  AND kind = 'schedule' AND schedule_id = sqlc.arg(schedule_id)::uuid
FOR UPDATE;

-- name: LockActiveScheduleRun :one
SELECT
    schedule_firings.id AS firing_id,
    schedule_firings.company_id,
    schedule_firings.version AS firing_version,
    schedule_firings.disposition AS firing_disposition,
    runs.id AS run_id,
    runs.status,
    runs.runtime_id,
    runs.updated_at AS run_updated_at
FROM schedule_firings
JOIN runs
  ON runs.schedule_firing_id = schedule_firings.id
 AND runs.company_id = schedule_firings.company_id
 AND runs.conversation_id = schedule_firings.conversation_id
WHERE schedule_firings.schedule_id = sqlc.arg(schedule_id)::uuid
 AND schedule_firings.company_id = sqlc.arg(company_id)::uuid
 AND schedule_firings.conversation_id = sqlc.arg(conversation_id)::uuid
 AND runs.status IN ('queued', 'deferred', 'dispatched', 'waiting_local_directory', 'running')
ORDER BY schedule_firings.occurrence_at DESC, schedule_firings.id DESC
FOR UPDATE OF schedule_firings, runs
LIMIT 1;

-- name: ListDriftedScheduleLockTargets :many
SELECT schedules.id AS schedule_id, schedules.company_id, schedules.creator_user_id
FROM schedules
    WHERE schedules.status = 'active'
      AND (
          NOT EXISTS (
              SELECT 1 FROM companies
              WHERE companies.id = schedules.company_id
                AND companies.deleting_at IS NULL
          )
          OR NOT EXISTS (
              SELECT 1
              FROM users
              JOIN company_members
                ON company_members.company_id = schedules.company_id
               AND company_members.user_id = users.id
               AND company_members.status = 'active'
               AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
              WHERE users.id = schedules.creator_user_id
                AND users.disabled_at IS NULL
          )
          OR NOT EXISTS (
              SELECT 1
              FROM agents
              JOIN agent_runtime_bindings AS creator_binding
                ON creator_binding.company_id = agents.company_id
               AND creator_binding.agent_id = agents.id
               AND creator_binding.user_id = schedules.creator_user_id
              JOIN runtimes
                ON runtimes.id = creator_binding.runtime_id
               AND runtimes.company_id = creator_binding.company_id
               AND runtimes.removed_at IS NULL
               AND runtimes.owner_user_id = creator_binding.user_id
              WHERE agents.id = schedules.agent_id
                AND agents.company_id = schedules.company_id
                AND agents.archived_at IS NULL
                AND agents.provider = schedules.reviewed_provider
          )
          OR EXISTS (
              SELECT 1
              FROM schedule_sources
              LEFT JOIN connector_catalog_items
                ON connector_catalog_items.id = schedule_sources.connector_id
               AND connector_catalog_items.company_id = schedule_sources.company_id
               AND connector_catalog_items.provider = schedule_sources.provider
               AND connector_catalog_items.source = schedule_sources.source
               AND connector_catalog_items.native_id = schedule_sources.native_id
              LEFT JOIN connector_policies
                ON connector_policies.connector_id = schedule_sources.connector_id
               AND connector_policies.company_id = schedule_sources.company_id
              WHERE schedule_sources.schedule_id = schedules.id
                AND (
                    schedule_sources.company_id <> schedules.company_id
                    OR schedule_sources.provider <> schedules.reviewed_provider
                    OR connector_catalog_items.id IS NULL
                    OR NOT connector_catalog_items.can_execute
                    OR connector_catalog_items.status <> 'connected'
                    OR connector_policies.connector_id IS NULL
                    OR NOT connector_policies.desired_enabled
                    OR connector_policies.review_required
                    OR connector_policies.version <> schedule_sources.policy_revision
                    OR connector_policies.reviewed_contract_digest
                          IS DISTINCT FROM schedule_sources.reviewed_contract_digest
                    OR connector_catalog_items.contract_digest
                          IS DISTINCT FROM schedule_sources.reviewed_contract_digest
                )
          )
      )

 AND schedules.id > sqlc.arg(after_id)::uuid
ORDER BY schedules.id
LIMIT sqlc.arg(page_size)::integer;

-- name: GetScheduleFiringReplay :one
SELECT schedule_firings.*
FROM schedule_firings
WHERE schedule_firings.company_id = sqlc.arg(company_id)::uuid
  AND schedule_firings.schedule_id = sqlc.arg(schedule_id)::uuid
  AND (
      (
          sqlc.arg(trigger)::text = 'scheduled'
          AND schedule_firings.trigger = 'scheduled'
          AND schedule_firings.occurrence_at = sqlc.arg(occurrence_at)::timestamptz
      )
      OR (
          sqlc.arg(trigger)::text = 'manual'
          AND schedule_firings.trigger = 'manual'
          AND schedule_firings.request_key = sqlc.narg(request_key)::uuid
      )
  );

-- One firing with the result columns ListScheduleFiringsForCreator reads, so
-- a Run now answer, new or replayed, has the history's shape. The caller
-- holds the creator's locked Schedule.
-- name: GetScheduleFiringWithResult :one
SELECT
    sqlc.embed(schedule_firings),
    input_message.seq AS message_seq,
    runs.id AS latest_run_id,
    runs.attempt AS run_attempt,
    runs.failure_reason AS run_failure_reason,
    runs.error AS run_cancel_reason,
    runtimes.status AS runtime_status,
    runtimes.last_seen_at AS runtime_last_seen_at,
    runtimes.unusable_reason_code AS runtime_unusable_reason,
    runs.status AS run_status,
    COALESCE(terminal_output.content, '')::text AS output,
    (terminal_output.content IS NOT NULL)::boolean AS has_output
FROM schedule_firings
LEFT JOIN runs ON runs.id = (
    SELECT r.id FROM runs AS r
    WHERE r.schedule_firing_id = schedule_firings.id AND r.company_id = schedule_firings.company_id
    ORDER BY r.attempt DESC, r.id DESC LIMIT 1
)
LEFT JOIN runtimes
  ON runtimes.id = runs.runtime_id
 AND runtimes.company_id = runs.company_id
 AND runtimes.owner_user_id = sqlc.arg(actor_user_id)::uuid
LEFT JOIN conversation_items AS input_message
  ON input_message.id = schedule_firings.message_id
 AND input_message.conversation_id = schedule_firings.conversation_id
 AND input_message.type = 'user_message'
LEFT JOIN LATERAL (
    SELECT conversation_items.content
    FROM conversation_items
    WHERE conversation_items.run_id = runs.id
      AND conversation_items.conversation_id = runs.conversation_id
      AND conversation_items.type = 'agent_text'
      AND runs.status = 'completed'
    ORDER BY conversation_items.seq DESC
    LIMIT 1
) AS terminal_output ON true
WHERE schedule_firings.id = sqlc.arg(firing_id)::uuid
  AND schedule_firings.company_id = sqlc.arg(company_id)::uuid;

-- name: InsertScheduleFiring :one
INSERT INTO schedule_firings (
    company_id, schedule_id, schedule_version, conversation_id,
    message_id, run_id, occurrence_at, trigger, request_key, disposition, reason, missed_count
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(schedule_id)::uuid,
    sqlc.arg(schedule_version)::bigint,
    sqlc.arg(conversation_id)::uuid,
    sqlc.narg(message_id)::uuid,
    sqlc.narg(run_id)::uuid,
    sqlc.arg(occurrence_at)::timestamptz,
    sqlc.arg(trigger)::text,
    sqlc.narg(request_key)::uuid,
    sqlc.arg(disposition)::text,
    sqlc.narg(reason)::text,
    sqlc.arg(missed_count)::integer
)
RETURNING schedule_firings.*;

-- name: LinkRunToScheduleFiring :one
-- The Run takes the Schedule's reviewed Connector sources as its exact scope.
-- They are copied, so a later Schedule edit cannot widen what this Run or its
-- retries may use. The caller's Schedule version check fences a concurrent edit.
WITH linked AS (
    UPDATE runs
    SET schedule_firing_id = sqlc.arg(firing_id)::uuid,
        connector_selection_mode = 'exact'
    WHERE runs.id = sqlc.arg(run_id)::uuid
      AND runs.company_id = sqlc.arg(company_id)::uuid
      AND runs.conversation_id = sqlc.arg(conversation_id)::uuid
      AND runs.schedule_firing_id IS NULL
    RETURNING runs.*
), selected AS (
    INSERT INTO run_connector_selections (
        run_id, connector_id, policy_revision, source, native_id, contract_digest
    )
    SELECT linked.id, sources.connector_id, sources.policy_revision,
        sources.source, sources.native_id, sources.reviewed_contract_digest
    FROM linked
    JOIN schedule_sources AS sources
      ON sources.schedule_id = sqlc.arg(schedule_id)::uuid
     AND sources.company_id = linked.company_id
)
SELECT * FROM linked;

-- name: AdvanceScheduleAfterFiring :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE schedules
SET status = sqlc.arg(status)::text,
    next_occurrence_at = sqlc.narg(next_occurrence_at)::timestamptz,
    last_occurrence_at = sqlc.arg(occurrence_at)::timestamptz,
    pause_reason = sqlc.narg(pause_reason)::text,
    updated_at = statement_clock.now_at,
    version = schedules.version + 1
FROM statement_clock
WHERE schedules.id = sqlc.arg(schedule_id)::uuid
  AND schedules.company_id = sqlc.arg(company_id)::uuid
  AND schedules.version = sqlc.arg(expected_version)::bigint
  AND schedules.version < 9007199254740991
  AND schedules.status = 'active'
RETURNING schedules.*;

-- name: RecordManualScheduleFiring :one
-- Run now is the Schedule's latest run too. It changes neither the definition
-- nor the next timed occurrence, so the revision stays: an edit or pause the
-- creator already has open still applies.
UPDATE schedules
SET last_occurrence_at = sqlc.arg(occurrence_at)::timestamptz
WHERE schedules.id = sqlc.arg(schedule_id)::uuid
  AND schedules.company_id = sqlc.arg(company_id)::uuid
  AND schedules.version = sqlc.arg(expected_version)::bigint
  AND schedules.status = 'active'
RETURNING schedules.*;

-- name: PauseInvalidDueSchedule :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE schedules
SET status = 'paused',
    pause_reason = sqlc.arg(reason)::text,
    updated_at = statement_clock.now_at,
    version = schedules.version + 1
FROM statement_clock
WHERE schedules.id = sqlc.arg(schedule_id)::uuid
  AND schedules.company_id = sqlc.arg(company_id)::uuid
  AND schedules.version = sqlc.arg(expected_version)::bigint
  AND schedules.version < 9007199254740991
  AND schedules.status = 'active'
RETURNING schedules.*;

-- name: UpdateScheduleFiringDisposition :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE schedule_firings
SET disposition = sqlc.arg(disposition)::text,
    reason = sqlc.arg(reason)::text,
    updated_at = statement_clock.now_at,
    version = schedule_firings.version + 1
FROM statement_clock
WHERE schedule_firings.id = sqlc.arg(firing_id)::uuid
  AND schedule_firings.company_id = sqlc.arg(company_id)::uuid
  AND schedule_firings.version < 9007199254740991
RETURNING schedule_firings.*;

-- name: ListScheduleFiringsForCreator :many
SELECT
    sqlc.embed(schedule_firings),
    input_message.seq AS message_seq,
    runs.id AS latest_run_id,
    runs.attempt AS run_attempt,
    runs.failure_reason AS run_failure_reason,
    runs.error AS run_cancel_reason,
    runtimes.status AS runtime_status,
    runtimes.last_seen_at AS runtime_last_seen_at,
    runtimes.unusable_reason_code AS runtime_unusable_reason,
    runs.status AS run_status,
    COALESCE(terminal_output.content, '')::text AS output,
    (terminal_output.content IS NOT NULL)::boolean AS has_output
FROM schedule_firings
JOIN schedules
  ON schedules.id = schedule_firings.schedule_id
 AND schedules.company_id = schedule_firings.company_id
JOIN companies
  ON companies.id = schedules.company_id
 AND companies.deleting_at IS NULL
JOIN company_members AS actor_membership
  ON actor_membership.company_id = schedules.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
 AND actor_membership.role = ANY(sqlc.arg(actor_roles)::text[])
LEFT JOIN runs ON runs.id = (
    SELECT r.id FROM runs AS r
    WHERE r.schedule_firing_id = schedule_firings.id AND r.company_id = schedule_firings.company_id
    ORDER BY r.attempt DESC, r.id DESC LIMIT 1
)
LEFT JOIN runtimes
  ON runtimes.id = runs.runtime_id
 AND runtimes.company_id = runs.company_id
 AND runtimes.owner_user_id = sqlc.arg(actor_user_id)::uuid
LEFT JOIN conversation_items AS input_message
  ON input_message.id = schedule_firings.message_id
 AND input_message.conversation_id = schedule_firings.conversation_id
 AND input_message.type = 'user_message'
LEFT JOIN LATERAL (
    SELECT conversation_items.content
    FROM conversation_items
    WHERE conversation_items.run_id = runs.id
      AND conversation_items.conversation_id = runs.conversation_id
      AND conversation_items.type = 'agent_text'
      AND runs.status = 'completed'
    ORDER BY conversation_items.seq DESC
    LIMIT 1
) AS terminal_output ON true
WHERE schedules.id = sqlc.arg(schedule_id)::uuid
  AND schedules.company_id = sqlc.arg(company_id)::uuid
  AND schedules.creator_user_id = sqlc.arg(actor_user_id)::uuid
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (schedule_firings.occurrence_at, schedule_firings.id) < (
          sqlc.arg(before_occurrence_at)::timestamptz,
          sqlc.arg(before_id)::uuid
      )
  )
ORDER BY schedule_firings.occurrence_at DESC, schedule_firings.id DESC
LIMIT sqlc.arg(page_size)::integer;

-- name: ListWaitingScheduleRuns :many
SELECT runs.* FROM runs
JOIN schedule_firings f ON f.id = runs.schedule_firing_id AND f.company_id = runs.company_id
WHERE f.schedule_id = $1 AND f.disposition = 'materialized'
 AND runs.status IN ('queued', 'deferred');

-- name: GetScheduleLag :one
WITH due AS MATERIALIZED (
 SELECT next_occurrence_at FROM schedules
 WHERE status = 'active' AND next_occurrence_at <= statement_timestamp()
 ORDER BY next_occurrence_at, id LIMIT 10000
)
SELECT COALESCE(EXTRACT(EPOCH FROM (statement_timestamp() - min(next_occurrence_at))), 0)::double precision AS lag_seconds,
 count(*)::bigint AS due_count,
 count(*) FILTER (WHERE next_occurrence_at <= statement_timestamp() - interval '60 seconds')::bigint AS overdue_count
FROM due;

-- name: ListScheduleWakeupTargets :many
SELECT DISTINCT runs.runtime_id, runs.company_id FROM runs
JOIN schedule_firings f ON f.id = runs.schedule_firing_id AND f.company_id = runs.company_id
JOIN runtimes ON runtimes.id = runs.runtime_id
WHERE runs.status IN ('queued', 'deferred') AND f.disposition = 'materialized'
 AND runtimes.status = 'online' AND runtimes.removed_at IS NULL
LIMIT 5000;

-- Called only after the Agent mutation's exclusive Company fence.
-- name: LockSchedulesForAgentArchive :many
SELECT schedules.* FROM schedules
WHERE company_id = sqlc.arg(company_id)::uuid AND agent_id = sqlc.arg(agent_id)::uuid
ORDER BY creator_user_id, id
FOR UPDATE;

-- name: PauseArchivedAgentSchedule :exec
UPDATE schedules SET status = 'paused', pause_reason = 'agent_unavailable',
 version = version + 1, updated_at = clock_timestamp()
WHERE id = $1 AND status IN ('active', 'paused') AND version < 9007199254740991;
