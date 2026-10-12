package company

import (
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/events"
)

// newResourceEvent stages the resource.created / resource.updated event whose
// payload is exactly the Resource response served over HTTP. The Company the
// frame belongs to is already on that projection.
func newResourceEvent(
	actor events.Actor,
	name string,
	resource ResourceResponse,
) events.Event {
	return events.Event{
		EntityType: TopicResource,
		Name:       name,
		CompanyID:  resource.CompanyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      actor,
		Payload:    resource,
	}
}

// newResourceDeletedEvent carries only the deleted identity and its terminal
// version; the row is gone, so there is no projection to snapshot. The
// Company id rides along off the wire, because the frame still has to be
// routed to that Company.
func newResourceDeletedEvent(
	actor events.Actor,
	companyID, resourceID uuid.UUID,
	terminalVersion int64,
) events.Event {
	return events.Event{
		EntityType: TopicResource,
		Name:       EventResourceDeleted,
		CompanyID:  companyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      actor,
		Payload: ResourceRemoval{
			ResourceID: resourceID,
			Revision:   terminalVersion,
			CompanyID:  companyID,
		},
	}
}
