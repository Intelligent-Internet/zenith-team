package db

import (
	"strings"
	"testing"
)

func TestDueScheduleDiscoveryPrecedesRestrictedScheduleLock(t *testing.T) {
	t.Parallel()

	source := queryFileSource(t, "schedules.sql")
	targets := normalizedQuery(t, source, "ListDueScheduleLockTargets")
	for _, fragment := range []string{
		"status = 'active'",
		"next_occurrence_at <= statement_timestamp() - make_interval",
		"order by next_occurrence_at, id",
		"limit sqlc.arg(page_size)::integer",
	} {
		if !strings.Contains(targets, fragment) {
			t.Errorf("due Schedule authority discovery lacks %q: %s", fragment, targets)
		}
	}
	if strings.Contains(targets, "for update") {
		t.Fatalf("due Schedule discovery locks Schedule before producer authority: %s", targets)
	}

	locked := normalizedQuery(t, source, "LockNextDueSchedule")
	for _, fragment := range []string{
		"schedules.id = sqlc.arg(schedule_id)::uuid",
		"for update of schedules skip locked",
	} {
		if !strings.Contains(locked, fragment) {
			t.Errorf("due Schedule lock lacks %q: %s", fragment, locked)
		}
	}
}
