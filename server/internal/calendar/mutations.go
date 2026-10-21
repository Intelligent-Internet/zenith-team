package calendar

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type Command struct {
	Action           string `json:"action"`
	EventID          string `json:"eventId"`
	ExpectedRevision int64  `json:"expectedRevision"`
	Input            *Input `json:"input"`
	ResponseStatus   string `json:"responseStatus"`
	OccurrenceDate   string `json:"occurrenceDate"`
}

type CommandResult struct {
	Event *Event `json:"event"`
}

func (s *DefaultCalendarService) Create(ctx context.Context, p auth.UserPrincipal, input Input) (Event, error) {
	result, err := s.write(ctx, p, Command{Action: "create", Input: &input})
	if err != nil {
		return Event{}, err
	}
	return *result.Event, nil
}

func (s *DefaultCalendarService) Update(
	ctx context.Context,
	p auth.UserPrincipal,
	id string,
	input Input,
) (Event, error) {
	result, err := s.write(
		ctx,
		p,
		Command{Action: "update", EventID: id, ExpectedRevision: input.ExpectedRevision, Input: &input},
	)
	if err != nil {
		return Event{}, err
	}
	return *result.Event, nil
}

func (s *DefaultCalendarService) Delete(ctx context.Context, p auth.UserPrincipal, id string, revision int64) error {
	_, err := s.write(ctx, p, Command{Action: "delete", EventID: id, ExpectedRevision: revision})
	return err
}

func (s *DefaultCalendarService) Respond(
	ctx context.Context,
	p auth.UserPrincipal,
	id string,
	revision int64,
	status string,
) error {
	_, err := s.write(
		ctx,
		p,
		Command{Action: "respond", EventID: id, ExpectedRevision: revision, ResponseStatus: status},
	)
	return err
}

func (s *DefaultCalendarService) Occurrence(
	ctx context.Context,
	p auth.UserPrincipal,
	id string,
	input OccurrenceInput,
) error {
	_, err := s.write(
		ctx,
		p,
		Command{
			Action:           input.Action,
			EventID:          id,
			ExpectedRevision: input.ExpectedRevision,
			OccurrenceDate:   input.OccurrenceDate,
		},
	)
	return err
}

func (s *DefaultCalendarService) write(
	ctx context.Context,
	p auth.UserPrincipal,
	command Command,
) (CommandResult, error) {
	var result CommandResult
	m := mutation{principal: p, actor: inbox.Actor{Kind: "user", ID: &p.UserID}}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockActor(ctx, q, p); err != nil {
			return err
		}
		var err error
		result, err = m.writeInTx(ctx, q, command)
		return err
	})
	if err == nil {
		s.publishMutation(ctx, m)
	}
	return result, err
}

// Company and acting-membership locks fence concurrent revocation. They precede
// event locks, and sharing uses this same fence for all participant membership.
func lockActor(ctx context.Context, q *generated.Queries, p auth.UserPrincipal) error {
	if p.CompanyID != uuid.Nil {
		company, err := q.LockCompanyForMembershipRead(ctx, p.CompanyID)
		if err != nil {
			return calendarError(err)
		}
		if company.DeletingAt.Valid {
			return ErrNotFound
		}
		member, err := q.GetCompanyMemberForShare(
			ctx,
			generated.GetCompanyMemberForShareParams{CompanyID: p.CompanyID, UserID: p.UserID},
		)
		if err != nil {
			return calendarError(err)
		}
		if member.Status != "active" {
			return ErrNotFound
		}
		// Account deletion locks User before Company and revokes memberships
		// under that Company lock. Do not invert its order with a User SHARE.
		return nil
	}
	_, err := q.LockEnabledUserForMembershipMutation(ctx, p.UserID)
	if err != nil {
		return calendarError(err)
	}
	return nil
}

