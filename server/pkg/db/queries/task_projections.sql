-- Canonical Task read projection. One statement serves list, exact reads,
-- mutation receipts, events, and the task-workspace snapshot. The filters
-- intentionally mirror ListTasksForActiveMember while the joined fields keep
-- clients from issuing one dependency or actor read per row.
--
-- unready_reason (ADR 0014): why the Task cannot start on its assignee's
-- computer, without naming that computer, or '' when it can: the assignee
-- may run tasks in the Project and has bound the executing or assisting Agent
-- to a runtime of theirs that is online, usable and signed in. Go reads
-- assigneeReady from it, so the two never disagree.
-- Precedence: assignee_inactive > agent_archived > assignee_cannot_run >
-- not_bound > runtime_unusable > runtime_offline > runtime_signed_out > ''
-- (ready).
--
-- assignee_cannot_run (ADR 0017): the assignee does not hold
-- project.tasks.execute on the Task's Project, so a start would be refused
-- with project_permission_required. Its arm states
-- project_permission_held's rule for that one person and permission, row by
-- row, because the function runs as one opaque call per row and is never used
-- in a list (TECH section 10, "Functions"): an active membership and an
-- enabled account (the assignee_inactive arm before it), then a fenced grant
-- row (one primary-key probe), or a fenced membership of a Group holding the
-- permission there (company_group_members_user_idx, then the
-- project_group_grants key), or a Company-visible Project and the permission
-- in the default set, or the permission in the management set and the
-- assignee's role holding the bridge. Go binds the permission and the
-- three sets from auth.ProjectPermissionHeldSets; no permission or role is
-- written here. The Task is read only by its Project's readers, so the
-- reason tells nobody anything about a Project they cannot see.
--
-- Project access (ADR 0017): a Task is listed only when the caller reads its
-- Project (project_access). Each row also carries the facts
-- auth.ProjectAccessOf turns into the caller's permissions on that Project:
-- its visibility and access revision, the project_access via, whether the
-- caller's Company role holds the management bridge (the roles are bound from
-- auth.RolesWith, never written here), the caller's fenced grant rows and
-- what their Groups hold there.
--
-- assignee_outside_project: the Task sits in a restricted Project and its
-- assignee has no project_access row there, so they cannot read or run it.
-- One correlated lookup per row naming the Project and the assignee, never
-- the assignee's whole readable set (TECH "How queries use the views").

