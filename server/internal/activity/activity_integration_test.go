//go:build integration

package activity

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestUncoveredEventsRetainEvidenceWithoutDuplicatingOwnerHistory(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var broadcast []Entry
	if err := bus.Subscribe(Topic, func(_ context.Context, event events.Event) error {
		entry, err := events.PayloadOf[Entry](event)
		if err == nil {
			broadcast = append(broadcast, entry)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterListeners(bus, queries); err != nil {
		t.Fatal(err)
	}
	publish := func(topic, action string, id uuid.UUID, actor events.Actor) {
		bus.Publish(ctx, topic, events.Event{
			EntityType: topic, Name: action, CompanyID: fixture.CompanyA, CreatedAt: time.Now().UTC(),
			Actor: actor, Payload: map[string]any{"id": id.String(), "title": "Stored title"},
		})
	}
	publish("agent", "agent.updated", fixture.AgentA, events.UserActor(fixture.UserA.String()))
	publish("runtime", "runtime.updated", fixture.RuntimeA, events.AgentActor(fixture.AgentA.String()))
	publish("task", "task.updated", fixture.TaskA, events.SystemActor())
	publish("run", "run.completed", fixture.RunA, events.SystemActor())
	publish("unknown", "unknown.updated", uuid.New(), events.SystemActor())
	feed, _, err := NewFeed(queries).List(ctx, fixture.CompanyA, ListOptions{UserID: fixture.UserA, Limit: 50})
	if err != nil || len(feed) != 2 || len(broadcast) != 2 {
		t.Fatalf("feed/broadcast = %v/%v, %v", feed, broadcast, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_events`).Scan(&count); err != nil || count != 3 {
		t.Fatalf(
			"stored evidence = %d, %v; want two visible, one unknown-scope, no duplicate core history",
			count,
			err,
		)
	}
	for i, entry := range broadcast {
		if !reflect.DeepEqual(entry, feed[len(feed)-1-i]) {
			t.Fatal("REST and realtime differ")
		}
		var details struct {
			SchemaVersion int            `json:"schemaVersion"`
			Payload       map[string]any `json:"payload"`
		}
		if err := json.Unmarshal(entry.Details, &details); err != nil {
			t.Fatal(err)
		}
		if details.SchemaVersion != 0 || details.Payload["title"] != "Stored title" || entry.Revision != nil {
			t.Fatalf("unversioned evidence = %+v", entry)
		}
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE users SET display_name = 'Renamed' WHERE id = $1`, fixture.UserA)
	userRows, _, err := NewFeed(
		queries,
	).List(ctx, fixture.CompanyA, ListOptions{UserID: fixture.UserA, ActorType: "user", ActorID: fixture.UserA.String(), Limit: 50})
	if err != nil || len(userRows) != 1 || userRows[0].Actor.DisplayName == nil ||
		*userRows[0].Actor.DisplayName != "Schema A" {
		t.Fatalf("frozen actor label = %+v, %v", userRows, err)
	}
	publish("runtime", "runtime.updated", fixture.RuntimeA, events.SystemActor())
	if entry := broadcast[len(broadcast)-1]; entry.Actor.ID != nil || entry.Actor.DisplayName != nil {
		t.Fatalf("system actor = %+v", entry.Actor)
	}
}

// A daemon wakeup records nothing anyone did and names a member's runtime,
// so it never becomes Company activity — neither when it is published on a
// Task's Conversation, which every member may read, nor from a row an older
// sink already stored.
func TestDaemonWakeupsStayOutOfTheFeed(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"runtime.work_available", "runtime.pending_work", "runtime.capabilities_changed", "connector.policy_changed", "conversation.channel_agents_changed", "conversation.discussion_resync", "wiki.run_reads_changed", "schedule.changed", "calendar.changed", "canvas.changed", "canvas.document_changed", "whiteboard.changed"} {
		if err := record(ctx, bus, queries, events.Event{
			EntityType: "runtime", Name: action, CompanyID: fixture.CompanyA,
			ConversationID: fixture.ConversationA, RunID: fixture.RunA, Actor: events.SystemActor(),
			Payload: struct{ RuntimeID, RunID string }{fixture.RuntimeA.String(), fixture.RunA.String()},
		}); err != nil {
			t.Fatal(err)
		}
	}
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_events WHERE entity_type = 'runtime'`).
		Scan(&stored); err != nil ||
		stored != 0 {
		t.Fatalf("stored daemon wakeups = %d, %v; want none", stored, err)
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO activity_events (company_id, actor_type, action, entity_type, entity_id, visibility, schema_version, details)
		VALUES ($1, 'system', 'runtime.work_available', 'runtime', '', 'company', 0, $2)`,
		fixture.CompanyA,
		`{"schemaVersion":0,"payload":{"RunID":"","RuntimeID":"`+fixture.RuntimeA.String()+`"},"context":{}}`,
	)
	feed, _, err := NewFeed(queries).List(ctx, fixture.CompanyA, ListOptions{UserID: fixture.UserA, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range feed {
		if strings.HasPrefix(entry.Action, "runtime.") ||
			strings.Contains(string(entry.Details), fixture.RuntimeA.String()) {
			t.Fatalf("feed shows a daemon wakeup: %+v", entry)
		}
	}
	calendar, err := NewFeed(queries).Calendar(ctx, fixture.CompanyA, CalendarOptions{
		UserID: fixture.UserA, Month: time.Now().UTC().Format("2006-01"), TimeZone: "UTC", Location: time.UTC,
		StartAt: time.Now().UTC().AddDate(0, 0, -1), EndAt: time.Now().UTC().AddDate(0, 0, 1),
	})
	if err != nil || calendar.TotalCount != int64(len(feed)) {
		t.Fatalf("calendar = %+v, %v; want the feed's %d rows", calendar, err, len(feed))
	}
}

func TestEvidenceUsesTheActionSubjectAndPreservesPrivateScope(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	draftID := uuid.New()
	var privateID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO conversations (company_id, kind, created_by_user_id) VALUES ($1, 'direct', $2) RETURNING id`, fixture.CompanyA, fixture.UserA).
		Scan(&privateID); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		action, topic, subjectType string
		subjectID, conversationID  uuid.UUID
		payload                    map[string]any
	}{
		{"run.message_created", "run", "message", fixture.MessageA, privateID, map[string]any{"id": fixture.MessageA}},
		{"mailbox.changed", "mailbox", "mailbox", fixture.CompanyA, uuid.Nil, map[string]any{"reason": "configured"}},
		{"mailbox.draft_changed", "mailbox", "mailbox", draftID, privateID, map[string]any{"draftId": draftID}},
	} {
		t.Run(test.action, func(t *testing.T) {
			if err := record(ctx, bus, queries, events.Event{
				EntityType: test.topic, Name: test.action, CompanyID: fixture.CompanyA,
				ConversationID: test.conversationID, Actor: events.SystemActor(), Payload: test.payload,
			}); err != nil {
				t.Fatal(err)
			}
			rows, _, err := NewFeed(queries).List(ctx, fixture.CompanyA, ListOptions{
				UserID:     fixture.UserA,
				EntityType: test.subjectType, EntityID: test.subjectID.String(), Limit: 10,
			})
			if err != nil || len(rows) != 1 {
				t.Fatalf("subject history = %+v, %v", rows, err)
			}
			if test.conversationID != uuid.Nil &&
				(!rows[0].VisibilityUserID.Valid || rows[0].VisibilityUserID.UUID != fixture.UserA) {
				t.Fatalf("private visibility = %+v", rows[0])
			}
		})
	}
}

func TestMailboxHistoryRequiresOwnerForFeedCalendarAndRealtime(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'admin')`,
		fixture.CompanyA,
		fixture.UserB,
	)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, fixture.UserB)
	for _, conversationID := range []uuid.UUID{uuid.Nil, fixture.ConversationA, fixture.DirectConversationA} {
		if err := record(
			ctx,
			bus,
			queries,
			events.Event{
				EntityType:     "mailbox",
				Name:           "mailbox.changed",
				CompanyID:      fixture.CompanyA,
				ConversationID: conversationID,
				Actor:          events.SystemActor(),
				Payload:        map[string]any{"reason": "configured"},
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	feed := NewFeed(queries)
	now := time.Now().UTC()
	ownerRows, _, err := feed.List(
		ctx,
		fixture.CompanyA,
		ListOptions{UserID: fixture.UserA, EntityType: "mailbox", Limit: 10},
	)
	if err != nil || len(ownerRows) != 3 {
		t.Fatalf("owner history = %v, %v", ownerRows, err)
	}
	for _, role := range []string{"admin", "member"} {
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`UPDATE company_members SET role=$3 WHERE company_id=$1 AND user_id=$2`,
			fixture.CompanyA,
			fixture.UserB,
			role,
		)
		rows, _, err := feed.List(ctx, fixture.CompanyA, ListOptions{UserID: fixture.UserB, Limit: 10})
		if err != nil || len(rows) != 0 {
			t.Errorf("%s history = %v, %v, want empty", role, rows, err)
		}
		calendar, err := feed.Calendar(
			ctx,
			fixture.CompanyA,
			CalendarOptions{
				UserID:   fixture.UserB,
				TimeZone: "UTC",
				Location: time.UTC,
				StartAt:  now.Add(-time.Hour),
				EndAt:    now.Add(time.Hour),
			},
		)
		if err != nil || calendar.TotalCount != 0 {
			t.Errorf("%s calendar = %+v, %v, want no Mailbox counts", role, calendar, err)
		}
		for _, conversationID := range []uuid.UUID{uuid.Nil, fixture.ConversationA, fixture.DirectConversationA} {
			row := generated.ActivityEvent{
				CompanyID:  fixture.CompanyA,
				EntityType: "mailbox",
				Visibility: optionalText("company"),
			}
			if conversationID == fixture.ConversationA {
				row.ProjectID = uuid.NullUUID{UUID: fixture.ProjectA, Valid: true}
			}
			if conversationID == fixture.DirectConversationA {
				row.Visibility = optionalText("user")
				row.VisibilityUserID = uuid.NullUUID{UUID: fixture.UserA, Valid: true}
			}
			audience, err := entryAudience(ctx, queries, row)
			if err != nil || audience == nil || audience.CompanyWide ||
				!slices.Equal(audience.UserIDs, []uuid.UUID{fixture.UserA}) {
				t.Errorf("Mailbox history audience = %+v, %v, want owner alone", audience, err)
			}
		}
	}
}
