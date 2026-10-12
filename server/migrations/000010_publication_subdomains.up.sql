ALTER TABLE output_publications
    ADD COLUMN custom_hostname text,
    ADD COLUMN route_binding_id text,
    ADD COLUMN route_pending boolean DEFAULT false NOT NULL,
    ADD CONSTRAINT output_publications_route_pending_check CHECK (
        (NOT route_pending OR (public_url IS NOT NULL AND service_ref IS NOT NULL))
        AND (status <> 'routing' OR route_pending)
    ),
    ADD CONSTRAINT output_publications_custom_hostname_key UNIQUE (custom_hostname),
    ADD CONSTRAINT output_publications_route_check CHECK (
        (custom_hostname IS NULL AND route_binding_id IS NULL AND route_pending = false)
        OR (custom_hostname IS NOT NULL AND route_binding_id IS NOT NULL)
    );

ALTER TABLE output_publications DROP CONSTRAINT output_publications_status_check;
ALTER TABLE output_publications ADD CONSTRAINT output_publications_status_check CHECK (
    status IN ('queued', 'uploading', 'building', 'deploying', 'routing', 'published', 'unpublishing', 'unpublished', 'failed')
);
DROP INDEX output_publications_work_idx;
CREATE INDEX output_publications_work_idx ON output_publications (next_attempt_at, created_at, id)
    WHERE status IN ('queued', 'uploading', 'building', 'deploying', 'routing', 'unpublishing');
