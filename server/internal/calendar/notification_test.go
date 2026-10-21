package calendar

import (
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
)

func newCalendarTestService(t *testing.T, store *db.Store) *DefaultCalendarService {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return NewCalendarService(store, bus, nil)
}

func TestCustomRecurrenceChangesNotifyGuests(t *testing.T) {
	t.Parallel()
	before := Event{
		Recurrence:            "weekly",
		RecurrenceInterval:    1,
		RecurrenceWeekdays:    []int32{1},
		RecurrenceMonthlyMode: "day_of_month",
	}
	for _, tt := range []struct {
		name   string
		change func(*Event)
	}{
		{"interval", func(e *Event) { e.RecurrenceInterval = 2 }},
		{"weekdays", func(e *Event) { e.RecurrenceWeekdays = []int32{3} }},
		{"monthly mode", func(e *Event) { e.RecurrenceMonthlyMode = "day_of_week" }},
		{"count", func(e *Event) { n := int32(5); e.RecurrenceCount = &n }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			after := before
			tt.change(&after)
			got := changedDetails(before, after)
			if len(got) != 1 || got[0] != "schedule" {
				t.Fatalf("changes = %v, want [schedule]", got)
			}
		})
	}
}
