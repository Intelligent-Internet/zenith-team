-- Connector discovery is a complete generation published by one authenticated
-- Runtime. The caller takes the canonical Company lock first; this query then
-- rechecks the acting membership before locking the exact Runtime row.

-- name: LockConnectorDiscoveryAuthority :one
SELECT
    runtimes.id AS runtime_id,
    runtimes.company_id,
    runtimes.daemon_id,
    runtimes.provider,
    clock_timestamp()::timestamptz AS server_time
FROM runtimes
JOIN companies
  ON companies.id = runtimes.company_id
 AND companies.deleting_at IS NULL
JOIN company_members AS actor_membership
  ON actor_membership.company_id = runtimes.company_id
 AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
 AND actor_membership.status = 'active'
WHERE runtimes.id = sqlc.arg(runtime_id)::uuid
  AND runtimes.company_id = sqlc.arg(company_id)::uuid
  AND runtimes.daemon_id = sqlc.arg(daemon_id)::text
FOR UPDATE OF runtimes;
-- name: GetConnectorDiscoveryGenerationForUpdate :one
SELECT *
FROM connector_discovery_generations
WHERE runtime_id = sqlc.arg(runtime_id)::uuid
FOR UPDATE;

-- A reaffirmation renews only the freshness of the Runtime's unchanged
-- current generation. Claim freezing, invalidation and the catalog `stale`
-- flag read freshness from this row alone, so no catalog, observation or
-- policy row is rewritten; a mismatched or older observation touches nothing.
-- name: TouchConnectorDiscoveryGeneration :one
UPDATE connector_discovery_generations
SET observed_at = sqlc.arg(observed_at)::timestamptz,
    updated_at = clock_timestamp()
WHERE runtime_id = sqlc.arg(runtime_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND provider = sqlc.arg(provider)::text
  AND generation = sqlc.arg(generation)::uuid
  AND manifest_digest = sqlc.arg(manifest_digest)::text
  AND observed_at < sqlc.arg(observed_at)::timestamptz
RETURNING runtime_id;

-- name: ApplyConnectorDiscoveryGeneration :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
), input_items AS MATERIALIZED (
    SELECT
        input.source::text,
        input."nativeId"::text AS native_id,
        input.name::text,
        input.description::text,
        input.category::text,
        input."iconUrl"::text AS icon_url,
        input."installCount"::bigint AS install_count,
        input.status::text,
        input.action::text,
        input."canExecute"::boolean AS can_execute,
        input."contractDigest"::text AS contract_digest,
        input."toolNamespace"::jsonb AS tool_namespace
    FROM jsonb_to_recordset(sqlc.arg(items)::jsonb) AS input(
        source text,
        "nativeId" text,
        name text,
        description text,
        category text,
        "iconUrl" text,
        "installCount" bigint,
        status text,
        action text,
        "canExecute" boolean,
        "contractDigest" text,
        "toolNamespace" jsonb
    )
), inserted_catalog AS (
    INSERT INTO connector_catalog_items (
        company_id, provider, source, native_id, name, description, category,
        icon_url, install_count, status, action, can_execute, contract_digest, tool_namespace,
        observed_at, created_at, updated_at
    )
    SELECT
        sqlc.arg(company_id)::uuid,
        sqlc.arg(provider)::text,
        input_items.source,
        input_items.native_id,
        input_items.name,
        input_items.description,
        input_items.category,
        input_items.icon_url,
        input_items.install_count,
        input_items.status,
        input_items.action,
        input_items.can_execute,
        input_items.contract_digest,
        input_items.tool_namespace,
        sqlc.arg(observed_at)::timestamptz,
        statement_clock.now_at,
        statement_clock.now_at
    FROM input_items
    CROSS JOIN statement_clock
    ON CONFLICT (company_id, provider, source, native_id) DO NOTHING
    RETURNING id, company_id, provider, source, native_id
), generation_catalog AS MATERIALIZED (
    SELECT id, company_id, provider, source, native_id
    FROM inserted_catalog
    UNION ALL
    SELECT
        connector_catalog_items.id,
        connector_catalog_items.company_id,
        connector_catalog_items.provider,
        connector_catalog_items.source,
        connector_catalog_items.native_id
    FROM connector_catalog_items
    JOIN input_items
      ON input_items.source = connector_catalog_items.source
     AND input_items.native_id = connector_catalog_items.native_id
    WHERE connector_catalog_items.company_id = sqlc.arg(company_id)::uuid
      AND connector_catalog_items.provider = sqlc.arg(provider)::text
      AND NOT EXISTS (
          SELECT 1
          FROM inserted_catalog
          WHERE inserted_catalog.id = connector_catalog_items.id
      )
), policy_inserts AS (
    INSERT INTO connector_policies (
        connector_id, company_id, desired_enabled,
        reviewed_contract_digest, review_required
    )
    SELECT
        generation_catalog.id,
        generation_catalog.company_id,
        false,
        NULL,
        false
    FROM generation_catalog
    ON CONFLICT (connector_id) DO NOTHING
    RETURNING connector_id
), observation_upserts AS (
    INSERT INTO connector_runtime_observations (
        runtime_id, connector_id, company_id, provider, generation, name,
        description, category, icon_url, install_count, status, action, can_execute,
        contract_digest, tool_namespace, observed_at
    )
    SELECT
        sqlc.arg(runtime_id)::uuid,
        generation_catalog.id,
        generation_catalog.company_id,
        generation_catalog.provider,
        sqlc.arg(generation)::uuid,
        input_items.name,
        input_items.description,
        input_items.category,
        input_items.icon_url,
        input_items.install_count,
        input_items.status,
        input_items.action,
        input_items.can_execute,
        input_items.contract_digest,
        input_items.tool_namespace,
        sqlc.arg(observed_at)::timestamptz
    FROM generation_catalog
    JOIN input_items
      ON input_items.source = generation_catalog.source
     AND input_items.native_id = generation_catalog.native_id
    ON CONFLICT (runtime_id, connector_id) DO UPDATE SET
        company_id = EXCLUDED.company_id,
        provider = EXCLUDED.provider,
        generation = EXCLUDED.generation,
        name = EXCLUDED.name,
        description = EXCLUDED.description,
        category = EXCLUDED.category,
        icon_url = EXCLUDED.icon_url,
        install_count = EXCLUDED.install_count,
        status = EXCLUDED.status,
        action = EXCLUDED.action,
        can_execute = EXCLUDED.can_execute,
        contract_digest = EXCLUDED.contract_digest,
        tool_namespace = EXCLUDED.tool_namespace,
        observed_at = EXCLUDED.observed_at,
        updated_at = (SELECT now_at FROM statement_clock)
    RETURNING connector_id
), removed_observations AS (
    DELETE FROM connector_runtime_observations
    WHERE connector_runtime_observations.runtime_id = sqlc.arg(runtime_id)::uuid
      AND NOT EXISTS (
          SELECT 1
          FROM generation_catalog
          WHERE generation_catalog.id = connector_runtime_observations.connector_id
      )
    RETURNING connector_id
), generation_upsert AS (
    INSERT INTO connector_discovery_generations (
        runtime_id, company_id, provider, generation, manifest_digest, observed_at
    )
    VALUES (
        sqlc.arg(runtime_id)::uuid,
        sqlc.arg(company_id)::uuid,
        sqlc.arg(provider)::text,
        sqlc.arg(generation)::uuid,
        sqlc.arg(manifest_digest)::text,
        sqlc.arg(observed_at)::timestamptz
    )
    ON CONFLICT (runtime_id) DO UPDATE SET
        company_id = EXCLUDED.company_id,
        provider = EXCLUDED.provider,
        generation = EXCLUDED.generation,
        manifest_digest = EXCLUDED.manifest_digest,
        observed_at = EXCLUDED.observed_at,
        updated_at = (SELECT now_at FROM statement_clock)
    RETURNING runtime_id
)
SELECT
    (SELECT count(*) FROM input_items)::bigint AS input_count,
    (SELECT count(*) FROM generation_catalog)::bigint AS catalog_count,
    (
        SELECT count(*)
        FROM generation_catalog
        LEFT JOIN policy_inserts
          ON policy_inserts.connector_id = generation_catalog.id
    )::bigint AS policy_count,
    (SELECT count(*) FROM observation_upserts)::bigint AS observation_count,
    (SELECT count(*) FROM removed_observations)::bigint AS removed_count,
    (SELECT count(*) FROM generation_upsert)::bigint AS generation_count;

