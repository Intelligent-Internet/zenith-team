package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
)

type ReadCalendarInput struct {
	From string `json:"from,omitempty" jsonschema:"Inclusive RFC3339 start with UTC offset. Omit both dates for the next 30 days and calendar/member discovery."`
	To   string `json:"to,omitempty" jsonschema:"Exclusive RFC3339 end with UTC offset. Maximum range 93 days."`
}

type CalendarEventInput struct {
	CalendarID            string   `json:"calendarId" jsonschema:"ID returned by calendar_read. Choose the user's personal calendar unless they explicitly request company-wide visibility. Cannot change an existing event's calendar."`
	Type                  string   `json:"type" jsonschema:"event or reminder"`
	Title                 string   `json:"title"`
	Description           string   `json:"description"`
	Location              string   `json:"location" jsonschema:"Place or meeting URL. This tool does not provision video calls."`
	Color                 string   `json:"color" jsonschema:"sage, sky, lavender, rose or sand"`
	AllDay                bool     `json:"allDay"`
	StartLocal            string   `json:"startLocal" jsonschema:"YYYY-MM-DDTHH:mm, or YYYY-MM-DD for all-day entries; interpreted in timeZone."`
	EndLocal              string   `json:"endLocal" jsonschema:"Exclusive end in the same local format. A one-day all-day event ends on the following date."`
	TimeZone              string   `json:"timeZone" jsonschema:"IANA zone such as Asia/Ho_Chi_Minh. Resolve ambiguous dates/time zones before writing."`
	Recurrence            string   `json:"recurrence" jsonschema:"none, daily, weekly, monthly or yearly"`
	RecurrenceInterval    int32    `json:"recurrenceInterval,omitempty" jsonschema:"Repeat every N frequency units, 1..999; omit for 1. Preserve existing custom fields when updating."`
	RecurrenceWeekdays    []int32  `json:"recurrenceWeekdays,omitempty" jsonschema:"Weekly only: unique weekday numbers, Sunday 0 through Saturday 6; empty uses start weekday. Weeks start Monday."`
	RecurrenceMonthlyMode string   `json:"recurrenceMonthlyMode,omitempty" jsonschema:"Monthly: day_of_month (default) or day_of_week for the start date ordinal weekday, fifth means last."`
	RecurrenceCount       *int32   `json:"recurrenceCount,omitempty" jsonschema:"Stop after 1..1000 occurrences from series start; mutually exclusive with recurrenceUntil."`
	RecurrenceUntil       *string  `json:"recurrenceUntil" jsonschema:"Last recurrence date YYYY-MM-DD, inclusive; null for no limit."`
	ReminderMinutes       *int32   `json:"reminderMinutes" jsonschema:"In-app reminder lead time 0..10080, or null. Does not send email."`
	AttendeeUserIDs       []string `json:"attendeeUserIds" jsonschema:"Active member IDs returned by calendar_read, excluding the organizer. Full replacement on update; retain existing guests unless asked to remove them. Never invent IDs or resolve ambiguous names silently."`
}

type WriteCalendarInput struct {
	Action           string              `json:"action" jsonschema:"create, update, delete, respond, cancel, complete, or reopen. update/delete affect the entire series; cancel affects one occurrence."`
	EventID          string              `json:"eventId,omitempty" jsonschema:"Existing ID from calendar_read, required except for create."`
	ExpectedRevision int64               `json:"expectedRevision,omitempty" jsonschema:"Current revision from calendar_read for all existing-event actions. Omit for create. Refetch after conflict, never guess a newer revision."`
	Input            *CalendarEventInput `json:"input,omitempty" jsonschema:"Complete event fields, required for create/update only."`
	ResponseStatus   string              `json:"responseStatus,omitempty" jsonschema:"accepted, tentative or declined, required for respond."`
	OccurrenceDate   string              `json:"occurrenceDate,omitempty" jsonschema:"YYYY-MM-DD occurrence date from calendar_read, required for cancel/complete/reopen."`
}

type CalendarMember struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
}

type CalendarAttendee struct {
	CalendarMember
	ResponseStatus string `json:"responseStatus"`
}

