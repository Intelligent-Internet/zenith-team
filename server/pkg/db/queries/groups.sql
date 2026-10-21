-- Groups: a named, flat set of Team
-- members in one Company that can hold Project grants. Every statement
-- repeats the Company scope. A membership counts only for an active member
-- and only when it was written since their latest (re)join (the rejoin fence
-- every access read applies), so the member lists and counts here agree with
-- what the Group gives. Lock order: company -> member -> group -> project ->
-- conversation -> run; a Group lock is never taken after a Project,
-- Conversation or Run lock (TestProjectLocksPrecedeConversationAndRunLocks).

-- name: ListCompanyGroups :many
-- Every Group of the Company with its member count, by name.
SELECT company_group.id, company_group.company_id, company_group.name, company_group.description,
       company_group.created_by_user_id, company_group.created_at, company_group.updated_at,
       company_group.version,
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
FROM company_groups AS company_group
WHERE company_group.company_id = sqlc.arg(company_id)::uuid
ORDER BY lower(company_group.name), company_group.id;

-- name: GetCompanyGroup :one
-- One Group with its member count, read without a lock.
SELECT company_group.id, company_group.company_id, company_group.name, company_group.description,
       company_group.created_by_user_id, company_group.created_at, company_group.updated_at,
       company_group.version,
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
FROM company_groups AS company_group
WHERE company_group.company_id = sqlc.arg(company_id)::uuid
  AND company_group.id = sqlc.arg(group_id)::uuid;

-- name: LockCompanyGroupForChange :one
-- Taken before a change of the Group itself (rename, members, delete), after
-- the Company and member locks and before any Project lock.
SELECT *
FROM company_groups
WHERE company_groups.company_id = sqlc.arg(company_id)::uuid
  AND company_groups.id = sqlc.arg(group_id)::uuid
FOR UPDATE;

-- name: LockCompanyGroupForShare :one
-- Holds the Group while a Group grant on a Project changes, so its members
-- cannot change under the grant. Same place in the lock order.
SELECT *
FROM company_groups
WHERE company_groups.company_id = sqlc.arg(company_id)::uuid
  AND company_groups.id = sqlc.arg(group_id)::uuid
FOR SHARE;

-- name: InsertCompanyGroup :one
-- A unique-name violation (company_groups_name_key) is the caller's
-- group_conflict.
INSERT INTO company_groups (company_id, name, description, created_by_user_id)
VALUES (sqlc.arg(company_id)::uuid, sqlc.arg(name)::text, sqlc.arg(description)::text, sqlc.arg(actor_user_id)::uuid)
RETURNING *;

-- name: UpdateCompanyGroup :one
-- Renames or redescribes the Group against the revision the caller read; no
-- row means the revision moved.
UPDATE company_groups
SET name = sqlc.arg(name)::text,
    description = sqlc.arg(description)::text,
    version = company_groups.version + 1,
    updated_at = clock_timestamp()
WHERE company_groups.company_id = sqlc.arg(company_id)::uuid
  AND company_groups.id = sqlc.arg(group_id)::uuid
  AND company_groups.version = sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: DeleteCompanyGroup :one
-- Deletes the Group against the revision the caller read. Its memberships
-- and its grants go with it (ON DELETE CASCADE); the caller resolved what
-- each member held before and sweeps their losses after.
DELETE FROM company_groups
WHERE company_groups.company_id = sqlc.arg(company_id)::uuid
  AND company_groups.id = sqlc.arg(group_id)::uuid
  AND company_groups.version = sqlc.arg(expected_version)::bigint
RETURNING company_groups.id;

-- name: BumpCompanyGroupVersion :one
-- Advances the Group's revision after a change of its members, so an editor
-- holding the old one is refused.
UPDATE company_groups
SET version = company_groups.version + 1,
    updated_at = clock_timestamp()
WHERE company_groups.company_id = sqlc.arg(company_id)::uuid
  AND company_groups.id = sqlc.arg(group_id)::uuid
RETURNING company_groups.version;

-- name: CountCompanyGroups :one
SELECT count(*)::bigint AS groups
FROM company_groups
WHERE company_groups.company_id = sqlc.arg(company_id)::uuid;

-- name: ListCompanyGroupMembers :many
-- The Group's members: active Team members whose membership passes the
-- rejoin fence, in the order they were added.
SELECT group_member.user_id, group_member.added_by_user_id, group_member.created_at
FROM company_group_members AS group_member
JOIN company_members AS member
  ON member.company_id = group_member.company_id
 AND member.user_id = group_member.user_id
 AND member.status = 'active'
WHERE group_member.company_id = sqlc.arg(company_id)::uuid
  AND group_member.group_id = sqlc.arg(group_id)::uuid
  AND group_member.created_at >= COALESCE(member.reactivated_at, member.created_at)
ORDER BY group_member.created_at, group_member.user_id;

-- name: ListGroupMemberCandidateIDs :many
-- The ids among the given ones that may join a Group: active Team members
-- with an enabled account in a Company that is not being deleted. An Agent's
-- id, a former member or a stranger is not returned.
SELECT member.user_id
FROM company_members AS member
JOIN companies AS company
  ON company.id = member.company_id AND company.deleting_at IS NULL
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE member.company_id = sqlc.arg(company_id)::uuid
  AND member.user_id = ANY(sqlc.arg(user_ids)::uuid[])
  AND member.status = 'active'
ORDER BY member.user_id;

