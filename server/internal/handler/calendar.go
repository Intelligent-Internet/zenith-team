package handler

import (
	"net/http"
	"strconv"
	"time"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/calendar"
	"boardly.dev/boardly/server/internal/middleware"
	"github.com/gin-gonic/gin"
)

type calendarOccurrenceCollection struct {
	Items []calendar.Occurrence `json:"items"`
}

type calendarInputRequest struct {
	CalendarID            string   `json:"calendarId" binding:"omitempty,uuid"`
	AttendeeUserIDs       []string `json:"attendeeUserIds" binding:"max=100,dive,uuid"`
	Type                  string   `json:"type" binding:"required,oneof=event reminder"`
	Title                 string   `json:"title" binding:"required,max=240"`
	Description           string   `json:"description" binding:"max=16000"`
	Location              string   `json:"location" binding:"max=1000"`
	Color                 string   `json:"color" binding:"required,oneof=sage sky lavender rose sand"`
	AllDay                bool     `json:"allDay"`
	StartLocal            string   `json:"startLocal" binding:"required,max=16"`
	EndLocal              string   `json:"endLocal" binding:"required,max=16"`
	TimeZone              string   `json:"timeZone" binding:"required,max=100"`
	Recurrence            string   `json:"recurrence" binding:"required,oneof=none daily weekly monthly yearly"`
	RecurrenceInterval    *int32   `json:"recurrenceInterval" binding:"omitempty,min=1,max=999"`
	RecurrenceWeekdays    []int32  `json:"recurrenceWeekdays" binding:"max=7,unique,dive,min=0,max=6"`
	RecurrenceMonthlyMode string   `json:"recurrenceMonthlyMode" binding:"omitempty,oneof=day_of_month day_of_week"`
	RecurrenceCount       *int32   `json:"recurrenceCount" binding:"omitempty,min=1,max=1000"`
	RecurrenceUntil       *string  `json:"recurrenceUntil" binding:"omitempty,datetime=2006-01-02"`
	ReminderMinutes       *int32   `json:"reminderMinutes" binding:"omitempty,min=0,max=10080"`
}

func (request calendarInputRequest) input() calendar.Input {
	interval := int32(1)
	if request.RecurrenceInterval != nil {
		interval = *request.RecurrenceInterval
	}
	return calendar.Input{
		CalendarID: request.CalendarID, AttendeeUserIDs: request.AttendeeUserIDs,
		Type: request.Type, Title: request.Title, Description: request.Description,
		Location: request.Location, Color: request.Color, AllDay: request.AllDay,
		StartLocal: request.StartLocal, EndLocal: request.EndLocal, TimeZone: request.TimeZone,
		Recurrence: request.Recurrence, RecurrenceUntil: request.RecurrenceUntil,
		ReminderMinutes:    request.ReminderMinutes,
		RecurrenceInterval: interval, RecurrenceWeekdays: request.RecurrenceWeekdays, RecurrenceMonthlyMode: request.RecurrenceMonthlyMode, RecurrenceCount: request.RecurrenceCount,
	}
}

type calendarCreateRequest struct {
	calendarInputRequest
	InitialRevision int64 `json:"expectedRevision" binding:"eq=0"`
}

type calendarUpdateRequest struct {
	calendarInputRequest
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}

type calendarOccurrenceRequest struct {
	ExpectedRevision int64  `json:"expectedRevision" binding:"revision"`
	OccurrenceDate   string `json:"occurrenceDate" binding:"required,datetime=2006-01-02"`
	Action           string `json:"action" binding:"required,oneof=cancel complete reopen"`
}

func (h *Handler) registerCalendarRoutes(api *gin.RouterGroup) {
	if h.AuthService == nil || h.CalendarService == nil {
		return
	}
	group := api.Group("/me/calendar", middleware.RequireUserAuth(h.AuthService), middleware.RequireHumanActor())
	h.registerCalendarHumanRoutes(group)
	if h.CompanyService != nil {
		company := api.Group("/companies/:companyId/calendar", middleware.RequireUserAuth(h.AuthService), middleware.RequireCompanyMember(h.CompanyService), middleware.RequireHumanActor())
		h.registerCalendarHumanRoutes(company)
		run := api.Group("/companies/:companyId/conversations/:conversationId/calendar", middleware.RequireUserAuth(h.AuthService), middleware.RequireCompanyMember(h.CompanyService))
		run.GET("", h.readAgentCalendar)
		run.POST("", h.writeAgentCalendar)
	}
}

