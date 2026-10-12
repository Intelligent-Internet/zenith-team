// Package telemetry owns Boardly's operational telemetry: the low-cardinality
// Prometheus registry, and opt-in redacted crash/freeze reports with their
// 30-day retention.
//
// Crash reports are anonymous operational diagnostics. A report carries only
// the type (crash or freeze), platform, architecture, app/daemon version, a
// bounded redacted reason. PostgreSQL assigns the receipt timestamp. The
// service trims the reason to its 16 KiB bound and applies the shared redactor
// (server/pkg/redact) before commit regardless of any client-side pass, so
// no prompt, message, email, or tool payload is ever persisted.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"boardly.dev/boardly/server/internal/db"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/redact"
)

type CrashReportServiceOptions struct {
	Store *db.Store
}

// CrashReportService owns the opt-in crash and freeze report submissions and
// the bounding and redaction every submission passes through before commit.
type CrashReportService interface {
	Submit(context.Context, Submission) error
}

var _ CrashReportService = (*DefaultCrashReportService)(nil)

// DefaultCrashReportService validates, bounds, and redacts submissions before
// commit. The server-side redaction pass always runs, so a client that skipped
// its own equivalent pass still never persists secrets or emails.
//
// Submissions are single-statement writes, so the service calls the insert
// through the pool's query set rather than opening a transaction.
type DefaultCrashReportService struct {
	store *db.Store
}

func NewCrashReportService(options CrashReportServiceOptions) (*DefaultCrashReportService, error) {
	if options.Store == nil {
		return nil, ErrConfiguration
	}
	return &DefaultCrashReportService{store: options.Store}, nil
}

func (s *DefaultCrashReportService) Submit(ctx context.Context, submission Submission) error {
	if s == nil || s.store == nil || ctx == nil {
		return ErrStore
	}
	report, err := s.redactedReport(submission)
	if err != nil {
		return err
	}
	if err := insertCrashReport(ctx, s.store.Queries(), report); err != nil {
		// The insert publishes diagnosable detail under its own ErrStore tag;
		// any other failure is collapsed so transport internals never reach
		// the public error.
		if errors.Is(err, ErrStore) {
			return err
		}
		return ErrStore
	}
	return nil
}

// insertCrashReport persists one already-normalized and redacted report. It
// takes the query set as a parameter, so a caller may pass the pool handle or
// a transaction. PostgreSQL generates the row identity and receipt timestamp.
func insertCrashReport(ctx context.Context, q *generated.Queries, report Report) error {
	if q == nil || ctx == nil {
		return fmt.Errorf("%w: store is not configured", ErrStore)
	}
	err := q.InsertCrashReport(ctx, generated.InsertCrashReportParams{
		Kind:         string(report.Type),
		Platform:     report.Platform,
		Architecture: report.Architecture,
		AppVersion:   report.AppVersion,
		Reason:       report.Reason,
	})
	if err != nil {
		return fmt.Errorf("%w: insert crash report: %w", ErrStore, err)
	}
	return nil
}

func (s *DefaultCrashReportService) redactedReport(submission Submission) (Report, error) {
	if !validSubmission(submission) {
		return Report{}, ErrInvalidInput
	}
	reason := boundedReason(submission.Reason)
	if reason == "" {
		return Report{}, ErrInvalidInput
	}
	return Report{
		Type:         submission.Type,
		Platform:     submission.Platform,
		Architecture: submission.Architecture,
		AppVersion:   submission.AppVersion,
		Reason:       reason,
	}, nil
}

func validSubmission(submission Submission) bool {
	return (submission.Type == ReportTypeCrash || submission.Type == ReportTypeFreeze) &&
		canonicalField(submission.Platform, maxPlatformBytes) &&
		canonicalField(submission.Architecture, maxPlatformBytes) &&
		canonicalField(submission.AppVersion, maxAppVersionBytes) &&
		utf8.ValidString(submission.Reason) &&
		!strings.ContainsRune(submission.Reason, 0)
}

// boundedReason trims oversize input to the storage bound, applies the shared
// diagnostic redactor, and re-applies the bound because placeholder rewrites
// may lengthen text. Redacting placeholder output is a no-op, so a client
// that already ran the equivalent pass double-redacts safely.
func boundedReason(raw string) string {
	trimmed := truncateUTF8(raw, MaxReasonBytes)
	redacted := redact.Diagnostic(trimmed)
	return strings.TrimSpace(truncateUTF8(redacted, MaxReasonBytes))
}

func canonicalField(value string, maximumBytes int) bool {
	if len(value) == 0 || len(value) > maximumBytes ||
		!utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func truncateUTF8(value string, maximumBytes int) string {
	if len(value) <= maximumBytes {
		return value
	}
	cut := maximumBytes
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
