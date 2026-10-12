package commerce

import (
	"context"
	"errors"
	"time"
)

const (
	defaultWebhookReconciliationInterval = 30 * time.Second
	minimumWebhookReconciliationInterval = 100 * time.Millisecond
	maximumWebhookReconciliationInterval = time.Hour
	defaultWebhookReconciliationBatch    = int32(100)
)

var ErrWebhookReconcilerConfiguration = errors.New(
	"invalid Commerce webhook reconciler configuration",
)

type WebhookReconciliationIssue string

const (
	WebhookReconciliationIssueProcessingFailed WebhookReconciliationIssue = "processing_failed"
	WebhookReconciliationIssueDeadLettered     WebhookReconciliationIssue = "dead_lettered"
)

type WebhookReconcilerOptions struct {
	PollInterval time.Duration
	BatchSize    int32
	OnIssue      func(WebhookReconciliationIssue)
}

type webhookReceiptReconciler interface {
	ReconcileWebhookReceipts(context.Context, int32) (WebhookReconciliationResult, error)
}

type WebhookReconciler struct {
	processor webhookReceiptReconciler
	options   WebhookReconcilerOptions
}

func NewWebhookReconciler(
	processor webhookReceiptReconciler,
	options WebhookReconcilerOptions,
) (*WebhookReconciler, error) {
	if processor == nil || !normalizeWebhookReconcilerOptions(&options) {
		return nil, ErrWebhookReconcilerConfiguration
	}
	return &WebhookReconciler{processor: processor, options: options}, nil
}

func (worker *WebhookReconciler) Run(ctx context.Context) error {
	if worker == nil || worker.processor == nil || ctx == nil {
		return ErrWebhookReconcilerConfiguration
	}
	ticker := time.NewTicker(worker.options.PollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := worker.processor.ReconcileWebhookReceipts(
			ctx,
			worker.options.BatchSize,
		)
		if result.DeadLettered > 0 {
			worker.observeIssue(WebhookReconciliationIssueDeadLettered)
		}
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return contextErr
			}
			worker.observeIssue(WebhookReconciliationIssueProcessingFailed)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func normalizeWebhookReconcilerOptions(options *WebhookReconcilerOptions) bool {
	if options == nil {
		return false
	}
	if options.PollInterval == 0 {
		options.PollInterval = defaultWebhookReconciliationInterval
	}
	if options.BatchSize == 0 {
		options.BatchSize = defaultWebhookReconciliationBatch
	}
	return options.PollInterval >= minimumWebhookReconciliationInterval &&
		options.PollInterval <= maximumWebhookReconciliationInterval &&
		options.PollInterval%time.Millisecond == 0 &&
		options.BatchSize >= 1 && options.BatchSize <= maxWebhookBatch
}

func (worker *WebhookReconciler) observeIssue(issue WebhookReconciliationIssue) {
	if worker.options.OnIssue == nil {
		return
	}
	func() {
		defer func() { _ = recover() }()
		worker.options.OnIssue(issue)
	}()
}
