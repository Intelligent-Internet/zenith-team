package realtime

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package when a test leaves a goroutine running.
//
// This package is almost entirely goroutines: every client connection owns a
// read pump and a write pump, and the relay owns one reader per shard plus a
// heartbeat. A pump that outlives its connection shows up in production as a
// slow climb in goroutine count and never as a test failure — unless something
// asserts it. That is what this catches.
//
// Two stacks are ignored, and neither is a leak this package could fix:
//
//   - (*Hub).Run is a process-lifetime loop with no stop path by design. The
//     composition root starts it once with `go hub.Run()` and the process exits
//     around it. Giving it a Stop purely so a test could call one would be
//     production surface added for a test's benefit.
//   - go-redis runs a circuit-breaker cleanup loop per client, owned by the
//     library and outliving Close.
//
// Anything else that survives a test is ours and is a bug.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction(
			"github.com/Intelligent-Internet/zenith-team/server/internal/realtime.(*Hub).Run",
		),
		goleak.IgnoreAnyFunction(
			"github.com/redis/go-redis/v9/maintnotifications.(*CircuitBreakerManager).cleanupLoop",
		),
	)
}
