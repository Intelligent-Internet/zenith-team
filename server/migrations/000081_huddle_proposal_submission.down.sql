UPDATE huddle_work_proposals SET state='rejected' WHERE state='submitted';
ALTER TABLE huddle_work_proposals
 DROP CONSTRAINT huddle_work_proposals_state_check,
 ADD CONSTRAINT huddle_work_proposals_state_check CHECK(state IN ('pending','rejected')),
 DROP COLUMN submitted_by_user_id,
 DROP COLUMN submitted_revision,
 DROP COLUMN submission_hash,
 DROP COLUMN submitted_task_id,
 DROP COLUMN submitted_task_key,
 DROP COLUMN submitted_run_id,
 DROP COLUMN submitted_at;
