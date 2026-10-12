package usage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func (s *DefaultUsageService) GetActivityInsight(
	ctx context.Context,
	principal auth.UserPrincipal,
) (ActivityInsight, error) {
	if s == nil || s.store == nil || ctx == nil {
		return ActivityInsight{}, ErrStore
	}

	var insight ActivityInsight
	err := s.store.WithinRawTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	}, func(tx generated.DBTX, queries *generated.Queries) error {
		if err := authorizeActivityInsightMember(ctx, tx, principal); err != nil {
			return err
		}
		row, err := queries.GetActivityInsight(ctx, generated.GetActivityInsightParams{
			CompanyID: principal.CompanyID, ActorUserID: principal.UserID,
		})
		if err != nil {
			return fmt.Errorf("read activity insight: %w", err)
		}
		var provider, model *string
		if row.Provider.Valid {
			value := row.Provider.String
			provider = &value
		}
		if row.Model.Valid {
			value := row.Model.String
			model = &value
		}
		var lastActiveAt *time.Time
		if row.LastActiveAt.Valid {
			value := row.LastActiveAt.Time.UTC()
			lastActiveAt = &value
		}
		insight = ActivityInsight{
			MostUsedProvider: provider,
			MostUsedModel:    model,
			SuccessfulTasks:  row.SuccessfulTasks,
			TotalSessions:    row.TotalSessions,
			CompletedRuns:    row.CompletedRuns,
			FailedRuns:       row.FailedRuns,
			TotalRuns:        row.TotalRuns,
			LastActiveAt:     lastActiveAt,
		}
		if (provider == nil) != (model == nil) {
			return errors.New("activity insight model attribution is incomplete")
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrActivityInsightForbidden) {
			return ActivityInsight{}, err
		}
		return ActivityInsight{}, fmt.Errorf("%w: %w", ErrStore, err)
	}
	return insight, nil
}

func authorizeActivityInsightMember(
	ctx context.Context,
	tx generated.DBTX,
	principal auth.UserPrincipal,
) error {
	var role, status string
	err := tx.QueryRow(ctx, `
		SELECT members.role, members.status
		FROM companies
		JOIN company_members AS members ON members.company_id = companies.id
		WHERE companies.id = $1 AND companies.deleting_at IS NULL AND members.user_id = $2`,
		principal.CompanyID, principal.UserID).Scan(&role, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrActivityInsightForbidden
	}
	if err != nil {
		return fmt.Errorf("authorize activity insight: %w", err)
	}
	if !auth.CompanyRole(role).Valid() || role != string(principal.Role) || status != "active" {
		return ErrActivityInsightForbidden
	}
	return nil
}
