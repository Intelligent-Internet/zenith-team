package main

import (
	"context"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/events"

	"github.com/google/uuid"
)

func TestDiscussionDispatcherRetainsHuddleInvalidation(t *testing.T) {
	t.Parallel()
	d := newDiscussionDispatcher(nil, nil)
	company, source := uuid.New(), uuid.New()
	for _, revision := range []int64{9, 2} {
		if err := d.enqueue(t.Context(), events.Event{
			EntityType: conversation.Topic, Name: conversation.EventDiscussionHuddleChanged,
			CompanyID: company, ConversationID: source,
			Payload: conversation.DiscussionHuddleChanged{ConversationID: source, HuddleID: uuid.New(), Revision: revision},
		}); err != nil {
			t.Fatal(err)
		}
	}
	jobs := d.take(8)
	if len(jobs) != 1 || jobs[0].event.ConversationID != source || jobs[0].userID != uuid.Nil {
		t.Fatalf("huddle invalidations = %+v, want one source-recipient broadcast", jobs)
	}
}

func TestDiscussionDispatcherCoalescesAndRetainsRevokeAudience(t *testing.T) {
	t.Parallel()
	d := newDiscussionDispatcher(nil, nil)
	d.capacity = 1
	company, id, user := uuid.New(), uuid.New(), uuid.New()
	changed := func(revision int64) events.Event {
		return events.Event{
			EntityType:     conversation.Topic,
			Name:           conversation.EventDiscussionChanged,
			CompanyID:      company,
			ConversationID: id,
			Payload: conversation.DiscussionChanged{
				ConversationID: id,
				Revision:       revision,
				HeadSeq:        revision,
			},
		}
	}
	if err := d.enqueue(context.Background(), changed(3)); err != nil {
		t.Fatal(err)
	}
	if err := d.enqueue(context.Background(), changed(2)); err != nil {
		t.Fatal(err)
	}
	revocation := events.Event{
		EntityType:     conversation.Topic,
		Name:           conversation.EventDiscussionAccessRevoked,
		CompanyID:      company,
		ConversationID: id,
		Payload: conversation.DiscussionAccessRevoked{
			ConversationID:  id,
			GrantGeneration: 2,
			UserID:          user,
		},
	}
	if err := d.enqueue(context.Background(), revocation); err != nil {
		t.Fatal(err)
	}
	overflow := changed(4)
	overflow.ConversationID = uuid.New()
	overflow.Payload = conversation.DiscussionChanged{ConversationID: overflow.ConversationID, Revision: 4}
	if err := d.enqueue(context.Background(), overflow); err != nil {
		t.Fatal(err)
	}
	jobs := d.take(8)
	if len(jobs) != 3 {
		t.Fatalf("jobs=%+v", jobs)
	}
	if jobs[0].event.Name != conversation.EventDiscussionAccessRevoked || jobs[0].userID != user {
		t.Fatal("revocation lost former recipient or priority")
	}
	if jobs[1].event.Name != "conversation.discussion_resync" || jobs[1].event.ConversationID != uuid.Nil {
		t.Fatalf("overflow did not request an opaque recovery: %+v", jobs[1])
	}
	payload, err := events.PayloadOf[conversation.DiscussionChanged](jobs[2].event)
	if err != nil || payload.Revision != 3 {
		t.Fatalf("coalesced=%+v %v", payload, err)
	}
}

func TestDiscussionRetryRetainsUnresolvedExecutionParent(t *testing.T) {
	d := newDiscussionDispatcher(nil, nil)
	executionID := uuid.New()
	job := discussionDispatchJob{executionConversationID: executionID, event: events.Event{
		CompanyID: uuid.New(), ConversationID: executionID, EntityType: conversation.Topic,
		Name: conversation.EventChannelAgentsChanged, Payload: conversation.ChannelAgentsChanged{},
	}}
	d.retry(job)
	if jobs := d.take(8); len(jobs) != 0 {
		t.Fatal("failed delivery retried without backoff")
	}
	d.mu.Lock()
	for key, pending := range d.changes {
		if pending.executionConversationID != executionID {
			t.Fatal("retry lost execution parent lookup")
		}
		pending.retryAt = time.Time{}
		d.changes[key] = pending
	}
	d.mu.Unlock()
	if jobs := d.take(8); len(jobs) != 1 {
		t.Fatalf("retry lost invalidation: %+v", jobs)
	}
}

func TestChannelAgentStatusHintSurvivesActivityCoalescing(t *testing.T) {
	t.Parallel()
	for _, activityFirst := range []bool{true, false} {
		d := newDiscussionDispatcher(nil, nil)
		companyID, conversationID := uuid.New(), uuid.New()
		for _, activityOnly := range []bool{activityFirst, !activityFirst} {
			err := d.enqueue(t.Context(), events.Event{
				CompanyID:      companyID,
				ConversationID: conversationID,
				EntityType:     conversation.Topic,
				Name:           conversation.EventChannelAgentsChanged,
				CreatedAt:      time.Now(),
				Actor:          events.SystemActor(),
				Payload: conversation.ChannelAgentsChanged{
					ConversationID: conversationID,
					ActivityOnly:   activityOnly,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		jobs := d.take(8)
		if len(jobs) != 1 {
			t.Fatalf("coalesced jobs = %d", len(jobs))
		}
		hint, err := events.PayloadOf[conversation.ChannelAgentsChanged](jobs[0].event)
		if err != nil || hint.ActivityOnly {
			t.Fatalf("status hint lost to transcript activity: %+v, %v", hint, err)
		}
	}
}
