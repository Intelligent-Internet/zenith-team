package mailbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const (
	defaultRetentionBatchSize = 100
	maxRetentionBatchSize     = 1000
	defaultRetentionInterval  = 15 * time.Minute
	maxRetentionRounds        = 16
)

var ErrRetentionConfiguration = errors.New("invalid mailbox retention configuration")

type PurgeResult struct {
	Attachments int64
	Messages    int64
	Receipts    int64
}

func (s *DefaultMailboxService) PurgeExpired(ctx context.Context, batchSize int) (PurgeResult, error) {
	if s == nil || s.store == nil || ctx == nil || batchSize < 1 || batchSize > maxRetentionBatchSize {
		return PurgeResult{}, ErrRetentionConfiguration
	}
	result := PurgeResult{}
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		messageRefs, err := queries.ClaimExpiredMailboxMessageRefs(
			ctx,
			generated.ClaimExpiredMailboxMessageRefsParams{
				NowAt: timestamp(s.now()), BatchSize: int32(batchSize),
			},
		)
		if err != nil {
			return fmt.Errorf("claim expired mailbox messages: %w", err)
		}
		if len(messageRefs) > 0 {
			messageIDs := make([]uuid.UUID, 0, len(messageRefs))
			for _, ref := range messageRefs {
				messageIDs = append(messageIDs, ref.ID)
				change, err := newChangedEvent(
					ctx, queries,
					ref.CompanyID,
					"system",
					nil,
					ChangeReasonMessageRemoved,
					&ref.ID,
					s.now(),
				)
				if err != nil {
					return err
				}
				pending = append(pending, change)
			}
			result.Attachments, err = queries.DeleteMailboxAttachmentsByMessageIDs(ctx, messageIDs)
			if err != nil {
				return fmt.Errorf("delete retained mailbox attachment metadata: %w", err)
			}
			result.Messages, err = queries.DeleteMailboxMessagesByIDs(ctx, messageIDs)
			if err != nil {
				return fmt.Errorf("delete retained mailbox message metadata: %w", err)
			}
		}

		receiptIDs, err := queries.ClaimExpiredMailboxReceiptIDs(
			ctx,
			generated.ClaimExpiredMailboxReceiptIDsParams{
				NowAt: timestamp(s.now()), BatchSize: int32(batchSize),
			},
		)
		if err != nil {
			return fmt.Errorf("claim expired mailbox receipts: %w", err)
		}
		if len(receiptIDs) > 0 {
			result.Receipts, err = queries.DeleteMailboxReceiptsByEventIDs(ctx, receiptIDs)
			if err != nil {
				return fmt.Errorf("delete retained mailbox receipts: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return PurgeResult{}, fmt.Errorf("purge expired mailbox metadata: %w", err)
	}
	publishMailboxEvents(ctx, s.bus, pending)
	if s.observer != nil {
		s.observer.ObserveMailboxRetention(result)
	}
	return result, nil
}

type RetentionWorkerOptions struct {
	BatchSize    int
	PollInterval time.Duration
	OnError      func(error)
}

type RetentionWorker struct {
	service *DefaultMailboxService
	options RetentionWorkerOptions
}

func NewRetentionWorker(
	service *DefaultMailboxService,
	options RetentionWorkerOptions,
) (*RetentionWorker, error) {
	if service == nil {
		return nil, ErrRetentionConfiguration
	}
	if options.BatchSize == 0 {
		options.BatchSize = defaultRetentionBatchSize
	}
	if options.PollInterval == 0 {
		options.PollInterval = defaultRetentionInterval
	}
	if options.BatchSize < 1 || options.BatchSize > maxRetentionBatchSize ||
		options.PollInterval < 100*time.Millisecond || options.PollInterval > 24*time.Hour {
		return nil, ErrRetentionConfiguration
	}
	return &RetentionWorker{service: service, options: options}, nil
}

func (w *RetentionWorker) Run(ctx context.Context) error {
	if w == nil || w.service == nil || ctx == nil {
		return ErrRetentionConfiguration
	}
	ticker := time.NewTicker(w.options.PollInterval)
	defer ticker.Stop()
	for {
		if _, err := w.PurgeOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.observeError(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *RetentionWorker) PurgeOnce(ctx context.Context) (PurgeResult, error) {
	if w == nil || w.service == nil || ctx == nil {
		return PurgeResult{}, ErrRetentionConfiguration
	}
	total := PurgeResult{}
	for range maxRetentionRounds {
		result, err := w.service.PurgeExpired(ctx, w.options.BatchSize)
		if err != nil {
			return total, err
		}
		total.Attachments += result.Attachments
		total.Messages += result.Messages
		total.Receipts += result.Receipts
		if result.Messages < int64(w.options.BatchSize) &&
			result.Receipts < int64(w.options.BatchSize) {
			return total, nil
		}
	}
	return total, nil
}

func (w *RetentionWorker) observeError(err error) {
	if w.options.OnError == nil || err == nil {
		return
	}
	func() {
		defer func() { _ = recover() }()
		w.options.OnError(err)
	}()
}
