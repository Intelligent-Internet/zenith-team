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
		"schedules.status = 'active'",
		"schedules.next_occurrence_at <= statement_clock.now_at",
		"order by schedules.next_occurrence_at, schedules.id",
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
		"schedules.id = any(sqlc.arg(schedule_ids)::uuid[])",
		"for update of schedules skip locked",
	} {
		if !strings.Contains(locked, fragment) {
			t.Errorf("due Schedule lock lacks %q: %s", fragment, locked)
		}
	}
}
