package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
)

func TestParseActivityQuery(t *testing.T) {
	t.Parallel()

	agentID := uuid.MustParse("0198b3c4-b0d5-7e62-8f19-3ac75d09e403")
	cursorFixture := activityCursorFixture()
	cursorOptions := activity.ListOptions{
		EntityType: "task",
		EntityID:   "TASK-42",
		Action:     "task.updated",
		ActorType:  "agent",
		ActorID:    agentID.String(),
	}
	from := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	cursorOptions.From, cursorOptions.To = &from, &to
	cursor := encodeActivityCursor(activityQueryContext(""), cursorOptions, &cursorFixture)
	tests := []struct {
		name     string
		rawQuery string
		check    func(t *testing.T, got activity.ListOptions)
		wantErr  error
	}{
		{
			name: "defaults",
			check: func(t *testing.T, got activity.ListOptions) {
				t.Helper()
				if got.Limit != 50 {
					t.Fatalf("Limit = %d, want 50", got.Limit)
				}
			},
		},
		{
			name: "all filters",
			rawQuery: "limit=25&cursor=" + cursor +
				"&from=2026-08-01T00%3A00%3A00Z&to=2026-09-01T00%3A00%3A00Z" +
				"&entity_type=task&entity_id=TASK-42&action=task.updated&actor_type=agent&actor_id=" + agentID.String(),
			check: func(t *testing.T, got activity.ListOptions) {
				t.Helper()
				if got.Limit != 25 || got.Before == nil || got.EntityType != "task" ||
					got.EntityID != "TASK-42" || got.Action != "task.updated" ||
					got.ActorType != "agent" || got.ActorID != agentID.String() || got.From == nil ||
					!got.From.Equal(from) || got.To == nil || !got.To.Equal(to) {
					t.Fatalf("options = %+v, want all filters", got)
				}
			},
		},
		{name: "unknown parameter", rawQuery: "offset=2", wantErr: errInvalidActivityInput},
		{
			name:     "old camel case entity",
			rawQuery: "entityType=task&entityId=1",
			wantErr:  errInvalidActivityInput,
		},
		{
			name:     "repeated action",
			rawQuery: "action=task.created&action=run.failed",
			wantErr:  errInvalidActivityInput,
		},
		{name: "empty action", rawQuery: "action=", wantErr: errInvalidActivityInput},
		// An action is an event's wire name, so a bare verb with no topic is not one.
		{name: "invalid action", rawQuery: "action=changed", wantErr: errInvalidActivityInput},
		{name: "shouting action", rawQuery: "action=Task.Created", wantErr: errInvalidActivityInput},
		// Activity-only domains may be two words (ADR 0017 project_grant rows).
		{
			name:     "project grant rows",
			rawQuery: "entity_type=project_grant&action=project_grant.joined",
			check: func(t *testing.T, got activity.ListOptions) {
				t.Helper()
				if got.EntityType != "project_grant" || got.Action != "project_grant.joined" {
					t.Fatal(got)
				}
			},
		},
		{
			name:     "entity type alone",
			rawQuery: "entity_type=task",
			check: func(t *testing.T, got activity.ListOptions) {
				t.Helper()
				if got.EntityType != "task" {
					t.Fatal(got)
				}
			},
		},
		{name: "entity id alone", rawQuery: "entity_id=1", wantErr: errInvalidActivityInput},
		// An entity type is a lowercase domain name.
		{
			name:     "invalid entity type",
			rawQuery: "entity_type=my-entity&entity_id=1",
			wantErr:  errInvalidActivityInput,
		},
		{name: "blank entity id", rawQuery: "entity_type=task&entity_id=+", wantErr: errInvalidActivityInput},
		{name: "invalid agent", rawQuery: "agent_id=agent", wantErr: errInvalidActivityInput},
		{name: "invalid from", rawQuery: "from=tomorrow", wantErr: errInvalidActivityInput},
		{
			name:     "non-increasing window",
			rawQuery: "from=2026-09-01T00%3A00%3A00Z&to=2026-09-01T00%3A00%3A00Z",
			wantErr:  errInvalidActivityInput,
		},
		{name: "invalid cursor", rawQuery: "cursor=garbage", wantErr: errInvalidActivityCursor},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseActivityQuery(activityQueryContext(test.rawQuery))
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("parseActivityQuery() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr == nil {
				test.check(t, got)
			}
		})
	}
}

func TestParseActivityCalendarQueryResolvesDSTMonthBounds(t *testing.T) {
	t.Parallel()

	got, err := parseActivityCalendarQuery(activityQueryContext("month=2026-03&time_zone=America%2FNew_York"))
	if err != nil {
		t.Fatalf("parseActivityCalendarQuery() error = %v", err)
	}
	wantStart := time.Date(2026, time.March, 1, 5, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, time.April, 1, 4, 0, 0, 0, time.UTC)
	if got.Month != "2026-03" || got.TimeZone != "America/New_York" ||
		!got.StartAt.Equal(wantStart) || !got.EndAt.Equal(wantEnd) {
		t.Fatalf("calendar query = %+v, want %s through %s", got, wantStart, wantEnd)
	}
}

func TestParseActivityCalendarQueryRejectsInvalidShape(t *testing.T) {
	t.Parallel()

	for _, rawQuery := range []string{
		"",
		"month=2026-08",
		"time_zone=UTC",
		"month=2026-8&time_zone=UTC",
		"month=2026-13&time_zone=UTC",
		"month=0000-01&time_zone=UTC",
		"month=2026-08&time_zone=Local",
		"month=2026-08&time_zone=Not%2FAZone",
		"month=2026-08&time_zone=UTC&extra=true",
		"month=2026-08&month=2026-09&time_zone=UTC",
		"month=2026-08&time_zone=UTC&time_zone=Europe%2FBerlin",
	} {
		t.Run(rawQuery, func(t *testing.T) {
			t.Parallel()
			if _, err := parseActivityCalendarQuery(
				activityQueryContext(rawQuery),
			); !errors.Is(
				err,
				errInvalidActivityInput,
			) {
				t.Fatalf("parseActivityCalendarQuery(%q) error = %v, want invalid input", rawQuery, err)
			}
		})
	}
}

func activityCursorFixture() activity.Cursor {
	return activity.Cursor{
		CreatedAt: time.Date(2026, time.August, 17, 8, 9, 10, 123456000, time.UTC),
		ID:        uuid.MustParse("0198b3c4-b0d5-7e62-8f19-3ac75d09e4b2"),
	}
}

func activityQueryContext(rawQuery string) *gin.Context {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	path := "/"
	if rawQuery != "" {
		path += "?" + rawQuery
	}
	context.Request = httptest.NewRequest(http.MethodGet, path, nil)
	return context
}