-- Connector policies are an internal execution-safety snapshot. Reconcile
-- from the exact selected Runtime for its provider whenever at least one
-- unarchived Agent is bound to this Runtime (ADR 0014); a secondary machine
-- still cannot change Run capability. `enable_only` limits the write to policies
-- the Runtime newly enables: a reaffirmation re-observed nothing, so it must
-- not revise what another Runtime's publication settled.
-- name: ReconcileCompanyConnectorPolicies :one
WITH selected_generation AS MATERIALIZED (
    SELECT
        runtimes.id AS runtime_id,
        runtimes.company_id,
        runtimes.provider,
        generations.generation
    FROM runtimes
    JOIN connector_discovery_generations AS generations
      ON generations.runtime_id = runtimes.id
     AND generations.company_id = runtimes.company_id
     AND generations.provider = runtimes.provider
    WHERE runtimes.id = sqlc.arg(runtime_id)::uuid
      AND runtimes.company_id = sqlc.arg(company_id)::uuid
      AND runtimes.provider = sqlc.arg(provider)::text
), policy_targets AS MATERIALIZED (
    SELECT
        connector_policies.connector_id,
        connector_policies.desired_enabled AS previous_desired_enabled,
        connector_policies.reviewed_contract_digest AS previous_reviewed_contract_digest,
        connector_policies.review_required AS previous_review_required,
        observations.contract_digest,
        COALESCE(observations.can_execute, false)::boolean AS can_execute,
        COALESCE(observations.status, 'unavailable')::text AS status,
        count(*) FILTER (WHERE connector_policies.desired_enabled) OVER ()::bigint
            AS desired_count,
        count(*) FILTER (
            WHERE NOT connector_policies.desired_enabled
              AND observations.can_execute
              AND observations.status = 'connected'
              AND observations.contract_digest IS NOT NULL
        ) OVER (
            ORDER BY lower(COALESCE(observations.name, connector_catalog_items.name)),
                connector_policies.connector_id
            ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
        )::bigint AS candidate_rank
    FROM selected_generation
    JOIN connector_catalog_items
      ON connector_catalog_items.company_id = selected_generation.company_id
     AND connector_catalog_items.provider = selected_generation.provider
    JOIN connector_policies
      ON connector_policies.connector_id = connector_catalog_items.id
     AND connector_policies.company_id = connector_catalog_items.company_id
    LEFT JOIN connector_runtime_observations AS observations
      ON observations.runtime_id = selected_generation.runtime_id
     AND observations.connector_id = connector_catalog_items.id
     AND observations.company_id = connector_catalog_items.company_id
     AND observations.provider = connector_catalog_items.provider
     AND observations.generation = selected_generation.generation
), policy_projection AS MATERIALIZED (
    SELECT
        policy_targets.connector_id,
        (
            policy_targets.previous_desired_enabled
            OR (
                policy_targets.can_execute
                AND policy_targets.status = 'connected'
                AND policy_targets.contract_digest IS NOT NULL
                AND policy_targets.candidate_rank <= GREATEST(
                    sqlc.arg(max_desired_executable_connectors)::bigint
                        - policy_targets.desired_count,
                    0
                )
            )
        )::boolean AS desired_enabled,
        CASE
            WHEN policy_targets.previous_desired_enabled
             AND policy_targets.previous_reviewed_contract_digest IS NOT NULL
                THEN policy_targets.previous_reviewed_contract_digest
            WHEN policy_targets.can_execute
             AND policy_targets.status = 'connected'
             AND policy_targets.contract_digest IS NOT NULL
             AND policy_targets.candidate_rank <= GREATEST(
                sqlc.arg(max_desired_executable_connectors)::bigint
                    - policy_targets.desired_count,
                0
             )
                THEN policy_targets.contract_digest
            ELSE policy_targets.previous_reviewed_contract_digest
        END::text AS reviewed_contract_digest,
        (
            policy_targets.previous_desired_enabled
            AND (
                policy_targets.previous_reviewed_contract_digest IS NULL
                OR NOT policy_targets.can_execute
                OR policy_targets.status <> 'connected'
                OR policy_targets.previous_reviewed_contract_digest
                    IS DISTINCT FROM policy_targets.contract_digest
            )
        )::boolean AS review_required
    FROM policy_targets
), reconciled AS (
    UPDATE connector_policies
    SET desired_enabled = policy_projection.desired_enabled,
        reviewed_contract_digest = policy_projection.reviewed_contract_digest,
        review_required = policy_projection.review_required,
        updated_at = clock_timestamp(),
        version = connector_policies.version + 1
    FROM policy_projection
    WHERE connector_policies.connector_id = policy_projection.connector_id
      AND connector_policies.company_id = sqlc.arg(company_id)::uuid
      AND (
          connector_policies.desired_enabled,
          connector_policies.reviewed_contract_digest,
          connector_policies.review_required
      ) IS DISTINCT FROM (
          policy_projection.desired_enabled,
          policy_projection.reviewed_contract_digest,
          policy_projection.review_required
      )
      AND (
          NOT sqlc.arg(enable_only)::boolean
          OR (NOT connector_policies.desired_enabled AND policy_projection.desired_enabled)
      )
      AND connector_policies.version < 9007199254740991
    RETURNING connector_policies.connector_id
)
SELECT
    (SELECT count(*) FROM policy_targets)::bigint AS policy_count,
    (SELECT count(*) FROM reconciled)::bigint AS reconciled_count;
