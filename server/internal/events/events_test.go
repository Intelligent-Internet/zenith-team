package events

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type taskChange struct {
	TaskID string
}

type otherShape struct {
	Value string
}

// testCompany is the tenant every event in this file belongs to. Most tests
// here are about the bus rather than the tenant, so they take it as given;
// the company's own rule is pinned by its own test below.
var testCompany = uuid.MustParse("11111111-1111-1111-1111-111111111111")

func newTestBus(t *testing.T) *Bus {
	t.Helper()
	bus, err := NewBus(BusOptions{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	return bus
}

// entityTypeOf reads the domain off a wire name. Production never needs it —
// a producer names its own entity type from a constant — but a table-driven
// test that takes names as data does.
func entityTypeOf(name string) string {
	entityType, _, _ := strings.Cut(name, ".")
	return entityType
}

// eventOf is the test's own builder: the production producers write the same
// literal, so this keeps the assertions about the bus rather than about
// filling in five fields each time.
func eventOf(name string, actor Actor, payload any) Event {
	return Event{
		EntityType: entityTypeOf(name),
		Name:       name,
		CompanyID:  testCompany,
		CreatedAt:  time.Now().UTC(),
		Actor:      actor,
		Payload:    payload,
	}
}

func subscribeOrFail(t *testing.T, bus *Bus, topic string, h func(context.Context, Event) error) {
	t.Helper()
	if err := bus.Subscribe(topic, h); err != nil {
		t.Fatalf("Subscribe(%s) error = %v", topic, err)
	}
}

func TestOneSubscriptionReceivesTheWholeDomain(t *testing.T) {
	t.Parallel()
	bus := newTestBus(t)

	// The point of a domain topic: a consumer subscribes once and switches.
	var seen []string
	subscribeOrFail(t, bus, "task", func(_ context.Context, e Event) error {
		seen = append(seen, e.Name)
		return nil
	})

	bus.Publish(
		context.Background(), "task",
		eventOf("task.created", SystemActor(), taskChange{TaskID: "t1"}),
		eventOf("task.updated", SystemActor(), taskChange{TaskID: "t1"}),
	)

	if len(seen) != 2 || seen[0] != "task.created" || seen[1] != "task.updated" {
		t.Fatalf("delivered = %v, want both task events in order", seen)
	}
}

// Nothing polices an event as it is built — a producer writes a literal — so
// the bus is what refuses one nobody can act on, and says why. All four are
// authoring mistakes that would otherwise reach consumers as a nil payload or
// an unattributable fact.
func TestPublishRefusesAnEventNobodyCanActOn(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		topic string
		event Event
	}{
		{"no topic", "", Event{
			Name: "task.created", CreatedAt: time.Now().UTC(),
			Actor: SystemActor(), Payload: taskChange{},
		}},
		{"malformed topic", "Task", Event{
			EntityType: "Task", Name: "task.created", CreatedAt: time.Now().UTC(),
			Actor: SystemActor(), Payload: taskChange{},
		}},
		{"no name", "task", Event{
			EntityType: "task", Actor: SystemActor(), Payload: taskChange{},
		}},
		{"malformed name", "task", Event{
			EntityType: "task", Name: "task", CreatedAt: time.Now().UTC(),
			Actor: SystemActor(), Payload: taskChange{},
		}},
		{"name from another domain", "task", Event{
			EntityType: "task", Name: "run.completed", CreatedAt: time.Now().UTC(),
			Actor: SystemActor(), Payload: taskChange{},
		}},
		{"another domain", "task", eventOf("run.completed", SystemActor(), taskChange{})},
		{"no payload", "task", Event{
			EntityType: "task", Name: "task.created", Actor: SystemActor(),
		}},
		{"no actor", "task", Event{
			EntityType: "task", Name: "task.created", Payload: taskChange{},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			logs := newCapturingLogger()
			bus, err := NewBus(BusOptions{Logger: logs.logger()})
			if err != nil {
				t.Fatalf("NewBus() error = %v", err)
			}
			var delivered int
			subscribeOrFail(t, bus, "task", func(context.Context, Event) error {
				delivered++
				return nil
			})

			bus.Publish(context.Background(), test.topic, test.event)

			if delivered != 0 {
				t.Fatalf("consumers ran %d times, want 0", delivered)
			}
			if !logs.contains("event_bus_refused_event") {
				t.Fatalf("logged records = %v, want the refusal", logs.records())
			}
		})
	}
}

func TestPayloadOfReportsAMismatchInsteadOfSkippingIt(t *testing.T) {
	t.Parallel()
	event := Event{Name: "task.created", Payload: taskChange{TaskID: "t1"}}

	got, err := PayloadOf[taskChange](event)
	if err != nil || got.TaskID != "t1" {
		t.Fatalf("PayloadOf[taskChange]() = %+v, %v; want the payload", got, err)
	}

	// The shape nobody expected must be reportable, so a handler returning
	// this gets it logged against the site that subscribed rather than
	// silently skipping the event.
	if _, err := PayloadOf[otherShape](event); err == nil {
		t.Fatal("PayloadOf[otherShape]() error = nil, want a mismatch error")
	} else if !strings.Contains(err.Error(), "task.created") {
		t.Fatalf("mismatch error = %v, want it to name the event", err)
	}
}

func TestPublishSkipsOtherTopics(t *testing.T) {
	t.Parallel()
	bus := newTestBus(t)
	var runSeen int
	subscribeOrFail(t, bus, "run", func(context.Context, Event) error {
		runSeen++
		return nil
	})
	bus.Publish(context.Background(), "task",
		eventOf("task.created", SystemActor(), taskChange{}))
	if runSeen != 0 {
		t.Fatalf("cross-topic deliveries = %d, want 0", runSeen)
	}
}

func TestConsumersRunInRegistrationOrder(t *testing.T) {
	t.Parallel()
	bus := newTestBus(t)
	var order []string
	for _, mark := range []string{"first", "second", "third"} {
		subscribeOrFail(t, bus, "task", func(context.Context, Event) error {
			order = append(order, mark)
			return nil
		})
	}
	bus.Publish(context.Background(), "task",
		eventOf("task.created", SystemActor(), taskChange{}))

	want := []string{"first", "second", "third"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("consumer order = %v, want %v", order, want)
		}
	}
}

