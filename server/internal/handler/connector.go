package handler

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/util"
)

const (
	maxConnectorMutationBodyBytes = 1024
	maxConnectorResolveBodyBytes  = 32 << 10
)

type connectorActionRequest struct {
	SchemaVersion int `json:"schemaVersion" binding:"required,eq=1"`
}

type connectorOperationResponse struct {
	State string `json:"state"`
}

type connectorResolveRequest struct {
	SchemaVersion int                        `json:"schemaVersion" binding:"required,eq=1"`
	Identities    []connectorIdentityRequest `json:"identities"    binding:"required,min=1,max=64,dive"`
}

type connectorIdentityRequest struct {
	Source   agent.ConnectorSource `json:"source"   binding:"required"`
	NativeID string                `json:"nativeId" binding:"required,max=240"`
}

func (h *Handler) registerConnectorRoutes(group *gin.RouterGroup) {
	if group == nil || h.AuthService == nil || h.CompanyService == nil || h.ConnectorService == nil {
		return
	}
	// The catalog read and its refresh are any member's, for a runtime they
	// registered (ADR 0014): the computer is theirs, so asking it for its
	// list again needs no Company role. The service's context query hides
	// every other member's machine. Resolving and accepting a Connector
	// contract set Company policy and stay connectors.manage writes.
	reads := group.Group(
		"/companies/:companyId/connectors",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	reads.GET("", h.listConnectors)
	reads.POST("/refresh", h.refreshConnectors)
	routes := group.Group(
		"/companies/:companyId/connectors",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionConnectorsManage),
	)
	routes.POST("/resolve", h.resolveConnectors)
	routes.POST("/:connectorId/connect", h.connectConnector)
}

func (h *Handler) resolveConnectors(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeConnectorError(c, agent.ErrUnauthenticated)
		return
	}
	request, err := bindRequest[connectorResolveRequest](c, maxConnectorResolveBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	identities := make([]agent.ConnectorIdentityInput, len(request.Identities))
	for index, identity := range request.Identities {
		identities[index] = agent.ConnectorIdentityInput{
			Source:   identity.Source,
			NativeID: identity.NativeID,
		}
	}
	result, err := h.ConnectorService.Resolve(
		c.Request.Context(),
		principal,
		agent.ConnectorResolveInput{Identities: identities},
	)
	if err != nil {
		writeConnectorError(c, err)
		return
	}
	if result.Connectors == nil {
		result.Connectors = []agent.ConnectorItemV1{}
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) listConnectors(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeConnectorError(c, agent.ErrUnauthenticated)
		return
	}
	input, err := parseConnectorListInput(c)
	if err != nil {
		writeConnectorError(c, err)
		return
	}
	result, err := h.ConnectorService.List(c.Request.Context(), principal, input)
	if err != nil {
		writeConnectorError(c, err)
		return
	}
	if result.Connectors == nil {
		result.Connectors = []agent.ConnectorItemV1{}
	}
	if result.CategoryCounts == nil {
		result.CategoryCounts = []agent.ConnectorCategoryCount{}
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) refreshConnectors(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeConnectorError(c, agent.ErrUnauthenticated)
		return
	}
	if _, err := bindRequest[connectorActionRequest](c, maxConnectorMutationBodyBytes); err != nil {
		writeRequestError(c, err)
		return
	}
	if err := h.ConnectorService.Refresh(c.Request.Context(), principal); err != nil {
		writeConnectorError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, connectorOperationResponse{State: "queued"})
}

func (h *Handler) connectConnector(c *gin.Context) {
	principal, connectorID, ok := connectorMutationContext(c)
	if !ok {
		return
	}
	if _, err := bindRequest[connectorActionRequest](c, maxConnectorMutationBodyBytes); err != nil {
		writeRequestError(c, err)
		return
	}
	if err := h.ConnectorService.Connect(c.Request.Context(), principal, connectorID); err != nil {
		writeConnectorError(c, err)
		return
	}
	c.JSON(http.StatusOK, connectorOperationResponse{State: "complete"})
}

