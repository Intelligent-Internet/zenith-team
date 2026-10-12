-- Runtime lifecycle: the columns the user-facing runtime lifecycle adds to
-- the daemon-registered row.
--
-- unusable_reason_code / unusable_reason_message: why a runtime the daemon
-- found on a machine cannot run work there (the provider CLI is below the
-- minimum version, or not executable). Written by registration and cleared
-- by the next registration without a reason. Typed columns rather than
-- metadata keys because the agent availability projection reads the reason
-- on every read, and because the CHECK below makes "unusable implies
-- offline" structural: a heartbeat cannot flip an unusable runtime online.
ALTER TABLE runtimes
    ADD COLUMN unusable_reason_code text,
    ADD COLUMN unusable_reason_message text,
    ADD CONSTRAINT runtimes_unusable_reason_check CHECK (
        (unusable_reason_code IS NULL AND unusable_reason_message IS NULL)
        OR (
            unusable_reason_code IN ('below_minimum_version', 'not_executable')
            AND unusable_reason_message IS NOT NULL
            AND status = 'offline'
        )
    );

-- custom_name: the name a person gave the machine, applied to every runtime
-- the daemon registered. NULL means "no custom name; show name". Written
-- only by the user-plane rename; registration never touches it, so a
-- re-registration cannot overwrite the user's choice. The length rule
-- (1..100 runes after trimming) lives at the request boundary and before
-- the write, not in a CHECK.
ALTER TABLE runtimes
    ADD COLUMN custom_name text;

-- removed_at: the owner's soft removal of a runtime. NULL means live. A
-- removed row leaves the roster, the detail read, the heartbeat touch and
-- the claim path, but stays in the table because runs.runtime_id points at
-- it and run history outlives the machine. Registration clears it: the same
-- machine announcing the same (company, daemon, provider) identity again is
-- the user restarting it on purpose. No index: the roster is a per-company
-- list of machines, and runtimes_company_status_idx already serves it.
ALTER TABLE runtimes
    ADD COLUMN removed_at timestamp with time zone;
