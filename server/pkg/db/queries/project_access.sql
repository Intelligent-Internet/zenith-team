-- Project access (ADR 0017). The rule lives in the project_access view and
-- the project_permission_held function; these statements
-- read and change the rows behind them. Go binds every permission set and
-- role list (auth.CompanyProjectDefaultPermissions,
-- auth.ManagementProjectPermissions, auth.RolesWith(projects.manage)), so no
-- permission or role name is written here.

-- name: ResolveProjectAccess :one
-- One caller's standing on one Project: the Project's access facts, the
-- project_access row (via is '' when there is none), whether the caller's
-- active Company role holds the management bridge, the caller's fenced
-- grant rows, and what their Groups hold there through memberships written
-- since their latest (re)join (one probe of company_group_members_user_idx,
-- then the project_group_grants key per Group). Go adds the Company default
-- set and the bridge to the rows.
WITH actor AS MATERIALIZED (
    SELECT member.company_id, member.user_id, member.role,
           COALESCE(member.reactivated_at, member.created_at) AS joined_at
    FROM company_members AS member
    JOIN companies AS company
      ON company.id = member.company_id AND company.deleting_at IS NULL
    JOIN users AS account
      ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
    WHERE member.company_id = sqlc.arg(company_id)::uuid
      AND member.user_id = sqlc.arg(actor_user_id)::uuid
      AND member.status = 'active'
)
SELECT projects.id,
       projects.company_id,
       projects.visibility,
       projects.access_version,
       projects.archived_at,
       COALESCE(access.via, '')::text AS via,
       COALESCE(actor.role = ANY(sqlc.arg(management_roles)::text[]), false)::boolean AS management,
       COALESCE((
           SELECT array_agg(grant_row.permission ORDER BY grant_row.permission)
           FROM project_grants AS grant_row
           WHERE grant_row.company_id = actor.company_id
             AND grant_row.project_id = projects.id
             AND grant_row.user_id = actor.user_id
             AND grant_row.updated_at >= actor.joined_at
       ), '{}')::text[] AS granted_permissions,
       COALESCE((
           SELECT array_agg(DISTINCT group_grant.permission ORDER BY group_grant.permission)
           FROM company_group_members AS group_member
           JOIN project_group_grants AS group_grant
             ON group_grant.company_id = group_member.company_id
            AND group_grant.project_id = projects.id
            AND group_grant.group_id = group_member.group_id
           WHERE group_member.company_id = actor.company_id
             AND group_member.user_id = actor.user_id
             AND group_member.created_at >= actor.joined_at
       ), '{}')::text[] AS group_permissions
FROM projects
LEFT JOIN actor ON actor.company_id = projects.company_id
LEFT JOIN project_access AS access
  ON access.company_id = projects.company_id
 AND access.project_id = projects.id
 AND access.user_id = sqlc.arg(actor_user_id)::uuid
WHERE projects.company_id = sqlc.arg(company_id)::uuid
  AND projects.id = sqlc.arg(project_id)::uuid;

-- name: ResolvePeopleProjectAccess :many
-- ResolveProjectAccess for many people on many Projects in one read, for a
-- Group change: one row per Project of the Company and
-- person named, with the same facts. The people's fenced own rows and Group
-- rows on the Projects are read once, as sets; the project_access via is a
-- correlated lookup that names the Project and the person, so each arm keeps
-- its index. A person who is not an active member with an enabled account
-- holds no rows and no via.
WITH person AS MATERIALIZED (
    SELECT member.user_id, member.role,
           COALESCE(member.reactivated_at, member.created_at) AS joined_at
    FROM company_members AS member
    JOIN companies AS company
      ON company.id = member.company_id AND company.deleting_at IS NULL
    JOIN users AS account
      ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
    WHERE member.company_id = sqlc.arg(company_id)::uuid
      AND member.user_id = ANY(sqlc.arg(user_ids)::uuid[])
      AND member.status = 'active'
), granted AS (
    SELECT grant_row.project_id, grant_row.user_id,
           array_agg(grant_row.permission ORDER BY grant_row.permission) AS permissions
    FROM person
    JOIN project_grants AS grant_row
      ON grant_row.company_id = sqlc.arg(company_id)::uuid
     AND grant_row.user_id = person.user_id
     AND grant_row.project_id = ANY(sqlc.arg(project_ids)::uuid[])
     AND grant_row.updated_at >= person.joined_at
    GROUP BY grant_row.project_id, grant_row.user_id
), grouped AS (
    SELECT group_grant.project_id, group_member.user_id,
           array_agg(DISTINCT group_grant.permission ORDER BY group_grant.permission) AS permissions
    FROM person
    JOIN company_group_members AS group_member
      ON group_member.company_id = sqlc.arg(company_id)::uuid
     AND group_member.user_id = person.user_id
     AND group_member.created_at >= person.joined_at
    JOIN project_group_grants AS group_grant
      ON group_grant.company_id = group_member.company_id
     AND group_grant.group_id = group_member.group_id
     AND group_grant.project_id = ANY(sqlc.arg(project_ids)::uuid[])
    GROUP BY group_grant.project_id, group_member.user_id
)
SELECT projects.id,
       projects.company_id,
       projects.visibility,
       projects.access_version,
       candidate.user_id::uuid AS user_id,
       COALESCE(access.via, '')::text AS via,
       COALESCE(person.role = ANY(sqlc.arg(management_roles)::text[]), false)::boolean AS management,
       COALESCE(granted.permissions, '{}')::text[] AS granted_permissions,
       COALESCE(grouped.permissions, '{}')::text[] AS group_permissions
