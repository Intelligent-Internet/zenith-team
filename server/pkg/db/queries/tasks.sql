-- name: GetTaskForActiveMember :one
-- One Task the caller reads: its Project must be readable to them
-- (project_access, ADR 0017), which also asserts an active membership, an
-- enabled account and a Company that is not being deleted. No row answers
-- "not found" whether the Task is missing or hidden.
SELECT tasks.*
FROM tasks
JOIN project_access AS access
  ON access.company_id = tasks.company_id
 AND access.project_id = tasks.project_id
 AND access.user_id = sqlc.arg(actor_user_id)::uuid
WHERE tasks.id = sqlc.arg(task_id)::uuid
  AND tasks.company_id = sqlc.arg(company_id)::uuid;

-- name: GetTaskByConversation :one
-- The Task behind a Task Conversation, for callers that hold the
-- Conversation lock and need the assignee (ADR 0014): Run queue promotion
-- asks whose deferred prompts it may materialize. No row for a direct
-- Conversation, or once the Task is deleted (conversations.task_id is SET
-- NULL and the Conversation outlives it).
SELECT tasks.*
FROM tasks
WHERE tasks.conversation_id = sqlc.arg(conversation_id)::uuid
  AND tasks.company_id = sqlc.arg(company_id)::uuid;

-- name: GetTaskForCompanyByIDForUpdate :one
SELECT tasks.*
FROM tasks
JOIN companies ON companies.id = tasks.company_id
WHERE tasks.id = sqlc.arg(task_id)::uuid
  AND tasks.company_id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL
FOR UPDATE OF tasks;

-- name: TaskHasNonterminalRunForCurrentExecutionGeneration :one
-- MarkFailed deliberately leaves its source Run active so the normal terminal
-- callback still owns transcript, usage, and session state. A member cannot
-- reopen that failed Task until the current generation has no active Run;
-- otherwise the same Run could fail the reopened Task again with a new key.
SELECT EXISTS (
    SELECT 1
    FROM runs
    JOIN tasks
      ON tasks.id = runs.task_id
     AND tasks.company_id = runs.company_id
    WHERE runs.task_id = sqlc.arg(task_id)::uuid
      AND tasks.company_id = sqlc.arg(company_id)::uuid
      AND runs.task_execution_generation = tasks.execution_generation
      AND runs.status IN (
          'queued', 'dispatched', 'waiting_local_directory', 'running', 'deferred'
      )
)::boolean;

-- name: UpdateTaskForRunTerminal :one
-- A terminal Run may affect its Task only while it still owns the Task's
-- execution generation and executing Agent. A newer root Run or a handoff
-- wins without a read/check race in Go: a confirmed handoff to another
-- member advances the execution generation in the same write that moves the
-- Task, so the previous assignee's Run is fenced out here by generation.
-- The Run's authorizer is deliberately NOT compared with the assignee: a Run
-- that was in flight across the private-runtime cutover carries whatever
-- authorizer it was enqueued with, and its Task must still hear its outcome.
-- Completing a Run that requested human input pauses the work in progress;
-- review starts only when there is no pending request or continuation.
UPDATE tasks
SET status = sqlc.arg(status)::text,
    completed_at = CASE
        WHEN sqlc.arg(status)::text IN ('done', 'failed', 'cancelled')
            THEN COALESCE(tasks.completed_at, clock_timestamp())
        ELSE NULL
    END,
    updated_at = clock_timestamp(),
    version = tasks.version + 1