-- name: ListTaskProjectionsForActiveMember :many
WITH actor AS MATERIALIZED (
    SELECT member.company_id,
           member.user_id,
           (member.role = ANY(sqlc.arg(management_roles)::text[]))::boolean AS management,
           COALESCE(member.reactivated_at, member.created_at) AS joined_at
    FROM company_members AS member
    WHERE member.company_id = sqlc.arg(company_id)::uuid
      AND member.user_id = sqlc.arg(actor_user_id)::uuid
      AND member.status = 'active'
), readable AS MATERIALIZED (
    -- The Projects the caller reads, decided once per list rather than once
    -- per Task, with the caller's fenced grant rows on each and what their
    -- Groups hold there (fenced memberships, one indexed probe per Project).
    -- The list then reaches each Project's Tasks through the (company,
    -- project) index.
    SELECT access.project_id,
           access.via,
           project.visibility,
           project.access_version,
           COALESCE(
               array_agg(grant_row.permission ORDER BY grant_row.permission)
                   FILTER (WHERE grant_row.permission IS NOT NULL),
               '{}'
           )::text[] AS granted_permissions,
           COALESCE((
               SELECT array_agg(DISTINCT group_grant.permission ORDER BY group_grant.permission)
               FROM company_group_members AS group_member
               JOIN project_group_grants AS group_grant
                 ON group_grant.company_id = group_member.company_id
                AND group_grant.project_id = access.project_id
                AND group_grant.group_id = group_member.group_id
               WHERE group_member.company_id = sqlc.arg(company_id)::uuid
                 AND group_member.user_id = sqlc.arg(actor_user_id)::uuid
                 AND group_member.created_at >= actor.joined_at
           ), '{}')::text[] AS group_permissions
    FROM actor
    JOIN project_access AS access
      ON access.company_id = sqlc.arg(company_id)::uuid
     AND access.user_id = sqlc.arg(actor_user_id)::uuid
    JOIN projects AS project
      ON project.company_id = access.company_id
     AND project.id = access.project_id
    LEFT JOIN project_grants AS grant_row
      ON grant_row.company_id = sqlc.arg(company_id)::uuid
     AND grant_row.user_id = sqlc.arg(actor_user_id)::uuid
     AND grant_row.project_id = access.project_id
     AND grant_row.updated_at >= actor.joined_at
    WHERE (NOT sqlc.arg(has_project_id)::boolean OR access.project_id = sqlc.arg(project_id)::uuid)
      -- A read of one Task decides access on its Project only, not on
      -- every Project the caller reads.
      AND (
          NOT sqlc.arg(has_task_id)::boolean
          OR access.project_id = (
              SELECT one_task.project_id
              FROM tasks AS one_task
              WHERE one_task.company_id = sqlc.arg(company_id)::uuid
                AND one_task.id = sqlc.arg(task_id)::uuid
          )
      )
    GROUP BY access.project_id, access.via, project.visibility, project.access_version, actor.joined_at
)
SELECT
    sqlc.embed(tasks),
    readable.visibility AS project_visibility,
    readable.access_version AS project_access_version,
    readable.via AS access_via,
    actor.management AS access_management,
    readable.granted_permissions,
    readable.group_permissions,
    (
        readable.visibility = 'restricted'
        AND (
            SELECT 1
            FROM project_access AS assignee_access
            WHERE assignee_access.company_id = tasks.company_id
              AND assignee_access.project_id = tasks.project_id
              AND assignee_access.user_id = tasks.assignee_user_id
            LIMIT 1
        ) IS NULL
    )::boolean AS assignee_outside_project,
    COALESCE(assignee_users.display_name, '')::text AS assignee_display_name,
    assignee_users.avatar_url AS assignee_avatar_url,
    COALESCE(executed_by_agents.name, '')::text AS executed_by_display_name,
    run_agents.id AS run_agent_id,
    COALESCE(run_agents.name, '')::text AS run_agent_name,
    COALESCE(creator_users.display_name, creator_agents.name, '')::text
        AS creator_display_name,
    creator_users.avatar_url AS creator_avatar_url,
    dependency_counts.blocked_by_count,
    (dependency_counts.unresolved_count = 0)::boolean AS dependencies_resolved,
    CASE
        WHEN assignee_membership.status IS DISTINCT FROM 'active'
          OR assignee_users.disabled_at IS NOT NULL
          OR assignee_users.deleted_at IS NOT NULL THEN 'assignee_inactive'
        WHEN run_agents.id IS NULL OR run_agents.archived_at IS NOT NULL THEN 'agent_archived'
        WHEN (
            (
                SELECT true
                FROM project_grants AS execute_grant
                WHERE execute_grant.company_id = tasks.company_id
                  AND execute_grant.project_id = tasks.project_id
                  AND execute_grant.user_id = tasks.assignee_user_id
                  AND execute_grant.permission = sqlc.arg(execute_permission)::text
                  AND execute_grant.updated_at >= COALESCE(
                      assignee_membership.reactivated_at, assignee_membership.created_at
                  )
                LIMIT 1
            ) IS NOT NULL
            OR (
                SELECT true
                FROM company_group_members AS execute_member
                JOIN project_group_grants AS execute_group_grant
                  ON execute_group_grant.company_id = execute_member.company_id
                 AND execute_group_grant.project_id = tasks.project_id
                 AND execute_group_grant.group_id = execute_member.group_id
                 AND execute_group_grant.permission = sqlc.arg(execute_permission)::text
                WHERE execute_member.company_id = tasks.company_id
                  AND execute_member.user_id = tasks.assignee_user_id
                  AND execute_member.created_at >= COALESCE(
                      assignee_membership.reactivated_at, assignee_membership.created_at
                  )
                LIMIT 1
            ) IS NOT NULL
            OR (
                readable.visibility = 'company'
                AND sqlc.arg(execute_permission)::text = ANY(sqlc.arg(company_default_permissions)::text[])
            )
            OR (
                sqlc.arg(execute_permission)::text = ANY(sqlc.arg(management_permissions)::text[])
                AND assignee_membership.role = ANY(sqlc.arg(management_roles)::text[])
            )
        ) IS NOT TRUE THEN 'assignee_cannot_run'
        WHEN assignee_runtimes.id IS NULL THEN 'not_bound'
        WHEN assignee_runtimes.unusable_reason_code IS NOT NULL THEN 'runtime_unusable'
        WHEN assignee_runtimes.status <> 'online' THEN 'runtime_offline'
        WHEN assignee_runtimes.provider_sign_in_state = 'signed_out' THEN 'runtime_signed_out'
        ELSE ''
    END::text AS unready_reason
FROM readable
JOIN tasks
  ON tasks.company_id = sqlc.arg(company_id)::uuid
 AND tasks.project_id = readable.project_id
CROSS JOIN actor
JOIN conversations
  ON conversations.company_id = tasks.company_id
 AND conversations.id = tasks.conversation_id
 AND (
      conversations.kind = 'task'
      OR conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
 )
LEFT JOIN users AS assignee_users
  ON assignee_users.id = tasks.assignee_user_id
