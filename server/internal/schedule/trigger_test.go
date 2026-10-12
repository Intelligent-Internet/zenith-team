package schedule

import (
	"errors"
	"testing"
	"time"
)

func TestStoredCronDoesNotRepeatReviewCadenceValidation(t *testing.T) {
	t.Parallel()
	trigger := Trigger{
		Type:       TriggerTypeCron,
		Expression: stringPointer("2,58 1,3 * * *"),
		TimeZone:   stringPointer("Europe/Berlin"),
	}
	after := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	if _, err := PreviewTrigger(trigger, after, 5); err == nil {
		t.Fatal("review must reject the upcoming four-minute DST gap")
	}
	if _, err := nextOccurrence(trigger, after); err != nil {
		t.Fatalf("previously reviewed cron must keep firing: %v", err)
	}
}

func TestStoredOccurrenceSurvivesTimeZoneRuleChange(t *testing.T) {
	t.Parallel()
	trigger := Trigger{
		Type:       TriggerTypeCron,
		Expression: stringPointer("0 9 * * *"),
		TimeZone:   stringPointer("UTC"),
	}
	stored := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	got, err := latestDueOccurrence(trigger, stored, stored.Add(2*time.Hour))
	if err != nil || !got.Equal(stored) {
		t.Fatalf("latestDueOccurrence() = %v, %v, want stored %v", got, err, stored)
	}
}

func TestPreviewTriggerAcceptsOneShotAndStandardCron(t *testing.T) {
	t.Parallel()
	after := time.Date(2026, time.August, 17, 0, 0, 0, 0, time.UTC)
	oneShot := after.Add(37 * time.Minute)

	tests := []struct {
		name      string
		trigger   Trigger
		limit     int
		expected  []time.Time
		wantCount int
	}{
		{
			name:     "one shot at any minute",
			trigger:  Trigger{Type: TriggerTypeOnce, RunAt: &oneShot},
			limit:    5,
			expected: []time.Time{oneShot},
		},
		{
			name: "daily cron in IANA timezone",
			trigger: Trigger{
				Type:       TriggerTypeCron,
				Expression: stringPointer("0 8 * * *"),
				TimeZone:   stringPointer("Asia/Ho_Chi_Minh"),
			},
			limit: 2,
			expected: []time.Time{
				time.Date(2026, time.August, 17, 1, 0, 0, 0, time.UTC),
				time.Date(2026, time.August, 18, 1, 0, 0, 0, time.UTC),
			},
		},
		{
			name: "default preview count",
			trigger: Trigger{
				Type:       TriggerTypeCron,
				Expression: stringPointer("*/5 * * * *"),
				TimeZone:   stringPointer("UTC"),
			},
			wantCount: defaultPreviewLimit,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			preview, err := PreviewTrigger(test.trigger, after, test.limit)
			if err != nil {
				t.Fatalf("PreviewTrigger() error = %v", err)
			}
			if test.wantCount != 0 && len(preview.Occurrences) != test.wantCount {
				t.Fatalf("occurrence count = %d, want %d", len(preview.Occurrences), test.wantCount)
			}
			if len(test.expected) != 0 {
				if len(preview.Occurrences) != len(test.expected) {
					t.Fatalf(
						"occurrence count = %d, want %d",
						len(preview.Occurrences),
						len(test.expected),
					)
				}
				for index, expected := range test.expected {
					if !preview.Occurrences[index].Equal(expected) {
						t.Fatalf(
							"occurrence[%d] = %s, want %s",
							index,
							preview.Occurrences[index],
							expected,
						)
					}
				}
			}
		})
	}
}

