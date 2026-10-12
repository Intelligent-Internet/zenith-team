// Package schedule owns server-authoritative one-shot and recurring Schedule
// definitions, their creator-private lifecycle, and replay-safe firings.
package schedule

import (
	"regexp"
	"strings"
	"time"

	// tzdata embeds the IANA time zone database in the binary so a Schedule's
	// IANA time zone still resolves on hosts that ship no zoneinfo files.
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/robfig/cron/v3"

	"boardly.dev/boardly/server/pkg/protocol"
)

var cronFieldPattern = regexp.MustCompile(`^[0-9*/,-]+$`)

const (
	defaultPreviewLimit          = 5
	maximumPreviewLimit          = 20
	minimumRecurringInterval     = 5 * time.Minute
	maximumCronExpressionBytes   = 256
	maximumTimeZoneBytes         = 255
	cadenceValidationOccurrences = 512
	cadenceValidationHorizonDays = 400
	maximumDueLookback           = 12 * 366 * 24 * time.Hour
)

type TriggerType string

const (
	TriggerTypeOnce TriggerType = "once"
	TriggerTypeCron TriggerType = "cron"
)

type Trigger struct {
	Type       TriggerType `json:"type"`
	RunAt      *time.Time  `json:"runAt,omitempty"`
	Expression *string     `json:"expression,omitempty"`
	TimeZone   *string     `json:"timeZone,omitempty"`
}

type OccurrenceDetail struct {
	At                time.Time `json:"at"`
	UTCOffsetSeconds  int       `json:"utcOffsetSeconds"`
	RepeatedLocalTime bool      `json:"repeatedLocalTime"`
	OffsetChanged     bool      `json:"offsetChanged"`
	SkippedLocalTime  *string   `json:"skippedLocalTime"`
}

type TriggerPreview struct {
	OccurrenceDetails []OccurrenceDetail `json:"occurrenceDetails"`
	Trigger           Trigger            `json:"trigger"`
	Occurrences       []time.Time        `json:"occurrences"`
}

type parsedTrigger struct {
	trigger  Trigger
	schedule cron.Schedule
}

// StoredOccurrences expands an already validated trigger within a bounded
// window. Task Calendar uses the same time-zone and DST semantics as Schedules.
func StoredOccurrences(trigger Trigger, from, to time.Time, limit int) ([]time.Time, error) {
	parsed, err := parseStoredTrigger(trigger)
	if err != nil {
		return nil, err
	}
	result := []time.Time{}
	if trigger.Type == TriggerTypeOnce {
		at := trigger.RunAt.UTC()
		if !at.Before(from) && at.Before(to) {
			result = append(result, at)
		}
		return result, nil
	}
	for at := parsed.schedule.Next(from.Add(-time.Nanosecond)).UTC(); !at.IsZero() && at.Before(to) && len(result) < limit; at = parsed.schedule.Next(at).UTC() {
		result = append(result, at)
	}
	return result, nil
}

// AdvanceStoredTrigger skips a downtime backlog without iterating every missed
// occurrence. Callers retain the latest occurrence and the skipped count.
func AdvanceStoredTrigger(trigger Trigger, next, now time.Time) (time.Time, time.Time, int32, error) {
	latest, err := latestDueOccurrence(trigger, next, now)
	if err != nil {
		return time.Time{}, time.Time{}, 0, err
	}
	following, err := nextOccurrence(trigger, latest)
	return latest, following, missedOccurrences(trigger, next, latest), err
}

func PreviewTrigger(trigger Trigger, after time.Time, limit int) (TriggerPreview, error) {
	if after.IsZero() {
		return TriggerPreview{}, ErrInvalidInput
	}
	if limit == 0 {
		limit = defaultPreviewLimit
	}
	if limit < 1 || limit > maximumPreviewLimit {
		return TriggerPreview{}, ErrInvalidInput
	}
	parsed, err := parseTrigger(trigger, after)
	if err != nil {
		return TriggerPreview{}, err
	}
	occurrences, err := triggerOccurrences(parsed, after, limit)
	if err != nil {
		return TriggerPreview{}, err
	}
	return TriggerPreview{
		Trigger:           parsed.trigger,
		Occurrences:       occurrences,
		OccurrenceDetails: occurrenceDetails(parsed.trigger, after, occurrences),
	}, nil
}

func parseTrigger(trigger Trigger, after time.Time) (parsedTrigger, error) {
	switch trigger.Type {
	case TriggerTypeOnce:
		return parseOneShotTrigger(trigger, after)
	case TriggerTypeCron:
		return parseCronTrigger(trigger, after)
	default:
		return parsedTrigger{}, ErrInvalidInput
	}
}

