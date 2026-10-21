//go:build integration

package conversation

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestDiscussionReadStateSeparatesThreadsAndPreservesManualUnread(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	dm, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := dm.Conversation.ID
	root, err := f.discussion.Send(f.ctx, f.a, id, textSend("Xin chào"))
	if err != nil {
		t.Fatal(err)
	}
	replyInput := textSend("thread reply")
	replyInput.ThreadRootID = &root.Message.ID
	reply, err := f.discussion.Send(f.ctx, f.a, id, replyInput)
	if err != nil {
		t.Fatal(err)
	}
	own, err := f.discussion.Send(f.ctx, f.b, id, textSend("own message"))
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.conversations.FetchHumanDirect(f.ctx, f.b, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := *detail.Conversation.UserState.UnreadCount; got != 1 {
		t.Fatalf("root unread = %d, want 1", got)
	}
	state, err := f.discussion.UpdateReadState(
		f.ctx,
		f.b,
		id,
		DiscussionReadInput{ReadThroughSeq: &own.Message.Seq, GrantGeneration: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if *state.UnreadCount != 0 {
		t.Fatalf("read state = %+v", state)
	}
	thread, err := f.discussion.ListThread(
		f.ctx,
		f.b,
		DiscussionMessageTarget{ConversationID: id, MessageID: root.Message.ID},
		DiscussionPageOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if *thread.State.UnreadCount != 1 {
		t.Fatalf("thread unread = %v", thread.State.UnreadCount)
	}
	staleRevision := state.Revision
	state, err = f.discussion.UpdateReadState(
		f.ctx,
		f.b,
		id,
		DiscussionReadInput{
			ManualUnreadSeq:  &root.Message.Seq,
			ExpectedRevision: &state.Revision,
			GrantGeneration:  1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err = f.discussion.UpdateReadState(
		f.ctx,
		f.b,
		id,
		DiscussionReadInput{
			ReadThroughSeq:   &own.Message.Seq,
			ExpectedRevision: &staleRevision,
			GrantGeneration:  1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if state.ManualUnreadSeq == nil || *state.UnreadCount != 1 {
		t.Fatalf("late read erased manual unread: %+v", state)
	}
	older := int64(0)
	state, err = f.discussion.UpdateReadState(
		f.ctx,
		f.b,
		id,
		DiscussionReadInput{ReadThroughSeq: &older, GrantGeneration: 1},
	)
	if err != nil || state.ReadThroughSeq != own.Message.Seq {
		t.Fatalf("cursor regressed: %+v, %v", state, err)
	}
	_, err = f.discussion.UpdateReadState(
		f.ctx,
		f.owner,
		id,
		DiscussionReadInput{ReadThroughSeq: &own.Message.Seq, GrantGeneration: 1},
	)
	if !errors.Is(err, ErrDiscussionNotFound) {
		t.Fatalf("outsider read = %v", err)
	}
	_, err = f.discussion.UpdateReadState(
		f.ctx,
		f.b,
		id,
		DiscussionReadInput{ReadThroughSeq: &own.Message.Seq, GrantGeneration: 2},
	)
	if !errors.Is(err, ErrDiscussionRevisionConflict) {
		t.Fatalf("stale grant read = %v", err)
	}
	_, err = f.discussion.UpdateThreadState(
		f.ctx,
		f.b,
		DiscussionMessageTarget{ConversationID: id, MessageID: root.Message.ID},
		DiscussionThreadStateInput{ReadThroughSeq: &reply.Message.Seq, GrantGeneration: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	thread, err = f.discussion.ListThread(
		f.ctx,
		f.b,
		DiscussionMessageTarget{ConversationID: id, MessageID: root.Message.ID},
		DiscussionPageOptions{},
	)
	if err != nil || *thread.State.UnreadCount != 0 {
		t.Fatalf("thread read = %+v, %v", thread.State, err)
	}
	_, err = f.discussion.DeleteMessage(
		f.ctx,
		f.a,
		DiscussionMessageTarget{ConversationID: id, MessageID: root.Message.ID},
		root.Message.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	detail, err = f.conversations.FetchHumanDirect(f.ctx, f.b, id)
	if err != nil || *detail.Conversation.UserState.UnreadCount != 0 {
		t.Fatalf("deleted unread = %+v, %v", detail.Conversation.UserState, err)
	}
}

func TestDiscussionUnreadStartsAtRegrantAndRejectsOtherStreams(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	channel, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{RequestKey: uuid.New(), Name: "Unread", MemberUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := channel.Conversation.ID
	root, err := f.discussion.Send(f.ctx, f.a, id, textSend("old root"))
	if err != nil {
		t.Fatal(err)
	}
	replyInput := textSend("old reply")
	replyInput.ThreadRootID = &root.Message.ID
	reply, err := f.discussion.Send(f.ctx, f.a, id, replyInput)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.discussion.UpdateReadState(
		f.ctx,
		f.b,
		id,
		DiscussionReadInput{ReadThroughSeq: &reply.Message.Seq, GrantGeneration: 1},
	)
	if !errors.Is(err, ErrDiscussionInvalid) {
		t.Fatalf("reply as main cursor = %v", err)
	}
	beyond := reply.Message.Seq + 100
	_, err = f.discussion.UpdateReadState(
		f.ctx,
		f.b,
		id,
		DiscussionReadInput{ReadThroughSeq: &beyond, GrantGeneration: 1},
	)
	if !errors.Is(err, ErrDiscussionInvalid) {
		t.Fatalf("future cursor = %v", err)
	}
	meta, err := f.conversations.FetchChannelManagement(f.ctx, f.a, id)
	if err != nil {
		t.Fatal(err)
	}
	meta, err = f.conversations.SetChannelMember(
		f.ctx,
		f.a,
		id,
		ChannelMemberInput{ExpectedRevision: meta.ConfigurationRevision, UserID: f.b.UserID, Active: false},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.discussion.Send(f.ctx, f.a, id, textSend("while absent"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.conversations.SetChannelMember(
		f.ctx,
		f.a,
		id,
		ChannelMemberInput{ExpectedRevision: meta.ConfigurationRevision, UserID: f.b.UserID, Active: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.conversations.FetchChannel(f.ctx, f.b, id)
	if err != nil {
		t.Fatal(err)
	}
	if *detail.Conversation.UserState.UnreadCount != 0 ||
		detail.Conversation.UserState.ReadThroughSeq < reply.Message.Seq {
		t.Fatalf("regrant unread = %+v", detail.Conversation.UserState)
	}
	thread, err := f.discussion.ListThread(
		f.ctx,
		f.b,
		DiscussionMessageTarget{ConversationID: id, MessageID: root.Message.ID},
		DiscussionPageOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if *thread.State.UnreadCount != 0 || thread.State.ReadThroughSeq < reply.Message.Seq {
		t.Fatalf("regrant thread = %+v", thread.State)
	}
	_, err = f.discussion.UpdateReadState(
		f.ctx,
		f.b,
		id,
		DiscussionReadInput{ReadThroughSeq: &root.Message.Seq, GrantGeneration: 1},
	)
	if !errors.Is(err, ErrDiscussionRevisionConflict) {
		t.Fatalf("old grant = %v", err)
	}
	sent, err := f.discussion.Send(f.ctx, f.a, id, textSend("after regrant"))
	if err != nil {
		t.Fatal(err)
	}
	// Two devices advance in either order; the cursor never moves backwards.
	for _, seq := range []int64{sent.Message.Seq, root.Message.Seq, sent.Message.Seq} {
		state, err := f.discussion.UpdateReadState(
			f.ctx,
			f.b,
			id,
			DiscussionReadInput{ReadThroughSeq: &seq, GrantGeneration: 2},
		)
		if err != nil {
			t.Fatal(err)
		}
		if state.ReadThroughSeq != sent.Message.Seq || *state.UnreadCount != 0 {
			t.Fatalf("device read = %+v", state)
		}
	}
}
