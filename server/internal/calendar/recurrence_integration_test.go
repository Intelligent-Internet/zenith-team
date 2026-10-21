//go:build integration

package calendar

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestCustomRecurrencePersistsAndRemindsOnlyScheduledOccurrences(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO company_members(company_id,user_id,role,status) VALUES($1,$2,'member','active')`,
		f.CompanyA,
		f.UserB,
	); err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	s := newCalendarTestService(t, store)
	actor := auth.UserPrincipal{UserID: f.UserA}
	count, lead := int32(3), int32(10)
	input := Input{
		Type:               "event",
		Title:              "Alternating team review",
		Color:              "sage",
		StartLocal:         "2026-09-23T09:00",
		EndLocal:           "2026-09-23T10:00",
		TimeZone:           "UTC",
		Recurrence:         "weekly",
		RecurrenceInterval: 2,
		RecurrenceWeekdays: []int32{5, 1},
		RecurrenceCount:    &count,
		ReminderMinutes:    &lead,
	}
	event, err := s.Create(ctx, actor, input)
	if err != nil {
		t.Fatal(err)
	}
	if event.RecurrenceInterval != 2 || !slices.Equal(event.RecurrenceWeekdays, []int32{1, 5}) ||
		event.RecurrenceCount == nil ||
		*event.RecurrenceCount != 3 {
		t.Fatalf("custom rule not preserved: %+v", event)
	}
	from := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	items, err := s.List(ctx, actor, from, from.AddDate(0, 1, 0))
	var got []string
	for _, item := range items {
		got = append(got, item.OccurrenceDate)
	}
	want := []string{"2026-09-25", "2026-10-05", "2026-10-09"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("occurrences = %v, %v; want %v", got, err, want)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reminders := NewReminderService(store, bus, nil)
	for _, date := range []string{"2026-09-23", "2026-09-25", "2026-10-05", "2026-10-09", "2026-10-19"} {
		now, _ := time.Parse(time.RFC3339, date+"T08:50:00Z")
		for range 2 {
			if err := reminders.Deliver(ctx, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	var notices int
	var finished bool
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE entity_id=$1 AND kind='calendar_reminder'`, event.ID).
		Scan(&notices); err != nil ||
		notices != 3 {
		t.Fatalf("reminders = %d, %v; want 3", notices, err)
	}
	if err := pool.QueryRow(ctx, `SELECT reminder_next_at IS NULL FROM calendar_events WHERE id=$1`, event.ID).
		Scan(&finished); err != nil ||
		!finished {
		t.Fatalf("counted reminder exhausted = %v, %v", finished, err)
	}
	input.ExpectedRevision = event.Revision
	input.RecurrenceInterval = 1
	input.RecurrenceWeekdays = []int32{3}
	updated, err := s.Update(ctx, actor, event.ID, input)
	if err != nil || updated.RecurrenceInterval != 1 || !slices.Equal(updated.RecurrenceWeekdays, []int32{3}) {
		t.Fatalf("updated rule = %+v, %v", updated, err)
	}
	items, err = s.List(ctx, actor, from, from.AddDate(0, 1, 0))
	got = nil
	for _, item := range items {
		got = append(got, item.OccurrenceDate)
	}
	want = []string{"2026-09-23", "2026-09-30", "2026-10-07"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("updated occurrences = %v, %v; want %v", got, err, want)
	}
	actor.CompanyID = f.CompanyA
	guest := auth.UserPrincipal{UserID: f.UserB, CompanyID: f.CompanyA}
	input.ExpectedRevision = updated.Revision
	input.AttendeeUserIDs = []string{f.UserB.String()}
	updated, err = s.Update(ctx, actor, event.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Respond(ctx, guest, event.ID, updated.Revision, "accepted"); err != nil {
		t.Fatal(err)
	}
	input.ExpectedRevision = updated.Revision
	input.RecurrenceInterval = 2
	updated, err = s.Update(ctx, actor, event.ID, input)
	if err != nil || len(updated.Attendees) != 1 || updated.Attendees[0].ResponseStatus != "needs_action" {
		t.Fatalf("custom repeat change must reset RSVP: %+v, %v", updated, err)
	}
}
