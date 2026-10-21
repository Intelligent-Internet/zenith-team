package attachment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

const (
	maximumCleanupBatchSize    = 1000
	minimumAbandonedReadyGrace = time.Hour
	maximumAbandonedReadyGrace = 30 * 24 * time.Hour
)

var (
	errCleanupPrepare      = errors.New("prepare expired Attachment cleanup")
	errCleanupClaim        = errors.New("claim deleting Attachment cleanup candidates")
	errCleanupObjectDelete = errors.New("delete Attachment object")
)

// CleanupIssue is a closed operational signal. It contains no Attachment ID,
// Company ID, object key, storage error, database error, path, or credential.
type CleanupIssue string

const (
	CleanupIssuePrepareFailed    CleanupIssue = "prepare_failed"
	CleanupIssueClaimFailed      CleanupIssue = "claim_failed"
	CleanupIssueDeleteFailed     CleanupIssue = "delete_failed"
	CleanupIssueInvalidStoreData CleanupIssue = "invalid_store_data"
)

// CleanupProcessor removes one already-durable deleting Attachment.
// DefaultAttachmentService satisfies this boundary and finalizes the terminal
// event transactionally.
type CleanupProcessor interface {
	Cleanup(context.Context, CleanupInput) error
}

type CleanupWorkerOptions struct {
	BatchSize           int
	AbandonedReadyGrace time.Duration
	OnIssue             func(CleanupIssue)
}

type CleanupWorker struct {
	store *db.Store
	// The store may return a deleting row to more than one API replica;
	// CleanupProcessor must therefore be target-state idempotent.
	processor CleanupProcessor
	options   CleanupWorkerOptions
}

func NewCleanupWorker(
	store *db.Store,
	processor CleanupProcessor,
	options CleanupWorkerOptions,
) (*CleanupWorker, error) {
	if store == nil || nilDependency(processor) || !normalizeCleanupWorkerOptions(&options) {
		return nil, ErrCleanupWorkerConfiguration
	}
	return &CleanupWorker{store: store, processor: processor, options: options}, nil
}

func normalizeCleanupWorkerOptions(options *CleanupWorkerOptions) bool {
	if options == nil {
		return false
	}
	if options.BatchSize == 0 {
		options.BatchSize = 64
	}
	if options.AbandonedReadyGrace == 0 {
		options.AbandonedReadyGrace = 24 * time.Hour
	}
	return options.BatchSize >= 1 && options.BatchSize <= maximumCleanupBatchSize &&
		options.AbandonedReadyGrace >= minimumAbandonedReadyGrace &&
		options.AbandonedReadyGrace <= maximumAbandonedReadyGrace
}

// Tick is one cleanup pass. The composition root owns the cadence and the
// loop; a failure is recorded and the next pass tries again, because the rows
// stay claimable and a transient store outage must not end the worker.
func (w *CleanupWorker) Tick(ctx context.Context) {
	if !validCleanupWorkerCall(ctx, w) {
		return
	}
	if _, err := w.CleanupOnce(ctx); err != nil && ctx.Err() == nil {
		w.observeCleanupError(err)
	}
}

func (w *CleanupWorker) CleanupOnce(ctx context.Context) (int, error) {
	if !validCleanupWorkerCall(ctx, w) {
		return 0, ErrCleanupWorkerConfiguration
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if _, err := transitionExpiredPendingForCleanup(
		ctx, w.store.Queries(), w.options.BatchSize,
	); err != nil {
		return 0, errors.Join(errCleanupPrepare, err)
	}
	if _, err := transitionAbandonedReadyForCleanup(
		ctx,
		w.store,
		w.options.AbandonedReadyGrace,
		w.options.BatchSize,
	); err != nil {
		return 0, errors.Join(errCleanupPrepare, err)
	}
	candidates, err := claimDeletingForCleanup(
		ctx, w.store.Queries(), w.options.BatchSize,
	)
	if err != nil {
		return 0, errors.Join(errCleanupClaim, err)
	}

	completed := 0
	cleanupErrors := make([]error, 0)
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return completed, err
		}
		if err := w.processor.Cleanup(ctx, candidate); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return completed, ctxErr
			}
			cleanupErrors = append(cleanupErrors, errors.Join(errCleanupObjectDelete, err))
			continue
		}
		completed++
	}
	return completed, errors.Join(cleanupErrors...)
}

func validCleanupWorkerCall(ctx context.Context, worker *CleanupWorker) bool {
	return worker != nil && ctx != nil && worker.store != nil && !nilDependency(worker.processor)
}

