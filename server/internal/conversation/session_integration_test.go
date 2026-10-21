//go:build integration

package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestExplicitDirectSessionsPreserveHistoryAndReplayIdentity(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service := newTestConversationService(t, store, bus)
	principal := auth.UserPrincipal{CompanyID: fixture.CompanyA, UserID: fixture.UserA, Role: auth.CompanyRoleOwner}
	key := uuid.New()
	first, err := service.CreateDirect(ctx, principal, CreateDirectInput{AgentID: fixture.AgentA, RequestKey: &key})
	if err != nil || !first.Created || first.Conversation.ID == fixture.DirectConversationA {
		t.Fatalf("new session = %+v %v", first, err)
	}
	replay, err := service.CreateDirect(
		ctx,
		principal,
		CreateDirectInput{AgentID: fixture.AgentA, RequestKey: &key},
	)
	if err != nil || replay.Created || replay.Conversation.ID != first.Conversation.ID {
		t.Fatalf("replay = %+v %v", replay, err)
	}
	secondKey := uuid.New()
	secondTitle := "Named review"
	second, err := service.CreateDirect(ctx,
		principal,
		CreateDirectInput{
			AgentID:    fixture.AgentA,
			RequestKey: &secondKey,
			Title:      &secondTitle,
		})
	if err != nil || !second.Created || second.Conversation.ID == first.Conversation.ID {
		t.Fatalf("second session = %+v %v", second, err)
	}
	messageKey := uuid.New()
	message, err := service.Send(ctx,
		principal,
		MessageTarget{
			Kind: TargetConversation,
			ID:   first.Conversation.ID,
		},
		SendInput{
			RequestKey: messageKey,
			Content:    "Plan\n our next steps.",
		})
	if err != nil {
		t.Fatal(err)
	}
	var runType string
	var sourceRevision, conversationRevision int64
	var frozenSource []byte
	if err := pool.QueryRow(ctx,
		`SELECT run_type,revision,context->'source' FROM runs WHERE id=$1`,
		*message.RunID).Scan(&runType,
		&sourceRevision,
		&frozenSource); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT version FROM conversations WHERE id=$1`,
		first.Conversation.ID).Scan(&conversationRevision); err != nil {
		t.Fatal(err)
	}
	var source struct {
		EntityID   uuid.UUID
		EntityType string
		Revision   int64
	}
	if err := json.Unmarshal(frozenSource, &source); err != nil {
		t.Fatal(err)
	}
	if *message.RunID == uuid.Nil ||
		runType != "direct_chat" ||
		sourceRevision != first.Conversation.Revision ||
		sourceRevision >= conversationRevision ||
		source.EntityID != first.Conversation.ID ||
		source.EntityType != "conversation" ||
		source.Revision != sourceRevision {
		t.Fatalf("Direct Run source = type %s, revision %d (Conversation now %d), %+v",
			runType,
			sourceRevision,
			conversationRevision,
			source)
	}
	var title string
	if err := pool.QueryRow(ctx,
		`SELECT title FROM conversations WHERE id=$1`,
		first.Conversation.ID).Scan(&title); err != nil ||
		title != "Plan our next steps." {
		t.Fatalf("first-message title = %q %v", title, err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, *message.RunID)
	if _, err := service.Send(ctx,
		principal,
		MessageTarget{
			Kind: TargetConversation,
			ID:   first.Conversation.ID,
		},
		SendInput{
			RequestKey: uuid.New(),
			Content:    "Keep this separate follow-up out of the title.",
		}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT title FROM conversations WHERE id=$1`,
		first.Conversation.ID).Scan(&title); err != nil ||
		title != "Plan our next steps." {
		t.Fatalf("stable first-message title = %q %v", title, err)
	}
	if _, err := service.Send(ctx,
		principal,
		MessageTarget{
			Kind: TargetConversation,
			ID:   second.Conversation.ID,
		},
		SendInput{
			RequestKey: uuid.New(),
			Content:    "This message must not replace an explicit title.",
		}); err != nil {
		t.Fatal(err)
	}
	listed, err := service.FetchDirect(ctx, principal, second.Conversation.ID)
	if err != nil || listed.Conversation.Title == nil || *listed.Conversation.Title != secondTitle {
		t.Fatalf("explicit title projection = %+v %v", listed, err)
	}
	var active int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM conversations WHERE company_id=$1 AND agent_id=$2 AND kind='direct' AND archived_at IS NULL`,
		fixture.CompanyA,
		fixture.AgentA,
	).Scan(&active); err != nil ||
		active != 3 {
		t.Fatalf("active sessions = %d %v", active, err)
	}
	foreign := auth.UserPrincipal{CompanyID: fixture.CompanyB, UserID: fixture.UserB, Role: auth.CompanyRoleOwner}
	if _, err := service.CreateDirect(ctx,
		foreign,
		CreateDirectInput{
			AgentID:    fixture.AgentA,
			RequestKey: &key,
		}); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign Agent = %v", err)
	}
}

func TestTaskConversationPinsArePrivateAndRevisioned(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role,status) VALUES($1,$2,'member','active')`,
		fixture.CompanyA,
		fixture.UserB)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, fixture.UserB)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service := newTestConversationService(t, store, bus)
	first := auth.UserPrincipal{CompanyID: fixture.CompanyA, UserID: fixture.UserA, Role: auth.CompanyRoleOwner}
	second := auth.UserPrincipal{CompanyID: fixture.CompanyA, UserID: fixture.UserB, Role: auth.CompanyRoleMember}
	initial, err := service.FetchState(ctx, first, fixture.ConversationA)
	if err != nil || initial.Revision != 0 {
		t.Fatalf("initial = %+v %v", initial, err)
	}
	pin := true
	saved, err := service.UpdateState(ctx,
		first,
		fixture.ConversationA,
		UpdateUserStateInput{
			ExpectedRevision: 0,
			Pinned:           &pin,
		})
	if err != nil || saved.Revision != 1 || saved.PinnedAt == nil {
		t.Fatalf("pin = %+v %v", saved, err)
	}
	other, err := service.FetchState(ctx, second, fixture.ConversationA)
	if err != nil || other.PinnedAt != nil || other.Revision != 0 {
		t.Fatalf("other viewer = %+v %v", other, err)
	}
	pins, err := service.ListPins(ctx, first)
	if err != nil || len(pins) != 1 || pins[0].ConversationID != fixture.ConversationA {
		t.Fatalf("pins = %+v %v", pins, err)
	}
	otherPins, err := service.ListPins(ctx, second)
	if err != nil || len(otherPins) != 0 {
		t.Fatalf("other pins = %+v %v", otherPins, err)
	}
	unpin := false
	if _, err := service.UpdateState(ctx,
		first,
		fixture.ConversationA,
		UpdateUserStateInput{
			ExpectedRevision: 0,
			Pinned:           &unpin,
		}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale unpin = %v", err)
	}
	cleared, err := service.UpdateState(ctx,
		first,
		fixture.ConversationA,
		UpdateUserStateInput{
			ExpectedRevision: 1,
			Pinned:           &unpin,
		})
	if err != nil || cleared.PinnedAt != nil || cleared.Revision != 2 {
		t.Fatalf("unpin = %+v %v", cleared, err)
	}
	dbtest.MustExec(t,
		ctx,
		pool,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		fixture.CompanyA,
		fixture.UserA)
	if _, err := service.UpdateState(ctx,
		first,
		fixture.ConversationA,
		UpdateUserStateInput{
			ExpectedRevision: 2,
			Pinned:           &pin,
		}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked pin = %v", err)
	}
}

