//go:build integration

package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type discussionFixture struct {
	ctx            context.Context
	pool           *pgxpool.Pool
	store          *db.Store
	bus            *events.Bus
	conversations  *DefaultConversationService
	discussion     *DefaultDiscussionService
	a, b, c, owner auth.UserPrincipal
	seed           dbtest.Fixture
}

func newDiscussionFixture(t *testing.T) discussionFixture {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seed := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewDiscussionService(
		DiscussionServiceOptions{Store: store, Bus: bus},
	)
	if err != nil {
		t.Fatal(err)
	}
	f := discussionFixture{
		ctx:           ctx,
		pool:          pool,
		store:         store,
		bus:           bus,
		conversations: newTestConversationService(t, store, bus),
		discussion:    s,
		seed:          seed,
	}
	f.a = auth.UserPrincipal{UserID: seed.UserA, CompanyID: seed.CompanyA, Role: auth.CompanyRoleAdmin}
	f.b = auth.UserPrincipal{UserID: seed.UserB, CompanyID: seed.CompanyA, Role: auth.CompanyRoleMember}
	f.c = auth.UserPrincipal{UserID: uuid.New(), CompanyID: seed.CompanyA, Role: auth.CompanyRoleMember}
	f.owner = auth.UserPrincipal{UserID: uuid.New(), CompanyID: seed.CompanyA, Role: auth.CompanyRoleOwner}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET role='admin' WHERE company_id=$1 AND user_id=$2`,
		seed.CompanyA,
		seed.UserA,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO users(id,email,display_name) VALUES($1,'c@example.test','C'),($2,'owner@example.test','Owner')`,
		f.c.UserID,
		f.owner.UserID,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member'),($1,$3,'member'),($1,$4,'owner')`,
		seed.CompanyA,
		seed.UserB,
		f.c.UserID,
		f.owner.UserID,
	)
	return f
}

func textSend(text string) DiscussionSendInput {
	return DiscussionSendInput{RequestKey: uuid.New(), Body: DiscussionBody{Version: 1, Markdown: text}}
}

