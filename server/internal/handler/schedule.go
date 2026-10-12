package handler

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/schedule"
	"boardly.dev/boardly/server/internal/util"
	"boardly.dev/boardly/server/pkg/protocol"
)

const (
	maxScheduleBodyBytes = 128 << 10
	defaultScheduleLimit = 50
	maximumScheduleLimit = 100
	scheduleCursorSize   = 25
	scheduleCursorV1     = byte(1)
)

// errInvalidScheduleCursor is the handler-local sentinel for a malformed or
// unsupported list cursor. The cursor is the handler's own encoding, so the
// domain never sees it, and a malformed one is answered as invalid_cursor
// rather than folded into validation_failed.
var errInvalidScheduleCursor = errors.New("invalid Schedule cursor")

type ScheduleService interface {
	Preview(context.Context, auth.UserPrincipal, schedule.PreviewInput) (schedule.PreviewResult, error)
	Create(context.Context, auth.UserPrincipal, schedule.CreateInput) (schedule.CreateResult, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (schedule.Response, error)
	List(context.Context, auth.UserPrincipal, schedule.ListOptions) (schedule.Page, error)
	Update(context.Context, auth.UserPrincipal, uuid.UUID, schedule.UpdateInput) (schedule.MutationResult, error)
	Pause(context.Context, auth.UserPrincipal, uuid.UUID, schedule.RevisionInput) (schedule.MutationResult, error)
	Resume(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		schedule.ReviewRevisionInput,
	) (schedule.MutationResult, error)
	Cancel(context.Context, auth.UserPrincipal, uuid.UUID, schedule.RevisionInput) (schedule.MutationResult, error)
	RunNow(context.Context, auth.UserPrincipal, uuid.UUID, schedule.RunNowInput) (schedule.RunNowResult, error)
	ListFirings(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		schedule.FiringListOptions,
	) (schedule.FiringPage, error)
}

type scheduleTriggerRequest struct {
	Type       string  `json:"type"       binding:"required,oneof=once cron"`
	RunAt      *string `json:"runAt"`
	Expression *string `json:"expression"`
	TimeZone   *string `json:"timeZone"`
}

type scheduleDraftRequest struct {
	SchemaVersion int                    `json:"schemaVersion" binding:"required,eq=1"`
	AgentID       string                 `json:"agentId"       binding:"required,uuid"`
	Name          string                 `json:"name"`
	Instructions  string                 `json:"instructions"`
	PresetKey     *string                `json:"presetKey"`
	Trigger       scheduleTriggerRequest `json:"trigger"       binding:"required"`
	SourceIDs     []string               `json:"sourceIds"     binding:"max=128,dive,uuid"`
}

type previewScheduleRequest = scheduleDraftRequest

// Create and update repeat the draft fields because the shared strict decoder
// treats anonymous struct fields as nested wire fields; these requests are flat
// JSON objects in the frontend contract.
type createScheduleRequest struct {
	SchemaVersion int                    `json:"schemaVersion" binding:"required,eq=1"`
	AgentID       string                 `json:"agentId"       binding:"required,uuid"`
	Name          string                 `json:"name"`
	Instructions  string                 `json:"instructions"`
	PresetKey     *string                `json:"presetKey"`
	Trigger       scheduleTriggerRequest `json:"trigger"       binding:"required"`
	SourceIDs     []string               `json:"sourceIds"     binding:"max=128,dive,uuid"`
	RequestKey    string                 `json:"requestKey"    binding:"required,uuid"`
	ReviewDigest  string                 `json:"reviewDigest"  binding:"required,len=64,lowercase,hexadecimal"`
}

type updateScheduleRequest struct {
	SchemaVersion    int                    `json:"schemaVersion"    binding:"required,eq=1"`
	AgentID          string                 `json:"agentId"          binding:"required,uuid"`
	Name             string                 `json:"name"`
	Instructions     string                 `json:"instructions"`
	PresetKey        *string                `json:"presetKey"`
	Trigger          scheduleTriggerRequest `json:"trigger"          binding:"required"`
	SourceIDs        []string               `json:"sourceIds"        binding:"max=128,dive,uuid"`
	ExpectedRevision int64                  `json:"expectedRevision" binding:"revision"`
	ReviewDigest     string                 `json:"reviewDigest"     binding:"required,len=64,lowercase,hexadecimal"`
}

func (r createScheduleRequest) draft() scheduleDraftRequest {
	return scheduleDraftRequest{
		SchemaVersion: r.SchemaVersion, AgentID: r.AgentID, Name: r.Name,
		Instructions: r.Instructions, PresetKey: r.PresetKey, Trigger: r.Trigger,
		SourceIDs: r.SourceIDs,
	}
}

func (r updateScheduleRequest) draft() scheduleDraftRequest {
	return scheduleDraftRequest{
		SchemaVersion: r.SchemaVersion, AgentID: r.AgentID, Name: r.Name,
		Instructions: r.Instructions, PresetKey: r.PresetKey, Trigger: r.Trigger,
		SourceIDs: r.SourceIDs,
	}
}

type scheduleLifecycleRequest struct {
	SchemaVersion    int    `json:"schemaVersion"    binding:"required,eq=1"`
	ExpectedRevision int64  `json:"expectedRevision" binding:"revision"`
	ReviewDigest     string `json:"reviewDigest"     binding:"omitempty,len=64,lowercase,hexadecimal"`
}

type runScheduleRequest struct {
	SchemaVersion int    `json:"schemaVersion" binding:"required,eq=1"`
	RequestKey    string `json:"requestKey"    binding:"required,uuid"`
}

type scheduleSourceResponse struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	NativeID string    `json:"nativeId"`
	Provider string    `json:"provider"`
	Source   string    `json:"source"`
}

