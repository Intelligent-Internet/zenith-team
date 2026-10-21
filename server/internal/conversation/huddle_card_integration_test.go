//go:build integration

package conversation

import (
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestHuddleCardConcurrentChannelStartsAndEmptyCall(t *testing.T) {
	f := newDiscussionFixture(t)
	service, err := NewHuddleService(
		HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: &lifecycleHuddleMedia{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	channel, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{
			RequestKey:    uuid.New(),
			Name:          "Huddle cards",
			MemberUserIDs: []uuid.UUID{f.b.UserID},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	// Every caller returns one result; buffers let all four finish before collection.
	results := make(chan StartHuddleResult, 4)
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() {
			result, err := service.Start(
				f.ctx,
				f.a,
				channel.Conversation.ID,
				StartHuddleRequest{RequestKey: uuid.New()},
			)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var id uuid.UUID
	for result := range results {
		if id != uuid.Nil && result.Huddle.ID != id {
			t.Fatal("concurrent starts created different huddles")
		}
		id = result.Huddle.ID
	}
	// No Agent or confirmed participant is needed for a completed card.
	if err := service.EndFromMediaAnyCompany(f.ctx, HuddleRoomName(id)); err != nil {
		t.Fatal(err)
	}
	page, err := f.discussion.ListMessages(f.ctx, f.b, channel.Conversation.ID, DiscussionPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, message := range page.Items {
		if message.Huddle == nil {
			continue
		}
		count++
		if message.Huddle.ID != id || message.Huddle.State != HuddleStateEnded ||
			message.Huddle.ParticipantCount != 0 ||
			message.Huddle.DurationSeconds != 0 ||
			message.Huddle.ChatConversationID != nil {
			t.Fatalf("empty ended card = %+v", message.Huddle)
		}
		if _, err := f.pool.Exec(
			f.ctx,
			`UPDATE conversation_items SET metadata='{"action":"channel_updated","actorUserId":null}' WHERE id=$1`,
			message.ID,
		); err == nil {
			t.Fatal("immutable call card accepted a metadata rewrite")
		}
	}
	if count != 1 {
		t.Fatalf("call cards = %d, want 1", count)
	}
}

func TestHuddleCardLifecycleAndHistoryAccess(t *testing.T) {
	f := newDiscussionFixture(t)
	service, err := NewHuddleService(
		HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: &lifecycleHuddleMedia{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := StartHuddleRequest{RequestKey: uuid.New()}
	started, err := service.Start(f.ctx, f.a, direct.Conversation.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	readCard := func(p auth.UserPrincipal) DiscussionMessage {
		t.Helper()
		page, err := f.discussion.ListMessages(f.ctx, p, direct.Conversation.ID, DiscussionPageOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var cards []DiscussionMessage
		for _, message := range page.Items {
			if message.System != nil && message.System.Action == "huddle_call" {
				cards = append(cards, message)
			}
		}
		if len(cards) != 1 {
			t.Fatalf("cards = %d, want 1", len(cards))
		}
		return cards[0]
	}
	initial := readCard(f.a)
	if initial.Huddle == nil || initial.Huddle.ParticipantCount != 0 || initial.Huddle.DurationSeconds != 0 ||
		initial.Huddle.ChatConversationID != nil {
		t.Fatalf("initial card = %+v", initial.Huddle)
	}
	for _, retry := range []StartHuddleRequest{request, {RequestKey: uuid.New()}} {
		if _, err := service.Start(f.ctx, f.a, direct.Conversation.ID, retry); err != nil {
			t.Fatal(err)
		}
		if got := readCard(f.a).ID; got != initial.ID {
			t.Fatalf("card id = %v, want %v", got, initial.ID)
		}
	}
	// Two confirmed connections from one person count once; a reservation does not count.
	for range 2 {
		joined, err := service.Join(f.ctx, f.a, started.Huddle.ID, JoinHuddleRequest{RequestKey: uuid.New()})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.ConfirmConnected(f.ctx, f.a.CompanyID, joined.Connection.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Leave(f.ctx, f.a, started.Huddle.ID, joined.Connection.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Join(
		f.ctx,
		f.b,
		started.Huddle.ID,
		JoinHuddleRequest{RequestKey: uuid.New()},
	); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_connections SET connected_at=now()-interval '2 minutes',ended_at=now()-interval '30 seconds' WHERE huddle_id=$1 AND connected_at IS NOT NULL`,
		started.Huddle.ID,
	)
	if err := service.EndFromMedia(f.ctx, f.a.CompanyID, HuddleRoomName(started.Huddle.ID)); err != nil {
		t.Fatal(err)
	}
	if err := service.EndFromMediaAnyCompany(f.ctx, HuddleRoomName(started.Huddle.ID)); err != nil {
		t.Fatal(err)
	}
	ended := readCard(f.a)
	if ended.ID != initial.ID || ended.Huddle.State != HuddleStateEnded || ended.Huddle.ParticipantCount != 1 ||
		ended.Huddle.DurationSeconds != 90 {
		t.Fatalf("ended card = %+v", ended.Huddle)
	}
	if ended.Huddle.ChatConversationID == nil ||
		*ended.Huddle.ChatConversationID != started.Huddle.ChatConversationID {
		t.Fatalf("participant history link = %+v", ended.Huddle)
	}
	if got := readCard(f.b); got.Huddle.ChatConversationID != nil {
		t.Fatal("reservation granted history access")
	}
	if _, err := f.discussion.ListMessages(
		f.ctx,
		f.b,
		started.Huddle.ChatConversationID,
		DiscussionPageOptions{},
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatalf("nonparticipant history error = %v, want not found", err)
	}
	if _, err := f.discussion.ListMessages(
		f.ctx,
		f.c,
		direct.Conversation.ID,
		DiscussionPageOptions{},
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatalf("outsider source error = %v, want not found", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE conversation_members SET status='revoked',revoked_at=now() WHERE conversation_id=$1 AND user_id=$2`,
		started.Huddle.ChatConversationID,
		f.a.UserID,
	)
	if got := readCard(f.a); got.Huddle.ChatConversationID != nil {
		t.Fatal("revoked participant retained history link")
	}
}
