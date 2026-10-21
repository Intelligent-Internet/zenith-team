-- The keys that match one Codex Plugin to another. Codex lists the
-- Claude marketplace's copies of many curated Plugins without a logo or
-- description, so the Plugins list leaves out such a copy, and a Plugin
-- without a loadable logo borrows one by name, then by website host, then by
-- developer. Presentation only: nothing here takes part in authority or the
-- Run claim.
--
-- name_key is the name without case, spaces or punctuation, so
-- "adobe-for-creativity" and "Adobe for Creativity" meet; letters and digits
-- of every script stay. website_host is the host of the developer's website
-- without a leading "www." (stored links are https without credentials,
-- validPluginLinkURL). Both are stored once per observation, so a read
-- compares plain columns instead of running regular expressions over a whole
-- catalog, which the planner prices as nearly free. ListCompanyCodexIconsByName
-- and ApplyPluginDiscoveryGeneration key names with the same expression.
--
-- curated_duplicate marks a Plugin the list leaves out: a Codex Plugin of a
-- marketplace outside OpenAI's (the native id after its last '@' does not
-- start with "openai-"), not installed, whose Runtime observes in the same
-- generation an openai-curated-remote Plugin with the same name key that is
-- installed or can be installed, when something besides the name agrees: the
-- slugs (the native ids before their last '@') keyed like names, a curated
-- slug that is an opaque app id, the developers keyed like names, or the
-- website hosts other than a code hosting or package site's. So a copy stays
-- listed when its twin cannot be installed, and a different product that
-- only shares a name ("Data") is not hidden. ApplyPluginDiscoveryGeneration
-- decides it the same way for every generation it writes.
ALTER TABLE plugin_runtime_observations
    ADD COLUMN name_key text NOT NULL GENERATED ALWAYS AS (
        regexp_replace(lower(name), '[[:space:][:punct:]]+', '', 'g')
    ) STORED,
    ADD COLUMN website_host text GENERATED ALWAYS AS (
        regexp_replace(
            substring(lower(website_url) FROM '^[a-z][a-z0-9+.-]*://([^/?#:]+)'),
            '^www\.', ''
        )
    ) STORED,
    ADD COLUMN curated_duplicate boolean NOT NULL DEFAULT false;

UPDATE plugin_runtime_observations AS observations
SET curated_duplicate = true
FROM plugin_catalog_items AS plugins
WHERE plugins.id = observations.plugin_id
  AND observations.provider = 'codex'
  AND NOT observations.installed
  AND strpos(plugins.native_id, '@') > 0
  AND split_part(plugins.native_id, '@', -1) NOT LIKE 'openai-%'
  AND observations.name_key <> ''
  AND EXISTS (
      SELECT 1
      FROM plugin_runtime_observations AS curated
      JOIN plugin_catalog_items AS curated_plugins ON curated_plugins.id = curated.plugin_id
      WHERE curated.runtime_id = observations.runtime_id
        AND curated.generation = observations.generation
        AND curated.name_key = observations.name_key
        AND curated_plugins.native_id LIKE '%@openai-curated-remote'
        AND (curated.installed OR curated.action = 'install')
        AND (
            regexp_replace(
                lower(regexp_replace(curated_plugins.native_id, '@[^@]*$', '')),
                '[[:space:][:punct:]]+', '', 'g'
            ) = regexp_replace(
                lower(regexp_replace(plugins.native_id, '@[^@]*$', '')),
                '[[:space:][:punct:]]+', '', 'g'
            )
            OR curated_plugins.native_id ~ '^app-[0-9a-f]+@openai-curated-remote$'
            OR regexp_replace(lower(curated.developer_name), '[[:space:][:punct:]]+', '', 'g')
                = nullif(
                    regexp_replace(lower(observations.developer_name), '[[:space:][:punct:]]+', '', 'g'),
                    ''
                )
            OR (
                curated.website_host = observations.website_host
                AND observations.website_host <> ALL (ARRAY[
                    'github.com', 'gitlab.com', 'bitbucket.org', 'npmjs.com', 'pypi.org',
                    'huggingface.co', 'raw.githubusercontent.com', 'gist.github.com'
                ])
            )
        )
  );

-- Logo borrowing looks lenders up by each key; only rows with a logo lend.
DROP INDEX plugin_runtime_observations_company_provider_name_idx;
CREATE INDEX plugin_runtime_observations_company_provider_name_key_idx
    ON plugin_runtime_observations (company_id, provider, name_key)
    WHERE icon_url IS NOT NULL;

CREATE INDEX plugin_runtime_observations_company_provider_website_host_idx
    ON plugin_runtime_observations (company_id, provider, website_host)
    WHERE icon_url IS NOT NULL;

CREATE INDEX plugin_runtime_observations_company_provider_developer_idx
    ON plugin_runtime_observations (company_id, provider, lower(developer_name))
    WHERE icon_url IS NOT NULL;
