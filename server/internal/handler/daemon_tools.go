package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/pkg/protocol"
)

type daemonToolReportRequest struct {
	Items []protocol.ToolObservation `json:"items" binding:"required,max=4096,dive"`
}

// daemonRuntimeBindingsResponse is one of this daemon's runtimes, as the
// server holds it, and the caller's own bindings on it.
type daemonRuntimeBindingsResponse struct {
	Runtime daemonRuntimeRef `json:"runtime"`
	Items   []agent.Binding  `json:"items"`
}

type daemonRuntimeRef struct {
	ID        string `json:"id"`
	CompanyID string `json:"companyId"`
	Provider  string `json:"provider"`
	DaemonID  string `json:"daemonId"`
}

// daemonRuntimeBindings answers which Agents the daemon's person has bound
// to one of its runtimes. The daemon checks it before a local Tool sign-in
// and before it publishes Tool status. It is the daemon plane's own read
// (D31): the daemon's own session cannot read the member routes for the
// runtime and its bindings. The runtime must be this daemon's and its
// person's, in a Company the person still belongs to; anything else is the
// same 404 as an unknown runtime.
func (h *Handler) daemonRuntimeBindings(c *gin.Context) {
	runtime, ok := h.requireDaemonRuntimeAccess(c, c.Param("runtimeId"))
	if !ok {
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeRuntimeError(c, agent.ErrRuntimeNotFound)
		return
	}
	principal.CompanyID = runtime.CompanyID
	items, err := h.AgentBindingService.ListForViewer(
		c.Request.Context(), principal, agent.BindingListOptions{RuntimeID: &runtime.ID},
	)
	if err != nil {
		writeAgentError(c, err)
		return
	}
	if items == nil {
		items = []agent.Binding{}
	}
	c.JSON(http.StatusOK, daemonRuntimeBindingsResponse{
		Runtime: daemonRuntimeRef{
			ID:        runtime.ID.String(),
			CompanyID: runtime.CompanyID.String(),
			Provider:  runtime.Provider,
			DaemonID:  runtime.DaemonID,
		},
		Items: items,
	})
}

func (h *Handler) daemonReportTools(c *gin.Context) {
	daemonID := strings.TrimSpace(c.GetHeader(middleware.HeaderDaemonID))
	if daemonID == "" {
		writeBoundaryError(c, boundaryErrorDaemonIDRequired)
		return
	}
	runtime, ok := h.requireDaemonRuntimeAccess(c, c.Param("runtimeId"))
	if !ok {
		return
	}
	request, err := bindRequest[daemonToolReportRequest](c, 2<<20)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeRuntimeError(c, agent.ErrRuntimeNotFound)
		return
	}
	principal.CompanyID = runtime.CompanyID
	if err := h.RuntimeService.ReportTools(c.Request.Context(), principal, agent.ToolObservationReport{
		RuntimeID: runtime.ID, DaemonID: daemonID, Items: request.Items,
	}); err != nil {
		writeRuntimeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
