-- Preserve Company choices and usage when reverting the creation defaults.
ALTER TABLE huddle_notes_policies
    ALTER COLUMN enabled SET DEFAULT false,
    ALTER COLUMN monthly_seconds SET DEFAULT 0;
