-- name: LockTaskDependencyGraph :exec
SELECT pg_advisory_xact_lock(hashtextextended(
    'boardly/task-dependency-graph/v1/' || sqlc.arg(company_id)::uuid::text,
    0
));

-- name: LockTaskDependencyEndpoints :many
-- Locks both endpoints of a dependency the caller is changing. An endpoint
-- whose Project the caller cannot read (task_access, ADR 0017) is neither
-- returned nor locked, so a caller that gets fewer than two rows answers
-- task_not_found for the hidden one exactly as for a missing one.
SELECT tasks.*
FROM tasks
JOIN task_access AS access
  ON access.company_id = tasks.company_id
 AND access.task_id = tasks.id
 AND access.user_id = sqlc.arg(actor_user_id)::uuid
WHERE tasks.company_id = sqlc.arg(company_id)::uuid
  AND tasks.id IN (
      sqlc.arg(dependent_task_id)::uuid,
      sqlc.arg(prerequisite_task_id)::uuid
  )
ORDER BY tasks.id
FOR UPDATE OF tasks;

-- name: LockCompanyPlanTaskDependencies :many
-- A Company Plan apply's dependency edges (ADR 0017): every edge inside the
-- Projects in the apply's scope that the applier reads, row-locked in a
-- fixed order. An edge never leaves its Project, so the edge's own Project
-- decides.
SELECT task_dependencies.dependent_task_id,
       task_dependencies.prerequisite_task_id,
       task_dependencies.version
FROM task_dependencies
JOIN project_access AS access
  ON access.company_id = task_dependencies.company_id
 AND access.project_id = task_dependencies.project_id
 AND access.user_id = sqlc.arg(actor_user_id)::uuid
WHERE task_dependencies.company_id = sqlc.arg(company_id)::uuid
  AND task_dependencies.project_id = ANY(sqlc.arg(project_ids)::uuid[])
ORDER BY task_dependencies.dependent_task_id, task_dependencies.prerequisite_task_id
FOR UPDATE OF task_dependencies;

-- name: ListTaskDependencyProjections :many
-- Both directions of one Task's dependencies. Dependencies stay inside one
-- Project, but the other endpoint is still read through task_access for the
-- caller (ADR 0017), so no row reaches a person who cannot read it.
SELECT
    sqlc.embed(task_dependencies),
    CASE
        WHEN task_dependencies.dependent_task_id = sqlc.arg(task_id)::uuid
            THEN 'blocked_by'
        ELSE 'blocks'
    END::text AS direction,
    related_task.id AS task_id,
    related_task.key AS task_key,
    related_task.number AS task_number,
    related_task.title AS task_title,
    related_task.status AS task_status,
    related_task.assignee_user_id AS task_assignee_id,
    COALESCE(assignee_users.display_name, '')::text AS task_assignee_display_name,
    assignee_users.avatar_url AS task_assignee_avatar_url,
    related_task.executed_by_agent_id AS task_executed_by_agent_id,
    COALESCE(executed_by_agents.name, '')::text AS task_executed_by_display_name,
    related_task.version AS task_revision
FROM task_dependencies
JOIN tasks AS related_task
  ON related_task.company_id = task_dependencies.company_id
 AND related_task.id = CASE
      WHEN task_dependencies.dependent_task_id = sqlc.arg(task_id)::uuid
          THEN task_dependencies.prerequisite_task_id
      ELSE task_dependencies.dependent_task_id
 END
JOIN task_access AS related_access
  ON related_access.company_id = related_task.company_id
 AND related_access.task_id = related_task.id
 AND related_access.user_id = sqlc.arg(actor_user_id)::uuid
LEFT JOIN users AS assignee_users
  ON assignee_users.id = related_task.assignee_user_id
LEFT JOIN agents AS executed_by_agents
  ON executed_by_agents.company_id = related_task.company_id
 AND executed_by_agents.id = related_task.executed_by_agent_id
WHERE task_dependencies.company_id = sqlc.arg(company_id)::uuid
  AND (
      task_dependencies.dependent_task_id = sqlc.arg(task_id)::uuid
      OR task_dependencies.prerequisite_task_id = sqlc.arg(task_id)::uuid
  )
ORDER BY direction, related_task.id;

-- name: ListRunTaskPrerequisiteBriefs :many
-- Only completed prerequisites are executable context. The Task mutation
-- boundary rejects unresolved dependency graphs before enqueue; this query
-- freezes the completed rows the provider may consult after claim.
SELECT
    prerequisite.id,
    prerequisite.conversation_id,
    prerequisite.key,
    prerequisite.title,
    prerequisite.description,
    prerequisite.status,
    prerequisite.completed_at
