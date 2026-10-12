package conversation

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestHuddleUsageRejectsInvalidStopReason(t *testing.T) {
	t.Parallel()
	for _, in := range []HuddleAIUsage{
		{ModelSessionID: "session", Closed: true, StopReason: "private arbitrary text"},
		{ModelSessionID: "session", StopReason: "provider_error"},
	} {
		service := &DefaultHuddleAIService{}
		if err := service.Finish(t.Context(), uuid.New(), "token", in); !errors.Is(err, ErrHuddleInvalid) {
			t.Fatalf("got = %v, want invalid input", err)
		}
	}
}
