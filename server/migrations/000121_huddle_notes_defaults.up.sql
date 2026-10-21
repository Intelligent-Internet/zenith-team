ALTER TABLE huddle_notes_policies
    ALTER COLUMN enabled SET DEFAULT true,
    ALTER COLUMN monthly_seconds SET DEFAULT 360000;

-- A policy save advances the revision. Only replace untouched legacy defaults.
UPDATE huddle_notes_policies
SET enabled = true, monthly_seconds = 360000, revision = revision + 1
WHERE revision = 1 AND NOT enabled AND monthly_seconds = 0
    AND retention_days = 30 AND reserved_seconds = 0;
