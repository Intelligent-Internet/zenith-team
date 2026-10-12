DROP INDEX IF EXISTS plugin_runtime_observations_company_provider_name_idx;
ALTER TABLE plugin_runtime_observations
    DROP COLUMN terms_of_service_url,
    DROP COLUMN privacy_policy_url,
    DROP COLUMN website_url,
    DROP COLUMN developer_name,
    DROP COLUMN brand_color,
    DROP COLUMN default_prompts,
    DROP COLUMN long_description;
