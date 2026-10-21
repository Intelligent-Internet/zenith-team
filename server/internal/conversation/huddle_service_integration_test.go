//go:build integration

package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
)

type unavailableHuddleRoom struct{}

func (unavailableHuddleRoom) EnsureRoom(context.Context, HuddleRoom) error {
	return errors.New("media unavailable")
}
func (unavailableHuddleRoom) DeleteRoom(context.Context, string) error { return nil }
func (unavailableHuddleRoom) ReadyRooms(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func (unavailableHuddleRoom) IssueParticipantToken(context.Context, string, string) (HuddleMediaToken, error) {
	return HuddleMediaToken{}, nil
}

func TestHuddleInvitationsWaitForRoomAndRemainAcceptableAfterRinging(t *testing.T) {
	f := newDiscussionFixture(t)
	direct, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewHuddleService(
		HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: unavailableHuddleRoom{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if !errors.Is(err, ErrHuddleUnavailable) {
		t.Fatalf("start error = %v, want unavailable", err)
	}
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM huddle_invitations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invitations before room readiness = %d, want 0", count)
	}

	service, err = NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	invites, err := service.ListPendingInvitations(f.ctx, f.b)
	if err != nil {
		t.Fatal(err)
	}
	if len(invites) != 1 || invites[0].HuddleID != started.Huddle.ID {
		t.Fatalf("invitations = %+v", invites)
	}
	if time.Until(invites[0].ExpiresAt) < 4*time.Minute {
		t.Fatalf("invitation expires too soon: %v", invites[0].ExpiresAt)
	}
}

func TestHuddleEndingRevokesPendingCalls(t *testing.T) {
	f := newDiscussionFixture(t)
	direct, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	roomName := HuddleRoomName(started.Huddle.ID)
	if _, err := f.pool.Exec(
		f.ctx,
		`UPDATE huddles SET room_name=$1 WHERE id=$2`,
		roomName,
		started.Huddle.ID,
	); err != nil {
		t.Fatal(err)
	}
	if err := service.EndFromMedia(f.ctx, f.a.CompanyID, roomName); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM huddle_invitations WHERE huddle_id=$1`, started.Huddle.ID).
		Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "revoked" {
		t.Fatalf("invitation status = %q, want revoked", status)
	}
}

func TestHuddleAcceptedOutsideInvitationAdmitsOnlyInvitee(t *testing.T) {
	f := newDiscussionFixture(t)
	direct, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := service.Join(f.ctx, f.a, started.Huddle.ID, JoinHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmConnected(f.ctx, f.a.CompanyID, joined.Connection.ID); err != nil {
		t.Fatal(err)
	}
	initial, err := service.ListPendingInvitations(f.ctx, f.b)
	if err != nil || len(initial) != 1 {
		t.Fatalf("initial invitations = %+v, error = %v", initial, err)
	}
	repeated, err := service.Invite(
		f.ctx,
		f.a,
		started.Huddle.ID,
		InviteHuddleRequest{InviteeUserID: f.b.UserID, RequestKey: uuid.New()},
	)
	if err != nil || repeated.ID != initial[0].ID {
		t.Fatalf("repeated invitation = %+v, error = %v", repeated, err)
	}
	if _, err := service.RespondInvitation(
		f.ctx,
		f.b,
		initial[0].ID,
		RespondHuddleInvitationRequest{Accept: true, ExpectedRevision: initial[0].Revision},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(
		f.ctx,
		`UPDATE conversation_members SET status='revoked', revoked_at=now() WHERE conversation_id=$1 AND user_id=$2`,
		direct.Conversation.ID,
		f.b.UserID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Join(
		f.ctx,
		f.b,
		started.Huddle.ID,
		JoinHuddleRequest{RequestKey: uuid.New()},
	); !errors.Is(
		err,
		ErrHuddleForbidden,
	) {
		t.Fatalf("revoked source member join error = %v, want forbidden", err)
	}
	invite, err := service.Invite(
		f.ctx,
		f.a,
		started.Huddle.ID,
		InviteHuddleRequest{InviteeUserID: f.c.UserID, RequestKey: uuid.New()},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Join(
		f.ctx,
		f.c,
		started.Huddle.ID,
		JoinHuddleRequest{RequestKey: uuid.New()},
	); !errors.Is(
		err,
		ErrHuddleForbidden,
	) {
		t.Fatalf("unaccepted invite join error = %v, want forbidden", err)
	}
	if _, err := service.RespondInvitation(
		f.ctx,
		f.c,
		invite.ID,
		RespondHuddleInvitationRequest{Accept: true, ExpectedRevision: invite.Revision},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Join(
		f.ctx,
		f.c,
		started.Huddle.ID,
		JoinHuddleRequest{RequestKey: uuid.New()},
	); err != nil {
		t.Fatalf("accepted invite join error = %v", err)
	}
	if _, err := service.ListConnections(f.ctx, f.c, started.Huddle.ID); err != nil {
		t.Fatalf("invited participant roster error = %v", err)
	}
}

func TestHuddleStartIsIdempotentAndCreatesPrivateChat(t *testing.T) {
	f := newDiscussionFixture(t)
	direct, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus})
	if err != nil {
		t.Fatal(err)
	}

	const callers = 8
	results := make(chan StartHuddleResult, callers)
	errs := make(chan error, callers)
	var group sync.WaitGroup
	for i := range callers {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			principal := f.a
			if i%2 == 1 {
				principal = f.b
			}
			result, startErr := service.Start(
				f.ctx,
				principal,
				direct.Conversation.ID,
				StartHuddleRequest{RequestKey: uuid.New()},
			)
			results <- result
			errs <- startErr
		}(i)
	}
	group.Wait()
	close(results)
	close(errs)
	for err = range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	var huddleID, chatID uuid.UUID
	created := 0
	for result := range results {
		if huddleID == uuid.Nil {
			huddleID = result.Huddle.ID
			chatID = result.Huddle.ChatConversationID
		}
		if result.Huddle.ID != huddleID || result.Huddle.ChatConversationID != chatID {
			t.Fatalf("concurrent Start() returned different huddles: %+v", result)
		}
		if result.Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created = %d, want 1", created)
	}

	var kind string
	var members, huddles int
	if err = f.pool.QueryRow(
		f.ctx,
		`SELECT kind,
			(SELECT count(*) FROM conversation_members WHERE conversation_id=$1),
			(SELECT count(*) FROM huddles WHERE source_conversation_id=$2 AND state<>'ended')
		 FROM conversations WHERE id=$1`,
		chatID,
		direct.Conversation.ID,
	).Scan(&kind, &members, &huddles); err != nil {
		t.Fatal(err)
	}
	if kind != "huddle" || members != 0 || huddles != 1 {
		t.Fatalf("chat kind/members/active huddles = %q/%d/%d", kind, members, huddles)
	}
}

func TestHuddleStartAuthorizationAndRequestReplay(t *testing.T) {
	f := newDiscussionFixture(t)
	direct, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.c.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus})
	if err != nil {
		t.Fatal(err)
	}
	request := StartHuddleRequest{RequestKey: uuid.New()}
	first, err := service.Start(f.ctx, f.a, direct.Conversation.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Start(f.ctx, f.a, direct.Conversation.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Created || replay.Huddle.ID != first.Huddle.ID {
		t.Fatalf("replay = %+v", replay)
	}
	if _, err = service.Start(
		f.ctx,
		f.a,
		other.Conversation.ID,
		request,
	); !errors.Is(
		err,
		ErrHuddleRequestConflict,
	) {
		t.Fatalf("request key reused for another source: %v", err)
	}
	if _, err = service.Start(
		f.ctx,
		f.c,
		direct.Conversation.ID,
		StartHuddleRequest{RequestKey: uuid.New()},
	); !errors.Is(err, ErrHuddleNotFound) {
		t.Fatalf("non-participant Start() error = %v", err)
	}
}

func TestHuddleJoinReservationIsIdempotentAndChecksSourceAccess(t *testing.T) {
	f := newDiscussionFixture(t)
	direct, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	request := JoinHuddleRequest{RequestKey: uuid.New()}
	first, err := service.Join(f.ctx, f.b, started.Huddle.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Join(f.ctx, f.b, started.Huddle.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := service.Join(f.ctx, f.b, started.Huddle.ID, JoinHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if first.Connection.Status != "reserved" || first.Connection.ID != replay.Connection.ID ||
		first.Connection.ID != otherKey.Connection.ID || !replay.Replayed {
		t.Fatalf("reservation/replay = %+v %+v %+v", first, replay, otherKey)
	}
	if _, err := service.Join(
		f.ctx,
		f.c,
		started.Huddle.ID,
		JoinHuddleRequest{RequestKey: uuid.New()},
	); !errors.Is(
		err,
		ErrHuddleForbidden,
	) {
		t.Fatalf("outsider Join() error = %v", err)
	}
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM huddle_connections WHERE huddle_id=$1`, started.Huddle.ID).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("reservations = %d, want 1", count)
	}
	confirmed, err := service.ConfirmConnected(f.ctx, f.a.CompanyID, first.Connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Huddle.State != HuddleStateOpen || confirmed.Connection.Status != "connected" {
		t.Fatalf("confirmed = %+v", confirmed)
	}
	reconfirmed, err := service.ConfirmConnected(f.ctx, f.a.CompanyID, first.Connection.ID)
	if err != nil || reconfirmed.Connection.ID != confirmed.Connection.ID ||
		reconfirmed.Connection.Revision != confirmed.Connection.Revision {
		t.Fatalf("reconfirmed = %+v, err=%v", reconfirmed, err)
	}
	if _, err := service.Join(
		f.ctx,
		f.b,
		started.Huddle.ID,
		JoinHuddleRequest{RequestKey: uuid.New()},
	); !errors.Is(
		err,
		ErrHuddleAlreadyConnected,
	) {
		t.Fatalf("second window Join() error = %v, want already connected", err)
	}
	sameWindow, err := service.Join(f.ctx, f.b, started.Huddle.ID, request)
	if err != nil || !sameWindow.Replayed || sameWindow.Connection.ID != first.Connection.ID {
		t.Fatalf("same window replay = %+v, err=%v", sameWindow, err)
	}
	var members int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM conversation_members WHERE conversation_id=$1 AND user_id=$2 AND source_type='huddle' AND status='active'`, started.Huddle.ChatConversationID, f.b.UserID).
		Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != 1 {
		t.Fatalf("confirmed chat members = %d, want 1", members)
	}
	left, err := service.Leave(f.ctx, f.b, started.Huddle.ID, first.Connection.ID)
	if err != nil || left.Connection.Status != "ended" {
		t.Fatalf("left = %+v, err=%v", left, err)
	}
	releft, err := service.Leave(f.ctx, f.b, started.Huddle.ID, first.Connection.ID)
	if err != nil || releft.Connection.Revision != left.Connection.Revision {
		t.Fatalf("releft = %+v, err=%v", releft, err)
	}
	reserved, err := service.Join(f.ctx, f.a, started.Huddle.ID, JoinHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	sweepCount, err := service.SweepExpiredReservations(f.ctx, time.Now().UTC().Add(time.Minute))
	if err != nil || sweepCount != 1 {
		t.Fatalf("sweep count=%d err=%v", sweepCount, err)
	}
	var status string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM huddle_connections WHERE id=$1`, reserved.Connection.ID).
		Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "revoked" {
		t.Fatalf("expired reservation status=%q", status)
	}
	rejoined, err := service.Join(f.ctx, f.b, started.Huddle.ID, JoinHuddleRequest{RequestKey: uuid.New()})
	if err != nil || rejoined.Connection.ID == first.Connection.ID || rejoined.Connection.Status != "reserved" {
		t.Fatalf("rejoin after leave = %+v, err=%v", rejoined, err)
	}
}

