package canvas

import (
	"errors"
	"time"
)

func (s *DefaultPresentationService) observeCapture(
	operation, outcome string,
	started time.Time,
	result Presentation,
	err error,
) {
	if s.captureObserver == nil {
		return
	}
	switch {
	case outcome == "mismatch":
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrNotFound):
		outcome = "denied"
	case errors.Is(err, ErrConflict):
		outcome = "conflict"
	case errors.Is(err, ErrInvalid):
		outcome = "invalid"
	case err != nil:
		outcome = "error"
	case len(result.Issues) > 0:
		outcome = "issues"
	}
	s.captureObserver(operation, outcome, time.Since(started))
}
