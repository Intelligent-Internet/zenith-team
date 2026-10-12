package calendar

import (
	"testing"
	"time"
)

func TestNextReminderTime(t *testing.T) {
	lead := int32(10)
	for _, tt := range []struct {
		name, start, end, zone, repeat, after, want string
	}{
		{"one shot elapsed", "2026-09-22T09:00", "2026-09-22T10:00", "Asia/Ho_Chi_Minh", "none", "2026-09-22T01:50:00Z", ""},
		{"daily local time", "2026-09-22T09:00", "2026-09-22T10:00", "Asia/Ho_Chi_Minh", "daily", "2026-09-22T01:50:00Z", "2026-09-23T01:50:00Z"},
		{"missing monthly day", "2026-01-31T09:00", "2026-01-31T10:00", "UTC", "monthly", "2026-01-31T08:50:00Z", "2026-03-31T08:50:00Z"},
		{"DST gap", "2026-03-07T02:30", "2026-03-07T03:30", "America/New_York", "daily", "2026-03-07T07:20:00Z", "2026-03-09T06:20:00Z"},
		{"century leap year gap", "2096-02-29T09:00", "2096-02-29T10:00", "UTC", "yearly", "2096-02-29T08:50:00Z", "2104-02-29T08:50:00Z"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			after, err := time.Parse(time.RFC3339, tt.after)
			if err != nil {
				t.Fatal(err)
			}
			got, err := nextReminderTime(Event{StartLocal: tt.start, EndLocal: tt.end, TimeZone: tt.zone, Recurrence: tt.repeat, ReminderMinutes: &lead}, after)
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == "" {
				if got.Valid {
					t.Fatalf("next = %s", got.Time)
				}
				return
			}
			if !got.Valid || got.Time.Format(time.RFC3339) != tt.want {
				t.Fatalf("next = %v; want %s", got, tt.want)
			}
		})
	}
}
