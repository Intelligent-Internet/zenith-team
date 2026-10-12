-- 000028_auth_refresh_idempotency: bind a client-authored rotation attempt to
-- its replacement session so a lost refresh response can be recovered safely.

ALTER TABLE auth_sessions
    ADD COLUMN rotation_id uuid;
