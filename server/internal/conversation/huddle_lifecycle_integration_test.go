//go:build integration

package conversation

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type lifecycleHuddleMedia struct {
	ensureErr  error
	deleteErr  error
	deleteErrs map[string]error
	deleted    int
	readyCalls int
}

func (m *lifecycleHuddleMedia) EnsureRoom(context.Context, HuddleRoom) error { return m.ensureErr }
func (m *lifecycleHuddleMedia) ReadyRooms(_ context.Context, names []string) (map[string]bool, error) {
	m.readyCalls++
	ready := make(map[string]bool, len(names))
	for _, name := range names {
		ready[name] = true
	}
	return ready, nil
}

func (m *lifecycleHuddleMedia) DeleteRoom(_ context.Context, name string) error {
	m.deleted++
	if err := m.deleteErrs[name]; err != nil {
		return err
	}
	return m.deleteErr
}

func (*lifecycleHuddleMedia) IssueParticipantToken(context.Context, string, string) (HuddleMediaToken, error) {
	return HuddleMediaToken{URL: "ws://test", Token: "test"}, nil
}

func TestHuddleLifecycleClosure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		leave      bool
		leaveAge   int
		deadline   bool
		reconnect  bool
		wantClosed int
	}{
		{name: "occupied room stays open"},
		{name: "last leave retains grace", leave: true},
		{name: "empty room retains grace before ten seconds", leave: true, leaveAge: 5},
		{name: "empty grace expires after ten seconds", leave: true, leaveAge: 11, wantClosed: 1},
		{name: "reconnect cancels closure", leave: true, leaveAge: 5, reconnect: true},
		{name: "deadline closes occupied room", deadline: true, wantClosed: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDiscussionFixture(t)
			media := &lifecycleHuddleMedia{}
			service, err := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: media})
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
			started, err := service.Start(
				f.ctx,
				f.a,
				direct.Conversation.ID,
				StartHuddleRequest{RequestKey: uuid.New()},
			)
			if err != nil {
				t.Fatal(err)
			}
			joined, err := service.Join(
				f.ctx,
				f.a,
				started.Huddle.ID,
				JoinHuddleRequest{RequestKey: uuid.New()},
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.ConfirmConnected(f.ctx, f.a.CompanyID, joined.Connection.ID); err != nil {
				t.Fatal(err)
			}
			if tc.leave {
				if _, err := service.Leave(
					f.ctx,
					f.a,
					started.Huddle.ID,
					joined.Connection.ID,
				); err != nil {
					t.Fatal(err)
				}
			}
			if tc.leaveAge > 0 {
				if _, err := f.pool.Exec(
					f.ctx,
					`UPDATE huddle_connections SET ended_at=now()-($2::int * interval '1 second') WHERE id=$1`,
					joined.Connection.ID,
					tc.leaveAge,
				); err != nil {
					t.Fatal(err)
				}
			}
			if tc.reconnect {
				joined, err = service.Join(
					f.ctx,
					f.a,
					started.Huddle.ID,
					JoinHuddleRequest{RequestKey: uuid.New()},
				)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := service.ConfirmConnected(
					f.ctx,
					f.a.CompanyID,
					joined.Connection.ID,
				); err != nil {
					t.Fatal(err)
				}
			}
			if tc.deadline {
				if _, err := f.pool.Exec(
					f.ctx,
					`UPDATE huddles SET deadline_at=now()-interval '1 second' WHERE id=$1`,
					started.Huddle.ID,
				); err != nil {
					t.Fatal(err)
				}
			}
			closed, err := service.ReconcileMediaRooms(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if closed != tc.wantClosed || media.deleted != tc.wantClosed {
				t.Fatalf("closed/deleted = %d/%d, want %d", closed, media.deleted, tc.wantClosed)
			}
		})
	}
}

