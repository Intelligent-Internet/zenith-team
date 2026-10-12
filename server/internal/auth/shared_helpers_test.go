package auth

// Helpers shared by this package's unit tests and its `//go:build integration`
// suites. They live in an untagged file on purpose: declared inside a tagged
// file, the untagged build of this package would fail with `undefined:`.

import (
	"context"
	"sync"
	"testing"

	"boardly.dev/boardly/server/internal/events"
)

func authTestBus(t *testing.T) *events.Bus {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct test bus: %v", err)
	}
	return bus
}

// recordedEvent is one delivered event with its payload already re-typed, so
// a test reads the payload as itself.
type recordedEvent[P any] struct {
	Name    string
	Actor   events.Actor
	Payload P
}

// topicLog collects what one topic publishes post-commit, replacing the
// deleted realtime_outbox table as the assertion surface. It re-types the
// payload at the subscription and reports a mismatch as a handler error, so a
// test never silently skips an event it meant to check.
type topicLog[P any] struct {
	mu     sync.Mutex
	events []recordedEvent[P]
}

func recordTopic[P any](t *testing.T, bus *events.Bus, topic string) *topicLog[P] {
	t.Helper()
	log := &topicLog[P]{}
	if err := bus.Subscribe(topic, func(_ context.Context, event events.Event) error {
		payload, err := events.PayloadOf[P](event)
		if err != nil {
			return err
		}
		log.mu.Lock()
		defer log.mu.Unlock()
		log.events = append(log.events, recordedEvent[P]{
			Name: event.Name, Actor: event.Actor, Payload: payload,
		})
		return nil
	}); err != nil {
		t.Fatalf("subscribe to %s: %v", topic, err)
	}
	return log
}

func (l *topicLog[P]) snapshot() []recordedEvent[P] {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]recordedEvent[P](nil), l.events...)
}
