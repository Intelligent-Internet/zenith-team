package calendar

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestExpandPreservesWallClockAcrossDST(t *testing.T) {
	t.Parallel()
	event := Event{ID: "test", Type: "event", Title: "Morning", StartLocal: "2026-03-07T09:00", EndLocal: "2026-03-07T10:00", TimeZone: "America/New_York", Recurrence: "daily"}
	items, err := expand(event, time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC))
	if err != nil || len(items) != 3 {
		t.Fatalf("expand() = %v, %v", items, err)
	}
	if items[0].StartAt.Hour() != 14 || items[1].StartAt.Hour() != 13 {
		t.Fatalf("UTC hours = %v, %v", items[0].StartAt, items[1].StartAt)
	}
}

func TestMonthlyRecurrenceSkipsMissingDates(t *testing.T) {
	t.Parallel()
	event := Event{ID: "test", Type: "event", Title: "Review", StartLocal: "2026-01-31T09:00", EndLocal: "2026-01-31T10:00", TimeZone: "UTC", Recurrence: "monthly"}
	items, err := expand(event, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(items) != 1 || items[0].OccurrenceDate != "2026-03-31" {
		t.Fatalf("expand() = %v, %v", items, err)
	}
}

func TestCalendarInputRejectsInvalidTimes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, start, end, zone string
		allDay                 bool
	}{
		{"DST gap", "2026-03-08T02:30", "2026-03-08T03:30", "America/New_York", false},
		{"end before start", "2026-09-22T12:00", "2026-09-22T11:00", "UTC", false},
		{"bad zone", "2026-09-22T12:00", "2026-09-22T13:00", "Mars/Olympus", false},
		{"empty all day", "2026-09-22", "2026-09-22", "UTC", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := normalize(Input{Type: "event", Title: "Test", StartLocal: tc.start, EndLocal: tc.end, TimeZone: tc.zone, AllDay: tc.allDay, Recurrence: "none", Color: "sage"})
			if err == nil {
				t.Fatal("normalize() accepted invalid time")
			}
		})
	}
}

func TestAllDayKeepsExclusiveDates(t *testing.T) {
	t.Parallel()
	event := Event{ID: "test", Type: "event", Title: "Away", AllDay: true, StartLocal: "2026-09-24", EndLocal: "2026-09-26", TimeZone: "Asia/Ho_Chi_Minh", Recurrence: "none"}
	items, err := expand(event, time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC))
	if err != nil || len(items) != 1 || items[0].EndLocal != "2026-09-26" {
		t.Fatalf("expand() = %v, %v", items, err)
	}
}