func TestPreviewTriggerRejectsInvalidContracts(t *testing.T) {
	t.Parallel()
	after := time.Date(2026, time.August, 17, 0, 0, 0, 0, time.UTC)
	past := after.Add(-time.Minute)
	withSeconds := after.Add(time.Minute + time.Second)
	future := after.Add(time.Hour)

	tests := []struct {
		name    string
		trigger Trigger
		limit   int
	}{
		{name: "unknown type", trigger: Trigger{Type: TriggerType("interval")}, limit: 5},
		{name: "past one shot", trigger: Trigger{Type: TriggerTypeOnce, RunAt: &past}, limit: 5},
		{name: "one shot with seconds", trigger: Trigger{Type: TriggerTypeOnce, RunAt: &withSeconds}, limit: 5},
		{
			name: "one shot with cron fields",
			trigger: Trigger{
				Type: TriggerTypeOnce, RunAt: &future,
				Expression: stringPointer("0 8 * * *"), TimeZone: stringPointer("UTC"),
			},
			limit: 5,
		},
		{
			name: "six field cron",
			trigger: Trigger{
				Type: TriggerTypeCron, Expression: stringPointer("0 0 8 * * *"),
				TimeZone: stringPointer("UTC"),
			},
			limit: 5,
		},
		{
			name: "descriptor cron",
			trigger: Trigger{
				Type:       TriggerTypeCron,
				Expression: stringPointer("@daily"),
				TimeZone:   stringPointer("UTC"),
			},
			limit: 5,
		},
		{
			name: "named weekday alias",
			trigger: Trigger{
				Type:       TriggerTypeCron,
				Expression: stringPointer("0 9 * * MON"),
				TimeZone:   stringPointer("UTC"),
			},
			limit: 5,
		},
		{
			name: "Quartz extension",
			trigger: Trigger{
				Type:       TriggerTypeCron,
				Expression: stringPointer("0 9 ? * 1"),
				TimeZone:   stringPointer("UTC"),
			},
			limit: 5,
		},
		{
			name: "embedded timezone",
			trigger: Trigger{
				Type: TriggerTypeCron, Expression: stringPointer("CRON_TZ=UTC 0 8 * * *"),
				TimeZone: stringPointer("UTC"),
			},
			limit: 5,
		},
		{
			name: "invalid timezone",
			trigger: Trigger{
				Type: TriggerTypeCron, Expression: stringPointer("0 8 * * *"),
				TimeZone: stringPointer("Mars/Olympus"),
			},
			limit: 5,
		},
		{
			name: "process local timezone",
			trigger: Trigger{
				Type: TriggerTypeCron, Expression: stringPointer("0 8 * * *"),
				TimeZone: stringPointer("Local"),
			},
			limit: 5,
		},
		{
			name: "faster than minimum cadence",
			trigger: Trigger{
				Type:       TriggerTypeCron,
				Expression: stringPointer("*/4 * * * *"),
				TimeZone:   stringPointer("UTC"),
			},
			limit: 5,
		},
		{
			name: "unsatisfiable cron",
			trigger: Trigger{
				Type:       TriggerTypeCron,
				Expression: stringPointer("0 0 31 2 *"),
				TimeZone:   stringPointer("UTC"),
			},
			limit: 5,
		},
		{
			name: "cron with run at",
			trigger: Trigger{
				Type: TriggerTypeCron, RunAt: &future,
				Expression: stringPointer("0 8 * * *"), TimeZone: stringPointer("UTC"),
			},
			limit: 5,
		},
		{
			name: "preview limit too large",
			trigger: Trigger{
				Type:       TriggerTypeCron,
				Expression: stringPointer("0 8 * * *"),
				TimeZone:   stringPointer("UTC"),
			},
			limit: maximumPreviewLimit + 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := PreviewTrigger(test.trigger, after, test.limit); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("PreviewTrigger() error = %v, want %v", err, ErrInvalidInput)
			}
		})
	}
}

func TestLatestDueOccurrenceChoosesTheMostRecentMissedCron(t *testing.T) {
	t.Parallel()

	expression := "0 8 * * *"
	timeZone := "Asia/Ho_Chi_Minh"
	trigger := Trigger{
		Type: TriggerTypeCron, Expression: &expression, TimeZone: &timeZone,
	}
	storedNext := time.Date(2026, time.August, 1, 1, 0, 0, 0, time.UTC)
	serverTime := time.Date(2026, time.August, 17, 5, 30, 0, 0, time.UTC)

	occurrence, err := latestDueOccurrence(trigger, storedNext, serverTime)
	if err != nil {
		t.Fatalf("latestDueOccurrence() error = %v", err)
	}
	want := time.Date(2026, time.August, 17, 1, 0, 0, 0, time.UTC)
	if !occurrence.Equal(want) {
		t.Fatalf("latestDueOccurrence() = %s, want %s", occurrence, want)
	}
}