FROM runs
WHERE runs.id = sqlc.arg(run_id)::uuid
  AND runs.company_id = tasks.company_id
  AND runs.task_id = tasks.id
  AND runs.task_execution_generation = tasks.execution_generation
  AND EXISTS (SELECT 1 FROM task_run_agents WHERE task_id = tasks.id AND agent_id = runs.agent_id)
  AND tasks.status = 'in_progress'
  AND tasks.status IS DISTINCT FROM sqlc.arg(status)::text
  AND (
      runs.status <> 'completed'
      OR (
          NOT EXISTS (
              SELECT 1 FROM approvals
              WHERE approvals.company_id = runs.company_id
                AND approvals.origin_run_id = runs.id
                AND approvals.task_id = tasks.id
                AND (approvals.status = 'pending' OR approvals.continuation_status = 'pending')
          )
          AND NOT EXISTS (
              SELECT 1 FROM questions
              WHERE questions.company_id = runs.company_id
                AND questions.origin_run_id = runs.id
                AND questions.task_id = tasks.id
                AND (questions.status = 'pending' OR questions.continuation_status = 'pending')
          )
      )
  )
RETURNING tasks.*;

-- name: InsertTaskWithConversation :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
), identities AS MATERIALIZED (
    SELECT gen_random_uuid()::uuid AS task_id,
           gen_random_uuid()::uuid AS conversation_id
), eligible_company AS MATERIALIZED (
    -- The write fence (ADR 0017): when fence_user_id is set, that person
    -- must hold fence_permission on the Project in this same statement, so a
    -- revoke that slipped in after the service's check fails the write.
    -- NULL is a write no person authorizes here (a Company Plan apply until
    -- it is scoped by Project permission).
    SELECT companies.id
    FROM companies
    WHERE companies.id = sqlc.arg(company_id)::uuid
      AND companies.deleting_at IS NULL
      AND (
          sqlc.narg(fence_user_id)::uuid IS NULL
          OR project_permission_held(
              sqlc.arg(company_id)::uuid,
              sqlc.arg(project_id)::uuid,
              sqlc.narg(fence_user_id)::uuid,
              sqlc.arg(fence_permission)::text,
              sqlc.arg(company_default_permissions)::text[],
              sqlc.arg(management_permissions)::text[],
              sqlc.arg(management_roles)::text[]
          )
      )
), inserted_conversation AS (
    INSERT INTO conversations (
        id, company_id, kind, task_id, project_id, title, created_at, updated_at
    )
    SELECT
        identities.conversation_id,
        eligible_company.id,
        'task',
        identities.task_id,
        sqlc.arg(project_id)::uuid,
        sqlc.arg(title)::text,
        statement_clock.now_at,
        statement_clock.now_at
    FROM eligible_company
    CROSS JOIN identities
    CROSS JOIN statement_clock
    RETURNING conversations.*
), inserted_task AS (
    INSERT INTO tasks (
        id, company_id, project_id, milestone_type,
        conversation_id, number, key, title, description,
        status, priority, labels, start_date, due_date, completed_at,
        assignee_user_id, executed_by_agent_id,
        creator_user_id, creator_agent_id,
        created_at, updated_at
    )
    SELECT
        identities.task_id,
        eligible_company.id,
        sqlc.arg(project_id)::uuid,
        sqlc.arg(milestone_type)::text,
        identities.conversation_id,
        sqlc.arg(number)::bigint,
        sqlc.arg(key)::text,
        sqlc.arg(title)::text,
        sqlc.arg(description)::text,
        sqlc.arg(status)::text,
        sqlc.arg(priority)::text,
        COALESCE(sqlc.narg(labels)::text[], '{}'::text[]),
        sqlc.narg(start_date)::date,
        sqlc.narg(due_date)::date,
        -- A Task born finished carries its outcome time from the start,
        -- under the same predicate UpdateTask applies.
        CASE
            WHEN sqlc.arg(status)::text IN ('done', 'failed', 'cancelled')
                THEN statement_clock.now_at
            ELSE NULL
        END,
        sqlc.arg(assignee_user_id)::uuid,
        sqlc.narg(executed_by_agent_id)::uuid,
        sqlc.narg(creator_user_id)::uuid,
        sqlc.narg(creator_agent_id)::uuid,
        statement_clock.now_at,
        statement_clock.now_at
    FROM eligible_company
    CROSS JOIN identities
    CROSS JOIN statement_clock
    -- The assignee is always an active member (ADR 0014); the executing
    -- Agent, when named, an unarchived Agent of the company.
    WHERE EXISTS (
            SELECT 1 FROM company_members
            WHERE company_members.company_id = eligible_company.id
              AND company_members.user_id = sqlc.arg(assignee_user_id)::uuid
              AND company_members.status = 'active'
        )
      AND (
        sqlc.narg(executed_by_agent_id)::uuid IS NULL
        OR EXISTS (
            SELECT 1 FROM agents
            WHERE agents.id = sqlc.narg(executed_by_agent_id)::uuid
              AND agents.company_id = eligible_company.id
              AND agents.archived_at IS NULL
        )
    )
      AND (
        sqlc.narg(creator_user_id)::uuid IS NULL
        OR EXISTS (
            SELECT 1 FROM company_members
            WHERE company_members.company_id = eligible_company.id
              AND company_members.user_id = sqlc.narg(creator_user_id)::uuid
              AND company_members.status = 'active'
        )
    )
      AND (
        sqlc.narg(creator_agent_id)::uuid IS NULL
        OR EXISTS (
            SELECT 1 FROM agents
            WHERE agents.id = sqlc.narg(creator_agent_id)::uuid
              AND agents.company_id = eligible_company.id
              AND agents.archived_at IS NULL
        )
    )
    RETURNING tasks.*
)
SELECT inserted_task.*
FROM inserted_task
JOIN inserted_conversation
  ON inserted_conversation.id = inserted_task.conversation_id;