type scheduleTriggerResponse interface {
	isScheduleTriggerResponse()
}

type scheduleOnceTriggerType string

const scheduleOnceTrigger scheduleOnceTriggerType = "once"

type scheduleOnceTriggerResponse struct {
	Type  scheduleOnceTriggerType `json:"type"`
	RunAt string                  `json:"runAt"`
}

func (scheduleOnceTriggerResponse) isScheduleTriggerResponse() {}

type scheduleCronTriggerType string

const scheduleCronTrigger scheduleCronTriggerType = "cron"

type scheduleCronTriggerResponse struct {
	Type       scheduleCronTriggerType `json:"type"`
	Expression string                  `json:"expression"`
	TimeZone   string                  `json:"timeZone"`
}

func (scheduleCronTriggerResponse) isScheduleTriggerResponse() {}

type scheduleResponse struct {
	ID           uuid.UUID                `json:"id"`
	AgentID      uuid.UUID                `json:"agentId"`
	Name         string                   `json:"name"`
	Instructions string                   `json:"instructions"`
	PresetKey    *string                  `json:"presetKey"`
	Provider     string                   `json:"provider"`
	Trigger      scheduleTriggerResponse  `json:"trigger"`
	Sources      []scheduleSourceResponse `json:"sources"`
	Status       schedule.Status          `json:"status"`
	PauseReason  *string                  `json:"pauseReason"`
	NextRunAt    *string                  `json:"nextRunAt"`
	LastRunAt    *string                  `json:"lastRunAt"`
	Revision     int64                    `json:"revision"`
	CreatedAt    string                   `json:"createdAt"`
	UpdatedAt    string                   `json:"updatedAt"`
}

func (r *scheduleResponse) UnmarshalJSON(data []byte) error {
	type responseAlias scheduleResponse
	decoded := struct {
		Trigger json.RawMessage `json:"trigger"`
		*responseAlias
	}{responseAlias: (*responseAlias)(r)}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var discriminator struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(decoded.Trigger, &discriminator); err != nil {
		return fmt.Errorf("decode Schedule trigger discriminator: %w", err)
	}
	switch discriminator.Type {
	case string(scheduleOnceTrigger):
		var trigger scheduleOnceTriggerResponse
		if err := json.Unmarshal(decoded.Trigger, &trigger); err != nil {
			return fmt.Errorf("decode one-shot Schedule trigger: %w", err)
		}
		r.Trigger = trigger
	case string(scheduleCronTrigger):
		var trigger scheduleCronTriggerResponse
		if err := json.Unmarshal(decoded.Trigger, &trigger); err != nil {
			return fmt.Errorf("decode cron Schedule trigger: %w", err)
		}
		r.Trigger = trigger
	default:
		return fmt.Errorf("decode Schedule trigger: unsupported type %q", discriminator.Type)
	}
	return nil
}

