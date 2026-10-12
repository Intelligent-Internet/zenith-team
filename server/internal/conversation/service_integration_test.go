//go:build integration

package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestCreateDirectDefaultsOnlyFirstCEOChatToPinned(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name            string
		otherAgent      bool
		explicitSession bool
		wantPinned      bool
	}{
		{name: "first CEO chat", wantPinned: true},
		{name: "first explicit CEO session", explicitSession: true, wantPinned: true},
		{name: "chat with another Agent", otherAgent: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
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
			dbtest.MustExec(t, ctx, pool, `
				INSERT INTO company_members(company_id,user_id,role,status)
				VALUES($1,$2,'member','active')`, fixture.CompanyA, fixture.UserB)
			agentID := fixture.AgentA
			if testCase.otherAgent {
				err = pool.QueryRow(ctx, `
					INSERT INTO agents(company_id,name,title,reports_to_agent_id,provider)
					VALUES($1,'Product','Product Agent',$2,'codex') RETURNING id`,
					fixture.CompanyA, fixture.AgentA).Scan(&agentID)
				if err != nil {
					t.Fatal(err)
				}
				dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, agentID,
					fixture.UserA, fixture.RuntimeA, "codex")
			}
			bus, err := events.NewBus(events.BusOptions{})
			if err != nil {
				t.Fatal(err)
			}
			service := newTestConversationService(t, store, bus)
			principal := auth.UserPrincipal{
				CompanyID: fixture.CompanyA, UserID: fixture.UserB, Role: auth.CompanyRoleMember,
			}
			input := CreateDirectInput{AgentID: agentID}
			if testCase.explicitSession {
				key := uuid.New()
				input.RequestKey = &key
			}
			created, err := service.CreateDirect(ctx, principal, input)
			if err != nil || !created.Created {
				t.Fatalf("CreateDirect() = %+v, %v, want a new conversation", created, err)
			}
			state, err := service.FetchState(ctx, principal, created.Conversation.ID)
			if err != nil || (state.PinnedAt != nil) != testCase.wantPinned {
				t.Fatalf("FetchState() = %+v, %v, want pinned %t", state, err, testCase.wantPinned)
			}
			pins, err := service.ListPins(ctx, principal)
			wantPins := 0
			if testCase.wantPinned {
				wantPins = 1
			}
			if err != nil || len(pins) != wantPins {
				t.Fatalf("ListPins() = %+v, %v, want %d pins", pins, err, wantPins)
			}
			if !testCase.wantPinned {
				return
			}

			pinned := false
			if _, err := service.UpdateState(ctx, principal, created.Conversation.ID, UpdateUserStateInput{
				ExpectedRevision: state.Revision, Pinned: &pinned,
			}); err != nil {
				t.Fatalf("unpin CEO chat: %v", err)
			}
			reopened, err := service.CreateDirect(ctx, principal, input)
			if err != nil || reopened.Created || reopened.Conversation.ID != created.Conversation.ID {
				t.Fatalf("reopen CEO chat = %+v, %v, want existing conversation", reopened, err)
			}
			state, err = service.FetchState(ctx, principal, reopened.Conversation.ID)
			if err != nil || state.PinnedAt != nil {
				t.Fatalf("reopened chat state = %+v, %v, want unpinned", state, err)
			}
			key := uuid.New()
			additional, err := service.CreateDirect(ctx, principal, CreateDirectInput{
				AgentID: agentID, RequestKey: &key,
			})
			if err != nil || !additional.Created {
				t.Fatalf("additional CEO session = %+v, %v, want a new conversation", additional, err)
			}
			state, err = service.FetchState(ctx, principal, additional.Conversation.ID)
			if err != nil || state.PinnedAt != nil {
				t.Fatalf("additional session state = %+v, %v, want unpinned", state, err)
			}
		})
	}
}