-- name: UpdateTask :one
WITH statement_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS now_at
)
UPDATE tasks
SET milestone_type = sqlc.arg(milestone_type)::text,
    title = sqlc.arg(title)::text,
    description = sqlc.arg(description)::text,
    status = sqlc.arg(status)::text,
    priority = sqlc.arg(priority)::text,
    labels = COALESCE(sqlc.narg(labels)::text[], tasks.labels),
    start_date = CASE WHEN sqlc.arg(set_dates)::boolean THEN sqlc.narg(start_date)::date ELSE tasks.start_date END,
    due_date = CASE WHEN sqlc.arg(set_dates)::boolean THEN sqlc.narg(due_date)::date ELSE tasks.due_date END,
    assignee_user_id = sqlc.arg(assignee_user_id)::uuid,
    executed_by_agent_id = sqlc.narg(executed_by_agent_id)::uuid,
    -- A confirmed handoff advances both generations in the same write
    -- (ADR 0014): the execution generation, so every Run of the previous
    -- generation — and every merge job and outcome report fenced on it —
    -- loses its hold on the Task; and the workspace generation, so the next
    -- assignee's daemon prepares a fresh root instead of continuing files
    -- another member left behind.
    execution_generation = CASE
        WHEN sqlc.arg(advance_generation)::boolean THEN tasks.execution_generation + 1
        ELSE tasks.execution_generation
    END,
    workspace_generation = CASE
        WHEN sqlc.arg(advance_generation)::boolean THEN tasks.workspace_generation + 1
        ELSE tasks.workspace_generation
    END,
    completed_at = CASE
        WHEN sqlc.arg(status)::text IN ('done', 'failed', 'cancelled')
            THEN COALESCE(tasks.completed_at, statement_clock.now_at)
        ELSE NULL
    END,
    updated_at = statement_clock.now_at,
    version = tasks.version + 1
