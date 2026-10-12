-- Plugin discovery is a complete presentation-only generation published by
-- one authenticated Runtime. Connector discovery remains the sole source of
-- executable authority.

-- name: GetPluginDiscoveryGenerationForUpdate :one
SELECT *
FROM plugin_discovery_generations
WHERE runtime_id = sqlc.arg(runtime_id)::uuid
FOR UPDATE;

-- name: ApplyPluginDiscoveryGeneration :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
), input_items AS MATERIALIZED (
    SELECT
        input."nativeId"::text AS native_id,
        input.name::text,
        input.description::text,
        input.category::text,
        input."iconUrl"::text AS icon_url,
        input."shareUrl"::text AS share_url,
        input.status::text,
        input.installed::boolean,
        input.enabled::boolean,
        input.featured::boolean,
        input.action::text,
        input."catalogRank"::integer AS catalog_rank,
        input."appCount"::integer AS app_count,
        input."mcpServerCount"::integer AS mcp_server_count,
        input."skillCount"::integer AS skill_count,
        input."connectorIdentities"::jsonb AS connector_identities,
        input."installCount"::bigint AS install_count,
        input."longDescription"::text AS long_description,
        input."defaultPrompts"::jsonb AS default_prompts,
        input."brandColor"::text AS brand_color,
        input."developerName"::text AS developer_name,
        input."websiteUrl"::text AS website_url,
        input."privacyPolicyUrl"::text AS privacy_policy_url,
        input."termsOfServiceUrl"::text AS terms_of_service_url
    FROM jsonb_to_recordset(sqlc.arg(items)::jsonb) AS input(
        "nativeId" text,
        name text,
        description text,
        category text,
        "iconUrl" text,
        "shareUrl" text,
        status text,
        installed boolean,
        enabled boolean,
        featured boolean,
        action text,
        "catalogRank" integer,
        "appCount" integer,
        "mcpServerCount" integer,
        "skillCount" integer,
        "connectorIdentities" jsonb,
        "installCount" bigint,
        "longDescription" text,
        "defaultPrompts" jsonb,
        "brandColor" text,
        "developerName" text,
        "websiteUrl" text,
        "privacyPolicyUrl" text,
        "termsOfServiceUrl" text
    )
), keyed_items AS MATERIALIZED (
    -- Each Codex Plugin's name key, as plugin_runtime_observations.name_key
    -- stores it.
    SELECT
        input_items.native_id,
        regexp_replace(lower(input_items.name), '[[:space:][:punct:]]+', '', 'g') AS name_key
    FROM input_items
    WHERE sqlc.arg(provider)::text = 'codex'
), curated_duplicates AS MATERIALIZED (
    -- The catalog leaves out (D26) a Codex Plugin of a marketplace outside
    -- OpenAI's (the native id after its last '@' does not start with
    -- "openai-"), not installed, that shares its name key with a curated
    -- Plugin of this generation that is installed or can be installed, when
    -- something besides the name agrees: the slugs (native ids before their
    -- last '@') keyed like names; a curated slug that is an opaque app id,
    -- which says nothing; the developers keyed like names; or the website
    -- hosts, unless a code hosting or package site's. The keys are computed
    -- as plugin_runtime_observations stores name_key and website_host, and as
    -- migration 000063's backfill computes them.
    SELECT DISTINCT copies.native_id
    FROM keyed_items AS copy_keys
    JOIN keyed_items AS curated_keys
      ON curated_keys.name_key = copy_keys.name_key
    JOIN input_items AS copies
      ON copies.native_id = copy_keys.native_id
    JOIN input_items AS curated
      ON curated.native_id = curated_keys.native_id
    CROSS JOIN LATERAL (
        SELECT
            regexp_replace(
                lower(regexp_replace(copies.native_id, '@[^@]*$', '')),
                '[[:space:][:punct:]]+', '', 'g'
            ) AS copy_slug,
            regexp_replace(
                lower(regexp_replace(curated.native_id, '@[^@]*$', '')),
                '[[:space:][:punct:]]+', '', 'g'
            ) AS curated_slug,
            nullif(
                regexp_replace(lower(copies.developer_name), '[[:space:][:punct:]]+', '', 'g'), ''
            ) AS copy_developer,
            regexp_replace(lower(curated.developer_name), '[[:space:][:punct:]]+', '', 'g')
                AS curated_developer,
            regexp_replace(
                substring(lower(copies.website_url) FROM '^[a-z][a-z0-9+.-]*://([^/?#:]+)'),
                '^www\.', ''
            ) AS copy_host,
            regexp_replace(
                substring(lower(curated.website_url) FROM '^[a-z][a-z0-9+.-]*://([^/?#:]+)'),
                '^www\.', ''
            ) AS curated_host
    ) AS keys
    WHERE curated.native_id LIKE '%@openai-curated-remote'
      AND (curated.installed OR curated.action = 'install')
      AND NOT copies.installed
      AND strpos(copies.native_id, '@') > 0
      AND split_part(copies.native_id, '@', -1) NOT LIKE 'openai-%'
      AND copy_keys.name_key <> ''
      AND (
          keys.curated_slug = keys.copy_slug
          OR curated.native_id ~ '^app-[0-9a-f]+@openai-curated-remote$'
          OR keys.curated_developer = keys.copy_developer
          OR (
              keys.curated_host = keys.copy_host
              AND keys.copy_host <> ALL (ARRAY[
                  'github.com', 'gitlab.com', 'bitbucket.org', 'npmjs.com', 'pypi.org',
                  'huggingface.co', 'raw.githubusercontent.com', 'gist.github.com'
              ])
          )
      )
), inserted_catalog AS (
    INSERT INTO plugin_catalog_items (
        company_id, provider, native_id, created_at, updated_at
    )
    SELECT
        sqlc.arg(company_id)::uuid,
        sqlc.arg(provider)::text,
        input_items.native_id,
        statement_clock.now_at,
        statement_clock.now_at
    FROM input_items
    CROSS JOIN statement_clock
    ON CONFLICT (company_id, provider, native_id) DO NOTHING
    RETURNING id, company_id, provider, native_id
), generation_catalog AS MATERIALIZED (
    SELECT id, company_id, provider, native_id
    FROM inserted_catalog
    UNION ALL
    SELECT
        plugin_catalog_items.id,
        plugin_catalog_items.company_id,
        plugin_catalog_items.provider,
        plugin_catalog_items.native_id
    FROM plugin_catalog_items
    JOIN input_items
      ON input_items.native_id = plugin_catalog_items.native_id
    WHERE plugin_catalog_items.company_id = sqlc.arg(company_id)::uuid
      AND plugin_catalog_items.provider = sqlc.arg(provider)::text
      AND NOT EXISTS (
          SELECT 1
          FROM inserted_catalog
          WHERE inserted_catalog.id = plugin_catalog_items.id
      )
), observation_upserts AS (
    INSERT INTO plugin_runtime_observations (
        runtime_id, plugin_id, company_id, provider, generation, name,
        description, category, icon_url, share_url, status, installed, enabled,
        featured, action, catalog_rank, app_count, mcp_server_count, skill_count,
        connector_identities, install_count, long_description, default_prompts,
        brand_color, developer_name, website_url, privacy_policy_url,
        terms_of_service_url, curated_duplicate, observed_at
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
        input_items.share_url,
        input_items.status,
        input_items.installed,
        input_items.enabled,
        input_items.featured,
        input_items.action,
        input_items.catalog_rank,
        input_items.app_count,
        input_items.mcp_server_count,
        input_items.skill_count,
        input_items.connector_identities,
        input_items.install_count,
        input_items.long_description,
        input_items.default_prompts,
        input_items.brand_color,
        input_items.developer_name,
        input_items.website_url,
        input_items.privacy_policy_url,
        input_items.terms_of_service_url,
        input_items.native_id IN (SELECT curated_duplicates.native_id FROM curated_duplicates),
        sqlc.arg(observed_at)::timestamptz
    FROM generation_catalog
    JOIN input_items
      ON input_items.native_id = generation_catalog.native_id
    ON CONFLICT (runtime_id, plugin_id) DO UPDATE SET
        company_id = EXCLUDED.company_id,
        provider = EXCLUDED.provider,
        generation = EXCLUDED.generation,
        name = EXCLUDED.name,
        description = EXCLUDED.description,
        category = EXCLUDED.category,
        icon_url = EXCLUDED.icon_url,
        share_url = EXCLUDED.share_url,
        status = EXCLUDED.status,
        installed = EXCLUDED.installed,
        enabled = EXCLUDED.enabled,
        featured = EXCLUDED.featured,
        action = EXCLUDED.action,
        catalog_rank = EXCLUDED.catalog_rank,
        app_count = EXCLUDED.app_count,
        mcp_server_count = EXCLUDED.mcp_server_count,
        skill_count = EXCLUDED.skill_count,
        connector_identities = EXCLUDED.connector_identities,
        install_count = EXCLUDED.install_count,
        long_description = EXCLUDED.long_description,
        default_prompts = EXCLUDED.default_prompts,
        brand_color = EXCLUDED.brand_color,
        developer_name = EXCLUDED.developer_name,
        website_url = EXCLUDED.website_url,
        privacy_policy_url = EXCLUDED.privacy_policy_url,
        terms_of_service_url = EXCLUDED.terms_of_service_url,
        curated_duplicate = EXCLUDED.curated_duplicate,
        observed_at = EXCLUDED.observed_at,
        updated_at = (SELECT now_at FROM statement_clock)
    RETURNING plugin_id
), removed_observations AS (
    DELETE FROM plugin_runtime_observations
    WHERE plugin_runtime_observations.runtime_id = sqlc.arg(runtime_id)::uuid
      AND NOT EXISTS (
          SELECT 1
          FROM generation_catalog
          WHERE generation_catalog.id = plugin_runtime_observations.plugin_id
      )
    RETURNING plugin_id
), generation_upsert AS (
    INSERT INTO plugin_discovery_generations (
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
    (SELECT count(*) FROM observation_upserts)::bigint AS observation_count,
    (SELECT count(*) FROM removed_observations)::bigint AS removed_count,
    (SELECT count(*) FROM generation_upsert)::bigint AS generation_count;

-- name: DeletePluginDiscoveryForRuntime :exec
DELETE FROM plugin_runtime_observations
WHERE runtime_id = sqlc.arg(runtime_id)::uuid;

-- name: DeletePluginDiscoveryGenerationForRuntime :exec
DELETE FROM plugin_discovery_generations
WHERE runtime_id = sqlc.arg(runtime_id)::uuid;

-- name: GetCompanyPluginContextForOwner :one
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
    )::boolean AS stale,
    generations.observed_at AS discovery_observed_at
FROM selected_runtime
LEFT JOIN plugin_discovery_generations AS generations
  ON generations.runtime_id = selected_runtime.id
 AND generations.provider = selected_runtime.provider;
-- name: ListCompanyPluginCatalog :many
SELECT
    plugin_catalog_items.id,
    plugin_catalog_items.native_id,
    observations.name,
    observations.description,
    observations.category,
    observations.icon_url,
    observations.share_url,
    observations.status,
    observations.installed,
    observations.enabled,
    observations.featured,
    observations.action,
    observations.catalog_rank,
    observations.app_count,
    observations.mcp_server_count,
    observations.skill_count,
    observations.connector_identities,
    observations.install_count,
    observations.observed_at,
    observations.developer_name,
    observations.website_host
FROM plugin_runtime_observations AS observations
JOIN plugin_discovery_generations AS generations
  ON generations.runtime_id = observations.runtime_id
 AND generations.company_id = observations.company_id
 AND generations.provider = observations.provider
 AND generations.generation = observations.generation
JOIN plugin_catalog_items
  ON plugin_catalog_items.id = observations.plugin_id
 AND plugin_catalog_items.company_id = observations.company_id
 AND plugin_catalog_items.provider = observations.provider
JOIN companies
  ON companies.id = observations.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
  ON company_members.company_id = observations.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
 AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE observations.runtime_id = sqlc.narg(selected_runtime_id)::uuid
  AND observations.company_id = sqlc.arg(company_id)::uuid
  AND observations.provider = sqlc.arg(provider)::text
  -- An uninstalled copy of a curated Plugin from another marketplace (D26);
  -- getPlugin still reads it by id.
  AND NOT observations.curated_duplicate
  AND (
      sqlc.arg(search_query)::text = ''
      OR observations.name ILIKE '%' || replace(replace(replace(
          sqlc.arg(search_query)::text, '\', '\\'
      ), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
      OR observations.description ILIKE '%' || replace(replace(replace(
          sqlc.arg(search_query)::text, '\', '\\'
      ), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
  )
  AND (
      sqlc.arg(category)::text = ''
      OR observations.category = sqlc.arg(category)::text
  )
  AND (
      sqlc.arg(status)::text = ''
      OR (sqlc.arg(status)::text = 'installed' AND observations.installed)
      OR (
          sqlc.arg(status)::text <> 'installed'
          AND observations.status = sqlc.arg(status)::text
      )
  )
  AND (NOT sqlc.arg(featured_only)::boolean OR observations.featured)
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (observations.catalog_rank, plugin_catalog_items.id) > (
          sqlc.arg(after_rank)::integer,
          sqlc.arg(after_id)::uuid
      )
  )
ORDER BY observations.catalog_rank, plugin_catalog_items.id
LIMIT sqlc.arg(page_size)::integer;

-- The catalog's size and its rows by category leave out exactly the rows
-- ListCompanyPluginCatalog leaves out.
-- name: CountCompanyPluginCatalog :one
SELECT count(*)::bigint
FROM plugin_runtime_observations AS observations
JOIN plugin_discovery_generations AS generations
  ON generations.runtime_id = observations.runtime_id
 AND generations.company_id = observations.company_id
 AND generations.provider = observations.provider
 AND generations.generation = observations.generation
JOIN companies
  ON companies.id = observations.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
  ON company_members.company_id = observations.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
 AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE observations.runtime_id = sqlc.narg(selected_runtime_id)::uuid
  AND observations.company_id = sqlc.arg(company_id)::uuid
  AND observations.provider = sqlc.arg(provider)::text
  AND NOT observations.curated_duplicate;

-- A curated duplicate is never installed, so every installed Plugin counts.
-- name: CountCompanyInstalledPlugins :one
SELECT count(*)::bigint
FROM plugin_runtime_observations AS observations
JOIN plugin_discovery_generations AS generations
  ON generations.runtime_id = observations.runtime_id
 AND generations.company_id = observations.company_id
 AND generations.provider = observations.provider
 AND generations.generation = observations.generation
JOIN companies
  ON companies.id = observations.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
  ON company_members.company_id = observations.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
 AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE observations.runtime_id = sqlc.narg(selected_runtime_id)::uuid
  AND observations.company_id = sqlc.arg(company_id)::uuid
  AND observations.provider = sqlc.arg(provider)::text
  AND observations.installed;

-- name: CountCompanyPluginsByCategory :many
SELECT observations.category, count(*)::bigint AS count
FROM plugin_runtime_observations AS observations
JOIN plugin_discovery_generations AS generations
  ON generations.runtime_id = observations.runtime_id
 AND generations.company_id = observations.company_id
 AND generations.provider = observations.provider
 AND generations.generation = observations.generation
JOIN companies
  ON companies.id = observations.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
  ON company_members.company_id = observations.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
 AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE observations.runtime_id = sqlc.narg(selected_runtime_id)::uuid
  AND observations.company_id = sqlc.arg(company_id)::uuid
  AND observations.provider = sqlc.arg(provider)::text
  AND NOT observations.curated_duplicate
GROUP BY observations.category
ORDER BY observations.category;

-- The provider of one Company Plugin, so its page selects a Runtime of that
-- provider exactly as the catalog does when asked for it.
-- name: GetCompanyPluginProvider :one
SELECT provider
FROM plugin_catalog_items
WHERE id = sqlc.arg(plugin_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid;

-- One Plugin as the selected Runtime's current generation observed it, for
-- its page, behind the catalog's owner gate.
-- name: GetCompanyPluginDetailForOwner :one
SELECT
    plugin_catalog_items.native_id AS plugin_native_id,
    sqlc.embed(plugin_runtime_observations)
FROM plugin_catalog_items
JOIN plugin_runtime_observations
  ON plugin_runtime_observations.plugin_id = plugin_catalog_items.id
 AND plugin_runtime_observations.runtime_id = sqlc.arg(selected_runtime_id)::uuid
 AND plugin_runtime_observations.company_id = plugin_catalog_items.company_id
 AND plugin_runtime_observations.provider = plugin_catalog_items.provider
JOIN plugin_discovery_generations AS generations
  ON generations.runtime_id = plugin_runtime_observations.runtime_id
 AND generations.company_id = plugin_runtime_observations.company_id
 AND generations.provider = plugin_runtime_observations.provider
 AND generations.generation = plugin_runtime_observations.generation
JOIN companies
  ON companies.id = plugin_catalog_items.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
  ON company_members.company_id = plugin_catalog_items.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
 AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE plugin_catalog_items.id = sqlc.arg(plugin_id)::uuid
  AND plugin_catalog_items.company_id = sqlc.arg(company_id)::uuid
  AND plugin_catalog_items.provider = sqlc.arg(provider)::text;

-- The apps one Plugin bundles, resolved in the caller's order against the
-- selected Runtime's current Connector generation. An identity that Runtime did
-- not observe keeps the Company catalog's presentation without a Connector id
-- or state; one no Runtime ever reported keeps only its identity.
-- Presentation only: the Run claim alone decides what executes.
-- name: ListPluginAppsForRuntime :many
WITH requested AS MATERIALIZED (
    SELECT
        (source_item.ordinality - 1)::bigint AS request_position,
        source_item.source,
        native_item.native_id
    -- Two arrays built from one slice: equal length, paired by position.
    FROM unnest(sqlc.arg(sources)::text[]) WITH ORDINALITY AS source_item(source, ordinality)
    JOIN unnest(sqlc.arg(native_ids)::text[]) WITH ORDINALITY AS native_item(native_id, ordinality)
      ON native_item.ordinality = source_item.ordinality
)
SELECT
    requested.request_position,
    requested.source::text AS source,
    requested.native_id::text AS native_id,
    (observations.connector_id IS NOT NULL)::boolean AS observed,
    (catalog.id IS NOT NULL)::boolean AS cataloged,
    observations.connector_id,
    COALESCE(observations.name, catalog.name, requested.native_id)::text AS name,
    COALESCE(observations.description, catalog.description) AS description,
    COALESCE(observations.icon_url, catalog.icon_url) AS icon_url,
    COALESCE(observations.status, '')::text AS status,
    COALESCE(observations.action, '')::text AS action
FROM requested
LEFT JOIN connector_catalog_items AS catalog
  ON catalog.company_id = sqlc.arg(company_id)::uuid
 AND catalog.provider = sqlc.arg(provider)::text
 AND catalog.source = requested.source
 AND catalog.native_id = requested.native_id
LEFT JOIN connector_discovery_generations AS generations
  ON generations.runtime_id = sqlc.arg(runtime_id)::uuid
 AND generations.company_id = catalog.company_id
 AND generations.provider = catalog.provider
LEFT JOIN connector_runtime_observations AS observations
  ON observations.runtime_id = generations.runtime_id
 AND observations.connector_id = catalog.id
 AND observations.company_id = catalog.company_id
 AND observations.provider = catalog.provider
 AND observations.generation = generations.generation
ORDER BY requested.request_position;

-- The unarchived Agents the viewer runs on one of their own Runtimes (their
-- bindings, ADR 0014) that can use one Plugin: the Runtime observes it
-- installed and enabled, and the Agent has not turned it off, the same
-- denial the daemon applies to the Agent's Runs.
-- name: ListPluginAgentUsesForRuntime :many
SELECT agents.id, agents.name
FROM agent_runtime_bindings AS bindings
JOIN agents
  ON agents.company_id = bindings.company_id
 AND agents.id = bindings.agent_id
JOIN plugin_runtime_observations AS observations
  ON observations.runtime_id = bindings.runtime_id
 AND observations.company_id = bindings.company_id
 AND observations.plugin_id = sqlc.arg(plugin_id)::uuid
 AND observations.installed
 AND observations.enabled
WHERE bindings.company_id = sqlc.arg(company_id)::uuid
  AND bindings.runtime_id = sqlc.arg(runtime_id)::uuid
  AND bindings.user_id = sqlc.arg(viewer_user_id)::uuid
  AND agents.archived_at IS NULL
  AND NOT (sqlc.arg(plugin_id)::uuid = ANY(agents.disabled_plugin_ids))
ORDER BY lower(agents.name), agents.id;

-- A Plugin without a loadable logo (every Claude Plugin reports none) borrows
-- first by name (D26): the logo of a Codex Plugin with the same name key
-- (case, spaces and punctuation ignored), else of a Codex app
-- with the same case-insensitive name, that a Runtime of the same Company
-- observes; a Codex app whose own logo loads only in a ChatGPT session borrows
-- the same way. Only the viewer's own Runtimes (ADR 0014) lend a logo, so
-- another member's computer never shows through a Workflow's app readiness
-- or a catalog. Every distinct logo comes back, Plugin logos first
-- and each kind newest first, so the caller can skip one a client cannot load.
-- Each name comes back as sent; names without a logo are absent.
-- name: ListCompanyCodexIconsByName :many
WITH requested AS MATERIALIZED (
    SELECT DISTINCT
        requested.name,
        -- As plugin_runtime_observations.name_key stores it.
        regexp_replace(lower(requested.name), '[[:space:][:punct:]]+', '', 'g') AS name_key
    FROM unnest(sqlc.arg(names)::text[]) AS requested(name)
),
candidates AS (
    SELECT
        requested.name,
        0 AS preference,
        observations.icon_url,
        observations.developer_name,
        observations.observed_at
    FROM requested
    JOIN plugin_runtime_observations AS observations
      ON observations.company_id = sqlc.arg(company_id)::uuid
     AND observations.provider = 'codex'
     AND observations.name_key = requested.name_key
     AND observations.icon_url IS NOT NULL
    JOIN runtimes
      ON runtimes.id = observations.runtime_id
     AND runtimes.company_id = observations.company_id
     AND runtimes.owner_user_id = sqlc.arg(viewer_user_id)::uuid
    -- An all-punctuation name keys to nothing and matches nothing.
    WHERE requested.name_key <> ''
    UNION ALL
    SELECT
        requested.name,
        1 AS preference,
        observations.icon_url,
        NULL::text AS developer_name,
        observations.observed_at
    FROM requested
    JOIN connector_catalog_items AS catalog
      ON catalog.company_id = sqlc.arg(company_id)::uuid
     AND catalog.provider = 'codex'
     AND catalog.source = 'codex_app'
     AND lower(catalog.name) = lower(requested.name)
    JOIN connector_runtime_observations AS observations
      ON observations.connector_id = catalog.id
     AND observations.company_id = catalog.company_id
     AND observations.provider = catalog.provider
     AND lower(observations.name) = lower(requested.name)
     AND observations.icon_url IS NOT NULL
    JOIN runtimes
      ON runtimes.id = observations.runtime_id
     AND runtimes.company_id = observations.company_id
     AND runtimes.owner_user_id = sqlc.arg(viewer_user_id)::uuid
)
SELECT
    ranked.name::text AS name,
    ranked.icon_url::text AS icon_url,
    -- The lending Plugin's developer, so a borrower from another developer
    -- ("data" by Astronomer) does not take this logo ("Data" by OpenAI).
    ranked.developer_name
FROM (
    SELECT DISTINCT ON (candidates.name, candidates.icon_url)
        candidates.name,
        candidates.preference,
        candidates.icon_url,
        candidates.developer_name,
        candidates.observed_at
    FROM candidates
    ORDER BY candidates.name, candidates.icon_url, candidates.preference, candidates.observed_at DESC
) AS ranked
ORDER BY ranked.name, ranked.preference, ranked.observed_at DESC, ranked.icon_url;

-- A Plugin that borrows no logo by name borrows by its publisher (D26): the
-- logo of a Codex Plugin whose website has the same host (website_host,
-- without "www."), else of one with the same case-insensitive developer, on
-- one of the viewer's own Runtimes (ADR 0014). The caller lends
-- only when exactly one Plugin with a loadable logo matches and its name
-- agrees with the borrower's, so every distinct logo of every matching Plugin
-- comes back with its name, each Plugin's newest first. Code hosting and
-- package sites publish every developer's Plugins, so their hosts match
-- nothing. Each host and developer comes back as sent.
-- name: ListCompanyCodexPluginLogosByPublisher :many
WITH requested_hosts AS MATERIALIZED (
    SELECT DISTINCT requested.host
    FROM unnest(sqlc.arg(website_hosts)::text[]) AS requested(host)
),
requested_developers AS MATERIALIZED (
    SELECT DISTINCT requested.developer_name
    FROM unnest(sqlc.arg(developer_names)::text[]) AS requested(developer_name)
),
candidates AS (
    SELECT
        true AS by_website,
        requested_hosts.host AS requested,
        observations.plugin_id,
        observations.name,
        observations.icon_url,
        observations.observed_at
    FROM requested_hosts
    JOIN plugin_runtime_observations AS observations
      ON observations.company_id = sqlc.arg(company_id)::uuid
     AND observations.provider = 'codex'
     AND observations.website_host = requested_hosts.host
     AND observations.icon_url IS NOT NULL
    JOIN runtimes
      ON runtimes.id = observations.runtime_id
     AND runtimes.company_id = observations.company_id
     AND runtimes.owner_user_id = sqlc.arg(viewer_user_id)::uuid
    WHERE requested_hosts.host <> ALL (ARRAY[
        'github.com', 'gitlab.com', 'bitbucket.org', 'npmjs.com', 'pypi.org',
        'huggingface.co', 'raw.githubusercontent.com', 'gist.github.com'
    ])
    UNION ALL
    SELECT
        false AS by_website,
        requested_developers.developer_name AS requested,
        observations.plugin_id,
        observations.name,
        observations.icon_url,
        observations.observed_at
    FROM requested_developers
    JOIN plugin_runtime_observations AS observations
      ON observations.company_id = sqlc.arg(company_id)::uuid
     AND observations.provider = 'codex'
     AND lower(observations.developer_name) = lower(requested_developers.developer_name)
     AND observations.icon_url IS NOT NULL
    JOIN runtimes
      ON runtimes.id = observations.runtime_id
     AND runtimes.company_id = observations.company_id
     AND runtimes.owner_user_id = sqlc.arg(viewer_user_id)::uuid
)
SELECT
    ranked.by_website::boolean AS by_website,
    ranked.requested::text AS requested,
    ranked.plugin_id,
    ranked.name,
    ranked.icon_url::text AS icon_url
FROM (
    SELECT DISTINCT ON (
        candidates.by_website, candidates.requested, candidates.plugin_id, candidates.icon_url
    )
        candidates.by_website,
        candidates.requested,
        candidates.plugin_id,
        candidates.name,
        candidates.icon_url,
        candidates.observed_at
    FROM candidates
    ORDER BY
        candidates.by_website, candidates.requested, candidates.plugin_id, candidates.icon_url,
        candidates.observed_at DESC
) AS ranked
ORDER BY ranked.by_website, ranked.requested, ranked.plugin_id, ranked.observed_at DESC, ranked.icon_url;

-- The Plugins one Runtime observes installed in its current generation, with
-- a logo and the Connector identities each bundles. A Codex app whose own logo
-- loads only in a ChatGPT session shows the logo of a Plugin that bundles it
-- (D26); the caller matches the identities.
-- name: ListInstalledPluginLogosForRuntime :many
SELECT
    observations.name,
    observations.icon_url::text AS icon_url,
    observations.connector_identities
FROM plugin_runtime_observations AS observations
JOIN plugin_discovery_generations AS generations
  ON generations.runtime_id = observations.runtime_id
 AND generations.company_id = observations.company_id
 AND generations.provider = observations.provider
 AND generations.generation = observations.generation
WHERE observations.company_id = sqlc.arg(company_id)::uuid
  AND observations.provider = 'codex'
  AND observations.runtime_id = sqlc.arg(runtime_id)::uuid
  AND observations.installed
  AND observations.icon_url IS NOT NULL
ORDER BY lower(observations.name), observations.plugin_id;

-- name: GetSelectedCompanyPluginForOwner :one
SELECT observations.action, observations.status, observations.installed
FROM plugin_catalog_items
JOIN plugin_runtime_observations AS observations
  ON observations.plugin_id = plugin_catalog_items.id
 AND observations.runtime_id = sqlc.arg(selected_runtime_id)::uuid
 AND observations.company_id = plugin_catalog_items.company_id
 AND observations.provider = plugin_catalog_items.provider
JOIN plugin_discovery_generations AS generations
  ON generations.runtime_id = observations.runtime_id
 AND generations.company_id = observations.company_id
 AND generations.provider = observations.provider
 AND generations.generation = observations.generation
JOIN companies
  ON companies.id = plugin_catalog_items.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
  ON company_members.company_id = plugin_catalog_items.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
 AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE plugin_catalog_items.id = sqlc.arg(plugin_id)::uuid
  AND plugin_catalog_items.company_id = sqlc.arg(company_id)::uuid
  AND plugin_catalog_items.provider = sqlc.arg(provider)::text;

-- Pinned Plugins observed in one Runtime's current generation, for Workflow
-- app readiness. Plugin state grants no execution authority; the caller uses
-- an installed Plugin's Connector identities as extra exact candidates and an
-- installable one as a connect path.
-- name: ListPluginsForRuntimeByNativeID :many
SELECT
    plugins.id,
    plugins.native_id,
    observations.name,
    observations.installed,
    observations.action,
    observations.connector_identities,
    observations.icon_url
FROM plugin_catalog_items AS plugins
JOIN plugin_runtime_observations AS observations
  ON observations.plugin_id = plugins.id
 AND observations.company_id = plugins.company_id
 AND observations.runtime_id = sqlc.arg(runtime_id)::uuid
JOIN plugin_discovery_generations AS generations
  ON generations.runtime_id = observations.runtime_id
 AND generations.company_id = observations.company_id
 AND generations.provider = observations.provider
 AND generations.generation = observations.generation
WHERE plugins.company_id = sqlc.arg(company_id)::uuid
  AND plugins.provider = sqlc.arg(provider)::text
  AND plugins.native_id = ANY(sqlc.arg(native_ids)::text[])
ORDER BY plugins.native_id;

-- The Plugins an Agent turned off, as they resolve on one Runtime, for Workflow
-- readiness to restate the daemon's capability denial: a disabled Plugin
-- denies its own claude_plugin Connector and every Connector identity it
-- declares there.
-- name: ListDisabledPluginIdentitiesForRuntime :many
SELECT
    plugins.native_id,
    COALESCE(observations.connector_identities, '[]'::jsonb)::jsonb AS connector_identities
FROM plugin_catalog_items AS plugins
LEFT JOIN plugin_runtime_observations AS observations
  ON observations.plugin_id = plugins.id
 AND observations.company_id = plugins.company_id
 AND observations.runtime_id = sqlc.arg(runtime_id)::uuid
WHERE plugins.company_id = sqlc.arg(company_id)::uuid
  AND plugins.id = ANY(sqlc.arg(plugin_ids)::uuid[])
ORDER BY plugins.native_id;
