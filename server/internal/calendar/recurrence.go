package calendar

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/jackc/pgx/v5/pgtype"
)

func normalize(input Input) (generated.CreateCalendarEventParams, error) {
	var result generated.CreateCalendarEventParams
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" || utf8.RuneCountInString(input.Title) > 240 || len(input.Description) > 16000 || len(input.Location) > 1000 {
		return result, ErrInvalid
	}
	if input.Type != "event" && input.Type != "reminder" {
		return result, ErrInvalid
	}
	switch input.Color {
	case "sage", "sky", "lavender", "rose", "sand":
	default:
		return result, ErrInvalid
	}
	switch input.Recurrence {
	case "none", "daily", "weekly", "monthly", "yearly":
	default:
		return result, ErrInvalid
	}
	if input.TimeZone == "" || input.TimeZone == "Local" {
		return result, ErrInvalid
	}
	zone, err := time.LoadLocation(input.TimeZone)
	if err != nil {
		return result, ErrInvalid
	}
	layout := "2006-01-02T15:04"
	if input.AllDay {
		layout = time.DateOnly
	}
	start, err := time.Parse(layout, input.StartLocal)
	if err != nil {
		return result, ErrInvalid
	}
	end, err := time.Parse(layout, input.EndLocal)
	if err != nil || !end.After(start) || end.Sub(start) > 366*24*time.Hour || start.Year() < 1900 || end.Year() > 2200 {
		return result, ErrInvalid
	}
	// ParseInLocation normalizes nonexistent DST times; reject that normalization.
	for _, value := range []string{input.StartLocal, input.EndLocal} {
		instant, parseErr := time.ParseInLocation(layout, value, zone)
		if parseErr != nil || instant.Format(layout) != value {
			return result, ErrInvalid
		}
	}
	result = generated.CreateCalendarEventParams{Type: input.Type, Title: input.Title, Description: strings.TrimSpace(input.Description), Location: strings.TrimSpace(input.Location), Color: input.Color, AllDay: input.AllDay, StartLocal: pgtype.Timestamp{Time: start, Valid: true}, EndLocal: pgtype.Timestamp{Time: end, Valid: true}, TimeZone: input.TimeZone, Recurrence: input.Recurrence}
	if input.RecurrenceInterval == 0 {
		input.RecurrenceInterval = 1
	}
	if input.RecurrenceMonthlyMode == "" {
		input.RecurrenceMonthlyMode = "day_of_month"
	}
	if input.RecurrenceInterval < 1 || input.RecurrenceInterval > 999 || len(input.RecurrenceWeekdays) > 7 {
		return result, ErrInvalid
	}
	weekdays := append([]int32{}, input.RecurrenceWeekdays...)
	slices.Sort(weekdays)
	for i, day := range weekdays {
		if day < 0 || day > 6 || (i > 0 && weekdays[i-1] == day) {
			return result, ErrInvalid
		}
	}
	if (input.Recurrence != "weekly" && len(weekdays) > 0) ||
		(input.RecurrenceMonthlyMode != "day_of_month" && input.RecurrenceMonthlyMode != "day_of_week") ||
		(input.Recurrence != "monthly" && input.RecurrenceMonthlyMode != "day_of_month") ||
		(input.Recurrence == "none" && (input.RecurrenceInterval != 1 || input.RecurrenceCount != nil)) {
		return result, ErrInvalid
	}
	result.RecurrenceInterval = input.RecurrenceInterval
	result.RecurrenceWeekdays = weekdays
	result.RecurrenceMonthlyMode = input.RecurrenceMonthlyMode
	if input.RecurrenceCount != nil {
		if *input.RecurrenceCount < 1 || *input.RecurrenceCount > 1000 || input.RecurrenceUntil != nil {
			return result, ErrInvalid
		}
		result.RecurrenceCount = pgtype.Int4{Int32: *input.RecurrenceCount, Valid: true}
	}
	if input.RecurrenceUntil != nil {
		until, parseErr := time.Parse(time.DateOnly, *input.RecurrenceUntil)
		if parseErr != nil || until.Format(time.DateOnly) < start.Format(time.DateOnly) || until.Year() > 2200 || input.Recurrence == "none" {
			return result, ErrInvalid
		}
		result.RecurrenceUntil = pgtype.Date{Time: until, Valid: true}
	}
	if input.ReminderMinutes != nil {
		if *input.ReminderMinutes < 0 || *input.ReminderMinutes > 10080 {
			return result, ErrInvalid
		}
		result.ReminderMinutes = pgtype.Int4{Int32: *input.ReminderMinutes, Valid: true}
	} else if input.Type == "reminder" {
		result.ReminderMinutes = pgtype.Int4{Int32: 0, Valid: true}
	}
	return result, nil
}

