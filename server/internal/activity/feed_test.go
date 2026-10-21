package activity

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestActivityPageUsesOverfetchRowOnlyForContinuation(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, time.August, 17, 8, 9, 10, 0, time.UTC)
	rows := []db.ActivityEvent{
		{
			ID:        uuid.MustParse("0198b3c4-b0d5-7e62-8f19-3ac75d09e4b2"),
			CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
		},
		{
			ID:        uuid.MustParse("0198b3c4-b0d5-7e62-8f19-3ac75d09e4b1"),
			CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
		},
		{
			ID:        uuid.MustParse("0198b3c4-b0d5-7e62-8f19-3ac75d09e4b0"),
			CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
		},
	}

	page, next := activityPage(rows[:2], 2)
	if len(page) != 2 || next != nil {
		t.Fatalf("exact-limit page = (%d rows, %+v cursor), want (2, nil)", len(page), next)
	}

	page, next = activityPage(rows, 2)
	if len(page) != 2 || next == nil {
		t.Fatalf("overfetched page = (%d rows, %+v cursor), want (2, non-nil)", len(page), next)
	}
	if next.ID != rows[1].ID || !next.CreatedAt.Equal(rows[1].CreatedAt.Time) {
		t.Fatalf("next cursor = %+v, want boundary row %+v", next, rows[1])
	}
}

// Activity is read by every member of the row's audience, and a member's
// machine is theirs alone (ADR 0014). A Run snapshot's runtimeId is
// withheld, and an Agent receipt stored before its frame dropped the
// creator's setup no longer names that machine; every other field reads
// back as stored.
func TestEntriesNameNoMembersMachine(t *testing.T) {
	t.Parallel()
	runtimeID := "b6e656b4-873b-4c4e-bb89-ae6be0cdd321"
	createdAt := pgtype.Timestamptz{Time: time.Date(2026, time.September, 25, 10, 31, 4, 0, time.UTC), Valid: true}
	for _, test := range []struct {
		name, entityType, details string
		want                      map[string]any
	}{
		{
			name:       "run snapshot",
			entityType: "run",
			details: `{"schemaVersion":1,"context":{"entityKey":"BSC-5","entityTitle":null,"actorDisplayName":null},` +
				`"before":{"id":"r1","status":"queued","runtimeId":"` + runtimeID + `"},` +
				`"after":{"id":"r1","status":"dispatched","runtimeId":"` + runtimeID + `"}}`,
			want: map[string]any{
				"schemaVersion": float64(1),
				"context":       map[string]any{"entityKey": "BSC-5", "entityTitle": nil, "actorDisplayName": nil},
				"before":        map[string]any{"id": "r1", "status": "queued", "runtimeId": nil},
				"after":         map[string]any{"id": "r1", "status": "dispatched", "runtimeId": nil},
			},
		},
		{
			name:       "run creation",
			entityType: "run",
			details:    `{"schemaVersion":1,"context":{},"before":null,"after":{"id":"r1","runtimeId":"` + runtimeID + `"}}`,
			want: map[string]any{
				"schemaVersion": float64(1), "context": map[string]any{}, "before": nil,
				"after": map[string]any{"id": "r1", "runtimeId": nil},
			},
		},
		{
			name:       "run receipt",
			entityType: "run",
			details: `{"schemaVersion":0,"context":{},` +
				`"payload":{"id":"r1","status":"completed","runtimeId":"` + runtimeID + `"}}`,
			want: map[string]any{
				"schemaVersion": float64(0), "context": map[string]any{},
				"payload": map[string]any{"id": "r1", "status": "completed", "runtimeId": nil},
			},
		},
		{
			name:       "agent receipt",
			entityType: "agent",
			details: `{"schemaVersion":0,"context":{},"payload":{"id":"a1","name":"Lyra",` +
				`"binding":{"id":"b1","runtime":{"id":"` + runtimeID + `","name":"Codex (AlexRivera)",` +
				`"signIn":{"state":"signed_in","detail":"Logged in using ChatGPT."}}},` +
				`"runtime":{"id":"` + runtimeID + `","name":"Codex (AlexRivera)"}}}`,
			want: map[string]any{
				"schemaVersion": float64(0), "context": map[string]any{},
				"payload": map[string]any{"id": "a1", "name": "Lyra", "binding": nil},
			},
		},
		{
			name:       "other subject",
			entityType: "task",
			details:    `{"schemaVersion":1,"context":{},"before":null,"after":{"id":"t1","title":"Stored"}}`,
			want: map[string]any{
				"schemaVersion": float64(1), "context": map[string]any{}, "before": nil,
				"after": map[string]any{"id": "t1", "title": "Stored"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			entry := entryOf(db.ActivityEvent{
				ID: uuid.New(), EntityType: test.entityType, EntityID: "subject",
				Details: []byte(test.details), CreatedAt: createdAt,
			})
			if strings.Contains(string(entry.Details), runtimeID) ||
				strings.Contains(string(entry.Details), "AlexRivera") {
				t.Fatalf("entry names a member's machine: %s", entry.Details)
			}
			var got map[string]any
			if err := json.Unmarshal(entry.Details, &got); err != nil {
				t.Fatalf("decode entry details %s: %v", entry.Details, err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("entry details = %v, want %v", got, test.want)
			}
		})
	}
}