-- name: DeleteConnectorDiscoveryForRuntime :exec
DELETE FROM connector_runtime_observations
WHERE runtime_id = sqlc.arg(runtime_id)::uuid;

-- name: DeleteConnectorDiscoveryGenerationForRuntime :exec
DELETE FROM connector_discovery_generations
WHERE runtime_id = sqlc.arg(runtime_id)::uuid;

-- name: FreezeRunConnectorSnapshot :one
-- Claim-time capability freezing belongs to the Connector domain even though
-- execution owns the surrounding transaction. Only reviewed contracts from
-- this exact Runtime's latest fresh discovery enter the immutable Run scope;
-- provider-local configuration and credentials remain daemon-local. SQL reads
-- the frozen rows; the daemon reads the same contract, tools included, from
-- runs.context. `name` is the app's display name for the Run's instructions
-- and `discoveryFresh` tells an empty scope caused by stale discovery apart
-- from one with nothing connected; neither takes part in any validity
-- comparison.
WITH candidate AS MATERIALIZED (
    SELECT runs.id, runs.company_id, runs.runtime_id, runs.connector_selection_mode, runtimes.provider,
        EXISTS (
            SELECT 1
            FROM connector_discovery_generations AS fresh_generations
            WHERE fresh_generations.runtime_id = runs.runtime_id
              AND fresh_generations.company_id = runs.company_id
              AND fresh_generations.provider = runtimes.provider
              AND fresh_generations.observed_at >= clock_timestamp()
                - sqlc.arg(discovery_stale_after_milliseconds)::bigint
                    * interval '1 millisecond'
        ) AS discovery_fresh
    FROM runs
    JOIN runtimes
      ON runtimes.id = runs.runtime_id
     AND runtimes.company_id = runs.company_id
    JOIN users AS runtime_owners
      ON runtime_owners.id = runtimes.owner_user_id
     AND runtime_owners.disabled_at IS NULL
    JOIN company_members AS runtime_owner_memberships
      ON runtime_owner_memberships.company_id = runtimes.company_id
     AND runtime_owner_memberships.user_id = runtimes.owner_user_id
     AND runtime_owner_memberships.status = 'active'
    WHERE runs.id = sqlc.arg(run_id)::uuid
      AND runs.runtime_id = sqlc.arg(runtime_id)::uuid
      AND runs.status = 'dispatched'
      AND sqlc.arg(discovery_stale_after_milliseconds)::bigint > 0
    FOR UPDATE OF runs
), eligible AS MATERIALIZED (
    SELECT
        catalog.id AS connector_id,
        policies.version AS policy_revision,
        catalog.source,
        catalog.native_id,
        COALESCE(observations.name, catalog.name) AS name,
        observations.contract_digest,
        observations.tool_namespace
    FROM candidate
    JOIN connector_catalog_items AS catalog
      ON catalog.company_id = candidate.company_id
     AND catalog.provider = candidate.provider
    JOIN connector_policies AS policies
      ON policies.connector_id = catalog.id
     AND policies.company_id = catalog.company_id
    JOIN connector_runtime_observations AS observations
      ON observations.runtime_id = candidate.runtime_id
     AND observations.connector_id = catalog.id
     AND observations.company_id = catalog.company_id
     AND observations.provider = catalog.provider
    JOIN connector_discovery_generations AS generations
      ON generations.runtime_id = observations.runtime_id
     AND generations.company_id = observations.company_id
     AND generations.provider = observations.provider
     AND generations.generation = observations.generation
     AND generations.observed_at >= clock_timestamp()
        - sqlc.arg(discovery_stale_after_milliseconds)::bigint
            * interval '1 millisecond'
    WHERE policies.desired_enabled
      AND NOT policies.review_required
      AND policies.reviewed_contract_digest = observations.contract_digest
      AND catalog.status = 'connected'
      AND catalog.can_execute
      AND catalog.contract_digest = observations.contract_digest
      AND catalog.tool_namespace = observations.tool_namespace
      AND observations.status = 'connected'
      AND observations.can_execute
      AND (
          (
              candidate.connector_selection_mode = 'all'
              AND CASE candidate.provider
                  WHEN 'codex' THEN catalog.source = 'codex_app'
                  WHEN 'claude' THEN catalog.source IN ('claude_plugin', 'claude_mcp')
                  ELSE false
              END
          )
          OR (
              candidate.connector_selection_mode = 'exact'
              AND EXISTS (
                  SELECT 1
                  FROM run_connector_selections AS selected
                  WHERE selected.run_id = candidate.id
                    AND selected.connector_id = catalog.id
                    AND selected.policy_revision = policies.version
                    AND selected.source = catalog.source
                    AND selected.native_id = catalog.native_id
                    AND selected.contract_digest = observations.contract_digest
              )
          )
      )
), frozen AS MATERIALIZED (
    -- An exact selection freezes whole or not at all; 'all' selects no rows.
    SELECT candidate.id, candidate.discovery_fresh
    FROM candidate
    WHERE (
        SELECT count(*) FROM run_connector_selections AS selected WHERE selected.run_id = candidate.id
    ) = CASE candidate.connector_selection_mode
        WHEN 'exact' THEN (SELECT count(*) FROM eligible)
        ELSE 0
    END
), removed AS (
    -- A claim requeued after delivery failure freezes again.
    DELETE FROM run_connector_snapshots AS snapshot
    USING frozen
    WHERE snapshot.run_id = frozen.id
      AND snapshot.connector_id NOT IN (SELECT eligible.connector_id FROM eligible)
), refreshed AS (
    INSERT INTO run_connector_snapshots (
        run_id, connector_id, policy_revision, source, native_id, contract_digest
    )
    SELECT frozen.id, eligible.connector_id, eligible.policy_revision,
        eligible.source, eligible.native_id, eligible.contract_digest
    FROM frozen CROSS JOIN eligible
    ON CONFLICT (run_id, connector_id) DO UPDATE
    SET policy_revision = EXCLUDED.policy_revision,
        source = EXCLUDED.source,
        native_id = EXCLUDED.native_id,
        contract_digest = EXCLUDED.contract_digest,
        updated_at = clock_timestamp()
)
UPDATE runs
SET connector_snapshot_frozen_at = clock_timestamp(),
    context = COALESCE(runs.context, '{}'::jsonb) || jsonb_build_object(
        'connectorSnapshot', jsonb_build_object(
            'schemaVersion', 1,
            'discoveryFresh', frozen.discovery_fresh,
            'connectors', COALESCE((
                SELECT jsonb_agg(
                    jsonb_build_object(
                        'id', eligible.connector_id,
                        'policyRevision', eligible.policy_revision,
                        'source', eligible.source,
                        'nativeId', eligible.native_id,
                        'name', eligible.name,
                        'contractDigest', eligible.contract_digest,
                        'tools', eligible.tool_namespace
                    )
                    ORDER BY eligible.source, eligible.native_id, eligible.connector_id
                )
                FROM eligible
            ), '[]'::jsonb)
        )
    )
