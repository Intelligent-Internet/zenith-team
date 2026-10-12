-- Version 24 permits only one Output per Run. Keep the newest presentation
-- when rolling back so the default compatibility result remains available.
WITH ranked_outputs AS (
    SELECT
        id,
        row_number() OVER (
            PARTITION BY run_id
            ORDER BY presentation_number DESC
        ) AS position
    FROM outputs
)
DELETE FROM outputs
USING ranked_outputs
WHERE outputs.id = ranked_outputs.id
  AND ranked_outputs.position > 1;

ALTER TABLE outputs
    DROP CONSTRAINT outputs_preview_url_type_check,
    DROP CONSTRAINT outputs_payload_object_check,
    DROP CONSTRAINT outputs_presentation_number_check,
    DROP CONSTRAINT outputs_run_presentation_fingerprint_key,
    DROP CONSTRAINT outputs_run_presentation_number_key,
    DROP COLUMN preview_url,
    DROP COLUMN payload,
    DROP COLUMN presentation_number,
    ADD CONSTRAINT outputs_run_id_key UNIQUE (run_id);
