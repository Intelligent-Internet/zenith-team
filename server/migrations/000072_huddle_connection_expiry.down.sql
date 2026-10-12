DROP INDEX huddle_connections_expiry_idx;
ALTER TABLE huddle_connections
    DROP CONSTRAINT huddle_connections_reservation_expiry_check,
    DROP COLUMN reservation_expires_at;