LEFT JOIN company_members AS assignee_membership
  ON assignee_membership.company_id = tasks.company_id
 AND assignee_membership.user_id = tasks.assignee_user_id
LEFT JOIN agents AS executed_by_agents
  ON executed_by_agents.company_id = tasks.company_id
 AND executed_by_agents.id = tasks.executed_by_agent_id
-- Correlated lookup: a plain join to this view reads tasks a second time and
-- plans as a scan of every Task (TECH section 10).
LEFT JOIN LATERAL (
    SELECT run_agent.agent_id FROM task_run_agents AS run_agent
    WHERE run_agent.task_id = tasks.id AND run_agent.company_id = tasks.company_id
    LIMIT 1
) AS task_run_agents ON true
LEFT JOIN agents AS run_agents
  ON run_agents.company_id = tasks.company_id
 AND run_agents.id = task_run_agents.agent_id
LEFT JOIN agent_runtime_bindings AS assignee_binding
  ON assignee_binding.company_id = tasks.company_id
 AND assignee_binding.agent_id = task_run_agents.agent_id
 AND assignee_binding.user_id = tasks.assignee_user_id
LEFT JOIN runtimes AS assignee_runtimes
  ON assignee_runtimes.company_id = assignee_binding.company_id
 AND assignee_runtimes.id = assignee_binding.runtime_id
 AND assignee_runtimes.removed_at IS NULL
 AND assignee_runtimes.owner_user_id = assignee_binding.user_id
LEFT JOIN users AS creator_users
  ON creator_users.id = tasks.creator_user_id
LEFT JOIN agents AS creator_agents
  ON creator_agents.company_id = tasks.company_id
 AND creator_agents.id = tasks.creator_agent_id
LEFT JOIN LATERAL (
    SELECT
        count(*)::bigint AS blocked_by_count,
        count(*) FILTER (WHERE prerequisite.status <> 'done')::bigint
            AS unresolved_count
    FROM task_dependencies
    JOIN tasks AS prerequisite
      ON prerequisite.company_id = task_dependencies.company_id
     AND prerequisite.id = task_dependencies.prerequisite_task_id
    WHERE task_dependencies.company_id = tasks.company_id
      AND task_dependencies.dependent_task_id = tasks.id
) AS dependency_counts ON true
WHERE tasks.company_id = sqlc.arg(company_id)::uuid
 AND (NOT sqlc.arg(has_project_id)::boolean OR tasks.project_id = sqlc.arg(project_id)::uuid)
  AND (
      NOT sqlc.arg(has_task_id)::boolean
      OR tasks.id = sqlc.arg(task_id)::uuid
  )
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
      OR (tasks.id = sqlc.narg(assisted_task_id)::uuid
          AND task_run_agents.agent_id = sqlc.arg(executed_by_agent_id)::uuid)
  )
  AND (
      NOT sqlc.arg(has_dependencies_resolved)::boolean
      OR sqlc.arg(dependencies_resolved)::boolean =
          (dependency_counts.unresolved_count = 0)
  )
ORDER BY tasks.number DESC;

