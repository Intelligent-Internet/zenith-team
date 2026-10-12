CREATE TABLE huddle_ai_states (
    huddle_id uuid PRIMARY KEY,
    company_id uuid NOT NULL,
    disabled boolean NOT NULL DEFAULT false,
    revision bigint NOT NULL DEFAULT 1,
    generation bigint NOT NULL DEFAULT 0,
    chat_start_seq bigint NOT NULL,
    chat_cursor bigint NOT NULL,
    chat_completed_seq bigint NOT NULL,
    greeted boolean NOT NULL DEFAULT false,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','starting','listening','speaking','unavailable','disabled','ended')),
    retry_at timestamptz NOT NULL DEFAULT now(),
    failures integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(company_id,huddle_id),
    FOREIGN KEY(company_id,huddle_id) REFERENCES huddles(company_id,id) ON DELETE CASCADE
);
CREATE TABLE huddle_ai_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL,
    huddle_id uuid NOT NULL,
    generation bigint NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','claimed','stopping','finished')),
    capability_hash bytea,
    lease_expires_at timestamptz NOT NULL DEFAULT now()+interval '30 seconds',
    deadline_at timestamptz NOT NULL,
    dispatch_after timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    usage_final boolean NOT NULL DEFAULT false,
    model_seconds double precision NOT NULL DEFAULT 0 CHECK(model_seconds>=0),
    spent_usd_ticks bigint NOT NULL DEFAULT 0 CHECK(spent_usd_ticks>=0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(company_id,id),
    UNIQUE(company_id,huddle_id,generation),
    FOREIGN KEY(company_id,huddle_id) REFERENCES huddle_ai_states(company_id,huddle_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX huddle_ai_sessions_active_idx ON huddle_ai_sessions(huddle_id) WHERE state <> 'finished';
CREATE TABLE huddle_ai_operations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL,
    huddle_id uuid NOT NULL,
    user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    enabled boolean NOT NULL,
    expected_revision bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(company_id,huddle_id,user_id,request_key),
    FOREIGN KEY(company_id,huddle_id) REFERENCES huddle_ai_states(company_id,huddle_id) ON DELETE CASCADE
);
CREATE TABLE huddle_ai_usage_receipts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL,
    huddle_id uuid NOT NULL,
    attempt_id uuid NOT NULL,
    model_session_id text NOT NULL,
    model_seconds double precision NOT NULL CHECK(model_seconds>=0),
    input_tokens bigint NOT NULL CHECK(input_tokens>=0),
    cached_input_tokens bigint NOT NULL CHECK(cached_input_tokens>=0 AND cached_input_tokens<=input_tokens),
    output_tokens bigint NOT NULL CHECK(output_tokens>=0),
    final boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(attempt_id,model_session_id)
);
