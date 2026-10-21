package realtime

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

// An Agent's cursor goes five seconds after its last tool call; a browser
// stays fifteen seconds after its last heartbeat.
func TestCanvasPresenceExpiresAgentsSoonAfterTheirLastCall(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		idle                     time.Duration
		wantAgents, wantBrowsers int
	}{
		{idle: 3 * time.Second, wantAgents: 1, wantBrowsers: 1},
		{idle: 6 * time.Second, wantAgents: 0, wantBrowsers: 1},
		{idle: 20 * time.Second, wantAgents: 0, wantBrowsers: 0},
	} {
		presence := NewCanvasPresence(nil)
		run := uuid.New()
		room := "company:whiteboard:board"
		for _, session := range []CanvasSession{
			{SessionID: "browser", UserID: "a"},
			{SessionID: run.String(), UserID: "a", RunID: &run},
		} {
			if _, err := presence.Update(t.Context(), room, session, false); err != nil {
				t.Fatal(err)
			}
		}
		stale := time.Now().Add(-tc.idle).UnixMilli()
		for field, session := range presence.rooms[room] {
			session.UpdatedAt = stale
			presence.rooms[room][field] = session
		}
		other := CanvasSession{SessionID: "other", UserID: "b"}
		sessions, err := presence.Update(t.Context(), room, other, false)
		if err != nil {
			t.Fatal(err)
		}
		var agents, browsers int
		for _, session := range sessions {
			if session.RunID != nil {
				agents++
			} else if session.SessionID == "browser" {
				browsers++
			}
		}
		if agents != tc.wantAgents || browsers != tc.wantBrowsers {
			t.Fatalf("after %v idle: %d Agents, %d browsers; want %d, %d",
				tc.idle, agents, browsers, tc.wantAgents, tc.wantBrowsers)
		}
	}
}

// One person cannot crowd a room: their oldest session gives way, and a room
// full of other people answers with a sentinel the HTTP layer maps.
func TestCanvasPresenceBoundsSessionsPerPersonAndRoom(t *testing.T) {
	t.Parallel()
	assertCanvasPresenceBounds(t, NewCanvasPresence(nil))
}

