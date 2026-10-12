BEGIN;

-- Fresh-install Project ownership. No default Project or existing-data backfill.
CREATE TABLE projects (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    key text NOT NULL,
    title text NOT NULL,
    description text DEFAULT '' NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT projects_pkey PRIMARY KEY (id),
    CONSTRAINT projects_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT projects_company_key_key UNIQUE (company_id, key)
);

ALTER TABLE tasks
    ADD COLUMN project_id uuid NOT NULL,
    ADD CONSTRAINT tasks_project_fkey
        FOREIGN KEY (company_id, project_id)
        REFERENCES projects(company_id, id) ON DELETE CASCADE,
    ADD CONSTRAINT tasks_company_project_id_key UNIQUE (company_id, project_id, id);

CREATE INDEX tasks_company_project_status_idx
    ON tasks (company_id, project_id, status, number DESC);

-- Both endpoints share the edge's Project identity. This makes cross-Project
-- dependencies impossible without relying on the caller's validation.
ALTER TABLE task_dependencies
    ADD COLUMN project_id uuid NOT NULL,
    DROP CONSTRAINT task_dependencies_dependent_task_fkey,
    DROP CONSTRAINT task_dependencies_prerequisite_task_fkey,
    ADD CONSTRAINT task_dependencies_dependent_task_fkey
        FOREIGN KEY (company_id, project_id, dependent_task_id)
        REFERENCES tasks(company_id, project_id, id) ON DELETE CASCADE,
    ADD CONSTRAINT task_dependencies_prerequisite_task_fkey
        FOREIGN KEY (company_id, project_id, prerequisite_task_id)
        REFERENCES tasks(company_id, project_id, id) ON DELETE CASCADE;

COMMIT;
