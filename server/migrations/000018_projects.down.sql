BEGIN;

ALTER TABLE task_dependencies
    DROP CONSTRAINT task_dependencies_dependent_task_fkey,
    DROP CONSTRAINT task_dependencies_prerequisite_task_fkey,
    DROP COLUMN project_id,
    ADD CONSTRAINT task_dependencies_dependent_task_fkey
        FOREIGN KEY (company_id, dependent_task_id)
        REFERENCES tasks(company_id, id) ON DELETE CASCADE,
    ADD CONSTRAINT task_dependencies_prerequisite_task_fkey
        FOREIGN KEY (company_id, prerequisite_task_id)
        REFERENCES tasks(company_id, id) ON DELETE CASCADE;

DROP INDEX tasks_company_project_status_idx;
ALTER TABLE tasks
    DROP CONSTRAINT tasks_project_fkey,
    DROP CONSTRAINT tasks_company_project_id_key,
    DROP COLUMN project_id;

DROP TABLE projects;

COMMIT;