func TestOneFailingConsumerDoesNotStopTheOthers(t *testing.T) {
	t.Parallel()
	logs := newCapturingLogger()
	bus, err := NewBus(BusOptions{Logger: logs.logger()})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	var reached int
	subscribeOrFail(t, bus, "task", func(context.Context, Event) error { panic("consumer exploded") })
	subscribeOrFail(t, bus, "task", func(context.Context, Event) error { return errors.New("consumer refused") })
	subscribeOrFail(t, bus, "task", func(context.Context, Event) error {
		reached++
		return nil
	})
	bus.Publish(context.Background(), "task",
		eventOf("task.created", SystemActor(), taskChange{}))

	if reached != 1 {
		t.Fatalf("consumers after a panicking and a failing sibling ran %d times, want 1", reached)
	}
	for _, want := range []string{"event_bus_handler_panic", "event_bus_handler_error", "events_test.go:"} {
		if !logs.contains(want) {
			t.Fatalf("logged records = %v, want one naming %s", logs.records(), want)
		}
	}
}

// A fact is stamped when it was established — inside the producing
// transaction, at Validate — not when it happened to be published. Publishing
// must not restamp it: a transaction whose facts span domains leaves in
// several batches, and a publish-time stamp would scatter facts that were
// true at the same moment across those batches.
func TestTheStampIsWhenTheFactWasEstablished(t *testing.T) {
	t.Parallel()
	bus := newTestBus(t)
	var stamps []time.Time
	subscribeOrFail(t, bus, "task", func(_ context.Context, e Event) error {
		stamps = append(stamps, e.CreatedAt)
		return nil
	})

	created := eventOf("task.created", SystemActor(), taskChange{})
	updated := eventOf("task.updated", UserActor("user-1"), taskChange{})
	if created.CreatedAt.IsZero() || updated.CreatedAt.IsZero() {
		t.Fatalf("Validate left the stamp zero: %v, %v", created.CreatedAt, updated.CreatedAt)
	}
	if updated.CreatedAt.Before(created.CreatedAt) {
		t.Fatalf("stamps run backwards: %v then %v", created.CreatedAt, updated.CreatedAt)
	}

	bus.Publish(context.Background(), "task", created)
	bus.Publish(context.Background(), "task", updated)

	if len(stamps) != 2 || !stamps[0].Equal(created.CreatedAt) ||
		!stamps[1].Equal(updated.CreatedAt) {
		t.Fatalf("delivered stamps = %v, want the staging stamps %v and %v",
			stamps, created.CreatedAt, updated.CreatedAt)
	}
}

// A refused event must not take the batch down with it: the facts staged
// beside it in the same transaction still reach their consumers.
func TestARefusedEventDoesNotStopTheBatch(t *testing.T) {
	t.Parallel()
	logs := newCapturingLogger()
	bus, err := NewBus(BusOptions{Logger: logs.logger()})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	var delivered int
	subscribeOrFail(t, bus, "task", func(context.Context, Event) error {
		delivered++
		return nil
	})
	// A zero Event was never built by a producer. It must not reach a
	// consumer, must not stop the batch it sits in, and must not pass in
	// silence.
	bus.Publish(context.Background(), "task", Event{},
		eventOf("task.created", SystemActor(), taskChange{}))

	if delivered != 1 {
		t.Fatalf("deliveries = %d, want 1", delivered)
	}
	if !logs.contains("event_bus_refused_event") {
		t.Fatalf("logged records = %v, want one naming event_bus_refused_event", logs.records())
	}
}

