package calendar

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// ReminderService materializes due occurrences into the existing Inbox. The
// next due time and Inbox insert commit together; no client needs to be open.
type ReminderService interface {
	Deliver(context.Context, time.Time) error
}

var _ ReminderService = (*DefaultReminderService)(nil)

type DefaultReminderService struct {
	store     *db.Store
	bus       *events.Bus
	presenter inbox.Presenter
}

func NewReminderService(store *db.Store, bus *events.Bus, presenter inbox.Presenter) *DefaultReminderService {
	return &DefaultReminderService{store: store, bus: bus, presenter: presenter}
}

// Deliver is a bounded pass. Row locks serialize replicas, and Inbox dedupe
// keys preserve receipts if an edited event becomes due again in the same pass.
func (s *DefaultReminderService) Deliver(ctx context.Context, now time.Time) error {
	candidates, err := s.store.Queries().ListDueCalendarReminders(ctx, pgtype.Timestamptz{Time: now, Valid: true})
	if err != nil {
		return fmt.Errorf("list due calendar reminders: %w", err)
	}
	var failures []error
	for _, candidate := range candidates {
		if err := s.deliverEvent(ctx, candidate, now); err != nil {
			failures = append(failures, fmt.Errorf("deliver calendar reminder %s: %w", candidate.ID, err))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(failures...)
}

type reminderRecipient struct{ userID, companyID uuid.UUID }

func (s *DefaultReminderService) deliverEvent(
	ctx context.Context,
	candidate generated.ListDueCalendarRemindersRow,
	now time.Time,
) error {
	var pending []events.Event
	var created []inbox.CreateResult
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = nil
		created = nil
		recipients, err := lockReminderRecipients(ctx, q, candidate)
		if err != nil {
			return err
		}
		row, err := q.LockCalendarEvent(ctx, candidate.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		// An edit may have changed sharing, recipients or the schedule while this
		// worker waited. Its next pass will establish the new membership fences.
		if row.Version != candidate.Version || !row.ReminderNextAt.Valid || row.ReminderNextAt.Time.After(now) {
			return nil
		}
		event := eventResponse(row)
		zone, err := time.LoadLocation(event.TimeZone)
		if err != nil {
			return err
		}
		lead := time.Duration(*event.ReminderMinutes) * time.Minute
		occurrences, err := expand(event, now.Add(-24*time.Hour).Add(lead), now.Add(lead).Add(time.Nanosecond))
		if err != nil {
			return err
		}
		// RSVP can change without changing the series revision; read it under the
		// event lock, after all membership locks have been acquired.
		attendees, err := q.ListCalendarAttendees(ctx, []uuid.UUID{row.ID})
		if err != nil {
			return err
		}
		declined := map[uuid.UUID]bool{}
		for _, attendee := range attendees {
			declined[attendee.UserID] = attendee.ResponseStatus == "declined"
		}
		for _, occurrence := range occurrences {
			due := occurrence.StartAt.Add(-lead)
			if due.After(now) || !due.After(now.Add(-24*time.Hour)) {
				continue
			}
			for _, recipient := range recipients {
				if declined[recipient.userID] {
					continue
				}
				date, _ := time.Parse(time.DateOnly, occurrence.OccurrenceDate)
				suppressed, err := q.CalendarReminderSuppressed(
					ctx,
					generated.CalendarReminderSuppressedParams{
						EventID:        row.ID,
						UserID:         recipient.userID,
						OccurrenceDate: pgtype.Date{Time: date, Valid: true},
					},
				)
				if err != nil {
					return err
				}
				if suppressed.Bool {
					continue
				}
				link := "/calendar?eventId=" + row.ID.String() + "&date=" + occurrence.OccurrenceDate
				when := occurrence.StartAt.In(zone).
					Format("Mon, 2 Jan 2006 · 15:04") +
					" (" + calendarOffset(
					occurrence.StartAt.In(zone),
				) + ")"
				if event.AllDay {
					when = occurrence.StartAt.In(zone).Format("Mon, 2 Jan 2006") + " · All day"
				}
				result, err := inbox.CreateInTx(ctx, q, &pending, inbox.CreateInput{
					CompanyID:       recipient.companyID,
					RecipientUserID: recipient.userID,
					Type:            inbox.TypeCalendarReminder,
					Entity: inbox.Entity{
						Type: protocol.EntityCalendar,
						ID:   row.ID.String(),
					},
					Title:    calendarNoticeTitle("Reminder: ", event.Title),
					Body:     when,
					DeepLink: &link,
					DedupeKey: fmt.Sprintf(
						"calendar:%s:%s:%d",
						row.ID,
						occurrence.OccurrenceDate,
						row.Version,
					),
				})
				if err != nil {
					return err
				}
				if result.Created {
					created = append(created, result)
				}
			}
		}
		next, err := nextReminderTime(event, now)
		if err != nil {
			return err
		}
		return q.AdvanceCalendarReminder(ctx, generated.AdvanceCalendarReminderParams{ID: row.ID, NextAt: next})
	})
	if err != nil {
		return err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	for _, result := range created {
		inbox.PresentCreated(s.presenter, result)
	}
	return nil
}

// Personal owner User first, then Company, then memberships, then Event: the
// same lifecycle order used by account deletion and calendar mutations.
func lockReminderRecipients(
	ctx context.Context,
	q *generated.Queries,
	candidate generated.ListDueCalendarRemindersRow,
) ([]reminderRecipient, error) {
	recipients := []reminderRecipient{}
	if candidate.CalendarType == "personal" {
		eligible, err := inbox.LockRecipientInTx(ctx, q, uuid.Nil, candidate.UserID)
		if err != nil {
			return nil, err
		}
		if !eligible {
			return recipients, nil
		}
		recipients = append(recipients, reminderRecipient{userID: candidate.UserID})
	}
	if candidate.CompanyID == uuid.Nil {
		return recipients, nil
	}
	if _, err := q.LockCompanyForMembershipRead(ctx, candidate.CompanyID); errors.Is(err, pgx.ErrNoRows) {
		return recipients, nil
	} else if err != nil {
		return nil, err
	}
	attendees, err := q.ListCalendarAttendees(ctx, []uuid.UUID{candidate.ID})
	if err != nil {
		return nil, err
	}
	ids := []uuid.UUID{candidate.UserID}
	for _, attendee := range attendees {
		ids = append(ids, attendee.UserID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	for _, id := range ids {
		eligible, err := inbox.LockRecipientInTx(ctx, q, candidate.CompanyID, id)
		if err != nil {
			return nil, err
		}
		if eligible && (candidate.CalendarType == "company" || id != candidate.UserID) {
			recipients = append(recipients, reminderRecipient{userID: id, companyID: candidate.CompanyID})
		}
	}
	return recipients, nil
}

// Stop at the first future reminder without restarting counted series for each
// search window. Missing month dates and DST gaps follow the calendar itself.
func nextReminderTime(event Event, after time.Time) (pgtype.Timestamptz, error) {
	var result pgtype.Timestamptz
	if event.ReminderMinutes == nil {
		return result, nil
	}
	lead := time.Duration(*event.ReminderMinutes) * time.Minute
	err := walkOccurrences(
		event,
		after.Add(lead),
		time.Date(2201, 1, 1, 0, 0, 0, 0, time.UTC),
		func(occurrence Occurrence) bool {
			due := occurrence.StartAt.Add(-lead)
			if !due.After(after) {
				return true
			}
			result = pgtype.Timestamptz{Time: due, Valid: true}
			return false
		},
	)
	return result, err
}
