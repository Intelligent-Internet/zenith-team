-- 000067_analytics_foundation: record the lifecycle and provider evidence
-- needed for truthful Analytics reads. Historical usage facts stay immutable;
-- missing historical lifecycle/provider fields remain NULL where the old
-- system never recorded them.

ALTER TABLE runs
    ADD COLUMN queued_at timestamp with time zone,
    ADD COLUMN claimed_at timestamp with time zone,
    ADD COLUMN started_at timestamp with time zone,
    ADD COLUMN finished_at timestamp with time zone;

-- Historical Runs retain unknown lifecycle timestamps. New terminal writes
-- record finished_at; no timestamp is inferred from an unrelated last update.

CREATE INDEX runs_company_finished_idx
    ON runs (company_id, finished_at)
    WHERE finished_at IS NOT NULL;

CREATE INDEX runs_company_authorizer_finished_idx
    ON runs (company_id, execution_authorized_by_user_id, finished_at)
    WHERE finished_at IS NOT NULL;

CREATE INDEX runs_finished_idx
    ON runs (finished_at DESC)
    WHERE finished_at IS NOT NULL;

ALTER TABLE run_usage
    ADD COLUMN reasoning_tokens bigint,
    ADD COLUMN turns integer,
    ADD COLUMN provider_duration_ms bigint,
    ADD COLUMN billing_mode text DEFAULT 'unknown' NOT NULL
        CONSTRAINT run_usage_billing_mode_check
        CHECK (billing_mode IN ('api', 'subscription', 'unknown'));

CREATE TABLE model_prices (
    provider text NOT NULL CHECK (provider IN ('claude', 'codex')),
    model text NOT NULL,
    effective_from timestamp with time zone NOT NULL,
    input_usd_per_mtok numeric(20,10) NOT NULL,
    output_usd_per_mtok numeric(20,10) NOT NULL,
    cache_read_usd_per_mtok numeric(20,10) NOT NULL,
    cache_write_usd_per_mtok numeric(20,10) NOT NULL,
    source text NOT NULL,
    created_at timestamp with time zone NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (provider, model, effective_from),
    CONSTRAINT model_prices_model_check CHECK (model = lower(model)),
    CONSTRAINT model_prices_nonnegative_check CHECK (
        input_usd_per_mtok >= 0
        AND output_usd_per_mtok >= 0
        AND cache_read_usd_per_mtok >= 0
        AND cache_write_usd_per_mtok >= 0
    )
);

-- Verified standard API rates as of this snapshot; never apply today's price
-- retroactively to historical facts. Missing models retain unknown cost.
INSERT INTO model_prices (provider, model, effective_from, input_usd_per_mtok,
    output_usd_per_mtok, cache_read_usd_per_mtok, cache_write_usd_per_mtok, source)
VALUES
    ('codex', 'gpt-5', '2026-09-30T00:00:00Z', 1.25, 10, 0.125, 0,
     'https://developers.openai.com/api/docs/models/gpt-5'),
    ('codex', 'gpt-5.3-codex', '2026-09-30T00:00:00Z', 1.75, 14, 0.175, 0,
     'https://developers.openai.com/api/docs/models/gpt-5.3-codex');

-- Proposed analytics surfaces keep mutable policy separate from immutable usage
-- facts. A budget is a soft notification threshold: it never participates in
-- run admission and therefore cannot block an already queued Run.
CREATE TABLE analytics_budgets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    owner_kind text NOT NULL CHECK (owner_kind IN ('company', 'agent')),
    agent_id uuid,
    monthly_limit_usd_ticks bigint NOT NULL CHECK (monthly_limit_usd_ticks > 0),
    enabled boolean NOT NULL DEFAULT true,
    created_by_user_id uuid NOT NULL,
    created_at timestamp with time zone NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamp with time zone NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (id),
    UNIQUE NULLS NOT DISTINCT (company_id, owner_kind, agent_id),
    CONSTRAINT analytics_budgets_agent_shape_check CHECK (
        (owner_kind = 'company' AND agent_id IS NULL)
        OR (owner_kind = 'agent' AND agent_id IS NOT NULL)
    ),
    CONSTRAINT analytics_budgets_agent_company_fkey
        FOREIGN KEY (company_id, agent_id) REFERENCES agents(company_id, id) ON DELETE CASCADE
);

CREATE INDEX analytics_budgets_company_enabled_idx
    ON analytics_budgets (company_id, enabled);

CREATE TABLE analytics_budget_alerts (
    budget_id uuid NOT NULL REFERENCES analytics_budgets(id) ON DELETE CASCADE,
    period_start date NOT NULL,
    threshold_percent smallint NOT NULL CHECK (threshold_percent IN (80, 100)),
    created_at timestamp with time zone NOT NULL DEFAULT statement_timestamp(),
    crossed_at timestamp with time zone NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (budget_id, period_start, threshold_percent)
);

ALTER TABLE inbox_items
    DROP CONSTRAINT inbox_items_kind_check,
    ADD CONSTRAINT inbox_items_kind_check CHECK (kind IN (
        'task_assigned','blocker','run_completed','run_failed','approval_request',
        'question_request','runtime_offline','invitation_renewal','calendar_reminder',
        'calendar_update','discussion_mention','task_run_requested','task_handoff','analytics_budget'));

-- Price rows must have a verified effective date and source. An absent price
-- remains unknown; never seed a guessed historical rate into production.