func TestHumanDirectPairSendRecoveryAndPrivacy(t *testing.T) {
	f := newDiscussionFixture(t)
	const callers = 8
	results := make(chan HumanDirectResult, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, peer := f.a, f.b.UserID
			if i%2 == 1 {
				p, peer = f.b, f.a.UserID
			}
			r, e := f.conversations.CreateHumanDirect(
				f.ctx,
				p,
				CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{peer}},
			)
			results <- r
			errs <- e
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id uuid.UUID
	created := 0
	for r := range results {
		if id == uuid.Nil {
			id = r.Conversation.ID
		}
		if r.Conversation.ID != id {
			t.Fatal("duplicate DM")
		}
		if r.Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created=%d, want 1", created)
	}
	input := textSend("Xin chào **B**")
	receipt, err := f.discussion.Send(f.ctx, f.a, id, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.discussion.Send(f.ctx, f.a, id, input)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Message.ID != receipt.Message.ID || replay.HeadSeq != 1 {
		t.Fatalf("replay=%+v", replay)
	}
	input.Body.Markdown = "Changed"
	if _, err = f.discussion.Send(f.ctx, f.a, id, input); !errors.Is(err, ErrDiscussionRequestConflict) {
		t.Fatalf("changed replay=%v", err)
	}
	for _, p := range []auth.UserPrincipal{f.c, f.owner} {
		if _, err = f.conversations.FetchHumanDirect(f.ctx, p, id); !errors.Is(err, ErrDiscussionNotFound) {
			t.Fatalf("foreign DM=%v", err)
		}
		states, err := f.discussion.QueryStates(f.ctx, p, []uuid.UUID{id})
		if err != nil || len(states) != 0 {
			t.Fatalf("foreign state=%+v %v", states, err)
		}
	}
	page, err := f.discussion.ListMessages(f.ctx, f.b, id, DiscussionPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Body.Markdown != "Xin chào **B**" {
		t.Fatalf("page=%+v", page)
	}
	wire, _ := json.Marshal(page)
	for _, key := range []string{"runId", "requestKey", "queue"} {
		var payload map[string]any
		_ = json.Unmarshal(wire, &payload)
		if _, ok := payload[key]; ok {
			t.Fatalf("unexpected %s", key)
		}
	}
	hidden := true
	if _, err = f.discussion.UpdateState(
		f.ctx,
		f.b,
		id,
		DiscussionStateInput{ExpectedRevision: 0, Hidden: &hidden},
	); err != nil {
		t.Fatal(err)
	}
	list, err := f.conversations.ListHumanDirects(f.ctx, f.b, DiscussionListOptions{})
	if err != nil || len(list.Items) != 0 {
		t.Fatalf("hidden list=%+v %v", list, err)
	}
	if _, err = f.discussion.Send(f.ctx, f.a, id, textSend("New peer message")); err != nil {
		t.Fatal(err)
	}
	list, err = f.conversations.ListHumanDirects(f.ctx, f.b, DiscussionListOptions{})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("revealed list=%+v %v", list, err)
	}
	var runs, items, conversations int
	if err = f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM runs WHERE conversation_id=$1),(SELECT count(*) FROM conversation_items WHERE conversation_id=$1),(SELECT count(*) FROM conversations WHERE kind='human_direct')`, id).
		Scan(&runs, &items, &conversations); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || items != 2 || conversations != 1 {
		t.Fatalf("runs/items/conversations=%d/%d/%d", runs, items, conversations)
	}
	if _, err = f.conversations.ListMessages(
		f.ctx,
		f.a,
		MessageTarget{Kind: TargetConversation, ID: id},
		MessageListOptions{},
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("legacy history admitted DM: %v", err)
	}
	if _, err = f.conversations.Send(
		f.ctx,
		f.a,
		MessageTarget{Kind: TargetConversation, ID: id},
		SendInput{RequestKey: uuid.New(), Content: "wrong lane"},
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("legacy send admitted DM: %v", err)
	}
}

func TestChannelContentManagementAndGrantGenerations(t *testing.T) {
	f := newDiscussionFixture(t)
	input := CreateChannelInput{
		RequestKey:    uuid.New(),
		Name:          "Private group",
		MemberUserIDs: []uuid.UUID{f.b.UserID},
		AgentIDs:      []uuid.UUID{f.seed.AgentA},
	}
	created, err := f.conversations.CreateChannel(f.ctx, f.a, input)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Conversation.ID
	if _, err = f.discussion.Send(f.ctx, f.a, id, textSend("Before C joined")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.conversations.FetchChannel(f.ctx, f.c, id); !errors.Is(err, ErrDiscussionNotFound) {
		t.Fatalf("member outside=%v", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE company_members SET role='admin',version=version+1 WHERE company_id=$1 AND user_id=$2`,
		f.c.CompanyID,
		f.c.UserID,
	)
	// The stale member principal is deliberately retained; SQL's current role wins.
	meta, err := f.conversations.FetchChannelManagement(f.ctx, f.c, id)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(meta)
	var keys map[string]any
	_ = json.Unmarshal(raw, &keys)
	for _, key := range []string{"headSeq", "conversationRevision", "userState", "messageCount", "lastMessage"} {
		if _, ok := keys[key]; ok {
			t.Fatalf("management leaks %s", key)
		}
	}
	if _, err = f.discussion.ListMessages(
		f.ctx,
		f.c,
		id,
		DiscussionPageOptions{},
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatalf("admin outside history=%v", err)
	}
	if _, err = f.discussion.Send(
		f.ctx,
		f.owner,
		id,
		textSend("Owner outside roster"),
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatal(err)
	}
	states, err := f.discussion.QueryStates(f.ctx, f.owner, []uuid.UUID{id})
	if err != nil || len(states) != 0 {
		t.Fatalf("passive owner state=%+v %v", states, err)
	}
	meta, err = f.conversations.SetChannelMember(
		f.ctx,
		f.c,
		id,
		ChannelMemberInput{UserID: f.c.UserID, ExpectedRevision: meta.ConfigurationRevision, Active: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.discussion.ListMessages(f.ctx, f.c, id, DiscussionPageOptions{})
	if err != nil || len(page.Items) < 2 {
		t.Fatalf("new member history=%+v %v", page, err)
	}
	oldCursor := page.NewerCursor
	meta, err = f.conversations.SetChannelMember(
		f.ctx,
		f.a,
		id,
		ChannelMemberInput{UserID: f.c.UserID, ExpectedRevision: meta.ConfigurationRevision},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.discussion.ListMessages(
		f.ctx,
		f.c,
		id,
		DiscussionPageOptions{Cursor: oldCursor},
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatalf("revoked cursor=%v", err)
	}
	meta, err = f.conversations.SetChannelMember(
		f.ctx,
		f.a,
		id,
		ChannelMemberInput{UserID: f.c.UserID, ExpectedRevision: meta.ConfigurationRevision, Active: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.discussion.ListMessages(
		f.ctx,
		f.c,
		id,
		DiscussionPageOptions{Cursor: oldCursor},
	); !errors.Is(
		err,
		ErrDiscussionCursorExpired,
	) {
		t.Fatalf("regrant cursor=%v", err)
	}
	name := "Renamed"
	meta, err = f.conversations.UpdateChannel(
		f.ctx,
		f.a,
		id,
		ChannelUpdateInput{ExpectedRevision: meta.ConfigurationRevision, Name: &name},
	)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.conversations.CreateChannel(f.ctx, f.a, input)
	if err != nil || !replay.Replayed || replay.Conversation.Title != "Renamed" {
		t.Fatalf("creation replay=%+v %v", replay, err)
	}
	input.Name = "Changed payload"
	if _, err = f.conversations.CreateChannel(f.ctx, f.a, input); !errors.Is(err, ErrDiscussionRequestConflict) {
		t.Fatalf("creation fingerprint=%v", err)
	}
	send := textSend("Before archive")
	original, err := f.discussion.Send(f.ctx, f.b, id, send)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.conversations.SetChannelArchived(
		f.ctx,
		f.a,
		id,
		ChannelArchiveInput{ExpectedRevision: meta.ConfigurationRevision, Archived: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := f.discussion.Send(f.ctx, f.b, id, send)
	if err != nil || !receipt.Replayed || receipt.Message.ID != original.Message.ID {
		t.Fatalf("archive replay=%+v %v", receipt, err)
	}
	if _, err = f.discussion.Send(
		f.ctx,
		f.b,
		id,
		textSend("After archive"),
	); !errors.Is(
		err,
		ErrDiscussionReadOnly,
	) {
		t.Fatalf("archive write=%v", err)
	}
}

func TestDiscussionLifecycleRegrantAndEmptyRestore(t *testing.T) {
	f := newDiscussionFixture(t)
	dm, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	channel, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{RequestKey: uuid.New(), Name: "Solo", AgentIDs: []uuid.UUID{f.seed.AgentA}},
	)
	if err != nil {
		t.Fatal(err)
	}
	old, err := f.discussion.ListMessages(f.ctx, f.a, dm.Conversation.ID, DiscussionPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	err = f.store.WithinTx(f.ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := q.LockCompanyForMembershipMutation(f.ctx, f.a.CompanyID); err != nil {
			return err
		}
		if _, err := q.RevokeCompanyMember(
			f.ctx,
			generated.RevokeCompanyMemberParams{
				CompanyID:       f.a.CompanyID,
				UserID:          f.a.UserID,
				ActorUserID:     f.owner.UserID,
				ExpectedVersion: 1,
			},
		); err != nil {
			return err
		}
		var pending []events.Event
		return RevokeDiscussionMembersInTx(
			f.ctx,
			q,
			&pending,
			DiscussionUserRevocation{
				CompanyID:   f.a.CompanyID,
				UserID:      f.a.UserID,
				ActorUserID: f.owner.UserID,
			},
		)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.discussion.Send(
		f.ctx,
		f.b,
		dm.Conversation.ID,
		textSend("inactive peer"),
	); !errors.Is(
		err,
		ErrDiscussionReadOnly,
	) {
		t.Fatalf("inactive peer send=%v", err)
	}
	if _, err = f.discussion.ListMessages(
		f.ctx,
		f.a,
		dm.Conversation.ID,
		DiscussionPageOptions{Cursor: old.NewerCursor},
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatalf("revoked user=%v", err)
	}
	meta, err := f.conversations.FetchChannelManagement(f.ctx, f.owner, channel.Conversation.ID)
	if err != nil || meta.ArchivedAt == nil || len(meta.Members) != 0 {
		t.Fatalf("empty channel=%+v %v", meta, err)
	}
	restored, err := f.conversations.SetChannelArchived(
		f.ctx,
		f.owner,
		meta.ID,
		ChannelArchiveInput{ExpectedRevision: meta.ConfigurationRevision},
	)
	if err != nil || len(restored.Members) != 1 || restored.Members[0].UserID != f.owner.UserID {
		t.Fatalf("empty restore=%+v %v", restored, err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE company_members SET status='active',version=version+1 WHERE company_id=$1 AND user_id=$2`,
		f.a.CompanyID,
		f.a.UserID,
	)
	states, err := f.discussion.QueryStates(f.ctx, f.a, []uuid.UUID{dm.Conversation.ID, channel.Conversation.ID})
	if err != nil || len(states) != 0 {
		t.Fatalf("Company rejoin revived grants=%+v %v", states, err)
	}
	reopened, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.b,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.a.UserID}},
	)
	if err != nil || reopened.Conversation.ID != dm.Conversation.ID || reopened.Created {
		t.Fatalf("DM reopen=%+v %v", reopened, err)
	}
	if _, err = f.discussion.ListMessages(
		f.ctx,
		f.a,
		dm.Conversation.ID,
		DiscussionPageOptions{Cursor: old.NewerCursor},
	); !errors.Is(
		err,
		ErrDiscussionCursorExpired,
	) {
		t.Fatalf("old DM cursor=%v", err)
	}
	err = f.store.WithinTx(f.ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := q.LockCompanyForMembershipMutation(f.ctx, f.a.CompanyID); err != nil {
			return err
		}
		var pending []events.Event
		return RevokeDiscussionAgentsInTx(
			f.ctx,
			q,
			&pending,
			DiscussionAgentRevocation{CompanyID: f.a.CompanyID, AgentID: f.seed.AgentA},
		)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.Queries().
		GetDiscussionAgentHistoryGrant(f.ctx, generated.GetDiscussionAgentHistoryGrantParams{CompanyID: f.a.CompanyID, ConversationID: meta.ID, AgentID: f.seed.AgentA}); !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		t.Fatalf("removed Agent still has history: %v", err)
	}
	meta, err = f.conversations.FetchChannelManagement(f.ctx, f.owner, meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta, err = f.conversations.SetChannelAgent(
		f.ctx,
		f.owner,
		meta.ID,
		ChannelAgentInput{ExpectedRevision: meta.ConfigurationRevision, AgentID: f.seed.AgentA, Active: true},
	)
	if err != nil || len(meta.Agents) != 1 || meta.Agents[0].Generation != 2 {
		t.Fatalf("Agent regrant=%+v %v", meta, err)
	}
}

func TestChannelOwnerParticipationAndSelfRemoval(t *testing.T) {
	f := newDiscussionFixture(t)
	created, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{
			RequestKey:    uuid.New(),
			Name:          "Owner participation",
			MemberUserIDs: []uuid.UUID{f.b.UserID},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Conversation.ID
	if _, err = f.discussion.Send(
		f.ctx,
		f.owner,
		id,
		textSend("Owner outside group"),
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatalf("owner without membership send = %v", err)
	}
	if _, err = f.conversations.FetchChannel(f.ctx, f.owner, id); !errors.Is(err, ErrDiscussionNotFound) {
		t.Fatalf("owner without membership read = %v", err)
	}
	meta, err := f.conversations.FetchChannelManagement(f.ctx, f.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.conversations.SetChannelMember(f.ctx, f.owner, id, ChannelMemberInput{
		UserID: f.owner.UserID, ExpectedRevision: meta.ConfigurationRevision, Active: true,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = f.conversations.FetchChannel(f.ctx, f.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	pinned := true
	state, err := f.discussion.UpdateState(
		f.ctx,
		f.owner,
		id,
		DiscussionStateInput{ExpectedRevision: 0, Pinned: &pinned},
	)
	if err != nil {
		t.Fatal(err)
	}
	if state.PinnedAt == nil {
		t.Fatal("pin not applied")
	}
	detail, err := f.conversations.FetchChannel(f.ctx, f.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := f.conversations.SetChannelMember(
		f.ctx,
		f.b,
		id,
		ChannelMemberInput{ExpectedRevision: *detail.Conversation.ConfigurationRevision, UserID: f.b.UserID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.Members) != 2 {
		t.Fatalf("self removal receipt=%+v", removed)
	}
	if _, err = f.conversations.FetchChannel(f.ctx, f.b, id); !errors.Is(err, ErrDiscussionNotFound) {
		t.Fatalf("self removed reader=%v", err)
	}
	detail, err = f.conversations.FetchChannel(f.ctx, f.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.conversations.LeaveChannel(
		f.ctx,
		f.owner,
		id,
		*detail.Conversation.ConfigurationRevision,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = f.discussion.UpdateState(
		f.ctx,
		f.owner,
		id,
		DiscussionStateInput{
			ExpectedRevision: state.Revision,
			Pinned:           &pinned,
		},
	); !errors.Is(err, ErrDiscussionNotFound) {
		t.Fatalf("removed owner state = %v", err)
	}
	if _, err = f.discussion.ListMessages(
		f.ctx,
		f.owner,
		id,
		DiscussionPageOptions{},
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatalf("removed owner history = %v", err)
	}
}

func TestDiscussionsAvailableByDefaultAcrossCompanies(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.seed.CompanyB,
		f.a.UserID,
	)
	for _, test := range []struct {
		name      string
		principal auth.UserPrincipal
		peerID    uuid.UUID
	}{
		{name: "first company", principal: f.a, peerID: f.b.UserID},
		{
			name: "second company",
			principal: auth.UserPrincipal{
				UserID: f.b.UserID, CompanyID: f.seed.CompanyB, Role: auth.CompanyRoleOwner,
			},
			peerID: f.a.UserID,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			direct, err := f.conversations.CreateHumanDirect(
				f.ctx,
				test.principal,
				CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{test.peerID}},
			)
			if err != nil {
				t.Fatalf("CreateHumanDirect() error = %v", err)
			}
			channel, err := f.conversations.CreateChannel(f.ctx, test.principal, CreateChannelInput{
				RequestKey: uuid.New(), Name: "Team chat", MemberUserIDs: []uuid.UUID{test.peerID},
			})
			if err != nil {
				t.Fatalf("CreateChannel() error = %v", err)
			}
			for _, conversation := range []DiscussionSummary{direct.Conversation, channel.Conversation} {
				if conversation.CompanyID != test.principal.CompanyID {
					t.Fatalf(
						"CompanyID = %v, want %v",
						conversation.CompanyID,
						test.principal.CompanyID,
					)
				}
				receipt, err := f.discussion.Send(
					f.ctx,
					test.principal,
					conversation.ID,
					textSend("Hello team"),
				)
				if err != nil {
					t.Fatalf("Send(%s) error = %v", conversation.Type, err)
				}
				page, err := f.discussion.ListMessages(
					f.ctx,
					test.principal,
					conversation.ID,
					DiscussionPageOptions{},
				)
				if err != nil {
					t.Fatalf("ListMessages(%s) error = %v", conversation.Type, err)
				}
				if len(page.Items) == 0 || page.Items[len(page.Items)-1].ID != receipt.Message.ID {
					t.Fatalf(
						"ListMessages(%s) = %+v, want sent message %v",
						conversation.Type,
						page.Items,
						receipt.Message.ID,
					)
				}
				pinned := true
				state, err := f.discussion.UpdateState(
					f.ctx,
					test.principal,
					conversation.ID,
					DiscussionStateInput{
						ExpectedRevision: 0, Pinned: &pinned,
					},
				)
				if err != nil || state.PinnedAt == nil {
					t.Fatalf(
						"UpdateState(%s) = %+v, %v; want pinned",
						conversation.Type,
						state,
						err,
					)
				}
			}
		})
	}
}

func TestDiscussionMessageWindowsAndConcurrentReplay(t *testing.T) {
	f := newDiscussionFixture(t)
	created, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Conversation.ID
	empty, err := f.discussion.ListMessages(f.ctx, f.a, id, DiscussionPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if empty.NewerCursor == "" || len(empty.Items) != 0 {
		t.Fatalf("empty page=%+v", empty)
	}
	input := textSend(strings.Repeat("a", 60000))
	receipts := make(chan DiscussionReceipt, 4)
	errs := make(chan error, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := f.discussion.Send(f.ctx, f.a, id, input)
			receipts <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(receipts)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	newCount := 0
	for result := range receipts {
		if result.Message.Seq != 1 {
			t.Fatal("replay allocated a new sequence")
		}
		if !result.Replayed {
			newCount++
		}
	}
	if newCount != 1 {
		t.Fatalf("new sends=%d", newCount)
	}
	for range 9 {
		input.RequestKey = uuid.New()
		if _, err = f.discussion.Send(f.ctx, f.a, id, input); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.discussion.ListMessages(f.ctx, f.a, id, DiscussionPageOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) > 512<<10 || len(page.Items) == 0 || len(page.Items) >= 10 ||
		page.Items[len(page.Items)-1].Seq != 10 ||
		page.OlderCursor == nil {
		t.Fatalf("byte-bounded page: bytes=%d items=%d", len(wire), len(page.Items))
	}
	for i, item := range page.Items {
		if i > 0 && item.Seq <= page.Items[i-1].Seq {
			t.Fatal("unordered page")
		}
	}
	suffix, err := f.discussion.ListMessages(
		f.ctx,
		f.a,
		id,
		DiscussionPageOptions{Cursor: empty.NewerCursor, Limit: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(suffix.Items) != 3 || suffix.Items[0].Seq != 1 || suffix.Items[2].Seq != 3 || suffix.HeadSeq != 10 {
		t.Fatal("empty cursor did not catch up from observed head")
	}
	older, err := f.discussion.ListMessages(f.ctx, f.a, id, DiscussionPageOptions{Cursor: *page.OlderCursor})
	if err != nil {
		t.Fatal(err)
	}
	if older.Items[0].Seq != 1 || older.OlderCursor != nil {
		t.Fatal("older page did not exhaust history")
	}
	if _, err = f.discussion.Send(f.ctx, f.a, id, textSend("after loaded window")); err != nil {
		t.Fatal(err)
	}
	refreshed, err := f.discussion.ListMessages(f.ctx, f.a, id, DiscussionPageOptions{Cursor: older.RefreshCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshed.Items) != len(older.Items) ||
		refreshed.Items[len(refreshed.Items)-1].Seq != older.Items[len(older.Items)-1].Seq ||
		refreshed.HeadSeq != 11 {
		t.Fatal("refresh moved an older window")
	}
	if _, err = f.discussion.ListMessages(
		f.ctx,
		f.c,
		id,
		DiscussionPageOptions{Cursor: "garbage"},
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatalf("cursor decoded before ACL: %v", err)
	}
	if _, err = f.discussion.ListMessages(
		f.ctx,
		f.b,
		id,
		DiscussionPageOptions{Cursor: page.NewerCursor},
	); !errors.Is(
		err,
		ErrDiscussionInvalidCursor,
	) {
		t.Fatalf("cross-user cursor=%v", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE company_members SET version=version+1,role='member' WHERE company_id=$1 AND user_id=$2`,
		f.a.CompanyID,
		f.a.UserID,
	)
	if _, err = f.discussion.ListMessages(
		f.ctx,
		f.a,
		id,
		DiscussionPageOptions{Cursor: page.NewerCursor},
	); !errors.Is(
		err,
		ErrDiscussionCursorExpired,
	) {
		t.Fatalf("membership cursor=%v", err)
	}
	q := f.store.Queries()
	if _, err = q.GetConversationContextSnapshotHead(
		f.ctx,
		generated.GetConversationContextSnapshotHeadParams{CompanyID: f.a.CompanyID, ConversationID: id},
	); !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		t.Fatalf("legacy snapshot head admitted DM: %v", err)
	}
	snapshot, err := q.ListChatItemsForContextSnapshot(
		f.ctx,
		generated.ListChatItemsForContextSnapshotParams{
			CompanyID:      f.a.CompanyID,
			ConversationID: id,
			ItemLimit:      10,
		},
	)
	if err != nil || len(snapshot) != 0 {
		t.Fatalf("legacy snapshot admitted DM: %d %v", len(snapshot), err)
	}
}

func TestDiscussionActivityIsInternalAndHintsAreBodyless(t *testing.T) {
	f := newDiscussionFixture(t)
	activityEvents := 0
	hints := 0
	if err := f.bus.Subscribe(
		activity.Topic,
		func(context.Context, events.Event) error { activityEvents++; return nil },
	); err != nil {
		t.Fatal(err)
	}
	if err := f.bus.Subscribe(Topic, func(_ context.Context, event events.Event) error {
		raw, err := json.Marshal(event.Payload)
		if err != nil {
			return err
		}
		hints++
		if len(raw) > 1024 || strings.Contains(string(raw), "PRIVATE") ||
			strings.Contains(string(raw), "body") ||
			strings.Contains(string(raw), "markdown") {
			t.Errorf("private data in hint: %s", raw)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	created, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{RequestKey: uuid.New(), Name: "PRIVATE group"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.discussion.Send(f.ctx, f.a, created.Conversation.ID, textSend("PRIVATE body")); err != nil {
		t.Fatal(err)
	}
	if activityEvents != 0 || hints != 2 {
		t.Fatalf("public Activity=%d, bodyless hints=%d", activityEvents, hints)
	}
	var internal, public int
	if err = f.pool.QueryRow(f.ctx, `SELECT count(*) FILTER(WHERE visibility IS NULL),count(*) FILTER(WHERE visibility IS NOT NULL) FROM activity_events WHERE entity_type='conversation' AND entity_id=$1`, created.Conversation.ID.String()).
		Scan(&internal, &public); err != nil {
		t.Fatal(err)
	}
	if internal != 2 || public != 0 {
		t.Fatalf("Activity internal/public=%d/%d", internal, public)
	}
}

func TestDiscussionSendWaitsForCompanyRevocationFence(t *testing.T) {
	f := newDiscussionFixture(t)
	created, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	locked, release := make(chan struct{}), make(chan struct{})
	revocation := make(chan error, 1)
	go func() {
		revocation <- f.store.WithinTx(f.ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			if _, err := q.LockCompanyForMembershipMutation(f.ctx, f.a.CompanyID); err != nil {
				return err
			}
			close(locked)
			<-release
			if _, err := q.RevokeCompanyMember(f.ctx, generated.RevokeCompanyMemberParams{CompanyID: f.a.CompanyID, UserID: f.b.UserID, ActorUserID: f.owner.UserID, ExpectedVersion: 1}); err != nil {
				return err
			}
			var pending []events.Event
			return RevokeDiscussionMembersInTx(f.ctx, q, &pending, DiscussionUserRevocation{CompanyID: f.a.CompanyID, UserID: f.b.UserID, ActorUserID: f.owner.UserID})
		})
	}()
	select {
	case <-locked:
	case err := <-revocation:
		t.Fatalf("lock revocation: %v", err)
	}
	send := make(chan error, 1)
	go func() {
		_, err := f.discussion.Send(
			f.ctx,
			f.b,
			created.Conversation.ID,
			textSend("must not commit after revoke"),
		)
		send <- err
	}()
	wait, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	waiting := false
	for !waiting {
		if err = f.pool.QueryRow(wait, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockCompanyForMembershipRead%')`).
			Scan(&waiting); err != nil {
			close(release)
			<-revocation
			<-send
			t.Fatal(err)
		}
		if !waiting {
			select {
			case <-wait.Done():
				close(release)
				<-revocation
				<-send
				t.Fatal("send did not reach Company SHARE fence")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	close(release)
	if err = <-revocation; err != nil {
		t.Fatal(err)
	}
	if err = <-send; !errors.Is(err, ErrDiscussionNotFound) {
		t.Fatalf("send after revoke=%v", err)
	}
	var messages int
	if err = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM conversation_items WHERE conversation_id=$1`, created.Conversation.ID).
		Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if messages != 0 {
		t.Fatalf("messages after revoked send=%d", messages)
	}
}

func TestDiscussionDirectNotificationAndReplay(t *testing.T) {
	f := newDiscussionFixture(t)
	direct, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var hints []inbox.ToastHint
	if err := f.bus.Subscribe(inbox.TopicNotification, func(_ context.Context, event events.Event) error {
		hint, err := events.PayloadOf[inbox.ToastHint](event)
		if err != nil {
			return err
		}
		hints = append(hints, hint)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	input := textSend("private message content")
	receipt, err := f.discussion.Send(f.ctx, f.a, direct.Conversation.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.discussion.Send(f.ctx, f.a, direct.Conversation.ID, input); err != nil {
		t.Fatal(err)
	}
	if len(hints) != 1 {
		t.Fatalf("got %d hints, want 1", len(hints))
	}
	hint := hints[0]
	if hint.RecipientUserID != f.b.UserID || hint.DeepLink == nil ||
		!strings.Contains(*hint.DeepLink, "messageId="+receipt.Message.ID.String()) ||
		!strings.Contains(*hint.DeepLink, "discussionType=human_direct") {
		t.Fatalf("unexpected notification: %+v", hint)
	}
	if strings.Contains(hint.Body, "private message content") {
		t.Fatal("notification exposed message body")
	}
	// It names the sender instead.
	if hint.Title != "New direct message" || hint.Body != "Schema A sent you a direct message." {
		t.Fatalf("direct notice = %q / %q", hint.Title, hint.Body)
	}
}

func TestDiscussionPresenceManualAwayAndExpiry(t *testing.T) {
	f := newDiscussionFixture(t)
	rows, err := f.discussion.Presence(f.ctx, f.a, DiscussionPresenceInput{Active: true})
	if err != nil || len(rows) != 1 || !rows[0].Online {
		t.Fatalf("active presence: %+v, %v", rows, err)
	}
	away := true
	rows, err = f.discussion.Presence(f.ctx, f.a, DiscussionPresenceInput{ManualAway: &away})
	if err != nil || len(rows) != 1 || rows[0].Online || !rows[0].ManualAway {
		t.Fatalf("manual away: %+v, %v", rows, err)
	}
	rows, err = f.discussion.Presence(f.ctx, f.a, DiscussionPresenceInput{Active: true})
	if err != nil || rows[0].Online {
		t.Fatalf("heartbeat overrode manual away: %+v, %v", rows, err)
	}
	away = false
	rows, err = f.discussion.Presence(f.ctx, f.a, DiscussionPresenceInput{Active: true, ManualAway: &away})
	if err != nil || !rows[0].Online {
		t.Fatalf("set active: %+v, %v", rows, err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE company_members SET presence_active_until=now()-interval '1 second' WHERE company_id=$1 AND user_id=$2`,
		f.a.CompanyID,
		f.a.UserID,
	)
	rows, err = f.discussion.Presence(f.ctx, f.b, DiscussionPresenceInput{UserIDs: []uuid.UUID{f.a.UserID}})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.UserID == f.a.UserID && row.Online {
			t.Fatal("expired heartbeat remained online")
		}
	}
}

func TestDiscussionHereNotificationVisibility(t *testing.T) {
	f := newDiscussionFixture(t)
	created, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{
			RequestKey:    uuid.New(),
			Name:          "Presence",
			MemberUserIDs: []uuid.UUID{f.b.UserID, f.c.UserID},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Conversation.ID
	mentions := "mentions"
	for _, principal := range []auth.UserPrincipal{f.b, f.c} {
		if _, err := f.discussion.UpdateState(
			f.ctx,
			principal,
			id,
			DiscussionStateInput{NotificationLevel: &mentions},
		); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := f.discussion.Presence(f.ctx, f.b, DiscussionPresenceInput{Active: true}); err != nil {
		t.Fatal(err)
	}
	away := true
	if _, err := f.discussion.Presence(
		f.ctx,
		f.c,
		DiscussionPresenceInput{Active: true, ManualAway: &away},
	); err != nil {
		t.Fatal(err)
	}
	var hints []inbox.ToastHint
	if err := f.bus.Subscribe(inbox.TopicNotification, func(_ context.Context, e events.Event) error {
		h, err := events.PayloadOf[inbox.ToastHint](e)
		if err == nil {
			hints = append(hints, h)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	input := textSend("@here")
	input.Body.References = []json.RawMessage{
		json.RawMessage(`{"start":0,"end":5,"type":"broadcast","scope":"here"}`),
	}
	sent, err := f.discussion.Send(f.ctx, f.a, id, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(hints) != 1 || hints[0].RecipientUserID != f.b.UserID {
		t.Fatalf("here recipients: %+v", hints)
	}
	q := generated.New(f.pool)
	params := generated.GetInboxItemParams{
		CompanyID: f.a.CompanyID,
		UserID:    f.b.UserID,
		ItemID:    hints[0].NotificationID,
	}
	if _, err := q.GetInboxItem(f.ctx, params); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE conversation_members SET generation=generation+1 WHERE conversation_id=$1 AND user_id=$2`,
		id,
		f.b.UserID,
	)
	if _, err := q.GetInboxItem(f.ctx, params); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("old grant item visible: %v", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE conversation_members SET generation=generation-1 WHERE conversation_id=$1 AND user_id=$2`,
		id,
		f.b.UserID,
	)
	if _, err := f.discussion.DeleteMessage(
		f.ctx,
		f.a,
		DiscussionMessageTarget{ConversationID: id, MessageID: sent.Message.ID},
		sent.Message.Revision,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetInboxItem(f.ctx, params); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted message item visible: %v", err)
	}
}

func TestDiscussionChannelNotificationLevels(t *testing.T) {
	f := newDiscussionFixture(t)
	created, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{
			RequestKey:    uuid.New(),
			Name:          "Notifications",
			MemberUserIDs: []uuid.UUID{f.b.UserID, f.c.UserID},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Conversation.ID
	mentions, muted := "mentions", "muted"
	if _, err := f.discussion.UpdateState(
		f.ctx,
		f.b,
		id,
		DiscussionStateInput{NotificationLevel: &mentions},
	); err != nil {
		t.Fatal(err)
	}
	var hints []inbox.ToastHint
	if err := f.bus.Subscribe(inbox.TopicNotification, func(_ context.Context, e events.Event) error {
		h, err := events.PayloadOf[inbox.ToastHint](e)
		if err == nil {
			hints = append(hints, h)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.discussion.Send(f.ctx, f.a, id, textSend("ordinary post")); err != nil {
		t.Fatal(err)
	}
	if len(hints) != 1 || hints[0].RecipientUserID != f.c.UserID {
		t.Fatalf("all versus mentions: %+v", hints)
	}
	// A post that names nobody says who wrote. The channel's title never
	// enters the notice; its deep link opens the channel.
	if hints[0].Title != "New channel message" || hints[0].Body != "Schema A wrote in a channel." {
		t.Fatalf("channel notice = %q / %q", hints[0].Title, hints[0].Body)
	}
	if _, err := f.discussion.UpdateState(
		f.ctx,
		f.c,
		id,
		DiscussionStateInput{NotificationLevel: &muted},
	); err != nil {
		t.Fatal(err)
	}
	hints = nil
	input := textSend("@everyone")
	input.Body.References = []json.RawMessage{
		json.RawMessage(`{"start":0,"end":9,"type":"broadcast","scope":"everyone"}`),
	}
	if _, err := f.discussion.Send(f.ctx, f.a, id, input); err != nil {
		t.Fatal(err)
	}
	if len(hints) != 1 || hints[0].RecipientUserID != f.b.UserID {
		t.Fatalf("muted broadcast: %+v", hints)
	}
}

func TestDiscussionEditMentionNotifiesOnce(t *testing.T) {
	f := newDiscussionFixture(t)
	created, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{
			RequestKey:    uuid.New(),
			Name:          "Edit mentions",
			MemberUserIDs: []uuid.UUID{f.b.UserID, f.c.UserID},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Conversation.ID
	mentions := "mentions"
	for _, p := range []auth.UserPrincipal{f.b, f.c} {
		if _, err := f.discussion.UpdateState(
			f.ctx,
			p,
			id,
			DiscussionStateInput{NotificationLevel: &mentions},
		); err != nil {
			t.Fatal(err)
		}
	}
	sent, err := f.discussion.Send(f.ctx, f.a, id, textSend("hello"))
	if err != nil {
		t.Fatal(err)
	}
	var hints []inbox.ToastHint
	if err := f.bus.Subscribe(inbox.TopicNotification, func(_ context.Context, e events.Event) error {
		h, err := events.PayloadOf[inbox.ToastHint](e)
		if err == nil {
			hints = append(hints, h)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	body := textSend("@B @everyone").Body
	body.References = []json.RawMessage{
		json.RawMessage(fmt.Sprintf(`{"type":"user","userId":"%s","start":0,"end":2}`, f.b.UserID)),
		json.RawMessage(`{"type":"broadcast","scope":"everyone","start":3,"end":12}`),
	}
	target := DiscussionMessageTarget{ConversationID: id, MessageID: sent.Message.ID}
	edited, err := f.discussion.EditMessage(
		f.ctx,
		f.a,
		target,
		DiscussionEditInput{ExpectedRevision: sent.Message.Revision, Body: body},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(hints) != 1 || hints[0].RecipientUserID != f.b.UserID {
		t.Fatalf("edit recipients: %+v", hints)
	}
	if hints[0].Title != "New mention in chat" || hints[0].Body != "Schema A mentioned you in a channel." {
		t.Fatalf("mention notice = %q / %q", hints[0].Title, hints[0].Body)
	}
	removed, err := f.discussion.EditMessage(
		f.ctx,
		f.a,
		target,
		DiscussionEditInput{ExpectedRevision: edited.Message.Revision, Body: textSend("hello again").Body},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.discussion.EditMessage(
		f.ctx,
		f.a,
		target,
		DiscussionEditInput{ExpectedRevision: removed.Message.Revision, Body: body},
	); err != nil {
		t.Fatal(err)
	}
	if len(hints) != 1 {
		t.Fatalf("re-added mention notified again: %+v", hints)
	}
}

func TestDiscussionEditPreservesHistoricalMention(t *testing.T) {
	f := newDiscussionFixture(t)
	created, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{
			RequestKey:    uuid.New(),
			Name:          "Historical mention",
			MemberUserIDs: []uuid.UUID{f.b.UserID},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Conversation.ID
	input := textSend("@B hello")
	input.Body.References = []json.RawMessage{
		json.RawMessage(fmt.Sprintf(`{"start":0,"end":2,"type":"user","userId":%q}`, f.b.UserID)),
	}
	sent, err := f.discussion.Send(f.ctx, f.a, id, input)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE conversation_members SET status='revoked',revoked_at=now(),version=version+1 WHERE conversation_id=$1 AND user_id=$2`,
		id,
		f.b.UserID,
	)
	body := DiscussionBody{
		Version:  1,
		Markdown: "Hi @B hello",
		References: []json.RawMessage{
			json.RawMessage(fmt.Sprintf(`{"start":3,"end":5,"type":"user","userId":%q}`, f.b.UserID)),
		},
	}
	target := DiscussionMessageTarget{ConversationID: id, MessageID: sent.Message.ID}
	edited, err := f.discussion.EditMessage(
		f.ctx,
		f.a,
		target,
		DiscussionEditInput{ExpectedRevision: sent.Message.Revision, Body: body},
	)
	if err != nil {
		t.Fatalf("edit unchanged historical mention: %v", err)
	}
	body.Markdown = "Hi @C hello"
	if _, err := f.discussion.EditMessage(
		f.ctx,
		f.a,
		target,
		DiscussionEditInput{ExpectedRevision: edited.Message.Revision, Body: body},
	); !errors.Is(
		err,
		ErrDiscussionInvalid,
	) {
		t.Fatalf("renamed historical mention: %v", err)
	}
	body.Markdown = "Hi @B hello"
	body.References = []json.RawMessage{
		json.RawMessage(fmt.Sprintf(`{"start":3,"end":5,"type":"user","userId":%q}`, f.c.UserID)),
	}
	if _, err := f.discussion.EditMessage(
		f.ctx,
		f.a,
		target,
		DiscussionEditInput{ExpectedRevision: edited.Message.Revision, Body: body},
	); !errors.Is(
		err,
		ErrDiscussionInvalid,
	) {
		t.Fatalf("retargeted historical mention: %v", err)
	}
	body.References = []json.RawMessage{
		json.RawMessage(fmt.Sprintf(`{"start":3,"end":5,"type":"user","userId":%q}`, f.b.UserID)),
	}
	body.Markdown = "Hi @B @B"
	body.References = append(
		body.References,
		json.RawMessage(fmt.Sprintf(`{"start":6,"end":8,"type":"user","userId":%q}`, f.b.UserID)),
	)
	if _, err := f.discussion.EditMessage(
		f.ctx,
		f.a,
		target,
		DiscussionEditInput{ExpectedRevision: edited.Message.Revision, Body: body},
	); !errors.Is(
		err,
		ErrDiscussionInvalid,
	) {
		t.Fatalf("new mention of departed member: %v", err)
	}
}
