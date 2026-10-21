package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

const maxTaskMergeBodyBytes = 8 << 10

type TaskMergeService interface {
	Claim(context.Context, auth.UserPrincipal, uuid.UUID, string) (*task.MergeClaim, error)
	ClaimManual(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		string,
		task.ManualMergeClaimInput,
	) (task.MergeClaim, error)
	RenewLease(context.Context, auth.UserPrincipal, uuid.UUID, string, uuid.UUID, int32) error
	RecordResult(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		string,
		uuid.UUID,
		task.MergeResultInput,
	) error
}

type taskMergeClaimRequest struct {
	DaemonID string `json:"daemonId" binding:"required,max=256"`
}

type taskMergeManualClaimRequest struct {
	DaemonID                    string    `json:"daemonId"                    binding:"required,max=256"`
	TaskID                      string    `json:"taskId"                      binding:"required,uuid"`
	RunID                       uuid.UUID `json:"runId"                       binding:"required"`
	ExpectedRevision            int64     `json:"expectedRevision"            binding:"revision"`
	ExpectedExecutionGeneration int64     `json:"expectedExecutionGeneration" binding:"required,min=1,max=9007199254740991"`
}

type taskMergeResultRequest struct {
	DaemonID     string                 `json:"daemonId"     binding:"required,max=256"`
	Attempt      int32                  `json:"attempt"      binding:"required,min=1"`
	Status       task.MergeResultStatus `json:"status"       binding:"required,oneof=merged blocked"`
	ResultCode   string                 `json:"resultCode"   binding:"max=256"`
	MergedCommit string                 `json:"mergedCommit" binding:"max=256"`
}

type taskMergeLeaseRequest struct {
	DaemonID string `json:"daemonId" binding:"required,max=256"`
	Attempt  int32  `json:"attempt"  binding:"required,min=1"`
}

func (h *Handler) registerTaskMergeRoutes(group *gin.RouterGroup) {
	if group == nil || h.TaskMergeService == nil {
		return
	}
	group.POST("/runtimes/:runtimeId/task-merges/claim", h.claimTaskMerge)
	group.POST("/runtimes/:runtimeId/task-merges/manual", h.claimManualTaskMerge)
	group.POST("/runtimes/:runtimeId/task-merges/:jobId/lease", h.renewTaskMergeLease)
	group.POST("/runtimes/:runtimeId/task-merges/:jobId/result", h.recordTaskMergeResult)
}

func (h *Handler) claimManualTaskMerge(c *gin.Context) {
	runtime, ok := h.requireDaemonRuntimeAccess(c, c.Param("runtimeId"))
	if !ok {
		return
	}
	principal, ok := daemonPrincipal(c)
	if !ok {
		return
	}
	request, err := bindRequest[taskMergeManualClaimRequest](c, maxTaskMergeBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	taskID, err := util.ParseGoUUID(request.TaskID)
	if err != nil {
		writeTaskMergeError(c, task.ErrMergeConflict)
		return
	}
	claim, err := h.TaskMergeService.ClaimManual(
		c.Request.Context(),
		principal,
		runtime.CompanyID,
		runtime.ID,
		request.DaemonID,
		task.ManualMergeClaimInput{
			TaskID: taskID, RunID: request.RunID,
			ExpectedRevision:            request.ExpectedRevision,
			ExpectedExecutionGeneration: request.ExpectedExecutionGeneration,
		},
	)
	if err != nil {
		writeTaskMergeError(c, err)
		return
	}
	c.JSON(http.StatusOK, claim)
}

func (h *Handler) claimTaskMerge(c *gin.Context) {
	runtime, ok := h.requireDaemonRuntimeAccess(c, c.Param("runtimeId"))
	if !ok {
		return
	}
	principal, ok := daemonPrincipal(c)
	if !ok {
		return
	}
	request, err := bindRequest[taskMergeClaimRequest](c, maxTaskMergeBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	claim, err := h.TaskMergeService.Claim(
		c.Request.Context(), principal, runtime.ID, request.DaemonID,
	)
	if err != nil {
		writeTaskMergeError(c, err)
		return
	}
	if claim == nil {
		c.Status(http.StatusNoContent)
		return
	}
	c.JSON(http.StatusOK, *claim)
}

func (h *Handler) renewTaskMergeLease(c *gin.Context) {
	runtime, ok := h.requireDaemonRuntimeAccess(c, c.Param("runtimeId"))
	if !ok {
		return
	}
	principal, ok := daemonPrincipal(c)
	if !ok {
		return
	}
	jobID, err := util.ParseGoUUID(c.Param("jobId"))
	if err != nil {
		writeTaskMergeError(c, task.ErrMergeClaimNotFound)
		return
	}
	request, err := bindRequest[taskMergeLeaseRequest](c, maxTaskMergeBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if err := h.TaskMergeService.RenewLease(
		c.Request.Context(),
		principal,
		runtime.ID,
		request.DaemonID,
		jobID,
		request.Attempt,
	); err != nil {
		writeTaskMergeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) recordTaskMergeResult(c *gin.Context) {
	runtime, ok := h.requireDaemonRuntimeAccess(c, c.Param("runtimeId"))
	if !ok {
		return
	}
	principal, ok := daemonPrincipal(c)
	if !ok {
		return
	}
	jobID, err := util.ParseGoUUID(c.Param("jobId"))
	if err != nil {
		writeTaskMergeError(c, task.ErrMergeClaimNotFound)
		return
	}
	request, err := bindRequest[taskMergeResultRequest](c, maxTaskMergeBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	err = h.TaskMergeService.RecordResult(
		c.Request.Context(),
		principal,
		runtime.ID,
		request.DaemonID,
		jobID,
		task.MergeResultInput{
			Attempt: request.Attempt, Status: request.Status, ResultCode: request.ResultCode,
			MergedCommit: request.MergedCommit,
		},
	)
	if err != nil {
		writeTaskMergeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

var taskMergeErrorMappings = []errorMapping{
	{
		task.ErrResultSelectionRequired,
		http.StatusConflict,
		"Select the delivered results before completing this Task",
		"task_result_selection_required",
	},
	{
		task.ErrResultApplicationRequired,
		http.StatusConflict,
		"Accept the required suggestions before approving these results",
		"task_result_application_required",
	},
	{
		task.ErrMergeClaimNotFound,
		http.StatusNotFound,
		"Task merge claim not found",
		"task_merge_claim_not_found",
	},
	{
		task.ErrMergeConflict,
		http.StatusConflict,
		"Task merge conflicts with current state",
		"task_merge_conflict",
	},
	// A manual merge by a member who may not run Tasks in the Project.
	projectPermissionRequiredMapping,
}

func writeTaskMergeError(c *gin.Context, err error) {
	writeMappedError(c, err, taskMergeErrorMappings, internalErrorFallback)
}