func TestHuddleRosterChangesNotifyCallMembers(t *testing.T) {
	f := newDiscussionFixture(t)
	type rosterChange struct {
		huddleID   uuid.UUID
		recipients []uuid.UUID
	}
	var changes []rosterChange
	if err := f.bus.Subscribe(TopicHuddle, func(_ context.Context, e events.Event) error {
		if e.Name != EventHuddleRosterChanged {
			return nil
		}
		roster, err := events.PayloadOf[HuddleRosterChangedEvent](e)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(e.Payload)
		if err != nil {
			return err
		}
		if string(raw) != `{"huddleId":"`+roster.HuddleID.String()+`"}` {
			t.Errorf("roster frame = %s, want only the Huddle id", raw)
		}
		changes = append(changes, rosterChange{roster.HuddleID, roster.RecipientUserIDs})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	expect := func(step string, want ...uuid.UUID) {
		t.Helper()
		if len(changes) != 1 {
			t.Fatalf("%s: roster changes = %d, want 1", step, len(changes))
		}
		got := changes[0].recipients
		if len(got) != len(want) {
			t.Fatalf("%s: recipients = %v, want %v", step, got, want)
		}
		for _, id := range want {
			if !slices.Contains(got, id) {
				t.Fatalf("%s: recipients = %v, want %v", step, got, want)
			}
		}
		changes = nil
	}

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
	started, err := service.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	changes = nil

	joinA, err := service.Join(f.ctx, f.a, started.Huddle.ID, JoinHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	expect("A reserves", f.a.UserID)
	if _, err := service.ConfirmConnected(f.ctx, f.a.CompanyID, joinA.Connection.ID); err != nil {
		t.Fatal(err)
	}
	expect("A connects", f.a.UserID)
	if _, err := service.ConfirmConnected(f.ctx, f.a.CompanyID, joinA.Connection.ID); err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("repeated confirmation published %d roster changes, want 0", len(changes))
	}
	if _, err := service.Join(
		f.ctx,
		f.b,
		started.Huddle.ID,
		JoinHuddleRequest{RequestKey: uuid.New()},
	); err != nil {
		t.Fatal(err)
	}
	expect("B reserves", f.a.UserID, f.b.UserID)
	if _, err := service.Leave(f.ctx, f.a, started.Huddle.ID, joinA.Connection.ID); err != nil {
		t.Fatal(err)
	}
	expect("A leaves", f.a.UserID, f.b.UserID)
	if _, err := service.SweepExpiredReservations(f.ctx, time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	expect("B's reservation expires", f.b.UserID)
}

// The call window refetches its chat on conversation.discussion_changed; the
// discussion dispatcher delivers it to the chat's current (admitted) members.
func TestHuddleChatMessageSignalsDiscussionChanged(t *testing.T) {
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
	started, err := service.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := service.Join(f.ctx, f.a, started.Huddle.ID, JoinHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ConfirmConnected(f.ctx, f.a.CompanyID, joined.Connection.ID); err != nil {
		t.Fatal(err)
	}
	var changed []uuid.UUID
	if err := f.bus.Subscribe(Topic, func(_ context.Context, e events.Event) error {
		if e.Name == EventDiscussionChanged {
			changed = append(changed, e.ConversationID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.discussion.Send(
		f.ctx,
		f.a,
		started.Huddle.ChatConversationID,
		textSend("hello from the call"),
	); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(changed, started.Huddle.ChatConversationID) {
		t.Fatalf("discussion changes = %v, want the Huddle chat %s", changed, started.Huddle.ChatConversationID)
	}
}
