package company

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
)

// A event event is opaque by design — holding one proves only that its
// payload matches its topic. So this reads the event where anything else
// reads it: at a subscriber, after publication.
func TestCompanyUpdatedEventCarriesTheCanonicalCompanyProjection(t *testing.T) {
	t.Parallel()

	companyID := uuid.Must(uuid.NewV7())
	actorID := uuid.Must(uuid.NewV7())
	value := Response{
		ID: companyID, Name: "Boardly", Revision: 2,
		Role: "owner", Permissions: []auth.Permission{auth.PermissionCompanyDelete},
	}

	bus := companyTestBus(t)
	received := recordTopic(t, bus, Topic)

	built := companyUpdatedEvent(actorID.String(), value)
	bus.Publish(context.Background(), built.EntityType, built)

	published := received.snapshot()
	if len(published) != 1 {
		t.Fatalf("published events = %d, want 1", len(published))
	}
	event := published[0]
	// The frame reaches every member, so it never carries the actor's own
	// role or permissions: those are the viewer's, and each viewer keeps the
	// ones their last Company read gave them.
	want := value
	want.Role = ""
	want.Permissions = nil
	if event.Name != "company.updated" || !reflect.DeepEqual(event.Payload, want) ||
		event.Actor != events.UserActor(actorID.String()) {
		t.Fatalf("event = %#v", event)
	}
	// company.updated reaches the Company room and nobody else: the founder
	// routing fact belongs to company.created alone, and the projection
	// carries no other bookkeeping.
	if published := payloadOf[Response](t, event); published.FounderUserID != uuid.Nil {
		t.Fatalf("routing founder = %v, want the zero value", published.FounderUserID)
	}
}
