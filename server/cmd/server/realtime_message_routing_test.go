package main

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/events"
)

// message.updated (ADR 0017) is an opaque "refetch this message's
// attachments" hint. It follows the conversation like every message frame:
// a Task chat's Project readers, a direct chat's creator, and nothing from the router for
// a channel or human direct message, whose current members the discussion
// dispatcher reads at delivery. A channel Agent's execution chat reaches no
// browser, as before.
func TestMessageUpdatedFollowsTheConversation(t *testing.T) {
	t.Parallel()
	bus, recorder := routedBus(t)
	companyID, creator, reader := uuid.New(), uuid.New(), uuid.New()
	for _, kind := range []conversation.Kind{
		conversation.KindTask, conversation.KindDirect, "channel", "human_direct", "channel_agent",
	} {
		publish(t, bus, companyID, conversation.TopicMessage, conversation.EventMessageUpdated,
			conversation.MessageUpdated{
				ConversationID: uuid.New(), MessageID: uuid.New(),
				ConversationKind: kind, ConversationCreatedByUserID: &creator,
				ProjectFrame: auth.ProjectFrame{Audience: &auth.ProjectAudience{UserIDs: []uuid.UUID{reader}}},
			})
	}
	want := []string{
		"company_user/" + companyID.String() + "/" + reader.String(),
		"company_user/" + companyID.String() + "/" + creator.String(),
	}
	if len(recorder.sent) != len(want) || recorder.sent[0] != want[0] || recorder.sent[1] != want[1] {
		t.Fatalf("rooms = %v, want %v", recorder.sent, want)
	}
}

// The dispatcher carries message.updated for channels and human direct
// messages, one hint per message, and leaves every other kind to the router.
func TestDiscussionDispatcherCarriesMessageUpdatesPerMessage(t *testing.T) {
	t.Parallel()
	d := newDiscussionDispatcher(nil, nil)
	companyID, conversationID := uuid.New(), uuid.New()
	updated := func(kind conversation.Kind, messageID uuid.UUID) events.Event {
		return conversation.NewMessageUpdatedEvent(uuid.New(), companyID, conversationID, messageID, kind, nil)
	}
	first, second := uuid.New(), uuid.New()
	for _, event := range []events.Event{
		updated("channel", first), updated("channel", first), updated("human_direct", second),
		updated(conversation.KindTask, uuid.New()), updated("channel_agent", uuid.New()),
	} {
		if err := d.enqueue(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	jobs := d.take(8)
	got := map[uuid.UUID]bool{}
	for _, job := range jobs {
		got[job.messageID] = true
	}
	if len(jobs) != 2 || !got[first] || !got[second] {
		t.Fatalf("dispatched message hints = %+v, want one each for %s and %s", jobs, first, second)
	}
}
