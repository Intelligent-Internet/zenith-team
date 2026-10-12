package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/events"
)

// recorded is one delivered event with its payload already re-typed. The
// embedded envelope keeps Name and Actor readable while Payload shadows the
// any-typed field with the projection the subscriber asked for.
type recorded[P any] struct {
	events.Event
	Payload P
}

// topicLog collects what one domain's subscriber was handed. A event event is
// opaque by design — holding one proves only that it passed staging — so these
// tests read an event where everything else reads it: at a subscriber, after
// publication.
type topicLog[P any] struct {
	mu     sync.Mutex
	events []recorded[P]
}

// recordTopic subscribes once to a domain and keeps every event it publishes,
// as the one subscription per domain the bus expects. A payload that is not
// the shape the test asked for is a mismatch the handler reports rather than
// skips.
func recordTopic[P any](t *testing.T, bus *events.Bus, topic string) *topicLog[P] {
	t.Helper()
	log := &topicLog[P]{}
	if err := bus.Subscribe(topic,
		func(_ context.Context, event events.Event) error {
			payload, err := events.PayloadOf[P](event)
			if err != nil {
				t.Errorf("payload of %s: %v", event.Name, err)
				return err
			}
			log.mu.Lock()
			defer log.mu.Unlock()
			log.events = append(log.events, recorded[P]{Event: event, Payload: payload})
			return nil
		}); err != nil {
		t.Fatalf("subscribe to %s: %v", topic, err)
	}
	return log
}

func (l *topicLog[P]) snapshot() []recorded[P] {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]recorded[P](nil), l.events...)
}

func TestMessageCreatedEventCarriesCanonicalProjectionAndRouting(t *testing.T) {
	t.Parallel()

	actorID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	now := time.Date(2026, time.July, 13, 4, 5, 6, 0, time.UTC)
	message := MessageResponse{
		ID: uuid.Must(uuid.NewV7()), ConversationID: conversationID, Seq: 7,
		Type:      ItemTypeUserMessage,
		Author:    ActorRef{Kind: ActorUser, ID: actorID, DisplayName: "Dana"},
		RunID:     uuid.MustParse("00000000-0000-4000-8000-000000000091"),
		Content:   "Ship the release",
		CreatedAt: now,
	}
	view := MessageView{Message: message}
	value := Response{
		ID: conversationID, CompanyID: companyID, Kind: KindDirect,
		CreatedByUserID: &actorID,
		CreatedAt:       now, UpdatedAt: now, Revision: 1,
	}

	bus := newTestBus(t)
	received := recordTopic[PostedMessage](t, bus, TopicMessage)
	built := newMessageCreatedEvent(actorID, value, view)
	bus.Publish(context.Background(), built.EntityType, built)

	published := received.snapshot()
	if len(published) != 1 {
		t.Fatalf("published message.created events = %d, want 1", len(published))
	}
	event := published[0]
	if event.Name != EventMessageCreated || event.Actor != events.UserActor(actorID.String()) {
		t.Fatalf("message.created envelope = %+v", event)
	}
	// The consumer that delivers reads these off the Go value; the client
	// never sees them.
	if event.Payload.CompanyID != companyID ||
		event.Payload.ConversationKind != KindDirect ||
		event.Payload.ConversationCreatedByUserID == nil ||
		*event.Payload.ConversationCreatedByUserID != actorID {
		t.Fatalf("message.created routing = %#v", event.Payload)
	}
	encoded, err := json.Marshal(event.Payload)
	if err != nil {
		t.Fatalf("marshal message.created payload: %v", err)
	}
	want := fmt.Sprintf(
		`{"id":%q,"conversationId":%q,"seq":7,"type":"user_message",`+
			`"author":{"id":%q,"kind":"user","name":"Dana","avatarUrl":null},`+
			`"runId":"00000000-0000-4000-8000-000000000091","content":"Ship the release","createdAt":"2026-07-13T04:05:06Z","attachments":[],"attachmentsWithheld":false}`,
		message.ID,
		conversationID,
		actorID,
	)
	if string(encoded) != want {
		t.Fatalf("message.created payload = %s, want %s", encoded, want)
	}
}