FROM projects
CROSS JOIN unnest(sqlc.arg(user_ids)::uuid[]) AS candidate(user_id)
LEFT JOIN person ON person.user_id = candidate.user_id
LEFT JOIN granted ON granted.project_id = projects.id AND granted.user_id = candidate.user_id
LEFT JOIN grouped ON grouped.project_id = projects.id AND grouped.user_id = candidate.user_id
LEFT JOIN LATERAL (
    SELECT project_access.via
    FROM project_access
    WHERE project_access.company_id = projects.company_id
      AND project_access.project_id = projects.id
      AND project_access.user_id = candidate.user_id
    LIMIT 1
) AS access ON true
WHERE projects.company_id = sqlc.arg(company_id)::uuid
  AND projects.id = ANY(sqlc.arg(project_ids)::uuid[])
ORDER BY projects.id, candidate.user_id;

-- name: GetTaskProjectID :one
-- The Project a Task belongs to, for a Task-scoped access check.
SELECT tasks.project_id
FROM tasks
WHERE tasks.company_id = sqlc.arg(company_id)::uuid
  AND tasks.id = sqlc.arg(task_id)::uuid;

-- name: GetConversationProjectID :one
-- The Project a chat belongs to: set on every Task chat, kept after its Task
-- is deleted, NULL on every other kind and on a Task chat whose Task was
-- deleted before Task chats recorded their Project (the legacy pocket, ADR
-- 0017).
SELECT conversations.kind, conversations.project_id
FROM conversations
WHERE conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.id = sqlc.arg(conversation_id)::uuid;

-- name: GetProjectAccessFacts :one
-- The Project's visibility and access revision, read without a lock. Frame
-- producers call it while they already hold Conversation and Run locks, so it
-- must not wait on the Project row. A frame read just before a revocation
-- commits can still reach the person who lost access (ADR 0017 accepts it);
-- project.access_changed then makes their client refetch. The title is what
-- a member's leaving records in each Project's Activity row.
SELECT projects.id, projects.visibility, projects.access_version, projects.title
FROM projects
WHERE projects.company_id = sqlc.arg(company_id)::uuid
  AND projects.id = sqlc.arg(project_id)::uuid;

-- name: LockProjectForAccessRead :one
-- Holds the Project's access facts steady while a write that depends on them
-- commits. Lock order: company -> member -> project -> conversation -> run,
-- so it is taken before any Conversation or Run lock in the transaction.
SELECT projects.id, projects.visibility, projects.access_version
FROM projects
WHERE projects.company_id = sqlc.arg(company_id)::uuid
  AND projects.id = sqlc.arg(project_id)::uuid
FOR SHARE;

-- name: LockProjectForAccessChange :one
-- Taken before a grant or visibility change; waits for every FOR SHARE reader.
-- Same lock order as LockProjectForAccessRead.
SELECT projects.id, projects.visibility, projects.access_version
FROM projects
WHERE projects.company_id = sqlc.arg(company_id)::uuid
  AND projects.id = sqlc.arg(project_id)::uuid
FOR UPDATE;

