-- The Codex presentation a Runtime observes for each Plugin: the long
-- description, up to three suggested prompts, the brand colour, the developer
-- and the developer's links. Like the rest of the row it is presentation only
-- and grants nothing. A Claude observation, and one from a daemon that predates
-- plugin-presentation-v1, leaves every column empty.
ALTER TABLE plugin_runtime_observations
    ADD COLUMN long_description text,
    ADD COLUMN default_prompts jsonb DEFAULT '[]'::jsonb NOT NULL,
    ADD COLUMN brand_color text,
    ADD COLUMN developer_name text,
    ADD COLUMN website_url text,
    ADD COLUMN privacy_policy_url text,
    ADD COLUMN terms_of_service_url text,
    ADD CONSTRAINT plugin_runtime_observations_default_prompts_check CHECK (
        CASE WHEN jsonb_typeof(default_prompts) = 'array'
            THEN jsonb_array_length(default_prompts) <= 3
            ELSE false
        END
    ),
    ADD CONSTRAINT plugin_runtime_observations_brand_color_check CHECK (
        brand_color IS NULL OR brand_color ~ '^#[0-9A-Fa-f]{6}$'
    );

-- A Claude Plugin reports no logo, so the catalog borrows the logo of a Codex
-- Plugin with the same case-insensitive name in the same Company. Codex apps
-- are matched through connector_catalog_items_company_provider_name_idx.
CREATE INDEX plugin_runtime_observations_company_provider_name_idx
    ON plugin_runtime_observations (company_id, provider, lower(name))
    WHERE icon_url IS NOT NULL;