func expand(event Event, from, to time.Time) ([]Occurrence, error) {
	result := []Occurrence{}
	err := walkOccurrences(event, from, to, func(item Occurrence) bool {
		result = append(result, item)
		return true
	})
	return result, err
}

// Both calendar reads and reminder delivery use the same wall-clock sequence.
// A counted series starts counting from its anchor, including occurrences before
// the requested window; invalid dates and DST gaps never consume an occurrence.
func walkOccurrences(event Event, from, to time.Time, visit func(Occurrence) bool) error {
	layout := "2006-01-02T15:04"
	if event.AllDay {
		layout = time.DateOnly
	}
	start, _ := time.Parse(layout, event.StartLocal)
	end, _ := time.Parse(layout, event.EndLocal)
	zone, err := time.LoadLocation(event.TimeZone)
	if err != nil {
		return err
	}
	duration := end.Sub(start)
	base := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	windowStart := from.In(zone).Add(-duration).Add(-24 * time.Hour)
	day := time.Date(windowStart.Year(), windowStart.Month(), windowStart.Day(), 0, 0, 0, 0, time.UTC)
	if day.Before(base) || event.Recurrence == "none" || event.RecurrenceCount != nil {
		day = base
	}
	windowEnd := to.In(zone).Add(48 * time.Hour)
	var count int32
	for ; !day.After(windowEnd) && day.Year() <= 2200; day = day.AddDate(0, 0, 1) {
		date := day.Format(time.DateOnly)
		if event.RecurrenceUntil != nil && date > *event.RecurrenceUntil {
			break
		}
		if !recurrenceMatches(event, base, day) {
			continue
		}
		localStart := time.Date(day.Year(), day.Month(), day.Day(), start.Hour(), start.Minute(), 0, 0, time.UTC)
		localEnd := localStart.Add(duration)
		actualStart, _ := time.ParseInLocation(layout, localStart.Format(layout), zone)
		actualEnd, _ := time.ParseInLocation(layout, localEnd.Format(layout), zone)
		if actualStart.Format(layout) != localStart.Format(layout) || actualEnd.Format(layout) != localEnd.Format(layout) {
			continue
		}
		count++
		if actualEnd.After(from) && actualStart.Before(to) {
			value := event
			value.StartLocal, value.EndLocal = localStart.Format(layout), localEnd.Format(layout)
			if !visit(Occurrence{Event: value, SeriesStartLocal: event.StartLocal, SeriesEndLocal: event.EndLocal, OccurrenceDate: date, StartAt: actualStart.UTC(), EndAt: actualEnd.UTC()}) {
				return nil
			}
		}
		if event.Recurrence == "none" || (event.RecurrenceCount != nil && count >= *event.RecurrenceCount) {
			break
		}
	}
	return nil
}

func recurrenceMatches(event Event, base, day time.Time) bool {
	interval := int(max(event.RecurrenceInterval, 1))
	days := int((day.Unix() - base.Unix()) / 86400)
	switch event.Recurrence {
	case "none":
		return day.Equal(base)
	case "daily":
		return days%interval == 0
	case "weekly":
		// Weeks start on Monday, matching the calendar grid.
		week := (days + (int(base.Weekday())+6)%7) / 7
		if week%interval != 0 {
			return false
		}
		if len(event.RecurrenceWeekdays) == 0 {
			return day.Weekday() == base.Weekday()
		}
		return slices.Contains(event.RecurrenceWeekdays, int32(day.Weekday()))
	case "monthly":
		months := (day.Year()-base.Year())*12 + int(day.Month()-base.Month())
		if months%interval != 0 {
			return false
		}
		if event.RecurrenceMonthlyMode != "day_of_week" {
			return day.Day() == base.Day()
		}
		if day.Weekday() != base.Weekday() {
			return false
		}
		ordinal := (base.Day() - 1) / 7
		if ordinal == 4 {
			return day.AddDate(0, 0, 7).Month() != day.Month()
		}
		return (day.Day()-1)/7 == ordinal
	case "yearly":
		return (day.Year()-base.Year())%interval == 0 && day.Day() == base.Day() && day.Month() == base.Month()
	default:
		return false
	}
}
