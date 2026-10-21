package handler

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
)

func TestTaskResultCursorKeepsExhaustedSectionsClosed(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name               string
		page               execution.TaskResultPage
		wantResultsSkipped bool
		wantSourcesSkipped bool
	}{
		{name: "only older outputs", page: execution.TaskResultPage{Next: &execution.Cursor{CreatedAt: at, ID: uuid.MustParse("00000000-0000-4000-8000-000000000041")}}, wantSourcesSkipped: true},
		{name: "only older sources", page: execution.TaskResultPage{NextSource: &execution.TaskResultSourceCursor{CreatedAt: at, ID: uuid.New()}}, wantResultsSkipped: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cursor := encodeTaskResultCursor(test.page)
			if cursor == nil {
				t.Fatal("continuation cursor is missing")
			}
			options, err := parseTaskResultOptions("limit=7&cursor=" + *cursor)
			if err != nil || options.Limit != 7 || options.SkipResults != test.wantResultsSkipped ||
				options.SkipSources != test.wantSourcesSkipped {
				t.Fatalf("parsed continuation = %+v, %v", options, err)
			}
		})
	}
	if got := encodeTaskResultCursor(execution.TaskResultPage{}); got != nil {
		t.Fatalf("exhausted continuation = %v, want nil", got)
	}
}

func TestTaskResultQueryRejectsAmbiguousPagination(t *testing.T) {
	t.Parallel()
	for _, query := range []string{"limit=0", "limit=51", "limit=01", "limit=1&limit=2", "unknown=value"} {
		t.Run(query, func(t *testing.T) {
			t.Parallel()
			if _, err := parseTaskResultOptions(
				query,
			); !errors.Is(
				err,
				execution.ErrDeliverableInvalidInput,
			) {
				t.Fatalf("parse %q error = %v, want invalid input", query, err)
			}
		})
	}
	if _, err := parseTaskResultOptions("cursor=invalid"); !errors.Is(err, errInvalidTaskResultCursor) {
		t.Fatalf("malformed cursor error = %v, want invalid cursor", err)
	}
}

func TestArtifactQueryUsesOneBoundedFeedCursor(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.September, 17, 9, 30, 0, 0, time.UTC)
	id := uuid.MustParse("00000000-0000-4000-8000-000000000041")
	cursor := encodeFeedCursor(at, id)
	options, err := parseArtifactListOptions("limit=7&cursor=" + cursor)
	if err != nil || options.Limit != 7 || options.Before == nil ||
		!options.Before.CreatedAt.Equal(at) || options.Before.ID != id {
		t.Fatalf("parseArtifactListOptions() = %+v, %v", options, err)
	}
	if _, err := parseArtifactListOptions("cursor=invalid"); !errors.Is(err, errInvalidArtifactCursor) {
		t.Fatalf("invalid cursor error = %v, want errInvalidArtifactCursor", err)
	}
}
