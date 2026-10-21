package handler

import (
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/internal/workflow"
)

// registerWorkflowRoutes serves the Workflow catalog: the release-owned
// templates the Workflows page renders and launches as Tasks. It is read
// from the same pinned, verified files a Task Run loads its playbook from
// (internal/platformbundle), it is identical for every Company and carries
// no Company data, so any member may read it. Launching is an ordinary Task
// create; nothing here writes. Readiness answers the Workflow's app hints
// for one Agent; the handler bridges the catalog and the Connector domain so
// internal/workflow never imports internal/agent.
func (h *Handler) registerWorkflowRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil {
		return
	}
	group.GET(
		"/companies/:companyId/workflows",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
		h.listWorkflows,
	)
	group.GET(
		"/companies/:companyId/workflows/:workflowKey",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
		h.getWorkflow,
	)
	if h.ConnectorService != nil {
		group.GET(
			"/companies/:companyId/workflows/:workflowKey/readiness",
			middleware.RequireUserAuth(h.AuthService),
			middleware.RequireCompanyMember(h.CompanyService),
			h.getWorkflowReadiness,
		)
	}
}

var workflowErrorMappings = []errorMapping{
	{workflow.ErrNotFound, http.StatusNotFound, "workflow not found", "workflow_not_found"},
	{agent.ErrUnauthenticated, http.StatusUnauthorized, "authentication is required", "authentication_required"},
	{agent.ErrInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
	{agent.ErrNotFound, http.StatusNotFound, "Agent not found", "agent_not_found"},
}

// workflowReadinessResponse is the WorkflowReadiness contract: one entry per
// app hint, in the Workflow's order, for the Agent the Workflow would run as.
// RuntimeID is the Runtime of the viewer's binding of that Agent, null while
// the viewer has not set it up.
type workflowReadinessResponse struct {
	SchemaVersion int                           `json:"schemaVersion"`
	WorkflowKey   string                        `json:"workflowKey"`
	AgentID       uuid.UUID                     `json:"agentId"`
	Provider      agent.ConnectorProvider       `json:"provider"`
	RuntimeID     *uuid.UUID                    `json:"runtimeId"`
	Discovery     agent.ConnectorAppDiscovery   `json:"discovery"`
	Apps          []agent.ConnectorAppReadiness `json:"apps"`
}

type workflowCollectionResponse struct {
	Items []workflow.Summary `json:"items"`
}

func (h *Handler) listWorkflows(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	if _, ok := auth.UserPrincipalFromContext(c.Request.Context()); !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	items, err := workflow.List()
	if err != nil {
		writeMappedError(c, err, workflowErrorMappings, internalErrorFallback)
		return
	}
	c.JSON(http.StatusOK, workflowCollectionResponse{Items: items})
}

func (h *Handler) getWorkflow(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	if _, ok := auth.UserPrincipalFromContext(c.Request.Context()); !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	item, err := workflow.Get(c.Param("workflowKey"))
	if err != nil {
		writeMappedError(c, err, workflowErrorMappings, internalErrorFallback)
		return
	}
	c.JSON(http.StatusOK, item)
}

func (h *Handler) getWorkflowReadiness(c *gin.Context) {
	agentID, ok := parseWorkflowReadinessQuery(c.Request.URL.RawQuery)
	if !ok {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	item, err := workflow.Get(c.Param("workflowKey"))
	if err != nil {
		writeMappedError(c, err, workflowErrorMappings, internalErrorFallback)
		return
	}
	apps := make([]agent.ConnectorAppType, len(item.Connectors))
	for index, app := range item.Connectors {
		apps[index] = agent.ConnectorAppType(app)
	}
	readiness, err := h.ConnectorService.ReadAppReadiness(
		c.Request.Context(),
		principal,
		agent.ConnectorAppReadinessInput{AgentID: agentID, Apps: apps},
	)
	if err != nil {
		writeMappedError(c, err, workflowErrorMappings, internalErrorFallback)
		return
	}
	var runtimeID *uuid.UUID
	if readiness.RuntimeID.Valid {
		runtimeID = &readiness.RuntimeID.UUID
	}
	c.JSON(http.StatusOK, workflowReadinessResponse{
		SchemaVersion: 1, WorkflowKey: item.Key, AgentID: readiness.AgentID,
		Provider: readiness.Provider, RuntimeID: runtimeID,
		Discovery: readiness.Discovery, Apps: readiness.Apps,
	})
}

// parseWorkflowReadinessQuery admits exactly one agent_id and nothing else.
func parseWorkflowReadinessQuery(raw string) (uuid.UUID, bool) {
	query, err := url.ParseQuery(raw)
	if err != nil || len(query) != 1 || len(query["agent_id"]) != 1 {
		return uuid.Nil, false
	}
	agentID, err := util.ParseGoUUID(query["agent_id"][0])
	return agentID, err == nil
}
