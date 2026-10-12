package db

import (
	"strings"
	"testing"
)

func TestScheduleCancellationQueryFencesTheOwningSchedule(t *testing.T) {
	t.Parallel()

	query := normalizedQuery(t, queryFileSource(t, "runs.sql"), "CancelQueuedScheduleRun")
	for _, fragment := range []string{
		"from schedule_firings",
		"runs.schedule_firing_id = sqlc.arg(firing_id)::uuid",
		"schedule_firings.id = runs.schedule_firing_id",
		"schedule_firings.schedule_id = sqlc.arg(schedule_id)::uuid",
		"schedule_firings.company_id = runs.company_id",
		"schedule_firings.conversation_id = runs.conversation_id",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("CancelQueuedScheduleRun does not contain %q: %s", fragment, query)
		}
	}
}

func TestScheduleCancellationQueryOnlyCancelsWaitingRuns(t *testing.T) {
	t.Parallel()

	query := normalizedQuery(t, queryFileSource(t, "runs.sql"), "CancelQueuedScheduleRun")
	if !strings.Contains(query, "runs.status in ('queued', 'deferred')") {
		t.Fatalf("CancelQueuedScheduleRun does not require waiting status: %s", query)
	}
}
