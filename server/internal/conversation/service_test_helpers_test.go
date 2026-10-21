package conversation

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func testTaskMessageRuns() TaskMessageRunPreparer {
	return TaskMessageRunPreparerFuncs{
		Check: func(context.Context, *generated.Queries, generated.Task) (bool, error) {
			return true, nil
		},
		Prepare: func(
			context.Context,
			*generated.Queries,
			*[]events.Event,
			uuid.UUID,
			generated.Task,
		) (bool, error) {
			return true, nil
		},
	}
}

func newTestConversationService(
	t *testing.T,
	store *db.Store,
	bus *events.Bus,
) *DefaultConversationService {
	t.Helper()
	service, err := NewConversationService(ServiceOptions{
		Store: store, Bus: bus, TaskMessageRuns: testTaskMessageRuns(),
	})
	if err != nil {
		t.Fatalf("construct Conversation service: %v", err)
	}
	return service
}
