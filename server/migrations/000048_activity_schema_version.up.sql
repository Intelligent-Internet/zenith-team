-- The details envelope's schemaVersion says which shape an Activity row holds:
-- 1 is a before/after snapshot captured in the owner's write, 0 is event
-- evidence (a payload with no owner state). SQL reads it from this column and
-- never parses details for it. The envelope keeps the key because clients read
-- details as a self-describing union; the Activity writers set both together.
-- Rows default to evidence: only the snapshot writer claims a snapshot.
ALTER TABLE activity_events ADD COLUMN schema_version integer NOT NULL DEFAULT 0
    CHECK (schema_version IN (0, 1));

-- The one read of the envelope. 000015 wrapped every legacy payload, so a
-- schemaVersion nested inside payload is never mistaken for the row's own.
UPDATE activity_events SET schema_version = 1 WHERE details->>'schemaVersion' = '1';

-- Serves the startup baseline's per-subject "has a snapshot yet?" probe.
CREATE INDEX activity_events_snapshot_subject_idx
    ON activity_events (company_id, entity_type, entity_id) WHERE schema_version = 1;