FROM statement_clock
WHERE tasks.id = sqlc.arg(task_id)::uuid
  AND tasks.company_id = sqlc.arg(company_id)::uuid
  AND tasks.version = sqlc.arg(expected_version)::bigint
  -- The assignee stays an active member, except that a Task already held by
  -- a departed member keeps them until someone reassigns it.
  AND EXISTS (
      SELECT 1 FROM company_members
      WHERE company_members.company_id = tasks.company_id
        AND company_members.user_id = sqlc.arg(assignee_user_id)::uuid
        AND (
            company_members.status = 'active'
            OR company_members.user_id = tasks.assignee_user_id
        )
  )
  AND (
      sqlc.narg(executed_by_agent_id)::uuid IS NULL
      OR EXISTS (
          SELECT 1 FROM agents
          WHERE agents.id = sqlc.narg(executed_by_agent_id)::uuid
            AND agents.company_id = tasks.company_id
            AND (
                agents.archived_at IS NULL
                OR agents.id = tasks.executed_by_agent_id
            )
      )
  )
  -- The write fence (ADR 0017), as in InsertTaskWithConversation: a person's
  -- write needs fence_permission on the Task's Project in this statement;
  -- NULL is a system writer acting on a Task it already holds.
  AND (
      sqlc.narg(fence_user_id)::uuid IS NULL
      OR project_permission_held(
          tasks.company_id,
          tasks.project_id,
          sqlc.narg(fence_user_id)::uuid,
          sqlc.arg(fence_permission)::text,
          sqlc.arg(company_default_permissions)::text[],
          sqlc.arg(management_permissions)::text[],
          sqlc.arg(management_roles)::text[]
      )
  )
RETURNING tasks.*;

-- name: LockCompanyPlanTasks :many
-- A Company Plan apply's Tasks (ADR 0017): every Task of the Projects in
-- the apply's scope that the applier reads, row-locked in id order. The
-- caller already holds the dependency-graph lock and every one of these
-- Tasks' Conversation locks (LockCompanyPlanTaskConversations), so the
-- Conversation -> Task order of the terminal paths holds.
SELECT tasks.id, tasks.conversation_id, tasks.project_id, tasks.key, tasks.milestone_type,
       tasks.title, tasks.description, tasks.status, tasks.priority, tasks.assignee_user_id,
       tasks.executed_by_agent_id, tasks.execution_generation, tasks.version
FROM tasks
JOIN project_access AS access
  ON access.company_id = tasks.company_id
 AND access.project_id = tasks.project_id
 AND access.user_id = sqlc.arg(actor_user_id)::uuid
WHERE tasks.company_id = sqlc.arg(company_id)::uuid
  AND tasks.project_id = ANY(sqlc.arg(project_ids)::uuid[])
ORDER BY tasks.id
FOR UPDATE OF tasks;

-- name: DeleteCompanyPlanTask :execrows
-- A Company Plan apply deletes a Task its document omits (ADR 0017): only at
-- the revision the apply locked, and only while the applier holds
-- fence_permission (project.tasks.write) on the Task's Project, checked in
-- this statement as every Task write is.
DELETE FROM tasks
WHERE tasks.company_id = sqlc.arg(company_id)::uuid
  AND tasks.id = sqlc.arg(task_id)::uuid
  AND tasks.version = sqlc.arg(expected_version)::bigint
  AND project_permission_held(
      tasks.company_id,
      tasks.project_id,
      sqlc.arg(fence_user_id)::uuid,
      sqlc.arg(fence_permission)::text,
      sqlc.arg(company_default_permissions)::text[],
      sqlc.arg(management_permissions)::text[],
      sqlc.arg(management_roles)::text[]
  );

-- name: AdvanceTaskRevisionAfterDependencyChange :one
-- A dependency mutation changes the Task's public blockedByCount and
-- dependenciesResolved projection even when no tasks column otherwise changes.
-- Advance the same optimistic ordering token every task.updated payload uses,
-- so a delayed frame from before the dependency mutation cannot overwrite it.
UPDATE tasks
SET updated_at = clock_timestamp(),
    version = tasks.version + 1
WHERE tasks.id = sqlc.arg(task_id)::uuid
  AND tasks.company_id = sqlc.arg(company_id)::uuid
  AND tasks.version = sqlc.arg(expected_version)::bigint
RETURNING tasks.*;

-- name: ListTasksForActiveMember :many
-- The Tasks of every Project the caller reads (ADR 0017).
SELECT tasks.*
FROM tasks
JOIN project_access AS access
  ON access.company_id = tasks.company_id
 AND access.project_id = tasks.project_id
 AND access.user_id = sqlc.arg(actor_user_id)::uuid
