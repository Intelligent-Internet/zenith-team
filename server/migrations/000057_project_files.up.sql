-- Retired associations retain immutable input bytes for frozen Runs and retries.
CREATE TABLE project_files (
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    file_type text NOT NULL CHECK (file_type IN ('logo', 'document')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    removed_at timestamptz,
    PRIMARY KEY (project_id, attachment_id),
    FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, attachment_id) REFERENCES attachments(company_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX project_files_one_logo ON project_files(project_id) WHERE file_type='logo' AND removed_at IS NULL;
CREATE INDEX project_files_attachment ON project_files(attachment_id);
