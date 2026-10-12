ALTER TABLE runs
    ADD COLUMN connector_selection jsonb
        DEFAULT '{"schemaVersion":1,"mode":"all","connectors":[]}'::jsonb NOT NULL,
    ADD COLUMN connector_snapshot jsonb
        DEFAULT '{"schemaVersion":1,"connectors":[]}'::jsonb NOT NULL;

UPDATE runs SET connector_selection = jsonb_build_object(
    'schemaVersion', 1, 'mode', 'exact',
    'connectors', COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
            'id', selected.connector_id, 'policyRevision', selected.policy_revision,
            'source', selected.source, 'nativeId', selected.native_id,
            'contractDigest', selected.contract_digest)
            ORDER BY selected.source COLLATE "C", selected.native_id COLLATE "C", selected.connector_id)
        FROM run_connector_selections AS selected
        WHERE selected.run_id = runs.id), '[]'::jsonb))
WHERE connector_selection_mode = 'exact';
-- The frozen document the daemon read, tools included.
UPDATE runs SET connector_snapshot = context->'connectorSnapshot'
WHERE connector_snapshot_frozen_at IS NOT NULL AND jsonb_typeof(context->'connectorSnapshot') = 'object';

ALTER TABLE runs DROP CONSTRAINT runs_schedule_connector_selection_check;
ALTER TABLE runs
    ADD CONSTRAINT runs_connector_selection_check CHECK (jsonb_typeof(connector_selection) = 'object'),
    ADD CONSTRAINT runs_connector_snapshot_check CHECK (jsonb_typeof(connector_snapshot) = 'object'),
    ADD CONSTRAINT runs_schedule_connector_selection_check CHECK (
        schedule_firing_id IS NULL
        OR ((
            connector_selection ->> 'schemaVersion' = '1'
            AND connector_selection ->> 'mode' = 'exact'
            AND jsonb_typeof(connector_selection -> 'connectors') = 'array'
        ) IS TRUE)
    );

DROP TABLE run_connector_snapshots;
DROP TABLE run_connector_selections;
ALTER TABLE runs DROP COLUMN connector_snapshot_frozen_at, DROP COLUMN connector_selection_mode;