func (w *CleanupWorker) observeCleanupError(err error) {
	if w == nil || w.options.OnIssue == nil || err == nil {
		return
	}
	issues := cleanupIssues(err)
	for _, issue := range issues {
		func() {
			defer func() { _ = recover() }()
			w.options.OnIssue(issue)
		}()
	}
}

func cleanupIssues(err error) []CleanupIssue {
	issues := make([]CleanupIssue, 0, 2)
	if errors.Is(err, errCleanupPrepare) {
		issues = append(issues, CleanupIssuePrepareFailed)
	}
	if errors.Is(err, errCleanupClaim) {
		issues = append(issues, CleanupIssueClaimFailed)
	}
	if errors.Is(err, errCleanupObjectDelete) {
		issues = append(issues, CleanupIssueDeleteFailed)
	}
	if len(issues) == 0 {
		// Unknown implementations and future failures still collapse to a fixed,
		// non-sensitive operational label.
		issues = append(issues, CleanupIssueInvalidStoreData)
	}
	return issues
}

func transitionExpiredPendingForCleanup(
	ctx context.Context,
	q *generated.Queries,
	batchSize int,
) (int, error) {
	if !validAttachmentCleanupStoreCall(ctx, q, batchSize) {
		return 0, ErrConflict
	}
	rows, err := q.TransitionExpiredPendingAttachmentsForCleanup(ctx, int32(batchSize))
	if err != nil {
		return 0, fmt.Errorf("transition expired pending Attachments for cleanup: %w", err)
	}
	return len(rows), nil
}

// transitionAbandonedReadyForCleanup owns the ReadCommitted claim-then-transition
// transaction so the claim's row locks hold until the guarded transition commits.
func transitionAbandonedReadyForCleanup(
	ctx context.Context,
	store *db.Store,
	grace time.Duration,
	batchSize int,
) (int, error) {
	if store == nil {
		return 0, ErrConflict
	}
	transitioned := 0
	err := store.WithinTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx *generated.Queries) error {
		var txErr error
		transitioned, txErr = claimAndTransitionAbandonedReady(ctx, tx, grace, batchSize)
		return txErr
	})
	if err != nil {
		return 0, err
	}
	return transitioned, nil
}

// claimAndTransitionAbandonedReady claims then transitions on the one q it is
// handed; q MUST be an open ReadCommitted transaction so the claim's row locks
// hold through the guarded transition. transitionAbandonedReadyForCleanup above
// owns that transaction.
func claimAndTransitionAbandonedReady(
	ctx context.Context,
	q *generated.Queries,
	grace time.Duration,
	batchSize int,
) (int, error) {
	if !validAttachmentCleanupStoreCall(ctx, q, batchSize) ||
		grace < minimumAbandonedReadyGrace || grace > maximumAbandonedReadyGrace {
		return 0, ErrConflict
	}
	graceMilliseconds, err := attachmentDurationMilliseconds(grace)
	if err != nil {
		return 0, ErrConflict
	}
	claimed, err := q.ClaimAbandonedReadyAttachmentCandidates(
		ctx,
		generated.ClaimAbandonedReadyAttachmentCandidatesParams{
			AbandonedGraceMilliseconds: graceMilliseconds, BatchSize: int32(batchSize),
		},
	)
	if err != nil {
		return 0, fmt.Errorf("claim abandoned ready Attachments: %w", err)
	}
	if len(claimed) == 0 {
		return 0, nil
	}
	rows, err := q.TransitionClaimedAbandonedReadyAttachments(
		ctx,
		generated.TransitionClaimedAbandonedReadyAttachmentsParams{
			AttachmentIds:              claimed,
			AbandonedGraceMilliseconds: graceMilliseconds,
		},
	)
	if err != nil {
		return 0, fmt.Errorf("transition abandoned ready Attachments: %w", err)
	}
	return len(rows), nil
}

func claimDeletingForCleanup(
	ctx context.Context,
	q *generated.Queries,
	batchSize int,
) ([]CleanupInput, error) {
	if !validAttachmentCleanupStoreCall(ctx, q, batchSize) {
		return nil, ErrConflict
	}
	rows, err := q.ClaimDeletingAttachmentCleanupCandidates(ctx, int32(batchSize))
	if err != nil {
		return nil, fmt.Errorf("claim deleting Attachment cleanup candidates: %w", err)
	}
	candidates := make([]CleanupInput, len(rows))
	for index, row := range rows {
		candidates[index] = CleanupInput{ID: row.ID, CompanyID: row.CompanyID}
	}
	return candidates, nil
}
