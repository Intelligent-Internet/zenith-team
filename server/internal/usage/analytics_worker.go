package usage

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// AnalyticsWorker emits soft budget notifications. Usage facts remain the
// source of truth, and the alert unique key deduplicates notices across passes.
type AnalyticsWorker struct {
	store     *db.Store
	presenter inbox.Presenter
	bus       *events.Bus
}

func NewAnalyticsWorker(store *db.Store, presenter inbox.Presenter, bus *events.Bus) (*AnalyticsWorker, error) {
	if store == nil {
		return nil, errors.New("analytics worker store is required")
	}
	return &AnalyticsWorker{store: store, presenter: presenter, bus: bus}, nil
}

func (w *AnalyticsWorker) Tick(ctx context.Context) error {
	if w == nil || w.store == nil {
		return ErrStore
	}
	return w.scanCompanies(ctx)
}

func (w *AnalyticsWorker) scanCompanies(ctx context.Context) error {
	// Use a read-only pass to discover Companies with enabled budgets. The
	// follow-up write transaction is per Company, which avoids holding a global
	// lock while a large installation is priced.
	var companies []uuid.UUID
	if err := w.store.WithinRawTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx generated.DBTX, _ *generated.Queries) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT company_id FROM analytics_budgets WHERE enabled ORDER BY company_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var companyID uuid.UUID
			if err := rows.Scan(&companyID); err != nil {
				return err
			}
			companies = append(companies, companyID)
		}
		return rows.Err()
	}); err != nil {
		return err
	}
	var failures []error
	for _, companyID := range companies {
		if err := w.scanCompany(ctx, companyID); err != nil {
			failures = append(failures, err)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(failures...)
}

func (w *AnalyticsWorker) scanCompany(ctx context.Context, companyID uuid.UUID) error {
	var pending []events.Event
	var notifications []inbox.CreateResult
	err := w.store.WithinRawTx(ctx, pgx.TxOptions{}, func(tx generated.DBTX, q *generated.Queries) error {
		if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '5s'"); err != nil {
			return err
		}
		if _, err := q.LockCompanyForMembershipRead(ctx, companyID); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		targets, err := q.ListAnalyticsBudgetTargets(ctx, companyID)
		if err != nil {
			return err
		}
		recipientRows, err := tx.Query(ctx, `SELECT user_id FROM company_members WHERE company_id=$1 AND status='active' AND role=ANY($2::text[]) ORDER BY user_id FOR SHARE`, companyID, auth.RolesWith(auth.PermissionUsageRead))
		if err != nil {
			return err
		}
		var recipients []uuid.UUID
		for recipientRows.Next() {
			var recipientID uuid.UUID
			if err := recipientRows.Scan(&recipientID); err != nil {
				recipientRows.Close()
				return err
			}
			recipients = append(recipients, recipientID)
		}
		if err := recipientRows.Err(); err != nil {
			recipientRows.Close()
			return err
		}
		recipientRows.Close()
		for _, target := range targets {
			var spent string
			var agentID any
			if target.AgentID.Valid {
				agentID = target.AgentID.UUID
			}
			if err := tx.QueryRow(ctx, `SELECT coalesce(sum(CASE
				WHEN u.billing_mode <> 'subscription' AND u.provider_cost_usd IS NOT NULL
					THEN round(u.provider_cost_usd * 10000000000)
				WHEN u.billing_mode = 'api' AND u.provider_cost_usd IS NULL AND price.model IS NOT NULL
					THEN round((u.input_tokens*price.input_usd_per_mtok + u.output_tokens*price.output_usd_per_mtok + u.cache_read_tokens*price.cache_read_usd_per_mtok + u.cache_write_tokens*price.cache_write_usd_per_mtok)*10000)
				ELSE 0 END),0)::text
				FROM runs r JOIN run_usage u ON u.run_id=r.id
				LEFT JOIN LATERAL (SELECT model,input_usd_per_mtok,output_usd_per_mtok,cache_read_usd_per_mtok,cache_write_usd_per_mtok
					FROM model_prices WHERE provider=u.provider AND model=lower(u.model) AND effective_from<=r.finished_at
					ORDER BY effective_from DESC LIMIT 1) price ON true
				WHERE r.company_id=$1 AND r.finished_at >= ($2::date::timestamp AT TIME ZONE 'UTC') AND r.finished_at < (($2::date + interval '1 month') AT TIME ZONE 'UTC')
				AND ($3::uuid IS NULL OR r.agent_id=$3)`, companyID, target.PeriodStart.Time, agentID).Scan(&spent); err != nil {
				return err
			}
			spentValue, ok := new(big.Int).SetString(spent, 10)
			if !ok {
				return fmt.Errorf("invalid analytics spend %q", spent)
			}
			limit := big.NewInt(target.MonthlyLimitUsdTicks)
			for _, threshold := range []int16{80, 100} {
				thresholdValue := new(big.Int).Mul(limit, big.NewInt(int64(threshold)))
				if new(big.Int).Mul(spentValue, big.NewInt(100)).Cmp(thresholdValue) < 0 {
					continue
				}
				_, err := q.InsertAnalyticsBudgetAlert(ctx, generated.InsertAnalyticsBudgetAlertParams{
					BudgetID: target.ID, PeriodStart: target.PeriodStart, ThresholdPercent: threshold,
				})
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				title, body := analyticsBudgetNotice(target.OwnerName, threshold)
				for _, recipientID := range recipients {
					deepLink := "/analytics?tab=overview"
					result, err := inbox.CreateInTx(ctx, q, &pending, inbox.CreateInput{
						CompanyID: companyID, RecipientUserID: recipientID,
						Type:      inbox.TypeAnalyticsBudget,
						Entity:    inbox.Entity{Type: protocol.EntityCompany, ID: companyID.String()},
						Title:     title,
						Body:      body,
						DeepLink:  &deepLink,
						DedupeKey: fmt.Sprintf("analytics-budget:%s:%s:%d", target.ID, target.PeriodStart.Time.Format("2006-01"), threshold),
					})
					if err != nil {
						return err
					}
					if result.Created {
						notifications = append(notifications, result)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("analytics budget company %s: %w", companyID, err)
	}
	for _, event := range pending {
		if w.bus != nil {
			w.bus.Publish(ctx, inbox.Topic, event)
		}
	}
	for _, notification := range notifications {
		inbox.PresentCreated(w.presenter, notification)
	}
	return nil
}

func analyticsBudgetNotice(name string, threshold int16) (string, string) {
	name = strings.Join(strings.Fields(name), " ")
	if runes := []rune(name); len(runes) > 64 {
		name = strings.TrimSpace(string(runes[:63])) + "…"
	}
	if threshold == 100 {
		return name + " reached its monthly budget",
			"100% of the monthly budget has been used. Work continues. Review spending in Analytics."
	}
	return name + " is nearing its monthly budget",
		"80% of the monthly budget has been used. Review spending in Analytics."
}