-- name: ListProjectAccessPaths :many
-- Every stored source of every person on the Project: one row per person
-- and source, their own grant rows (via 'grant', group_id the zero uuid) and
-- each Group they are in that holds rows there (via 'group'). Only active
-- members with enabled accounts, only own rows written since their latest
-- (re)join and only memberships added since then, so the list agrees with
-- project_access and project_access_paths. updated_at is the source's latest
-- change (for a Group, its latest grant row or when the person was added,
-- whichever is later); granted_by_user_id is the author of the person's most
-- recent own row, NULL on a Group row. management says whether the person's
-- Company role holds the management bridge. The Company-visible floor and the
-- bridge are not stored, so Go adds them as sources.
WITH path AS (
    SELECT grant_row.user_id,
           'grant'::text AS via,
           '00000000-0000-0000-0000-000000000000'::uuid AS group_id,
           array_agg(grant_row.permission ORDER BY grant_row.permission)::text[] AS permissions,
           max(grant_row.updated_at)::timestamptz AS updated_at
    FROM project_grants AS grant_row
    JOIN company_members AS member
      ON member.company_id = grant_row.company_id
     AND member.user_id = grant_row.user_id
     AND member.status = 'active'
    JOIN users AS account
      ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
    WHERE grant_row.company_id = sqlc.arg(company_id)::uuid
      AND grant_row.project_id = sqlc.arg(project_id)::uuid
      AND grant_row.updated_at >= COALESCE(member.reactivated_at, member.created_at)
    GROUP BY grant_row.user_id
    UNION ALL
    SELECT group_member.user_id,
           'group'::text AS via,
           group_member.group_id,
           array_agg(group_grant.permission ORDER BY group_grant.permission)::text[] AS permissions,
           GREATEST(max(group_grant.updated_at), group_member.created_at)::timestamptz AS updated_at
    FROM project_group_grants AS group_grant
    JOIN company_group_members AS group_member
      ON group_member.company_id = group_grant.company_id
     AND group_member.group_id = group_grant.group_id
    JOIN company_members AS member
      ON member.company_id = group_member.company_id
     AND member.user_id = group_member.user_id
     AND member.status = 'active'
    JOIN users AS account
      ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
    WHERE group_grant.company_id = sqlc.arg(company_id)::uuid
      AND group_grant.project_id = sqlc.arg(project_id)::uuid
      AND group_member.created_at >= COALESCE(member.reactivated_at, member.created_at)
    GROUP BY group_member.user_id, group_member.group_id, group_member.created_at
)
SELECT path.user_id, path.via, path.group_id, path.permissions, path.updated_at,
       latest.granted_by_user_id,
       (member.role = ANY(sqlc.arg(management_roles)::text[]))::boolean AS management
FROM path
JOIN company_members AS member
  ON member.company_id = sqlc.arg(company_id)::uuid
 AND member.user_id = path.user_id
LEFT JOIN LATERAL (
    SELECT grant_row.granted_by_user_id
    FROM project_grants AS grant_row
    WHERE path.via = 'grant'
      AND grant_row.company_id = sqlc.arg(company_id)::uuid
      AND grant_row.project_id = sqlc.arg(project_id)::uuid
      AND grant_row.user_id = path.user_id
    ORDER BY grant_row.updated_at DESC, grant_row.permission
    LIMIT 1
) AS latest ON true
ORDER BY path.user_id, path.via, path.group_id;

-- name: ListProjectGroupGrants :many
-- The Groups holding grants on the Project: what each holds, who wrote
-- its most recent row, when it last changed and how many members it has
-- (counted as ListCompanyGroupMembers lists them). A null group_id lists
-- every Group there.
SELECT company_group.id AS group_id, company_group.name,
       grant_set.permissions, grant_set.updated_at, latest.granted_by_user_id,
       (
           SELECT count(*)
           FROM company_group_members AS group_member
           JOIN company_members AS member
             ON member.company_id = group_member.company_id
            AND member.user_id = group_member.user_id
            AND member.status = 'active'
           WHERE group_member.company_id = company_group.company_id
             AND group_member.group_id = company_group.id
             AND group_member.created_at >= COALESCE(member.reactivated_at, member.created_at)
       )::bigint AS member_count