func TestHuddleLifecycleDeletionRetryFencesAdmission(t *testing.T) {
	f := newDiscussionFixture(t)
	media := &lifecycleHuddleMedia{deleteErr: errors.New("media temporarily unavailable")}
	service, err := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: media})
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
	reserved, err := service.Join(f.ctx, f.a, started.Huddle.ID, JoinHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(
		f.ctx,
		`UPDATE huddles SET deadline_at=now()-interval '1 second' WHERE id=$1`,
		started.Huddle.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmConnected(
		f.ctx,
		f.a.CompanyID,
		reserved.Connection.ID,
	); !errors.Is(
		err,
		ErrHuddleUnavailable,
	) {
		t.Fatalf("expired confirmation error = %v, want unavailable", err)
	}
	if _, err := service.ReconcileMediaRooms(f.ctx); !errors.Is(err, media.deleteErr) {
		t.Fatalf("reconcile error = %v, want %v", err, media.deleteErr)
	}
	if _, err := service.Join(
		f.ctx,
		f.a,
		started.Huddle.ID,
		JoinHuddleRequest{RequestKey: uuid.New()},
	); !errors.Is(
		err,
		ErrHuddleUnavailable,
	) {
		t.Fatalf("join error = %v, want unavailable", err)
	}
	if _, err := service.ConfirmConnected(
		f.ctx,
		f.a.CompanyID,
		reserved.Connection.ID,
	); !errors.Is(
		err,
		ErrHuddleUnavailable,
	) {
		t.Fatalf("late confirmation error = %v, want unavailable", err)
	}
	media.deleteErr = nil
	closed, err := service.ReconcileMediaRooms(f.ctx)
	if err != nil || closed != 1 {
		t.Fatalf("retry closed = %d, error = %v, want 1, nil", closed, err)
	}
	closed, err = service.ReconcileMediaRooms(f.ctx)
	if err != nil || closed != 0 {
		t.Fatalf("replay closed = %d, error = %v, want 0, nil", closed, err)
	}
}

func TestHuddleLifecycleEndsAbandonedRoomlessHuddle(t *testing.T) {
	f := newDiscussionFixture(t)
	media := &lifecycleHuddleMedia{ensureErr: errors.New("media unavailable")}
	service, err := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: media})
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
	if _, err := service.Start(
		f.ctx,
		f.a,
		direct.Conversation.ID,
		StartHuddleRequest{RequestKey: uuid.New()},
	); !errors.Is(
		err,
		ErrHuddleUnavailable,
	) {
		t.Fatalf("start error = %v, want unavailable", err)
	}
	var stuckID uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT id FROM huddles WHERE source_conversation_id=$1 AND state='starting' AND room_name IS NULL`, direct.Conversation.ID).
		Scan(&stuckID); err != nil {
		t.Fatal(err)
	}

	closed, err := service.ReconcileMediaRooms(f.ctx)
	if err != nil || closed != 0 {
		t.Fatalf("within grace closed = %d, error = %v, want 0, nil", closed, err)
	}
	if _, err := f.pool.Exec(
		f.ctx,
		`UPDATE huddles SET created_at=now()-interval '3 minutes' WHERE id=$1`,
		stuckID,
	); err != nil {
		t.Fatal(err)
	}
	closed, err = service.ReconcileMediaRooms(f.ctx)
	if err != nil || closed != 1 || media.deleted != 0 {
		t.Fatalf("after grace closed/deleted = %d/%d, error = %v, want 1/0, nil", closed, media.deleted, err)
	}
	var state string
	if err := f.pool.QueryRow(f.ctx, `SELECT state FROM huddles WHERE id=$1`, stuckID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(HuddleStateEnded) {
		t.Fatalf("abandoned huddle state = %q, want ended", state)
	}

	media.ensureErr = nil
	started, err := service.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if !started.Created || started.Huddle.ID == stuckID || started.Huddle.RoomName == nil {
		t.Fatalf("restart = %+v, want a new Huddle with a room", started)
	}
}

func TestHuddleLifecycleClosesHuddleNobodyJoined(t *testing.T) {
	f := newDiscussionFixture(t)
	media := &lifecycleHuddleMedia{}
	service, err := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: media})
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
	// The starter's Join never happens, so the room stays empty.
	started, err := service.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := service.ReconcileMediaRooms(f.ctx)
	if err != nil || closed != 0 {
		t.Fatalf("within grace closed = %d, error = %v, want 0, nil", closed, err)
	}
	if _, err := f.pool.Exec(
		f.ctx,
		`UPDATE huddles SET created_at=now()-interval '3 minutes' WHERE id=$1`,
		started.Huddle.ID,
	); err != nil {
		t.Fatal(err)
	}
	closed, err = service.ReconcileMediaRooms(f.ctx)
	if err != nil || closed != 1 || media.deleted != 1 {
		t.Fatalf("after grace closed/deleted = %d/%d, error = %v, want 1/1, nil", closed, media.deleted, err)
	}
	var state, invitation string
	if err := f.pool.QueryRow(f.ctx, `SELECT h.state, i.status FROM huddles h JOIN huddle_invitations i ON i.huddle_id=h.id WHERE h.id=$1`, started.Huddle.ID).
		Scan(&state, &invitation); err != nil {
		t.Fatal(err)
	}
	if state != string(HuddleStateEnded) || invitation != "revoked" {
		t.Fatalf("unjoined huddle state/invitation = %q/%q, want ended/revoked", state, invitation)
	}
}

func TestHuddleLifecycleFailureDoesNotBlockOtherRooms(t *testing.T) {
	f := newDiscussionFixture(t)
	media := &lifecycleHuddleMedia{deleteErrs: map[string]error{}}
	service, err := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: media})
	if err != nil {
		t.Fatal(err)
	}
	var huddles []Huddle
	for _, peer := range []uuid.UUID{f.b.UserID, f.c.UserID} {
		direct, err := f.conversations.CreateHumanDirect(
			f.ctx,
			f.a,
			CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{peer}},
		)
		if err != nil {
			t.Fatal(err)
		}
		started, err := service.Start(
			f.ctx,
			f.a,
			direct.Conversation.ID,
			StartHuddleRequest{RequestKey: uuid.New()},
		)
		if err != nil {
			t.Fatal(err)
		}
		huddles = append(huddles, started.Huddle)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE huddles SET deadline_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("media temporarily unavailable")
	media.deleteErrs[*huddles[0].RoomName] = failure

	closed, err := service.ReconcileMediaRooms(f.ctx)
	if !errors.Is(err, failure) || closed != 1 {
		t.Fatalf("closed = %d, error = %v, want 1, %v", closed, err, failure)
	}
	if media.readyCalls != 1 {
		t.Fatalf("room readiness calls = %d, want one batch for both rooms", media.readyCalls)
	}
	var state string
	if err := f.pool.QueryRow(f.ctx, `SELECT state FROM huddles WHERE id=$1`, huddles[1].ID).
		Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(HuddleStateEnded) {
		t.Fatalf("healthy huddle state = %q, want ended", state)
	}

	delete(media.deleteErrs, *huddles[0].RoomName)
	closed, err = service.ReconcileMediaRooms(f.ctx)
	if err != nil || closed != 1 {
		t.Fatalf("retry closed = %d, error = %v, want 1, nil", closed, err)
	}
}
