package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

const providerFixtureWaitTimeout = 30 * time.Second

// providerFixtureExecTimeout bounds a fixture run that is NOT about the
// timeout. It has to survive `go test -race ./...`, where these
// process-backed fixtures compete with the Docker-backed packages, so it
// is far longer than the work needs; it still sits under
// providerFixtureWaitTimeout so a genuinely wedged fixture fails as a
// timeout rather than as the harness's own deadline.
const providerFixtureExecTimeout = 20 * time.Second

func TestExecutionEventStreamStopsOnCancellationWithoutConsumer(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream := newExecutionEventStream()
	if stream.send(ctx, SessionReady{SessionID: "session-1"}) {
		t.Fatal("execution stream reported delivery without a consumer")
	}
}

func TestExecutionEventStreamWaitsForCompletedItemAcknowledgement(t *testing.T) {
	t.Parallel()

	stream := newExecutionEventStream()
	if capacity := cap(stream.events); capacity != 0 {
		t.Fatalf("execution event channel capacity = %d, want 0", capacity)
	}
	sent := make(chan bool, 1)
	go func() {
		sent <- stream.send(context.Background(), CompletedItem{
			Key: "item-1", Role: RoleAgent, Parts: []Part{TextPart{Text: "done"}},
		})
	}()
	event := <-stream.events
	item, ok := event.(CompletedItem)
	if !ok {
		t.Fatalf("event = %T, want CompletedItem", event)
	}
	select {
	case <-sent:
		t.Fatal("completed-item sender advanced before acknowledgement")
	case <-time.After(20 * time.Millisecond):
	}
	item.Acknowledge()
	select {
	case delivered := <-sent:
		if !delivered {
			t.Fatal("completed-item sender reported cancellation after acknowledgement")
		}
	case <-time.After(time.Second):
		t.Fatal("completed-item sender remained blocked after acknowledgement")
	}
}

func testExecutionEventEmitter(ch chan<- Event) executionEventEmitter {
	return func(_ context.Context, event Event) bool {
		ch <- event
		return true
	}
}

// Test-only aliases keep the protocol decoder tests compact while the old
// split stream shape remains private implementation detail.
type (
	Message     = providerMessage
	MessageType = providerMessageType
)

const (
	MessageText       = providerMessageText
	MessageThinking   = providerMessageThinking
	MessageToolUse    = providerMessageToolUse
	MessageToolResult = providerMessageToolResult
	MessageStatus     = providerMessageStatus
	MessageError      = providerMessageError
	MessageLog        = providerMessageLog
	messageItemDelta  = providerMessageItemDelta
)

// executionResults adapts the ordered public event stream for older focused
// provider tests that assert only the terminal outcome. New stream-contract
// tests should inspect Execution.Events directly.
func executionResults(execution *Execution) <-chan Result {
	results := make(chan Result, 1)
	go func() {
		defer close(results)
		for event := range execution.Events {
			if item, ok := event.(CompletedItem); ok {
				item.Acknowledge()
			}
			if finished, ok := event.(Finished); ok {
				results <- finished.Result
			}
		}
	}()
	return results
}

func collectExecution(t *testing.T, execution *Execution, timeout time.Duration) (Result, []Event) {
	t.Helper()
	var events []Event
	finishedCount := 0
	var result Result
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-execution.Events:
			if !ok {
				if finishedCount != 1 {
					t.Fatalf("finished event count = %d, want 1", finishedCount)
				}
				return result, events
			}
			events = append(events, event)
			if item, ok := event.(CompletedItem); ok {
				item.Acknowledge()
			}
			if finished, ok := event.(Finished); ok {
				finishedCount++
				result = finished.Result
			}
		case <-timer.C:
			t.Fatal("timeout waiting for execution event stream")
			return Result{}, nil
		}
	}
}

// mustNewProvider constructs a provider through the production factory so
// every test exercises the same construction path the daemon uses.
func mustNewProvider(t *testing.T, name string, cfg Config) Provider {
	t.Helper()
	constructed, err := New(name, cfg)
	if err != nil {
		t.Fatalf("New(%s) error = %v", name, err)
	}
	return constructed
}

// The factory and the protocol vocabulary must never drift: every supported
// provider constructs, and an unknown name is rejected with the supported
// set spelled out.
func TestNewConstructsEverySupportedProvider(t *testing.T) {
	t.Parallel()
	for _, name := range protocol.SupportedProviders {
		constructed, err := New(name, Config{})
		if err != nil || constructed == nil {
			t.Fatalf("New(%s) = %v, %v; want a provider", name, constructed, err)
		}
		if LaunchHeader(name) == "" {
			t.Fatalf("LaunchHeader(%s) is empty; every supported provider shows its launch skeleton", name)
		}
	}
	if LaunchHeader("hermes") != "" {
		t.Fatal("LaunchHeader(hermes) should be empty for an unknown provider")
	}

	if _, err := New("hermes", Config{}); err == nil ||
		!strings.Contains(err.Error(), "claude") || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("New(hermes) error = %v, want the supported set named", err)
	}
}

func TestNewDefaultsTheLogger(t *testing.T) {
	t.Parallel()
	constructed := mustNewProvider(t, "claude", Config{})
	if constructed.(*claudeProvider).cfg.Logger == nil {
		t.Fatal("New() left cfg.Logger nil; providers assume a usable logger")
	}
}
