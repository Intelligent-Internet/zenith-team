-- 000003_agents: AI Agents — the agent-employee domain (renamed end to end
-- from the old worker vocabulary): agent rows, the reportsTo tree, and
-- Departments-as-Agents configuration and capabilities.

CREATE TABLE agents (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    name text NOT NULL,
    title text NOT NULL,
    instructions text DEFAULT ''::text NOT NULL,
    reports_to_agent_id uuid,
    model text,
    thinking_level text,
    -- How many of this agent's TASK-lane runs may be in flight at once:
    -- status IN ('dispatched', 'running', 'waiting_local_directory') AND
    -- runs.lane = 'task'. Direct-conversation runs (lane 'direct') are exempt
    -- in both directions — they neither wait for a slot nor occupy one. The
    -- lane is a column on the run and not derived from task_id, because a
    -- deleted task nulls task_id out from under a run that is still holding
    -- a slot; see the runs.lane comment. No CHECK constraint: the request binder owns the 1..10 bounds,
    -- as it owns every value rule in this repo (the binding:"revision"
    -- precedent) — a constraint name is not an error a caller can act on.
    max_concurrent_runs integer DEFAULT 3 NOT NULL,
    settings jsonb DEFAULT '{}'::jsonb NOT NULL,
    archived_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    department_key text,
    CONSTRAINT agents_pkey PRIMARY KEY (id),
    CONSTRAINT agents_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT agents_company_id_id_department_key_key UNIQUE (company_id, id, department_key),
    CONSTRAINT agents_company_reports_to_fkey FOREIGN KEY (company_id, reports_to_agent_id)
        REFERENCES agents(company_id, id)
        ON DELETE SET NULL (reports_to_agent_id)
        DEFERRABLE
);

CREATE UNIQUE INDEX agents_company_department_key_idx ON agents USING btree (company_id, department_key) WHERE department_key IS NOT NULL;
CREATE INDEX agents_company_reports_to_idx ON agents USING btree (company_id, reports_to_agent_id) WHERE archived_at IS NULL;
CREATE UNIQUE INDEX agents_one_root_per_company_idx ON agents USING btree (company_id) WHERE reports_to_agent_id IS NULL AND archived_at IS NULL;

CREATE TABLE department_configs (
    agent_id uuid NOT NULL,
    company_id uuid NOT NULL,
    department_key text NOT NULL,
    display_name text NOT NULL,
    icon_key text NOT NULL,
    accent_color text NOT NULL,
    avatar_key text NOT NULL,
    identity_style text NOT NULL,
    communication_tone text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT department_configs_pkey PRIMARY KEY (agent_id),
    CONSTRAINT department_configs_company_id_department_key_key UNIQUE (company_id, department_key),
    CONSTRAINT department_configs_agent_owner_fkey
        FOREIGN KEY (company_id, agent_id, department_key)
        REFERENCES agents(company_id, id, department_key) ON DELETE CASCADE
);

CREATE TABLE department_capabilities (
    agent_id uuid NOT NULL,
    company_id uuid NOT NULL,
    capability_key text NOT NULL,
    label text NOT NULL,
    "position" smallint NOT NULL,
    weight_percent smallint NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT department_capabilities_pkey PRIMARY KEY (agent_id, capability_key),
    CONSTRAINT department_capabilities_agent_id_position_key UNIQUE (agent_id, "position"),
    CONSTRAINT department_capabilities_agent_owner_fkey
        FOREIGN KEY (company_id, agent_id)
        REFERENCES agents(company_id, id) ON DELETE CASCADE
);
