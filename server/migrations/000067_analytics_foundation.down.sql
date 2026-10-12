DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM inbox_items WHERE kind = 'analytics_budget') THEN
        RAISE EXCEPTION 'Analytics budget notifications must be retained; use a forward migration';
    END IF;
END $$;
ALTER TABLE inbox_items
    DROP CONSTRAINT inbox_items_kind_check,
    ADD CONSTRAINT inbox_items_kind_check CHECK (kind IN (
        'task_assigned','blocker','run_completed','run_failed','approval_request',
        'question_request','runtime_offline','invitation_renewal','calendar_reminder',
        'calendar_update','discussion_mention','task_run_requested','task_handoff'));
DROP TABLE IF EXISTS analytics_budget_alerts;
DROP TABLE IF EXISTS analytics_budgets;
DROP TABLE IF EXISTS model_prices;

ALTER TABLE run_usage
    DROP CONSTRAINT IF EXISTS run_usage_billing_mode_check,
    DROP COLUMN IF EXISTS billing_mode,
    DROP COLUMN IF EXISTS provider_duration_ms,
    DROP COLUMN IF EXISTS turns,
    DROP COLUMN IF EXISTS reasoning_tokens;

DROP INDEX IF EXISTS runs_company_authorizer_finished_idx;
DROP INDEX IF EXISTS runs_company_finished_idx;
DROP INDEX IF EXISTS runs_finished_idx;
ALTER TABLE runs
    DROP COLUMN IF EXISTS finished_at,
    DROP COLUMN IF EXISTS started_at,
    DROP COLUMN IF EXISTS claimed_at,
    DROP COLUMN IF EXISTS queued_at;
