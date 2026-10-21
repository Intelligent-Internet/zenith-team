ALTER TABLE huddle_notes DROP CONSTRAINT huddle_notes_state_check;
ALTER TABLE huddle_notes ADD CONSTRAINT huddle_notes_state_check
 CHECK (state IN ('off','starting','capturing','stopping','stopped','interrupted','ended','expired'));
ALTER TABLE huddle_notes_intervals
 ADD COLUMN stop_requested_at timestamptz,
 ADD COLUMN drain_until timestamptz;
