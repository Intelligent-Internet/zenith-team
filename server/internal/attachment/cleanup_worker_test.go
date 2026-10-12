package attachment

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/db"
)

func TestNewCleanupWorkerDefaultsAndBoundsConfiguration(t *testing.T) {
	t.Parallel()

	processor := &cleanupProcessorStub{}
	worker, err := NewCleanupWorker(
		&db.Store{},
		processor,
		CleanupWorkerOptions{},
	)
	if err != nil {
		t.Fatalf("NewCleanupWorker(defaults) error = %v", err)
	}
	if worker.options.BatchSize != 64 ||
		worker.options.AbandonedReadyGrace != 24*time.Hour {
		t.Fatalf("defaults = %+v", worker.options)
	}

	for name, mutate := range map[string]func(*CleanupWorkerOptions){
		"negative batch":  func(options *CleanupWorkerOptions) { options.BatchSize = -1 },
		"oversized batch": func(options *CleanupWorkerOptions) { options.BatchSize = maximumCleanupBatchSize + 1 },
		"short abandoned grace": func(options *CleanupWorkerOptions) {
			options.AbandonedReadyGrace = minimumAbandonedReadyGrace - time.Nanosecond
		},
		"long abandoned grace": func(options *CleanupWorkerOptions) {
			options.AbandonedReadyGrace = maximumAbandonedReadyGrace + time.Nanosecond
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			options := CleanupWorkerOptions{
				BatchSize:           1,
				AbandonedReadyGrace: 24 * time.Hour,
			}
			mutate(&options)
			if candidate, err := NewCleanupWorker(&db.Store{}, processor, options); candidate != nil ||
				!errors.Is(err, ErrCleanupWorkerConfiguration) {
				t.Fatalf("NewCleanupWorker() = %#v, %v", candidate, err)
			}
		})
	}

	if candidate, err := NewCleanupWorker(
		nil,
		processor,
		CleanupWorkerOptions{},
	); candidate != nil ||
		!errors.Is(err, ErrCleanupWorkerConfiguration) {
		t.Fatalf("NewCleanupWorker(nil store) = %#v, %v", candidate, err)
	}
	var nilProcessor *cleanupProcessorStub
	if candidate, err := NewCleanupWorker(
		&db.Store{},
		nilProcessor,
		CleanupWorkerOptions{},
	); candidate != nil ||
		!errors.Is(err, ErrCleanupWorkerConfiguration) {
		t.Fatalf("NewCleanupWorker(typed nil processor) = %#v, %v", candidate, err)
	}
}

func TestCleanupWorkerCallbackIsClosedAndRedacted(t *testing.T) {
	t.Parallel()

	// An uninitialized store makes the very first prepare step fail for real,
	// which is the loop path that reaches the operator callback.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	issues := make(chan CleanupIssue, 1)
	worker, err := NewCleanupWorker(&db.Store{}, &cleanupProcessorStub{}, CleanupWorkerOptions{
		OnIssue: func(issue CleanupIssue) {
			issues <- issue
			cancel()
		},
	})
	if err != nil {
		t.Fatalf("NewCleanupWorker() error = %v", err)
	}
	worker.Tick(ctx)
	select {
	case issue := <-issues:
		if issue != CleanupIssuePrepareFailed {
			t.Fatalf("cleanup issue = %q", issue)
		}
	default:
		t.Fatal("cleanup issue callback was not invoked")
	}

	// Whatever the underlying failure carried, only the fixed label escapes.
	const secret = "postgresql://admin:secret@private/attachments"
	for name, failure := range map[string]struct {
		err  error
		want []CleanupIssue
	}{
		"prepare":  {errors.Join(errCleanupPrepare, errors.New(secret)), []CleanupIssue{CleanupIssuePrepareFailed}},
		"claim":    {errors.Join(errCleanupClaim, errors.New(secret)), []CleanupIssue{CleanupIssueClaimFailed}},
		"delete":   {errors.Join(errCleanupObjectDelete, errors.New(secret)), []CleanupIssue{CleanupIssueDeleteFailed}},
		"unknown":  {errors.New(secret), []CleanupIssue{CleanupIssueInvalidStoreData}},
		"combined": {errors.Join(errCleanupPrepare, errCleanupObjectDelete), []CleanupIssue{CleanupIssuePrepareFailed, CleanupIssueDeleteFailed}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := cleanupIssues(failure.err)
			if !reflect.DeepEqual(got, failure.want) {
				t.Fatalf("cleanupIssues() = %v, want %v", got, failure.want)
			}
			for _, issue := range got {
				if strings.Contains(string(issue), "secret") ||
					strings.Contains(string(issue), "postgres") {
					t.Fatalf("cleanup issue leaked the raw failure: %q", issue)
				}
			}
		})
	}
}

type cleanupProcessorStub struct {
	cleanup func(context.Context, CleanupInput) error
}

func (p *cleanupProcessorStub) Cleanup(ctx context.Context, input CleanupInput) error {
	if p.cleanup == nil {
		return nil
	}
	return p.cleanup(ctx, input)
}

var _ CleanupProcessor = (*cleanupProcessorStub)(nil)