func connectorMutationContext(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeConnectorError(c, agent.ErrUnauthenticated)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	connectorID, err := util.ParseGoUUID(c.Param("connectorId"))
	if err != nil {
		writeConnectorError(c, agent.ErrConnectorNotFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, connectorID, true
}

func parseConnectorListInput(c *gin.Context) (agent.ConnectorListInput, error) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return agent.ConnectorListInput{}, agent.ErrInvalidInput
	}
	allowed := map[string]bool{
		"query": true, "category": true, "provider": true, "status": true, "runtime_id": true,
		"source": true, "review_required": true, "cursor": true, "limit": true,
	}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 || key != "query" && values[0] == "" {
			return agent.ConnectorListInput{}, agent.ErrInvalidInput
		}
	}
	input := agent.ConnectorListInput{}
	if values, exists := query["runtime_id"]; exists {
		runtimeID, err := util.ParseGoUUID(values[0])
		if err != nil {
			return agent.ConnectorListInput{}, agent.ErrInvalidInput
		}
		input.RuntimeID = &runtimeID
	}
	if values, exists := query["provider"]; exists {
		input.Provider = agent.ConnectorProvider(values[0])
		if input.Provider != agent.ConnectorProviderCodex &&
			input.Provider != agent.ConnectorProviderClaude {
			return agent.ConnectorListInput{}, agent.ErrInvalidInput
		}
	}
	if values, exists := query["source"]; exists {
		input.Source = agent.ConnectorSource(values[0])
		switch input.Source {
		case agent.ConnectorSourceCodexApp, agent.ConnectorSourceCodexMCP,
			agent.ConnectorSourceClaudePlugin, agent.ConnectorSourceClaudeMCP, agent.ConnectorSourceClaudeCloud:
		default:
			return agent.ConnectorListInput{}, agent.ErrInvalidInput
		}
		if input.Provider != "" && !strings.HasPrefix(values[0], string(input.Provider)+"_") {
			return agent.ConnectorListInput{}, agent.ErrInvalidInput
		}
	}
	if values, exists := query["query"]; exists {
		input.Query = strings.TrimSpace(values[0])
	}
	if values, exists := query["category"]; exists {
		input.Category = strings.TrimSpace(values[0])
		if input.Category == "" {
			return agent.ConnectorListInput{}, agent.ErrInvalidInput
		}
	}
	if values, exists := query["status"]; exists {
		if values[0] != string(agent.ConnectorStatusConnected) {
			return agent.ConnectorListInput{}, agent.ErrInvalidInput
		}
		input.Status = agent.ConnectorStatusConnected
	}
	if values, exists := query["review_required"]; exists {
		if values[0] != "true" {
			return agent.ConnectorListInput{}, agent.ErrInvalidInput
		}
		input.ReviewRequired = true
	}
	if values, exists := query["cursor"]; exists {
		cursor, err := agent.ParseConnectorCursor(values[0])
		if err != nil {
			return agent.ConnectorListInput{}, err
		}
		input.Cursor = cursor
	}
	if values, exists := query["limit"]; exists {
		limit, err := strconv.Atoi(values[0])
		if err != nil || strconv.Itoa(limit) != values[0] || limit < 1 || limit > 100 {
			return agent.ConnectorListInput{}, agent.ErrInvalidInput
		}
		input.Limit = limit
	}
	return input, nil
}

var connectorErrorMappings = []errorMapping{
	{agent.ErrUnauthenticated, http.StatusUnauthorized, "authentication is required", "authentication_required"},
	{
		agent.ErrForbidden,
		http.StatusForbidden,
		"Your Company role does not allow this action",
		"company_permission_required",
	},
	{agent.ErrInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
	{agent.ErrConnectorNotFound, http.StatusNotFound, "Connector not found", "connector_not_found"},
	// A runtime_id that is not one of the caller's runtimes gets the runtime
	// routes' own answer (ADR 0014), so the catalog is not an oracle either.
	{agent.ErrRuntimeNotFound, http.StatusNotFound, "Runtime not found", "runtime_not_found"},
	{
		agent.ErrConnectorProviderUnavailable,
		http.StatusConflict,
		"Company Connector provider is unavailable",
		"connector_provider_unavailable",
	},
	{
		agent.ErrConnectorConflict,
		http.StatusConflict,
		"Connector policy conflicts with current state",
		"connector_conflict",
	},
	{
		agent.ErrConnectorVersionExhausted,
		http.StatusConflict,
		"Connector policy version is exhausted",
		"connector_version_exhausted",
	},
	{
		agent.ErrConnectorActionUnavailable,
		http.StatusServiceUnavailable,
		"Connector action is unavailable",
		"connector_action_unavailable",
	},
}

func writeConnectorError(c *gin.Context, err error) {
	writeMappedError(
		c,
		err,
		connectorErrorMappings,
		errorFallback{detail: "could not access Connectors", code: "connector_request_failed"},
	)
}