func (m *mutation) writeInTx(ctx context.Context, q *generated.Queries, command Command) (CommandResult, error) {
	p := m.principal
	if command.Action == "create" {
		if command.Input == nil || command.ExpectedRevision != 0 || command.Input.ExpectedRevision != 0 ||
			command.EventID != "" {
			return CommandResult{}, ErrInvalid
		}
		event, err := createInTx(ctx, q, p, *command.Input)
		if err != nil {
			return CommandResult{}, err
		}
		return CommandResult{
				Event: &event,
			}, m.notifyInTx(
				ctx,
				q,
				notificationChange{action: "create", after: event},
			)
	}
	id, err := uuid.Parse(command.EventID)
	if err != nil || id == uuid.Nil || command.ExpectedRevision < 1 {
		return CommandResult{}, ErrInvalid
	}
	// Visibility is checked before and after the lock. Never return unauthorized
	// event metadata through a conflict response or accept a stale role snapshot.
	visible, err := visibleEvent(ctx, q, p, id)
	if err != nil {
		return CommandResult{}, err
	}
	if visible.CompanyID != nil && *visible.CompanyID != p.CompanyID.String() {
		return CommandResult{}, ErrNotFound
	}
	row, err := q.LockCalendarEvent(ctx, id)
	if err != nil {
		return CommandResult{}, calendarError(err)
	}
	visible, err = visibleEvent(ctx, q, p, id)
	if err != nil {
		return CommandResult{}, err
	}
	edit := command.Action == "update" || command.Action == "delete" || command.Action == "cancel"
	if edit && !visible.CanEdit {
		return CommandResult{}, ErrNotFound
	}
	if row.Version != command.ExpectedRevision {
		return CommandResult{}, ErrConflict
	}
	change := notificationChange{action: command.Action, before: visible, occurrenceDate: command.OccurrenceDate}
	switch command.Action {
	case "update":
		if command.Input == nil {
			return CommandResult{}, ErrInvalid
		}
		if command.Input.CalendarID != "" && command.Input.CalendarID != row.CalendarID.String() {
			return CommandResult{}, ErrInvalid
		}
		params, err := normalize(*command.Input)
		if err != nil {
			return CommandResult{}, err
		}
		sharingCompany := row.SharingCompanyID
		if visible.CalendarType == "personal" &&
			(visible.CompanyID == nil || len(command.Input.AttendeeUserIDs) == 0) {
			sharingCompany = uuid.NullUUID{}
		}
		sharing, err := replaceAttendees(
			ctx,
			q,
			p,
			row.ID,
			row.UserID,
			sharingCompany,
			command.Input.AttendeeUserIDs,
		)
		if err != nil {
			return CommandResult{}, err
		}
		_, err = q.UpdateCalendarEvent(ctx, generated.UpdateCalendarEventParams{
			ID:                    id,
			ExpectedRevision:      row.Version,
			SharingCompanyID:      sharing,
			Type:                  params.Type,
			Title:                 params.Title,
			Description:           params.Description,
			Location:              params.Location,
			Color:                 params.Color,
			AllDay:                params.AllDay,
			StartLocal:            params.StartLocal,
			EndLocal:              params.EndLocal,
			TimeZone:              params.TimeZone,
			Recurrence:            params.Recurrence,
			RecurrenceUntil:       params.RecurrenceUntil,
			RecurrenceInterval:    params.RecurrenceInterval,
			RecurrenceWeekdays:    params.RecurrenceWeekdays,
			RecurrenceMonthlyMode: params.RecurrenceMonthlyMode,
			RecurrenceCount:       params.RecurrenceCount,
			ReminderMinutes:       params.ReminderMinutes,
		})
		if err != nil {
			return CommandResult{}, err
		}
		if row.AllDay != params.AllDay ||
			!row.StartLocal.Time.Equal(params.StartLocal.Time) ||
			!row.EndLocal.Time.Equal(params.EndLocal.Time) ||
			row.TimeZone != params.TimeZone ||
			row.Recurrence != params.Recurrence ||
			row.RecurrenceUntil != params.RecurrenceUntil ||
			row.RecurrenceInterval != params.RecurrenceInterval ||
			!slices.Equal(row.RecurrenceWeekdays, params.RecurrenceWeekdays) ||
			row.RecurrenceMonthlyMode != params.RecurrenceMonthlyMode ||
			row.RecurrenceCount != params.RecurrenceCount {
			if err = q.ResetCalendarResponses(ctx, id); err != nil {
				return CommandResult{}, err
			}
		}
	case "delete":
		if err := m.notifyInTx(ctx, q, change); err != nil {
			return CommandResult{}, err
		}
		return CommandResult{}, q.DeleteCalendarEvent(ctx, id)
	case "respond":
		if command.ResponseStatus != "accepted" && command.ResponseStatus != "tentative" &&
			command.ResponseStatus != "declined" {
			return CommandResult{}, ErrInvalid
		}
		if visible.ResponseStatus == command.ResponseStatus {
			return CommandResult{Event: &visible}, nil
		}
		updatedAt, err := q.RespondCalendarInvitation(
			ctx,
			generated.RespondCalendarInvitationParams{
				EventID:        id,
				UserID:         p.UserID,
				ResponseStatus: command.ResponseStatus,
			},
		)
		if err != nil {
			return CommandResult{}, calendarError(err)
		}
		change.responseKey = p.UserID.String() + ":" + updatedAt.Time.UTC().Format(time.RFC3339Nano)
	case "cancel", "complete", "reopen":
		if err = changeOccurrence(ctx, q, p, row, command); err != nil {
			return CommandResult{}, err
		}
	default:
		return CommandResult{}, ErrInvalid
	}
	event, err := visibleEvent(ctx, q, p, id)
	if err != nil {
		return CommandResult{}, err
	}
	change.after = event
	return CommandResult{Event: &event}, m.notifyInTx(ctx, q, change)
}

