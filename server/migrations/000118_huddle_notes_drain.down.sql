UPDATE huddle_notes_intervals SET state='finished',ended_at=COALESCE(stop_requested_at,now()) WHERE drain_until IS NOT NULL AND state<>'finished';
UPDATE huddle_notes SET state='stopped',stop_reason='drain_downgrade',revision=revision+1 WHERE state='stopping';
ALTER TABLE huddle_notes_intervals DROP COLUMN drain_until, DROP COLUMN stop_requested_at;
ALTER TABLE huddle_notes DROP CONSTRAINT huddle_notes_state_check;
ALTER TABLE huddle_notes ADD CONSTRAINT huddle_notes_state_check
 CHECK (state IN ('off','starting','capturing','stopped','interrupted','ended','expired'));
