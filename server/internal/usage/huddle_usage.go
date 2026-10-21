package usage

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

var (
	ErrHuddleBudgetInvalid   = errors.New("invalid huddle budget")
	ErrHuddleBudgetForbidden = errors.New("huddle budget access forbidden")
	ErrHuddleBudgetConflict  = errors.New("huddle budget revision conflict")
	ErrHuddleBudgetExhausted = errors.New("huddle budget exhausted")
)

type HuddleBudget struct {
	MonthlyLimitUSDTicks string    `json:"monthlyLimitUsdTicks"`
	CommittedUSDTicks    string    `json:"committedUsdTicks"`
	SpentUSDTicks        string    `json:"spentUsdTicks"`
	ReservedUSDTicks     string    `json:"reservedUsdTicks"`
	RemainingUSDTicks    string    `json:"remainingUsdTicks"`
	Revision             int64     `json:"revision"`
	PeriodStart          time.Time `json:"periodStart"`
	PeriodEnd            time.Time `json:"periodEnd"`
	Warning              bool      `json:"warning"`
}

type ConfigureHuddleBudgetInput struct {
	MonthlyLimitUSDTicks int64
	ExpectedRevision     int64
}

// DefaultHuddleMonthlyLimitUSDTicks is the monthly voice AI budget a Company
// starts with: $2, at 1e10 ticks per USD. Owners can raise, lower or zero it.
const DefaultHuddleMonthlyLimitUSDTicks int64 = 2 * 10_000_000_000

// SeedHuddleBudgetInTx gives a new Company the default voice budget inside its
// creation transaction. An existing configuration is left unchanged.
func SeedHuddleBudgetInTx(ctx context.Context, q *generated.Queries, companyID uuid.UUID) error {
	_, err := q.ConfigureHuddleAIBudget(
		ctx,
		generated.ConfigureHuddleAIBudgetParams{
			CompanyID:            companyID,
			MonthlyLimitUsdTicks: DefaultHuddleMonthlyLimitUSDTicks,
			ExpectedRevision:     0,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// HuddleBudgetService owns Company voice spending configuration. Zero, including
// an absent configuration, authorizes no model spending; new Companies are
// seeded with DefaultHuddleMonthlyLimitUSDTicks.
type HuddleBudgetService interface {
	Get(context.Context, auth.UserPrincipal) (HuddleBudget, error)
	Configure(context.Context, auth.UserPrincipal, ConfigureHuddleBudgetInput) (HuddleBudget, error)
}

var _ HuddleBudgetService = (*DefaultHuddleBudgetService)(nil)

type DefaultHuddleBudgetService struct{ store *db.Store }

func NewHuddleBudgetService(store *db.Store) (*DefaultHuddleBudgetService, error) {
	if store == nil {
		return nil, errors.New("huddle budget store is required")
	}
	return &DefaultHuddleBudgetService{store: store}, nil
}

func (s *DefaultHuddleBudgetService) Get(ctx context.Context, principal auth.UserPrincipal) (HuddleBudget, error) {
	var result HuddleBudget
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockHuddleBudgetActor(ctx, q, principal, auth.PermissionUsageRead); err != nil {
			return err
		}
		var err error
		result, err = readHuddleBudget(ctx, q, principal.CompanyID)
		return err
	})
	return result, err
}

func (s *DefaultHuddleBudgetService) Configure(
	ctx context.Context,
	principal auth.UserPrincipal,
	input ConfigureHuddleBudgetInput,
) (HuddleBudget, error) {
	if input.MonthlyLimitUSDTicks < 0 || input.MonthlyLimitUSDTicks > 9007199254740991 ||
		input.ExpectedRevision < 0 ||
		input.ExpectedRevision >= 9007199254740991 {
		return HuddleBudget{}, ErrHuddleBudgetInvalid
	}
	var result HuddleBudget
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockHuddleBudgetActor(ctx, q, principal, auth.PermissionCompanyProfileUpdate); err != nil {
			return err
		}
		var err error
		if input.ExpectedRevision == 0 {
			_, err = q.ConfigureHuddleAIBudget(
				ctx,
				generated.ConfigureHuddleAIBudgetParams{
					CompanyID:            principal.CompanyID,
					MonthlyLimitUsdTicks: input.MonthlyLimitUSDTicks,
					ExpectedRevision:     input.ExpectedRevision,
				},
			)
		} else {
			_, err = q.UpdateHuddleAIBudget(
				ctx,
				generated.UpdateHuddleAIBudgetParams{
					CompanyID:            principal.CompanyID,
					MonthlyLimitUsdTicks: input.MonthlyLimitUSDTicks,
					ExpectedRevision:     input.ExpectedRevision,
				},
			)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrHuddleBudgetConflict
		}
		if err != nil {
			return err
		}
		result, err = readHuddleBudget(ctx, q, principal.CompanyID)
		return err
	})
	return result, err
}

