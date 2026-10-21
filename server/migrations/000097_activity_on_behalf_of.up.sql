-- Audit has three identities. An Activity row an Agent writes in a Run
-- names the Agent (actor_type 'agent', actor_id), the person the Run works
-- for (on_behalf_of_user_id, the Run's Authorizer) and the Run (run_id). A
-- person's own row and a system row store NULL in both. Both columns are
-- attribution on append-only evidence: no foreign keys, and no index while
-- nothing filters on them.
ALTER TABLE activity_events
    ADD COLUMN on_behalf_of_user_id uuid,
    ADD COLUMN run_id uuid,
    ADD CONSTRAINT activity_events_on_behalf_of_check
        CHECK (on_behalf_of_user_id IS NULL OR actor_type = 'agent'),
    ADD CONSTRAINT activity_events_run_check
        CHECK (run_id IS NULL OR on_behalf_of_user_id IS NOT NULL);

-- A Task a Run creates keeps its person as the creator and also names the
-- Agent that created it, so a Task may now carry both creators.
ALTER TABLE tasks
    DROP CONSTRAINT tasks_creator_exactly_one_check,
    ADD CONSTRAINT tasks_creator_present_check
        CHECK (num_nonnulls(creator_user_id, creator_agent_id) >= 1);

-- A person's own Task still runs with the Company's root Agent; the
-- creating Agent counts only for a Task no person created. The body is
-- 000065_human_task_agents' with that one condition added.
CREATE OR REPLACE VIEW task_run_agents AS
SELECT tasks.id AS task_id, tasks.company_id,
       COALESCE(tasks.executed_by_agent_id,
                CASE WHEN tasks.creator_user_id IS NULL THEN creator.id END,
                root.id) AS agent_id
FROM tasks
LEFT JOIN agents AS creator
  ON creator.company_id = tasks.company_id
 AND creator.id = tasks.creator_agent_id
LEFT JOIN agents AS root
  ON root.company_id = tasks.company_id
 AND root.reports_to_agent_id IS NULL
 AND root.archived_at IS NULL;
