package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"runtime"
	"sort"
	"sync"
)

// Bus is pub/sub and nothing else: it knows topics, consumers, and how to
// hand one to the other. It has no idea what a WebSocket is, and no domain
// type appears in its surface.
//
// A consumer registers against a key, and an event is delivered to the
// consumers of two keys: its topic and its own name. So a consumer takes a
// whole domain with Subscribe(task.Topic, …) or exactly one fact with
// Subscribe(inbox.EventItemCreated, …), whichever suits it, and the producer
// is unaffected either way. SubscribeAll takes the stream itself, for the
// sinks that are about every fact rather than any one domain.
//
// The semantics are deliberately small: delivery is synchronous and
// in-process on the publisher's goroutine, consumers of a key run in
// registration order, every invocation is panic-isolated, a handler error is
// logged with the site that registered it, and publishing to a key nobody
// subscribes to is a no-op.
//
// Delivery is best-effort and per-node. Nothing is retried or persisted, so
// anything needing durability writes rows of its own. Handlers doing I/O own
// their timeouts, and Publish detaches from request cancellation so a
// cancelled caller never truncates a committed fact's fan-out.
type Bus struct {
	logger *slog.Logger

	mu    sync.RWMutex
	byKey map[string][]handler
}

// allKey is where the SubscribeAll consumers live. Neither name pattern can
// produce it, so no topic or event can collide with the slot.
const allKey = "*"

// handler is a consumer's reaction to one event. It is an alias, so callers
// pass a plain function and never name this type.
type handler = func(context.Context, Event) error

// BusOptions configures a Bus. The zero value is usable.
type BusOptions struct {
	// Logger receives handler panics, handler errors, and refused events.
	// Defaults to slog.Default().
	Logger *slog.Logger
}

// NewBus returns a ready Bus. The error return exists for constructor-shape
// consistency; construction cannot currently fail beyond option defaulting.
func NewBus(options BusOptions) (*Bus, error) {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Bus{logger: logger, byKey: make(map[string][]handler)}, nil
}