FROM frozen
WHERE runs.id = frozen.id
RETURNING runs.*;

-- name: ListRunIDsWithInvalidConnectorSnapshots :many
-- Post-commit discovery/policy reconciliation uses this read to find only
-- active Runs whose frozen contract is no longer executable on their exact
-- Runtime. The execution domain performs each terminal transition; this query
-- merely answers the Connector-domain validity question.
SELECT runs.id
FROM runs
JOIN runtimes
  ON runtimes.id = runs.runtime_id
 AND runtimes.company_id = runs.company_id
WHERE runs.company_id = sqlc.arg(company_id)::uuid
  AND runtimes.provider = sqlc.arg(provider)::text
  AND runs.status IN ('dispatched', 'waiting_local_directory', 'running')
  AND (
      sqlc.narg(runtime_id)::uuid IS NULL
      OR runs.runtime_id = sqlc.narg(runtime_id)::uuid
  )
  AND (
      (
          runs.connector_selection_mode = 'exact'
          AND (
              -- A selected connector the snapshot does not carry unchanged.
              EXISTS (
                  SELECT 1
                  FROM run_connector_selections AS selected
                  WHERE selected.run_id = runs.id
                    AND (
                        sqlc.narg(connector_id)::uuid IS NULL
                        OR selected.connector_id = sqlc.narg(connector_id)::uuid
                    )
                    AND NOT EXISTS (
                        SELECT 1
                        FROM run_connector_snapshots AS snapshot
                        WHERE snapshot.run_id = selected.run_id
                          AND snapshot.connector_id = selected.connector_id
                          AND snapshot.policy_revision = selected.policy_revision
                          AND snapshot.source = selected.source
                          AND snapshot.native_id = selected.native_id
                          AND snapshot.contract_digest = selected.contract_digest
                    )
              )
              -- A frozen connector the selection never asked for.
              OR EXISTS (
                  SELECT 1
                  FROM run_connector_snapshots AS snapshot
                  WHERE snapshot.run_id = runs.id
                    AND (
                        sqlc.narg(connector_id)::uuid IS NULL
                        OR snapshot.connector_id = sqlc.narg(connector_id)::uuid
                    )
                    AND NOT EXISTS (
                        SELECT 1
                        FROM run_connector_selections AS selected
                        WHERE selected.run_id = snapshot.run_id
                          AND selected.connector_id = snapshot.connector_id
                          AND selected.policy_revision = snapshot.policy_revision
                          AND selected.source = snapshot.source
                          AND selected.native_id = snapshot.native_id
                          AND selected.contract_digest = snapshot.contract_digest
                    )
              )
          )
      )
      -- A frozen connector no longer executable on this Runtime. The digest is
      -- the hash of the tool list, so an equal digest means the same tools.
      OR EXISTS (
          SELECT 1
          FROM run_connector_snapshots AS snapshot
          WHERE snapshot.run_id = runs.id
            AND (
                sqlc.narg(connector_id)::uuid IS NULL
                OR snapshot.connector_id = sqlc.narg(connector_id)::uuid
            )
            AND NOT EXISTS (
                SELECT 1
                FROM connector_catalog_items AS catalog
                JOIN connector_policies AS policies
                  ON policies.connector_id = catalog.id
                 AND policies.company_id = catalog.company_id
                JOIN connector_runtime_observations AS observations
                  ON observations.runtime_id = runs.runtime_id
                 AND observations.connector_id = catalog.id
                 AND observations.company_id = catalog.company_id
                 AND observations.provider = catalog.provider
                 AND observations.status = 'connected'
                 AND observations.can_execute
                JOIN connector_discovery_generations AS generations
                  ON generations.runtime_id = observations.runtime_id
                 AND generations.company_id = observations.company_id
                 AND generations.provider = observations.provider
                 AND generations.generation = observations.generation
                 AND generations.observed_at >= clock_timestamp()
                    - sqlc.arg(discovery_stale_after_milliseconds)::bigint
                        * interval '1 millisecond'
                WHERE catalog.id = snapshot.connector_id
                  AND catalog.company_id = runs.company_id
                  AND catalog.provider = runtimes.provider
                  AND catalog.source = snapshot.source
                  AND catalog.native_id = snapshot.native_id
                  AND catalog.status = 'connected'
                  AND catalog.can_execute
                  AND catalog.contract_digest = snapshot.contract_digest
                  AND policies.desired_enabled
                  AND NOT policies.review_required
                  AND policies.reviewed_contract_digest = snapshot.contract_digest
                  AND policies.version = snapshot.policy_revision
                  AND observations.contract_digest = snapshot.contract_digest
            )
      )
  )
ORDER BY runs.id;

-- The Company row copies one Runtime's observation per Connector. The claim
-- freeze and app readiness admit a Connector only while that row is connected
-- and executable with the Run Runtime's own digest, so an online Runtime that
-- can execute the Connector outranks a newer publication from one that cannot:
-- a desktop without the app connected must not switch it off for the desktop
-- that has it.
-- name: RefreshCompanyConnectorCatalogFromObservations :one
WITH ranked AS MATERIALIZED (
    SELECT DISTINCT ON (observations.connector_id)
        observations.*
    FROM connector_runtime_observations AS observations
    JOIN connector_discovery_generations AS generations
      ON generations.runtime_id = observations.runtime_id
     AND generations.generation = observations.generation
    JOIN runtimes
      ON runtimes.id = observations.runtime_id
     AND runtimes.company_id = observations.company_id
     AND runtimes.provider = observations.provider
    JOIN users AS runtime_owners
      ON runtime_owners.id = runtimes.owner_user_id
     AND runtime_owners.disabled_at IS NULL
    JOIN company_members AS runtime_owner_memberships
      ON runtime_owner_memberships.company_id = runtimes.company_id
     AND runtime_owner_memberships.user_id = runtimes.owner_user_id
     AND runtime_owner_memberships.status = 'active'
    WHERE observations.company_id = sqlc.arg(company_id)::uuid
      AND observations.provider = sqlc.arg(provider)::text
    ORDER BY
        observations.connector_id,
        (runtimes.status = 'online') DESC,
        (observations.status = 'connected' AND observations.can_execute) DESC,
        observations.observed_at DESC,
        observations.runtime_id
), refreshed AS (
    UPDATE connector_catalog_items
    SET name = ranked.name,
        description = ranked.description,
        category = ranked.category,
        icon_url = ranked.icon_url,
        install_count = ranked.install_count,
        status = ranked.status,
        action = ranked.action,
        can_execute = ranked.can_execute,
        contract_digest = ranked.contract_digest,
        tool_namespace = ranked.tool_namespace,
        observed_at = ranked.observed_at,
        updated_at = clock_timestamp(),
        version = connector_catalog_items.version + 1
    FROM ranked
    WHERE connector_catalog_items.id = ranked.connector_id
      AND connector_catalog_items.company_id = ranked.company_id
    RETURNING connector_catalog_items.id
)
SELECT count(*)::bigint AS refreshed_count
FROM refreshed;

