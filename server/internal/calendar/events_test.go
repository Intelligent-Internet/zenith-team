package calendar

import (
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
)

func TestCalendarChangedAudienceIncludesRemovedInviteesWithoutLeakingPrivateEvents(t *testing.T) {
	actor, removed, added, company := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	id := company.String()
	p := auth.UserPrincipal{UserID: actor, CompanyID: company}
	before := Event{
		CalendarType:    "personal",
		CompanyID:       &id,
		OrganizerUserID: actor.String(),
		Attendees:       []Attendee{{UserID: removed.String()}},
	}
	after := before
	after.Attendees = []Attendee{{UserID: added.String()}}
	changes := calendarChangedEvents(p, notificationChange{action: "update", before: before, after: after})
	seen := map[uuid.UUID]bool{}
	for _, event := range changes {
		payload, err := events.PayloadOf[Changed](event)
		if err != nil || payload.CompanyWide {
			t.Fatalf("private event widened: %+v, %v", payload, err)
		}
		seen[payload.UserID] = true
	}
	if len(seen) != 3 || !seen[actor] || !seen[added] || !seen[removed] {
		t.Fatalf("audience = %v", seen)
	}
	personal := calendarChangedEvents(p, notificationChange{action: "complete", before: before, after: after})
	if len(personal) != 1 {
		t.Fatalf("personal completion audience = %+v", personal)
	}
	after.CalendarType = "company"
	shared := calendarChangedEvents(p, notificationChange{action: "update", after: after})
	found := false
	for _, event := range shared {
		payload, _ := events.PayloadOf[Changed](event)
		found = found || payload.CompanyWide
	}
	if !found {
		t.Fatal("company calendar has no Company invalidation")
	}
}
