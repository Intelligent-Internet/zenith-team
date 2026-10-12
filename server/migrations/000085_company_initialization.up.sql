CREATE TABLE company_initializations (
    company_id uuid PRIMARY KEY,
    created_by_user_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    research_run_id uuid,
    welcome_run_id uuid,
    context_markdown text NOT NULL DEFAULT '',
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (company_id, conversation_id)
        REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, research_run_id)
        REFERENCES runs(company_id, id) ON DELETE SET NULL (research_run_id),
    FOREIGN KEY (company_id, welcome_run_id)
        REFERENCES runs(company_id, id) ON DELETE SET NULL (welcome_run_id)
);