FROM task_dependencies
JOIN tasks AS prerequisite
  ON prerequisite.id = task_dependencies.prerequisite_task_id
 AND prerequisite.company_id = task_dependencies.company_id
WHERE task_dependencies.company_id = sqlc.arg(company_id)::uuid
  AND task_dependencies.dependent_task_id = sqlc.arg(dependent_task_id)::uuid
  AND prerequisite.status = 'done'
ORDER BY prerequisite.key, prerequisite.id;

-- name: LockDependentTasksForProjectionChange :many
-- A prerequisite crossing `done` flips every dependent's public
-- dependenciesResolved projection. The dependents are locked here so the
-- revision advance that announces the flip cannot race a concurrent write
-- to one of them.
SELECT dependent.id, dependent.version
FROM task_dependencies
JOIN tasks AS dependent
  ON dependent.company_id = task_dependencies.company_id
 AND dependent.id = task_dependencies.dependent_task_id
WHERE task_dependencies.company_id = sqlc.arg(company_id)::uuid
  AND task_dependencies.prerequisite_task_id = sqlc.arg(prerequisite_task_id)::uuid
ORDER BY dependent.id
FOR UPDATE OF dependent;

-- name: ListCompanyTaskDependencyGraph :many
-- The dependency edges of every Project the caller reads (ADR 0017). Both
-- endpoints share the edge's Project, so one project_access match covers
-- them.
SELECT
    task_dependencies.company_id,
    task_dependencies.project_id,
    task_dependencies.dependent_task_id,
    task_dependencies.prerequisite_task_id,
    task_dependencies.created_at,
    task_dependencies.version,
    dependent.key AS dependent_key,
    dependent.number AS dependent_number,
    dependent.title AS dependent_title,
    dependent.status AS dependent_status,
    prerequisite.key AS prerequisite_key,
    prerequisite.number AS prerequisite_number,
    prerequisite.title AS prerequisite_title,
    prerequisite.status AS prerequisite_status
FROM task_dependencies
JOIN tasks AS dependent
  ON dependent.id = task_dependencies.dependent_task_id
 AND dependent.company_id = task_dependencies.company_id
JOIN tasks AS prerequisite
  ON prerequisite.id = task_dependencies.prerequisite_task_id
 AND prerequisite.company_id = task_dependencies.company_id
JOIN project_access AS access
  ON access.company_id = task_dependencies.company_id
 AND access.project_id = task_dependencies.project_id
 AND access.user_id = sqlc.arg(actor_user_id)::uuid
WHERE task_dependencies.company_id = sqlc.arg(company_id)::uuid
ORDER BY task_dependencies.prerequisite_task_id, task_dependencies.dependent_task_id;

-- name: TaskDependencyPathExists :one
WITH RECURSIVE reachable(task_id) AS (
    SELECT sqlc.arg(from_task_id)::uuid
    UNION
    SELECT task_dependencies.dependent_task_id
    FROM task_dependencies
    JOIN reachable
      ON task_dependencies.prerequisite_task_id = reachable.task_id
    WHERE task_dependencies.company_id = sqlc.arg(company_id)::uuid
)
SELECT EXISTS (
    SELECT 1
    FROM reachable
    WHERE task_id = sqlc.arg(to_task_id)::uuid
)::boolean;

-- name: TaskHasUnresolvedDependencies :one
SELECT EXISTS (
    SELECT 1
    FROM task_dependencies
    JOIN tasks AS prerequisite
      ON prerequisite.id = task_dependencies.prerequisite_task_id
     AND prerequisite.company_id = task_dependencies.company_id
    WHERE task_dependencies.company_id = sqlc.arg(company_id)::uuid
      AND task_dependencies.dependent_task_id = sqlc.arg(task_id)::uuid
      AND prerequisite.status <> 'done'
)::boolean;

-- name: InsertTaskDependency :one
INSERT INTO task_dependencies (
    project_id,
    company_id,
    dependent_task_id,
    prerequisite_task_id,
    created_by_user_id,
    created_by_agent_id
)
VALUES (
    (SELECT project_id FROM tasks WHERE company_id = sqlc.arg(company_id)::uuid AND id = sqlc.arg(dependent_task_id)::uuid),
    sqlc.arg(company_id)::uuid,
    sqlc.arg(dependent_task_id)::uuid,
    sqlc.arg(prerequisite_task_id)::uuid,
    sqlc.narg(created_by_user_id)::uuid,
    sqlc.narg(created_by_agent_id)::uuid
)
RETURNING *;

-- name: DeleteTaskDependency :one
DELETE FROM task_dependencies
WHERE company_id = sqlc.arg(company_id)::uuid
  AND dependent_task_id = sqlc.arg(dependent_task_id)::uuid
  AND prerequisite_task_id = sqlc.arg(prerequisite_task_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
RETURNING *;
