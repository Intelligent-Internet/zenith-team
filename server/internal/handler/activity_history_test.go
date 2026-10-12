package handler

import (
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/activity"
	"github.com/google/uuid"
)

func TestActivityRevisionFiltersAndCursorStayWithinTheirSubject(t *testing.T) {
	t.Parallel()
	subject := "00000000-0000-4000-8000-000000000031"
	for _, query := range []string{
		"entity_type=task&entity_id=" + subject + "&before_revision=8",
		"entity_type=conversation&entity_id=" + subject + "&revision=8",
		"entity_type=agent", "actor_type=system", "actor_type=agent&actor_id=" + subject,
	} {
		if _, err := parseActivityQuery(runQueryContext(query)); err != nil {
			t.Errorf("valid query %s: %v", query, err)
		}
	}
	for _, query := range []string{
		"revision=8", "entity_type=run&entity_id=" + subject + "&revision=8",
		"entity_type=task&entity_id=" + subject + "&revision=8&before_revision=9",
		"entity_type=task&entity_id=" + subject + "&revision=9007199254740992",
		"actor_id=" + subject, "actor_type=system&actor_id=" + subject, "agent_id=" + subject,
	} {
		if _, err := parseActivityQuery(runQueryContext(query)); err == nil {
			t.Errorf("accepted invalid query %s", query)
		}
	}
	query := "entity_type=task&entity_id=" + subject + "&before_revision=8"
	c := runQueryContext(query)
	options, err := parseActivityQuery(c)
	if err != nil {
		t.Fatal(err)
	}
	cursor := encodeActivityCursor(c, options, &activity.Cursor{ID: uuid.New(), CreatedAt: time.Now().UTC(), Revision: 7})
	if _, err := parseActivityQuery(runQueryContext(query + "&cursor=" + cursor)); err != nil {
		t.Fatalf("same filters: %v", err)
	}
	if _, err := parseActivityQuery(runQueryContext(query + "&action=task.updated&cursor=" + cursor)); err == nil {
		t.Fatal("accepted cursor with a different filter")
	}
}
