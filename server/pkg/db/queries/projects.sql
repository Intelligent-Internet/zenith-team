-- Project reads (ADR 0017). The caller's access is decided inside the read:
-- project_access admits the Projects the caller reads; the management bridge
-- (a Company role holding projects.manage, bound from Go) makes a Project
-- visible as metadata only. Each row carries what Go needs to build the
-- caller's access block (auth.ProjectAccessOf): via, the bridge flag, the
-- caller's fenced grant rows and what their Groups hold there (fenced Group
-- memberships, one indexed probe per Project). The actor CTE is the same
-- active-member gate ResolveProjectAccess uses.

-- name: ListProjectsForActiveMember :many
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
SELECT sqlc.embed(projects),
       access.via::text AS via,
       (actor.role = ANY(sqlc.arg(management_roles)::text[]))::boolean AS management,
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
FROM actor
JOIN projects ON projects.company_id = actor.company_id
JOIN project_access AS access
  ON access.company_id = projects.company_id
 AND access.project_id = projects.id
 AND access.user_id = actor.user_id
WHERE (sqlc.arg(include_archived)::boolean OR projects.archived_at IS NULL)
ORDER BY projects.created_at, projects.id;

-- name: ListProjectsForManageView :many
-- The Managed projects view: Projects the caller's Company role manages
-- through projects.manage but the caller cannot read. Metadata only; Join
-- is how the caller reads one.
WITH actor AS MATERIALIZED (
    SELECT member.company_id, member.user_id, member.role
    FROM company_members AS member
    JOIN companies AS company
      ON company.id = member.company_id AND company.deleting_at IS NULL
    JOIN users AS account
      ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
    WHERE member.company_id = sqlc.arg(company_id)::uuid
      AND member.user_id = sqlc.arg(actor_user_id)::uuid
      AND member.status = 'active'
      AND member.role = ANY(sqlc.arg(management_roles)::text[])
)
SELECT projects.*
FROM actor
JOIN projects ON projects.company_id = actor.company_id
WHERE (sqlc.arg(include_archived)::boolean OR projects.archived_at IS NULL)
  AND NOT EXISTS (
      SELECT 1 FROM project_access AS access
      WHERE access.company_id = projects.company_id
        AND access.project_id = projects.id
        AND access.user_id = actor.user_id
  )
ORDER BY projects.created_at, projects.id;

-- name: GetProjectForActiveMember :one
-- One Project the caller reads or manages through the bridge; no row for
-- anything else, so an invisible Project and a missing one look the same.
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
SELECT sqlc.embed(projects),
       COALESCE(access.via, '')::text AS via,
       (actor.role = ANY(sqlc.arg(management_roles)::text[]))::boolean AS management,
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
FROM actor
JOIN projects ON projects.company_id = actor.company_id
LEFT JOIN project_access AS access
  ON access.company_id = projects.company_id
 AND access.project_id = projects.id
 AND access.user_id = actor.user_id
WHERE projects.id = sqlc.arg(project_id)::uuid
  AND (access.user_id IS NOT NULL OR actor.role = ANY(sqlc.arg(management_roles)::text[]));

-- name: LockCompanyPlanProjects :many
-- The Projects a Company Plan apply may reach for its applier (ADR 0017):
-- the ones the applier reads, row-locked in id order, each with the facts
-- auth.ProjectAccessOf needs to decide what the apply may do there. A Project
-- the applier cannot read is neither returned nor locked; the apply treats
-- its key like a key it never heard of. The caller holds the Company lock,
-- which every grant, visibility and role change also takes.
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
SELECT sqlc.embed(projects),
       access.via::text AS via,
       (actor.role = ANY(sqlc.arg(management_roles)::text[]))::boolean AS management,
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
FROM actor
JOIN projects ON projects.company_id = actor.company_id
JOIN project_access AS access
  ON access.company_id = projects.company_id
 AND access.project_id = projects.id
 AND access.user_id = actor.user_id
ORDER BY projects.id
FOR UPDATE OF projects;

-- name: GetProjectForUpdate :one
SELECT * FROM projects WHERE company_id = sqlc.arg(company_id)::uuid
 AND id = sqlc.arg(project_id)::uuid FOR UPDATE;

-- name: CreateProject :one
INSERT INTO projects(company_id, key, title, description)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: UpdateProject :one
UPDATE projects SET title = $3, description = $4, version = version + 1, updated_at = clock_timestamp()
WHERE company_id = $1 AND id = $2 AND version = sqlc.arg(expected_revision)::bigint
 AND archived_at IS NULL RETURNING *;