type schedulePreviewResponse struct {
	NormalizedTrigger scheduleTriggerResponse  `json:"normalizedTrigger"`
	HumanSchedule     string                   `json:"humanSchedule"`
	NextOccurrences   []string                 `json:"nextOccurrences"`
	Provider          string                   `json:"provider"`
	Sources           []scheduleSourceResponse `json:"sources"`
	ReviewDigest      string                   `json:"reviewDigest"`
}

type schedulePageResponse struct {
	Schedules  []scheduleResponse `json:"schedules"`
	NextCursor *string            `json:"nextCursor"`
}

type scheduleFiringResponse struct {
	FiringID       uuid.UUID                  `json:"firingId"`
	ConversationID uuid.UUID                  `json:"conversationId"`
	MessageID      *uuid.UUID                 `json:"messageId"`
	MessageSeq     *int64                     `json:"messageSeq"`
	OccurrenceAt   string                     `json:"occurrenceAt"`
	Trigger        schedule.FiringTrigger     `json:"trigger"`
	Disposition    schedule.FiringDisposition `json:"disposition"`
	Reason         *string                    `json:"reason"`
	RunID          *uuid.UUID                 `json:"runId"`
	RunStatus      *string                    `json:"runStatus"`
	Output         *string                    `json:"output"`
	CreatedAt      string                     `json:"createdAt"`
}

type scheduleFiringPageResponse struct {
	Runs       []scheduleFiringResponse `json:"runs"`
	NextCursor *string                  `json:"nextCursor"`
}

func (h *Handler) registerScheduleRoutes(group *gin.RouterGroup) {
	if group == nil || h.AuthService == nil || h.CompanyService == nil || h.ScheduleService == nil {
		return
	}
	routes := group.Group(
		"/companies/:companyId/schedules",
		schedulePrivateNoStore(),
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionSchedulesManage),
	)
	routes.POST("/preview", h.previewSchedule)
	routes.GET("", h.listSchedules)
	routes.POST("", h.createSchedule)
	routes.GET("/:scheduleId", h.getSchedule)
	routes.PATCH("/:scheduleId", h.updateSchedule)
	routes.POST("/:scheduleId/pause", h.pauseSchedule)
	routes.POST("/:scheduleId/resume", h.resumeSchedule)
	routes.POST("/:scheduleId/cancel", h.cancelSchedule)
	routes.GET("/:scheduleId/runs", h.listScheduleFirings)
	routes.POST("/:scheduleId/runs", h.runScheduleNow)
}

func schedulePrivateNoStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "private, no-store")
		c.Next()
	}
}

