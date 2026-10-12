-- Every Company gets the $2 monthly Huddle AI budget (1e10 ticks per USD)
-- unless an owner already configured one, including an explicit zero.
INSERT INTO huddle_ai_budgets (company_id, monthly_limit_usd_ticks)
SELECT id, 20000000000 FROM companies
ON CONFLICT (company_id) DO NOTHING;
