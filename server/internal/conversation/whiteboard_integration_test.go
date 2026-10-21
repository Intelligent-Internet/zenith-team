//go:build integration

package conversation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// A message can make a Whiteboard: an Artifact its sender owns, shared into
// the chat like any pushed Artifact, so the chat's people can view it and only
// a grant lets them edit. A retry replays the message without a second
// Whiteboard; a thread reply and a Huddle chat make none.
func TestDiscussionMessageMakesAWhiteboard(t *testing.T) {
	f := newDiscussionFixture(t)
	group, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{RequestKey: uuid.New(), Name: "Design", MemberUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	input := DiscussionSendInput{
		RequestKey: uuid.New(),
		Body:       DiscussionBody{Version: 1},
		Whiteboard: &DiscussionWhiteboard{Title: "  Release plan  "},
	}
	sent, err := f.discussion.Send(f.ctx, f.a, group.Conversation.ID, input)
	if err != nil || len(sent.Message.Artifacts) != 1 || sent.Message.Artifacts[0].Type != "whiteboard" ||
		sent.Message.Artifacts[0].Title != "Release plan" {
		t.Fatalf("send = %+v, %v; want one Whiteboard Artifact", sent, err)
	}
	board := sent.Message.Artifacts[0].ID
	access := func(userID uuid.UUID) int {
		t.Helper()
		var level int
		if err := f.pool.QueryRow(f.ctx, `SELECT COALESCE((SELECT access_level FROM output_access
			WHERE output_id=$1 AND user_id=$2),0)`, board, userID).Scan(&level); err != nil {
			t.Fatal(err)
		}
		return level
	}
	if owner, member, outsider := access(f.a.UserID), access(f.b.UserID), access(f.c.UserID); owner != 3 ||
		member != 1 || outsider != 0 {
		t.Fatalf("access owner %d, member %d, outsider %d; want 3, 1, 0", owner, member, outsider)
	}
	var boards int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM boards WHERE id=$1`, board).Scan(&boards); err != nil ||
		boards != 1 {
		t.Fatalf("Boards = %d, %v; want the Whiteboard's scene", boards, err)
	}
	replay, err := f.discussion.Send(f.ctx, f.a, group.Conversation.ID, input)
	if err != nil || !replay.Replayed || replay.Message.ID != sent.Message.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	var whiteboards int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM outputs WHERE type='whiteboard' AND conversation_id=$1`,
		group.Conversation.ID).Scan(&whiteboards); err != nil || whiteboards != 1 {
		t.Fatalf("Whiteboards after a retry = %d, %v; want one", whiteboards, err)
	}
	reply := DiscussionSendInput{
		RequestKey:   uuid.New(),
		Body:         DiscussionBody{Version: 1},
		ThreadRootID: &sent.Message.ID,
		Whiteboard:   &DiscussionWhiteboard{Title: "In a thread"},
	}
	if _, err = f.discussion.Send(f.ctx, f.a, group.Conversation.ID, reply); !errors.Is(err, ErrDiscussionInvalid) {
		t.Fatalf("Whiteboard in a thread = %v, want invalid", err)
	}
	blank := DiscussionSendInput{
		RequestKey: uuid.New(),
		Body:       DiscussionBody{Version: 1},
		Whiteboard: &DiscussionWhiteboard{Title: "   "},
	}
	if _, err = f.discussion.Send(f.ctx, f.a, group.Conversation.ID, blank); !errors.Is(err, ErrDiscussionInvalid) {
		t.Fatalf("Whiteboard without a title = %v, want invalid", err)
	}
}

// A Whiteboard made in an Agent or Task chat belongs to its maker and to the
// chat, and the chat gets no message: a turn there would ask the Agent. The
// route names no Company, so the chat's own decides it.
func TestCreateWhiteboardInAgentAndTaskChats(t *testing.T) {
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
	maker := auth.UserPrincipal{UserID: fixture.UserA}
	items := func() int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM conversation_items WHERE conversation_id = ANY($1)`,
			[]uuid.UUID{fixture.DirectConversationA, fixture.ConversationA}).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	before := items()
	for _, chat := range []struct {
		target       MessageTarget
		conversation uuid.UUID
	}{
		{MessageTarget{Kind: TargetConversation, ID: fixture.DirectConversationA}, fixture.DirectConversationA},
		{MessageTarget{Kind: TargetTask, ID: fixture.TaskA}, fixture.ConversationA},
	} {
		created, err := service.CreateWhiteboard(ctx, maker, chat.target, "  Sprint plan  ")
		if err != nil {
			t.Fatalf("CreateWhiteboard(%+v) = %v", chat.target, err)
		}
		if created.CompanyID != fixture.CompanyA {
			t.Fatalf("made in Company %s; want %s", created.CompanyID, fixture.CompanyA)
		}
		var title, kind string
		var owner, conversationID uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT o.title, o.type, o.owner_user_id, o.conversation_id
			FROM outputs o JOIN boards b ON b.company_id = o.company_id AND b.id = o.id WHERE o.id = $1`,
			created.ArtifactID).Scan(&title, &kind, &owner, &conversationID); err != nil {
			t.Fatalf("read the new Whiteboard: %v", err)
		}
		if title != "Sprint plan" || kind != "whiteboard" || owner != fixture.UserA ||
			conversationID != chat.conversation {
			t.Fatalf("Whiteboard = %q %s owner %s chat %s", title, kind, owner, conversationID)
		}
	}
	if after := items(); after != before {
		t.Fatalf("chat items %d -> %d; want no message", before, after)
	}
	direct := MessageTarget{Kind: TargetConversation, ID: fixture.DirectConversationA}
	if _, err := service.CreateWhiteboard(ctx, maker, direct, "   "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank title error = %v; want %v", err, ErrInvalidInput)
	}
	stranger := auth.UserPrincipal{UserID: fixture.UserB}
	if _, err := service.CreateWhiteboard(ctx, stranger, direct, "Theirs"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else's Agent chat error = %v; want %v", err, ErrNotFound)
	}
}
