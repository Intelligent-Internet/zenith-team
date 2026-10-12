package commerce

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestWebhookReconcilerRunsImmediatelyAndRedactsIssues(t *testing.T) {
	t.Parallel()
	processor := &recordingWebhookReceiptReconciler{err: errors.New("rk_live_must_not_escape")}
	issues := make(chan WebhookReconciliationIssue, 1)
	worker, err := NewWebhookReconciler(processor, WebhookReconcilerOptions{
		PollInterval: 100 * time.Millisecond,
		BatchSize:    7,
		OnIssue: func(issue WebhookReconciliationIssue) {
			issues <- issue
		},
	})
	if err != nil {
		t.Fatalf("NewWebhookReconciler() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case issue := <-issues:
		if issue != WebhookReconciliationIssueProcessingFailed {
			t.Fatalf("Webhook issue = %q", issue)
		}
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("WebhookReconciler did not run immediately")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("WebhookReconciler.Run() error = %v", err)
	}
	if calls, limit := processor.snapshot(); calls != 1 || limit != 7 {
		t.Fatalf("Webhook reconciliation calls/limit = %d/%d", calls, limit)
	}
}

func TestWebhookReconcilerReportsDeadLettersSeparately(t *testing.T) {
	t.Parallel()
	processor := &recordingWebhookReceiptReconciler{
		result: WebhookReconciliationResult{DeadLettered: 2},
	}
	issues := make(chan WebhookReconciliationIssue, 1)
	worker, err := NewWebhookReconciler(processor, WebhookReconcilerOptions{
		PollInterval: 100 * time.Millisecond,
		BatchSize:    7,
		OnIssue: func(issue WebhookReconciliationIssue) {
			issues <- issue
		},
	})
	if err != nil {
		t.Fatalf("NewWebhookReconciler() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case issue := <-issues:
		if issue != WebhookReconciliationIssueDeadLettered {
			t.Fatalf("Webhook issue = %q", issue)
		}
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("WebhookReconciler did not report dead-lettered receipts")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("WebhookReconciler.Run() error = %v", err)
	}
}

type recordingWebhookReceiptReconciler struct {
	mu     sync.Mutex
	calls  int
	limit  int32
	err    error
	result WebhookReconciliationResult
}

func (reconciler *recordingWebhookReceiptReconciler) ReconcileWebhookReceipts(
	_ context.Context,
	limit int32,
) (WebhookReconciliationResult, error) {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	reconciler.calls++
	reconciler.limit = limit
	return reconciler.result, reconciler.err
}

func (reconciler *recordingWebhookReceiptReconciler) snapshot() (int, int32) {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	return reconciler.calls, reconciler.limit
}
