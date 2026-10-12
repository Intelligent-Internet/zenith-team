package daemon

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package when a test leaves a goroutine running.
//
// The daemon runtime starts goroutines for work it must later stop — a leak
// here is a shutdown path that does not finish, which in production is a
// process that will not exit rather than one that crashes. Nothing is ignored:
// every goroutine started here is owned by something with a stop.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