// A file on the message that is an active Project file follows its Projects'
// readers, not the frame's audience (ADR 0017): the frame then carries no
// attachment at all, not even the ones the sender could open, and says so.
func TestMessageCreatedCarriesNoAttachmentsWhenOneIsAProjectFile(t *testing.T) {
	t.Parallel()

	actorID, companyID := uuid.New(), uuid.New()
	view := MessageView{
		Message: MessageResponse{ID: uuid.New(), ConversationID: uuid.New(), Type: ItemTypeUserMessage},
		Attachments: []attachment.Response{
			{ID: uuid.New(), Filename: "launch-plan.pdf"},
			{ID: uuid.New(), Filename: "notes.txt"},
		},
		carriesProjectFile: true,
	}
	event := NewMemberMessageCreatedEvent(actorID, companyID, KindTask, nil, view)
	encoded, err := json.Marshal(event.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "launch-plan.pdf") || strings.Contains(string(encoded), "notes.txt") ||
		!strings.Contains(string(encoded), `"attachments":[],"attachmentsWithheld":true`) {
		t.Fatalf("message.created payload = %s, want no attachments and attachmentsWithheld", encoded)
	}
	view.carriesProjectFile = false
	encoded, err = json.Marshal(NewMemberMessageCreatedEvent(actorID, companyID, KindTask, nil, view).Payload)
	if err != nil || !strings.Contains(string(encoded), "notes.txt") ||
		!strings.Contains(string(encoded), `"attachmentsWithheld":false`) {
		t.Fatalf("message.created payload without a Project file = %s, %v", encoded, err)
	}
}

func TestDirectConversationEventsCarryCreatorRouting(t *testing.T) {
	t.Parallel()

	actorID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	title := "Release agent"
	now := time.Date(2026, time.July, 13, 4, 5, 6, 0, time.UTC)
	value := Response{
		ID: uuid.Must(uuid.NewV7()), CompanyID: companyID, Kind: KindDirect,
		AgentID:         &agentID,
		CreatedByUserID: &actorID,
		Title:           &title,
		CreatedAt:       now, UpdatedAt: now, Revision: 1,
	}

	bus := newTestBus(t)
	familyLog := recordTopic[Snapshot](t, bus, Topic)
	built := newConversationEvent(
		actorID, nil, value, EventConversationCreated, nil,
	)
	bus.Publish(context.Background(), built.EntityType, built)

	createdEvents := familyLog.snapshot()
	if len(createdEvents) != 1 {
		t.Fatalf("published conversation.created events = %d, want 1", len(createdEvents))
	}
	created := createdEvents[0]
	if created.Name != EventConversationCreated ||
		created.Actor != events.UserActor(actorID.String()) {
		t.Fatalf("direct conversation.created envelope = %+v", created)
	}
	// conversation.created and conversation.updated share one payload shape:
	// the created event carries the zero-activity summary a fresh chat has.
	createdPayload := created.Payload
	if createdPayload.Conversation.Kind != KindDirect ||
		createdPayload.Conversation.CompanyID != companyID ||
		createdPayload.Conversation.CreatedByUserID == nil ||
		*createdPayload.Conversation.CreatedByUserID != actorID {
		t.Fatalf("conversation.created payload = %#v", createdPayload)
	}
	if createdPayload.Summary.UpdatedAt != value.UpdatedAt ||
		createdPayload.Summary.LastMessage != nil ||
		createdPayload.Summary.Runs != (RunsSummary{}) {
		t.Fatalf("conversation.created summary = %#v", createdPayload.Summary)
	}
	encoded, err := json.Marshal(createdPayload)
	if err != nil {
		t.Fatalf("marshal conversation.created payload: %v", err)
	}
	// lastMessage is explicit null, never absent, so a client cache-writes one
	// shape; the conversation carries its own taskId.
	for _, fragment := range []string{`"lastMessage":null`, `"taskId":null`} {
		if !strings.Contains(string(encoded), fragment) {
			t.Fatalf("conversation.created payload %s misses %s", encoded, fragment)
		}
	}

	value.Revision, value.UpdatedAt = 2, now.Add(time.Second)
	summary := ListSummary{
		UpdatedAt: value.UpdatedAt,
		Runs:      RunsSummary{},
	}
	built = newConversationEvent(
		actorID, nil, value, EventConversationUpdated, &summary,
	)
	bus.Publish(context.Background(), built.EntityType, built)

	familyEvents := familyLog.snapshot()
	if len(familyEvents) != 2 {
		t.Fatalf("published conversation family events = %d, want 2", len(familyEvents))
	}
	updated := familyEvents[1]
	if updated.Name != EventConversationUpdated {
		t.Fatalf("direct conversation.updated envelope = %+v", updated)
	}
	if updated.Payload.Conversation != value ||
		updated.Payload.Summary.UpdatedAt != summary.UpdatedAt {
		t.Fatalf("direct conversation.updated payload = %#v", updated.Payload)
	}
}