func parseStoredTrigger(trigger Trigger) (parsedTrigger, error) {
	switch trigger.Type {
	case TriggerTypeOnce:
		if trigger.RunAt == nil {
			return parsedTrigger{}, ErrInvalidStoredState
		}
		return parseOneShotTrigger(trigger, trigger.RunAt.Add(-time.Minute))
	case TriggerTypeCron:
		return parseCronExpression(trigger)
	default:
		return parsedTrigger{}, ErrInvalidStoredState
	}
}

func parseOneShotTrigger(trigger Trigger, after time.Time) (parsedTrigger, error) {
	if trigger.RunAt == nil || trigger.Expression != nil || trigger.TimeZone != nil {
		return parsedTrigger{}, ErrInvalidInput
	}
	runAt := trigger.RunAt.UTC()
	if !runAt.After(after) || runAt.Second() != 0 || runAt.Nanosecond() != 0 {
		return parsedTrigger{}, ErrInvalidInput
	}
	return parsedTrigger{trigger: Trigger{Type: TriggerTypeOnce, RunAt: &runAt}}, nil
}

func parseCronTrigger(trigger Trigger, after time.Time) (parsedTrigger, error) {
	parsed, err := parseCronExpression(trigger)
	if err != nil {
		return parsedTrigger{}, err
	}
	if err := validateCronCadence(parsed.schedule, after); err != nil {
		return parsedTrigger{}, err
	}
	return parsed, nil
}