func TestConversationHistoryPrivacyCountsAndRevisionPaging(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'admin')`,
		fixture.CompanyA,
		fixture.UserB)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := activity.RegisterListeners(bus, store.Queries()); err != nil {
		t.Fatal(err)
	}
	var announced []activity.Entry
	if err := bus.Subscribe(activity.Topic, func(_ context.Context, event events.Event) error {
		entry, err := events.PayloadOf[activity.Entry](event)
		if err == nil {
			announced = append(announced, entry)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	service := newTestConversationService(t, store, bus)
	principal := auth.UserPrincipal{CompanyID: fixture.CompanyA, UserID: fixture.UserA, Role: auth.CompanyRoleOwner}
	title := "Creator-only planning"
	requestKey := uuid.New()
	created, err := service.CreateDirect(ctx,
		principal,
		CreateDirectInput{
			AgentID:    fixture.AgentA,
			Title:      &title,
			RequestKey: &requestKey,
		})
	if err != nil {
		t.Fatal(err)
	}
	conversationID := created.Conversation.ID
	state, err := service.FetchState(ctx, principal, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	pin := true
	pinned, err := service.UpdateState(ctx,
		principal,
		conversationID,
		UpdateUserStateInput{
			ExpectedRevision: state.Revision,
			Pinned:           &pin,
		})
	if err != nil {
		t.Fatal(err)
	}
	archived, err := service.UpdateState(ctx,
		principal,
		conversationID,
		UpdateUserStateInput{
			ExpectedRevision: pinned.Revision,
			Archived:         &pin,
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateState(ctx,
		principal,
		fixture.ConversationA,
		UpdateUserStateInput{
			ExpectedRevision: 0,
			Pinned:           &pin,
		}); err != nil {
		t.Fatal(err)
	}
	if len(announced) != 4 {
		t.Fatalf("private history announcements = %+v, want create, pin, archive and Task pin", announced)
	}
	for _, entry := range announced {
		if !entry.VisibilityUserID.Valid || entry.VisibilityUserID.UUID != fixture.UserA {
			t.Fatalf("private routing = %+v", entry)
		}
	}
	// Known public evidence and unknown legacy scope bracket the private rows.
	// The public row is about an Agent: a Company-wide row about a Task must
	// name its Project and reads only for its readers (ADR 0017).
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO activity_events(company_id,actor_type,action,entity_type,entity_id,visibility,details,created_at) VALUES($1,'system','agent.updated','agent',$2,'company','{"schemaVersion":0,"payload":{}}',clock_timestamp()),($1,'system','agent.updated','agent',$2,NULL,'{"schemaVersion":0,"payload":{}}',clock_timestamp())`,
		fixture.CompanyA,
		fixture.AgentA.String(),
	)
	feed := activity.NewFeed(store.Queries())
	today := time.Now().UTC()
	month := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		userID uuid.UUID
		want   int
	}{{"creator", fixture.UserA, 5}, {"other admin", fixture.UserB, 1}, {"outsider", uuid.New(), 0}} {
		t.Run(tc.name, func(t *testing.T) {
			var all []activity.Entry
			var cursor *activity.Cursor
			for {
				rows,
					next, err := feed.List(ctx,
					fixture.CompanyA,
					activity.ListOptions{
						UserID: tc.userID,
						Limit:  1,
						Before: cursor,
					})
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) == 0 && next != nil {
					t.Fatal("hidden row produced an empty continuation")
				}
				all = append(all, rows...)
				if next == nil {
					break
				}
				if len(all) > tc.want {
					t.Fatal("pagination repeated or disclosed rows")
				}
				cursor = next
			}
			if len(all) != tc.want {
				t.Fatalf("feed = %+v, want %d", all, tc.want)
			}
			calendar, err := feed.Calendar(ctx,
				fixture.CompanyA,
				activity.CalendarOptions{
					UserID:   tc.userID,
					Month:    month.Format("2006-01"),
					TimeZone: "UTC",
					Location: time.UTC,
					StartAt:  month,
					EndAt: month.AddDate(0,
						1,
						0),
				})
			if err != nil || calendar.TotalCount != int64(tc.want) {
				t.Fatalf("calendar = %+v, %v, want %d", calendar, err, tc.want)
			}
			if tc.userID == fixture.UserA {
				for _, expected := range announced {
					found := false
					for _, got := range all {
						if got.ID == expected.ID {
							found = true
							// The frame's audience is a routing fact the
							// feed read never carries (ADR 0017).
							expected.Audience = nil
							if !reflect.DeepEqual(got, expected) {
								t.Fatalf(
									"REST/realtime mismatch: %+v vs %+v",
									got,
									expected,
								)
							}
						}
					}
					if !found {
						t.Fatalf("announced history missing: %s", expected.ID)
					}
				}
			}
		})
	}
	upper := archived.Revision + 1
	var cursor *activity.Cursor
	for _, revision := range []int64{archived.Revision, pinned.Revision, state.Revision} {
		rows,
			next, err := feed.List(ctx,
			fixture.CompanyA,
			activity.ListOptions{
				UserID:         fixture.UserA,
				EntityType:     "conversation",
				EntityID:       conversationID.String(),
				BeforeRevision: &upper,
				Limit:          1,
				Before:         cursor,
			})
		if err != nil ||
			len(rows) != 1 ||
			rows[0].Revision == nil ||
			*rows[0].Revision != revision ||
			rows[0].Entity.ID != conversationID.String() {
			t.Fatalf("revision page %d = %+v, %v", revision, rows, err)
		}
		exact,
			_, err := feed.List(ctx,
			fixture.CompanyA,
			activity.ListOptions{
				UserID:     fixture.UserA,
				EntityType: "conversation",
				EntityID:   conversationID.String(),
				Revision:   &revision,
				Limit:      10,
			})
		if err != nil || len(exact) != 1 || exact[0].ID != rows[0].ID {
			t.Fatalf("exact revision = %+v, %v", exact, err)
		}
		if revision == pinned.Revision {
			var details struct {
				Before map[string]any
				After  map[string]any
			}
			if err := json.Unmarshal(rows[0].Details, &details); err != nil {
				t.Fatal(err)
			}
			if value, ok := details.Before["pinnedAt"]; !ok ||
				value != nil ||
				details.After["pinnedAt"] == nil ||
				details.After["id"] != conversationID.String() {
				t.Fatalf("pin snapshot = %s", rows[0].Details)
			}
		}
		cursor = next
	}
	if cursor != nil {
		t.Fatal("revision paging included unversioned filing evidence")
	}
	dbtest.MustExec(t,
		ctx,
		pool,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		fixture.CompanyA,
		fixture.UserA)
	rows, next, err := feed.List(ctx, fixture.CompanyA, activity.ListOptions{UserID: fixture.UserA, Limit: 1})
	if err != nil || len(rows) != 0 || next != nil {
		t.Fatalf("revoked feed = %+v, %v, %v", rows, next, err)
	}
	calendar, err := feed.Calendar(ctx,
		fixture.CompanyA,
		activity.CalendarOptions{
			UserID:   fixture.UserA,
			Month:    month.Format("2006-01"),
			TimeZone: "UTC",
			Location: time.UTC,
			StartAt:  month,
			EndAt: month.AddDate(0,
				1,
				0),
		})
	if err != nil || calendar.TotalCount != 0 {
		t.Fatalf("revoked calendar = %+v, %v", calendar, err)
	}
}