// FetchDirectDetail returns the newest window of a conversation's messages plus
// the bounds that locate that window in the whole history. With three messages
// and a limit of two the window is the newest two, hasOlder reports the one
// below it, and a limit that covers everything reports neither older nor newer.
// The agent ref and the caller's state ride along, so the panel paints from the
// one response.
func TestFetchDirectDetailReturnsNewestWindowWithBounds(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	service := newTestConversationService(t, store, bus)

	base := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	runID := seedRun(t, ctx, pool, fixture, fixture.DirectConversationA, "completed")
	for seq := int64(1); seq <= 3; seq++ {
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO conversation_items (
				id, company_id, conversation_id, run_id, seq, type, author_kind, author_id,
				content, created_at
			)
			VALUES ($1, $2, $3, $4, $5, 'user_message', 'user', $6, $7, $8)`,
			dbtest.NewUUID(t), fixture.CompanyA, fixture.DirectConversationA, runID, seq,
			fixture.UserA, "message "+strconv.FormatInt(seq, 10),
			base.Add(time.Duration(seq)*time.Minute),
		)
	}

	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}

	detail, err := service.FetchDirectDetail(
		ctx, principal, fixture.DirectConversationA, MessageWindow{Limit: 2},
	)
	if err != nil {
		t.Fatalf("FetchDirectDetail(limit=2): %v", err)
	}
	if len(detail.Messages) != 2 ||
		detail.Messages[0].Message.Seq != 2 || detail.Messages[1].Message.Seq != 3 {
		t.Fatalf("window seqs = %v, want [2 3]", messageSeqs(detail.Messages))
	}
	// Whether the window missed older or newer rows is derived, not carried:
	// the window's own seq range against the bounds is the whole answer.
	if detail.ThroughSeq != 3 || detail.EarliestAvailableSeq != 1 ||
		detail.Messages[0].Message.Seq <= detail.EarliestAvailableSeq ||
		detail.Messages[len(detail.Messages)-1].Message.Seq != detail.ThroughSeq {
		t.Fatalf("bounds = through %d earliest %d over %v",
			detail.ThroughSeq, detail.EarliestAvailableSeq, messageSeqs(detail.Messages))
	}
	if detail.Agent == nil || detail.Agent.ID != fixture.AgentA {
		t.Fatalf("agent = %+v, want ref to %s", detail.Agent, fixture.AgentA)
	}
	if detail.UserState.UserID != fixture.UserA || detail.Conversation.ID != fixture.DirectConversationA {
		t.Fatalf("detail identity = state %+v / conversation %+v", detail.UserState, detail.Conversation)
	}

	full, err := service.FetchDirectDetail(
		ctx, principal, fixture.DirectConversationA, MessageWindow{Limit: 50},
	)
	if err != nil {
		t.Fatalf("FetchDirectDetail(limit=50): %v", err)
	}
	if len(full.Messages) != 3 || full.EarliestAvailableSeq != 1 ||
		full.Messages[0].Message.Seq != full.EarliestAvailableSeq ||
		full.Messages[len(full.Messages)-1].Message.Seq != full.ThroughSeq {
		t.Fatalf("full window = %d messages over %v, want the complete range",
			len(full.Messages), messageSeqs(full.Messages))
	}
}

func messageSeqs(views []MessageView) []int64 {
	seqs := make([]int64, len(views))
	for index, view := range views {
		seqs[index] = view.Message.Seq
	}
	return seqs
}

func TestRenameDirectPreservesChatAndRecordsPrivateCommittedHistory(t *testing.T) {
	t.Parallel()
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
	created, err := service.CreateDirect(ctx, principal, CreateDirectInput{AgentID: fixture.AgentA, RequestKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Conversation.ID
	first, err := service.RenameDirect(ctx, principal, id, RenameDirectInput{
		Title: "Planning", ExpectedRevision: created.Conversation.Revision,
	})
	if err != nil || first.Title == nil || *first.Title != "Planning" {
		t.Fatalf("rename untitled Conversation = %+v, %v", first, err)
	}
	if _, err := service.Send(ctx, principal, MessageTarget{Kind: TargetConversation, ID: id}, SendInput{
		RequestKey: uuid.New(), Content: "The first message must not replace my chosen title.",
	}); err != nil {
		t.Fatal(err)
	}
	state, err := service.FetchState(ctx, principal, id)
	if err != nil {
		t.Fatal(err)
	}
	set := true
	state, err = service.UpdateState(ctx, principal, id, UpdateUserStateInput{
		ExpectedRevision: state.Revision, Pinned: &set, Archived: &set,
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := service.FetchDirect(ctx, principal, id)
	if err != nil || before.Conversation.Title == nil || *before.Conversation.Title != "Planning" {
		t.Fatalf("title after first message = %+v, %v", before, err)
	}
	preservedState := func() []byte {
		t.Helper()
		var data []byte
		if err := pool.QueryRow(ctx, `
			SELECT jsonb_build_object(
				'conversation', to_jsonb(c) - 'title' - 'updated_at' - 'version',
				'messages', (SELECT jsonb_agg(to_jsonb(i) ORDER BY i.seq) FROM conversation_items i WHERE i.conversation_id = c.id),
				'runs', (SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM runs r WHERE r.conversation_id = c.id))
			FROM conversations c WHERE c.id = $1`, id).Scan(&data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	wantPreserved := preservedState()
	var snapshots []Snapshot
	if err := bus.Subscribe(Topic, func(_ context.Context, event events.Event) error {
		if event.Name != EventConversationUpdated {
			return nil
		}
		snapshot, err := events.PayloadOf[Snapshot](event)
		if err != nil {
			return err
		}
		var title string
		var revision int64
		if err := pool.QueryRow(ctx, `SELECT title, version FROM conversations WHERE id = $1`, id).Scan(&title, &revision); err != nil {
			t.Errorf("read committed rename in subscriber: %v", err)
		} else if snapshot.Conversation.Title == nil || title != *snapshot.Conversation.Title || revision != snapshot.Conversation.Revision {
			t.Errorf("published rename = %+v, stored title %q revision %d", snapshot, title, revision)
		}
		snapshots = append(snapshots, snapshot)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var history []activity.Entry
	if err := bus.Subscribe(activity.Topic, func(_ context.Context, event events.Event) error {
		entry, err := events.PayloadOf[activity.Entry](event)
		if err == nil {
			history = append(history, entry)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got, err := service.RenameDirect(ctx, principal, id, RenameDirectInput{
		Title: "\ufeff Release review \u202f", ExpectedRevision: state.Revision,
	})
	if err != nil || got.Title == nil || *got.Title != "Release review" || got.Revision != state.Revision+1 {
		t.Fatalf("RenameDirect() = %+v, %v, want renamed title and one revision increment", got, err)
	}
	if !bytes.Equal(preservedState(), wantPreserved) {
		t.Fatal("rename changed chat state outside title, revision or updated time")
	}
	if len(snapshots) != 1 || snapshots[0].Conversation.Kind != KindDirect ||
		snapshots[0].Conversation.CreatedByUserID == nil || *snapshots[0].Conversation.CreatedByUserID != fixture.UserA {
		t.Fatalf("rename snapshots = %+v, want one creator-private direct snapshot", snapshots)
	}
	if len(history) != 1 || !history[0].VisibilityUserID.Valid || history[0].VisibilityUserID.UUID != fixture.UserA {
		t.Fatalf("rename history = %+v, want one creator-private change", history)
	}
	var change struct {
		Before struct {
			Title    string
			Revision int64
		}
		After struct {
			Title    string
			Revision int64
		}
	}
	if err := json.Unmarshal(history[0].Details, &change); err != nil {
		t.Fatal(err)
	}
	if change.Before.Title != "Planning" || change.After.Title != "Release review" ||
		change.Before.Revision != state.Revision || change.After.Revision != got.Revision {
		t.Fatalf("rename history change = %+v", change)
	}
	replay, err := service.CreateDirect(ctx, principal, CreateDirectInput{AgentID: fixture.AgentA, RequestKey: &key})
	if err != nil || replay.Created || replay.Conversation.ID != id || replay.Conversation.Title == nil || *replay.Conversation.Title != *got.Title {
		t.Fatalf("creation replay after rename = %+v, %v", replay, err)
	}
	noop, err := service.RenameDirect(ctx, principal, id, RenameDirectInput{Title: " Release review ", ExpectedRevision: got.Revision})
	if err != nil || noop.Revision != got.Revision || !noop.UpdatedAt.Equal(got.UpdatedAt) {
		t.Fatalf("same-title rename = %+v, %v, want unchanged snapshot", noop, err)
	}
	for _, title := range []string{"Stale edit", "Release review"} {
		if _, err := service.RenameDirect(ctx, principal, id, RenameDirectInput{Title: title, ExpectedRevision: state.Revision}); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale rename %q error = %v, want %v", title, err, ErrConflict)
		}
	}
	if len(snapshots) != 1 || len(history) != 1 || !bytes.Equal(preservedState(), wantPreserved) {
		t.Fatalf("no-op/conflict published events or changed chat state: snapshots %d, history %d", len(snapshots), len(history))
	}
	var storedTitle string
	var storedRevision int64
	if err := pool.QueryRow(ctx, `SELECT title, version FROM conversations WHERE id = $1`, id).Scan(&storedTitle, &storedRevision); err != nil {
		t.Fatal(err)
	}
	if storedTitle != *got.Title || storedRevision != got.Revision {
		t.Fatalf("stored rename = %q revision %d, want %q revision %d", storedTitle, storedRevision, *got.Title, got.Revision)
	}
}

func TestRenameDirectRejectsInaccessibleConversations(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"other owner", "other admin", "foreign company", "revoked member", "deleting company", "task", "schedule", "human direct", "channel"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
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
			id := fixture.DirectConversationA
			switch name {
			case "other owner", "other admin":
				principal.UserID = fixture.UserB
				if name == "other admin" {
					principal.Role = auth.CompanyRoleAdmin
				}
				dbtest.MustExec(t, ctx, pool, `INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, $3)`, fixture.CompanyA, fixture.UserB, string(principal.Role))
			case "foreign company":
				id = fixture.UnownedConversationB
			case "revoked member":
				dbtest.MustExec(t, ctx, pool, `UPDATE company_members SET status = 'revoked' WHERE company_id = $1 AND user_id = $2`, fixture.CompanyA, fixture.UserA)
			case "deleting company":
				dbtest.MustExec(t, ctx, pool, `UPDATE companies SET deleting_at = clock_timestamp() WHERE id = $1`, fixture.CompanyA)
			case "task":
				id = fixture.ConversationA
			case "schedule":
				id = uuid.New()
				scheduleID := uuid.New()
				dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
					dbtest.MustExecTx(t, ctx, tx, `
						INSERT INTO schedules (id, company_id, conversation_id, creator_user_id, request_key,
							agent_id, reviewed_provider, name, instructions, trigger_type, run_at)
						VALUES ($1, $2, $3, $4, gen_random_uuid(), $5, 'codex', 'Digest', 'Digest', 'once', now())`,
						scheduleID, fixture.CompanyA, id, fixture.UserA, fixture.AgentA)
					dbtest.MustExecTx(t, ctx, tx, `
						INSERT INTO conversations (id, company_id, kind, schedule_id, agent_id, created_by_user_id, title)
						VALUES ($1, $2, 'schedule', $3, $4, $5, 'Digest')`,
						id, fixture.CompanyA, scheduleID, fixture.AgentA, fixture.UserA)
				})
			case "human direct":
				dbtest.MustExec(t, ctx, pool, `INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`, fixture.CompanyA, fixture.UserB)
				created, err := service.CreateHumanDirect(ctx, principal, CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{fixture.UserB}})
				if err != nil {
					t.Fatal(err)
				}
				id = created.Conversation.ID
			case "channel":
				created, err := service.CreateChannel(ctx, principal, CreateChannelInput{RequestKey: uuid.New(), Name: "Team updates"})
				if err != nil {
					t.Fatal(err)
				}
				id = created.Conversation.ID
			}
			var before, after []byte
			if err := pool.QueryRow(ctx, `SELECT to_jsonb(c) FROM conversations c WHERE id = $1`, id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			var published int
			if err := bus.SubscribeAll(func(context.Context, events.Event) error { published++; return nil }); err != nil {
				t.Fatal(err)
			}
			if _, err := service.RenameDirect(ctx, principal, id, RenameDirectInput{Title: "Forbidden rename", ExpectedRevision: 1}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("RenameDirect() error = %v, want %v", err, ErrNotFound)
			}
			if err := pool.QueryRow(ctx, `SELECT to_jsonb(c) FROM conversations c WHERE id = $1`, id).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || published != 0 {
				t.Fatalf("denied rename changed conversation or published %d events", published)
			}
		})
	}
}

func TestRenameDirectSerializesConcurrentTitles(t *testing.T) {
	t.Parallel()
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
	start := make(chan struct{})
	var workers sync.WaitGroup
	var results [2]struct {
		value Response
		err   error
	}
	for index := range results {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results[index].value, results[index].err = service.RenameDirect(ctx, principal, fixture.DirectConversationA, RenameDirectInput{
				Title: "Concurrent title " + strconv.Itoa(index), ExpectedRevision: 1,
			})
		}()
	}
	close(start)
	workers.Wait()
	var winner Response
	var successes, conflicts int
	for _, result := range results {
		switch {
		case result.err == nil:
			successes++
			winner = result.value
		case errors.Is(result.err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("concurrent rename error = %v", result.err)
		}
	}
	if successes != 1 || conflicts != 1 || winner.Revision != 2 {
		t.Fatalf("concurrent rename = %d successes, %d conflicts, winner %+v", successes, conflicts, winner)
	}
	stored, err := service.FetchDirect(ctx, principal, fixture.DirectConversationA)
	if err != nil || stored.Conversation.Title == nil || *stored.Conversation.Title != *winner.Title || stored.Conversation.Revision != winner.Revision {
		t.Fatalf("stored concurrent winner = %+v, %v, want %+v", stored, err, winner)
	}
}
