-- The envelope still carries schemaVersion, so dropping the column loses nothing.
DROP INDEX activity_events_snapshot_subject_idx;
ALTER TABLE activity_events DROP COLUMN schema_version;
