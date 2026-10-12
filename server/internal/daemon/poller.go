package daemon

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Poll loop
// ---------------------------------------------------------------------------

// The shutdown drain, in the order stopAndDrain waits them out.
const (
	// providerDrainWindow is how long a stopping daemon waits for the runs
	// still executing in a provider CLI.
	providerDrainWindow = 30 * time.Second
	// callbackDrainWindow extends the wait once the providers have exited
	// and only terminal callbacks remain. Their retry schedule totals ~124s,
	// and waiting is cheap by then — abandoning a COMPLETED run's callback
	// mid-retry makes RecoverOrphans report it as a daemon-restart failure
	// and the retry child redoes finished work.
	callbackDrainWindow = 150 * time.Second
)

// ShutdownDrainBudget is the daemon's own worst case between the signal and
// process exit: the provider drain, the terminal-callback extension that
// follows it, the deregister call, app preview cleanup, and the health
// server's shutdown, which ends only after its graceful timeout plus the
// forced-close drain. It is exported because `zen daemon restart` has to
// outwait it before concluding the old process is stuck and refusing to start
// a replacement — a number that must move with these constants, not beside it.
const ShutdownDrainBudget = providerDrainWindow + callbackDrainWindow +
	deregisterTimeout + appPreviewShutdownCleanupWindow + healthShutdownGrace + healthCloseDrainGrace

