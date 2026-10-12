-- Remove only untouched default budgets that never reserved spend; an owner's
-- edit raises the revision and a reservation references its budget.
DELETE FROM huddle_ai_budgets b
WHERE b.revision = 1 AND b.monthly_limit_usd_ticks = 20000000000
  AND NOT EXISTS (SELECT 1 FROM huddle_ai_budget_reservations r WHERE r.company_id = b.company_id);
