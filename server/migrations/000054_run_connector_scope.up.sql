-- A Run's Connector scope as typed rows. The requested selection and the
-- claim-time frozen snapshot were jsonb documents that the claim and the
-- invalidation sweep searched element by element. The daemon still receives
-- the frozen contract, tools included, inside runs.context; SQL reads these.

-- 'all': every Company-enabled connector of the Runtime's provider.
-- 'exact': exactly the Run's run_connector_selections rows (Schedule Runs and
-- their retries).
ALTER TABLE runs ADD COLUMN connector_selection_mode text NOT NULL DEFAULT 'all'
    CONSTRAINT runs_connector_selection_mode_check CHECK (connector_selection_mode IN ('all', 'exact'));
-- Set by the claim that froze this Run's snapshot. A retry starts without
-- one, so a failed freeze can still cancel it while it is queued.
ALTER TABLE runs ADD COLUMN connector_snapshot_frozen_at timestamptz;

-- Connector ids carry no foreign key in either table: a removed catalog item
-- must still fail the exact match instead of vanishing from the scope.
CREATE TABLE run_connector_selections (
    run_id uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    connector_id uuid NOT NULL,
    policy_revision bigint NOT NULL,
    source text NOT NULL,
    native_id text NOT NULL,
    contract_digest text NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT run_connector_selections_pkey PRIMARY KEY (run_id, connector_id)
);

CREATE TABLE run_connector_snapshots (
    run_id uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    connector_id uuid NOT NULL,
    policy_revision bigint NOT NULL,
    source text NOT NULL,
    native_id text NOT NULL,
    contract_digest text NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    -- A claim requeued after delivery failure freezes again.
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT run_connector_snapshots_pkey PRIMARY KEY (run_id, connector_id)
);

-- The one read of the old documents.
UPDATE runs SET connector_selection_mode = 'exact' WHERE connector_selection->>'mode' = 'exact';
INSERT INTO run_connector_selections (run_id, connector_id, policy_revision, source, native_id, contract_digest)
SELECT runs.id, (entry->>'id')::uuid, (entry->>'policyRevision')::bigint,
    entry->>'source', entry->>'nativeId', entry->>'contractDigest'
FROM runs, jsonb_array_elements(
    CASE WHEN jsonb_typeof(runs.connector_selection->'connectors') = 'array'
        THEN runs.connector_selection->'connectors' ELSE '[]'::jsonb END) AS entry
WHERE runs.connector_selection_mode = 'exact';
INSERT INTO run_connector_snapshots (run_id, connector_id, policy_revision, source, native_id, contract_digest)
SELECT runs.id, (entry->>'id')::uuid, (entry->>'policyRevision')::bigint,
    entry->>'source', entry->>'nativeId', entry->>'contractDigest'
FROM runs, jsonb_array_elements(
    CASE WHEN jsonb_typeof(runs.connector_snapshot->'connectors') = 'array'
        THEN runs.connector_snapshot->'connectors' ELSE '[]'::jsonb END) AS entry;
UPDATE runs SET connector_snapshot_frozen_at = updated_at WHERE context ? 'connectorSnapshot';

ALTER TABLE runs DROP CONSTRAINT runs_schedule_connector_selection_check;
ALTER TABLE runs ADD CONSTRAINT runs_schedule_connector_selection_check
    CHECK (schedule_firing_id IS NULL OR connector_selection_mode = 'exact');
ALTER TABLE runs DROP COLUMN connector_selection, DROP COLUMN connector_snapshot;