FROM (
    SELECT group_grant.group_id,
           array_agg(group_grant.permission ORDER BY group_grant.permission)::text[] AS permissions,
           max(group_grant.updated_at)::timestamptz AS updated_at
    FROM project_group_grants AS group_grant
    WHERE group_grant.company_id = sqlc.arg(company_id)::uuid
      AND group_grant.project_id = sqlc.arg(project_id)::uuid
      AND (sqlc.narg(group_id)::uuid IS NULL OR group_grant.group_id = sqlc.narg(group_id)::uuid)
    GROUP BY group_grant.group_id
) AS grant_set
JOIN company_groups AS company_group
  ON company_group.company_id = sqlc.arg(company_id)::uuid
 AND company_group.id = grant_set.group_id
LEFT JOIN LATERAL (
    SELECT group_grant.granted_by_user_id
    FROM project_group_grants AS group_grant
    WHERE group_grant.company_id = sqlc.arg(company_id)::uuid
      AND group_grant.project_id = sqlc.arg(project_id)::uuid
      AND group_grant.group_id = grant_set.group_id
    ORDER BY group_grant.updated_at DESC, group_grant.permission
    LIMIT 1
) AS latest ON true
ORDER BY lower(company_group.name), company_group.id;

-- name: GetProjectGroupGrant :one
-- What one Group holds on one Project, empty when it holds nothing there.
SELECT COALESCE((
    SELECT array_agg(group_grant.permission ORDER BY group_grant.permission)
    FROM project_group_grants AS group_grant
    WHERE group_grant.company_id = sqlc.arg(company_id)::uuid
      AND group_grant.project_id = sqlc.arg(project_id)::uuid
      AND group_grant.group_id = sqlc.arg(group_id)::uuid
), '{}')::text[] AS permissions;

-- name: ReplaceProjectGroupGrants :many
-- Makes the Group's rows on the Project exactly the passed, non-empty set,
-- as ReplaceProjectGrants does for a person (Go normalizes the set first, so
-- it holds project.read; removing a Group is DeleteProjectGroupGrants). Both
-- arms run only for a Group of the Company, in a Company that is not being
-- deleted, and only for a non-empty set; otherwise nothing changes and no
-- rows come back. The upsert refreshes updated_at. A set without project.read
-- is refused at commit by project_group_grants_read_fkey.
WITH target AS MATERIALIZED (
    SELECT company_group.company_id, company_group.id
    FROM company_groups AS company_group
    JOIN companies AS company
      ON company.id = company_group.company_id AND company.deleting_at IS NULL
    WHERE company_group.company_id = sqlc.arg(company_id)::uuid
      AND company_group.id = sqlc.arg(group_id)::uuid
      AND cardinality(sqlc.arg(permissions)::text[]) > 0
), removed AS (
    DELETE FROM project_group_grants
    USING target
    WHERE project_group_grants.company_id = target.company_id
      AND project_group_grants.project_id = sqlc.arg(project_id)::uuid
      AND project_group_grants.group_id = target.id
      AND NOT (project_group_grants.permission = ANY(sqlc.arg(permissions)::text[]))
), kept AS (
    INSERT INTO project_group_grants (company_id, project_id, group_id, permission, granted_by_user_id)
    SELECT target.company_id, sqlc.arg(project_id)::uuid, target.id,
           requested.permission, sqlc.arg(actor_user_id)::uuid
    FROM target
    CROSS JOIN unnest(sqlc.arg(permissions)::text[]) AS requested(permission)
    ON CONFLICT (company_id, project_id, group_id, permission) DO UPDATE
    SET updated_at = clock_timestamp(),
        granted_by_user_id = EXCLUDED.granted_by_user_id
    RETURNING project_group_grants.permission
)
SELECT kept.permission FROM kept ORDER BY kept.permission;

-- name: DeleteProjectGroupGrants :many
-- Removes one Group from one Project; returns the permissions it held.
DELETE FROM project_group_grants
WHERE project_group_grants.company_id = sqlc.arg(company_id)::uuid
  AND project_group_grants.project_id = sqlc.arg(project_id)::uuid
  AND project_group_grants.group_id = sqlc.arg(group_id)::uuid
RETURNING project_group_grants.permission;

-- name: CountProjectGroupGrants :one
-- How many Groups hold grants on the Project (for the cap on Groups per Project).
SELECT count(DISTINCT project_group_grants.group_id)::bigint AS groups
FROM project_group_grants
WHERE project_group_grants.company_id = sqlc.arg(company_id)::uuid
  AND project_group_grants.project_id = sqlc.arg(project_id)::uuid;

