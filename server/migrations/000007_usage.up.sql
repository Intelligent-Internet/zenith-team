-- 000007_usage: immutable provider/model usage facts reported once per run.
-- The table is deliberately FK-free so accounting evidence survives deletion
-- of the run and of the dimensions it measured.

CREATE TABLE run_usage (
    -- bigint, matching runs.id; still no FK, so a usage fact outlives the run.
    run_id bigint NOT NULL,
    provider text NOT NULL,
    model text NOT NULL,
    input_tokens bigint DEFAULT 0 NOT NULL,
    output_tokens bigint DEFAULT 0 NOT NULL,
    cache_read_tokens bigint DEFAULT 0 NOT NULL,
    cache_write_tokens bigint DEFAULT 0 NOT NULL,
    provider_cost_usd numeric(30,12),
    raw_json jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT run_usage_pkey PRIMARY KEY (run_id, provider, model),
    CONSTRAINT run_usage_provider_check CHECK (provider IN ('claude', 'codex'))
);
