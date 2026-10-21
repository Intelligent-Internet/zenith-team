//go:build integration

package conversation

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/usage"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestHuddleBudgetReservation(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	budgetService, err := usage.NewHuddleBudgetService(f.store)
	if err != nil {
		t.Fatal(err)
	}
	huddles, err := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	huddle, err := huddles.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	input := usage.ReserveHuddleBudgetInput{
		CompanyID:       f.a.CompanyID,
		HuddleID:        huddle.Huddle.ID,
		Deadline:        time.Now().Add(10 * time.Minute),
		TicksPerSecond:  100,
		AdditionalTicks: 1000,
	}
	reserve := func() (generated.HuddleAiBudgetReservation, error) {
		var result generated.HuddleAiBudgetReservation
		err := f.store.WithinTx(f.ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			var err error
			result, err = usage.ReserveHuddleBudgetInTx(f.ctx, q, input)
			return err
		})
		return result, err
	}
	if _, err := reserve(); !errors.Is(err, usage.ErrHuddleBudgetExhausted) {
		t.Fatalf("unconfigured reserve error = %v", err)
	}
	if _, err := budgetService.Configure(
		f.ctx,
		f.a,
		usage.ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: 4000},
	); err != nil {
		t.Fatal(err)
	}
	first, err := reserve()
	if err != nil {
		t.Fatal(err)
	}
	if first.ReservedUsdTicks != 4000 || time.Until(first.DeadlineAt.Time) > 30*time.Second ||
		time.Until(first.DeadlineAt.Time) < 25*time.Second {
		t.Fatalf("partial reservation = %+v, want 4000 ticks and 30 seconds", first)
	}
	second, err := reserve()
	if err != nil || first.ID != second.ID || !first.DeadlineAt.Time.Equal(second.DeadlineAt.Time) {
		t.Fatalf("retry = %+v, error = %v", second, err)
	}
	report, err := budgetService.Get(f.ctx, f.a)
	if err != nil || report.CommittedUSDTicks != "4000" || report.RemainingUSDTicks != "0" || !report.Warning {
		t.Fatalf("report = %+v, error = %v", report, err)
	}
	if report.SpentUSDTicks != "0" || report.ReservedUSDTicks != "4000" {
		t.Fatalf("initial spend/hold = %+v", report)
	}
	if _, err := budgetService.Configure(
		f.ctx,
		f.a,
		usage.ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: 0, ExpectedRevision: 1},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := reserve(); !errors.Is(err, usage.ErrHuddleBudgetExhausted) {
		t.Fatalf("disabled reserve error = %v", err)
	}
	spend := usage.RecordHuddleSpendInput{CompanyID: f.a.CompanyID, HuddleID: huddle.Huddle.ID, SpentUSDTicks: 1500}
	record := func() error {
		return f.store.WithinTx(
			f.ctx,
			pgx.TxOptions{},
			func(q *generated.Queries) error { return usage.RecordHuddleSpendInTx(f.ctx, q, spend) },
		)
	}
	if err := record(); err != nil {
		t.Fatal(err)
	}
	report, err = budgetService.Get(f.ctx, f.a)
	if err != nil || report.CommittedUSDTicks != "4000" {
		t.Fatalf("uncertain commitment = %+v, error = %v", report, err)
	}
	if report.SpentUSDTicks != "1500" || report.ReservedUSDTicks != "2500" {
		t.Fatalf("uncertain spend/hold = %+v", report)
	}
	spend.Final = true
	if err := record(); err != nil {
		t.Fatal(err)
	}
	if err := record(); err != nil {
		t.Fatalf("final replay: %v", err)
	}
	report, err = budgetService.Get(f.ctx, f.a)
	if err != nil || report.CommittedUSDTicks != "1500" {
		t.Fatalf("settled commitment = %+v, error = %v", report, err)
	}
	if report.SpentUSDTicks != "1500" || report.ReservedUSDTicks != "0" {
		t.Fatalf("settled spend/hold = %+v", report)
	}
	spend.SpentUSDTicks = 1400
	if err := record(); !errors.Is(err, usage.ErrHuddleBudgetConflict) {
		t.Fatalf("conflicting final error = %v", err)
	}
}

func TestHuddleBudgetConcurrentRooms(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	budgets, err := usage.NewHuddleBudgetService(f.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := budgets.Configure(
		f.ctx,
		f.a,
		usage.ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: 10000},
	); err != nil {
		t.Fatal(err)
	}
	huddles, err := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus})
	if err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for _, peer := range []uuid.UUID{f.b.UserID, f.c.UserID} {
		direct, err := f.conversations.CreateHumanDirect(
			f.ctx,
			f.a,
			CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{peer}},
		)
		if err != nil {
			t.Fatal(err)
		}
		huddle, err := huddles.Start(
			f.ctx,
			f.a,
			direct.Conversation.ID,
			StartHuddleRequest{RequestKey: uuid.New()},
		)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, huddle.Huddle.ID)
	}
	results := make(chan error)
	for _, id := range ids {
		go func() {
			err := f.store.WithinTx(f.ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
				_, err := usage.ReserveHuddleBudgetInTx(
					f.ctx,
					q,
					usage.ReserveHuddleBudgetInput{
						CompanyID:      f.a.CompanyID,
						HuddleID:       id,
						Deadline:       time.Now().Add(time.Hour),
						TicksPerSecond: 100,
					},
				)
				return err
			})
			results <- err
		}()
	}
	var succeeded, exhausted int
	for range ids {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, usage.ErrHuddleBudgetExhausted):
			exhausted++
		default:
			t.Errorf("reserve error = %v", err)
		}
	}
	if succeeded != 1 || exhausted != 1 {
		t.Fatalf("successful/exhausted = %d/%d, want 1/1", succeeded, exhausted)
	}
	report, err := budgets.Get(f.ctx, f.a)
	if err != nil || report.CommittedUSDTicks != "10000" {
		t.Fatalf("concurrent commitment = %+v, error = %v", report, err)
	}
}
