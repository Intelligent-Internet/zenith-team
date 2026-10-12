package calendar

import (
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
)

const (
	Topic        = "calendar"
	EventChanged = "calendar.changed"
)

// Changed carries only routing facts. Calendar contents are read with current
// access after the opaque hint, including by an invitee who was just removed.
type Changed struct {
	CompanyWide bool      `json:"-"`
	UserID      uuid.UUID `json:"-"`
}

func calendarChangedEvents(p auth.UserPrincipal, change notificationChange) []events.Event {
	users := map[uuid.UUID]bool{p.UserID: true}
	companyWide := false
	if change.action != "complete" && change.action != "reopen" {
		for _, value := range []Event{change.before, change.after} {
			companyWide = companyWide || value.CalendarType == "company"
			if value.OrganizerUserID != "" {
				users[uuid.MustParse(value.OrganizerUserID)] = true
			}
			for _, attendee := range value.Attendees {
				users[uuid.MustParse(attendee.UserID)] = true
			}
		}
	}
	frame := events.Event{
		EntityType: Topic, Name: EventChanged, CompanyID: p.CompanyID,
		CreatedAt: time.Now().UTC(), Actor: events.SystemActor(),
	}
	if companyWide {
		frame.Payload = Changed{CompanyWide: true}
		return []events.Event{frame}
	}
	result := make([]events.Event, 0, len(users))
	for user := range users {
		frame.Payload = Changed{UserID: user}
		result = append(result, frame)
	}
	return result
}