func (d *Daemon) pollLoop(ctx context.Context, runWakeups <-chan runWakeup) error {
	sem := newRunSlotSemaphore(d.cfg.MaxConcurrentRuns)
	var runWG sync.WaitGroup

	runtimeSetCh, unsub := d.runtimeSet.Subscribe()
	defer unsub()

	runWakeup := make(chan struct{}, 1)
	mergeWakeup := make(chan struct{}, 1)
	nudgeRun := func() {
		signalPollerWakeup(runWakeup)
	}
	nudgeAll := func() {
		signalPollerWakeup(runWakeup)
		signalPollerWakeup(mergeWakeup)
	}

	pollerCtx, pollerCancel := context.WithCancel(ctx)
	runPollerDone := make(chan struct{})
	go func() {
		defer close(runPollerDone)
		d.runBatchPoller(pollerCtx, ctx, sem, runWakeup, &runWG)
	}()
	mergePollerDone := make(chan struct{})
	go func() {
		defer close(mergePollerDone)
		d.runTaskMergePoller(pollerCtx, mergeWakeup)
	}()

	stopAndDrain := func() {
		d.logger.Info("poll loop stopping, waiting for in-flight runs",
			"max_wait", providerDrainWindow.String())
		pollerCancel()
		// Wait for both pollers to fully return before waiting on runWG so
		// the Run poller cannot race Wait between claim and runWG.Add(1).
		<-runPollerDone
		<-mergePollerDone
		waitDone := make(chan struct{})
		go func() { runWG.Wait(); close(waitDone) }()
		select {
		case <-waitDone:
		case <-time.After(providerDrainWindow):
			if d.runningRuns.Load() == 0 && d.activeRuns.Load() > 0 {
				d.logger.Info("providers exited; extending drain for terminal callbacks",
					"max_extra_wait", callbackDrainWindow.String())
				select {
				case <-waitDone:
				case <-time.After(callbackDrainWindow):
					d.logger.Warn(
						"timed out waiting for terminal callbacks; completed results may be redone after restart",
					)
				}
			} else {
				d.logger.Warn("timed out waiting for in-flight runs")
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			stopAndDrain()
			return ctx.Err()
		case <-runPollerDone:
			if ctx.Err() != nil {
				stopAndDrain()
				return ctx.Err()
			}
			stopAndDrain()
			return errors.New("run poller stopped unexpectedly")
		case <-mergePollerDone:
			if ctx.Err() != nil {
				stopAndDrain()
				return ctx.Err()
			}
			stopAndDrain()
			return errors.New("task merge poller stopped unexpectedly")
		case <-runtimeSetCh:
			nudgeAll()
		case <-runWakeups:
			nudgeRun()
		}
	}
}

// runBatchPoller is the single machine-level claim+dispatch loop. Each cycle
// it acquires whatever execution slots are free (slot-before-claim, so a
// claimed run never sits dispatched without local capacity), asks the server
// for up to that many runs across all runtimes in one call, and dispatches
// each returned run into a slot.
func (d *Daemon) runBatchPoller(
	pollerCtx, parentCtx context.Context,
	sem chan int,
	wakeup chan struct{},
	runWG *sync.WaitGroup,
) {
	releaseSlots := func(slots []int) {
		for _, sl := range slots {
			sem <- sl
		}
	}

	for {
		if pollerCtx.Err() != nil {
			return
		}

		runtimeIDs := d.claimableRuntimeIDs()
		if len(runtimeIDs) == 0 {
			if err := sleepWithContextOrWakeup(pollerCtx, d.cfg.PollInterval, wakeup); err != nil {
				return
			}
			continue
		}

		slot, acquired, woke, err := waitForRunSlot(pollerCtx, sem, wakeup, 2*time.Second)
		if err != nil {
			return
		}
		if !acquired {
			if woke {
				continue
			}
			if err := sleepWithContextOrWakeup(
				pollerCtx,
				capacityBackoff(d.cfg.PollInterval),
				wakeup,
			); err != nil {
				return
			}
			continue
		}
		slots := append([]int{slot}, drainAvailableSlots(sem, d.cfg.MaxConcurrentRuns-1)...)

		// A Run freezes the apps whose discovery the cloud holds fresh when
		// it is claimed; renew a stale Runtime's first. Fail-open: the claim
		// goes ahead either way.
		if err := d.connectors.ensureFreshForClaim(pollerCtx, runtimeIDs); err != nil && pollerCtx.Err() == nil {
			d.logger.Warn("connector discovery not confirmed before claim", "error", err)
		}

		// A restart into a replaced binary holds the claim barrier; nothing
		// is claimed until the successor is up, so no run is cut short.
		if !d.enterClaim() {
			releaseSlots(slots)
			if err := sleepWithContextOrWakeup(pollerCtx, d.cfg.PollInterval, wakeup); err != nil {
				return
			}
			continue
		}
		runs, err := d.ClaimRunsWSFirst(pollerCtx, d.cfg.DaemonID, runtimeIDs, len(slots))
		if err != nil {
			d.exitClaim()
			releaseSlots(slots)
			if pollerCtx.Err() == nil {
				d.logger.Warn("batch claim failed", "error", err)
			}
			if err := sleepWithContextOrWakeup(pollerCtx, d.cfg.PollInterval, wakeup); err != nil {
				return
			}
			continue
		}

		dispatched := 0
		for i := range runs {
			if i >= len(slots) || runs[i] == nil {
				break
			}
			r := *runs[i]
			slot := slots[i]
			d.logger.Info("run received", "run", r.ID, "conversation", shortID(r.ConversationID))
			runWG.Add(1)
			d.activeRuns.Add(1)
			go func(r Run, slot int) {
				defer runWG.Done()
				defer d.activeRuns.Add(-1)
				defer func() {
					// Release local capacity before waking the poller so a
					// queued successor is claimed immediately instead of
					// waiting for PollInterval.
					sem <- slot
					signalPollerWakeup(wakeup)
				}()
				d.handleRun(parentCtx, r, slot)
			}(r, slot)
			dispatched++
		}
		// Every claimed run is now counted in activeRuns, so the barrier
		// check never sees a gap between the claim and its run.
		d.exitClaim()
		if dispatched < len(slots) {
			releaseSlots(slots[dispatched:])
		}

		// If we filled every slot, more work may be queued — loop immediately.
		if dispatched > 0 && dispatched == len(slots) {
			continue
		}
		if err := sleepWithContextOrWakeup(pollerCtx, d.cfg.PollInterval, wakeup); err != nil {
			return
		}
	}
}

func signalPollerWakeup(wakeup chan<- struct{}) {
	select {
	case wakeup <- struct{}{}:
	default:
	}
}

// drainAvailableSlots non-blockingly pulls up to limit additional slots.
func drainAvailableSlots(sem chan int, limit int) []int {
	if limit <= 0 {
		return nil
	}
	var slots []int
	for len(slots) < limit {
		select {
		case s := <-sem:
			slots = append(slots, s)
		default:
			return slots
		}
	}
	return slots
}

func capacityBackoff(pollInterval time.Duration) time.Duration {
	if pollInterval <= 0 || pollInterval > runSlotCapacityBackoff {
		return runSlotCapacityBackoff
	}
	return pollInterval
}

func waitForRunSlot(
	ctx context.Context,
	sem chan int,
	wakeup <-chan struct{},
	wait time.Duration,
) (slot int, acquired, woke bool, err error) {
	select {
	case slot = <-sem:
		return slot, true, false, nil
	case <-ctx.Done():
		return 0, false, false, ctx.Err()
	default:
	}

	if wait <= 0 {
		return 0, false, false, nil
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case slot = <-sem:
		return slot, true, false, nil
	case <-wakeup:
		return 0, false, true, nil
	case <-ctx.Done():
		return 0, false, false, ctx.Err()
	case <-timer.C:
		return 0, false, false, nil
	}
}

// newRunSlotSemaphore returns a buffered channel pre-populated with stable
// slot indices [0, n). Receive to acquire, send the same slot back to
// release; the index is exposed to spawned runs as BOARDLY_RUN_SLOT.
func newRunSlotSemaphore(maxConcurrentRuns int) chan int {
	sem := make(chan int, maxConcurrentRuns)
	for i := range maxConcurrentRuns {
		sem <- i
	}
	return sem
}

// gcLoop periodically reclaims expired run workspaces (retained worktrees
// past their TTL, completed scratch dirs). The first pass waits 30 seconds so
// startup registration and recover-orphans finish before any disk churn; a
// locally executing run is always fenced by the manager's in-use set.
// Server-truth reconciliation (orphan-marking against the active-conversation
// set) ships with its daemon-plane endpoint later.
