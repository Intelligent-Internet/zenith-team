-- A Task a Run created keeps its person as its only creator, so the single
-- creator rule can come back.
UPDATE tasks SET creator_agent_id = NULL WHERE creator_user_id IS NOT NULL;

ALTER TABLE tasks
    DROP CONSTRAINT tasks_creator_present_check,
    ADD CONSTRAINT tasks_creator_exactly_one_check
        CHECK (num_nonnulls(creator_user_id, creator_agent_id) = 1);

-- Restore the 000065_human_task_agents body.
CREATE OR REPLACE VIEW task_run_agents AS
SELECT tasks.id AS task_id, tasks.company_id,
       COALESCE(tasks.executed_by_agent_id, creator.id, root.id) AS agent_id
FROM tasks
LEFT JOIN agents AS creator
  ON creator.company_id = tasks.company_id
 AND creator.id = tasks.creator_agent_id
LEFT JOIN agents AS root
  ON root.company_id = tasks.company_id
 AND root.reports_to_agent_id IS NULL
 AND root.archived_at IS NULL;

-- The Agent stays the actor of the rows it wrote; who it acted for and the
-- Run are dropped with their columns.
ALTER TABLE activity_events
    DROP CONSTRAINT activity_events_run_check,
    DROP CONSTRAINT activity_events_on_behalf_of_check,
    DROP COLUMN run_id,
    DROP COLUMN on_behalf_of_user_id;
