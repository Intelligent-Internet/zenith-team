CREATE TABLE huddle_ai_budgets (
    company_id uuid PRIMARY KEY REFERENCES companies(id) ON DELETE CASCADE,
    monthly_limit_usd_ticks bigint NOT NULL CHECK (monthly_limit_usd_ticks >= 0),
    revision bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Reservations are product state. They pin a room's original UTC month even
-- when the room crosses a month boundary; retrying cannot acquire a fresh quota.
CREATE TABLE huddle_ai_budget_reservations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL,
    huddle_id uuid NOT NULL UNIQUE,
    period_start timestamptz NOT NULL,
    reserved_usd_ticks bigint NOT NULL CHECK (reserved_usd_ticks >= 0),
    spent_usd_ticks bigint NOT NULL DEFAULT 0 CHECK (spent_usd_ticks >= 0),
    deadline_at timestamptz NOT NULL,
    settled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(company_id, id),
    FOREIGN KEY(company_id, huddle_id) REFERENCES huddles(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY(company_id) REFERENCES huddle_ai_budgets(company_id) ON DELETE CASCADE
);
CREATE INDEX huddle_ai_budget_period_idx ON huddle_ai_budget_reservations(company_id, period_start);
