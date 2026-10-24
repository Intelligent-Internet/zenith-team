package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/calendar"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

type calendarCommandRequest struct {
	Action           string                `json:"action"           binding:"required,oneof=create update delete respond cancel complete reopen"`
	EventID          string                `json:"eventId"          binding:"omitempty,uuid"`
	ExpectedRevision *int64                `json:"expectedRevision" binding:"omitempty,revision"`
	Input            *calendarInputRequest `json:"input"`
	ResponseStatus   string                `json:"responseStatus"   binding:"omitempty,oneof=accepted tentative declined"`
	OccurrenceDate   string                `json:"occurrenceDate"   binding:"omitempty,datetime=2006-01-02"`
}

func (h *Handler) calendarRunContext(c *gin.Context) (auth.UserPrincipal, calendar.RunContext, bool) {
	p, _ := auth.UserPrincipalFromContext(c.Request.Context())
	conversation, err := util.ParseGoUUID(c.Param("conversationId"))
	if err != nil || h.RunProofResolver == nil || c.GetHeader(runTokenHeader) == "" {
		writeCalendarError(c, execution.ErrRunProofInvalid)
		return p, calendar.RunContext{}, false
	}
	origin, err := h.resolveRunOrigin(c, c.GetHeader(runTokenHeader))
	if err != nil {
		writeCalendarError(c, err)
		return p, calendar.RunContext{}, false
	}
	if !runProofBearer(p, origin) || origin.ConversationID != conversation || origin.ConversationKind != "direct" ||
		origin.OriginUserID == nil ||
		*origin.OriginUserID != p.UserID {
		writeCalendarError(c, execution.ErrRunProofInvalid)
		return p, calendar.RunContext{}, false
	}
	return p, calendar.RunContext{RunID: origin.RunID, ConversationID: conversation, AgentID: origin.AgentID}, true
}

func (h *Handler) readAgentCalendar(c *gin.Context) {
	p, run, ok := h.calendarRunContext(c)
	if !ok {
		return
	}
	fromText, toText := c.Query("from"), c.Query("to")
	if fromText == "" && toText == "" {
		now := time.Now().UTC()
		fromText = now.Add(-24 * time.Hour).Format(time.RFC3339)
		toText = now.Add(30 * 24 * time.Hour).Format(time.RFC3339)
	}
	from, err := time.Parse(time.RFC3339, fromText)
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	to, err := time.Parse(time.RFC3339, toText)
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	result, err := h.CalendarService.ReadForRun(c.Request.Context(), p, run, from, to)
	if err != nil {
		writeCalendarError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) writeAgentCalendar(c *gin.Context) {
	p, run, ok := h.calendarRunContext(c)
	if !ok {
		return
	}
	key, err := util.ParseGoUUID(c.GetHeader(idempotencyHeader))
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	run.RequestKey = key
	request, err := bindRequest[calendarCommandRequest](c, 32<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if request.Input != nil && rejectInlineUI(c,
		storedText{"Calendar event title", request.Input.Title},
		storedText{"Calendar event description", request.Input.Description},
		storedText{"Calendar event location", request.Input.Location},
	) {
		return
	}
	command := calendar.Command{
		Action:         request.Action,
		EventID:        request.EventID,
		ResponseStatus: request.ResponseStatus,
		OccurrenceDate: request.OccurrenceDate,
	}
	if request.ExpectedRevision != nil {
		command.ExpectedRevision = *request.ExpectedRevision
	}
	if request.Input != nil {
		input := request.Input.input()
		command.Input = &input
	}
	result, err := h.CalendarService.WriteForRun(c.Request.Context(), p, run, command)
	if err != nil {
		writeCalendarError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