// Subscribe registers a consumer against a key: either a topic, to take a
// whole domain, or one event name, to take a single fact.
func (b *Bus) Subscribe(key string, handle handler) error {
	if b == nil {
		return errors.New("events: subscribing needs a bus")
	}
	if !topicName.MatchString(key) && !eventName.MatchString(key) {
		return fmt.Errorf("events: %q is neither a topic nor an event name", key)
	}
	if handle == nil {
		return fmt.Errorf("events: subscribing to %s needs a handler", key)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.byKey[key] = append(b.byKey[key], handle)
	return nil
}

// SubscribeAll registers a consumer against every event on the bus, whatever
// its topic. It is for the sinks that take the stream rather than a domain —
// the audit log is what it exists for — and saves them from a subscription
// per topic that has to be extended every time a domain is added.
//
// These run after the topic and name consumers, so a domain's own reactions
// to a fact happen before anything watching the whole stream.
func (b *Bus) SubscribeAll(handle handler) error {
	if b == nil {
		return errors.New("events: subscribing needs a bus")
	}
	if handle == nil {
		return errors.New("events: subscribing to every event needs a handler")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.byKey[allKey] = append(b.byKey[allKey], handle)
	return nil
}

// Subscriptions reports every key a consumer has registered against, sorted.
//
// It exists for the composition root's own gate: a fact a domain publishes
// and nobody consumes is either an oversight or deliberately internal, and
// the difference has to be visible to a test rather than to whoever notices
// the missing frame in production.
func (b *Bus) Subscriptions() []string {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	keys := make([]string, 0, len(b.byKey))
	for key := range b.byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Publish delivers a topic's facts after the transaction that built them has
// committed. Producers collect them inside the transaction and call this once
// commit returns nil.
//
// The topic is named here, the same string a consumer names in Subscribe, so
// the two halves of the bus read alike. A transaction whose facts span
// domains publishes once per domain — see GroupByTopic — rather than sending
// one batch that claims a topic most of it does not belong to.
//
// An event whose name does not belong to the named topic is refused: it would
// otherwise reach that topic's consumers, which expect their own domain's
// payloads. Publishing on a nil Bus panics: a service wired without one would
// drop every fact it commits, and silence is the failure this design exists
// to end.
func (b *Bus) Publish(ctx context.Context, topic string, pending ...Event) {
	if b == nil {
		panic("events: publish on a nil Bus — the service was wired without one, " +
			"so every fact it commits would be dropped")
	}
	if len(pending) == 0 {
		return
	}
	ctx = context.WithoutCancel(ctx)
	for _, event := range pending {
		if err := event.validate(topic); err != nil {
			// Nothing downstream can act on this, and the transaction that
			// produced it has already committed. Report it rather than
			// dropping it quietly.
			b.logger.Error("event bus refused an event it cannot deliver",
				"error_code", "event_bus_refused_event",
				"topic", topic, "event", event.Name, "error", err)
			continue
		}
		b.dispatch(ctx, topic, event)
	}
}

// TopicBatch is one domain's share of a transaction's staged facts.
type TopicBatch struct {
	Topic  string
	Events []Event
}

// GroupByTopic splits a transaction's staged facts by the domain each belongs
// to, for the producers whose one transaction commits facts across several.
// Each group is then published under its own topic.
//
// Groups come back in the order their entity type was first staged, and
// events keep their order within a group, so the sequence a producer staged
// is the sequence consumers see. A map would have made that order random
// between runs, and producers do stage in a meaningful order — a company
// frame before its personal twin, a run before the prompt that names it.
func GroupByTopic(pending []Event) []TopicBatch {
	grouped := make([]TopicBatch, 0, 4)
	at := make(map[string]int, 4)
	for _, event := range pending {
		topic := event.EntityType
		index, seen := at[topic]
		if !seen {
			at[topic] = len(grouped)
			grouped = append(grouped, TopicBatch{Topic: topic, Events: []Event{event}})
			continue
		}
		grouped[index].Events = append(grouped[index].Events, event)
	}
	return grouped
}

// dispatch delivers to the domain's consumers first, then to the consumers of
// this one event, then to whoever watches the whole stream. The order is
// fixed so a consumer cannot be interleaved differently from run to run.
func (b *Bus) dispatch(ctx context.Context, topic string, event Event) {
	b.mu.RLock()
	consumers := make([]handler, 0,
		len(b.byKey[topic])+len(b.byKey[event.Name])+len(b.byKey[allKey]))
	consumers = append(consumers, b.byKey[topic]...)
	consumers = append(consumers, b.byKey[event.Name]...)
	consumers = append(consumers, b.byKey[allKey]...)
	b.mu.RUnlock()

	for _, handle := range consumers {
		b.invoke(ctx, handle, event)
	}
}

func (b *Bus) invoke(ctx context.Context, handle handler, event Event) {
	defer func() {
		if recovered := recover(); recovered != nil {
			b.logger.Error("event bus handler panicked",
				"error_code", "event_bus_handler_panic",
				"topic", event.EntityType,
				"event", event.Name,
				"subscription_site", siteOf(handle),
				"panic", recovered)
		}
	}()
	if err := handle(ctx, event); err != nil {
		b.logger.Error("event bus handler failed",
			"error_code", "event_bus_handler_error",
			"topic", event.EntityType,
			"event", event.Name,
			"subscription_site", siteOf(handle),
			"error", err)
	}
}

// siteOf reports the file:line where a consumer was written.
//
// The bus logs it when a handler fails, because that failure is otherwise
// invisible: a post-commit reaction runs long after the response went out, so
// the log is the only evidence there is, and most topics have more than one
// consumer — its own, plus the audit sink that takes every event.
//
// It is read off the handler when something has already gone wrong, rather
// than remembered against every subscription: a func value already knows
// where it came from, so storing that beside it would be the same fact twice.
func siteOf(handle handler) string {
	function := runtime.FuncForPC(reflect.ValueOf(handle).Pointer())
	if function == nil {
		return "unknown"
	}
	file, line := function.FileLine(function.Entry())
	return fmt.Sprintf("%s:%d", shortPath(file), line)
}

// shortPath keeps the last two segments, enough to identify a file without
// the machine's directory layout.
func shortPath(file string) string {
	slash := len(file) - 1
	for count := 0; slash >= 0 && count < 2; slash-- {
		if file[slash] == '/' {
			count++
			if count == 2 {
				return file[slash+2:]
			}
		}
	}
	return file
}