type CalendarEvent struct {
	CalendarEventInput
	ID               string             `json:"id"`
	CalendarType     string             `json:"calendarType"`
	CalendarName     string             `json:"calendarName"`
	CompanyID        *string            `json:"companyId"`
	OrganizerName    string             `json:"organizerName"`
	OrganizerUserID  string             `json:"organizerUserId"`
	CanEdit          bool               `json:"canEdit"`
	ResponseStatus   string             `json:"responseStatus"`
	Attendees        []CalendarAttendee `json:"attendees"`
	Revision         int64              `json:"revision"`
	SeriesStartLocal string             `json:"seriesStartLocal,omitempty"`
	SeriesEndLocal   string             `json:"seriesEndLocal,omitempty"`
	OccurrenceDate   string             `json:"occurrenceDate,omitempty"`
	StartAt          string             `json:"startAt,omitempty"`
	EndAt            string             `json:"endAt,omitempty"`
	Completed        bool               `json:"completed"`
}

type CalendarCatalog struct {
	Calendars []struct {
		ID        string  `json:"id"`
		Type      string  `json:"type"`
		Name      string  `json:"name"`
		CompanyID *string `json:"companyId"`
	} `json:"calendars"`
	Members []CalendarMember `json:"members"`
	UserID  string           `json:"userId"`
	Now     string           `json:"now"`
}

type ReadCalendarResult struct {
	Catalog CalendarCatalog `json:"catalog"`
	Items   []CalendarEvent `json:"items"`
}

type WriteCalendarResult struct {
	Event *CalendarEvent `json:"event"`
}

type calendarAPI interface {
	FetchRunJSON(context.Context, string, any) error
	PostRunJSON(context.Context, string, string, any, any) error
}

// CalendarToolService adapts the authenticated, Run-proven calendar HTTP surface.
type CalendarToolService interface {
	ReadCalendar(context.Context, ReadCalendarInput) (ReadCalendarResult, error)
	WriteCalendar(context.Context, WriteCalendarInput) (WriteCalendarResult, error)
}

var _ CalendarToolService = (*DefaultCalendarToolService)(nil)

type DefaultCalendarToolService struct {
	api     calendarAPI
	context RunContext
}

func NewCalendarToolService(api calendarAPI, run RunContext) (*DefaultCalendarToolService, error) {
	if api == nil {
		return nil, errors.New("calendar API is unavailable")
	}
	if err := validateRunContext(run); err != nil {
		return nil, err
	}
	return &DefaultCalendarToolService{api: api, context: run}, nil
}

func (s *DefaultCalendarToolService) path() string {
	return fmt.Sprintf("/companies/%s/conversations/%s/calendar", url.PathEscape(s.context.CompanyID), url.PathEscape(s.context.ConversationID))
}

func (s *DefaultCalendarToolService) ReadCalendar(ctx context.Context, input ReadCalendarInput) (ReadCalendarResult, error) {
	query := url.Values{}
	if input.From != "" {
		query.Set("from", input.From)
	}
	if input.To != "" {
		query.Set("to", input.To)
	}
	var result ReadCalendarResult
	if err := s.api.FetchRunJSON(ctx, s.path()+"?"+query.Encode(), &result); err != nil {
		return result, fmt.Errorf("read calendar: %w", err)
	}
	for i := range result.Items {
		result.Items[i].AttendeeUserIDs = attendeeIDs(result.Items[i].Attendees)
	}
	return result, nil
}

func (s *DefaultCalendarToolService) WriteCalendar(ctx context.Context, input WriteCalendarInput) (WriteCalendarResult, error) {
	key, err := runRequestKey("calendar_write", s.context, input)
	if err != nil {
		return WriteCalendarResult{}, err
	}
	var result WriteCalendarResult
	if err = s.api.PostRunJSON(ctx, s.path(), key, input, &result); err != nil {
		return result, fmt.Errorf("write calendar: %w", err)
	}
	if result.Event != nil {
		result.Event.AttendeeUserIDs = attendeeIDs(result.Event.Attendees)
	}
	return result, nil
}

func attendeeIDs(guests []CalendarAttendee) []string {
	ids := make([]string, 0, len(guests))
	for _, guest := range guests {
		ids = append(ids, guest.UserID)
	}
	return ids
}