-- name: ArchiveProject :one
UPDATE projects SET archived_at = clock_timestamp(), updated_at = clock_timestamp(), version = version + 1
WHERE company_id = $1 AND id = $2 AND version = sqlc.arg(expected_revision)::bigint
 AND archived_at IS NULL RETURNING *;

-- name: AdvanceProjectRevision :one
-- A change to the Project that is not a column of its own (its files)
-- advances the revision so readers see a new projection.
UPDATE projects SET version = version + 1, updated_at = clock_timestamp()
WHERE company_id = sqlc.arg(company_id)::uuid AND id = sqlc.arg(project_id)::uuid
 AND archived_at IS NULL RETURNING *;

-- name: ProjectHasOpenTasks :one
SELECT EXISTS(SELECT 1 FROM tasks WHERE tasks.company_id = $1 AND tasks.project_id = $2
 AND (tasks.status NOT IN ('done', 'cancelled')
 OR EXISTS (SELECT 1 FROM runs WHERE runs.task_id = tasks.id AND runs.company_id = tasks.company_id
   AND runs.status IN ('queued','dispatched','waiting_local_directory','running','deferred'))
 OR EXISTS (SELECT 1 FROM task_merge_jobs WHERE task_merge_jobs.task_id = tasks.id
   AND task_merge_jobs.company_id = tasks.company_id AND task_merge_jobs.status IN ('pending','executing'))))::boolean;

-- name: GetTaskProjectForShare :one
SELECT projects.* FROM projects JOIN tasks ON tasks.company_id = projects.company_id AND tasks.project_id = projects.id
WHERE tasks.company_id = sqlc.arg(company_id)::uuid AND tasks.id = sqlc.arg(task_id)::uuid
FOR SHARE OF projects;

-- name: ListProjectFiles :many
-- The active files of these Projects that the person may open, each checked
-- through attachment_access row by row (ADR 0017).
SELECT f.project_id, f.file_type, a.id, a.filename, a.content_type, a.size_bytes
FROM project_files f JOIN attachments a ON a.id=f.attachment_id AND a.company_id=f.company_id
CROSS JOIN LATERAL (
    SELECT 1 FROM attachment_access AS access
    WHERE access.company_id = f.company_id
      AND access.attachment_id = f.attachment_id
      AND access.user_id = sqlc.arg(actor_user_id)::uuid
    LIMIT 1
) AS readable
WHERE f.company_id=sqlc.arg(company_id)::uuid AND f.project_id=ANY(sqlc.arg(project_ids)::uuid[])
 AND f.removed_at IS NULL AND a.status='ready'
ORDER BY f.created_at, a.id;

-- name: RetireProjectFiles :many
UPDATE project_files SET removed_at=clock_timestamp()
WHERE company_id=$1 AND project_id=$2 AND removed_at IS NULL
RETURNING attachment_id;

-- name: RegisterProjectFile :exec
INSERT INTO project_files(company_id,project_id,attachment_id,file_type)
VALUES ($1,$2,$3,$4)
ON CONFLICT (project_id,attachment_id) DO UPDATE SET removed_at=NULL,file_type=EXCLUDED.file_type;

-- name: LockProjectUpload :one
SELECT a.* FROM attachments a
WHERE a.company_id=sqlc.arg(company_id)::uuid AND a.id=sqlc.arg(attachment_id)::uuid
 AND a.status='ready'
 AND (a.uploaded_by_user_id=sqlc.arg(actor_user_id)::uuid OR EXISTS (
   SELECT 1 FROM project_files f WHERE f.company_id=a.company_id AND f.attachment_id=a.id
    AND f.project_id=sqlc.arg(project_id)::uuid AND f.removed_at IS NULL))
 AND NOT EXISTS (SELECT 1 FROM asset_versions v WHERE v.attachment_id=a.id)
FOR UPDATE OF a;

-- name: ListTaskProjectDocuments :many
-- The documents a Task Run freezes: the Task's Project documents its
-- authorizer may open (attachment_access, row by row).
SELECT a.id, a.filename, a.content_type, a.size_bytes
FROM tasks t JOIN project_files f ON f.company_id=t.company_id AND f.project_id=t.project_id
JOIN attachments a ON a.company_id=f.company_id AND a.id=f.attachment_id
CROSS JOIN LATERAL (
    SELECT 1 FROM attachment_access AS access
    WHERE access.company_id = f.company_id
      AND access.attachment_id = f.attachment_id
      AND access.user_id = sqlc.arg(actor_user_id)::uuid
    LIMIT 1
) AS readable
WHERE t.company_id=sqlc.arg(company_id)::uuid AND t.id=sqlc.arg(task_id)::uuid
 AND f.file_type='document' AND f.removed_at IS NULL AND a.status='ready'
ORDER BY f.created_at,a.id;
