package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"boardly.dev/boardly/server/internal/approval"
	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/schedule"
	"boardly.dev/boardly/server/internal/telemetry"
)

// How often each background job runs. They live together because "what runs
// on a timer, and how often" is one question, and answering it used to mean
// opening four packages.
//
// Human-input decisions also wake their worker immediately; its timer recovers
// missed events and retries durable receipts. Crash-report retention is
// bookkeeping nobody waits for, so it runs rarely.
const (
	attachmentCleanupInterval = 30 * time.Second
	humanInputInterval        = 30 * time.Second
	huddleLifecycleInterval   = 5 * time.Second
	crashRetentionInterval    = 15 * time.Minute
)

// backgroundWorker is one long-lived job serveApplication supervises. A
// worker that returns before shutdown takes the server down with it — nothing
// else is watching the work it was meant to keep doing.
type backgroundWorker struct {
	name string
	run  func(context.Context) error
}

// repeating builds a worker that runs one bounded pass every interval until
// the context ends. That is the shape of every reconciler here — claim a
// bounded batch, record what failed, come back — and each one used to
// hand-roll the same ticker and select.
//
// The first pass is immediate: after a restart the backlog should be worked
// at once rather than sit for a whole interval.
func repeating(name string, every time.Duration, pass func(context.Context)) backgroundWorker {
	return repeatingOnWake(
		name,
		every,
		pass,
		nil,
	)
}

func repeatingOnWake(
	name string,
	every time.Duration,
	pass func(context.Context),
	wake <-chan struct{},
) backgroundWorker {
	return backgroundWorker{name: name, run: func(ctx context.Context) error {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			pass(ctx)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			case <-wake:
			}
		}
	}}
}

func newHumanInputLifecycleWorker(bus *events.Bus, pass func(context.Context)) (backgroundWorker, error) {
	// One pending signal coalesces bursts while a pass is running. Publishing
	// a committed decision never waits for reconciliation or starts a goroutine.
	wake := make(chan struct{}, 1)
	for _, name := range []string{approval.EventApprovalResolved, approval.EventQuestionResolved} {
		if err := bus.Subscribe(name, func(context.Context, events.Event) error {
			select {
			case wake <- struct{}{}:
			default:
			}
			return nil
		}); err != nil {
			return backgroundWorker{}, fmt.Errorf("register human-input lifecycle wakeup: %w", err)
		}
	}
	return repeatingOnWake(
		"human-input-lifecycle",
		humanInputInterval,
		pass,
		wake,
	), nil
}

// settling is repeating with the first pass one interval away.
//
// The runtime sweep waits because it decides liveness from heartbeat age. A
// restart longer than the stale threshold would otherwise mark the whole
// fleet offline in the moment before their daemons reconnect, and flip them
// all back seconds later — a burst of status frames describing nothing.
func settling(name string, every time.Duration, pass func(context.Context)) backgroundWorker {
	return backgroundWorker{name: name, run: func(ctx context.Context) error {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				pass(ctx)
			}
		}
	}}
}

func newAttachmentCleanupIssueHandler(logger *slog.Logger) func(attachment.CleanupIssue) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(issue attachment.CleanupIssue) {
		errorCode := "invalid_store_data"
		switch issue {
		case attachment.CleanupIssuePrepareFailed,
			attachment.CleanupIssueClaimFailed,
			attachment.CleanupIssueDeleteFailed,
			attachment.CleanupIssueInvalidStoreData:
			errorCode = string(issue)
		}
		logger.Warn(
			"Attachment cleanup failed",
			"component", "attachment-cleanup",
			"error_code", errorCode,
			"operation", "cleanup_expired_abandoned_or_deleting_attachments",
		)
	}
}

func newPublicationIssueHandler(logger *slog.Logger) func(execution.PublicationIssueReport) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(report execution.PublicationIssueReport) {
		logger.Warn(
			"Output publication processing failed",
			"component", "output-publication",
			"error_code", string(report.Issue),
			"operation", "process_output_publication",
			"publication_id", report.PublicationID,
			"attempt", report.Attempt,
			"provider_type", report.ProviderType,
			"error", report.Err,
		)
	}
}

func newCrashReportRetentionIssueHandler(logger *slog.Logger) func(telemetry.RetentionIssue) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(issue telemetry.RetentionIssue) {
		errorCode := "invalid_store_data"
		if issue == telemetry.RetentionIssuePurgeFailed {
			errorCode = string(issue)
		}
		logger.Warn(
			"crash report retention failed",
			"component", "crash-report-retention",
			"error_code", errorCode,
			"operation", "purge_expired_crash_reports",
		)
	}
}

func newMailboxRetentionErrorHandler(logger *slog.Logger) func(error) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(error) {
		logger.Warn(
			"Mailbox retention failed",
			"component", "mailbox-retention",
			"error_code", "purge_failed",
			"operation", "purge_expired_mailbox_metadata",
		)
	}
}

func newAgentMailWorkerErrorHandler(logger *slog.Logger) func(error) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(error) {
		logger.Warn(
			"Agent Mail send processing failed",
			"component", "agent-mail-send",
			"error_code", "send_processing_failed",
			"operation", "process_queued_mail_drafts",
		)
	}
}

func newScheduleFiringIssueHandler(logger *slog.Logger) func(schedule.FiringIssue) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(issue schedule.FiringIssue) {
		fields := []any{
			"component",
			"schedule",
			"path",
			issue.Path,
			"schedule_id",
			issue.ScheduleID,
			"company_id",
			issue.CompanyID,
			"error_code",
			issue.Code,
			"error",
			issue.Err,
		}
		if len(issue.Stack) > 0 {
			fields = append(fields, "stack", string(issue.Stack))
		}
		if issue.Code == "noop" || issue.Code == "duplicate" {
			logger.Debug("Schedule firing", fields...)
			return
		}
		logger.Warn("Schedule firing needs attention", fields...)
	}
}
