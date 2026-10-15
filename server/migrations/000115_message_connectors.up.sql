-- A message pins selected contracts and the sender's Agent placements. Queues
-- and retries must never silently switch to a different connected account.
-- Preservation edges defer until commit so Company-owned graph cascades finish.
CREATE TABLE message_connectors (
    company_id UUID NOT NULL,
    conversation_id UUID NOT NULL,
    request_key UUID NOT NULL,
    selected_by_user_id UUID NOT NULL,
    position INTEGER NOT NULL CHECK (position >= 0),
    connector_id UUID NOT NULL REFERENCES connector_catalog_items(id) DEFERRABLE INITIALLY DEFERRED,
    name TEXT NOT NULL,
    policy_revision BIGINT NOT NULL,
    source TEXT NOT NULL,
    native_id TEXT NOT NULL,
    contract_digest TEXT NOT NULL,
    start_offset INTEGER NOT NULL CHECK (start_offset >= 0),
    end_offset INTEGER NOT NULL CHECK (end_offset > start_offset),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (conversation_id, selected_by_user_id, request_key, position),
    FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE
);
CREATE TABLE message_connector_bindings (
    company_id UUID NOT NULL,
    conversation_id UUID NOT NULL,
    request_key UUID NOT NULL,
    selected_by_user_id UUID NOT NULL,
    agent_id UUID NOT NULL REFERENCES agents(id) DEFERRABLE INITIALLY DEFERRED,
    runtime_id UUID NOT NULL REFERENCES runtimes(id) DEFERRABLE INITIALLY DEFERRED,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (conversation_id, selected_by_user_id, request_key, agent_id),
    FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE
);
