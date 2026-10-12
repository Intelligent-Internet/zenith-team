package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/util"
)

type taskFailureRequest struct {
	Reason string `json:"reason" binding:"required,min=1,max=2000"`
}

type taskFailureStatus string

const taskFailureStatusFailed taskFailureStatus = "failed"

type taskFailureResponse struct {
	Status taskFailureStatus `json:"status"`
	Task   task.Response     `json:"task"`
}

func (h *Handler) registerTaskFailureRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil ||
		h.TaskService == nil || h.RunProofResolver == nil {
		return
	}
	routes := group.Group(
		"/companies/:companyId/task-failures",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	routes.POST("", h.markTaskFailed)
}

func (h *Handler) markTaskFailed(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	request, err := bindRequest[taskFailureRequest](c, 32<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	origin, requestKey, ok := h.resolveTaskWriteOrigin(c, principal)
	if !ok {
		return
	}
	failed, err := h.TaskService.MarkFailed(
		c.Request.Context(), principal, origin,
		task.FailureInput{RequestKey: requestKey, Reason: request.Reason},
	)
	if err != nil {
		writeTaskFailureError(c, err)
		return
	}
	c.JSON(http.StatusOK, taskFailureResponse{Status: taskFailureStatusFailed, Task: failed})
}

func (h *Handler) resolveTaskWriteOrigin(
	c *gin.Context,
	principal auth.UserPrincipal,
) (execution.RunOrigin, uuid.UUID, bool) {
	requestKey, err := util.ParseGoUUID(c.GetHeader(idempotencyHeader))
	if err != nil {
		writeTaskFailureError(c, task.ErrInvalidInput)
		return execution.RunOrigin{}, uuid.Nil, false
	}
	if h.RunProofResolver == nil {
		writeTaskFailureError(c, errors.New("Task Run proof resolver is not configured"))
		return execution.RunOrigin{}, uuid.Nil, false
	}
	token := c.GetHeader(runTokenHeader)
	if token == "" {
		writeTaskFailureError(c, execution.ErrRunProofInvalid)
		return execution.RunOrigin{}, uuid.Nil, false
	}
	origin, err := h.resolveRunOrigin(c, token)
	if err != nil {
		writeTaskFailureError(c, err)
		return execution.RunOrigin{}, uuid.Nil, false
	}
	if !runProofBearer(principal, origin) || origin.RunID == uuid.Nil ||
		origin.TaskID == nil || !util.ValidUUID(*origin.TaskID) ||
		!util.ValidUUID(origin.AgentID) || !util.ValidUUID(origin.ConversationID) ||
		origin.ConversationKind != "task" {
		writeTaskFailureError(c, execution.ErrRunProofInvalid)
		return execution.RunOrigin{}, uuid.Nil, false
	}
	return origin, requestKey, true
}

var taskFailureErrorMappings = []errorMapping{
	runTokenInvalidMapping,
	{task.ErrInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
	{task.ErrConflict, http.StatusConflict, "Task mutation conflicts with current state", "task_conflict"},
	{task.ErrNotFound, http.StatusNotFound, "Task not found", "task_not_found"},
}

func writeTaskFailureError(c *gin.Context, err error) {
	writeMappedError(c, err, taskFailureErrorMappings, internalErrorFallback)
}
