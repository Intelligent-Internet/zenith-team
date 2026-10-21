// Package calendar owns personal and company calendars, invitations, recurrence
// exceptions and per-user reminder receipts. Entries never create Agent Runs.
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

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

var (
	ErrNotFound = errors.New("calendar event not found")
	ErrInvalid  = errors.New("invalid calendar input")
	ErrConflict = errors.New("calendar event changed")
	ErrRange    = errors.New("calendar range or result exceeds the supported limit")
)

type Input struct {
	CalendarID            string   `json:"calendarId"`
	AttendeeUserIDs       []string `json:"attendeeUserIds"`
	Type                  string   `json:"type"`
	Title                 string   `json:"title"`
	Description           string   `json:"description"`
	Location              string   `json:"location"`
	Color                 string   `json:"color"`
	AllDay                bool     `json:"allDay"`
	StartLocal            string   `json:"startLocal"`
	EndLocal              string   `json:"endLocal"`
	TimeZone              string   `json:"timeZone"`
	Recurrence            string   `json:"recurrence"`
	RecurrenceInterval    int32    `json:"recurrenceInterval"`
	RecurrenceWeekdays    []int32  `json:"recurrenceWeekdays"`
	RecurrenceMonthlyMode string   `json:"recurrenceMonthlyMode"`
	RecurrenceCount       *int32   `json:"recurrenceCount"`
	RecurrenceUntil       *string  `json:"recurrenceUntil"`
	ReminderMinutes       *int32   `json:"reminderMinutes"`
	ExpectedRevision      int64    `json:"expectedRevision"`
}

type Event struct {
	CalendarID            string     `json:"calendarId"`
	CalendarType          string     `json:"calendarType"`
	CalendarName          string     `json:"calendarName"`
	CompanyID             *string    `json:"companyId"`
	OrganizerName         string     `json:"organizerName"`
	OrganizerUserID       string     `json:"organizerUserId"`
	CanEdit               bool       `json:"canEdit"`
	ResponseStatus        string     `json:"responseStatus"`
	Attendees             []Attendee `json:"attendees"`
	ID                    string     `json:"id"`
	Type                  string     `json:"type"`
	Title                 string     `json:"title"`
	Description           string     `json:"description"`
	Location              string     `json:"location"`
	Color                 string     `json:"color"`
	AllDay                bool       `json:"allDay"`
	StartLocal            string     `json:"startLocal"`
	EndLocal              string     `json:"endLocal"`
	TimeZone              string     `json:"timeZone"`
	Recurrence            string     `json:"recurrence"`
	RecurrenceInterval    int32      `json:"recurrenceInterval"`
	RecurrenceWeekdays    []int32    `json:"recurrenceWeekdays"`
	RecurrenceMonthlyMode string     `json:"recurrenceMonthlyMode"`
	RecurrenceCount       *int32     `json:"recurrenceCount"`
	RecurrenceUntil       *string    `json:"recurrenceUntil"`
	ReminderMinutes       *int32     `json:"reminderMinutes"`
	Revision              int64      `json:"revision"`
}

type Occurrence struct {
	Event
	SeriesStartLocal string    `json:"seriesStartLocal"`
	SeriesEndLocal   string    `json:"seriesEndLocal"`
	OccurrenceDate   string    `json:"occurrenceDate"`
	StartAt          time.Time `json:"startAt"`
	EndAt            time.Time `json:"endAt"`
	Completed        bool      `json:"completed"`
}

type OccurrenceInput struct {
	ExpectedRevision int64  `json:"expectedRevision"`
	OccurrenceDate   string `json:"occurrenceDate"`
	Action           string `json:"action"`
}

type Attendee struct {
	UserID         string `json:"userId"`
	DisplayName    string `json:"displayName"`
	Email          string `json:"email"`
	ResponseStatus string `json:"responseStatus"`
}

type Calendar struct {
	ID        string  `json:"id"`
	Type      string  `json:"type"`
	Name      string  `json:"name"`
	CompanyID *string `json:"companyId"`
}

type Member struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
}

type Catalog struct {
	Calendars []Calendar `json:"calendars"`
	Members   []Member   `json:"members"`
	UserID    string     `json:"userId"`
	Now       time.Time  `json:"now"`
}

type CalendarService interface {
	Catalog(context.Context, auth.UserPrincipal) (Catalog, error)
	List(context.Context, auth.UserPrincipal, time.Time, time.Time) ([]Occurrence, error)
	Create(context.Context, auth.UserPrincipal, Input) (Event, error)
	Update(context.Context, auth.UserPrincipal, string, Input) (Event, error)
	Delete(context.Context, auth.UserPrincipal, string, int64) error
	Respond(context.Context, auth.UserPrincipal, string, int64, string) error
	Occurrence(context.Context, auth.UserPrincipal, string, OccurrenceInput) error
	ReadForRun(context.Context, auth.UserPrincipal, RunContext, time.Time, time.Time) (ReadResult, error)
	WriteForRun(context.Context, auth.UserPrincipal, RunContext, Command) (CommandResult, error)
}

