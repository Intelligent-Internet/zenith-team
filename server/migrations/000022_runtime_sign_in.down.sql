ALTER TABLE runtimes
    DROP CONSTRAINT runtimes_provider_sign_in_check,
    DROP COLUMN provider_sign_in_state,
    DROP COLUMN provider_sign_in_detail,
    DROP COLUMN provider_sign_in_checked_at;
