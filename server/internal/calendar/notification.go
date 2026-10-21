package calendar

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// Both HTTP and Run-scoped tools stage Inbox writes in the event transaction.
// Presentation is deferred until the event and any tool receipt have committed.
type mutation struct {
	principal auth.UserPrincipal
	actor     inbox.Actor
	pending   []events.Event
	created   []inbox.CreateResult
}

type notificationChange struct {
	action         string
	before, after  Event
	occurrenceDate string
	responseKey    string
}

type noticeDetails struct {
	title, body, key, date string
	openEvent              bool
}

func (s *DefaultCalendarService) publishMutation(ctx context.Context, m mutation) {
	for _, group := range events.GroupByTopic(m.pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	for _, result := range m.created {
		inbox.PresentCreated(s.presenter, result)
	}
}

func (m *mutation) notifyInTx(ctx context.Context, q *generated.Queries, change notificationChange) error {
	m.pending = append(m.pending, calendarChangedEvents(m.principal, change)...)
	for _, notice := range change.notices(m.principal.UserID.String()) {
		notice.Actor = m.actor
		result, err := inbox.CreateInTx(ctx, q, &m.pending, notice)
		if err != nil {
			return err
		}
		if result.Created {
			m.created = append(m.created, result)
		}
	}
	return nil
}

// Visibility alone never subscribes every company member. Notifications go to
// the organizer and affected invitees, excluding the person making the change.
// Complete/reopen receipts are personal state, not changes to the shared event.
func (c notificationChange) notices(actorID string) []inbox.CreateInput {
	var notices []inbox.CreateInput
	add := func(event Event, userID string, details noticeDetails) {
		if userID == actorID || event.CompanyID == nil {
			return
		}
		link := "/calendar?date=" + details.date
		if details.openEvent {
			link = "/calendar?eventId=" + event.ID + "&date=" + details.date
		}
		notices = append(notices, inbox.CreateInput{
			CompanyID:       uuid.MustParse(*event.CompanyID),
			RecipientUserID: uuid.MustParse(userID),
			Type:            inbox.TypeCalendarUpdate,
			Entity:          inbox.Entity{Type: protocol.EntityCalendar, ID: event.ID},
			Title:           details.title,
			Body:            details.body,
			DeepLink:        &link,
			DedupeKey:       "calendar-update:" + event.ID + ":" + details.key,
		})
	}
	afterDate := strings.SplitN(c.after.StartLocal, "T", 2)[0]
	beforeDate := strings.SplitN(c.before.StartLocal, "T", 2)[0]
	revision := fmt.Sprint(c.after.Revision)
	switch c.action {
	case "create", "update":
		oldGuests, newGuests := attendeeIDs(c.before), attendeeIDs(c.after)
		for _, guest := range c.after.Attendees {
			if _, existed := oldGuests[guest.UserID]; !existed {
				add(c.after, guest.UserID, noticeDetails{
					title: calendarNoticeTitle(
						calendarNoticeText(c.after.OrganizerName, 48)+" invited you to ",
						c.after.Title,
					),
					body:      calendarSummary(c.after),
					key:       "invited:" + revision,
					date:      afterDate,
					openEvent: true,
				})
			}
		}
		for _, guest := range c.before.Attendees {
			if _, remains := newGuests[guest.UserID]; !remains {
				add(c.before, guest.UserID, noticeDetails{
					title: calendarNoticeTitle(
						"Invitation removed: ",
						c.before.Title,
					),
					body: calendarSummary(c.before),
					key:  "removed:" + revision,
					date: beforeDate,
				})
			}
		}
		if c.action == "create" {
			break
		}
		changes := changedDetails(c.before, c.after)
		if !sameGuestList(oldGuests, newGuests) {
			changes = append(changes, "guests")
		}
		if len(changes) == 0 {
			break
		}
		body := calendarSummary(c.after) + " · Updated: " + strings.Join(changes, ", ")
		details := noticeDetails{
			title:     calendarNoticeTitle("Updated: ", c.after.Title),
			body:      body,
			key:       "updated:" + revision,
			date:      afterDate,
			openEvent: true,
		}
		add(c.after, c.after.OrganizerUserID, details)
		for _, guest := range c.after.Attendees {
			if _, existed := oldGuests[guest.UserID]; !existed {
				continue // The invitation already contains the new details.
			}
			guestBody := body
			if scheduleChanged(c.before, c.after) {
				guestBody += ". Please respond again."
			}
			details.body = guestBody
			add(c.after, guest.UserID, details)
		}
	case "respond":
		for _, guest := range c.after.Attendees {
			if guest.UserID == actorID {
				verb := guest.ResponseStatus
				if verb == "tentative" {
					verb = "may attend"
				}
				title := calendarNoticeTitle(
					calendarNoticeText(guest.DisplayName, 48)+" "+verb+" ",
					c.after.Title,
				)
				add(c.after, c.after.OrganizerUserID, noticeDetails{
					title:     title,
					body:      calendarSummary(c.after),
					key:       "response:" + c.responseKey,
					date:      afterDate,
					openEvent: true,
				})
				break
			}
		}
	case "delete", "cancel":
		date, key := beforeDate, "deleted"
		body := calendarSummary(c.before)
		if c.action == "cancel" {
			date, key = c.occurrenceDate, "cancelled:"+c.occurrenceDate
			day, _ := time.Parse(time.DateOnly, date)
			body = day.Format("Mon, 2 Jan 2006") + " · This occurrence only."
		}
		details := noticeDetails{
			title: calendarNoticeTitle("Cancelled: ", c.before.Title),
			body:  body,
			key:   key,
			date:  date,
		}
		add(c.before, c.before.OrganizerUserID, details)
		for _, guest := range c.before.Attendees {
			add(c.before, guest.UserID, details)
		}
	}
	return notices
}

func calendarNoticeText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return strings.TrimSpace(string([]rune(value)[:limit-1])) + "…"
}