-- name: ListPeopleProjectAccessPaths :many
-- The stored paths of the given people on the given Projects, for a Group
-- change preview: one row per Project, person, source and permission, from
-- project_access_paths through a correlated lookup that names the Project and
-- the person (each arm keeps its index). group_id is the zero uuid on an own
-- row.
SELECT candidate.project_id::uuid AS project_id,
       person.user_id::uuid AS user_id,
       path.via,
       path.group_id,
       path.permission
FROM unnest(sqlc.arg(project_ids)::uuid[]) AS candidate(project_id)
CROSS JOIN unnest(sqlc.arg(user_ids)::uuid[]) AS person(user_id)
CROSS JOIN LATERAL (
    SELECT access_path.via,
           COALESCE(access_path.group_id, '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS group_id,
           access_path.permission
    FROM project_access_paths AS access_path
    WHERE access_path.company_id = sqlc.arg(company_id)::uuid
      AND access_path.project_id = candidate.project_id
      AND access_path.user_id = person.user_id
) AS path
ORDER BY candidate.project_id, person.user_id, path.via, path.group_id, path.permission;

-- name: ListUserProjectPaths :many
-- One person's stored access across the Company, per Project, for leaving it:
-- the permissions from every source (their own rows and their Groups'
-- rows), and whether a Group reached the Project. Only rows and memberships
-- written since their latest (re)join count. It does not ask whether the
-- membership is active: the revocation reads it after revoking it.
WITH joined AS MATERIALIZED (
    SELECT COALESCE(member.reactivated_at, member.created_at) AS joined_at
    FROM company_members AS member
    WHERE member.company_id = sqlc.arg(company_id)::uuid
      AND member.user_id = sqlc.arg(user_id)::uuid
), path AS (
    SELECT grant_row.project_id, grant_row.permission, false AS through_group
    FROM project_grants AS grant_row
    WHERE grant_row.company_id = sqlc.arg(company_id)::uuid
      AND grant_row.user_id = sqlc.arg(user_id)::uuid
      AND grant_row.updated_at >= (SELECT joined.joined_at FROM joined)
    UNION ALL
    SELECT group_grant.project_id, group_grant.permission, true AS through_group
    FROM company_group_members AS group_member
    JOIN project_group_grants AS group_grant
      ON group_grant.company_id = group_member.company_id
     AND group_grant.group_id = group_member.group_id
    WHERE group_member.company_id = sqlc.arg(company_id)::uuid
      AND group_member.user_id = sqlc.arg(user_id)::uuid
      AND group_member.created_at >= (SELECT joined.joined_at FROM joined)
)
SELECT path.project_id,
       array_agg(DISTINCT path.permission ORDER BY path.permission)::text[] AS permissions,
       bool_or(path.through_group)::boolean AS through_group
FROM path
GROUP BY path.project_id
ORDER BY path.project_id;

-- name: AdvanceProjectAccessVersions :many
-- Advances the access revision of each given Project, for a change that
-- holds no expected revision (a member leaving the Company). Returns each
-- Project with its new revision.
UPDATE projects
SET access_version = projects.access_version + 1,
    access_changed_at = statement_timestamp()
WHERE projects.company_id = sqlc.arg(company_id)::uuid
  AND projects.id = ANY(sqlc.arg(project_ids)::uuid[])
RETURNING projects.id, projects.access_version;

-- name: ReplaceProjectGrants :many
-- Makes the person's rows exactly the passed, non-empty set (Go normalizes it
-- first, so it holds project.read; removing a person is
-- DeleteProjectGrantsForUser). Both arms run only for an active member with
-- an enabled account in a Company that is not being deleted, and only for a
-- non-empty set; otherwise nothing changes and no rows come back, which the
-- caller answers with project_member_required. The delete arm never touches
-- a kept row; the upsert refreshes updated_at (on the clock company_members
-- uses) so a deliberate re-grant after a Company rejoin passes the fence. A
-- set without project.read is refused at commit by project_grants_read_fkey.
WITH target AS MATERIALIZED (
    SELECT member.company_id, member.user_id
    FROM company_members AS member
    JOIN companies AS company
      ON company.id = member.company_id AND company.deleting_at IS NULL
    JOIN users AS account
      ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
    WHERE member.company_id = sqlc.arg(company_id)::uuid
      AND member.user_id = sqlc.arg(user_id)::uuid
      AND member.status = 'active'
      AND cardinality(sqlc.arg(permissions)::text[]) > 0
), removed AS (
    DELETE FROM project_grants
    USING target
    WHERE project_grants.company_id = target.company_id
      AND project_grants.project_id = sqlc.arg(project_id)::uuid
      AND project_grants.user_id = target.user_id
      AND NOT (project_grants.permission = ANY(sqlc.arg(permissions)::text[]))
), kept AS (
    INSERT INTO project_grants (company_id, project_id, user_id, permission, granted_by_user_id)
    SELECT target.company_id, sqlc.arg(project_id)::uuid, target.user_id,
           requested.permission, sqlc.arg(actor_user_id)::uuid
    FROM target
    CROSS JOIN unnest(sqlc.arg(permissions)::text[]) AS requested(permission)
    ON CONFLICT (company_id, project_id, user_id, permission) DO UPDATE
    SET updated_at = clock_timestamp(),
        granted_by_user_id = EXCLUDED.granted_by_user_id
    RETURNING project_grants.permission
)
SELECT kept.permission FROM kept ORDER BY kept.permission;

