-- A human still does their own Task. Its chat runs with the creating Agent,
-- or the Company's CEO when a member created it. Keep this resolution in one
-- projection so admission, continuation, retries and completion agree.
CREATE VIEW task_run_agents AS
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