func lockHuddleBudgetActor(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	permission auth.Permission,
) error {
	if _, err := q.LockCompanyForMembershipRead(ctx, principal.CompanyID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrHuddleBudgetForbidden
		}
		return err
	}
	membership, err := q.GetCompanyMemberForShare(
		ctx,
		generated.GetCompanyMemberForShareParams{CompanyID: principal.CompanyID, UserID: principal.UserID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrHuddleBudgetForbidden
	}
	if err != nil {
		return err
	}
	if membership.Status != "active" || !auth.CompanyRole(membership.Role).Can(permission) {
		return ErrHuddleBudgetForbidden
	}
	return nil
}

func readHuddleBudget(ctx context.Context, q *generated.Queries, companyID uuid.UUID) (HuddleBudget, error) {
	budget, err := q.GetHuddleAIBudget(ctx, companyID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return HuddleBudget{}, err
	}
	start := huddleBudgetPeriod(time.Now())
	totals, err := q.GetHuddleAIBudgetTotals(
		ctx,
		generated.GetHuddleAIBudgetTotalsParams{
			CompanyID:   companyID,
			PeriodStart: pgtype.Timestamptz{Time: start, Valid: true},
		},
	)
	if err != nil {
		return HuddleBudget{}, err
	}
	committed := totals.SpentUsdTicks + totals.ReservedUsdTicks
	return HuddleBudget{
		MonthlyLimitUSDTicks: strconv.FormatInt(budget.MonthlyLimitUsdTicks, 10),
		CommittedUSDTicks:    strconv.FormatInt(committed, 10),
		SpentUSDTicks:        strconv.FormatInt(totals.SpentUsdTicks, 10),
		ReservedUSDTicks:     strconv.FormatInt(totals.ReservedUsdTicks, 10),
		RemainingUSDTicks:    strconv.FormatInt(max(0, budget.MonthlyLimitUsdTicks-committed), 10),
		Revision:             budget.Revision, PeriodStart: start, PeriodEnd: start.AddDate(0, 1, 0),
		Warning: budget.MonthlyLimitUsdTicks > 0 && committed >= (budget.MonthlyLimitUsdTicks*4+4)/5,
	}, nil
}

func huddleBudgetPeriod(now time.Time) time.Time {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}

type ReserveHuddleBudgetInput struct {
	CompanyID       uuid.UUID
	HuddleID        uuid.UUID
	Deadline        time.Time
	TicksPerSecond  int64
	AdditionalTicks int64
}

