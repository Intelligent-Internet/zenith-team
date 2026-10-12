-- Numeric-only binaries cannot consume Runs written after the UUID cutover.
-- Restore the rehearsed pre-cutover backup rather than inventing numeric IDs.
DO $$ BEGIN
    RAISE EXCEPTION 'UUID Run migration requires restoring the pre-cutover backup; automatic downgrade is unsupported';
END $$;
