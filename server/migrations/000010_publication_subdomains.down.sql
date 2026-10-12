-- Rollback requires draining routing jobs before the older status constraint
-- can be restored. Refuse to silently discard work still configuring a route.
ALTER TABLE output_publications DROP CONSTRAINT output_publications_status_check;
ALTER TABLE output_publications ADD CONSTRAINT output_publications_status_check CHECK (
    status IN ('queued', 'uploading', 'building', 'deploying', 'published', 'unpublishing', 'unpublished', 'failed')
);
DROP INDEX output_publications_work_idx;
CREATE INDEX output_publications_work_idx ON output_publications (next_attempt_at, created_at, id)
    WHERE status IN ('queued', 'uploading', 'building', 'deploying', 'unpublishing');
ALTER TABLE output_publications DROP COLUMN route_pending, DROP COLUMN route_binding_id, DROP COLUMN custom_hostname;
