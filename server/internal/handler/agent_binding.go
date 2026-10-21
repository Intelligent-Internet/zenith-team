package handler

import (
	"context"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

// AgentBindingService is the member's placement of an Agent template on one
// of their own runtimes (ADR 0014): set up, take down, list mine.
type AgentBindingService interface {
	Bind(context.Context, auth.UserPrincipal, uuid.UUID, agent.BindInput) (agent.Response, error)
	Unbind(context.Context, auth.UserPrincipal, uuid.UUID) error
	ListForViewer(context.Context, auth.UserPrincipal, agent.BindingListOptions) ([]agent.Binding, error)
}

// bindAgentRequest names the caller's runtime the agent runs on. Required,
// so a binding is never set by an empty object.
type bindAgentRequest struct {
	RuntimeID string `json:"runtimeId" binding:"required,uuid"`
}

type agentBindingCollectionResponse struct {
	Items []agent.Binding `json:"items"`
}

func (h *Handler) registerAgentBindingRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.AgentBindingService == nil {
		return
	}
	// Member-gated, every one of them: a binding is a member's private setup
	// of an Agent on their own machine, so no Company permission is
	// involved, and the service answers another member's runtime as not
	// found rather than forbidden.
	routes := group.Group(
		"/companies/:companyId",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	routes.PUT("/agents/:agentId/binding", h.bindAgent)
	routes.DELETE("/agents/:agentId/binding", h.unbindAgent)
	routes.GET("/agent-bindings", h.listMyAgentBindings)
}

func (h *Handler) bindAgent(c *gin.Context) {
	principal, agentID, ok := agentMutationIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[bindAgentRequest](c, 16<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	runtimeID, err := util.ParseGoUUID(request.RuntimeID)
	if err != nil {
		writeAgentError(c, agent.ErrInvalidInput)
		return
	}
	result, err := h.AgentBindingService.Bind(
		c.Request.Context(), principal, agentID, agent.BindInput{RuntimeID: runtimeID},
	)
	if err != nil {
		writeAgentError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) unbindAgent(c *gin.Context) {
	principal, agentID, ok := agentMutationIdentity(c)
	if !ok {
		return
	}
	if c.Request.ContentLength != 0 {
		if _, err := bindRequest[struct{}](c, 16<<10); err != nil {
			writeRequestError(c, err)
			return
		}
	}
	if err := h.AgentBindingService.Unbind(c.Request.Context(), principal, agentID); err != nil {
		writeAgentError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) listMyAgentBindings(c *gin.Context) {
	options, err := parseAgentBindingListOptions(c.Request.URL.RawQuery)
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	items, err := h.AgentBindingService.ListForViewer(c.Request.Context(), principal, options)
	if err != nil {
		writeAgentError(c, err)
		return
	}
	if items == nil {
		items = []agent.Binding{}
	}
	c.JSON(http.StatusOK, agentBindingCollectionResponse{Items: items})
}

func parseAgentBindingListOptions(rawQuery string) (agent.BindingListOptions, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return agent.BindingListOptions{}, agent.ErrInvalidInput
	}
	for key, values := range query {
		if key != "runtime_id" || len(values) != 1 || values[0] == "" {
			return agent.BindingListOptions{}, agent.ErrInvalidInput
		}
	}
	options := agent.BindingListOptions{}
	if values, exists := query["runtime_id"]; exists {
		runtimeID, err := util.ParseGoUUID(values[0])
		if err != nil {
			return agent.BindingListOptions{}, agent.ErrInvalidInput
		}
		options.RuntimeID = &runtimeID
	}
	return options, nil
}
