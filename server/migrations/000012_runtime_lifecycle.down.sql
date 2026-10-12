ALTER TABLE runtimes
    DROP COLUMN removed_at,
    DROP COLUMN custom_name,
    DROP CONSTRAINT runtimes_unusable_reason_check,
    DROP COLUMN unusable_reason_message,
    DROP COLUMN unusable_reason_code;
