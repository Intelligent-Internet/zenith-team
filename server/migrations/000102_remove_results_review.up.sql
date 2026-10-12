-- Removes "Review task results" (project.results.review) from the Project
-- catalog (D31 revision, owner ruling 2026-10-07). Nothing on the server
-- checked it once result review was retired, so holding it gave nothing.
-- The catalog is nine permissions.
--
-- 1. Every row holding it goes, a person's (project_grants) and a Group's
--    (project_group_grants). Every other row stays, project.read included,
--    so nobody loses sight of a Project.
-- 2. Each Project that lost a row advances its access revision and change
--    time, as every grant change does: an editor holding the old revision
--    is refused and reads again, and the Inbox counts version moves on.
-- 3. Both CHECKs drop the permission, so it can no longer be stored.
--    TestProjectGrantsCheckMatchesCatalog pins the newest definition of each
--    against auth.ProjectPermissions().
WITH removed AS (
    DELETE FROM project_grants
    WHERE project_grants.permission = 'project.results.review'
    RETURNING project_grants.company_id, project_grants.project_id
), removed_from_groups AS (
    DELETE FROM project_group_grants
    WHERE project_group_grants.permission = 'project.results.review'
    RETURNING project_group_grants.company_id, project_group_grants.project_id
)
UPDATE projects
SET access_version = projects.access_version + 1,
    access_changed_at = statement_timestamp()
WHERE (projects.company_id, projects.id) IN (
    SELECT removed.company_id, removed.project_id FROM removed
    UNION
    SELECT removed_from_groups.company_id, removed_from_groups.project_id FROM removed_from_groups
);

-- The deletes queued the deferred read-row checks, and Postgres refuses
-- ALTER TABLE on a table with pending trigger events. Run them now; the
-- setting ends with this migration's transaction.
SET CONSTRAINTS ALL IMMEDIATE;

ALTER TABLE project_grants
    DROP CONSTRAINT project_grants_permission_check,
    ADD CONSTRAINT project_grants_permission_check CHECK (permission IN (
        'project.read',
        'project.tasks.write',
        'project.tasks.execute',
        'project.files.manage',
        'project.approvals.resolve',
        'project.visibility.update',
        'project.settings.update',
        'project.grants.manage',
        'project.archive'
    ));

ALTER TABLE project_group_grants
    DROP CONSTRAINT project_group_grants_permission_check,
    ADD CONSTRAINT project_group_grants_permission_check CHECK (permission IN (
        'project.read',
        'project.tasks.write',
        'project.tasks.execute',
        'project.files.manage',
        'project.approvals.resolve',
        'project.visibility.update',
        'project.settings.update',
        'project.grants.manage',
        'project.archive'
    ));
