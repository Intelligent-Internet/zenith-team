ALTER TABLE huddle_connections
    ADD COLUMN reservation_expires_at timestamptz;

UPDATE huddle_connections
SET reservation_expires_at = created_at + interval '30 seconds'
WHERE status='reserved';

ALTER TABLE huddle_connections
    ADD CONSTRAINT huddle_connections_reservation_expiry_check
    CHECK (status<>'reserved' OR reservation_expires_at IS NOT NULL);

CREATE INDEX huddle_connections_expiry_idx
    ON huddle_connections(reservation_expires_at)
    WHERE status='reserved';
