package main

import (
	"context"
	"log/slog"
	"time"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/execution"
	db "boardly.dev/boardly/server/pkg/db/generated"
)

const (
	// sweepInterval is how often we check for stale runtimes and runs. The
	// runtime stale window itself is domain policy:
	// agent.RuntimeStaleThreshold.
	sweepInterval = 30 * time.Second
	// dispatchTimeoutSeconds fails runs stuck in 'dispatched' beyond this.
	// The dispatched→running transition should be near-instant, so 5 minutes
	// means something went wrong (e.g. the start callback failed silently).
	// Runs holding a live prepare lease are excluded by the query.
	dispatchTimeoutSeconds = 300.0
	// runningTimeoutSeconds fails runs stuck in 'running' beyond this. It is
	// a coarse server-side backstop keyed on started_at, AND-gated by daemon
	// liveness (runtimes.last_seen_at freshness within
	// agent.RuntimeStaleThreshold): a running run whose runtime is still
	// heartbeating is NEVER killed by this wall clock, even after the timeout
	// elapses — that is what lets healthy multi-hour runs survive. The
	// primary "daemon died" path is sweepStaleRuntimes in the same tick.
	runningTimeoutSeconds = 9000.0
	// queuedTTLSeconds expires runs that have been sitting in 'queued' for
	// longer than this without ever being claimed — the time-bounded exit for
	// work already on the queue when a runtime dropped off.
	queuedTTLSeconds = 2 * 3600.0
	// queuedExpireBatchSize caps how many queued rows a single sweeper tick
	// transitions to failed, keeping the sweep transaction short even when a
	// backlog is large.
	queuedExpireBatchSize = 500
)

// sweepOnce is one reconciliation pass over everything that can go wrong
// while nobody is watching: a daemon that crashed or was killed without
// deregistering, a run left non-terminal, a deferred retry whose daemon never
// polled again.
//
// The runtime policy itself — which runtimes flip, which get deleted, and the
// roster and agent.status_changed fanout — lives in agent.RuntimeService.
// This is the order the duties run in and the run-domain follow-up, nothing
// more. Each arm logs its own failure and none stops the next: a sweep that
// gave up halfway would leave exactly the wreckage it exists to clear.
func sweepOnce(
	ctx context.Context,
	queries *db.Queries,
	runSvc execution.RunService,
	runtimeSvc agent.RuntimeService,
) {
	sweepStaleRuntimes(ctx, runSvc, runtimeSvc)
	sweepStaleRuns(ctx, runSvc)
	promoteDueDeferredRuns(ctx, runSvc)
	sweepExpiredQueuedRuns(ctx, runSvc)
	pruneOfflineRuntimes(ctx, runtimeSvc)
	gcRunTokens(ctx, queries)
}

// promoteDueDeferredRuns returns due deferred runs to the queued ladder.
// The daemon claim path promotes for the runtimes it serves; this arm covers
// the runtimes nothing serves, so a deferred retry whose daemon never polls
// again cannot count as its conversation's current run forever. Promotion
// deliberately precedes sweepExpiredQueuedRuns: a long-dead deferred row
// keeps its created_at, so the queued TTL disposes of it on a later tick.
func promoteDueDeferredRuns(ctx context.Context, runSvc execution.RunService) {
	promoted, err := runSvc.PromoteDueDeferredRuns(ctx)
	if err != nil {
		slog.Warn("runtime sweeper: failed to promote due deferred runs", "error", err)
		return
	}
	if len(promoted) > 0 {
		slog.Info("runtime sweeper: promoted due deferred runs", "count", len(promoted))
	}
}

