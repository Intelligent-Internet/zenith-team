ALTER TABLE huddle_ai_states
    ADD COLUMN agent_id uuid,
    ADD COLUMN agent_profile jsonb NOT NULL DEFAULT '{}',
    ADD CONSTRAINT huddle_ai_states_agent_fkey FOREIGN KEY (company_id, agent_id)
        REFERENCES agents(company_id, id) ON DELETE SET NULL (agent_id);

CREATE INDEX huddle_ai_states_agent_idx ON huddle_ai_states(company_id, agent_id)
    WHERE agent_id IS NOT NULL;

-- Receipt attribution survives Agent deletion.
ALTER TABLE huddle_ai_operations ADD COLUMN agent_id uuid;
