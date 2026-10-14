-- A send's selections outlive queueing, edits and the source installation.
-- The conversation owns the receipt; request_key is shared by its eventual
-- message and queue item. Versions remain pinned until the chat is deleted.
CREATE TABLE message_skills (
    company_id UUID NOT NULL,
    conversation_id UUID NOT NULL,
    request_key UUID NOT NULL,
    selected_by_user_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    position INTEGER NOT NULL CHECK (position >= 0),
    skill_id UUID NOT NULL,
    version_id UUID NOT NULL,
    start_offset INTEGER NOT NULL CHECK (start_offset >= 0),
    end_offset INTEGER NOT NULL CHECK (end_offset > start_offset),
    PRIMARY KEY (conversation_id, selected_by_user_id, request_key, position),
    FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, version_id) REFERENCES skill_versions(company_id, id)
);
CREATE INDEX message_skills_version ON message_skills(version_id);
