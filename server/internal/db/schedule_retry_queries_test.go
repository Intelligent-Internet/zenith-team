package db

import (
	"strings"
	"testing"
)

func TestScheduleRetryAndClaimKeepFiringAuthority(t *testing.T) {
	source := queryFileSource(t, "runs.sql")
	for _, name := range []string{"CreateRetryRun", "SelectClaimableRun", "ListQueuedClaimCandidatesByRuntimes", "ReclaimStaleDispatchedRunsForRuntimes"} {
		query := normalizedQuery(t, source, name)
		for _, fragment := range []string{"schedule_firing_id", "disposition = 'materialized'", "interval '7 days'"} {
			if !strings.Contains(query, fragment) {
				t.Errorf("%s lacks %s", name, fragment)
			}
		}
	}
	for _, name := range []string{"ListQueuedRunExpiryCandidates", "ExpireStaleQueuedRuns"} {
		query := normalizedQuery(t, source, name)
		for _, fragment := range []string{"schedule_firing_id is null", "firing.created_at", "once_wait_secs"} {
			if !strings.Contains(query, fragment) {
				t.Errorf("%s lacks %s", name, fragment)
			}
		}
	}
}
