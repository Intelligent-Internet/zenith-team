-- The delegate rule in one place (D31, IA-FR10). A delegate acts with one
-- person's access and never more: a Run, or the Huddle voice assistant. Its
-- person is its Authorizer. Enqueue, claim, every Run-proof read and the
-- voice reads ask these two functions; nothing restates the rule.
--
-- The delegate rule: what a Run or the voice assistant may do on a Project.
-- Its Authorizer holds the permission and read there (never through the
-- management bridge: a delegate never manages); a Task chat's answer stays
-- in that chat's Project; a group chat's audience must all read the
-- Project. agent_id is the acting Agent; a later migration (Access TECH C1) adds
-- its policy term.
-- Go binds every permission; SQL names none.
--
-- Arguments are positional because a SQL function resolves a bare name to a
-- column first. Neither function is inlined (each body holds a subquery), so
-- each is one opaque call per candidate Project, never inside a list over a
-- Company's Tasks.
CREATE FUNCTION delegate_permission_held(
    company_id uuid,
    authorizer_user_id uuid,
    answer_conversation_id uuid,
    audience_conversation_id uuid,
    project_id uuid,
    permission text,
    read_permission text,
    company_default_permissions text[],
    agent_id uuid
) RETURNS boolean
LANGUAGE sql STABLE
AS $$
SELECT $2 IS NOT NULL
   AND project_permission_held($1, $5, $2, $7, $8, '{}', '{}')
   AND project_permission_held($1, $5, $2, $6, $8, '{}', '{}')
   AND NOT EXISTS (
       SELECT 1 FROM conversations AS answer
       WHERE answer.company_id = $1 AND answer.id = $3
         AND answer.kind = 'task' AND answer.project_id IS DISTINCT FROM $5
   )
   AND ($4 IS NULL OR conversation_audience_reads($1, $4, $5))
$$;

-- The same rule for one Run, from its own row: its Authorizer, the chat it
-- answers into, the channel or group DM a tagged Run answers into (its
-- invocation's channel), and its Agent. A Run that is not in the Company, or
-- has no Authorizer, may do nothing.
CREATE FUNCTION run_permission_held(
    company_id uuid,
    run_id uuid,
    project_id uuid,
    permission text,
    read_permission text,
    company_default_permissions text[]
) RETURNS boolean
LANGUAGE sql STABLE
AS $$
SELECT COALESCE((
    SELECT delegate_permission_held(
        run.company_id, run.execution_authorized_by_user_id, run.conversation_id,
        invocation.channel_id, $3, $4, $5, $6, run.agent_id)
    FROM runs AS run
    LEFT JOIN channel_invocations AS invocation
      ON invocation.company_id = run.company_id AND invocation.id = run.invocation_id
    WHERE run.company_id = $1 AND run.id = $2
), false)
$$;
