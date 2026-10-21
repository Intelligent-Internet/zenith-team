-- Activity owner snapshots and evidence. Mutations use the caller's transaction.
-- Visibility is retained with each row and precedes filtering and pagination.

-- name: CreateActivity :one
-- An Agent's row in a Run also names the person it acted for and the Run;
-- every other row stores NULL in both.
INSERT INTO activity_events (
    company_id, actor_type, actor_id, action, entity_type, entity_id, revision,
    visibility, visibility_user_id, agent_id, task_id, conversation_id, schema_version, details,
    project_id, on_behalf_of_user_id, run_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
RETURNING *;

-- name: GetActivitySubjectState :one
SELECT entity_type, entity_id, company_id, revision, COALESCE(visibility,'')::text AS visibility, visibility_user_id, state, context, project_id FROM activity_subject_states
WHERE entity_type = sqlc.arg(entity_type)::text AND entity_id = sqlc.arg(entity_id)::uuid;

-- name: LockActivityBaselines :exec
SELECT pg_advisory_xact_lock(hashtextextended('activity-history-baseline', 0));

-- name: ListActivitySubjectsWithoutHistory :many
-- A subject has history once it has a snapshot row (schema_version 1). Event
-- evidence (schema_version 0) carries no owner state, so it cannot start one.
SELECT subject.entity_type, subject.entity_id, subject.company_id, subject.revision, COALESCE(subject.visibility,'')::text AS visibility, subject.visibility_user_id, subject.state, subject.context, subject.project_id FROM activity_subject_states AS subject
WHERE NOT EXISTS (
    SELECT 1 FROM activity_events AS activity
    WHERE activity.company_id = subject.company_id
      AND activity.entity_type = subject.entity_type AND activity.entity_id = subject.entity_id::text
      AND activity.schema_version = 1
)
ORDER BY subject.company_id, subject.entity_type, subject.entity_id;

-- name: GetActivityActorDisplayName :one
SELECT COALESCE(CASE sqlc.arg(actor_type)::text
    WHEN 'user' THEN (SELECT users.display_name FROM users JOIN company_members ON company_members.user_id = users.id
        WHERE users.id::text = sqlc.arg(actor_id)::text AND company_members.company_id = sqlc.arg(company_id)::uuid)
    WHEN 'agent' THEN (SELECT name FROM agents WHERE id::text = sqlc.arg(actor_id)::text AND company_id = sqlc.arg(company_id)::uuid)
END, '')::text AS display_name;

-- name: ResolveActivityScope :one
WITH scopes AS (
    SELECT 0 AS precedence, visibility, visibility_user_id FROM activity_subject_states
    WHERE company_id = sqlc.arg(company_id)::uuid
      AND entity_type = sqlc.arg(entity_type)::text AND entity_id::text = sqlc.arg(entity_id)::text
    UNION ALL
    SELECT 1, CASE kind WHEN 'task' THEN 'company' WHEN 'direct' THEN 'user' WHEN 'schedule' THEN 'user' END,
        CASE WHEN kind IN ('direct','schedule') THEN created_by_user_id END
    FROM conversations WHERE company_id = sqlc.arg(company_id)::uuid AND id = sqlc.narg(conversation_id)::uuid
    UNION ALL
    SELECT 0, CASE WHEN origin_user_id IS NULL THEN 'company' ELSE 'user' END, origin_user_id
    FROM approvals WHERE sqlc.arg(entity_type)::text = 'approval'
        AND company_id = sqlc.arg(company_id)::uuid AND id::text = sqlc.arg(entity_id)::text
    UNION ALL
    SELECT 0, CASE WHEN origin_user_id IS NULL THEN 'company' ELSE 'user' END, origin_user_id
    FROM questions WHERE sqlc.arg(entity_type)::text = 'question'
        AND company_id = sqlc.arg(company_id)::uuid AND id::text = sqlc.arg(entity_id)::text
    UNION ALL
    SELECT 0, 'user', recipient_user_id FROM inbox_items WHERE sqlc.arg(entity_type)::text = 'inbox'
        AND company_id = sqlc.arg(company_id)::uuid AND id::text = sqlc.arg(entity_id)::text
    UNION ALL
    SELECT 2, 'company', NULL::uuid
    WHERE sqlc.narg(conversation_id)::uuid IS NULL AND sqlc.arg(entity_id)::text <> ''
      AND sqlc.arg(entity_type)::text IN ('company', 'project', 'agent', 'task', 'runtime', 'resource', 'deliverable', 'publication', 'commerce', 'mailbox')
)
SELECT COALESCE(visibility,'')::text AS visibility, visibility_user_id FROM scopes ORDER BY precedence LIMIT 1;

-- name: ResolveActivityProject :one
-- The Project a receipt is about (ADR 0017): the chat its event names, else
-- the Task it names, else its subject (a Task, Project, chat, Run, message,
-- file in a chat, Approval or Question). No row means it is about none. Every
-- arm reads one row by its key.
SELECT scope.project_id::uuid AS project_id
FROM (
    SELECT 0 AS precedence, conversation.project_id
    FROM conversations AS conversation
    WHERE conversation.company_id = sqlc.arg(company_id)::uuid
      AND conversation.id = sqlc.narg(conversation_id)::uuid
    UNION ALL
    SELECT 1, task.project_id
    FROM tasks AS task
    WHERE task.company_id = sqlc.arg(company_id)::uuid AND task.id = sqlc.narg(task_id)::uuid
    UNION ALL
    SELECT 2, task.project_id
    FROM tasks AS task
    WHERE sqlc.arg(entity_type)::text = 'task'
      AND task.company_id = sqlc.arg(company_id)::uuid AND task.id = sqlc.narg(subject_id)::uuid
    UNION ALL
    SELECT 2, project.id
    FROM projects AS project
    WHERE sqlc.arg(entity_type)::text IN ('project', 'project_grant')
      AND project.company_id = sqlc.arg(company_id)::uuid AND project.id = sqlc.narg(subject_id)::uuid
    UNION ALL
    SELECT 2, conversation.project_id
    FROM conversations AS conversation
    WHERE sqlc.arg(entity_type)::text = 'conversation'
      AND conversation.company_id = sqlc.arg(company_id)::uuid AND conversation.id = sqlc.narg(subject_id)::uuid
    UNION ALL
    SELECT 2, conversation.project_id
    FROM runs AS run
    JOIN conversations AS conversation
      ON conversation.company_id = run.company_id AND conversation.id = run.conversation_id
    WHERE sqlc.arg(entity_type)::text = 'run'
      AND run.company_id = sqlc.arg(company_id)::uuid AND run.id = sqlc.narg(subject_id)::uuid
    UNION ALL
    SELECT 2, conversation.project_id
    FROM conversation_items AS item
    JOIN conversations AS conversation
      ON conversation.company_id = item.company_id AND conversation.id = item.conversation_id
    WHERE sqlc.arg(entity_type)::text = 'message'
      AND item.company_id = sqlc.arg(company_id)::uuid AND item.id = sqlc.narg(subject_id)::uuid
    UNION ALL
    SELECT 2, conversation.project_id
    FROM message_attachments AS link
    JOIN conversation_items AS item
      ON item.company_id = link.company_id AND item.id = link.message_id
    JOIN conversations AS conversation
      ON conversation.company_id = item.company_id AND conversation.id = item.conversation_id
    WHERE sqlc.arg(entity_type)::text = 'attachment'
      AND link.company_id = sqlc.arg(company_id)::uuid AND link.attachment_id = sqlc.narg(subject_id)::uuid
    UNION ALL
    SELECT 2, conversation.project_id
    FROM approvals AS approval
    JOIN conversations AS conversation
      ON conversation.company_id = approval.company_id AND conversation.id = approval.conversation_id
    WHERE sqlc.arg(entity_type)::text = 'approval'
      AND approval.company_id = sqlc.arg(company_id)::uuid AND approval.id = sqlc.narg(subject_id)::uuid
    UNION ALL
    SELECT 2, conversation.project_id
    FROM questions AS question
    JOIN conversations AS conversation
      ON conversation.company_id = question.company_id AND conversation.id = question.conversation_id
    WHERE sqlc.arg(entity_type)::text = 'question'
      AND question.company_id = sqlc.arg(company_id)::uuid AND question.id = sqlc.narg(subject_id)::uuid
) AS scope
WHERE scope.project_id IS NOT NULL
ORDER BY scope.precedence
LIMIT 1;

-- name: ListActivity :many
-- Daemon wakeups were once kept as evidence; they record nothing anyone did
-- and name a member's runtime (ADR 0014), so the feed and the calendar skip
-- any row an older sink stored.
SELECT activity.* FROM activity_events AS activity
WHERE activity.company_id = sqlc.arg(company_id)::uuid
  AND activity.entity_type NOT IN ('deliverable', 'publication', 'artifact', 'inbox')
  AND activity.action NOT IN ('runtime.work_available', 'runtime.pending_work')
  -- Who reads a row (ADR 0017), each lookup one probe per row:
  -- - a row addressed to one person reads for them, and when it names a
  --   Project only while they read that Project;
  -- - a Company-wide row about a Project, and a project_grant row (who let
  --   whom into a Project), read for that Project's readers;
  -- - a project_grant row also reads for the members whose role holds
  --   projects.manage, so an owner sees an admin let themselves in;
  -- - any other Company-wide row reads for every member, except a row about
  --   a Task, its chat, a Run, a message, a file, an Approval or a Question
  --   that names no Project: nobody can say whose it is, so nobody reads it.
  AND (
      (activity.visibility = 'user' AND activity.visibility_user_id = sqlc.arg(user_id)::uuid
          AND activity.project_id IS NULL)
      OR ((activity.visibility = 'company' OR activity.entity_type = 'project_grant'
           OR (activity.visibility = 'user' AND activity.visibility_user_id = sqlc.arg(user_id)::uuid))
          AND activity.project_id IS NOT NULL
          AND COALESCE((
              SELECT true FROM project_access AS access
              WHERE access.company_id = activity.company_id AND access.project_id = activity.project_id
                AND access.user_id = sqlc.arg(user_id)::uuid
              LIMIT 1
          ), false))
      OR (activity.entity_type = 'project_grant' AND EXISTS (
          SELECT 1 FROM company_members AS manager
          WHERE manager.company_id = sqlc.arg(company_id)::uuid
            AND manager.user_id = sqlc.arg(user_id)::uuid
            AND manager.status = 'active'
            AND manager.role = ANY(sqlc.arg(management_roles)::text[])))
      OR (activity.visibility = 'company' AND activity.project_id IS NULL
          AND activity.entity_type NOT IN (
              'task', 'conversation', 'run', 'message', 'attachment', 'approval', 'question', 'project_grant'))
  )
  AND EXISTS (SELECT 1 FROM company_members JOIN companies ON companies.id = company_members.company_id
      WHERE company_members.company_id = activity.company_id AND company_members.user_id = sqlc.arg(user_id)::uuid
        AND company_members.status = 'active' AND companies.deleting_at IS NULL
        AND (activity.entity_type <> 'mailbox'
             OR company_members.role = ANY(sqlc.arg(mailbox_roles)::text[])))
  AND (NOT @has_from::boolean OR activity.created_at >= @from_created_at::timestamptz)
  AND (NOT @has_to::boolean OR activity.created_at < @to_created_at::timestamptz)
  AND (sqlc.arg(entity_type)::text = '' OR activity.entity_type = sqlc.arg(entity_type)::text)
  AND (sqlc.arg(entity_id)::text = '' OR activity.entity_id = sqlc.arg(entity_id)::text)
  AND (sqlc.arg(action)::text = '' OR activity.action = sqlc.arg(action)::text)
  AND (sqlc.arg(actor_type)::text = '' OR activity.actor_type = sqlc.arg(actor_type)::text)
  AND (sqlc.arg(actor_id)::text = '' OR activity.actor_id = sqlc.arg(actor_id)::text)
  AND (NOT @has_revision::boolean OR activity.revision = sqlc.arg(revision)::bigint)
  AND (NOT @has_before_revision::boolean OR activity.revision < sqlc.arg(before_revision)::bigint)
  AND (NOT @has_cursor::boolean OR
      (CASE WHEN sqlc.arg(revision_order)::boolean THEN activity.revision < sqlc.arg(cursor_revision)::bigint
       ELSE (activity.created_at, activity.id) < (@before_created_at::timestamptz, @before_id::uuid) END))
ORDER BY CASE WHEN sqlc.arg(revision_order)::boolean THEN activity.revision END DESC,
    activity.created_at DESC, activity.id DESC
LIMIT @page_size;

-- name: CountActivityByLocalDay :many
SELECT to_char(activity.created_at AT TIME ZONE sqlc.arg(time_zone)::text, 'YYYY-MM-DD') AS day,
    count(*)::bigint AS activity_count
FROM activity_events AS activity
WHERE activity.company_id = sqlc.arg(company_id)::uuid
  AND activity.entity_type NOT IN ('deliverable', 'publication', 'artifact', 'inbox')
  AND activity.action NOT IN ('runtime.work_available', 'runtime.pending_work')
  -- Who reads a row (ADR 0017), each lookup one probe per row:
  -- - a row addressed to one person reads for them, and when it names a
  --   Project only while they read that Project;
  -- - a Company-wide row about a Project, and a project_grant row (who let
  --   whom into a Project), read for that Project's readers;
  -- - a project_grant row also reads for the members whose role holds
  --   projects.manage, so an owner sees an admin let themselves in;
  -- - any other Company-wide row reads for every member, except a row about
  --   a Task, its chat, a Run, a message, a file, an Approval or a Question
  --   that names no Project: nobody can say whose it is, so nobody reads it.
  AND (
      (activity.visibility = 'user' AND activity.visibility_user_id = sqlc.arg(user_id)::uuid
          AND activity.project_id IS NULL)
      OR ((activity.visibility = 'company' OR activity.entity_type = 'project_grant'
           OR (activity.visibility = 'user' AND activity.visibility_user_id = sqlc.arg(user_id)::uuid))
          AND activity.project_id IS NOT NULL
          AND COALESCE((
              SELECT true FROM project_access AS access
              WHERE access.company_id = activity.company_id AND access.project_id = activity.project_id
                AND access.user_id = sqlc.arg(user_id)::uuid
              LIMIT 1
          ), false))
      OR (activity.entity_type = 'project_grant' AND EXISTS (
          SELECT 1 FROM company_members AS manager
          WHERE manager.company_id = sqlc.arg(company_id)::uuid
            AND manager.user_id = sqlc.arg(user_id)::uuid
            AND manager.status = 'active'
            AND manager.role = ANY(sqlc.arg(management_roles)::text[])))
      OR (activity.visibility = 'company' AND activity.project_id IS NULL
          AND activity.entity_type NOT IN (
              'task', 'conversation', 'run', 'message', 'attachment', 'approval', 'question', 'project_grant'))
  )
  AND EXISTS (SELECT 1 FROM company_members JOIN companies ON companies.id = company_members.company_id
      WHERE company_members.company_id = activity.company_id AND company_members.user_id = sqlc.arg(user_id)::uuid
        AND company_members.status = 'active' AND companies.deleting_at IS NULL
        AND (activity.entity_type <> 'mailbox'
             OR company_members.role = ANY(sqlc.arg(mailbox_roles)::text[])))
  AND activity.created_at >= sqlc.arg(start_at)::timestamptz
  AND activity.created_at < sqlc.arg(end_at)::timestamptz
GROUP BY 1 ORDER BY 1;