var _ CalendarService = (*DefaultCalendarService)(nil)

type DefaultCalendarService struct {
	store     *db.Store
	bus       *events.Bus
	presenter inbox.Presenter
}

func NewCalendarService(store *db.Store, bus *events.Bus, presenter inbox.Presenter) *DefaultCalendarService {
	return &DefaultCalendarService{store: store, bus: bus, presenter: presenter}
}

func (s *DefaultCalendarService) Catalog(ctx context.Context, p auth.UserPrincipal) (Catalog, error) {
	var result Catalog
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockActor(ctx, q, p); err != nil {
			return err
		}
		if err := ensureCalendars(ctx, q, p); err != nil {
			return err
		}
		var err error
		result, err = catalog(ctx, q, p)
		return err
	})
	return result, err
}

// Defaults are provisioned on first calendar access, inside the caller's
// authorized transaction. Unique ownership keys make concurrent access converge.
func ensureCalendars(ctx context.Context, q *generated.Queries, p auth.UserPrincipal) error {
	if err := q.EnsurePersonalCalendar(ctx, uuid.NullUUID{UUID: p.UserID, Valid: true}); err != nil {
		return fmt.Errorf("ensure personal calendar: %w", err)
	}
	if p.CompanyID != uuid.Nil {
		if err := q.EnsureCompanyCalendar(ctx, uuid.NullUUID{UUID: p.CompanyID, Valid: true}); err != nil {
			return fmt.Errorf("ensure company calendar: %w", err)
		}
	}
	return nil
}

func catalog(ctx context.Context, q *generated.Queries, p auth.UserPrincipal) (Catalog, error) {
	result := Catalog{
		Calendars: []Calendar{},
		Members:   []Member{},
		UserID:    p.UserID.String(),
		Now:       time.Now().UTC(),
	}
	rows, err := q.ListCalendarCatalog(
		ctx,
		generated.ListCalendarCatalogParams{
			ActorUserID:    uuid.NullUUID{UUID: p.UserID, Valid: true},
			ActorCompanyID: p.CompanyID,
		},
	)
	if err != nil {
		return result, fmt.Errorf("list calendars: %w", err)
	}
	for _, row := range rows {
		result.Calendars = append(
			result.Calendars,
			Calendar{
				ID:        row.ID.String(),
				Type:      row.Type,
				Name:      row.Name,
				CompanyID: optionalID(row.CompanyID),
			},
		)
	}
	if p.CompanyID != uuid.Nil {
		members, err := q.ListActiveCompanyMembers(
			ctx,
			generated.ListActiveCompanyMembersParams{CompanyID: p.CompanyID, ActorUserID: p.UserID},
		)
		if err != nil {
			return result, fmt.Errorf("list calendar members: %w", err)
		}
		for _, m := range members {
			if !m.AccountDisabled {
				result.Members = append(
					result.Members,
					Member{
						UserID:      m.CompanyMember.UserID.String(),
						DisplayName: m.MemberDisplayName,
						Email:       m.MemberEmail,
					},
				)
			}
		}
	}
	return result, nil
}

func (s *DefaultCalendarService) List(
	ctx context.Context,
	p auth.UserPrincipal,
	from, to time.Time,
) ([]Occurrence, error) {
	var result []Occurrence
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			var err error
			result, err = list(ctx, q, p, from, to)
			return err
		},
	)
	return result, err
}

func list(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, from, to time.Time) ([]Occurrence, error) {
	if !to.After(from) || to.Sub(from) > 93*24*time.Hour || from.Year() < 1900 || to.Year() > 2200 {
		return nil, ErrRange
	}
	rows, err := q.ListCalendarEvents(
		ctx,
		generated.ListCalendarEventsParams{
			ActorUserID:    uuid.NullUUID{UUID: p.UserID, Valid: true},
			ActorCompanyID: p.CompanyID,
			ActorRoles:     auth.RolesWith(auth.PermissionCalendarManage),
			BeforeLocal:    pgtype.Timestamp{Time: to.Add(48 * time.Hour), Valid: true},
			AfterLocal:     pgtype.Timestamp{Time: from.Add(-48 * time.Hour), Valid: true},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list calendar events: %w", err)
	}
	if len(rows) > 2000 {
		return nil, ErrRange
	}
	if len(rows) == 0 {
		return []Occurrence{}, nil
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.CalendarEvent.ID)
	}
	attendees, err := q.ListCalendarAttendees(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list calendar attendees: %w", err)
	}
	states, err := q.ListCalendarOccurrenceStates(
		ctx,
		generated.ListCalendarOccurrenceStatesParams{
			EventIds: ids,
			FromDate: pgtype.Date{Time: from.Add(-368 * 24 * time.Hour), Valid: true},
			ToDate:   pgtype.Date{Time: to.Add(48 * time.Hour), Valid: true},
		},
	)
	if err != nil {
		return nil, err
	}
	receipts, err := q.ListCalendarReminderStates(
		ctx,
		generated.ListCalendarReminderStatesParams{
			EventIds: ids,
			UserID:   p.UserID,
			FromDate: pgtype.Date{Time: from.Add(-368 * 24 * time.Hour), Valid: true},
			ToDate:   pgtype.Date{Time: to.Add(48 * time.Hour), Valid: true},
		},
	)
	if err != nil {
		return nil, err
	}
	cancelled := map[string]bool{}
	for _, r := range states {
		cancelled[r.EventID.String()+":"+r.OccurrenceDate.Time.Format(time.DateOnly)] = r.Cancelled
	}
	personal := map[string]generated.CalendarReminderState{}
	for _, r := range receipts {
		personal[r.EventID.String()+":"+r.OccurrenceDate.Time.Format(time.DateOnly)] = r
	}
	guests := map[uuid.UUID][]Attendee{}
	for _, a := range attendees {
		guests[a.EventID] = append(guests[a.EventID], attendeeResponse(a))
	}
	result := []Occurrence{}
	for _, row := range rows {
		event := visibleResponse(
			generated.GetVisibleCalendarEventRow(row),
			p.UserID,
			guests[row.CalendarEvent.ID],
		)
		occurrences, err := expand(event, from, to)
		if err != nil {
			return nil, err
		}
		for _, item := range occurrences {
			key := item.ID + ":" + item.OccurrenceDate
			if cancelled[key] {
				continue
			}
			state := personal[key]
			item.Completed = state.Completed
			result = append(result, item)
		}
		if len(result) > 5000 {
			return nil, ErrRange
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].StartAt.Equal(result[j].StartAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].StartAt.Before(result[j].StartAt)
	})
	return result, nil
}

