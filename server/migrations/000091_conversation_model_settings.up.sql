-- A chat keeps its own choices; each Run still freezes them in runs.context.
-- Key by Agent as well so a Task handoff cannot reuse another provider's model.
CREATE TABLE conversation_model_settings (
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    model text,
    thinking_level text,
    revision bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (conversation_id, agent_id),
    FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, agent_id) REFERENCES agents(company_id, id) ON DELETE CASCADE
);

-- Preserve the last configuration actually used by an existing chat, including
-- absent provider overrides. Chats with no Run start from the Agent template.
INSERT INTO conversation_model_settings (company_id, conversation_id, agent_id, model, thinking_level)
SELECT c.company_id, c.id, a.id,
       CASE WHEN last_run.id IS NULL THEN a.model ELSE last_run.context->'agent'->>'model' END,
       CASE WHEN last_run.id IS NULL THEN a.thinking_level ELSE last_run.context->'agent'->>'thinkingLevel' END
FROM conversations c
LEFT JOIN task_run_agents t ON t.task_id = c.task_id
JOIN agents a ON a.id = COALESCE(t.agent_id, c.agent_id) AND a.company_id = c.company_id
LEFT JOIN LATERAL (
    SELECT r.id, r.context FROM runs r
    WHERE r.conversation_id = c.id AND r.agent_id = a.id
      AND r.context ? 'agent'
      AND r.context->'executionProfile'->>'source' IS DISTINCT FROM 'company_research'
    ORDER BY r.created_at DESC, r.id DESC LIMIT 1
) last_run ON true
WHERE c.kind IN ('direct', 'task');

-- Model changes are Conversation history, not messages consumed by a Run.
-- Agent and member ids are attribution; history survives a Task handoff.
CREATE TABLE conversation_model_changes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    changed_by_user_id uuid NOT NULL,
    previous_model text,
    model text,
    after_seq bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE
);
CREATE INDEX conversation_model_changes_history_idx
    ON conversation_model_changes (conversation_id, created_at, id);
