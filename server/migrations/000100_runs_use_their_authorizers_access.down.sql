-- Restores the functions as 000099 left them: the audience term, read
-- through conversation_audience_reads (its 000086 body), and the unused Agent
-- argument. The queries of the previous release call these forms.
-- run_permission_held calls delegate_permission_held, so it goes first.
DROP FUNCTION run_permission_held(uuid, uuid, uuid, text, text, text[]);
DROP FUNCTION delegate_permission_held(uuid, uuid, uuid, uuid, text, text, text[]);

CREATE FUNCTION conversation_audience_reads(
    company_id uuid,
    conversation_id uuid,
    project_id uuid
) RETURNS boolean
LANGUAGE sql STABLE
AS $$
SELECT EXISTS (
    SELECT 1
    FROM conversations AS conversation
    WHERE conversation.company_id = $1
      AND conversation.id = $2
      AND conversation.kind IN ('channel', 'human_direct', 'huddle')
) AND EXISTS (
    SELECT 1
    FROM conversation_members AS participant
    WHERE participant.company_id = $1
      AND participant.conversation_id = $2
      AND participant.status = 'active'
) AND NOT EXISTS (
    SELECT 1
    FROM conversation_members AS participant
    JOIN company_members AS member
      ON member.company_id = participant.company_id
     AND member.user_id = participant.user_id
     AND member.status = 'active'
    JOIN users AS account
      ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
    WHERE participant.company_id = $1
      AND participant.conversation_id = $2
      AND participant.status = 'active'
      AND NOT EXISTS (
          SELECT 1 FROM project_access AS access
          WHERE access.company_id = $1
            AND access.project_id = $3
            AND access.user_id = participant.user_id
      )
)
$$;

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