func (h *Handler) previewSchedule(c *gin.Context) {
	principal, ok := schedulePrincipal(c)
	if !ok {
		return
	}
	request, err := bindRequest[previewScheduleRequest](c, maxScheduleBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if request.SourceIDs == nil {
		writeScheduleError(c, schedule.ErrInvalidInput)
		return
	}
	input, err := schedulePreviewInput(request)
	if err != nil {
		writeScheduleError(c, err)
		return
	}
	preview, err := h.ScheduleService.Preview(c.Request.Context(), principal, input)
	if err != nil {
		writeScheduleError(c, err)
		return
	}
	c.JSON(http.StatusOK, schedulePreviewToResponse(preview))
}

func (h *Handler) createSchedule(c *gin.Context) {
	principal, ok := schedulePrincipal(c)
	if !ok {
		return
	}
	request, err := bindRequest[createScheduleRequest](c, maxScheduleBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if request.SourceIDs == nil {
		writeScheduleError(c, schedule.ErrInvalidInput)
		return
	}
	preview, err := schedulePreviewInput(request.draft())
	if err != nil {
		writeScheduleError(c, err)
		return
	}
	requestKey, err := util.ParseGoUUID(request.RequestKey)
	if err != nil {
		writeScheduleError(c, schedule.ErrInvalidInput)
		return
	}
	result, err := h.ScheduleService.Create(c.Request.Context(), principal, schedule.CreateInput{
		RequestKey: requestKey, AgentID: preview.AgentID,
		Name: preview.Name, Instructions: preview.Instructions, PresetKey: preview.PresetKey,
		Trigger: preview.Trigger, ConnectorIDs: preview.ConnectorIDs,
		Status: schedule.StatusActive, ReviewDigest: request.ReviewDigest,
	})
	if err != nil {
		writeScheduleError(c, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, scheduleToResponse(result.Schedule))
}

func (h *Handler) getSchedule(c *gin.Context) {
	principal, scheduleID, ok := scheduleRequestIdentity(c)
	if !ok {
		return
	}
	result, err := h.ScheduleService.Get(c.Request.Context(), principal, scheduleID)
	if err != nil {
		writeScheduleError(c, err)
		return
	}
	c.JSON(http.StatusOK, scheduleToResponse(result))
}

func (h *Handler) listSchedules(c *gin.Context) {
	principal, ok := schedulePrincipal(c)
	if !ok {
		return
	}
	options, err := parseScheduleListOptions(c)
	if err != nil {
		writeScheduleError(c, err)
		return
	}
	page, err := h.ScheduleService.List(c.Request.Context(), principal, options)
	if err != nil {
		writeScheduleError(c, err)
		return
	}
	response := schedulePageResponse{Schedules: make([]scheduleResponse, len(page.Schedules))}
	for index, item := range page.Schedules {
		response.Schedules[index] = scheduleToResponse(item)
	}
	if page.NextCursor != nil {
		value, err := formatScheduleCursor(page.NextCursor.CreatedAt, page.NextCursor.ID)
		if err != nil {
			writeScheduleError(c, err)
			return
		}
		response.NextCursor = &value
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) updateSchedule(c *gin.Context) {
	principal, scheduleID, ok := scheduleRequestIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[updateScheduleRequest](c, maxScheduleBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if request.SourceIDs == nil {
		writeScheduleError(c, schedule.ErrInvalidInput)
		return
	}
	preview, err := schedulePreviewInput(request.draft())
	if err != nil {
		writeScheduleError(c, err)
		return
	}
	result, err := h.ScheduleService.Update(c.Request.Context(), principal, scheduleID, schedule.UpdateInput{
		ExpectedRevision: request.ExpectedRevision,
		AgentID:          &preview.AgentID, Name: &preview.Name, Instructions: &preview.Instructions,
		PresetKey:    schedule.NullableTextChange{Present: true, Value: preview.PresetKey},
		Trigger:      &preview.Trigger,
		Sources:      schedule.SourceChange{Present: true, ConnectorIDs: preview.ConnectorIDs},
		ReviewDigest: request.ReviewDigest,
	})
	if err != nil {
		writeScheduleError(c, err)
		return
	}
	c.JSON(http.StatusOK, scheduleToResponse(result.Schedule))
}

func (h *Handler) pauseSchedule(c *gin.Context) {
	h.scheduleLifecycle(c, "pause")
}

func (h *Handler) resumeSchedule(c *gin.Context) {
	h.scheduleLifecycle(c, "resume")
}

func (h *Handler) cancelSchedule(c *gin.Context) {
	h.scheduleLifecycle(c, "cancel")
}

func (h *Handler) scheduleLifecycle(c *gin.Context, operation string) {
	principal, scheduleID, ok := scheduleRequestIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[scheduleLifecycleRequest](c, maxScheduleBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	var result schedule.MutationResult
	switch operation {
	case "pause":
		if request.ReviewDigest != "" {
			err = schedule.ErrInvalidInput
			break
		}
		result, err = h.ScheduleService.Pause(
			c.Request.Context(),
			principal,
			scheduleID,
			schedule.RevisionInput{
				ExpectedRevision: request.ExpectedRevision,
			},
		)
	case "resume":
		if request.ReviewDigest == "" {
			err = schedule.ErrInvalidInput
			break
		}
		result, err = h.ScheduleService.Resume(
			c.Request.Context(),
			principal,
			scheduleID,
			schedule.ReviewRevisionInput{
				ExpectedRevision: request.ExpectedRevision, ReviewDigest: request.ReviewDigest,
			},
		)
	case "cancel":
		if request.ReviewDigest != "" {
			err = schedule.ErrInvalidInput
			break
		}
		result, err = h.ScheduleService.Cancel(
			c.Request.Context(),
			principal,
			scheduleID,
			schedule.RevisionInput{
				ExpectedRevision: request.ExpectedRevision,
			},
		)
	default:
		err = schedule.ErrInvalidInput
	}
	if err != nil {
		writeScheduleError(c, err)
		return
	}
	c.JSON(http.StatusOK, scheduleToResponse(result.Schedule))
}

func (h *Handler) runScheduleNow(c *gin.Context) {
	principal, scheduleID, ok := scheduleRequestIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[runScheduleRequest](c, maxScheduleBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	requestKey, err := util.ParseGoUUID(request.RequestKey)
	if err != nil {
		writeScheduleError(c, schedule.ErrInvalidInput)
		return
	}
	result, err := h.ScheduleService.RunNow(
		c.Request.Context(), principal, scheduleID, schedule.RunNowInput{RequestKey: requestKey},
	)
	if err != nil {
		writeScheduleError(c, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, scheduleFiringToResponse(result.Firing))
}

func (h *Handler) listScheduleFirings(c *gin.Context) {
	principal, scheduleID, ok := scheduleRequestIdentity(c)
	if !ok {
		return
	}
	options, err := parseScheduleFiringListOptions(c)
	if err != nil {
		writeScheduleError(c, err)
		return
	}
	page, err := h.ScheduleService.ListFirings(c.Request.Context(), principal, scheduleID, options)
	if err != nil {
		writeScheduleError(c, err)
		return
	}
	response := scheduleFiringPageResponse{Runs: make([]scheduleFiringResponse, len(page.Firings))}
	for index, firing := range page.Firings {
		response.Runs[index] = scheduleFiringToResponse(firing)
	}
	if page.NextCursor != nil {
		value, err := formatScheduleCursor(page.NextCursor.OccurrenceAt, page.NextCursor.ID)
		if err != nil {
			writeScheduleError(c, err)
			return
		}
		response.NextCursor = &value
	}
	c.JSON(http.StatusOK, response)
}

func schedulePreviewInput(request scheduleDraftRequest) (schedule.PreviewInput, error) {
	agentID, err := util.ParseGoUUID(request.AgentID)
	if err != nil || request.SourceIDs == nil {
		return schedule.PreviewInput{}, schedule.ErrInvalidInput
	}
	trigger, err := parseScheduleTrigger(request.Trigger)
	if err != nil {
		return schedule.PreviewInput{}, err
	}
	sourceIDs := make([]uuid.UUID, len(request.SourceIDs))
	for index, raw := range request.SourceIDs {
		sourceIDs[index], err = util.ParseGoUUID(raw)
		if err != nil {
			return schedule.PreviewInput{}, schedule.ErrInvalidInput
		}
	}
	return schedule.PreviewInput{
		AgentID: agentID, Name: request.Name, Instructions: request.Instructions,
		PresetKey: request.PresetKey, Trigger: trigger, ConnectorIDs: sourceIDs,
	}, nil
}

func parseScheduleTrigger(request scheduleTriggerRequest) (schedule.Trigger, error) {
	trigger := schedule.Trigger{Type: schedule.TriggerType(request.Type)}
	switch trigger.Type {
	case schedule.TriggerTypeOnce:
		if request.RunAt == nil || request.Expression != nil || request.TimeZone != nil {
			return schedule.Trigger{}, schedule.ErrInvalidInput
		}
		runAt, err := protocol.ParseWireTime(*request.RunAt)
		if err != nil {
			return schedule.Trigger{}, schedule.ErrInvalidInput
		}
		trigger.RunAt = &runAt
	case schedule.TriggerTypeCron:
		if request.RunAt != nil || request.Expression == nil || request.TimeZone == nil {
			return schedule.Trigger{}, schedule.ErrInvalidInput
		}
		trigger.Expression = request.Expression
		trigger.TimeZone = request.TimeZone
	default:
		return schedule.Trigger{}, schedule.ErrInvalidInput
	}
	return trigger, nil
}

func scheduleToResponse(value schedule.Response) scheduleResponse {
	sources := make([]scheduleSourceResponse, len(value.Sources))
	for index, source := range value.Sources {
		sources[index] = scheduleSourceResponse{
			ID: source.ConnectorID, Name: source.NativeID, NativeID: source.NativeID,
			Provider: source.Provider, Source: source.Source,
		}
	}
	return scheduleResponse{
		ID: value.ID, AgentID: value.AgentID, Name: value.Name, Instructions: value.Instructions,
		PresetKey: value.PresetKey, Provider: value.ReviewedProvider,
		Trigger: scheduleTriggerToResponse(value.Trigger),
		Sources: sources, Status: value.Status, PauseReason: value.PauseReason,
		NextRunAt: protocol.FormatOptionalWireTime(value.NextOccurrenceAt),
		LastRunAt: protocol.FormatOptionalWireTime(value.LastOccurrenceAt),
		Revision:  value.Revision, CreatedAt: protocol.FormatWireTime(value.CreatedAt),
		UpdatedAt: protocol.FormatWireTime(value.UpdatedAt),
	}
}

func schedulePreviewToResponse(value schedule.PreviewResult) schedulePreviewResponse {
	occurrences := make([]string, len(value.Occurrences))
	for index, occurrence := range value.Occurrences {
		occurrences[index] = protocol.FormatWireTime(occurrence)
	}
	sources := make([]scheduleSourceResponse, len(value.Sources))
	for index, source := range value.Sources {
		sources[index] = scheduleSourceResponse{
			ID: source.ConnectorID, Name: source.NativeID, NativeID: source.NativeID,
			Provider: source.Provider, Source: source.Source,
		}
	}
	return schedulePreviewResponse{
		NormalizedTrigger: scheduleTriggerToResponse(value.Trigger),
		HumanSchedule:     humanSchedule(value.Trigger),
		NextOccurrences:   occurrences, Provider: value.ReviewedProvider,
		Sources: sources, ReviewDigest: value.ReviewDigest,
	}
}

func scheduleTriggerToResponse(value schedule.Trigger) scheduleTriggerResponse {
	if value.Type == schedule.TriggerTypeOnce {
		return scheduleOnceTriggerResponse{
			Type: scheduleOnceTrigger, RunAt: protocol.FormatWireTime(*value.RunAt),
		}
	}
	return scheduleCronTriggerResponse{
		Type: scheduleCronTrigger, Expression: *value.Expression, TimeZone: *value.TimeZone,
	}
}

func scheduleFiringToResponse(value schedule.Firing) scheduleFiringResponse {
	return scheduleFiringResponse{
		FiringID: value.ID, ConversationID: value.ConversationID, MessageID: value.MessageID,
		MessageSeq:   value.MessageSeq,
		OccurrenceAt: protocol.FormatWireTime(value.OccurrenceAt),
		Trigger:      value.Trigger, Disposition: value.Disposition, Reason: value.Reason,
		RunID: value.RunID, RunStatus: value.RunStatus, Output: value.Output,
		CreatedAt: protocol.FormatWireTime(value.CreatedAt),
	}
}

func humanSchedule(trigger schedule.Trigger) string {
	if trigger.Type == schedule.TriggerTypeOnce && trigger.RunAt != nil {
		return "Once at " + protocol.FormatWireTime(*trigger.RunAt)
	}
	if trigger.Type == schedule.TriggerTypeCron && trigger.Expression != nil && trigger.TimeZone != nil {
		return describeCron(*trigger.Expression) + " · " + *trigger.TimeZone
	}
	return ""
}

var cronWeekdays = [...]string{
	"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday",
}

// describeCron puts the common cron shapes in plain words, such as "Every
// weekday at 09:00", and returns any other expression as it is. The caller
// adds the time zone, since a time of day means nothing without it.
func describeCron(expression string) string {
	fields := strings.Fields(expression)
	if len(fields) != 5 || fields[3] != "*" {
		return expression
	}
	minuteField, hourField, dayField, weekdayField := fields[0], fields[1], fields[2], fields[4]
	anyDay := dayField == "*" && weekdayField == "*"
	if step, ok := cronStep(minuteField, 60); ok && hourField == "*" && anyDay {
		return fmt.Sprintf("Every %d minutes", step)
	}
	minute, ok := cronValue(minuteField, 0, 59)
	if !ok {
		return expression
	}
	pastHour := ""
	if minute != 0 {
		pastHour = fmt.Sprintf(" at :%02d", minute)
	}
	if hourField == "*" && anyDay {
		return "Every hour" + pastHour
	}
	if step, ok := cronStep(hourField, 24); ok && anyDay {
		return fmt.Sprintf("Every %d hours%s", step, pastHour)
	}
	hour, ok := cronValue(hourField, 0, 23)
	if !ok {
		return expression
	}
	at := fmt.Sprintf(" at %02d:%02d", hour, minute)
	if anyDay {
		return "Every day" + at
	}
	if dayField == "*" && weekdayField == "1-5" {
		return "Every weekday" + at
	}
	if dayField == "*" {
		if days, ok := cronWeekdayNames(weekdayField); ok {
			return "Every " + days + at
		}
		return expression
	}
	// Days 29 to 31 are missing from some months, so they keep the cron text.
	if day, ok := cronValue(dayField, 1, 28); ok && weekdayField == "*" {
		return fmt.Sprintf("Every month on day %d%s", day, at)
	}
	return expression
}

// cronStep reads "*/N" when N divides the cycle, so the gap is always N.
func cronStep(field string, cycle int) (int, bool) {
	text, ok := strings.CutPrefix(field, "*/")
	if !ok {
		return 0, false
	}
	step, err := strconv.Atoi(text)
	return step, err == nil && step > 1 && step < cycle && cycle%step == 0
}

func cronValue(field string, minimum, maximum int) (int, bool) {
	value, err := strconv.Atoi(field)
	return value, err == nil && value >= minimum && value <= maximum
}

// cronWeekdayNames names a list of single weekdays, such as "1,3,5" as
// "Monday, Wednesday and Friday".
func cronWeekdayNames(field string) (string, bool) {
	values := strings.Split(field, ",")
	names := make([]string, 0, len(values))
	seen := make(map[int]bool, len(values))
	for _, value := range values {
		day, ok := cronValue(value, 0, len(cronWeekdays)-1)
		if !ok || seen[day] {
			return "", false
		}
		seen[day] = true
		names = append(names, cronWeekdays[day])
	}
	if len(names) == 1 {
		return names[0], true
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1], true
}

func schedulePrincipal(c *gin.Context) (auth.UserPrincipal, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeScheduleError(c, schedule.ErrUnauthenticated)
	}
	return principal, ok
}

func scheduleRequestIdentity(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := schedulePrincipal(c)
	if !ok {
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	scheduleID, err := util.ParseGoUUID(c.Param("scheduleId"))
	if err != nil {
		writeScheduleError(c, schedule.ErrNotFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, scheduleID, true
}

func parseScheduleListOptions(c *gin.Context) (schedule.ListOptions, error) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return schedule.ListOptions{}, schedule.ErrInvalidInput
	}
	allowed := map[string]bool{"cursor": true, "limit": true, "preset_key": true, "status": true}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 || values[0] == "" {
			return schedule.ListOptions{}, schedule.ErrInvalidInput
		}
	}
	options := schedule.ListOptions{Limit: defaultScheduleLimit}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || strconv.Itoa(limit) != value || limit < 1 || limit > maximumScheduleLimit {
			return schedule.ListOptions{}, schedule.ErrInvalidInput
		}
		options.Limit = limit
	}
	if value := query.Get("status"); value != "" {
		status := schedule.Status(value)
		switch status {
		case schedule.StatusDraft, schedule.StatusActive, schedule.StatusPaused,
			schedule.StatusCompleted, schedule.StatusCancelled:
		default:
			return schedule.ListOptions{}, schedule.ErrInvalidInput
		}
		options.Status = &status
	}
	if value := query.Get("preset_key"); value != "" {
		options.PresetKey = &value
	}
	if value := query.Get("cursor"); value != "" {
		at, id, err := parseScheduleCursor(value)
		if err != nil {
			return schedule.ListOptions{}, err
		}
		options.After = &schedule.ListCursor{CreatedAt: at, ID: id}
	}
	return options, nil
}

func parseScheduleFiringListOptions(c *gin.Context) (schedule.FiringListOptions, error) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return schedule.FiringListOptions{}, schedule.ErrInvalidInput
	}
	for key, values := range query {
		if (key != "cursor" && key != "limit") || len(values) != 1 || values[0] == "" {
			return schedule.FiringListOptions{}, schedule.ErrInvalidInput
		}
	}
	options := schedule.FiringListOptions{Limit: defaultScheduleLimit}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || strconv.Itoa(limit) != value || limit < 1 || limit > maximumScheduleLimit {
			return schedule.FiringListOptions{}, schedule.ErrInvalidInput
		}
		options.Limit = limit
	}
	if value := query.Get("cursor"); value != "" {
		at, id, err := parseScheduleCursor(value)
		if err != nil {
			return schedule.FiringListOptions{}, err
		}
		options.After = &schedule.FiringCursor{OccurrenceAt: at, ID: id}
	}
	return options, nil
}

func formatScheduleCursor(at time.Time, id uuid.UUID) (string, error) {
	if at.IsZero() || !util.ValidUUID(id) || at.Nanosecond()%1000 != 0 {
		return "", schedule.ErrInvalidInput
	}
	payload := make([]byte, scheduleCursorSize)
	payload[0] = scheduleCursorV1
	binary.BigEndian.PutUint64(payload[1:9], uint64(at.UTC().UnixMicro()))
	copy(payload[9:], id[:])
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func parseScheduleCursor(raw string) (time.Time, uuid.UUID, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(decoded) != scheduleCursorSize || decoded[0] != scheduleCursorV1 {
		return time.Time{}, uuid.Nil, errInvalidScheduleCursor
	}
	micros := int64(binary.BigEndian.Uint64(decoded[1:9]))
	id, err := uuid.FromBytes(decoded[9:])
	at := time.UnixMicro(micros).UTC()
	canonical, formatErr := formatScheduleCursor(at, id)
	if err != nil || formatErr != nil || canonical != raw {
		return time.Time{}, uuid.Nil, errInvalidScheduleCursor
	}
	return at, id, nil
}

var scheduleErrorMappings = []errorMapping{
	{schedule.ErrUnauthenticated, http.StatusUnauthorized, "authentication is required", "authentication_required"},
	{
		schedule.ErrForbidden,
		http.StatusForbidden,
		"Your Company role does not allow this action",
		"company_permission_required",
	},
	{schedule.ErrInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
	{schedule.ErrNotFound, http.StatusNotFound, "Schedule not found", "schedule_not_found"},
	{
		schedule.ErrVersionConflict,
		http.StatusConflict,
		"Schedule changed in another session",
		"schedule_version_conflict",
	},
	{schedule.ErrReviewStale, http.StatusConflict, "Schedule review is stale", "schedule_review_stale"},
	{
		schedule.ErrSelectionUnavailable,
		http.StatusConflict,
		"Selected Agent or sources are unavailable",
		"schedule_conflict",
	},
	// Every firing runs on the creator's own binding of the Agent (ADR 0014):
	// a creator without one gets the setup step every enqueue names.
	{
		schedule.ErrAgentBindingRequired,
		http.StatusConflict,
		"set this Agent up on one of your computers first",
		"agent_binding_required",
	},
	{schedule.ErrRunActive, http.StatusConflict, "Schedule already has an active Run", "schedule_run_active"},
	{schedule.ErrTerminal, http.StatusConflict, "Schedule is terminal", "schedule_terminal"},
	{
		schedule.ErrInvalidState,
		http.StatusConflict,
		"Schedule state does not allow this operation",
		"schedule_invalid_state",
	},
	{schedule.ErrConflict, http.StatusConflict, "Schedule conflicts with current state", "schedule_conflict"},
	{errInvalidScheduleCursor, http.StatusUnprocessableEntity, "cursor is invalid", "invalid_cursor"},
}

func writeScheduleError(c *gin.Context, err error) {
	writeMappedError(
		c,
		err,
		scheduleErrorMappings,
		errorFallback{detail: "could not access Schedules", code: "schedule_request_failed"},
	)
}
