DROP INDEX IF EXISTS plugin_runtime_observations_company_provider_developer_idx;
DROP INDEX IF EXISTS plugin_runtime_observations_company_provider_website_host_idx;
DROP INDEX IF EXISTS plugin_runtime_observations_company_provider_name_key_idx;
ALTER TABLE plugin_runtime_observations
    DROP COLUMN IF EXISTS curated_duplicate,
    DROP COLUMN IF EXISTS website_host,
    DROP COLUMN IF EXISTS name_key;

CREATE INDEX plugin_runtime_observations_company_provider_name_idx
    ON plugin_runtime_observations (company_id, provider, lower(name))
    WHERE icon_url IS NOT NULL;
