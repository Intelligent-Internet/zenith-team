package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	// Embeds the IANA timezone database so time.LoadLocation resolves the
	// caller's zone on a container image that ships no /usr/share/zoneinfo.
	_ "time/tzdata"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/util"
)

var (
	errInvalidActivityCursor = errors.New("invalid activity cursor")
	errInvalidActivityInput  = errors.New("invalid activity input")
)

// ActivityFeed is the read surface the activity routes serve
// (satisfied by *activity.Feed).
type ActivityFeed interface {
	List(context.Context, uuid.UUID, activity.ListOptions) ([]activity.Entry, *activity.Cursor, error)
	Calendar(context.Context, uuid.UUID, activity.CalendarOptions) (activity.Calendar, error)
}

type activityPageResponse struct {
	HistoryCoverage string           `json:"historyCoverage"`
	HistoryStartsAt *time.Time       `json:"historyStartsAt"`
	Items           []activity.Entry `json:"items"`
	NextCursor      *string          `json:"nextCursor"`
}

func (h *Handler) registerActivityRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.ActivityFeed == nil {
		return
	}
	routes := group.Group(
		"/companies/:companyId/activity",
		middleware.NoStore(),
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	routes.GET("", h.listActivity)
	routes.GET("/calendar", h.getActivityCalendar)
}

func (h *Handler) getActivityCalendar(c *gin.Context) {
	companyID, ok := parseUUIDOrBadRequest(c, c.Param("companyId"), "companyId")
	if !ok {
		return
	}
	options, err := parseActivityCalendarQuery(c)
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, _ := auth.UserPrincipalFromContext(c.Request.Context())
	options.UserID = principal.UserID
	calendar, err := h.ActivityFeed.Calendar(c.Request.Context(), companyID, options)
	if err != nil {
		writeBoundaryError(c, boundaryErrorActivityCalendarUnavailable)
		return
	}
	c.JSON(http.StatusOK, calendar)
}

func (h *Handler) listActivity(c *gin.Context) {
	companyID, ok := parseUUIDOrBadRequest(c, c.Param("companyId"), "companyId")
	if !ok {
		return
	}
	query, err := parseActivityQuery(c)
	if err != nil {
		if errors.Is(err, errInvalidActivityCursor) {
			writeBoundaryError(c, boundaryErrorInvalidCursor)
		} else {
			writeBoundaryError(c, boundaryErrorValidationFailed)
		}
		return
	}
	entries, next, err := h.ActivityFeed.List(c.Request.Context(), companyID, query)
	if err != nil {
		writeBoundaryError(c, boundaryErrorActivityFeedUnavailable)
		return
	}
	response := activityPageResponse{Items: entries, HistoryCoverage: "partial"}
	if next != nil {
		cursor := encodeActivityCursor(c, query, next)
		response.NextCursor = &cursor
	}
	c.JSON(http.StatusOK, response)
}

