package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/util"
)

type TaskReportService interface {
	Preview(context.Context, auth.UserPrincipal, uuid.UUID) (task.ReportPreview, error)
	Send(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		task.SendReportInput,
	) (task.SendReportResult, error)
}

type sendTaskReportsRequest struct {
	RequestKey string `json:"requestKey" binding:"required,uuid"`
}

type taskReportOutcome string

const (
	taskReportOutcomeDone   taskReportOutcome = "done"
	taskReportOutcomeFailed taskReportOutcome = "failed"
)

type taskReportCompletionSource string

const (
	taskReportCompletionSourceManual         taskReportCompletionSource = "manual"
	taskReportCompletionSourceReviewAccepted taskReportCompletionSource = "review_accepted"
	taskReportCompletionSourceAgentFailure   taskReportCompletionSource = "agent_business_failure"
)

type taskReportEventResponse struct {
	ID               uuid.UUID                  `json:"id"`
	TaskID           uuid.UUID                  `json:"taskId"`
	TaskKey          string                     `json:"taskKey"`
	TaskTitle        string                     `json:"taskTitle"`
	Outcome          taskReportOutcome          `json:"outcome"`
	CompletionSource taskReportCompletionSource `json:"completionSource"`
	CompletedAt      time.Time                  `json:"completedAt"`
	ResultSummary    string                     `json:"resultSummary"`
}

type taskReportPreviewResponse struct {
	RecipientAgentID uuid.UUID                 `json:"recipientAgentId"`
	ConversationID   uuid.UUID                 `json:"conversationId"`
	PendingCount     int64                     `json:"pendingCount"`
	AgentBusy        bool                      `json:"agentBusy"`
	Truncated        bool                      `json:"truncated"`
	Events           []taskReportEventResponse `json:"events"`
}

func taskReportPreviewToResponse(value task.ReportPreview) taskReportPreviewResponse {
	events := make([]taskReportEventResponse, len(value.Events))
	for index, event := range value.Events {
		outcome := taskReportOutcomeDone
		if event.Outcome == task.StatusFailed {
			outcome = taskReportOutcomeFailed
		}
		completionSource := taskReportCompletionSourceManual
		switch event.CompletionSource {
		case "review_accepted":
			completionSource = taskReportCompletionSourceReviewAccepted
		case "agent_business_failure":
			completionSource = taskReportCompletionSourceAgentFailure
		}
		events[index] = taskReportEventResponse{
			ID: event.ID, TaskID: event.TaskID, TaskKey: event.TaskKey, TaskTitle: event.TaskTitle,
			Outcome:          outcome,
			CompletionSource: completionSource, CompletedAt: event.CompletedAt,
			ResultSummary: event.ResultSummary,
		}
	}
	return taskReportPreviewResponse{
		RecipientAgentID: value.RecipientAgentID, ConversationID: value.ConversationID,
		PendingCount: value.PendingCount, AgentBusy: value.AgentBusy,
		Truncated: value.Truncated, Events: events,
	}
}

func (h *Handler) registerTaskReportRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.TaskReportService == nil {
		return
	}
	routes := group.Group(
		"/companies/:companyId/conversations/:conversationId/task-reports",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	routes.GET("", h.previewTaskReports)
	routes.POST("/send", h.sendTaskReports)
}

func (h *Handler) previewTaskReports(c *gin.Context) {
	principal, conversationID, ok := taskReportRequestIdentity(c)
	if !ok {
		return
	}
	result, err := h.TaskReportService.Preview(c.Request.Context(), principal, conversationID)
	if err != nil {
		writeTaskError(c, err)
		return
	}
	c.JSON(http.StatusOK, taskReportPreviewToResponse(result))
}

func (h *Handler) sendTaskReports(c *gin.Context) {
	principal, conversationID, ok := taskReportRequestIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[sendTaskReportsRequest](c, maxTaskBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	requestKey, err := util.ParseGoUUID(request.RequestKey)
	if err != nil {
		writeTaskError(c, task.ErrInvalidInput)
		return
	}
	result, err := h.TaskReportService.Send(
		c.Request.Context(), principal, conversationID,
		task.SendReportInput{RequestKey: requestKey},
	)
	if err != nil {
		writeTaskError(c, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, result)
}

func taskReportRequestIdentity(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	conversationID, err := util.ParseGoUUID(c.Param("conversationId"))
	if err != nil {
		writeTaskError(c, task.ErrNotFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, conversationID, true
}
