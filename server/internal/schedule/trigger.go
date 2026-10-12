// Package schedule owns server-authoritative one-shot and recurring Schedule
// definitions, their creator-private lifecycle, and replay-safe firings.
package schedule

import (
	"errors"
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

type TriggerPreview struct {
	Trigger     Trigger     `json:"trigger"`
	Occurrences []time.Time `json:"occurrences"`
}

type parsedTrigger struct {
	trigger  Trigger
	schedule cron.Schedule
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
	return TriggerPreview{Trigger: parsed.trigger, Occurrences: occurrences}, nil
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
		return parseCronTrigger(trigger, time.Now().UTC())
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
	if err := validateCronCadence(schedule, after); err != nil {
		return parsedTrigger{}, err
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
	preview, err := PreviewTrigger(trigger, after, 1)
	if err != nil {
		return time.Time{}, err
	}
	if len(preview.Occurrences) != 1 {
		return time.Time{}, errors.New("Schedule trigger returned no next occurrence")
	}
	return preview.Occurrences[0], nil
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
		return time.Time{}, ErrInvalidStoredState
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
