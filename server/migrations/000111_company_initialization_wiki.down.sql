ALTER TABLE company_initializations
    ADD COLUMN context_markdown text NOT NULL DEFAULT '',
    ADD COLUMN welcome_run_id uuid,
    ADD FOREIGN KEY (company_id, welcome_run_id)
        REFERENCES runs(company_id, id) ON DELETE SET NULL (welcome_run_id);