-- name: DeleteProjectGrantsForUser :many
-- Removes one person from one Project; returns the permissions they held.
DELETE FROM project_grants
WHERE project_grants.company_id = sqlc.arg(company_id)::uuid
  AND project_grants.project_id = sqlc.arg(project_id)::uuid
  AND project_grants.user_id = sqlc.arg(user_id)::uuid
RETURNING project_grants.permission;

-- name: DeleteCompanyProjectGrantsForUser :many
-- Company revocation: removes every grant the person holds in the Company and
-- advances the access revision of each Project that lost one, in one
-- statement. Returns those Project ids with the permissions the person held
-- there: the rows written since their latest (re)join, the ones that
-- counted, for the Activity record of the removal.
WITH joined AS MATERIALIZED (
    SELECT COALESCE(member.reactivated_at, member.created_at) AS joined_at
    FROM company_members AS member
    WHERE member.company_id = sqlc.arg(company_id)::uuid
      AND member.user_id = sqlc.arg(user_id)::uuid
), removed AS (
    DELETE FROM project_grants
    WHERE project_grants.company_id = sqlc.arg(company_id)::uuid
      AND project_grants.user_id = sqlc.arg(user_id)::uuid
    RETURNING project_grants.project_id, project_grants.permission, project_grants.updated_at
), bumped AS (
    UPDATE projects
    SET access_version = projects.access_version + 1,
        access_changed_at = statement_timestamp()
    WHERE projects.company_id = sqlc.arg(company_id)::uuid
      AND projects.id IN (SELECT removed.project_id FROM removed)
    RETURNING projects.id
)
SELECT bumped.id,
       COALESCE(array_agg(removed.permission ORDER BY removed.permission)
           FILTER (WHERE removed.updated_at >= (SELECT joined.joined_at FROM joined)), '{}')::text[] AS permissions
FROM bumped
JOIN removed ON removed.project_id = bumped.id
GROUP BY bumped.id
ORDER BY bumped.id;

-- name: SetProjectVisibility :one
UPDATE projects
SET visibility = sqlc.arg(visibility)::text,
    access_version = projects.access_version + 1,
    access_changed_at = statement_timestamp()
WHERE projects.company_id = sqlc.arg(company_id)::uuid
  AND projects.id = sqlc.arg(project_id)::uuid
  AND projects.access_version = sqlc.arg(expected_revision)::bigint
RETURNING projects.id, projects.visibility, projects.access_version;

-- name: BumpProjectAccessVersion :one
UPDATE projects
SET access_version = projects.access_version + 1,
    access_changed_at = statement_timestamp()
WHERE projects.company_id = sqlc.arg(company_id)::uuid
  AND projects.id = sqlc.arg(project_id)::uuid
  AND projects.access_version = sqlc.arg(expected_revision)::bigint
RETURNING projects.access_version;

-- name: ListProjectReaderIDs :many
-- Everyone holding project.read, sorted: the audience of Task-scoped frames.
SELECT access.user_id
FROM project_access AS access
WHERE access.company_id = sqlc.arg(company_id)::uuid
  AND access.project_id = sqlc.arg(project_id)::uuid
ORDER BY access.user_id;