func TestCustomRecurrenceExpansion(t *testing.T) {
	t.Parallel()
	count := int32(3)
	until := "2026-09-28"
	for _, tt := range []struct {
		name, start, end, frequency, zone, from, to string
		interval                                    int32
		weekdays                                    []int32
		monthly                                     string
		count                                       *int32
		until                                       *string
		want                                        []string
	}{
		{name: "every two days", start: "2026-09-23T09:00", end: "2026-09-23T10:00", frequency: "daily", interval: 2, from: "2026-09-23", to: "2026-09-29", want: []string{"2026-09-23", "2026-09-25", "2026-09-27"}},
		{name: "interval across supported date range", start: "1900-01-01T09:00", end: "1900-01-01T10:00", frequency: "daily", interval: 2, from: "2200-01-01", to: "2200-01-04", want: []string{"2200-01-02"}},
		{name: "alternate weeks with multiple days", start: "2026-09-23T09:00", end: "2026-09-23T10:00", frequency: "weekly", interval: 2, weekdays: []int32{1, 3, 5}, from: "2026-09-21", to: "2026-10-10", want: []string{"2026-09-23", "2026-09-25", "2026-10-05", "2026-10-07", "2026-10-09"}},
		{name: "unselected start weekday", start: "2026-09-23T09:00", end: "2026-09-23T10:00", frequency: "weekly", weekdays: []int32{1, 5}, from: "2026-09-23", to: "2026-09-30", want: []string{"2026-09-25", "2026-09-28"}},
		{name: "monthly fourth Wednesday", start: "2026-09-23T09:00", end: "2026-09-23T10:00", frequency: "monthly", monthly: "day_of_week", from: "2026-09-01", to: "2026-12-01", want: []string{"2026-09-23", "2026-10-28", "2026-11-25"}},
		{name: "monthly last Monday", start: "2026-08-31T09:00", end: "2026-08-31T10:00", frequency: "monthly", monthly: "day_of_week", from: "2026-08-01", to: "2026-11-01", want: []string{"2026-08-31", "2026-09-28", "2026-10-26"}},
		{name: "missing month dates do not consume count", start: "2026-01-31T09:00", end: "2026-01-31T10:00", frequency: "monthly", count: &count, from: "2026-01-01", to: "2027-01-01", want: []string{"2026-01-31", "2026-03-31", "2026-05-31"}},
		{name: "count applies to series not visible window", start: "2026-09-23T09:00", end: "2026-09-23T10:00", frequency: "daily", count: &count, from: "2026-09-25", to: "2026-10-01", want: []string{"2026-09-25"}},
		{name: "inclusive end date", start: "2026-09-23T09:00", end: "2026-09-23T10:00", frequency: "weekly", weekdays: []int32{1, 3, 5}, until: &until, from: "2026-09-01", to: "2026-10-01", want: []string{"2026-09-23", "2026-09-25", "2026-09-28"}},
		{name: "leap year interval", start: "2024-02-29T09:00", end: "2024-02-29T10:00", frequency: "yearly", interval: 2, count: &count, from: "2024-01-01", to: "2035-01-01", want: []string{"2024-02-29", "2028-02-29", "2032-02-29"}},
		{name: "DST gap does not consume count", start: "2026-03-07T02:30", end: "2026-03-07T03:30", frequency: "daily", zone: "America/New_York", count: &count, from: "2026-03-07", to: "2026-03-15", want: []string{"2026-03-07", "2026-03-09", "2026-03-10"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			zone := tt.zone
			if zone == "" {
				zone = "UTC"
			}
			from, _ := time.Parse(time.DateOnly, tt.from)
			to, _ := time.Parse(time.DateOnly, tt.to)
			event := Event{StartLocal: tt.start, EndLocal: tt.end, TimeZone: zone, Recurrence: tt.frequency, RecurrenceInterval: tt.interval, RecurrenceWeekdays: tt.weekdays, RecurrenceMonthlyMode: tt.monthly, RecurrenceCount: tt.count, RecurrenceUntil: tt.until}
			items, err := expand(event, from, to)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, item := range items {
				got = append(got, item.OccurrenceDate)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("dates = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeCustomRecurrence(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		change func(*Input)
	}{
		{"negative interval", func(i *Input) { i.RecurrenceInterval = -1 }},
		{"excessive interval", func(i *Input) { i.RecurrenceInterval = 1000 }},
		{"invalid weekday", func(i *Input) { i.RecurrenceWeekdays = []int32{7} }},
		{"duplicate weekdays", func(i *Input) { i.RecurrenceWeekdays = []int32{1, 1} }},
		{"weekdays on daily rule", func(i *Input) { i.Recurrence = "daily"; i.RecurrenceWeekdays = []int32{1} }},
		{"invalid monthly mode", func(i *Input) { i.RecurrenceMonthlyMode = "unknown" }},
		{"monthly mode on weekly rule", func(i *Input) { i.RecurrenceMonthlyMode = "day_of_week" }},
		{"zero count", func(i *Input) { n := int32(0); i.RecurrenceCount = &n }},
		{"excessive count", func(i *Input) { n := int32(1001); i.RecurrenceCount = &n }},
		{"both end modes", func(i *Input) {
			n := int32(3)
			until := "2026-10-01"
			i.RecurrenceCount = &n
			i.RecurrenceUntil = &until
		}},
		{"custom nonrepeating", func(i *Input) { i.Recurrence = "none"; i.RecurrenceInterval = 2 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := Input{Type: "event", Title: "Weekly meeting", Color: "sage", StartLocal: "2026-09-23T09:00", EndLocal: "2026-09-23T10:00", TimeZone: "UTC", Recurrence: "weekly"}
			tt.change(&input)
			if _, err := normalize(input); !errors.Is(err, ErrInvalid) {
				t.Fatalf("normalize error = %v, want ErrInvalid", err)
			}
		})
	}
}
