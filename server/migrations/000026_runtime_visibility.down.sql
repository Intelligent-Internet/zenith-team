-- Rolling back forgets every sharing decision and every Run's authorizer.
-- The pending Runs the up migration cancelled are terminal and are not
-- restored. A build that ignores stored consent must not serve a database
-- that still carries it; drop the columns only as part of a reviewed
-- downgrade.
ALTER TABLE runs
    DROP COLUMN execution_authorized_by_user_id;

ALTER TABLE agents
    DROP COLUMN runtime_bound_by_user_id;

ALTER TABLE runtimes
    DROP CONSTRAINT runtimes_visibility_check,
    DROP COLUMN visibility,
    DROP COLUMN visibility_revision;
