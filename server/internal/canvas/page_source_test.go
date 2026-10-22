package canvas

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

func TestPageSourceMetricsClassifyOutcomes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, want string
		err        error
		rejected   bool
	}{
		{"accepted", "accepted", nil, false},
		{"diagnostic", "rejected", nil, true},
		{"invalid", "rejected", ErrInvalid, false},
		{"compiler", "rejected", &wiki.SourceDiagnostic{Code: "source.component"}, false},
		{"size", "rejected", wiki.ErrPageBodyTooLarge, false},
		{"conflict", "conflict", ErrConflict, false},
		{"native conflict", "conflict", wiki.ErrPageConflict, false},
		{"denied", "denied", ErrForbidden, false},
		{"unavailable", "denied", ErrNotFound, false},
		{"rollback", "disabled", wiki.ErrUIAuthoringDisabled, false},
		{"error", "error", errors.New("internal error"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			service := NewPageService(
				nil,
				nil,
				WithPageSourceObserver(
					func(operation, outcome string, duration time.Duration, bytes int) {
						calls++
						if operation != "check" || outcome != test.want || duration < 0 ||
							bytes != 9 {
							t.Fatalf(
								"observation = %s, %s, %v, %d; want check, %s, nonnegative, 9",
								operation,
								outcome,
								duration,
								bytes,
								test.want,
							)
						}
					},
				),
			)
			service.observeSource(
				pageSourceObservation{
					operation: "check",
					bytes:     9,
					started:   time.Now(),
					rejected:  test.rejected,
				},
				test.err,
			)
			if calls != 1 {
				t.Fatalf("observations = %d, want 1", calls)
			}
		})
	}
}

func TestPageSourceMetricsIncludeEarlyRejections(t *testing.T) {
	t.Parallel()
	source := "<日本語"
	var got []string
	service := NewPageService(
		nil,
		nil,
		WithPageSourceObserver(func(operation, outcome string, duration time.Duration, bytes int) {
			if outcome != "rejected" || duration < 0 || bytes != len(source) {
				t.Fatalf("rejection = %s, %v, %d", outcome, duration, bytes)
			}
			got = append(got, operation)
		}),
	)
	principal := auth.UserPrincipal{}
	if _, err := service.CreatePage(
		t.Context(),
		principal,
		uuid.Nil,
		PageCreateInput{Source: &source, LanguageVersion: 1},
	); err == nil {
		t.Fatal("invalid create accepted")
	}
	if _, err := service.EditPageForRun(
		t.Context(),
		principal,
		uuid.Nil,
		uuid.Nil,
		PageEditInput{Operations: []wiki.BlockOperation{{Type: "insert", Source: &source}}},
	); err == nil {
		t.Fatal("invalid proposal accepted")
	}
	if _, err := service.PreviewPageSource(
		t.Context(),
		principal,
		uuid.Nil,
		uuid.Nil,
		PageSourcePreviewInput{Source: &source},
	); err == nil {
		t.Fatal("invalid check accepted")
	}
	// Reads and native operations do not contribute source-submission samples.
	_, _ = service.PreviewPageSource(t.Context(), principal, uuid.Nil, uuid.Nil, PageSourcePreviewInput{})
	if len(got) != 3 || got[0] != "create" || got[1] != "proposal" || got[2] != "check" {
		t.Fatalf("operations = %v, want create/proposal/check only", got)
	}
}
