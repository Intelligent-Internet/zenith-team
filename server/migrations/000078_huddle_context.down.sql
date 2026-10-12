ALTER TABLE huddle_ai_sessions DROP COLUMN last_tool_at, DROP COLUMN tool_window_calls, DROP COLUMN tool_calls;
ALTER TABLE huddle_ai_operations DROP COLUMN allow_company_context;
ALTER TABLE huddle_ai_states DROP COLUMN tool_activity, DROP COLUMN context_granted_by_user_id, DROP COLUMN allow_company_context;