// sweepStaleRuntimes demotes runtimes whose heartbeat went stale, then fails
// any runs belonging to those now-offline runtimes. The demotion counts those
// runs while it still owns the transaction and raises the runtime_offline
// Inbox items from that count, so the number the owner is told is the number
// this step fails a moment later.
func sweepStaleRuntimes(
	ctx context.Context,
	runSvc execution.RunService,
	runtimeSvc agent.RuntimeService,
) {
	flipped, err := runtimeSvc.MarkStaleRuntimesOffline(ctx)
	if err != nil {
		slog.Warn("runtime sweeper: failed to mark stale runtimes offline", "error", err)
		return
	}
	if flipped == 0 {
		return
	}

	// Fail orphaned runs (dispatched/running/waiting) whose runtimes just
	// went offline.
	failedBatch, err := runSvc.FailRunsForOfflineRuntimes(ctx)
	if err != nil {
		slog.Warn("runtime sweeper: failed to clean up stale runs", "error", err)
	} else if len(failedBatch.Runs) > 0 {
		slog.Info("runtime sweeper: failed orphaned runs",
			"count", len(failedBatch.Runs),
			"retried", failedBatch.RetryCount,
		)
	}
}

// pruneOfflineRuntimes deletes offline runtimes past the retention TTL.
func pruneOfflineRuntimes(ctx context.Context, runtimeSvc agent.RuntimeService) {
	if err := runtimeSvc.PruneOfflineRuntimes(ctx); err != nil {
		slog.Warn("runtime GC: failed to delete stale offline runtimes", "error", err)
	}
}

// gcRunTokens reaps expired brt_ credentials. The 24h expiry is enforced at
// read time by GetRunTokenByHash; this keeps the table from accreting.
func gcRunTokens(ctx context.Context, queries *db.Queries) {
	if err := queries.DeleteExpiredRunTokens(ctx); err != nil {
		slog.Warn("run token GC: failed to delete expired run tokens", "error", err)
	}
}

// sweepStaleRuns fails runs stuck in dispatched/running for too long, even
// when the runtime is still online at the row level. Each branch pairs the
// wall clock with a run-appropriate liveness signal so healthy long runs are
// preserved:
//   - dispatched: excludes rows with an active prepare_lease (renewed by the
//     daemon between claim and start).
//   - running: excludes rows whose runtime is 'online' with a fresh
//     last_seen_at (renewed by the daemon heartbeat ~every 15s).
//
// The daemon-dead case is primarily handled upstream by sweepStaleRuntimes in
// the same tick; this is a defensive backstop for the residual edge where a
// runtime row lingers online-with-stale-heartbeat past the wall clock.
func sweepStaleRuns(ctx context.Context, runSvc execution.RunService) {
	failedBatch, err := runSvc.FailStaleRuns(ctx, execution.StaleRunOptions{
		DispatchTimeout: time.Duration(dispatchTimeoutSeconds * float64(time.Second)),
		RunningTimeout:  time.Duration(runningTimeoutSeconds * float64(time.Second)),
		// Reuse the runtime stale window so the running-run backstop exactly
		// matches what MarkStaleRuntimesOffline considers "not alive".
		RuntimeStale: agent.RuntimeStaleThreshold,
	})
	if err != nil {
		slog.Warn("run sweeper: failed to clean up stale runs", "error", err)
		return
	}
	if len(failedBatch.Runs) == 0 {
		return
	}

	slog.Info("run sweeper: failed stale runs",
		"count", len(failedBatch.Runs),
		"retried", failedBatch.RetryCount,
	)
}

// sweepExpiredQueuedRuns fails runs that have been sitting in 'queued' for
// longer than the TTL — the cleanup arm for work already on the queue when a
// runtime dropped off (or that lost the race against a runtime going offline
// mid-tick). Capped per tick so a big backlog can't monopolise the DB.
func sweepExpiredQueuedRuns(ctx context.Context, runSvc execution.RunService) {
	failedBatch, err := runSvc.ExpireStaleQueuedRuns(ctx, execution.QueuedRunExpiryOptions{
		TTL:      time.Duration(queuedTTLSeconds * float64(time.Second)),
		MaxBatch: queuedExpireBatchSize,
	})
	if err != nil {
		slog.Warn("run sweeper: failed to expire stale queued runs", "error", err)
		return
	}
	if len(failedBatch.Runs) == 0 {
		return
	}

	slog.Info("run sweeper: expired stale queued runs",
		"count", len(failedBatch.Runs),
		"retried", failedBatch.RetryCount,
	)
}