func parseCronExpression(trigger Trigger) (parsedTrigger, error) {
	if trigger.RunAt != nil || trigger.Expression == nil || trigger.TimeZone == nil {
		return parsedTrigger{}, ErrInvalidInput
	}
	expression := protocol.TrimContractWhitespace(*trigger.Expression)
	timeZone := protocol.TrimContractWhitespace(*trigger.TimeZone)
	validText := utf8.ValidString(expression) && utf8.ValidString(timeZone)
	fields := strings.Fields(expression)
	exactFields := len(fields) == 5
	containsEmbeddedZone := strings.Contains(expression, "TZ=")
	if !validText || expression == "" || timeZone == "" || !exactFields || containsEmbeddedZone ||
		len(expression) > maximumCronExpressionBytes || len(timeZone) > maximumTimeZoneBytes {
		return parsedTrigger{}, ErrInvalidInput
	}
	for _, field := range fields {
		if !cronFieldPattern.MatchString(field) {
			return parsedTrigger{}, ErrInvalidInput
		}
	}
	if timeZone == "Local" {
		return parsedTrigger{}, ErrInvalidInput
	}
	if _, err := time.LoadLocation(timeZone); err != nil {
		return parsedTrigger{}, ErrInvalidInput
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse("CRON_TZ=" + timeZone + " " + expression)
	if err != nil {
		return parsedTrigger{}, ErrInvalidInput
	}
	return parsedTrigger{
		trigger: Trigger{
			Type: TriggerTypeCron, Expression: &expression, TimeZone: &timeZone,
		},
		schedule: schedule,
	}, nil
}

func validateCronCadence(schedule cron.Schedule, after time.Time) error {
	first := schedule.Next(after)
	if first.IsZero() {
		return ErrInvalidInput
	}
	previous := first
	horizon := after.Add(cadenceValidationHorizonDays * 24 * time.Hour)
	for range cadenceValidationOccurrences {
		next := schedule.Next(previous)
		if next.IsZero() {
			return ErrInvalidInput
		}
		if next.Sub(previous) < minimumRecurringInterval {
			return ErrInvalidInput
		}
		if next.After(horizon) {
			return nil
		}
		previous = next
	}
	return nil
}

func triggerOccurrences(parsed parsedTrigger, after time.Time, limit int) ([]time.Time, error) {
	if parsed.trigger.Type == TriggerTypeOnce {
		return []time.Time{parsed.trigger.RunAt.UTC()}, nil
	}
	occurrences := make([]time.Time, 0, limit)
	current := after
	for range limit {
		next := parsed.schedule.Next(current)
		if next.IsZero() || !next.After(current) {
			return nil, ErrInvalidInput
		}
		occurrences = append(occurrences, next.UTC())
		current = next
	}
	return occurrences, nil
}

func nextOccurrence(trigger Trigger, after time.Time) (time.Time, error) {
	parsed, err := parseStoredTrigger(trigger)
	if err != nil {
		return time.Time{}, err
	}
	if parsed.trigger.Type == TriggerTypeOnce {
		return parsed.trigger.RunAt.UTC(), nil
	}
	next := parsed.schedule.Next(after).UTC()
	if next.IsZero() {
		return time.Time{}, ErrInvalidStoredState
	}
	return next, nil
}

// latestDueOccurrence collapses any downtime backlog to the closest missed
// occurrence. The binary search asks cron.Next at logarithmically narrowing
// timestamps, avoiding one iteration per missed five-minute firing while the
// Schedule row is locked in PostgreSQL.
func latestDueOccurrence(trigger Trigger, storedNext, serverTime time.Time) (time.Time, error) {
	storedNext = storedNext.UTC()
	serverTime = serverTime.UTC()
	parsed, err := parseStoredTrigger(trigger)
	if err != nil {
		return time.Time{}, ErrInvalidStoredState
	}
	if parsed.trigger.Type == TriggerTypeOnce {
		return storedNext, nil
	}
	if previous := parsed.schedule.Next(storedNext.Add(-time.Minute)); !previous.Equal(storedNext) {
		return storedNext, nil
	}

	window := minimumRecurringInterval
	var lower time.Time
	for window <= maximumDueLookback {
		candidate := serverTime.Add(-window)
		if next := parsed.schedule.Next(candidate); !next.After(serverTime) {
			lower = candidate
			break
		}
		window *= 2
	}
	if lower.IsZero() {
		return time.Time{}, ErrInvalidStoredState
	}
	upper := serverTime
	for upper.Sub(lower) > time.Minute {
		middle := lower.Add(upper.Sub(lower) / 2)
		if next := parsed.schedule.Next(middle); !next.After(serverTime) {
			lower = middle
		} else {
			upper = middle
		}
	}
	latest := parsed.schedule.Next(lower).UTC()
	if latest.After(serverTime) || latest.Before(storedNext) {
		return time.Time{}, ErrInvalidStoredState
	}
	return latest, nil
}

// missedOccurrences is bounded even after years offline. A saturated value is
// presented as "1000+"; finding the latest due time remains logarithmic.
func missedOccurrences(trigger Trigger, storedNext, firedAt time.Time) int32 {
	if trigger.Type == TriggerTypeOnce || !storedNext.Before(firedAt) {
		return 0
	}
	parsed, err := parseStoredTrigger(trigger)
	if err != nil {
		return 0
	}
	var count int32
	for at := storedNext; at.Before(firedAt) && count < 1000; at = parsed.schedule.Next(at) {
		if at.IsZero() {
			break
		}
		count++
	}
	return count
}

// occurrenceDetails uses the location's actual transition bounds, including
// midnight and non-hour changes. Cron runs in UTC here only to match wall-clock
// fields inside a gap; it never changes the scheduling algorithm.
func occurrenceDetails(trigger Trigger, after time.Time, occurrences []time.Time) []OccurrenceDetail {
	result := make([]OccurrenceDetail, 0, len(occurrences))
	location := time.UTC
	var wallCron cron.Schedule
	if trigger.TimeZone != nil {
		location, _ = time.LoadLocation(*trigger.TimeZone)
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		wallCron, _ = parser.Parse("CRON_TZ=UTC " + *trigger.Expression)
	}
	previous := after
	for _, at := range occurrences {
		local := at.In(location)
		_, offset := local.Zone()
		_, previousOffset := previous.In(location).Zone()
		detail := OccurrenceDetail{At: at, UTCOffsetSeconds: offset, OffsetChanged: offset != previousOffset}
		start, end := local.ZoneBounds()
		if !start.IsZero() {
			_, before := start.Add(-time.Second).In(location).Zone()
			detail.RepeatedLocalTime = before > offset &&
				at.Before(start.Add(time.Duration(before-offset)*time.Second))
		}
		if !end.IsZero() {
			_, next := end.In(location).Zone()
			detail.RepeatedLocalTime = detail.RepeatedLocalTime ||
				next < offset && !at.Before(end.Add(-time.Duration(offset-next)*time.Second))
		}
		for cursor := previous; wallCron != nil; {
			_, boundary := cursor.In(location).ZoneBounds()
			if boundary.IsZero() || !boundary.After(cursor) || boundary.After(at) {
				break
			}
			_, before := boundary.Add(-time.Second).In(location).Zone()
			_, next := boundary.In(location).Zone()
			if next > before {
				gapStart := boundary.UTC().Add(time.Duration(before) * time.Second)
				gapEnd := boundary.UTC().Add(time.Duration(next) * time.Second)
				missing := wallCron.Next(gapStart.Add(-time.Nanosecond))
				if !missing.IsZero() && missing.Before(gapEnd) {
					value := missing.Format("2006-01-02 15:04")
					detail.SkippedLocalTime = &value
				}
			}
			cursor = boundary
		}
		result = append(result, detail)
		previous = at
	}
	return result
}