func TestTaskConversationEventCarriesCompanyRouting(t *testing.T) {
	t.Parallel()

	actorID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	taskID := uuid.Must(uuid.NewV7())
	now := time.Date(2026, time.July, 13, 4, 5, 6, 0, time.UTC)
	value := Response{
		ID: uuid.Must(uuid.NewV7()), CompanyID: companyID, Kind: KindTask,
		CreatedAt: now, UpdatedAt: now, Revision: 1,
	}

	bus := newTestBus(t)
	received := recordTopic[Snapshot](t, bus, Topic)
	built := newConversationEvent(
		actorID,
		&taskID,
		value,
		EventConversationCreated,
		nil,
	)
	bus.Publish(context.Background(), built.EntityType, built)

	published := received.snapshot()
	if len(published) != 1 {
		t.Fatalf("published conversation.created events = %d, want 1", len(published))
	}
	if published[0].Name != EventConversationCreated {
		t.Fatalf("task conversation event name = %q", published[0].Name)
	}
	payload := published[0].Payload
	// A Task conversation routes to the whole Company, and kind is what says
	// so; the creator is absent by construction.
	if payload.Conversation.Kind != KindTask || payload.Conversation.CompanyID != companyID ||
		payload.Conversation.CreatedByUserID != nil {
		t.Fatalf("task conversation.created routing = %#v", payload.Conversation)
	}
	// A caller that resolved the task separately backfills the conversation's
	// own taskId; there is no wrapper-level copy.
	if payload.Conversation.TaskID == nil || *payload.Conversation.TaskID != taskID {
		t.Fatalf("task conversation.created payload = %#v", payload)
	}
}

func TestConversationUserStateEventReachesOnlyTheReader(t *testing.T) {
	t.Parallel()

	actorID, companyID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	state := UserState{
		ConversationID: uuid.Must(uuid.NewV7()), UserID: actorID,
		UpdatedAt: time.Now().UTC(), Revision: 2,
	}

	bus := newTestBus(t)
	received := recordTopic[UserState](t, bus, Topic)
	built := newConversationUserStateEvent(actorID, companyID, state)
	bus.Publish(context.Background(), built.EntityType, built)

	published := received.snapshot()
	if len(published) != 1 {
		t.Fatalf("published conversation.read events = %d, want 1", len(published))
	}
	event := published[0]
	if event.Name != EventConversationRead || event.Actor != events.UserActor(actorID.String()) {
		t.Fatalf("conversation.read envelope = %+v", event)
	}
	// The watermark names its reader on the wire and its company only in
	// process: both are what the delivering consumer routes on.
	if event.Payload.CompanyID != companyID || event.Payload.UserID != actorID {
		t.Fatalf("conversation.read routing = %#v", event.Payload)
	}
	want := state
	want.CompanyID = companyID
	if event.Payload != want {
		t.Fatalf("conversation.read payload = %#v, want %#v", event.Payload, want)
	}
	encoded, err := json.Marshal(event.Payload)
	if err != nil {
		t.Fatalf("marshal conversation.read payload: %v", err)
	}
	if strings.Contains(string(encoded), companyID.String()) {
		t.Fatalf("conversation.read payload %s leaks its routing company", encoded)
	}
}
