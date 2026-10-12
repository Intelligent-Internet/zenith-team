ALTER TABLE huddle_ai_operations DROP COLUMN agent_id;
DROP INDEX huddle_ai_states_agent_idx;
ALTER TABLE huddle_ai_states
    DROP CONSTRAINT huddle_ai_states_agent_fkey,
    DROP COLUMN agent_profile,
    DROP COLUMN agent_id;