func parseActivityQuery(c *gin.Context) (activity.ListOptions, error) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return activity.ListOptions{}, errInvalidActivityInput
	}
	for key, values := range query {
		known := key == "cursor" || key == "limit" || key == "from" || key == "to" ||
			key == "entity_type" || key == "entity_id" || key == "action" || key == "actor_type" || key == "actor_id" || key == "revision" || key == "before_revision"
		if !known || len(values) != 1 || values[0] == "" {
			return activity.ListOptions{}, errInvalidActivityInput
		}
	}
	principal, _ := auth.UserPrincipalFromContext(c.Request.Context())
	parsed := activity.ListOptions{Limit: 50, UserID: principal.UserID}
	if values, exists := query["limit"]; exists {
		limit, err := strconv.Atoi(values[0])
		if err != nil || strconv.Itoa(limit) != values[0] || limit < 1 || limit > 100 {
			return activity.ListOptions{}, errInvalidActivityInput
		}
		parsed.Limit = int32(limit)
	}
	if values, exists := query["from"]; exists {
		from, err := time.Parse(time.RFC3339Nano, values[0])
		if err != nil {
			return activity.ListOptions{}, errInvalidActivityInput
		}
		from = from.UTC()
		parsed.From = &from
	}
	if values, exists := query["to"]; exists {
		to, err := time.Parse(time.RFC3339Nano, values[0])
		if err != nil {
			return activity.ListOptions{}, errInvalidActivityInput
		}
		to = to.UTC()
		parsed.To = &to
	}
	if parsed.From != nil && parsed.To != nil && !parsed.To.After(*parsed.From) {
		return activity.ListOptions{}, errInvalidActivityInput
	}
	parsed.EntityType = firstValue(query, "entity_type")
	parsed.EntityID = firstValue(query, "entity_id")
	// An entity ID needs its type. A stored row's entity_type is the
	// changed subject type and its action is the domain event name, so
	// both are checked for that shape rather than against a list — a typo
	// still reads as bad input, and a domain adding an event does not have to
	// come back here.
	if parsed.EntityID != "" && parsed.EntityType == "" {
		return activity.ListOptions{}, errInvalidActivityInput
	}
	if parsed.EntityType != "" && !activity.ValidEntityTypeFilter(parsed.EntityType) {
		return activity.ListOptions{}, errInvalidActivityInput
	}
	parsed.Action = firstValue(query, "action")
	if parsed.Action != "" && !activity.ValidActionFilter(parsed.Action) {
		return activity.ListOptions{}, errInvalidActivityInput
	}
	parsed.ActorType, parsed.ActorID = firstValue(query, "actor_type"), firstValue(query, "actor_id")
	if parsed.ActorType != "" && parsed.ActorType != "user" && parsed.ActorType != "agent" && parsed.ActorType != "system" {
		return activity.ListOptions{}, errInvalidActivityInput
	}
	if parsed.ActorID != "" {
		if parsed.ActorType != "user" && parsed.ActorType != "agent" {
			return activity.ListOptions{}, errInvalidActivityInput
		}
		if _, err := util.ParseGoUUID(parsed.ActorID); err != nil {
			return activity.ListOptions{}, errInvalidActivityInput
		}
	}
	for key, target := range map[string]**int64{"revision": &parsed.Revision, "before_revision": &parsed.BeforeRevision} {
		if value := firstValue(query, key); value != "" {
			revision, err := strconv.ParseInt(value, 10, 64)
			if err != nil || revision < 1 || revision > 9007199254740991 || strconv.FormatInt(revision, 10) != value {
				return activity.ListOptions{}, errInvalidActivityInput
			}
			*target = &revision
		}
	}
	if parsed.Revision != nil || parsed.BeforeRevision != nil {
		if parsed.Revision != nil && parsed.BeforeRevision != nil {
			return activity.ListOptions{}, errInvalidActivityInput
		}
		if parsed.EntityType != "task" && parsed.EntityType != "conversation" {
			return activity.ListOptions{}, errInvalidActivityInput
		}
		if _, err := util.ParseGoUUID(parsed.EntityID); err != nil {
			return activity.ListOptions{}, errInvalidActivityInput
		}
	}
	if parsed.EntityID != "" && strings.TrimSpace(parsed.EntityID) == "" {
		return activity.ListOptions{}, errInvalidActivityInput
	}
	if value := firstValue(query, "cursor"); value != "" {
		cursor, ok := decodeActivityCursor(c, parsed, value)
		if !ok {
			return activity.ListOptions{}, errInvalidActivityCursor
		}
		parsed.Before = cursor
	}
	return parsed, nil
}

func parseActivityCalendarQuery(c *gin.Context) (activity.CalendarOptions, error) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil || len(query) != 2 {
		return activity.CalendarOptions{}, errInvalidActivityInput
	}
	monthValues, hasMonth := query["month"]
	zoneValues, hasZone := query["time_zone"]
	if !hasMonth || !hasZone || len(monthValues) != 1 || len(zoneValues) != 1 ||
		monthValues[0] == "" || zoneValues[0] == "" {
		return activity.CalendarOptions{}, errInvalidActivityInput
	}
	month := monthValues[0]
	parsedMonth, err := time.Parse("2006-01", month)
	if err != nil || len(month) != 7 || parsedMonth.Year() < 1 || parsedMonth.Format("2006-01") != month {
		return activity.CalendarOptions{}, errInvalidActivityInput
	}
	timeZone := zoneValues[0]
	if timeZone == "Local" {
		return activity.CalendarOptions{}, errInvalidActivityInput
	}
	location, err := time.LoadLocation(timeZone)
	if err != nil {
		return activity.CalendarOptions{}, errInvalidActivityInput
	}
	localStart := time.Date(parsedMonth.Year(), parsedMonth.Month(), 1, 0, 0, 0, 0, location)
	localEnd := localStart.AddDate(0, 1, 0)
	return activity.CalendarOptions{
		Month: month, TimeZone: timeZone, Location: location,
		StartAt: localStart.UTC(), EndAt: localEnd.UTC(),
	}, nil
}

func firstValue(query url.Values, key string) string {
	if values, exists := query[key]; exists {
		return values[0]
	}
	return ""
}
