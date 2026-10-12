-- App Artifacts persist their type-specific launch document as JSON while
-- retaining a derived loopback URL for existing Artifact readers. A running
-- Run may also save multiple immutable Artifacts; their explicit sequence
-- makes the newest presentation deterministic without timestamp or UUID ties.
ALTER TABLE outputs
    ADD COLUMN preview_url text,
    ADD COLUMN payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    ADD COLUMN presentation_number bigint DEFAULT 1 NOT NULL,
    DROP CONSTRAINT outputs_run_id_key,
    ADD CONSTRAINT outputs_run_presentation_number_key
        UNIQUE (run_id, presentation_number),
    ADD CONSTRAINT outputs_run_presentation_fingerprint_key
        UNIQUE (run_id, presentation_fingerprint),
    ADD CONSTRAINT outputs_preview_url_type_check
        CHECK (preview_url IS NULL OR type = 'app'),
    ADD CONSTRAINT outputs_payload_object_check
        CHECK (jsonb_typeof(payload) = 'object'),
    ADD CONSTRAINT outputs_presentation_number_check
        CHECK (presentation_number BETWEEN 1 AND 9007199254740991);