func (h *Handler) registerCalendarHumanRoutes(group *gin.RouterGroup) {
	group.GET("/catalog", h.calendarCatalog)
	group.GET("/events", h.listCalendarEvents)
	group.POST("/events", h.createCalendarEvent)
	group.PUT("/events/:eventId", h.updateCalendarEvent)
	group.DELETE("/events/:eventId", h.deleteCalendarEvent)
	group.POST("/events/:eventId/occurrences", h.changeCalendarOccurrence)
	group.POST("/events/:eventId/response", h.respondCalendarInvitation)
}

func (h *Handler) listCalendarEvents(c *gin.Context) {
	from, err := time.Parse(time.RFC3339, c.Query("from"))
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	to, err := time.Parse(time.RFC3339, c.Query("to"))
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, _ := auth.UserPrincipalFromContext(c.Request.Context())
	items, err := h.CalendarService.List(c.Request.Context(), principal, from, to)
	if err != nil {
		writeCalendarError(c, err)
		return
	}
	c.JSON(http.StatusOK, calendarOccurrenceCollection{Items: items})
}

func (h *Handler) createCalendarEvent(c *gin.Context) {
	request, err := bindRequest[calendarCreateRequest](c, 32<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, _ := auth.UserPrincipalFromContext(c.Request.Context())
	event, err := h.CalendarService.Create(c.Request.Context(), principal, request.input())
	if err != nil {
		writeCalendarError(c, err)
		return
	}
	c.JSON(http.StatusCreated, event)
}

func (h *Handler) updateCalendarEvent(c *gin.Context) {
	request, err := bindRequest[calendarUpdateRequest](c, 32<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, _ := auth.UserPrincipalFromContext(c.Request.Context())
	input := request.input()
	input.ExpectedRevision = request.ExpectedRevision
	event, err := h.CalendarService.Update(c.Request.Context(), principal, c.Param("eventId"), input)
	if err != nil {
		writeCalendarError(c, err)
		return
	}
	c.JSON(http.StatusOK, event)
}

func (h *Handler) deleteCalendarEvent(c *gin.Context) {
	revision, err := strconv.ParseInt(c.Query("expected_revision"), 10, 64)
	if err != nil || revision < 1 {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, _ := auth.UserPrincipalFromContext(c.Request.Context())
	if err := h.CalendarService.Delete(c.Request.Context(), principal, c.Param("eventId"), revision); err != nil {
		writeCalendarError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) changeCalendarOccurrence(c *gin.Context) {
	request, err := bindRequest[calendarOccurrenceRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, _ := auth.UserPrincipalFromContext(c.Request.Context())
	input := calendar.OccurrenceInput{
		ExpectedRevision: request.ExpectedRevision,
		OccurrenceDate:   request.OccurrenceDate,
		Action:           request.Action,
	}
	if err := h.CalendarService.Occurrence(c.Request.Context(), principal, c.Param("eventId"), input); err != nil {
		writeCalendarError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

var calendarErrorMappings = []errorMapping{
	runTokenInvalidMapping,
	{calendar.ErrNotFound, http.StatusNotFound, "calendar event not found", "calendar_not_found"},
	{calendar.ErrInvalid, http.StatusUnprocessableEntity, "check the event fields, time zone and date range", "calendar_invalid"},
	{calendar.ErrConflict, http.StatusConflict, "this event changed; refresh before saving", "calendar_conflict"},
	{calendar.ErrRange, http.StatusUnprocessableEntity, "choose a smaller calendar date range", "calendar_range_exceeded"},
}

func writeCalendarError(c *gin.Context, err error) {
	writeMappedError(c, err, calendarErrorMappings, internalErrorFallback)
}

type calendarResponseRequest struct {
	ExpectedRevision int64  `json:"expectedRevision" binding:"revision"`
	ResponseStatus   string `json:"responseStatus" binding:"required,oneof=accepted tentative declined"`
}

func (h *Handler) calendarCatalog(c *gin.Context) {
	principal, _ := auth.UserPrincipalFromContext(c.Request.Context())
	result, err := h.CalendarService.Catalog(c.Request.Context(), principal)
	if err != nil {
		writeCalendarError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) respondCalendarInvitation(c *gin.Context) {
	request, err := bindRequest[calendarResponseRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, _ := auth.UserPrincipalFromContext(c.Request.Context())
	if err = h.CalendarService.Respond(c.Request.Context(), principal, c.Param("eventId"), request.ExpectedRevision, request.ResponseStatus); err != nil {
		writeCalendarError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
