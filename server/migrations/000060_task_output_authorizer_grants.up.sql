-- A Task Run executes on its authorizer's own computer (ADR 0014), but a Task
-- Output belonged to the Task's creator alone, so the member who ran the Task
-- could not see what their own Run presented. InsertOutput now gives the
-- Run's authorizer a view grant when they are not the owner.
--
-- Earlier Runs are not all like that. Before ADR 0014 a Run's authorizer was
-- whoever started it: the member who sent a Task message, assigned or started
-- the Task, or decided an Approval or answered a Question. The Run ran on the
-- Agent's runtime, often another member's computer. Granting those people
-- would open an owner's private Outputs to members the owner never chose. So
-- an earlier Task Output gets the grant only when its Run clearly ran for its
-- authorizer, on their own computer, for their own Task:
--   - the authorizer registered the Run's runtime (runtimes.owner_user_id);
--   - the authorizer is the Task's assignee (tasks.assignee_user_id);
--   - the authorizer is not the Output's owner;
--   - the authorizer is an active member now and was one when the Output was
--     saved. A member who left and came back gets none for Outputs saved
--     before their return, since a grant older than a return never counts.
-- Every other earlier Task Output keeps exactly the access it had; its owner
-- can still share it.
--
-- It only adds access. The owner stays the owner, and a grant the authorizer
-- already holds, view or manage, is kept as it is. An Output that gains a
-- grant becomes shared, as any person grant makes it, and its access revision
-- advances so a client holding the old revision reloads.
WITH granted AS (
    INSERT INTO output_grants (company_id, output_id, user_id, permission, granted_by_user_id)
    SELECT output.company_id, output.id, run.execution_authorized_by_user_id, 'view',
           run.execution_authorized_by_user_id
    FROM outputs AS output
    JOIN runs AS run
      ON run.id = output.run_id
     AND run.company_id = output.company_id
    JOIN conversations AS conversation
      ON conversation.id = run.conversation_id
     AND conversation.company_id = run.company_id
     AND conversation.kind = 'task'
    JOIN runtimes AS runtime
      ON runtime.id = run.runtime_id
     AND runtime.company_id = run.company_id
     AND runtime.owner_user_id = run.execution_authorized_by_user_id
    JOIN tasks AS task
      ON task.id = run.task_id
     AND task.company_id = run.company_id
     AND task.assignee_user_id = run.execution_authorized_by_user_id
    JOIN company_members AS member
      ON member.company_id = run.company_id
     AND member.user_id = run.execution_authorized_by_user_id
     AND member.status = 'active'
     AND output.created_at >= COALESCE(member.reactivated_at, member.created_at)
    WHERE output.owner_user_id IS DISTINCT FROM run.execution_authorized_by_user_id
    ON CONFLICT (company_id, output_id, user_id) DO NOTHING
    RETURNING company_id, output_id
)
UPDATE outputs
SET visibility = CASE WHEN outputs.visibility = 'private' THEN 'shared' ELSE outputs.visibility END,
    access_version = outputs.access_version + 1
FROM granted
WHERE outputs.company_id = granted.company_id
  AND outputs.id = granted.output_id;
