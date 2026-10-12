package redis

import "testing"

func TestNewClientTreatsAnEmptyURLAsNotConfigured(t *testing.T) {
	t.Parallel()

	client, err := NewClient("")
	if err != nil {
		t.Fatalf("NewClient(\"\") error = %v, want nil — running without Redis is supported", err)
	}
	if client != nil {
		t.Fatal("NewClient(\"\") returned a client; callers branch on nil to take their local-only path")
	}
}

func TestNewClientRejectsAnUnparseableURL(t *testing.T) {
	t.Parallel()

	if _, err := NewClient("://nonsense"); err == nil {
		t.Fatal("NewClient() error = nil for an unparseable URL")
	}
}

// The timeout flag is the reason this constructor exists rather than a bare
// goredis.NewClient at the call site. Without it go-redis swaps the caller's
// context for context.Background around socket I/O, so a blackholed peer hangs
// the stream relay and the readiness probe past their own deadlines — a
// failure that looks like a stall, not an error.
func TestNewClientKeepsCommandContexts(t *testing.T) {
	t.Parallel()

	client, err := NewClient("redis://localhost:6379/0")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client == nil {
		t.Fatal("NewClient() = nil for a valid URL")
	}
	t.Cleanup(func() { _ = client.Close() })

	if !client.Options().ContextTimeoutEnabled {
		t.Error("ContextTimeoutEnabled = false; relay and readiness deadlines would not be honored")
	}
}
