-- Provider sign-in: the daemon's last look at whether the provider CLI on a
-- machine is signed in, taken with `claude auth status` / `codex login
-- status` in the same probe round as the version. Typed columns rather than
-- metadata keys because every roster and picker reads the state, and because
-- the CHECK keeps state and check time together. A signed-out provider keeps
-- its runtime online: runs on it fail with a
-- readable provider_auth_or_access reason; the roster just says so first.
-- NULL means the registration predates the probe.
ALTER TABLE runtimes
    ADD COLUMN provider_sign_in_state text,
    ADD COLUMN provider_sign_in_detail text,
    ADD COLUMN provider_sign_in_checked_at timestamp with time zone,
    ADD CONSTRAINT runtimes_provider_sign_in_check CHECK (
        (
            provider_sign_in_state IS NULL
            AND provider_sign_in_detail IS NULL
            AND provider_sign_in_checked_at IS NULL
        )
        OR (
            provider_sign_in_state IN ('signed_in', 'signed_out', 'unknown')
            AND provider_sign_in_checked_at IS NOT NULL
        )
    );