-- name: ListProjectDeciderIDs :many
-- The readers of the Project who hold a deciding permission, sorted: who may
-- decide a Task-origin Approval or Question (ADR 0017). One
-- project_permission_held call per reader, never per row of a list.
SELECT access.user_id
FROM project_access AS access
WHERE access.company_id = sqlc.arg(company_id)::uuid
  AND access.project_id = sqlc.arg(project_id)::uuid
  AND project_permission_held(
      access.company_id, access.project_id, access.user_id,
      sqlc.arg(permission)::text,
      sqlc.arg(company_default_permissions)::text[],
      sqlc.arg(management_permissions)::text[],
      sqlc.arg(management_roles)::text[]
  )
ORDER BY access.user_id;

-- name: ProjectPermissionHeld :one
-- The write-time fence: whether the person holds the permission on the
-- Project right now. Every argument is cast so sqlc and Postgres resolve the
-- one function signature.
SELECT project_permission_held(
    sqlc.arg(company_id)::uuid,
    sqlc.arg(project_id)::uuid,
    sqlc.arg(user_id)::uuid,
    sqlc.arg(permission)::text,
    sqlc.arg(company_default_permissions)::text[],
    sqlc.arg(management_permissions)::text[],
    sqlc.arg(management_roles)::text[]
)::boolean AS held;

-- name: ListRunReadableProjects :many
-- The Projects among project_ids where a Run may use the permission: the
-- delegate rule for that Run (run_permission_held). Its Authorizer
-- holds the permission and read there, and a Task Run stays in its Task
-- chat's Project. A read asks project.read, a Task create
-- project.tasks.write. One call per candidate Project; the caller passes the
-- Projects its read found.
SELECT candidate.project_id::uuid AS project_id
FROM unnest(sqlc.arg(project_ids)::uuid[]) AS candidate(project_id)
WHERE run_permission_held(
    sqlc.arg(company_id)::uuid,
    sqlc.arg(run_id)::uuid,
    candidate.project_id,
    sqlc.arg(permission)::text,
    sqlc.arg(read_permission)::text,
    sqlc.arg(company_default_permissions)::text[]
)
ORDER BY candidate.project_id;

-- name: ListProjectAccessReport :many
-- The access report (ADR 0017): on each restricted Project that is not
-- archived, the active members with an enabled account who cannot read it
-- but either are the assignee of one of its open Tasks or authorized a Run
-- in one of its Task chats that started or last changed since the given
-- time. They are the people a restriction, or the switch to Project access,
-- left outside work they were doing: a Run queued long ago that a revoke or
-- that switch cancelled counts from when it ended.
-- One row per Project and person, with why they are listed. It names no
-- Task, chat or Run: the report is for projects.manage holders, who may
-- not read these Projects.
WITH involved AS (
    SELECT task.project_id, task.assignee_user_id AS user_id, 'task_assignee'::text AS reason
    FROM tasks AS task
    WHERE task.company_id = sqlc.arg(company_id)::uuid
      AND task.status NOT IN ('done', 'cancelled')
    UNION
    SELECT chat.project_id, run.execution_authorized_by_user_id, 'run_authorizer'::text AS reason
    FROM conversations AS chat
    JOIN runs AS run ON run.company_id = chat.company_id AND run.conversation_id = chat.id
    WHERE chat.company_id = sqlc.arg(company_id)::uuid
      AND chat.kind = 'task'
      AND chat.project_id IS NOT NULL
      AND run.execution_authorized_by_user_id IS NOT NULL
      AND GREATEST(run.created_at, run.updated_at) >= sqlc.arg(since)::timestamptz
)
SELECT project.id AS project_id, project.key, project.title, involved.user_id::uuid AS user_id,
    array_agg(involved.reason ORDER BY involved.reason)::text[] AS reasons
FROM involved
JOIN projects AS project
  ON project.company_id = sqlc.arg(company_id)::uuid
 AND project.id = involved.project_id
 AND project.visibility = 'restricted'
 AND project.archived_at IS NULL
JOIN company_members AS member
  ON member.company_id = project.company_id
 AND member.user_id = involved.user_id
 AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE NOT EXISTS (
    SELECT 1 FROM project_access AS access
    WHERE access.company_id = project.company_id
      AND access.project_id = project.id
      AND access.user_id = involved.user_id
)
GROUP BY project.id, project.key, project.title, involved.user_id
ORDER BY project.key, project.id, involved.user_id;
