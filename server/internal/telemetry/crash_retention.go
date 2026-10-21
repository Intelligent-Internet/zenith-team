package telemetry

import (
	"context"
	"errors"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

const (
	// DefaultRetention keeps opt-in crash/freeze reports for 30 days.
	DefaultRetention = 30 * 24 * time.Hour

	minimumRetention = time.Hour

	// maximumRetentionRoundsPerSweep bounds one sweep's work even when a
	// large expired backlog keeps returning full delete batches.
	maximumRetentionRoundsPerSweep = 16
)

var errRetentionPurge = errors.New("purge expired crash reports")

// RetentionIssue is a closed operational signal. It carries no report ID,
// reason text, database error, or other row material.
type RetentionIssue string

const (
	RetentionIssuePurgeFailed RetentionIssue = "purge_failed"
)

type RetentionWorkerOptions struct {
	Retention time.Duration
	BatchSize int
	OnIssue   func(RetentionIssue)
}

// RetentionWorker enforces the 30-day crash report retention with a periodic
// bounded delete loop.
//
// Each round is a single bounded DELETE, so the agent issues it through the
// pool's query set rather than opening a transaction.
type RetentionWorker struct {
	store   *db.Store
	options RetentionWorkerOptions
}

func NewRetentionWorker(database *db.Store, options RetentionWorkerOptions) (*RetentionWorker, error) {
	if database == nil || !normalizeRetentionWorkerOptions(&options) {
		return nil, ErrRetentionWorkerConfiguration
	}
	return &RetentionWorker{store: database, options: options}, nil
}

// purgeExpiredCrashReports deletes one bounded batch of reports received
// before the retention window and reports how many rows were removed. SKIP
// LOCKED semantics keep concurrent replicas from contending on the same batch.
// It takes the query set as a parameter, so a caller may pass the pool handle
// or a transaction.
func purgeExpiredCrashReports(
	ctx context.Context,
	q *generated.Queries,
	retention time.Duration,
	batchSize int,
) (int, error) {
	if q == nil || ctx == nil ||
		retention < time.Millisecond || retention%time.Millisecond != 0 ||
		batchSize < 1 || batchSize > 1000 {
		return 0, ErrStore
	}
	purged, err := q.PurgeExpiredCrashReports(ctx, generated.PurgeExpiredCrashReportsParams{
		RetentionMilliseconds: retention.Milliseconds(),
		BatchSize:             int32(batchSize),
	})
	if err != nil {
		return 0, ErrStore
	}
	return len(purged), nil
}

func normalizeRetentionWorkerOptions(options *RetentionWorkerOptions) bool {
	if options == nil {
		return false
	}
	if options.Retention == 0 {
		options.Retention = DefaultRetention
	}
	if options.BatchSize == 0 {
		options.BatchSize = 100
	}
	return options.Retention >= minimumRetention && options.Retention <= 365*24*time.Hour &&
		options.Retention%time.Millisecond == 0 &&
		options.BatchSize >= 1 && options.BatchSize <= 1000
}

// Tick is one retention pass. The composition root owns the cadence and the
// loop; a failure is recorded and the next pass tries again.
func (w *RetentionWorker) Tick(ctx context.Context) {
	if w == nil || w.store == nil || ctx == nil {
		return
	}
	if _, err := w.PurgeOnce(ctx); err != nil && ctx.Err() == nil {
		w.observeRetentionError(err)
	}
}

// PurgeOnce deletes expired reports in bounded batches until a batch comes
// back short or the per-sweep round bound is reached.
func (w *RetentionWorker) PurgeOnce(ctx context.Context) (int, error) {
	if w == nil || w.store == nil || ctx == nil {
		return 0, ErrRetentionWorkerConfiguration
	}
	total := 0
	for range maximumRetentionRoundsPerSweep {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		purged, err := purgeExpiredCrashReports(
			ctx, w.store.Queries(), w.options.Retention, w.options.BatchSize)
		if err != nil {
			return total, errors.Join(errRetentionPurge, err)
		}
		total += purged
		if purged < w.options.BatchSize {
			return total, nil
		}
	}
	return total, nil
}

func (w *RetentionWorker) observeRetentionError(err error) {
	if w == nil || w.options.OnIssue == nil || err == nil {
		return
	}
	if !errors.Is(err, errRetentionPurge) {
		return
	}
	func() {
		defer func() { _ = recover() }()
		w.options.OnIssue(RetentionIssuePurgeFailed)
	}()
}
