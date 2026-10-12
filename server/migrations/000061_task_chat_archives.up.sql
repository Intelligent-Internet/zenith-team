ALTER TABLE conversation_user_states ADD COLUMN archived_at timestamptz;

CREATE INDEX conversation_user_states_archives_idx
    ON conversation_user_states(company_id, user_id, archived_at DESC)
    WHERE archived_at IS NOT NULL;
