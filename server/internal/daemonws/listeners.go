package daemonws

import (
	"context"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
)

// EmptyClaimBumper invalidates a runtime's cached "nothing to claim" verdict.
//
// It has one implementation, execution.EmptyClaimCache, and this package
// already imports execution for the topic names — so the interface is not
// buying a swap or a dependency direction. It is here to be observable: the
// bump-before-wakeup order below is a correctness rule, and a test can only
// watch it happen through a seam. listeners_test.go is that test.
type EmptyClaimBumper interface {
	Bump(ctx context.Context, runtimeKey string)
}

// Wakeup is the daemon-plane transport this package already implements. It is
// an interface because it genuinely varies: the composition root passes the
// local Hub when Redis is absent and RelayNotifier when it is present, so the
// wakeup reaches whichever API node holds the daemon's socket.
type Wakeup interface {
	NotifyRunAvailable(runtimeID, runID string)
	NotifyPendingWork(runtimeID, kind string)
	NotifyCompaniesChanged(userID string)
}

// RegisterListeners wakes daemons in reaction to facts the domains publish,
// replacing the four one-method notifier interfaces that used to be threaded
// through the composition root.
//
// Each consumer subscribes to the one event it acts on rather than to that
// event's whole topic. The daemon plane reacts to exactly three facts, so
// naming them is what the registration should say — and agent and execution
// both publish on a topic called "runtime", so topic subscriptions would run
// every one of these handlers for every runtime fact and let two of them
// return early.
//
// The ordering that mattered is now local. A claim in flight may be about to
// write an "empty" verdict for this runtime; bumping first makes that verdict
// stale on read, so the woken claim cannot return null for work that already
// exists. Both halves live in this one handler, so no wiring order preserves
// them and nothing else can be interposed between them.
func RegisterListeners(bus *events.Bus, bumper EmptyClaimBumper, wakeup Wakeup) error {
	if err := bus.Subscribe(execution.EventRuntimeWorkAvailable,
		func(ctx context.Context, event events.Event) error {
			work, err := events.PayloadOf[execution.RuntimeWork](event)
			if err != nil {
				return err
			}
			if work.RuntimeID == "" {
				return nil
			}
			if bumper != nil {
				bumper.Bump(ctx, work.RuntimeID)
			}
			if wakeup != nil {
				wakeup.NotifyRunAvailable(work.RuntimeID, work.RunID)
			}
			return nil
		}); err != nil {
		return err
	}

	if err := bus.Subscribe(agent.EventRuntimePendingWork,
		func(_ context.Context, event events.Event) error {
			work, err := events.PayloadOf[agent.PendingWork](event)
			if err != nil {
				return err
			}
			if wakeup == nil || work.RuntimeID == "" {
				return nil
			}
			wakeup.NotifyPendingWork(work.RuntimeID, work.Kind)
			return nil
		}); err != nil {
		return err
	}

	return bus.Subscribe(company.EventCompanyMembershipChanged,
		func(_ context.Context, event events.Event) error {
			membership, err := events.PayloadOf[company.Membership](event)
			if err != nil {
				return err
			}
			if wakeup == nil || membership.UserID == "" {
				return nil
			}
			wakeup.NotifyCompaniesChanged(membership.UserID)
			return nil
		})
}
