-- Puts "Review task results" (project.results.review) back in both CHECKs,
-- as 000086 and 000101 declared them, so the previous release can store it
-- again. The rows 000102 deleted are not restored: no person or Group gets
-- the permission back, and no access revision moves.
ALTER TABLE project_grants
    DROP CONSTRAINT project_grants_permission_check,
    ADD CONSTRAINT project_grants_permission_check CHECK (permission IN (
        'project.read',
        'project.tasks.write',
        'project.tasks.execute',
        'project.files.manage',
        'project.approvals.resolve',
        'project.results.review',
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
        'project.results.review',
        'project.visibility.update',
        'project.settings.update',
        'project.grants.manage',
        'project.archive'
    ));
