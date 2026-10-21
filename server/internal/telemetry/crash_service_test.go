package telemetry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
)

// newUnvalidatedStoreService builds a service over a zero-valued store whose
// query set is nil. Every submission this service accepts fails at the insert
// with ErrStore, so an ErrInvalidInput result proves the submission was
// rejected before it could reach the database. Storage behavior itself is
// covered by tests/integration/telemetry.
func newUnvalidatedStoreService(t *testing.T) *DefaultCrashReportService {
	t.Helper()
	service, err := NewCrashReportService(CrashReportServiceOptions{Store: new(db.Store)})
	if err != nil {
		t.Fatalf("NewCrashReportService() error = %v", err)
	}
	return service
}

func validSubmissionFixture() Submission {
	return Submission{
		Type:         ReportTypeCrash,
		Platform:     "darwin",
		Architecture: "arm64",
		AppVersion:   "0.1.0",
		Reason:       "TypeError: renderer render-process-gone reason=crashed exitCode=134",
	}
}

func TestNewServiceRequiresStore(t *testing.T) {
	t.Parallel()
	if _, err := NewCrashReportService(CrashReportServiceOptions{}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("NewCrashReportService() error = %v", err)
	}
	var service *DefaultCrashReportService
	if err := service.Submit(context.Background(), validSubmissionFixture()); !errors.Is(err, ErrStore) {
		t.Fatalf("nil service Submit() error = %v", err)
	}
}

func TestSubmitRejectsInvalidSubmissions(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*Submission){
		"unknown kind":        func(s *Submission) { s.Type = "hang" },
		"empty platform":      func(s *Submission) { s.Platform = "" },
		"oversize platform":   func(s *Submission) { s.Platform = strings.Repeat("p", 65) },
		"padded platform":     func(s *Submission) { s.Platform = " darwin" },
		"control platform":    func(s *Submission) { s.Platform = "dar\twin" },
		"empty architecture":  func(s *Submission) { s.Architecture = "" },
		"oversize version":    func(s *Submission) { s.AppVersion = strings.Repeat("v", 121) },
		"empty reason":        func(s *Submission) { s.Reason = "" },
		"blank reason":        func(s *Submission) { s.Reason = " \n\t " },
		"nul reason":          func(s *Submission) { s.Reason = "crash\x00stack" },
		"invalid utf8 reason": func(s *Submission) { s.Reason = "crash\xff" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			service := newUnvalidatedStoreService(t)
			submission := validSubmissionFixture()
			mutate(&submission)
			// ErrInvalidInput (rather than this service's unavoidable
			// ErrStore) proves the submission never reached the insert.
			if err := service.Submit(context.Background(), submission); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Submit() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestBoundedReasonTrimsBeforeAndAfterRedaction(t *testing.T) {
	t.Parallel()
	// A multi-byte rune straddling the bound must not be split.
	oversize := strings.Repeat("a", MaxReasonBytes-1) + "é"
	bounded := boundedReason(oversize)
	if len(bounded) > MaxReasonBytes || strings.ContainsRune(bounded, 'é') {
		t.Fatalf("bounded reason = %d bytes, tail %q", len(bounded), bounded[len(bounded)-4:])
	}
	// A secret that starts inside the bound is either dropped or redacted,
	// never persisted verbatim.
	secret := "token=verysecretvalue1234"
	input := strings.Repeat("b", MaxReasonBytes-10) + secret
	if redacted := boundedReason(input); strings.Contains(redacted, "verysecretvalue1234") {
		t.Fatalf("bounded reason leaked truncated secret: %q", redacted[len(redacted)-64:])
	}
}

func TestBoundedReasonRedactsEmailAndCredentials(t *testing.T) {
	t.Parallel()
	got := boundedReason("Failed for alice@example.com with password=hunter2")
	want := "Failed for [redacted:email] with password=[redacted:kv-secret]"
	if got != want {
		t.Fatalf("boundedReason() = %q, want %q", got, want)
	}
}