func visibleEvent(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, id uuid.UUID) (Event, error) {
	row, err := q.GetVisibleCalendarEvent(
		ctx,
		generated.GetVisibleCalendarEventParams{
			ID:             id,
			ActorUserID:    uuid.NullUUID{UUID: p.UserID, Valid: true},
			ActorCompanyID: p.CompanyID,
			ActorRoles:     auth.RolesWith(auth.PermissionCalendarManage),
		},
	)
	if err != nil {
		return Event{}, calendarError(err)
	}
	guests, err := q.ListCalendarAttendees(ctx, []uuid.UUID{id})
	if err != nil {
		return Event{}, err
	}
	attendees := make([]Attendee, 0, len(guests))
	for _, g := range guests {
		attendees = append(attendees, attendeeResponse(g))
	}
	return visibleResponse(row, p.UserID, attendees), nil
}

func visibleResponse(row generated.GetVisibleCalendarEventRow, viewer uuid.UUID, guests []Attendee) Event {
	result := eventResponse(row.CalendarEvent)
	result.CalendarID = row.CalendarEvent.CalendarID.String()
	result.CalendarType = row.CalendarType
	result.CalendarName = row.CalendarName
	result.CompanyID = optionalID(row.CompanyID)
	if row.CalendarType == "personal" && !row.SharingActive {
		result.CompanyID = nil
	}
	result.OrganizerUserID = row.CalendarEvent.UserID.String()
	result.OrganizerName = row.OrganizerName
	result.CanEdit = row.CanEdit
	result.Attendees = guests
	if result.Attendees == nil {
		result.Attendees = []Attendee{}
	}
	for _, g := range guests {
		if g.UserID == viewer.String() {
			result.ResponseStatus = g.ResponseStatus
		}
	}
	return result
}

func attendeeResponse(row generated.ListCalendarAttendeesRow) Attendee {
	return Attendee{
		UserID:         row.UserID.String(),
		DisplayName:    row.DisplayName,
		Email:          row.Email,
		ResponseStatus: row.ResponseStatus,
	}
}

func optionalID(id uuid.NullUUID) *string {
	if !id.Valid {
		return nil
	}
	value := id.UUID.String()
	return &value
}

func calendarError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("calendar access: %w", err)
}

func eventResponse(row generated.CalendarEvent) Event {
	layout := "2006-01-02T15:04"
	if row.AllDay {
		layout = time.DateOnly
	}
	result := Event{
		ID:                    row.ID.String(),
		Type:                  row.Type,
		Title:                 row.Title,
		Description:           row.Description,
		Location:              row.Location,
		Color:                 row.Color,
		AllDay:                row.AllDay,
		StartLocal:            row.StartLocal.Time.Format(layout),
		EndLocal:              row.EndLocal.Time.Format(layout),
		TimeZone:              row.TimeZone,
		Recurrence:            row.Recurrence,
		Revision:              row.Version,
		RecurrenceInterval:    row.RecurrenceInterval,
		RecurrenceWeekdays:    append([]int32{}, row.RecurrenceWeekdays...),
		RecurrenceMonthlyMode: row.RecurrenceMonthlyMode,
	}
	if row.RecurrenceCount.Valid {
		result.RecurrenceCount = &row.RecurrenceCount.Int32
	}
	if row.ReminderMinutes.Valid {
		result.ReminderMinutes = &row.ReminderMinutes.Int32
	}
	if row.RecurrenceUntil.Valid {
		value := row.RecurrenceUntil.Time.Format(time.DateOnly)
		result.RecurrenceUntil = &value
	}
	return result
}
