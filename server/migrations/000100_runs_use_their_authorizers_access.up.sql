-- A Run or the voice assistant uses its Authorizer's Project access, never
-- more (D31, revised 2026-10-06). There is no chat-audience term: the person
-- who tags an Agent answers for what it shares. A Task chat's answer stays in
-- that chat's Project. Go binds every permission; SQL names none.
--
-- 000099 is never edited, so this migration replaces its two functions:
-- delegate_permission_held loses the audience and the unused Agent
-- arguments, and run_permission_held no longer reads the Run's invocation.
-- Nothing calls conversation_audience_reads any more, so it goes.
-- run_permission_held calls delegate_permission_held, and the old
-- delegate_permission_held calls conversation_audience_reads, so they go in
-- that order.
DROP FUNCTION run_permission_held(uuid, uuid, uuid, text, text, text[]);
DROP FUNCTION delegate_permission_held(uuid, uuid, uuid, uuid, uuid, text, text, text[], uuid);
DROP FUNCTION conversation_audience_reads(uuid, uuid, uuid);

-- The delegate rule: what a Run or the voice assistant may do on a Project.
-- Its Authorizer holds the permission and read there (never through the
-- management bridge: a delegate never manages), and a Task chat's answer
-- stays in that chat's Project.
--
-- Arguments are positional because a SQL function resolves a bare name to a
-- column first. Neither function is inlined (each body holds a subquery), so
-- each is one opaque call per candidate Project, never inside a list over a
-- Company's Tasks.
CREATE FUNCTION delegate_permission_held(
    company_id uuid,
    authorizer_user_id uuid,
    answer_conversation_id uuid,
    project_id uuid,
    permission text,
    read_permission text,
    company_default_permissions text[]
) RETURNS boolean
LANGUAGE sql STABLE
AS $$
SELECT $2 IS NOT NULL
   AND project_permission_held($1, $4, $2, $6, $7, '{}', '{}')
   AND project_permission_held($1, $4, $2, $5, $7, '{}', '{}')
   AND NOT EXISTS (
       SELECT 1 FROM conversations AS answer
       WHERE answer.company_id = $1 AND answer.id = $3
         AND answer.kind = 'task' AND answer.project_id IS DISTINCT FROM $4
   )
$$;

-- The same rule for one Run, from its own row: its Authorizer and the chat
-- it answers into. A Run that is not in the Company, or has no Authorizer,
-- may do nothing.
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
        $3, $4, $5, $6)
    FROM runs AS run
    WHERE run.company_id = $1 AND run.id = $2
), false)
$$;