func calendarNoticeTitle(prefix, title string) string {
	return prefix + "“" + calendarNoticeText(title, 158-utf8.RuneCountInString(prefix)) + "”"
}

func calendarSummary(event Event) string {
	if event.AllDay {
		start, _ := time.Parse(time.DateOnly, event.StartLocal)
		end, _ := time.Parse(time.DateOnly, event.EndLocal)
		last := end.AddDate(0, 0, -1)
		when := start.Format("Mon, 2 Jan 2006")
		if !start.Equal(last) {
			when += " – " + last.Format("Mon, 2 Jan 2006")
		}
		return when + " · All day"
	}
	// Dates and zones are canonical persisted Calendar values. Conversion uses
	// the event's zone, including its offset at each end of a DST transition.
	zone, _ := time.LoadLocation(event.TimeZone)
	start, _ := time.ParseInLocation("2006-01-02T15:04", event.StartLocal, zone)
	end, _ := time.ParseInLocation("2006-01-02T15:04", event.EndLocal, zone)
	startZone, endZone := calendarOffset(start), calendarOffset(end)
	if start.Format(time.DateOnly) == end.Format(time.DateOnly) {
		when := start.Format("Mon, 2 Jan 2006 · 15:04")
		if startZone != endZone {
			return when + " (" + startZone + ") – " + end.Format("15:04") + " (" + endZone + ")"
		}
		return when + "–" + end.Format("15:04") + " (" + startZone + ")"
	}
	when := start.Format("Mon, 2 Jan 2006, 15:04")
	if startZone != endZone {
		when += " (" + startZone + ")"
	}
	return when + " – " + end.Format("Mon, 2 Jan 2006, 15:04") + " (" + endZone + ")"
}

func calendarOffset(value time.Time) string {
	_, offset := value.Zone()
	if offset == 0 {
		return "UTC"
	}
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	zone := fmt.Sprintf("UTC%s%d", sign, offset/3600)
	if minutes := offset % 3600 / 60; minutes != 0 {
		zone += fmt.Sprintf(":%02d", minutes)
	}
	return zone
}

func attendeeIDs(event Event) map[string]bool {
	result := make(map[string]bool, len(event.Attendees))
	for _, attendee := range event.Attendees {
		result[attendee.UserID] = true
	}
	return result
}

func sameGuestList(before, after map[string]bool) bool {
	if len(before) != len(after) {
		return false
	}
	for id := range before {
		if _, ok := after[id]; !ok {
			return false
		}
	}
	return true
}

func scheduleChanged(before, after Event) bool {
	return before.StartLocal != after.StartLocal || before.EndLocal != after.EndLocal ||
		before.AllDay != after.AllDay || before.TimeZone != after.TimeZone ||
		before.RecurrenceInterval != after.RecurrenceInterval || !slices.Equal(before.RecurrenceWeekdays, after.RecurrenceWeekdays) ||
		before.RecurrenceMonthlyMode != after.RecurrenceMonthlyMode || !sameRecurrenceCount(before.RecurrenceCount, after.RecurrenceCount) ||
		before.Recurrence != after.Recurrence || optionalDate(before.RecurrenceUntil) != optionalDate(after.RecurrenceUntil)
}

func optionalDate(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func changedDetails(before, after Event) []string {
	var changes []string
	if scheduleChanged(before, after) {
		changes = append(changes, "schedule")
	}
	if before.Title != after.Title {
		changes = append(changes, "title")
	}
	if before.Description != after.Description {
		changes = append(changes, "description")
	}
	if before.Location != after.Location {
		changes = append(changes, "location")
	}
	if before.Type != after.Type {
		changes = append(changes, "event type")
	}
	return changes
}

func sameRecurrenceCount(a, b *int32) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
