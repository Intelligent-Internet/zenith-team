ALTER TABLE huddle_work_proposals
 DROP CONSTRAINT huddle_work_proposals_state_check,
 ADD CONSTRAINT huddle_work_proposals_state_check CHECK(state IN ('pending','rejected','submitted')),
 ADD COLUMN submitted_by_user_id uuid,
 ADD COLUMN submitted_revision bigint,
 ADD COLUMN submission_hash bytea,
 ADD COLUMN submitted_task_id uuid,
 ADD COLUMN submitted_task_key text,
 ADD COLUMN submitted_run_id uuid,
 ADD COLUMN submitted_at timestamptz,
 ADD FOREIGN KEY(company_id,submitted_task_id) REFERENCES tasks(company_id,id) ON DELETE SET NULL(submitted_task_id),
 ADD FOREIGN KEY(company_id,submitted_run_id) REFERENCES runs(company_id,id) ON DELETE SET NULL(submitted_run_id);
