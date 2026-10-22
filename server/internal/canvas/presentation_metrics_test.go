package canvas

import (
	"errors"
	"testing"
	"time"
)

func TestCaptureObservationClassifiesOutcomes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		outcome string
		err     error
		issues  []string
		want    string
	}{
		{name: "prepared", outcome: "prepared", want: "prepared"},
		{name: "saved", outcome: "saved", want: "saved"},
		{name: "replayed", outcome: "replayed", want: "replayed"},
		{name: "mismatch", outcome: "mismatch", err: ErrConflict, want: "mismatch"},
		{name: "stale", outcome: "saved", err: ErrConflict, want: "conflict"},
		{name: "forbidden", err: ErrForbidden, want: "denied"},
		{name: "missing", err: ErrNotFound, want: "denied"},
		{name: "invalid", err: ErrInvalid, want: "invalid"},
		{name: "failure", err: errors.New("private failure details"), want: "error"},
		{name: "issues", outcome: "prepared", issues: []string{"private issue content"}, want: "issues"},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			service := NewPresentationService(
				nil,
				nil,
				nil,
				WithPresentationCaptureObserver(
					func(operation, outcome string, duration time.Duration) {
						called = true
						if operation != "prepare" || outcome != test.want || duration < 0 {
							t.Errorf(
								"observation = %s, %s, %s; want prepare, %s, nonnegative",
								operation,
								outcome,
								duration,
								test.want,
							)
						}
					},
				),
			)
			service.observeCapture(
				"prepare",
				test.outcome,
				time.Now(),
				Presentation{Issues: test.issues},
				test.err,
			)
			if !called {
				t.Fatal("observer was not called")
			}
		})
	}
}
