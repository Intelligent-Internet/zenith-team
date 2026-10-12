package daemonws

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package when a test leaves a goroutine running.
//
// A daemon connection owns a read pump and a write pump, and the hub owns the
// per-daemon send loops behind them. None of those show up in production as a
// test failure when they outlive their connection — they show up as a
// goroutine count that only climbs, on the process daemons stay connected to
// longest. This is the assertion that turns that into a red test.
//
// No stack is ignored, and none should need to be: every goroutine this
// package starts belongs to a connection, and every connection ends.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
