//go:build integration

package calendar

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestPersonalCalendarOwnershipRevisionAndOccurrences(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	s := newCalendarTestService(t, store)
	hints := 0
	if err := s.bus.Subscribe(EventChanged, func(_ context.Context, event events.Event) error {
		change, err := events.PayloadOf[Changed](event)
		if err != nil || change.UserID != fixture.UserA || change.CompanyWide {
			t.Errorf("personal calendar hint = %+v, %v", change, err)
		}
		hints++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a := auth.UserPrincipal{UserID: fixture.UserA}
	b := auth.UserPrincipal{UserID: fixture.UserB}
	emptyFrom := time.Date(2026, time.September, 22, 0, 0, 0, 0, time.UTC)
	empty, err := s.List(ctx, a, emptyFrom, emptyFrom.Add(24*time.Hour))
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty calendar = %v, %v; want non-nil empty list", empty, err)
	}
	input := Input{
		Type:       "reminder",
		Title:      "Review",
		StartLocal: "2026-09-22T09:00",
		EndLocal:   "2026-09-22T09:15",
		TimeZone:   "Asia/Ho_Chi_Minh",
		Recurrence: "daily",
		Color:      "sage",
	}
	event, err := s.Create(ctx, a, input)
	if err != nil {
		t.Fatal(err)
	}
	from, _ := time.Parse(time.RFC3339, "2026-09-22T00:00:00Z")
	to := from.Add(48 * time.Hour)
	private, err := s.List(ctx, b, from, to)
	if err != nil || len(private) != 0 {
		t.Fatalf("other user's list = %v, %v", private, err)
	}
	input.ExpectedRevision = event.Revision
	if _, err := s.Update(ctx, b, event.ID, input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user update = %v", err)
	}
	input.Title = "Updated"
	updated, err := s.Update(ctx, a, event.ID, input)
	if err != nil || updated.Revision != 2 {
		t.Fatalf("update = %v, %v", updated, err)
	}
	if _, err := s.Update(ctx, a, event.ID, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update = %v", err)
	}
	if err := s.Occurrence(
		ctx,
		a,
		event.ID,
		OccurrenceInput{ExpectedRevision: 2, OccurrenceDate: "2026-09-22", Action: "complete"},
	); err != nil {
		t.Fatal(err)
	}
	items, err := s.List(ctx, a, from, to)
	if err != nil || len(items) != 2 || !items[0].Completed || items[1].Completed {
		t.Fatalf("occurrence completion = %v, %v", items, err)
	}
	if err := s.Delete(ctx, b, event.ID, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user delete = %v", err)
	}
	if hints != 3 {
		t.Fatalf("calendar create/update/complete hints = %d, want 3; rejected changes must not emit", hints)
	}
}