WHERE tasks.company_id = sqlc.arg(company_id)::uuid
  AND (
      NOT sqlc.arg(has_statuses)::boolean
      OR tasks.status = ANY(sqlc.arg(statuses)::text[])
  )
  AND (
      NOT sqlc.arg(has_priorities)::boolean
      OR tasks.priority = ANY(sqlc.arg(priorities)::text[])
  )
  AND (
      NOT sqlc.arg(has_assignee)::boolean
      OR tasks.assignee_user_id = sqlc.arg(assignee_id)::uuid
  )
  AND (
      NOT sqlc.arg(has_executed_by)::boolean
      OR tasks.executed_by_agent_id = sqlc.arg(executed_by_agent_id)::uuid
  )
  AND (
      NOT sqlc.arg(has_dependencies_resolved)::boolean
      OR sqlc.arg(dependencies_resolved)::boolean = (
          NOT EXISTS (
              SELECT 1
              FROM task_dependencies
              JOIN tasks AS prerequisite
                ON prerequisite.company_id = task_dependencies.company_id
               AND prerequisite.id = task_dependencies.prerequisite_task_id
              WHERE task_dependencies.company_id = tasks.company_id
                AND task_dependencies.dependent_task_id = tasks.id
                AND prerequisite.status <> 'done'
          )
      )
  )
ORDER BY tasks.number DESC;

-- name: TaskKeyTaken :one
-- Whether a Task already holds this key in the company. The mint path asks
-- before inserting so a label-shaped key that reached the table earlier
-- skips that number instead of failing every keyless create on the unique
-- fence.
SELECT EXISTS (
    SELECT 1
    FROM tasks
    WHERE tasks.company_id = sqlc.arg(company_id)::uuid
      AND tasks.key = sqlc.arg(key)::text
)::boolean AS taken;

-- name: ResolveTaskReference :one
-- The Task a person or Agent names instead of its id, for the two Task
-- reads: its key in any letter case, else the {task_prefix}-{number} label
-- the board shows, in the Company's own prefix and any letter case. A key
-- that is exactly the reference wins, then a key equal in any case, then
-- the label, so a key that reads like another Task's label still names its
-- own Task. Keys are unique only in their exact letter case: when two keys
-- equal the reference in some other case, the older Task (lower number)
-- wins, so the same name always names the same Task. label_number is 0 when
-- the reference is not label-shaped. Only Tasks in Projects the caller reads
-- compete (ADR 0017): a hidden Task never shadows a visible one or reveals
-- that its key exists. Only the id comes back; the read that follows still
-- applies the caller's visibility and scope. The scan stays inside one
-- Company's Tasks (company_id leads every tasks index).
SELECT tasks.id
FROM tasks
JOIN companies
  ON companies.id = tasks.company_id
JOIN project_access
  ON project_access.company_id = tasks.company_id
 AND project_access.project_id = tasks.project_id
 AND project_access.user_id = sqlc.arg(actor_user_id)::uuid
WHERE tasks.company_id = sqlc.arg(company_id)::uuid
  AND (
      lower(tasks.key) = lower(sqlc.arg(reference)::text)
      OR (
          tasks.number = sqlc.arg(label_number)::bigint
          AND lower(companies.task_prefix) = lower(sqlc.arg(label_prefix)::text)
      )
  )
ORDER BY
    (tasks.key = sqlc.arg(reference)::text) DESC,
    (lower(tasks.key) = lower(sqlc.arg(reference)::text)) DESC,
    tasks.number
LIMIT 1;

-- name: GetTaskRunAgent :one
SELECT agents.*
FROM task_run_agents
JOIN agents ON agents.company_id = task_run_agents.company_id
 AND agents.id = task_run_agents.agent_id
WHERE task_run_agents.task_id = sqlc.arg(task_id)::uuid
 AND task_run_agents.company_id = sqlc.arg(company_id)::uuid;