func createInTx(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, input Input) (Event, error) {
	params, err := normalize(input)
	if err != nil {
		return Event{}, err
	}
	if err = ensureCalendars(ctx, q, p); err != nil {
		return Event{}, err
	}
	calendars, err := q.ListCalendarCatalog(
		ctx,
		generated.ListCalendarCatalogParams{
			ActorUserID:    uuid.NullUUID{UUID: p.UserID, Valid: true},
			ActorCompanyID: p.CompanyID,
		},
	)
	if err != nil {
		return Event{}, err
	}
	var target *generated.ListCalendarCatalogRow
	for i := range calendars {
		c := &calendars[i]
		if c.ID.String() == input.CalendarID || (input.CalendarID == "" && c.Type == "personal") {
			target = c
			break
		}
	}
	if target == nil {
		return Event{}, ErrNotFound
	}
	params.CalendarID = target.ID
	params.UserID = p.UserID
	if target.Type == "company" {
		params.SharingCompanyID = uuid.NullUUID{UUID: p.CompanyID, Valid: true}
	}
	row, err := q.CreateCalendarEvent(ctx, params)
	if err != nil {
		return Event{}, err
	}
	sharing, err := replaceAttendees(ctx, q, p, row.ID, row.UserID, row.SharingCompanyID, input.AttendeeUserIDs)
	if err != nil {
		return Event{}, err
	}
	if sharing != row.SharingCompanyID {
		if err = q.SetCalendarSharingCompany(
			ctx,
			generated.SetCalendarSharingCompanyParams{ID: row.ID, SharingCompanyID: sharing},
		); err != nil {
			return Event{}, err
		}
	}
	return visibleEvent(ctx, q, p, row.ID)
}

func replaceAttendees(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	eventID, organizer uuid.UUID,
	company uuid.NullUUID,
	ids []string,
) (uuid.NullUUID, error) {
	if len(ids) > 100 {
		return company, ErrInvalid
	}
	if len(ids) > 0 {
		if p.CompanyID == uuid.Nil {
			return company, ErrInvalid
		}
		if company.Valid && company.UUID != p.CompanyID {
			return company, ErrNotFound
		}
		company = uuid.NullUUID{UUID: p.CompanyID, Valid: true}
	}
	seen := map[uuid.UUID]bool{}
	users := make([]uuid.UUID, 0, len(ids))
	for _, value := range ids {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil || id == organizer || seen[id] {
			return company, ErrInvalid
		}
		seen[id] = true
		member, err := q.GetCompanyMemberForShare(
			ctx,
			generated.GetCompanyMemberForShareParams{CompanyID: company.UUID, UserID: id},
		)
		if err != nil {
			return company, calendarError(err)
		}
		if member.Status != "active" {
			return company, ErrNotFound
		}
		users = append(users, id)
	}
	if err := q.ReplaceCalendarAttendees(
		ctx,
		generated.ReplaceCalendarAttendeesParams{EventID: eventID, UserIds: users},
	); err != nil {
		return company, err
	}
	for _, id := range users {
		if err := q.AddCalendarAttendee(
			ctx,
			generated.AddCalendarAttendeeParams{EventID: eventID, CompanyID: company.UUID, UserID: id},
		); err != nil {
			return company, err
		}
	}
	return company, nil
}

func changeOccurrence(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	row generated.CalendarEvent,
	command Command,
) error {
	date, err := time.Parse(time.DateOnly, command.OccurrenceDate)
	if err != nil {
		return ErrInvalid
	}
	items, err := expand(eventResponse(row), date.Add(-24*time.Hour), date.Add(48*time.Hour))
	if err != nil {
		return err
	}
	found := false
	for _, item := range items {
		found = found || item.OccurrenceDate == command.OccurrenceDate
	}
	if !found {
		return ErrNotFound
	}
	if command.Action == "cancel" {
		return q.CancelCalendarOccurrence(
			ctx,
			generated.CancelCalendarOccurrenceParams{
				EventID:        row.ID,
				OccurrenceDate: pgtype.Date{Time: date, Valid: true},
			},
		)
	}
	params := generated.SetCalendarReminderStateParams{
		EventID:        row.ID,
		UserID:         p.UserID,
		OccurrenceDate: pgtype.Date{Time: date, Valid: true},
	}
	if row.Type != "reminder" {
		return ErrInvalid
	}
	params.Completed = command.Action == "complete"
	return q.SetCalendarReminderState(ctx, params)
}
