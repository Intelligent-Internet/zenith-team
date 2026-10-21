package company

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
)

// Staged events are opaque, so these read the frames at a subscriber — the
// same place the realtime bridge reads them.
func TestNewResourceEventCarriesResourceProjection(t *testing.T) {
	t.Parallel()

	actorID := uuid.Must(uuid.NewV7()).String()
	actor := events.UserActor(actorID)
	created := validResourceDTOForTest(t)

	bus := companyTestBus(t)
	log := recordTopic(t, bus, TopicResource)

	built := newResourceEvent(actor, EventResourceCreated, created)
	bus.Publish(context.Background(), built.EntityType, built)

	publishedCreated := log.named(EventResourceCreated)
	if len(publishedCreated) != 1 {
		t.Fatalf("published resource.created events = %d, want 1", len(publishedCreated))
	}
	createdEvent := publishedCreated[0]
	if createdEvent.Name != "resource.created" || createdEvent.Actor != actor ||
		!reflect.DeepEqual(createdEvent.Payload, created) {
		t.Fatalf("created event = %+v", createdEvent)
	}
	// The frame belongs to the resource's Company, and the projection says so.
	createdProjection := payloadOf[ResourceResponse](t, createdEvent)
	if createdProjection.CompanyID != created.CompanyID {
		t.Fatalf("routing company = %v, want %v", createdProjection.CompanyID, created.CompanyID)
	}
	createdPayload := marshalledPayload(t, createdEvent.Payload)
	if string(createdPayload["id"]) != `"`+created.ID.String()+`"` ||
		string(createdPayload["companyId"]) != `"`+created.CompanyID.String()+`"` ||
		string(createdPayload["revision"]) != "1" {
		t.Fatalf("created payload = %#v", createdPayload)
	}

	updated := created
	updated.Label = "Renamed"
	updated.Revision = 2
	updated.UpdatedAt = created.UpdatedAt.Add(time.Second)
	built = newResourceEvent(actor, EventResourceUpdated, updated)
	bus.Publish(context.Background(), built.EntityType, built)

	publishedUpdated := log.named(EventResourceUpdated)
	if len(publishedUpdated) != 1 {
		t.Fatalf("published resource.updated events = %d, want 1", len(publishedUpdated))
	}
	updatedEvent := publishedUpdated[0]
	updatedPayload := marshalledPayload(t, updatedEvent.Payload)
	if updatedEvent.Name != "resource.updated" || string(updatedPayload["label"]) != `"Renamed"` {
		t.Fatalf("updated payload = %#v", updatedPayload)
	}
}

func TestResourceDeletedEventCarriesOnlyIdentityAndTerminalVersion(t *testing.T) {
	t.Parallel()

	companyID := uuid.Must(uuid.NewV7())
	resourceID := uuid.Must(uuid.NewV7())

	bus := companyTestBus(t)
	received := recordTopic(t, bus, TopicResource)

	built := newResourceDeletedEvent(
		events.UserActor(uuid.Must(uuid.NewV7()).String()),
		companyID, resourceID, 3,
	)
	bus.Publish(context.Background(), built.EntityType, built)

	published := received.snapshot()
	if len(published) != 1 {
		t.Fatalf("published resource.deleted events = %d, want 1", len(published))
	}
	event := published[0]
	removal := payloadOf[ResourceRemoval](t, event)
	if event.Name != "resource.deleted" || removal.ResourceID != resourceID ||
		removal.Revision != 3 {
		t.Fatalf("deleted event = %+v", event)
	}
	// The row is gone, so the Company the frame belongs to rides the payload
	// — off the wire, which still carries identity and revision alone.
	if removal.CompanyID != companyID {
		t.Fatalf("routing company = %v, want %v", removal.CompanyID, companyID)
	}
	keys := marshalledPayload(t, event.Payload)
	if len(keys) != 2 || string(keys["resourceId"]) != `"`+resourceID.String()+`"` ||
		string(keys["revision"]) != "3" {
		t.Fatalf("deleted payload = %#v", keys)
	}
}

func marshalledPayload(t *testing.T, payload any) map[string]json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return decoded
}

func validResourceDTOForTest(t *testing.T) ResourceResponse {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	daemonID := "daemon-" + uuid.Must(uuid.NewV7()).String()
	return ResourceResponse{
		ID:               uuid.Must(uuid.NewV7()),
		CompanyID:        uuid.Must(uuid.NewV7()),
		ResourceType:     ResourceTypeLocalDirectory,
		ResourceRef:      ResourceRefResponse{DaemonID: daemonID, DaemonName: "Owner laptop"},
		DaemonStatus:     DaemonStatusOnline,
		DaemonLastSeenAt: &now,
		Label:            "Zenith Team",
		Position:         10,
		Metadata:         json.RawMessage(`{}`),
		CreatedBy: ActorRef{
			ID: uuid.Must(uuid.NewV7()), Kind: ActorTypeUser, DisplayName: "Owner",
		},
		CreatedAt: now,
		UpdatedAt: now,
		Revision:  1,
	}
}
