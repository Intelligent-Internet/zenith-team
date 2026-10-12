ALTER TABLE huddle_ai_states
    ADD COLUMN allow_company_context boolean NOT NULL DEFAULT false,
    ADD COLUMN context_granted_by_user_id uuid,
    ADD COLUMN tool_activity jsonb NOT NULL DEFAULT '{}';
ALTER TABLE huddle_ai_operations ADD COLUMN allow_company_context boolean NOT NULL DEFAULT false;
ALTER TABLE huddle_ai_sessions
    ADD COLUMN tool_calls integer NOT NULL DEFAULT 0,
    ADD COLUMN tool_window_calls integer NOT NULL DEFAULT 0,
    ADD COLUMN last_tool_at timestamptz;
