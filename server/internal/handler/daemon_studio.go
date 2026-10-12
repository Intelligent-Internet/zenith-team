package handler

import (
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/pkg/protocol"
)

type studioProjectRequest struct {
	SchemaVersion int    `json:"schemaVersion,omitempty"`
	RequestID     string `json:"requestId,omitempty"`
	Action        string `json:"action"                  binding:"required,oneof=get reserve bind ensure"`
	RunToken      string `json:"runToken"                binding:"required,max=256"`
	Title         string `json:"title,omitempty"         binding:"max=200"`
	Description   string `json:"description,omitempty"   binding:"max=4000"`
	BindingID     string `json:"bindingId,omitempty"     binding:"omitempty,uuid"`
	ProjectID     string `json:"projectId,omitempty"     binding:"omitempty,uuid"`
	APIOrigin     string `json:"apiOrigin,omitempty"     binding:"omitempty,max=512"`
}

type studioAgentResponse struct {
	SchemaVersion int                                 `json:"schemaVersion"`
	RequestID     uuid.UUID                           `json:"requestId"`
	Binding       *conversation.StudioBindingResponse `json:"binding"`
}

func (h *Handler) daemonStudioProject(c *gin.Context) {
	daemonID := strings.TrimSpace(c.GetHeader(middleware.HeaderDaemonID))
	if daemonID == "" {
		writeBoundaryError(c, boundaryErrorDaemonIDRequired)
		return
	}
	runtime, ok := h.requireDaemonRuntimeAccess(c, c.Param("runtimeId"))
	if !ok {
		return
	}
	runID, ok := parseRunPathID(c.Param("runId"))
	if !ok {
		writeBoundaryError(c, boundaryErrorRunNotFound)
		return
	}
	request, err := bindRequest[studioProjectRequest](c, 16<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := daemonPrincipal(c)
	if !ok {
		return
	}
	principal.CompanyID = runtime.CompanyID
	if request.SchemaVersion != 0 ||
		(request.Action != "get" && request.Action != "reserve" && request.Action != "bind") {
		writeMappedError(
			c,
			conversation.ErrStudioEditorUnsupported,
			studioEditorErrorMappings,
			internalErrorFallback,
		)
		return
	}
	result, err := h.StudioProjectService.Resolve(
		c.Request.Context(),
		principal,
		conversation.StudioProjectAccess{RunID: runID, RuntimeID: runtime.ID, DaemonID: daemonID},
		protocol.StudioProjectRequest{
			Action:      request.Action,
			RunToken:    request.RunToken,
			Title:       request.Title,
			Description: request.Description,
			BindingID:   request.BindingID,
			ProjectID:   request.ProjectID,
			APIOrigin:   request.APIOrigin,
		},
	)
	if err != nil {
		writeMappedError(
			c,
			err,
			slices.Concat(studioProjectErrorMappings, studioEditorErrorMappings),
			internalErrorFallback,
		)
		return
	}
	c.JSON(http.StatusOK, result)
}

var studioProjectErrorMappings = []errorMapping{
	{
		conversation.ErrStudioProjectNotFound,
		http.StatusNotFound,
		"Studio project Run not found",
		"studio_project_run_not_found",
	},
	{
		conversation.ErrStudioProjectInvalid,
		http.StatusUnprocessableEntity,
		"invalid Studio project operation",
		"studio_project_invalid",
	},
	{
		conversation.ErrStudioProjectConflict,
		http.StatusConflict,
		"Studio project is already bound",
		"studio_project_conflict",
	},
}
