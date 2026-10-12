-- Studio integration: source canvases, final media roles, and Conversation projects.
-- Supplemental source snapshots do not change the frozen output or review items.
CREATE TABLE output_studio_canvases (
    output_id uuid PRIMARY KEY,
    company_id uuid NOT NULL,
    canvas jsonb NOT NULL CHECK (jsonb_typeof(canvas) = 'object' AND octet_length(canvas::text) <= 2097152),
    added_by_user_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE
);

ALTER TABLE output_items DROP CONSTRAINT output_items_role_check;
ALTER TABLE output_items ADD CONSTRAINT output_items_role_check
    CHECK (role IN ('primary', 'final', 'supporting'));

CREATE TABLE conversation_studio_projects (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL UNIQUE,
    project_id uuid UNIQUE,
    title text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 4000),
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (company_id, conversation_id)
        REFERENCES conversations(company_id, id) ON DELETE CASCADE
);
