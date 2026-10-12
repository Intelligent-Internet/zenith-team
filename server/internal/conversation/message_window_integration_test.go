//go:build integration

package conversation

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// A chat page is a count of messages, and only PostgreSQL can decide which
// rows that count lands on. Under one stream a Run's reasoning, tool_call and
// tool_result items take seq numbers between the chat's, so a page cut from a
// seq range holds fewer messages than asked for — or, when the newest Run was
// busy, none at all and the panel opens blank. Both surfaces that open on the
// newest messages are covered here: the direct-chat panel and the Task
// workspace.
func TestNewestMessageWindowCountsMessagesAcrossInvisibleItems(t *testing.T) {
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
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}

	seedMessage := func(
		conversationID uuid.UUID,
		runID uuid.UUID, seq int64,
		itemType, authorKind string,
		authorID uuid.UUID,
		content string,
	) {
		t.Helper()
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO conversation_items (
				company_id, conversation_id, run_id, seq, type, author_kind, author_id, content
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			fixture.CompanyA, conversationID, runID, seq, itemType, authorKind, authorID, content)
	}
	// What a working Run writes that the chat never renders. It shares the
	// stream's one seq counter, which is the whole reason a page of chat
	// cannot be a span of seq numbers.
	//
	// Every parameter is cast: an INSERT ... SELECT gives its select list no
	// type context from the target columns, so an uncast parameter arrives as
	// text and leans on an assignment coercion to become a uuid again.
	seedRunDetail := func(conversationID uuid.UUID, runID uuid.UUID, firstSeq, lastSeq int64) {
		t.Helper()
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO conversation_items (
				company_id, conversation_id, run_id, seq, type, author_kind, author_id, content
			)
			SELECT $1::uuid, $2::uuid, $3::uuid, detail.seq,
			       'tool_call', 'agent', $4::uuid, 'read_file'
			FROM generate_series($5::bigint, $6::bigint) AS detail(seq)`,
			fixture.CompanyA, conversationID, runID, fixture.AgentA, firstSeq, lastSeq)
	}

	// An empty Conversation has nothing to walk back over, and its bounds
	// still describe an empty history rather than failing the read.
	empty, err := service.FetchDirectDetail(
		ctx, principal, fixture.DirectConversationA, MessageWindow{Limit: 10},
	)
	if err != nil {
		t.Fatalf("FetchDirectDetail(empty Conversation): %v", err)
	}
	if len(empty.Messages) != 0 || empty.ThroughSeq != 0 || empty.EarliestAvailableSeq != 1 {
		t.Fatalf("empty Conversation detail = %d Messages, through %d, earliest %d",
			len(empty.Messages), empty.ThroughSeq, empty.EarliestAvailableSeq)
	}

	directRunID := seedRun(t, ctx, pool, fixture, fixture.DirectConversationA, "completed")
	seedMessage(fixture.DirectConversationA, directRunID, 1, "user_message", "user",
		fixture.UserA, "first question")
	// Thirty items between two messages: more than a page holds, so the seq
	// range that would have covered the newest two messages covers none.
	seedRunDetail(fixture.DirectConversationA, directRunID, 2, 31)
	seedMessage(fixture.DirectConversationA, directRunID, 32, "agent_text", "agent",
		fixture.AgentA, "first answer")
	seedMessage(fixture.DirectConversationA, directRunID, 33, "user_message", "user",
		fixture.UserA, "second question")
	seedRunDetail(fixture.DirectConversationA, directRunID, 34, 38)
	seedMessage(fixture.DirectConversationA, directRunID, 39, "agent_text", "agent",
		fixture.AgentA, "second answer")
	// The newest items in the stream are invisible, so the watermark sits past
	// every message: a window ending at it must still find the messages below.
	seedRunDetail(fixture.DirectConversationA, directRunID, 40, 42)

	newest, err := service.FetchDirectDetail(
		ctx, principal, fixture.DirectConversationA, MessageWindow{Limit: 2},
	)
	if err != nil {
		t.Fatalf("FetchDirectDetail(limit=2): %v", err)
	}
	// A full page of two, in display order, and a bound that counts the whole
	// stream — the invisible tail moves the watermark but not the page.
	if !slices.Equal(messageSeqs(newest.Messages), []int64{33, 39}) || newest.ThroughSeq != 42 ||
		newest.EarliestAvailableSeq != 1 {
		t.Fatalf("newest page = %v, through %d, earliest %d, want [33 39] through 42 earliest 1",
			messageSeqs(newest.Messages), newest.ThroughSeq, newest.EarliestAvailableSeq)
	}
	if newest.Messages[0].Message.Content != "second question" ||
		newest.Messages[1].Message.Type != ItemTypeAgentText {
		t.Fatalf("newest page projections = %+v", messageContents(newest.Messages))
	}

	// A page boundary landing inside an invisible run: the third message back
	// is at seq 32 and the fourth at seq 1, with thirty tool calls between
	// them, so the cut falls on rows the page must not count or return.
	boundary, err := service.FetchDirectDetail(
		ctx, principal, fixture.DirectConversationA, MessageWindow{Limit: 3},
	)
	if err != nil {
		t.Fatalf("FetchDirectDetail(limit=3): %v", err)
	}
	if !slices.Equal(messageSeqs(boundary.Messages), []int64{32, 33, 39}) ||
		boundary.ThroughSeq != 42 {
		t.Fatalf("boundary page = %v through %d, want [32 33 39] through 42",
			messageSeqs(boundary.Messages), boundary.ThroughSeq)
	}

	// A limit that covers the history stops at the oldest message instead of
	// padding the page, and the first message sitting on EarliestAvailableSeq
	// is how the client derives that nothing older exists.
	full, err := service.FetchDirectDetail(
		ctx, principal, fixture.DirectConversationA, MessageWindow{Limit: 50},
	)
	if err != nil {
		t.Fatalf("FetchDirectDetail(limit=50): %v", err)
	}
	if !slices.Equal(messageSeqs(full.Messages), []int64{1, 32, 33, 39}) ||
		full.Messages[0].Message.Seq != full.EarliestAvailableSeq {
		t.Fatalf("full page = %v, earliest %d, want [1 32 33 39] earliest 1",
			messageSeqs(full.Messages), full.EarliestAvailableSeq)
	}

	// A bound with no floor on the detail read is a scroll-back too: the
	// newest messages at or below through_seq, not the oldest ones under it.
	// The cut falls on seq 33, so the page is the two messages at or below it.
	scrollBound := int64(33)
	scrolled, err := service.FetchDirectDetail(
		ctx, principal, fixture.DirectConversationA, MessageWindow{Limit: 2, ThroughSeq: &scrollBound},
	)
	if err != nil {
		t.Fatalf("FetchDirectDetail(through_seq=33, limit=2): %v", err)
	}
	if !slices.Equal(messageSeqs(scrolled.Messages), []int64{32, 33}) || scrolled.ThroughSeq != 42 {
		t.Fatalf("detail scroll-back = %v through %d, want [32 33] through 42",
			messageSeqs(scrolled.Messages), scrolled.ThroughSeq)
	}

	// A cursor past the head is a caller that is ahead of this read, not a
	// broken one: the transcript read answers it with an empty page, and so
	// does the chat window, with the real head so the caller can see it is
	// ahead. A scroll-back bound past the head reads from the real head.
	target := MessageTarget{Kind: TargetConversation, ID: fixture.DirectConversationA}
	ahead, err := service.ListMessages(ctx, principal, target, MessageListOptions{AfterSeq: 100, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(after_seq past the head): %v", err)
	}
	if len(ahead.Items) != 0 || ahead.ThroughSeq != 42 {
		t.Fatalf("page past the head = %v through %d, want [] through 42",
			messageSeqs(ahead.Items), ahead.ThroughSeq)
	}
	bound := int64(100)
	back, err := service.ListMessages(ctx, principal, target, MessageListOptions{
		ThroughSeq: &bound, Limit: 2, FromNewest: true,
	})
	if err != nil {
		t.Fatalf("ListMessages(through_seq past the head): %v", err)
	}
	if !slices.Equal(messageSeqs(back.Items), []int64{33, 39}) || back.ThroughSeq != 42 {
		t.Fatalf("scroll-back past the head = %v through %d, want [33 39] through 42",
			messageSeqs(back.Items), back.ThroughSeq)
	}
	forwardBound := int64(100)
	forward, err := service.ListMessages(ctx, principal, target, MessageListOptions{
		AfterSeq: 32, ThroughSeq: &forwardBound, Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListMessages(after_seq with through_seq past the head): %v", err)
	}
	if !slices.Equal(messageSeqs(forward.Items), []int64{33, 39}) || forward.ThroughSeq != 42 {
		t.Fatalf("forward page bounded past the head = %v through %d, want [33 39] through 42",
			messageSeqs(forward.Items), forward.ThroughSeq)
	}

	// The other surface that opens on the newest messages. Its Conversation
	// ends in run detail too, and it reads through the same backward page.
	seedRunDetail(fixture.ConversationA, fixture.RunA, 2, 11)
	workspace, err := ReadTaskWorkspaceInTx(
		ctx, store.Queries(), fixture.UserA, fixture.CompanyA, fixture.TaskA, 50,
	)
	if err != nil {
		t.Fatalf("ReadTaskWorkspaceInTx: %v", err)
	}
	if !slices.Equal(messageSeqs(workspace.Messages), []int64{1}) || workspace.ThroughSeq != 11 {
		t.Fatalf("Task workspace page = %v through %d, want [1] through 11",
			messageSeqs(workspace.Messages), workspace.ThroughSeq)
	}
}

// The context read's `limit` is a count of chat messages around the target,
// not the width of a seq range. Under one stream a Run's detail items take
// seq numbers between the chat's, so a range of width limit can hold one
// message or all of them; a search hit has to arrive with its neighbors
// whatever the Runs around it wrote.
func TestMessageContextCountsMessagesAroundTheTarget(t *testing.T) {
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
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	runID := seedRun(t, ctx, pool, fixture, fixture.DirectConversationA, "completed")

	// Chat at 1,2,3,4,8,10,11,15,24,26,27,31; every other seq up to 34 is
	// run detail the chat never shows.
	chatSeqs := []int64{1, 2, 3, 4, 8, 10, 11, 15, 24, 26, 27, 31}
	messageIDs := make(map[int64]uuid.UUID, len(chatSeqs))
	for seq := int64(1); seq <= 34; seq++ {
		itemType, authorKind, authorID := "tool_call", "agent", fixture.AgentA
		if slices.Contains(chatSeqs, seq) {
			itemType, authorKind, authorID = "user_message", "user", fixture.UserA
			if seq%2 == 0 {
				itemType, authorKind, authorID = "agent_text", "agent", fixture.AgentA
			}
		}
		id := dbtest.NewUUID(t)
		messageIDs[seq] = id
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO conversation_items (
				id, company_id, conversation_id, run_id, seq, type, author_kind, author_id, content
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			id, fixture.CompanyA, fixture.DirectConversationA, runID, seq,
			itemType, authorKind, authorID, "item "+strconv.FormatInt(seq, 10))
	}

	tests := []struct {
		name      string
		targetSeq int64
		limit     int
		want      []int64
	}{
		{
			name: "centered with neighbors on both sides", targetSeq: 15, limit: 6,
			want: []int64{8, 10, 11, 15, 24, 26},
		},
		{name: "the target alone", targetSeq: 15, limit: 1, want: []int64{15}},
		{name: "near the end fills backward", targetSeq: 31, limit: 6, want: []int64{11, 15, 24, 26, 27, 31}},
		{name: "near the start fills forward", targetSeq: 2, limit: 6, want: []int64{1, 2, 3, 4, 8, 10}},
		{name: "wider than the chat returns it all", targetSeq: 15, limit: 50, want: chatSeqs},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, err := service.FetchMessageContext(
				ctx, principal, fixture.DirectConversationA, messageIDs[test.targetSeq], test.limit,
			)
			if err != nil {
				t.Fatalf("FetchMessageContext(seq %d, limit %d): %v", test.targetSeq, test.limit, err)
			}
			if !slices.Equal(messageSeqs(page.Items), test.want) || page.TargetSeq != test.targetSeq ||
				page.ThroughSeq != 34 || page.EarliestAvailableSeq != 1 {
				t.Fatalf("context = %v target %d through %d earliest %d, want %v target %d",
					messageSeqs(page.Items), page.TargetSeq, page.ThroughSeq,
					page.EarliestAvailableSeq, test.want, test.targetSeq)
			}
		})
	}

	// A deep link to run detail names a row that exists and is readable and
	// still has no place on the chat surface.
	if _, err := service.FetchMessageContext(
		ctx, principal, fixture.DirectConversationA, messageIDs[5], 6,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FetchMessageContext(tool_call) error = %v, want %v", err, ErrNotFound)
	}
}

func messageContents(views []MessageView) []string {
	contents := make([]string, len(views))
	for index, view := range views {
		contents[index] = view.Message.Content
	}
	return contents
}
