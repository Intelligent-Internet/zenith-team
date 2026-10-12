-- name: GetHuddleAIBudget :one
SELECT * FROM huddle_ai_budgets WHERE company_id=sqlc.arg(company_id)::uuid;

-- name: LockHuddleAIBudget :one
SELECT * FROM huddle_ai_budgets WHERE company_id=sqlc.arg(company_id)::uuid FOR UPDATE;

-- name: ConfigureHuddleAIBudget :one
INSERT INTO huddle_ai_budgets(company_id, monthly_limit_usd_ticks)
SELECT sqlc.arg(company_id)::uuid, sqlc.arg(monthly_limit_usd_ticks)::bigint
WHERE sqlc.arg(expected_revision)::bigint=0
ON CONFLICT(company_id) DO NOTHING
RETURNING *;

-- name: UpdateHuddleAIBudget :one
UPDATE huddle_ai_budgets
SET monthly_limit_usd_ticks=sqlc.arg(monthly_limit_usd_ticks)::bigint, revision=revision+1, updated_at=now()
WHERE company_id=sqlc.arg(company_id)::uuid AND revision=sqlc.arg(expected_revision)::bigint
RETURNING *;

-- name: GetHuddleAIBudgetCommitted :one
SELECT COALESCE(sum(CASE WHEN settled_at IS NULL THEN greatest(reserved_usd_ticks,spent_usd_ticks) ELSE spent_usd_ticks END),0)::bigint AS committed_usd_ticks
FROM huddle_ai_budget_reservations
WHERE company_id=sqlc.arg(company_id)::uuid AND period_start=sqlc.arg(period_start)::timestamptz;

-- name: GetHuddleAIBudgetReservation :one
SELECT * FROM huddle_ai_budget_reservations
WHERE company_id=sqlc.arg(company_id)::uuid AND huddle_id=sqlc.arg(huddle_id)::uuid
FOR UPDATE;

-- name: GetHuddleAIBudgetTotals :one
SELECT COALESCE(sum(spent_usd_ticks),0)::bigint AS spent_usd_ticks,
       COALESCE(sum(CASE WHEN settled_at IS NULL THEN greatest(reserved_usd_ticks-spent_usd_ticks,0) ELSE 0 END),0)::bigint AS reserved_usd_ticks
FROM huddle_ai_budget_reservations
WHERE company_id=sqlc.arg(company_id)::uuid AND period_start=sqlc.arg(period_start)::timestamptz;

-- name: HuddleAIHasStartBudget :one
SELECT EXISTS (
    SELECT 1 FROM huddle_ai_budgets b
    WHERE b.company_id=sqlc.arg(company_id)::uuid AND b.monthly_limit_usd_ticks>0
    AND CASE WHEN EXISTS (
        SELECT 1 FROM huddle_ai_budget_reservations r
        WHERE r.company_id=b.company_id AND r.huddle_id=sqlc.arg(huddle_id)::uuid
    ) THEN EXISTS (
        SELECT 1 FROM huddle_ai_budget_reservations r
        WHERE r.company_id=b.company_id AND r.huddle_id=sqlc.arg(huddle_id)::uuid
          AND r.settled_at IS NULL AND r.deadline_at>now() AND r.spent_usd_ticks<r.reserved_usd_ticks
    ) ELSE b.monthly_limit_usd_ticks - (
        SELECT COALESCE(sum(CASE WHEN r.settled_at IS NULL THEN greatest(r.reserved_usd_ticks,r.spent_usd_ticks) ELSE r.spent_usd_ticks END),0)
        FROM huddle_ai_budget_reservations r
        WHERE r.company_id=b.company_id AND r.period_start=sqlc.arg(period_start)::timestamptz
    ) >= sqlc.arg(minimum_ticks)::bigint END
)::boolean AS available;

-- name: ReserveHuddleAIBudget :one
INSERT INTO huddle_ai_budget_reservations(company_id,huddle_id,period_start,reserved_usd_ticks,deadline_at)
VALUES(sqlc.arg(company_id)::uuid,sqlc.arg(huddle_id)::uuid,sqlc.arg(period_start)::timestamptz,
    sqlc.arg(reserved_usd_ticks)::bigint,sqlc.arg(deadline_at)::timestamptz)
RETURNING *;

-- name: RecordHuddleAIBudgetSpend :one
UPDATE huddle_ai_budget_reservations
SET spent_usd_ticks=sqlc.arg(spent_usd_ticks)::bigint, updated_at=now(),
    settled_at=CASE WHEN sqlc.arg(final)::boolean THEN now() ELSE NULL END
WHERE company_id=sqlc.arg(company_id)::uuid AND huddle_id=sqlc.arg(huddle_id)::uuid
  AND settled_at IS NULL AND spent_usd_ticks<=sqlc.arg(spent_usd_ticks)::bigint
RETURNING *;