func TestPreviewTriggerPinsDaylightSavingTransitions(t *testing.T) {
	t.Parallel()
	expression := "30 2 * * *"
	timeZone := "America/New_York"
	spring, err := PreviewTrigger(
		Trigger{Type: TriggerTypeCron, Expression: &expression, TimeZone: &timeZone},
		time.Date(2026, time.March, 8, 0, 0, 0, 0, time.UTC),
		1,
	)
	if err != nil {
		t.Fatalf("PreviewTrigger(spring gap) error = %v", err)
	}
	wantSpring := time.Date(2026, time.March, 9, 6, 30, 0, 0, time.UTC)
	if len(spring.Occurrences) != 1 || !spring.Occurrences[0].Equal(wantSpring) {
		t.Fatalf("spring occurrences = %v, want [%s]", spring.Occurrences, wantSpring)
	}

	expression = "30 1 * * *"
	fall, err := PreviewTrigger(
		Trigger{Type: TriggerTypeCron, Expression: &expression, TimeZone: &timeZone},
		time.Date(2026, time.November, 1, 4, 0, 0, 0, time.UTC),
		2,
	)
	if err != nil {
		t.Fatalf("PreviewTrigger(fall fold) error = %v", err)
	}
	wantFall := []time.Time{
		time.Date(2026, time.November, 1, 5, 30, 0, 0, time.UTC),
		time.Date(2026, time.November, 1, 6, 30, 0, 0, time.UTC),
	}
	if len(fall.Occurrences) != len(wantFall) {
		t.Fatalf("fall occurrence count = %d, want %d", len(fall.Occurrences), len(wantFall))
	}
	for index, expected := range wantFall {
		if !fall.Occurrences[index].Equal(expected) {
			t.Fatalf("fall occurrence[%d] = %s, want %s", index, fall.Occurrences[index], expected)
		}
	}
}

func TestPreviewTriggerUsesPOSIXDayOfMonthOrDayOfWeekSemantics(t *testing.T) {
	t.Parallel()

	expression := "0 9 13 * 1"
	timeZone := "UTC"
	preview, err := PreviewTrigger(
		Trigger{Type: TriggerTypeCron, Expression: &expression, TimeZone: &timeZone},
		time.Date(2026, time.August, 9, 0, 0, 0, 0, time.UTC),
		2,
	)
	if err != nil {
		t.Fatalf("PreviewTrigger(DOM/DOW) error = %v", err)
	}
	want := []time.Time{
		time.Date(2026, time.August, 10, 9, 0, 0, 0, time.UTC),
		time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC),
	}
	for index := range want {
		if !preview.Occurrences[index].Equal(want[index]) {
			t.Fatalf("DOM/DOW occurrence[%d] = %s, want %s", index, preview.Occurrences[index], want[index])
		}
	}
}

func stringPointer(value string) *string {
	return &value
}

func TestPreviewReportsDSTAtTheActualLocalTimes(t *testing.T) {
	for _, test := range []struct {
		name, expression, zone, after string
		repeated, skipped             bool
	}{
		{"New York repeat", "30 1 * * *", "America/New_York", "2026-11-01T00:00:00Z", true, false},
		{"Berlin repeat", "30 2 * * *", "Europe/Berlin", "2026-10-25T00:00:00Z", true, false},
		{"New York gap", "30 2 * * *", "America/New_York", "2026-03-08T00:00:00Z", false, true},
		{"Cairo midnight gap", "0 0 * * *", "Africa/Cairo", "2026-04-23T21:00:00Z", false, true},
		{"Santiago midnight gap", "0 0 * * *", "America/Santiago", "2026-09-06T02:00:00Z", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			after, err := time.Parse(time.RFC3339, test.after)
			if err != nil {
				t.Fatal(err)
			}
			preview, err := PreviewTrigger(
				Trigger{Type: TriggerTypeCron, Expression: &test.expression, TimeZone: &test.zone},
				after,
				1,
			)
			if err != nil {
				t.Fatal(err)
			}
			detail := preview.OccurrenceDetails[0]
			if detail.RepeatedLocalTime != test.repeated ||
				(detail.SkippedLocalTime != nil) != test.skipped {
				t.Fatalf("DST metadata: %+v", detail)
			}
		})
	}
}
