-- Without the column a daemon session would read as its person's own, with
-- no limits. End every live daemon session first; its daemon signs in again
-- after the downgrade.
UPDATE auth_sessions
SET revoked_at = now(),
    revocation_reason = 'downgrade',
    updated_at = now()
WHERE client = 'daemon'
  AND revoked_at IS NULL;

ALTER TABLE auth_sessions
    DROP COLUMN client;