func TestPublishWithoutABusIsReportedRatherThanIgnored(t *testing.T) {
	t.Parallel()
	event := eventOf("task.created", SystemActor(), taskChange{})
	defer func() {
		if recover() == nil {
			t.Fatal("publishing on a nil bus was ignored; a service wired without a bus drops every " +
				"fact it commits, which is the failure this design exists to end")
		}
	}()
	var nilBus *Bus
	nilBus.Publish(context.Background(), "task", event)
}

func TestPublishWithNoSubscribersIsANoOp(t *testing.T) {
	t.Parallel()
	bus := newTestBus(t)
	bus.Publish(context.Background(), "task",
		eventOf("task.created", SystemActor(), taskChange{}))
	bus.Publish(context.Background(), "task")
}

func TestAConsumerTakesEitherAWholeDomainOrOneEvent(t *testing.T) {
	t.Parallel()
	bus := newTestBus(t)

	// One consumer watches the domain, another watches a single fact. The
	// producer knows about neither.
	var domainSaw []string
	var oneSaw []string
	subscribeOrFail(t, bus, "task", func(_ context.Context, e Event) error {
		domainSaw = append(domainSaw, e.Name)
		return nil
	})
	subscribeOrFail(t, bus, "task.updated", func(_ context.Context, e Event) error {
		oneSaw = append(oneSaw, e.Name)
		return nil
	})

	bus.Publish(
		context.Background(), "task",
		eventOf("task.created", SystemActor(), taskChange{}),
		eventOf("task.updated", SystemActor(), taskChange{}),
	)

	if len(domainSaw) != 2 {
		t.Fatalf("domain consumer saw %v, want both events", domainSaw)
	}
	if len(oneSaw) != 1 || oneSaw[0] != "task.updated" {
		t.Fatalf("event consumer saw %v, want only task.updated", oneSaw)
	}
}

func TestSubscribeRefusesAKeyThatIsNeitherTopicNorEventName(t *testing.T) {
	t.Parallel()
	bus := newTestBus(t)
	for _, key := range []string{"", "Task", "task events", "task.created.twice", "task."} {
		if err := bus.Subscribe(key, func(context.Context, Event) error { return nil }); err == nil {
			t.Fatalf("Subscribe(%q) error = nil, want a rejection", key)
		}
	}
}

// The company rides the envelope so every consumer reads it the same way,
// instead of digging a differently-named field out of each domain's payload.
// It is what the producer said it is: a payload disagreeing with it does not
// get a vote, because the envelope is the thing routing trusts.
func TestTheCompanyRidesTheEnvelopeNotThePayload(t *testing.T) {
	t.Parallel()
	type scopedPayload struct{ CompanyID uuid.UUID }
	other := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	event := eventOf("task.created", SystemActor(), scopedPayload{CompanyID: other})

	if event.CompanyID != testCompany {
		t.Fatalf("CompanyID = %v, want the company the producer named (%v)", event.CompanyID, testCompany)
	}
}

// A sink that is about the stream rather than a domain takes every event with
// one registration, so a new domain never needs a line added to it — which is
// what an audit log has to guarantee to be worth anything.
func TestSubscribeAllTakesEveryTopic(t *testing.T) {
	t.Parallel()
	bus := newTestBus(t)
	var seen []string
	if err := bus.SubscribeAll(func(_ context.Context, e Event) error {
		seen = append(seen, e.Name)
		return nil
	}); err != nil {
		t.Fatalf("SubscribeAll() error = %v", err)
	}

	bus.Publish(context.Background(), "task", eventOf("task.created", SystemActor(), taskChange{}))
	bus.Publish(context.Background(), "run", eventOf("run.completed", SystemActor(), taskChange{}))
	bus.Publish(context.Background(), "inbox",
		eventOf("inbox.item_created", SystemActor(), taskChange{}))

	want := []string{"task.created", "run.completed", "inbox.item_created"}
	if len(seen) != len(want) {
		t.Fatalf("stream consumer saw %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("stream consumer saw %v, want %v", seen, want)
		}
	}
}

