-- A bounded at-most-once speech checkpoint, not evidence of audible delivery.
ALTER TABLE huddle_work_proposals
 ADD COLUMN voice_notice_keys text[] NOT NULL DEFAULT '{}';
