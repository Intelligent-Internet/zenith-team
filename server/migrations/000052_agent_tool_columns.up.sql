-- Capability facts SQL filters on get typed columns. The capabilities document
-- in agents.settings and the frozen Run context stay the payloads the API and
-- the daemon read; each writer sets the column from the same value.

-- The Plugins an Agent turned off, for its frozen Run brief. No foreign key: a
-- removed catalog item simply stops matching.
ALTER TABLE agents ADD COLUMN disabled_plugin_ids uuid[] NOT NULL DEFAULT '{}';

-- Whether the Run was frozen with the zenith-studio tool, for the Studio lock.
ALTER TABLE runs ADD COLUMN studio_enabled boolean NOT NULL DEFAULT false;

-- The one read of each document.
UPDATE agents SET disabled_plugin_ids = ARRAY(
    SELECT plugin.id::uuid
    FROM jsonb_array_elements_text(settings->'capabilities'->'disabledPlugins') AS plugin(id))
WHERE jsonb_typeof(settings->'capabilities'->'disabledPlugins') = 'array';
UPDATE runs SET studio_enabled = true
WHERE context @> '{"agent":{"capabilities":{"enabledTools":["zenith-studio"]}}}';
