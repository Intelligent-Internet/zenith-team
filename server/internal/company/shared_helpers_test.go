package company

// Helpers shared by this package's unit tests and its `//go:build integration`
// suites. They live in an untagged file on purpose: declared inside a tagged
// file, the untagged build of this package would fail with `undefined:`.

import (
	"context"
	"sync"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
)

// topicLog collects what one topic publishes post-commit. A subscription is
// per topic and carries the domain's whole catalog, so the log keeps every
// event the topic delivers and a test narrows it by name.
type topicLog struct {
	mu     sync.Mutex
	events []events.Event
}

func recordTopic(t *testing.T, bus *events.Bus, topic string) *topicLog {
	t.Helper()
	log := &topicLog{}
	if err := bus.Subscribe(topic,
		func(_ context.Context, event events.Event) error {
			log.mu.Lock()
			defer log.mu.Unlock()
			log.events = append(log.events, event)
			return nil
		}); err != nil {
		t.Fatalf("subscribe to %s: %v", topic, err)
	}
	return log
}

func (l *topicLog) snapshot() []events.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]events.Event(nil), l.events...)
}

// named filters the log to one member of the recorded family, so a test can
// keep asserting on exactly the event it event.
func (l *topicLog) named(name string) []events.Event {
	var filtered []events.Event
	for _, event := range l.snapshot() {
		if event.Name == name {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

// payloadOf re-types one recorded payload. A shape nobody expected fails the
// test rather than being skipped in silence.
func payloadOf[P any](t *testing.T, event events.Event) P {
	t.Helper()
	payload, err := events.PayloadOf[P](event)
	if err != nil {
		t.Fatalf("payload of %s: %v", event.Name, err)
	}
	return payload
}

func companyTestBus(t *testing.T) *events.Bus {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	return bus
}
