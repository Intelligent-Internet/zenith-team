package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestNewRetentionWorkerValidatesConfiguration(t *testing.T) {
	t.Parallel()
	if _, err := NewRetentionWorker(
		nil,
		RetentionWorkerOptions{},
	); !errors.Is(
		err,
		ErrRetentionWorkerConfiguration,
	) {
		t.Fatalf("NewRetentionWorker(nil store) error = %v", err)
	}
	invalid := map[string]RetentionWorkerOptions{
		"short retention":           {Retention: time.Minute},
		"long retention":            {Retention: 366 * 24 * time.Hour},
		"sub-millisecond retention": {Retention: DefaultRetention + time.Nanosecond},
		"negative batch":            {BatchSize: -1},
		"oversize batch":            {BatchSize: 1001},
		"negative retention":        {Retention: -DefaultRetention},
	}
	for name, options := range invalid {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewRetentionWorker(
				new(db.Store),
				options,
			); !errors.Is(
				err,
				ErrRetentionWorkerConfiguration,
			) {
				t.Fatalf("NewRetentionWorker(%+v) error = %v", options, err)
			}
		})
	}
}

// TestRetentionWorkerDefaultsToThirtyDays pins the defaults the constructor
// applies and that the constructed agent carries them, since every sweep
// reads agent.options for the retention window and the batch bound. That
// those values reach the DELETE is covered by tests/integration/telemetry.
func TestRetentionWorkerDefaultsToThirtyDays(t *testing.T) {
	t.Parallel()
	options := RetentionWorkerOptions{}
	if !normalizeRetentionWorkerOptions(&options) {
		t.Fatalf("zero retention options rejected: %+v", options)
	}
	if options.Retention != 30*24*time.Hour || options.BatchSize != 100 {
		t.Fatalf("normalized options = %+v", options)
	}
	agent, err := NewRetentionWorker(new(db.Store), RetentionWorkerOptions{})
	if err != nil {
		t.Fatalf("NewRetentionWorker() error = %v", err)
	}
	if agent.options.Retention != options.Retention ||
		agent.options.BatchSize != options.BatchSize {
		t.Fatalf("constructed agent options = %+v, want %+v", agent.options, options)
	}
	if DefaultRetention != 30*24*time.Hour {
		t.Fatalf("DefaultRetention = %v, want 30 days", DefaultRetention)
	}
}

// TestPurgeExpiredCrashReportsRejectsInvalidParameters pins the guard that
// runs before the bounded DELETE. The query set is built over a nil DBTX, so
// a parameter set that slipped past the guard would panic on the query rather
// than pass quietly.
func TestPurgeExpiredCrashReportsRejectsInvalidParameters(t *testing.T) {
	t.Parallel()
	queries := generated.New(nil)
	invalid := map[string]struct {
		retention time.Duration
		batchSize int
	}{
		"zero batch":                {retention: DefaultRetention, batchSize: 0},
		"negative batch":            {retention: DefaultRetention, batchSize: -1},
		"oversize batch":            {retention: DefaultRetention, batchSize: 1001},
		"sub-millisecond retention": {retention: DefaultRetention + time.Nanosecond, batchSize: 100},
		"zero retention":            {retention: 0, batchSize: 100},
		"negative retention":        {retention: -time.Hour, batchSize: 100},
	}
	for name, parameters := range invalid {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			purged, err := purgeExpiredCrashReports(
				context.Background(), queries, parameters.retention, parameters.batchSize)
			if !errors.Is(err, ErrStore) || purged != 0 {
				t.Fatalf("purgeExpiredCrashReports(%s) = %d, %v", name, purged, err)
			}
		})
	}
	// An unconfigured query set fails under the same tag rather than panicking
	// on a nil handle.
	if _, err := purgeExpiredCrashReports(
		context.Background(), new(db.Store).Queries(), DefaultRetention, 100); !errors.Is(err, ErrStore) {
		t.Fatalf("purgeExpiredCrashReports(nil queries) error = %v", err)
	}
}

// TestObserveRetentionErrorReportsClosedIssues pins the sweep-failure
// classification without inventing validation for the trusted DELETE result.
func TestObserveRetentionErrorReportsClosedIssues(t *testing.T) {
	t.Parallel()
	agent, err := NewRetentionWorker(new(db.Store), RetentionWorkerOptions{})
	if err != nil {
		t.Fatalf("NewRetentionWorker() error = %v", err)
	}
	observed := make([]RetentionIssue, 0, 2)
	agent.options.OnIssue = func(issue RetentionIssue) { observed = append(observed, issue) }

	agent.observeRetentionError(
		errors.Join(errRetentionPurge, errors.New("relation crash_reports at 10.1.2.3 is unavailable")),
	)
	agent.observeRetentionError(errors.New("unclassified internal error"))
	agent.observeRetentionError(nil)
	if len(observed) != 1 || observed[0] != RetentionIssuePurgeFailed {
		t.Fatalf("observed issues = %v", observed)
	}

	// A panicking observer must not take the sweep loop down with it.
	agent.options.OnIssue = func(RetentionIssue) { panic("observer exploded") }
	agent.observeRetentionError(errRetentionPurge)
}

// The harness calls Tick on a schedule and ignores what it returns, so an
// unconfigured worker has to be inert rather than fatal: a panic here would
// take the whole server down on a timer.
func TestTickOnAnUnconfiguredWorkerDoesNothing(t *testing.T) {
	t.Parallel()
	var agent *RetentionWorker
	agent.Tick(context.Background())
}