-- name: InsertCompanyGroupMembers :many
-- Adds people to the Group: active Team members with an enabled account only,
-- stamped now on the company_members clock. Someone already in stays as they
-- are. A row left from before the person's latest (re)join counts for
-- nothing, so it is stamped again: adding them back must give them the Group,
-- and keeping the old row (DO NOTHING) would leave it fenced out. Returns the
-- people added or stamped again.
INSERT INTO company_group_members (company_id, group_id, user_id, added_by_user_id)
SELECT member.company_id, sqlc.arg(group_id)::uuid, member.user_id, sqlc.arg(actor_user_id)::uuid
FROM company_members AS member
JOIN companies AS company
  ON company.id = member.company_id AND company.deleting_at IS NULL
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE member.company_id = sqlc.arg(company_id)::uuid
  AND member.user_id = ANY(sqlc.arg(user_ids)::uuid[])
  AND member.status = 'active'
ON CONFLICT (company_id, group_id, user_id) DO UPDATE
SET created_at = clock_timestamp(),
    added_by_user_id = EXCLUDED.added_by_user_id
WHERE company_group_members.created_at < (
    SELECT COALESCE(joined.reactivated_at, joined.created_at)
    FROM company_members AS joined
    WHERE joined.company_id = company_group_members.company_id
      AND joined.user_id = company_group_members.user_id
)
RETURNING company_group_members.user_id;

-- name: DeleteCompanyGroupMembers :many
-- Takes people out of the Group; returns who was taken out.
DELETE FROM company_group_members
WHERE company_group_members.company_id = sqlc.arg(company_id)::uuid
  AND company_group_members.group_id = sqlc.arg(group_id)::uuid
  AND company_group_members.user_id = ANY(sqlc.arg(user_ids)::uuid[])
RETURNING company_group_members.user_id;

-- name: CountCompanyGroupMembers :one
-- Counted the way ListCompanyGroupMembers lists them.
SELECT count(*)::bigint AS members
FROM company_group_members AS group_member
JOIN company_members AS member
  ON member.company_id = group_member.company_id
 AND member.user_id = group_member.user_id
 AND member.status = 'active'
WHERE group_member.company_id = sqlc.arg(company_id)::uuid
  AND group_member.group_id = sqlc.arg(group_id)::uuid
  AND group_member.created_at >= COALESCE(member.reactivated_at, member.created_at);

-- name: ListGroupProjects :many
-- The Projects the Group holds grants on, with what it holds on each, in
-- Project id order (the order a Group change locks them in). Every Project is
-- listed, whoever asks: a change checks each one.
SELECT group_grant.project_id,
       array_agg(group_grant.permission ORDER BY group_grant.permission)::text[] AS permissions
FROM project_group_grants AS group_grant
WHERE group_grant.company_id = sqlc.arg(company_id)::uuid
  AND group_grant.group_id = sqlc.arg(group_id)::uuid
GROUP BY group_grant.project_id
ORDER BY group_grant.project_id;

-- name: ListVisibleGroupProjects :many
-- What each Group holds on the Projects the viewer can see: those they read,
-- or every Project when their active Company role holds the management bridge
-- (Go binds auth.RolesWith(projects.manage)). A null group_id lists every
-- Group of the Company.
SELECT group_grant.group_id,
       group_grant.project_id,
       array_agg(group_grant.permission ORDER BY group_grant.permission)::text[] AS permissions
FROM project_group_grants AS group_grant
WHERE group_grant.company_id = sqlc.arg(company_id)::uuid
  AND (sqlc.narg(group_id)::uuid IS NULL OR group_grant.group_id = sqlc.narg(group_id)::uuid)
  AND (
      EXISTS (
          SELECT 1
          FROM company_members AS viewer
          WHERE viewer.company_id = group_grant.company_id
            AND viewer.user_id = sqlc.arg(viewer_user_id)::uuid
            AND viewer.status = 'active'
            AND viewer.role = ANY(sqlc.arg(management_roles)::text[])
      )
      OR EXISTS (
          SELECT 1
          FROM project_access AS access
          WHERE access.company_id = group_grant.company_id
            AND access.project_id = group_grant.project_id
            AND access.user_id = sqlc.arg(viewer_user_id)::uuid
      )
  )
GROUP BY group_grant.group_id, group_grant.project_id
ORDER BY group_grant.group_id, group_grant.project_id;

-- name: DeleteCompanyGroupMembershipsForUser :many
-- Leaving the Company ends every Group membership; a rejoin restores
-- none. Returns the Groups the person was in.
DELETE FROM company_group_members
WHERE company_group_members.company_id = sqlc.arg(company_id)::uuid
  AND company_group_members.user_id = sqlc.arg(user_id)::uuid
RETURNING company_group_members.group_id;

-- name: ListProjectGrantManagerIDs :many
-- Everyone who manages the Project's access: the active members holding the
-- given permission (project.grants.manage) there by any source, their own
-- grant, a Group's grant or the management bridge. One project_permission_held
-- call per active member; Go binds every set.
SELECT member.user_id
FROM company_members AS member
WHERE member.company_id = sqlc.arg(company_id)::uuid
  AND member.status = 'active'
  AND project_permission_held(
      member.company_id, sqlc.arg(project_id)::uuid, member.user_id,
      sqlc.arg(permission)::text,
      sqlc.arg(company_default_permissions)::text[],
      sqlc.arg(management_permissions)::text[],
      sqlc.arg(management_roles)::text[]
  )
ORDER BY member.user_id;

-- name: ListCompanyMemberNames :many
-- The display names of the given people in the Company, for an Inbox notice.
SELECT member.user_id, account.display_name
FROM company_members AS member
JOIN users AS account ON account.id = member.user_id
WHERE member.company_id = sqlc.arg(company_id)::uuid
  AND member.user_id = ANY(sqlc.arg(user_ids)::uuid[])
ORDER BY member.user_id;