-- name: GetTaskProjectionForSystem :one
-- Background lifecycle writers already hold Company/Task authority and have
-- no member principal. They still publish the exact canonical Task response,
-- so this is the same projection without the active-member visibility join.
SELECT
    sqlc.embed(tasks),
    (
        task_project.visibility = 'restricted'
        AND (
            SELECT 1
            FROM project_access AS assignee_access
            WHERE assignee_access.company_id = tasks.company_id
              AND assignee_access.project_id = tasks.project_id
              AND assignee_access.user_id = tasks.assignee_user_id
            LIMIT 1
        ) IS NULL
    )::boolean AS assignee_outside_project,
    COALESCE(assignee_users.display_name, '')::text AS assignee_display_name,
    assignee_users.avatar_url AS assignee_avatar_url,
    COALESCE(executed_by_agents.name, '')::text AS executed_by_display_name,
    run_agents.id AS run_agent_id,
    COALESCE(run_agents.name, '')::text AS run_agent_name,
    COALESCE(creator_users.display_name, creator_agents.name, '')::text
        AS creator_display_name,
    creator_users.avatar_url AS creator_avatar_url,
    dependency_counts.blocked_by_count,
    (dependency_counts.unresolved_count = 0)::boolean AS dependencies_resolved,
    CASE
        WHEN assignee_membership.status IS DISTINCT FROM 'active'
          OR assignee_users.disabled_at IS NOT NULL
          OR assignee_users.deleted_at IS NOT NULL THEN 'assignee_inactive'
        WHEN run_agents.id IS NULL OR run_agents.archived_at IS NOT NULL THEN 'agent_archived'
        WHEN (
            (
                SELECT true
                FROM project_grants AS execute_grant
                WHERE execute_grant.company_id = tasks.company_id
                  AND execute_grant.project_id = tasks.project_id
                  AND execute_grant.user_id = tasks.assignee_user_id
                  AND execute_grant.permission = sqlc.arg(execute_permission)::text
                  AND execute_grant.updated_at >= COALESCE(
                      assignee_membership.reactivated_at, assignee_membership.created_at
                  )
                LIMIT 1
            ) IS NOT NULL
            OR (
                SELECT true
                FROM company_group_members AS execute_member
                JOIN project_group_grants AS execute_group_grant
                  ON execute_group_grant.company_id = execute_member.company_id
                 AND execute_group_grant.project_id = tasks.project_id
                 AND execute_group_grant.group_id = execute_member.group_id
                 AND execute_group_grant.permission = sqlc.arg(execute_permission)::text
                WHERE execute_member.company_id = tasks.company_id
                  AND execute_member.user_id = tasks.assignee_user_id
                  AND execute_member.created_at >= COALESCE(
                      assignee_membership.reactivated_at, assignee_membership.created_at
                  )
                LIMIT 1
            ) IS NOT NULL
            OR (
                task_project.visibility = 'company'
                AND sqlc.arg(execute_permission)::text = ANY(sqlc.arg(company_default_permissions)::text[])
            )
            OR (
                sqlc.arg(execute_permission)::text = ANY(sqlc.arg(management_permissions)::text[])
                AND assignee_membership.role = ANY(sqlc.arg(management_roles)::text[])
            )
        ) IS NOT TRUE THEN 'assignee_cannot_run'
        WHEN assignee_runtimes.id IS NULL THEN 'not_bound'
        WHEN assignee_runtimes.unusable_reason_code IS NOT NULL THEN 'runtime_unusable'
        WHEN assignee_runtimes.status <> 'online' THEN 'runtime_offline'
        WHEN assignee_runtimes.provider_sign_in_state = 'signed_out' THEN 'runtime_signed_out'
        ELSE ''
    END::text AS unready_reason
FROM tasks
JOIN companies
  ON companies.id = tasks.company_id
 AND companies.deleting_at IS NULL
JOIN projects AS task_project
  ON task_project.company_id = tasks.company_id
 AND task_project.id = tasks.project_id
JOIN conversations
  ON conversations.company_id = tasks.company_id
 AND conversations.id = tasks.conversation_id
LEFT JOIN users AS assignee_users
  ON assignee_users.id = tasks.assignee_user_id
LEFT JOIN company_members AS assignee_membership
  ON assignee_membership.company_id = tasks.company_id
 AND assignee_membership.user_id = tasks.assignee_user_id
LEFT JOIN agents AS executed_by_agents
  ON executed_by_agents.company_id = tasks.company_id
 AND executed_by_agents.id = tasks.executed_by_agent_id
-- Correlated lookup: a plain join to this view reads tasks a second time and
-- plans as a scan of every Task (TECH section 10).
LEFT JOIN LATERAL (
    SELECT run_agent.agent_id FROM task_run_agents AS run_agent
    WHERE run_agent.task_id = tasks.id AND run_agent.company_id = tasks.company_id
    LIMIT 1
) AS task_run_agents ON true
LEFT JOIN agents AS run_agents
  ON run_agents.company_id = tasks.company_id
 AND run_agents.id = task_run_agents.agent_id
LEFT JOIN agent_runtime_bindings AS assignee_binding
  ON assignee_binding.company_id = tasks.company_id
 AND assignee_binding.agent_id = task_run_agents.agent_id
 AND assignee_binding.user_id = tasks.assignee_user_id
LEFT JOIN runtimes AS assignee_runtimes
  ON assignee_runtimes.company_id = assignee_binding.company_id
 AND assignee_runtimes.id = assignee_binding.runtime_id
 AND assignee_runtimes.removed_at IS NULL
 AND assignee_runtimes.owner_user_id = assignee_binding.user_id
LEFT JOIN users AS creator_users
  ON creator_users.id = tasks.creator_user_id
LEFT JOIN agents AS creator_agents
  ON creator_agents.company_id = tasks.company_id
 AND creator_agents.id = tasks.creator_agent_id
LEFT JOIN LATERAL (
    SELECT
        count(*)::bigint AS blocked_by_count,
        count(*) FILTER (WHERE prerequisite.status <> 'done')::bigint
            AS unresolved_count
    FROM task_dependencies
    JOIN tasks AS prerequisite
      ON prerequisite.company_id = task_dependencies.company_id
     AND prerequisite.id = task_dependencies.prerequisite_task_id
    WHERE task_dependencies.company_id = tasks.company_id
      AND task_dependencies.dependent_task_id = tasks.id
) AS dependency_counts ON true
WHERE tasks.company_id = sqlc.arg(company_id)::uuid
  AND tasks.id = sqlc.arg(task_id)::uuid;