// ReserveHuddleBudgetInTx participates in the caller's Huddle transaction.
// All allocators lock the Company budget before its reservation; this serializes
// different rooms competing for the same allowance. Existing reservations never
// move to a new period or acquire a new allowance on retry.
func ReserveHuddleBudgetInTx(
	ctx context.Context,
	q *generated.Queries,
	input ReserveHuddleBudgetInput,
) (generated.HuddleAiBudgetReservation, error) {
	now := time.Now().UTC()
	if input.CompanyID == uuid.Nil || input.HuddleID == uuid.Nil || input.TicksPerSecond <= 0 ||
		input.TicksPerSecond > 1000000000000 ||
		input.AdditionalTicks < 0 ||
		input.AdditionalTicks > 9007199254740991 ||
		!input.Deadline.After(now) ||
		input.Deadline.After(now.Add(time.Hour)) {
		return generated.HuddleAiBudgetReservation{}, ErrHuddleBudgetInvalid
	}
	budget, err := q.LockHuddleAIBudget(ctx, input.CompanyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.HuddleAiBudgetReservation{}, ErrHuddleBudgetExhausted
	}
	if err != nil {
		return generated.HuddleAiBudgetReservation{}, err
	}
	if budget.MonthlyLimitUsdTicks == 0 {
		return generated.HuddleAiBudgetReservation{}, ErrHuddleBudgetExhausted
	}
	// Lock contention can outlive a short budget deadline.
	now = time.Now().UTC()
	existing, err := q.GetHuddleAIBudgetReservation(
		ctx,
		generated.GetHuddleAIBudgetReservationParams{CompanyID: input.CompanyID, HuddleID: input.HuddleID},
	)
	if err == nil {
		if existing.SettledAt.Valid || existing.SpentUsdTicks >= existing.ReservedUsdTicks ||
			!existing.DeadlineAt.Time.After(now) {
			return generated.HuddleAiBudgetReservation{}, ErrHuddleBudgetExhausted
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return generated.HuddleAiBudgetReservation{}, err
	}
	period := pgtype.Timestamptz{Time: huddleBudgetPeriod(now), Valid: true}
	committed, err := q.GetHuddleAIBudgetCommitted(
		ctx,
		generated.GetHuddleAIBudgetCommittedParams{CompanyID: input.CompanyID, PeriodStart: period},
	)
	if err != nil {
		return generated.HuddleAiBudgetReservation{}, err
	}
	available := max(0, budget.MonthlyLimitUsdTicks-committed-input.AdditionalTicks)
	seconds := min(int64(input.Deadline.Sub(now)/time.Second), available/input.TicksPerSecond)
	if seconds < 1 {
		return generated.HuddleAiBudgetReservation{}, ErrHuddleBudgetExhausted
	}
	return q.ReserveHuddleAIBudget(ctx, generated.ReserveHuddleAIBudgetParams{
		CompanyID: input.CompanyID, HuddleID: input.HuddleID, PeriodStart: period,
		ReservedUsdTicks: seconds*input.TicksPerSecond + input.AdditionalTicks,
		DeadlineAt:       pgtype.Timestamptz{Time: now.Add(time.Duration(seconds) * time.Second), Valid: true},
	})
}

type RecordHuddleSpendInput struct {
	CompanyID     uuid.UUID
	HuddleID      uuid.UUID
	SpentUSDTicks int64
	Final         bool
}

// RecordHuddleSpendInTx records the reconciled cumulative cost of all attempts
// for a Huddle, never an additive heartbeat counter. The caller must establish
// that all model sessions have closed and all usage is reconciled before Final.
// An uncertain crash retains its hold. Provider overages remain real costs and
// are retained even if they exceed the original reservation.
func RecordHuddleSpendInTx(ctx context.Context, q *generated.Queries, input RecordHuddleSpendInput) error {
	if input.CompanyID == uuid.Nil || input.HuddleID == uuid.Nil || input.SpentUSDTicks < 0 ||
		input.SpentUSDTicks > 9007199254740991 {
		return ErrHuddleBudgetInvalid
	}
	if _, err := q.LockHuddleAIBudget(ctx, input.CompanyID); err != nil {
		return err
	}
	reservation, err := q.GetHuddleAIBudgetReservation(
		ctx,
		generated.GetHuddleAIBudgetReservationParams{CompanyID: input.CompanyID, HuddleID: input.HuddleID},
	)
	if err != nil {
		return err
	}
	if reservation.SettledAt.Valid {
		if input.Final && reservation.SpentUsdTicks == input.SpentUSDTicks {
			return nil
		}
		return ErrHuddleBudgetConflict
	}
	if input.SpentUSDTicks < reservation.SpentUsdTicks {
		return ErrHuddleBudgetConflict
	}
	_, err = q.RecordHuddleAIBudgetSpend(
		ctx,
		generated.RecordHuddleAIBudgetSpendParams{
			CompanyID:     input.CompanyID,
			HuddleID:      input.HuddleID,
			SpentUsdTicks: input.SpentUSDTicks,
			Final:         input.Final,
		},
	)
	return err
}