-- name: ListCompanyConnectorCatalog :many
WITH selected_observations AS MATERIALIZED (
    SELECT observations.*
    FROM connector_runtime_observations AS observations
    JOIN connector_discovery_generations AS generations
      ON generations.runtime_id = observations.runtime_id
     AND generations.company_id = observations.company_id
     AND generations.provider = observations.provider
     AND generations.generation = observations.generation
    WHERE observations.runtime_id = sqlc.narg(selected_runtime_id)::uuid
      AND observations.company_id = sqlc.arg(company_id)::uuid
      AND observations.provider = sqlc.arg(provider)::text
)
SELECT
    sqlc.embed(connector_catalog_items),
    connector_policies.desired_enabled,
    connector_policies.version AS policy_version,
    connector_policies.review_required,
    COALESCE(selected_observations.name, connector_catalog_items.name)::text AS selected_name,
    (selected_observations.connector_id IS NOT NULL)::boolean AS has_selected_observation,
    selected_observations.description AS selected_description,
    selected_observations.category AS selected_category,
    selected_observations.icon_url AS selected_icon_url,
    selected_observations.install_count AS selected_install_count,
    COALESCE(selected_observations.status, 'unavailable')::text AS selected_status,
    COALESCE(selected_observations.action, 'none')::text AS selected_action,
    COALESCE(selected_observations.can_execute, false)::boolean AS selected_can_execute,
    COALESCE(selected_observations.observed_at, connector_catalog_items.observed_at)::timestamptz
        AS selected_observed_at,
    lower(COALESCE(selected_observations.name, connector_catalog_items.name))::text AS sort_name
FROM connector_catalog_items
JOIN connector_policies
  ON connector_policies.connector_id = connector_catalog_items.id
 AND connector_policies.company_id = connector_catalog_items.company_id
LEFT JOIN selected_observations
  ON selected_observations.connector_id = connector_catalog_items.id
