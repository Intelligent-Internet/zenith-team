package schedule

import (
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
)

const (
	Topic        = "schedule"
	EventChanged = "schedule.changed"
)

type Changed struct {
	ID            uuid.UUID `json:"id"`
	CreatorUserID uuid.UUID `json:"-"`
}

func changedEvent(value Response) events.Event {
	return events.Event{
		EntityType: Topic, Name: EventChanged,
		CompanyID: value.CompanyID, CreatedAt: time.Now().UTC(), Actor: events.SystemActor(),
		Payload: Changed{ID: value.ID, CreatorUserID: value.CreatorUserID},
	}
}