// A domain's own reactions to a fact run before anything watching the whole
// stream, so the audit row is appended after the fact has been acted on
// rather than in the middle of it.
func TestStreamConsumersRunAfterTheDomainsOwn(t *testing.T) {
	t.Parallel()
	bus := newTestBus(t)
	var order []string
	if err := bus.SubscribeAll(func(context.Context, Event) error {
		order = append(order, "stream")
		return nil
	}); err != nil {
		t.Fatalf("SubscribeAll() error = %v", err)
	}
	subscribeOrFail(t, bus, "task", func(context.Context, Event) error {
		order = append(order, "topic")
		return nil
	})
	subscribeOrFail(t, bus, "task.created", func(context.Context, Event) error {
		order = append(order, "name")
		return nil
	})

	bus.Publish(context.Background(), "task",
		eventOf("task.created", SystemActor(), taskChange{}))

	want := []string{"topic", "name", "stream"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("delivery order = %v, want %v", order, want)
		}
	}
}

// A transaction whose facts span domains publishes once per domain, and the
// order it staged them in is the order they leave: groups by first appearance,
// events in sequence inside each. Producers do stage in a meaningful order.
func TestGroupByTopicKeepsTheStagedOrder(t *testing.T) {
	t.Parallel()
	staged := []Event{
		eventOf("run.queued", SystemActor(), taskChange{TaskID: "r1"}),
		eventOf("task.created", SystemActor(), taskChange{TaskID: "t1"}),
		eventOf("run.dispatched", SystemActor(), taskChange{TaskID: "r2"}),
		eventOf("inbox.new", SystemActor(), taskChange{TaskID: "i1"}),
	}

	groups := GroupByTopic(staged)

	wantTopics := []string{"run", "task", "inbox"}
	if len(groups) != len(wantTopics) {
		t.Fatalf("groups = %d, want %d", len(groups), len(wantTopics))
	}
	for i, want := range wantTopics {
		if groups[i].Topic != want {
			t.Fatalf("group %d = %q, want %q — groups follow first appearance",
				i, groups[i].Topic, want)
		}
	}
	if len(groups[0].Events) != 2 ||
		groups[0].Events[0].Name != "run.queued" ||
		groups[0].Events[1].Name != "run.dispatched" {
		t.Fatalf("run group = %v, want queued then dispatched", groups[0].Events)
	}
}

// The row references are optional: a fact about none of them leaves them at
// their zero values, which is what absent means here — runs.id is a bigserial
// so 0 is never a run, and uuid.Nil is never a row.
func TestAnEventIsAboutNothingUnlessItSaysSo(t *testing.T) {
	t.Parallel()
	event := eventOf("agent.updated", SystemActor(), taskChange{})

	if event.RunID != uuid.Nil || event.TaskID != uuid.Nil || event.ConversationID != uuid.Nil {
		t.Fatalf("references = %v/%v/%v, want all absent",
			event.RunID, event.TaskID, event.ConversationID)
	}
}

// A run frame is about all three rows at once, and they reach the consumer
// as the producer wrote them: the bus carries the envelope through without
// dropping or rewriting what a fact says it is about.
func TestTheRowReferencesReachTheConsumer(t *testing.T) {
	t.Parallel()
	bus := newTestBus(t)
	taskID, conversationID := uuid.New(), uuid.New()
	var seen Event
	subscribeOrFail(t, bus, "run", func(_ context.Context, e Event) error {
		seen = e
		return nil
	})

	staged := eventOf("run.completed", SystemActor(), taskChange{})
	staged.RunID, staged.TaskID, staged.ConversationID = uuid.MustParse(
		"00000000-0000-4000-8000-000000000042",
	), taskID, conversationID
	bus.Publish(context.Background(), "run", staged)

	if seen.RunID != uuid.MustParse("00000000-0000-4000-8000-000000000042") || seen.TaskID != taskID ||
		seen.ConversationID != conversationID {
		t.Fatalf("references = %v/%v/%v, want 42/%v/%v",
			seen.RunID, seen.TaskID, seen.ConversationID, taskID, conversationID)
	}
}

// A producer writes the whole literal, so the one field with no natural
// default is the one that can go missing. Left alone it reaches the browser
// as a year-1 timestamp — wrong, and silent.
func TestPublishRefusesAnUnstampedEvent(t *testing.T) {
	t.Parallel()
	logs := newCapturingLogger()
	bus, err := NewBus(BusOptions{Logger: logs.logger()})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	var delivered int
	subscribeOrFail(t, bus, "task", func(context.Context, Event) error {
		delivered++
		return nil
	})

	unstamped := eventOf("task.created", SystemActor(), taskChange{})
	unstamped.CreatedAt = time.Time{}
	bus.Publish(context.Background(), "task", unstamped)

	if delivered != 0 {
		t.Fatalf("consumers ran %d times for an unstamped event, want 0", delivered)
	}
	if !logs.contains("event_bus_refused_event") {
		t.Fatalf("logged records = %v, want the refusal", logs.records())
	}
}