func assertCanvasPresenceBounds(t *testing.T, presence *CanvasPresence) {
	t.Helper()
	ctx := t.Context()
	room := "company:whiteboard:" + uuid.NewString()
	var sessions []CanvasSession
	var err error
	for i := range canvasPresenceSessionsPerUser + 1 {
		sessions, err = presence.Update(
			ctx,
			room,
			CanvasSession{SessionID: fmt.Sprint("tab-", i), UserID: "a"},
			false,
		)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(sessions) != canvasPresenceSessionsPerUser || slices.ContainsFunc(sessions, func(s CanvasSession) bool {
		return s.SessionID == "tab-0"
	}) {
		t.Fatalf("sessions = %+v; want the oldest tab replaced", sessions)
	}
	listed, err := presence.Sessions(ctx, room)
	if err != nil || len(listed) != canvasPresenceSessionsPerUser {
		t.Fatalf("listed sessions = %+v, %v", listed, err)
	}
	for i := range canvasPresenceRoomSessions - canvasPresenceSessionsPerUser {
		if _, err = presence.Update(
			ctx,
			room,
			CanvasSession{SessionID: "tab", UserID: fmt.Sprint("user-", i)},
			false,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = presence.Update(
		ctx,
		room,
		CanvasSession{SessionID: "tab", UserID: "late"},
		false,
	); !errors.Is(
		err,
		ErrCanvasPresenceFull,
	) {
		t.Fatalf("full room = %v, want ErrCanvasPresenceFull", err)
	}
}

// An Agent's session stays while it works, through reads and edits that are
// not presence calls, and leaves as soon as its Run ends.
func TestCanvasPresenceFollowsAgentRuns(t *testing.T) {
	t.Parallel()
	assertCanvasPresenceFollowsRuns(t, NewCanvasPresence(nil))
}

func assertCanvasPresenceFollowsRuns(t *testing.T, presence *CanvasPresence) {
	t.Helper()
	ctx := t.Context()
	run := uuid.New()
	rooms := []string{"company:whiteboard:" + uuid.NewString(), "company:whiteboard:" + uuid.NewString()}
	for _, room := range rooms {
		if _, err := presence.Update(
			ctx,
			room,
			CanvasSession{SessionID: run.String(), UserID: "member", RunID: &run},
			false,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := presence.Update(
			ctx,
			room,
			CanvasSession{SessionID: "tab", UserID: "member"},
			false,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := presence.RemoveRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	for _, room := range rooms {
		sessions, err := presence.Sessions(ctx, room)
		if err != nil || len(sessions) != 1 || sessions[0].RunID != nil {
			t.Fatalf(
				"room %s after the Run ended = %+v, %v; want only the member's tab",
				room,
				sessions,
				err,
			)
		}
	}
}

// An Agent working for a person is not one of that person's tabs: it never
// pushes their oldest tab out of the room, and their tabs never push it out.
func TestCanvasPresenceKeepsAgentsOutOfThePersonalLimit(t *testing.T) {
	t.Parallel()
	assertCanvasPresenceAgentsOutsidePersonalLimit(t, NewCanvasPresence(nil))
}

func assertCanvasPresenceAgentsOutsidePersonalLimit(t *testing.T, presence *CanvasPresence) {
	t.Helper()
	ctx := t.Context()
	room := "company:whiteboard:" + uuid.NewString()
	run := uuid.New()
	for i := range canvasPresenceSessionsPerUser {
		if _, err := presence.Update(
			ctx,
			room,
			CanvasSession{SessionID: fmt.Sprint("tab-", i), UserID: "a"},
			false,
		); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	sessions, err := presence.Update(
		ctx,
		room,
		CanvasSession{SessionID: run.String(), UserID: "a", RunID: &run},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != canvasPresenceSessionsPerUser+1 {
		t.Fatalf("sessions after the Agent joined = %+v; want every tab kept", sessions)
	}
	sessions, err = presence.Update(ctx, room, CanvasSession{SessionID: "tab-late", UserID: "a"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != canvasPresenceSessionsPerUser+1 ||
		!slices.ContainsFunc(sessions, func(s CanvasSession) bool { return s.RunID != nil }) ||
		slices.ContainsFunc(sessions, func(s CanvasSession) bool { return s.SessionID == "tab-0" }) {
		t.Fatalf("sessions after a fifth tab = %+v; want the oldest tab replaced and the Agent kept", sessions)
	}
}

// The area a person last looked at outlives their session for a while, so an
// Agent can still work where they were after they switch tabs.
func TestCanvasPresenceRemembersTheLastView(t *testing.T) {
	t.Parallel()
	assertCanvasPresenceRemembersLastView(t, NewCanvasPresence(nil))
}

func assertCanvasPresenceRemembersLastView(t *testing.T, presence *CanvasPresence) {
	t.Helper()
	ctx := t.Context()
	room := "company:whiteboard:" + uuid.NewString()
	view := &CanvasRect{X: -120, Y: 40.5, Width: 1600, Height: 900}
	session := CanvasSession{SessionID: "tab", UserID: "a", View: view}
	if _, err := presence.Update(ctx, room, session, false); err != nil {
		t.Fatal(err)
	}
	if _, err := presence.Update(ctx, room, session, true); err != nil {
		t.Fatal(err)
	}
	got, err := presence.LastView(ctx, room, "a")
	if err != nil || got == nil || *got != *view {
		t.Fatalf("last view = %+v, %v; want %+v", got, err, view)
	}
	if other, err := presence.LastView(ctx, room, "b"); err != nil || other != nil {
		t.Fatalf("another person's last view = %+v, %v; want none", other, err)
	}
	run := uuid.New()
	agent := CanvasSession{
		SessionID: run.String(),
		UserID:    "a",
		RunID:     &run,
		View:      &CanvasRect{Width: 1, Height: 1},
	}
	if _, err := presence.Update(ctx, room, agent, false); err != nil {
		t.Fatal(err)
	}
	if got, err = presence.LastView(ctx, room, "a"); err != nil || got == nil || *got != *view {
		t.Fatalf("last view after an Agent update = %+v, %v; want the person's %+v", got, err, view)
	}
}