JOIN companies
  ON companies.id = connector_catalog_items.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
  ON company_members.company_id = connector_catalog_items.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
 AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE connector_catalog_items.company_id = sqlc.arg(company_id)::uuid
  AND connector_catalog_items.provider = sqlc.arg(provider)::text
  AND (sqlc.arg(source)::text = '' OR connector_catalog_items.source = sqlc.arg(source)::text)
  AND (
      sqlc.arg(search_query)::text = ''
      OR COALESCE(selected_observations.name, connector_catalog_items.name)
          ILIKE '%' || replace(replace(replace(
              sqlc.arg(search_query)::text, '\', '\\'
          ), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
      OR CASE WHEN selected_observations.connector_id IS NULL
          THEN connector_catalog_items.description
          ELSE selected_observations.description END
          ILIKE '%' || replace(replace(replace(
              sqlc.arg(search_query)::text, '\', '\\'
          ), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
  )
  AND (
      sqlc.arg(category)::text = ''
      OR CASE WHEN selected_observations.connector_id IS NULL
          THEN connector_catalog_items.category
          ELSE selected_observations.category END = sqlc.arg(category)::text
  )
  AND (
      sqlc.arg(status)::text = ''
      OR COALESCE(selected_observations.status, 'unavailable') = sqlc.arg(status)::text
  )
  AND (
      NOT sqlc.arg(review_required)::boolean
      OR connector_policies.review_required
  )
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (
          lower(COALESCE(selected_observations.name, connector_catalog_items.name)),
          connector_catalog_items.id
      ) > (
          sqlc.arg(after_name)::text,
          sqlc.arg(after_id)::uuid
      )
  )
ORDER BY lower(COALESCE(selected_observations.name, connector_catalog_items.name)),
    connector_catalog_items.id
LIMIT sqlc.arg(page_size)::integer;

-- Resolve a bounded, exact identity set for the active provider without making
-- frontend curation depend on catalog pagination. The request position is
-- server-authored after domain validation, so missing identities can be
-- skipped while the remaining projection preserves caller order.
-- name: ResolveCompanyConnectorsByIdentity :many
WITH requested AS MATERIALIZED (
    SELECT
        (source_item.ordinality - 1)::bigint AS request_position,
        source_item.source,
        native_item.native_id
    -- Two arrays built from one slice: equal length, paired by position.
    FROM unnest(sqlc.arg(sources)::text[]) WITH ORDINALITY AS source_item(source, ordinality)
    JOIN unnest(sqlc.arg(native_ids)::text[]) WITH ORDINALITY AS native_item(native_id, ordinality)
      ON native_item.ordinality = source_item.ordinality
), selected_observations AS MATERIALIZED (
    SELECT observations.*
    FROM connector_runtime_observations AS observations
    JOIN connector_discovery_generations AS generations
      ON generations.runtime_id = observations.runtime_id
     AND generations.company_id = observations.company_id
     AND generations.provider = observations.provider
     AND generations.generation = observations.generation
    WHERE observations.runtime_id = sqlc.narg(selected_runtime_id)::uuid
      AND observations.company_id = sqlc.arg(company_id)::uuid
      AND observations.provider = sqlc.arg(provider)::text
)
SELECT
    sqlc.embed(connector_catalog_items),
    connector_policies.desired_enabled,
    connector_policies.version AS policy_version,
    connector_policies.review_required,
    COALESCE(selected_observations.name, connector_catalog_items.name)::text AS selected_name,
    (selected_observations.connector_id IS NOT NULL)::boolean AS has_selected_observation,
    selected_observations.description AS selected_description,
    selected_observations.category AS selected_category,
    selected_observations.icon_url AS selected_icon_url,
    selected_observations.install_count AS selected_install_count,
    COALESCE(selected_observations.status, 'unavailable')::text AS selected_status,
    COALESCE(selected_observations.action, 'none')::text AS selected_action,
    COALESCE(selected_observations.can_execute, false)::boolean AS selected_can_execute,
    COALESCE(selected_observations.observed_at, connector_catalog_items.observed_at)::timestamptz
        AS selected_observed_at,
    requested.request_position
FROM requested
JOIN connector_catalog_items
  ON connector_catalog_items.company_id = sqlc.arg(company_id)::uuid
 AND connector_catalog_items.provider = sqlc.arg(provider)::text
 AND connector_catalog_items.source = requested.source
 AND connector_catalog_items.native_id = requested.native_id
JOIN connector_policies
  ON connector_policies.connector_id = connector_catalog_items.id
 AND connector_policies.company_id = connector_catalog_items.company_id
LEFT JOIN selected_observations
  ON selected_observations.connector_id = connector_catalog_items.id
JOIN companies
  ON companies.id = connector_catalog_items.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
  ON company_members.company_id = connector_catalog_items.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
 AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
ORDER BY requested.request_position;

-- name: CountCompanyConnectorCatalog :one
WITH selected_observations AS MATERIALIZED (
    SELECT observations.*
    FROM connector_runtime_observations AS observations
    JOIN connector_discovery_generations AS generations
      ON generations.runtime_id = observations.runtime_id
     AND generations.company_id = observations.company_id
     AND generations.provider = observations.provider
     AND generations.generation = observations.generation
    WHERE observations.runtime_id = sqlc.narg(selected_runtime_id)::uuid
      AND observations.company_id = sqlc.arg(company_id)::uuid
      AND observations.provider = sqlc.arg(provider)::text
)
SELECT count(*)::bigint
FROM connector_catalog_items
JOIN connector_policies
  ON connector_policies.connector_id = connector_catalog_items.id
 AND connector_policies.company_id = connector_catalog_items.company_id
LEFT JOIN selected_observations
  ON selected_observations.connector_id = connector_catalog_items.id
JOIN companies
  ON companies.id = connector_catalog_items.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
  ON company_members.company_id = connector_catalog_items.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
 AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE connector_catalog_items.company_id = sqlc.arg(company_id)::uuid
  AND connector_catalog_items.provider = sqlc.arg(provider)::text
  AND (sqlc.arg(source)::text = '' OR connector_catalog_items.source = sqlc.arg(source)::text)
  AND (
      sqlc.arg(search_query)::text = ''
      OR COALESCE(selected_observations.name, connector_catalog_items.name)
          ILIKE '%' || replace(replace(replace(
              sqlc.arg(search_query)::text, '\', '\\'
          ), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
      OR CASE WHEN selected_observations.connector_id IS NULL
          THEN connector_catalog_items.description
          ELSE selected_observations.description END
          ILIKE '%' || replace(replace(replace(
              sqlc.arg(search_query)::text, '\', '\\'
          ), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
  )
  AND (
      sqlc.arg(category)::text = ''
      OR CASE WHEN selected_observations.connector_id IS NULL
          THEN connector_catalog_items.category
          ELSE selected_observations.category END = sqlc.arg(category)::text
  )
  AND (
      sqlc.arg(status)::text = ''
      OR COALESCE(selected_observations.status, 'unavailable') = sqlc.arg(status)::text
  )
  AND (
      NOT sqlc.arg(review_required)::boolean
      OR connector_policies.review_required
  );

-- name: CountCompanyConnectedConnectors :one
WITH selected_observations AS MATERIALIZED (
    SELECT observations.connector_id, observations.status
    FROM connector_runtime_observations AS observations
    JOIN connector_discovery_generations AS generations
      ON generations.runtime_id = observations.runtime_id
     AND generations.company_id = observations.company_id
     AND generations.provider = observations.provider
     AND generations.generation = observations.generation
    WHERE observations.runtime_id = sqlc.narg(selected_runtime_id)::uuid
      AND observations.company_id = sqlc.arg(company_id)::uuid
      AND observations.provider = sqlc.arg(provider)::text
)
SELECT count(*)::bigint
FROM connector_catalog_items
JOIN connector_policies
  ON connector_policies.connector_id = connector_catalog_items.id
 AND connector_policies.company_id = connector_catalog_items.company_id
LEFT JOIN selected_observations
  ON selected_observations.connector_id = connector_catalog_items.id
JOIN companies
  ON companies.id = connector_catalog_items.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
  ON company_members.company_id = connector_catalog_items.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
 AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE connector_catalog_items.company_id = sqlc.arg(company_id)::uuid
  AND connector_catalog_items.provider = sqlc.arg(provider)::text
  AND (sqlc.arg(source)::text = '' OR connector_catalog_items.source = sqlc.arg(source)::text)
  AND COALESCE(selected_observations.status, 'unavailable') = 'connected';

-- name: CountCompanyConnectorsByCategory :many
WITH selected_observations AS MATERIALIZED (
    SELECT observations.*
    FROM connector_runtime_observations AS observations
    JOIN connector_discovery_generations AS generations
      ON generations.runtime_id = observations.runtime_id
     AND generations.company_id = observations.company_id
     AND generations.provider = observations.provider
     AND generations.generation = observations.generation
    WHERE observations.runtime_id = sqlc.narg(selected_runtime_id)::uuid
      AND observations.company_id = sqlc.arg(company_id)::uuid
      AND observations.provider = sqlc.arg(provider)::text
), categorized AS MATERIALIZED (
    SELECT CASE WHEN selected_observations.connector_id IS NULL
        THEN connector_catalog_items.category
        ELSE selected_observations.category END::text AS category
FROM connector_catalog_items
JOIN connector_policies
  ON connector_policies.connector_id = connector_catalog_items.id
 AND connector_policies.company_id = connector_catalog_items.company_id
LEFT JOIN selected_observations
  ON selected_observations.connector_id = connector_catalog_items.id
JOIN companies
  ON companies.id = connector_catalog_items.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
  ON company_members.company_id = connector_catalog_items.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
 AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE connector_catalog_items.company_id = sqlc.arg(company_id)::uuid
  AND connector_catalog_items.provider = sqlc.arg(provider)::text
  AND (sqlc.arg(source)::text = '' OR connector_catalog_items.source = sqlc.arg(source)::text)
)
SELECT COALESCE(categorized.category, '')::text AS category, count(*)::bigint AS count
FROM categorized
GROUP BY COALESCE(categorized.category, '')
ORDER BY COALESCE(categorized.category, '');

-- name: GetCompanyConnectorContextForOwner :one
WITH owner_membership AS MATERIALIZED (
    SELECT company_members.company_id
    FROM company_members
    JOIN companies ON companies.id = company_members.company_id
    WHERE company_members.company_id = sqlc.arg(company_id)::uuid
      AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
      AND company_members.status = 'active'
      AND companies.deleting_at IS NULL
), selected_runtime AS MATERIALIZED (
    SELECT runtimes.*
    FROM runtimes
    JOIN owner_membership ON owner_membership.company_id = runtimes.company_id
    JOIN users AS runtime_owners
      ON runtime_owners.id = runtimes.owner_user_id
     AND runtime_owners.disabled_at IS NULL
    JOIN company_members AS runtime_owner_memberships
      ON runtime_owner_memberships.company_id = runtimes.company_id
     AND runtime_owner_memberships.user_id = runtimes.owner_user_id
     AND runtime_owner_memberships.status = 'active'
    WHERE runtimes.removed_at IS NULL
      -- ADR 0014: only the viewer's own runtimes are discovery contexts.
      AND runtimes.owner_user_id = sqlc.arg(actor_user_id)::uuid
      AND (
          sqlc.arg(explicit_provider)::text = ''
          OR runtimes.provider = sqlc.arg(explicit_provider)::text
      )
      AND (
          sqlc.narg(explicit_runtime_id)::uuid IS NULL
          OR runtimes.id = sqlc.narg(explicit_runtime_id)::uuid
      )
      AND (
          sqlc.narg(explicit_runtime_id)::uuid IS NOT NULL
          OR EXISTS (
              SELECT 1
              FROM agent_runtime_bindings AS binding
              JOIN agents
                ON agents.company_id = binding.company_id
               AND agents.id = binding.agent_id
              WHERE binding.company_id = runtimes.company_id
                AND binding.runtime_id = runtimes.id
                AND agents.archived_at IS NULL
          )
      )
    ORDER BY
        (runtimes.status = 'online') DESC,
        runtimes.last_seen_at DESC,
        runtimes.id
    LIMIT 1
)
SELECT
    selected_runtime.provider,
    selected_runtime.id AS runtime_id,
    selected_runtime.name AS runtime_name,
    (selected_runtime.status = 'online')::boolean AS runtime_online,
    (
        selected_runtime.last_seen_at IS NULL
        OR selected_runtime.last_seen_at <
            clock_timestamp() -
            sqlc.arg(runtime_stale_after_milliseconds)::bigint * interval '1 millisecond'
        OR generations.runtime_id IS NULL
        OR generations.observed_at <
            clock_timestamp() -
            sqlc.arg(discovery_stale_after_milliseconds)::bigint * interval '1 millisecond'
    )::boolean AS stale
FROM selected_runtime
LEFT JOIN connector_discovery_generations AS generations
  ON generations.runtime_id = selected_runtime.id
 AND generations.provider = selected_runtime.provider;
-- name: GetEnableConnectorContractOnSelectedRuntimeForOwner :one
WITH authorized_connector AS MATERIALIZED (
    SELECT connector_catalog_items.*
    FROM connector_catalog_items
    JOIN companies
      ON companies.id = connector_catalog_items.company_id
     AND companies.deleting_at IS NULL
    JOIN company_members
      ON company_members.company_id = connector_catalog_items.company_id
     AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
     AND company_members.status = 'active'
     AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
    WHERE connector_catalog_items.id = sqlc.arg(connector_id)::uuid
      AND connector_catalog_items.company_id = sqlc.arg(company_id)::uuid
      AND (
          (connector_catalog_items.provider = 'codex'
              AND connector_catalog_items.source = 'codex_app')
          OR (connector_catalog_items.provider = 'claude'
              AND connector_catalog_items.source IN ('claude_plugin', 'claude_mcp'))
      )
), selected_runtime AS MATERIALIZED (
    SELECT runtimes.id, runtimes.company_id, runtimes.provider
    FROM authorized_connector
    JOIN runtimes
      ON runtimes.company_id = authorized_connector.company_id
     AND runtimes.provider = authorized_connector.provider
    JOIN company_members AS runtime_owners
      ON runtime_owners.company_id = runtimes.company_id
     AND runtime_owners.user_id = runtimes.owner_user_id
     AND runtime_owners.status = 'active'
    JOIN users AS runtime_owner_users
      ON runtime_owner_users.id = runtimes.owner_user_id
     AND runtime_owner_users.disabled_at IS NULL
    WHERE runtimes.status = 'online'
      -- ADR 0014: a runtime proves a contract for its registrant only.
      AND runtimes.owner_user_id = sqlc.arg(actor_user_id)::uuid
      AND runtimes.last_seen_at >=
          clock_timestamp() -
          sqlc.arg(runtime_stale_after_milliseconds)::bigint * interval '1 millisecond'
      AND EXISTS (
          SELECT 1
          FROM agent_runtime_bindings AS binding
          JOIN agents
            ON agents.company_id = binding.company_id
           AND agents.id = binding.agent_id
          WHERE binding.company_id = runtimes.company_id
            AND binding.runtime_id = runtimes.id
            AND agents.archived_at IS NULL
      )
    ORDER BY runtimes.last_seen_at DESC, runtimes.id
    LIMIT 1
)
SELECT observations.contract_digest
FROM authorized_connector
JOIN selected_runtime
  ON selected_runtime.company_id = authorized_connector.company_id
 AND selected_runtime.provider = authorized_connector.provider
JOIN connector_discovery_generations AS generations
  ON generations.runtime_id = selected_runtime.id
 AND generations.company_id = selected_runtime.company_id
 AND generations.provider = selected_runtime.provider
 AND generations.observed_at >=
     clock_timestamp() -
     sqlc.arg(discovery_stale_after_milliseconds)::bigint * interval '1 millisecond'
JOIN connector_runtime_observations AS observations
  ON observations.runtime_id = selected_runtime.id
 AND observations.connector_id = authorized_connector.id
 AND observations.company_id = selected_runtime.company_id
 AND observations.provider = selected_runtime.provider
 AND observations.generation = generations.generation
WHERE observations.status = 'connected'
  AND observations.can_execute
  AND observations.contract_digest IS NOT NULL
  AND (
      SELECT count(*)
      FROM connector_policies AS desired_policies
      JOIN connector_catalog_items AS desired_connectors
        ON desired_connectors.id = desired_policies.connector_id
       AND desired_connectors.company_id = desired_policies.company_id
      WHERE desired_policies.company_id = authorized_connector.company_id
        AND desired_policies.desired_enabled
        AND desired_policies.connector_id <> authorized_connector.id
        AND desired_connectors.provider = authorized_connector.provider
  ) < sqlc.arg(max_desired_executable_connectors)::integer
LIMIT 1;
-- name: UpdateConnectorPolicyForOwner :one
WITH authorized AS MATERIALIZED (
    SELECT connector_catalog_items.id
    FROM connector_catalog_items
    JOIN companies
      ON companies.id = connector_catalog_items.company_id
     AND companies.deleting_at IS NULL
    JOIN company_members
      ON company_members.company_id = connector_catalog_items.company_id
     AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
     AND company_members.status = 'active'
     AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
    WHERE connector_catalog_items.id = sqlc.arg(connector_id)::uuid
      AND connector_catalog_items.company_id = sqlc.arg(company_id)::uuid
      AND (
          (connector_catalog_items.provider = 'codex'
              AND connector_catalog_items.source = 'codex_app')
          OR (connector_catalog_items.provider = 'claude'
              AND connector_catalog_items.source IN ('claude_plugin', 'claude_mcp'))
      )
), updated AS (
    UPDATE connector_policies
    SET desired_enabled = sqlc.arg(desired_enabled)::boolean,
        reviewed_contract_digest = sqlc.arg(reviewed_contract_digest)::text,
        review_required = false,
        updated_at = CASE WHEN desired_enabled <> sqlc.arg(desired_enabled)::boolean
            OR review_required
            OR (sqlc.arg(desired_enabled)::boolean AND reviewed_contract_digest
                IS DISTINCT FROM sqlc.arg(reviewed_contract_digest)::text)
            THEN clock_timestamp() ELSE updated_at END,
        version = CASE WHEN desired_enabled <> sqlc.arg(desired_enabled)::boolean
            OR review_required
            OR (sqlc.arg(desired_enabled)::boolean AND reviewed_contract_digest
                IS DISTINCT FROM sqlc.arg(reviewed_contract_digest)::text)
            THEN connector_policies.version + 1 ELSE connector_policies.version END
    FROM authorized
    WHERE connector_policies.connector_id = authorized.id
      AND connector_policies.company_id = sqlc.arg(company_id)::uuid
      AND sqlc.arg(desired_enabled)::boolean
      AND connector_policies.version = sqlc.arg(expected_version)::bigint
      AND connector_policies.version < 9007199254740991
    RETURNING connector_policies.*
)
SELECT * FROM updated;

-- name: GetCompanyConnectorForOwner :one
WITH locked_company AS MATERIALIZED (
    SELECT companies.id
    FROM companies
    JOIN company_members
      ON company_members.company_id = companies.id
     AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
     AND company_members.status = 'active'
     AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
    WHERE companies.id = sqlc.arg(company_id)::uuid
      AND companies.deleting_at IS NULL
    FOR UPDATE OF companies
)
SELECT
    sqlc.embed(connector_catalog_items),
    connector_policies.desired_enabled,
    connector_policies.version AS policy_version,
    connector_policies.review_required
FROM connector_catalog_items
JOIN locked_company
  ON locked_company.id = connector_catalog_items.company_id
JOIN connector_policies
  ON connector_policies.connector_id = connector_catalog_items.id
 AND connector_policies.company_id = connector_catalog_items.company_id
WHERE connector_catalog_items.id = sqlc.arg(connector_id)::uuid
  AND connector_catalog_items.company_id = sqlc.arg(company_id)::uuid
FOR UPDATE OF connector_policies;

-- Workflow app readiness reads the Runtime of the viewer's own binding of one
-- Agent (ADR 0014), the Runtime a Run the viewer starts would execute on. A
-- viewer who has not set the Agent up has no binding, and a bound Runtime
-- that was removed is not eligible; both are reported rather than filtered,
-- so the caller can answer `unbound` or `missing` without a second read;
-- freshness uses the same window as the claim freeze. The Agent's disabled
-- Plugins come from their typed column and its disabled MCP servers from the
-- capabilities payload the daemon applies; the caller matches both, so no
-- row is filtered on jsonb.
-- name: GetAgentConnectorReadinessContext :one
SELECT
    agents.id AS agent_id,
    agents.provider,
    agents.disabled_plugin_ids,
    COALESCE(agents.settings -> 'capabilities' -> 'disabledMcpServers', '[]'::jsonb)::jsonb
        AS disabled_mcp_servers,
    viewer_binding.runtime_id AS bound_runtime_id,
    (runtimes.id IS NOT NULL)::boolean AS runtime_eligible,
    generations.generation,
    generations.observed_at AS discovery_observed_at,
    COALESCE(
        generations.observed_at >= clock_timestamp()
            - sqlc.arg(discovery_stale_after_milliseconds)::bigint * interval '1 millisecond',
        false
    )::boolean AS discovery_fresh
FROM agents
JOIN companies
  ON companies.id = agents.company_id
 AND companies.deleting_at IS NULL
JOIN company_members AS viewer_memberships
  ON viewer_memberships.company_id = agents.company_id
 AND viewer_memberships.user_id = sqlc.arg(viewer_user_id)::uuid
 AND viewer_memberships.status = 'active'
LEFT JOIN agent_runtime_bindings AS viewer_binding
  ON viewer_binding.company_id = agents.company_id
 AND viewer_binding.agent_id = agents.id
 AND viewer_binding.user_id = sqlc.arg(viewer_user_id)::uuid
LEFT JOIN runtimes
  ON runtimes.company_id = viewer_binding.company_id
 AND runtimes.id = viewer_binding.runtime_id
 AND runtimes.owner_user_id = viewer_binding.user_id
 AND runtimes.removed_at IS NULL
LEFT JOIN connector_discovery_generations AS generations
  ON generations.runtime_id = runtimes.id
 AND generations.company_id = runtimes.company_id
 AND generations.provider = runtimes.provider
WHERE agents.id = sqlc.arg(agent_id)::uuid
  AND agents.company_id = sqlc.arg(company_id)::uuid
  AND agents.archived_at IS NULL;

-- Resolve exact pinned identities on one Runtime's current generation.
-- `eligible` restates FreezeRunConnectorSnapshot's per-Connector rule for an
-- all-mode Run without its freshness join, which the caller applies from the
-- generation. The caller counts tools from `tool_namespace` and restates the
-- daemon's per-Agent capability denial. A missing identity yields no row.
-- name: ListConnectorAppReadiness :many
WITH requested AS MATERIALIZED (
    SELECT DISTINCT source_item.source, native_item.native_id
    -- Two arrays built from one slice: equal length, paired by position.
    FROM unnest(sqlc.arg(sources)::text[]) WITH ORDINALITY AS source_item(source, ordinality)
    JOIN unnest(sqlc.arg(native_ids)::text[]) WITH ORDINALITY AS native_item(native_id, ordinality)
      ON native_item.ordinality = source_item.ordinality
)
SELECT
    catalog.id,
    catalog.source,
    catalog.native_id,
    COALESCE(observations.name, catalog.name)::text AS name,
    (observations.connector_id IS NOT NULL)::boolean AS observed,
    COALESCE(observations.status, 'unavailable')::text AS status,
    COALESCE(observations.action, 'none')::text AS action,
    COALESCE(observations.can_execute, false)::boolean AS can_execute,
    COALESCE(observations.tool_namespace, '[]'::jsonb)::jsonb AS tool_namespace,
    COALESCE(observations.icon_url, catalog.icon_url) AS icon_url,
    policies.review_required,
    COALESCE(
        policies.desired_enabled
        AND NOT policies.review_required
        AND policies.reviewed_contract_digest = observations.contract_digest
        AND catalog.status = 'connected'
        AND catalog.can_execute
        AND catalog.contract_digest = observations.contract_digest
        AND catalog.tool_namespace = observations.tool_namespace
        AND observations.status = 'connected'
        AND observations.can_execute
        AND CASE catalog.provider
            WHEN 'codex' THEN catalog.source = 'codex_app'
            WHEN 'claude' THEN catalog.source IN ('claude_plugin', 'claude_mcp')
            ELSE false
        END,
        false
    )::boolean AS eligible
FROM requested
JOIN connector_catalog_items AS catalog
  ON catalog.company_id = sqlc.arg(company_id)::uuid
 AND catalog.provider = sqlc.arg(provider)::text
 AND catalog.source = requested.source
 AND catalog.native_id = requested.native_id
JOIN connector_policies AS policies
  ON policies.connector_id = catalog.id
 AND policies.company_id = catalog.company_id
LEFT JOIN connector_runtime_observations AS observations
  ON observations.runtime_id = sqlc.arg(runtime_id)::uuid
 AND observations.connector_id = catalog.id
 AND observations.company_id = catalog.company_id
 AND observations.provider = catalog.provider
 AND observations.generation = sqlc.arg(generation)::uuid
ORDER BY catalog.source, catalog.native_id;
