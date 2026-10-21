package handler

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

const maxPluginMutationBodyBytes = 1024

type pluginActionRequest struct {
	SchemaVersion int `json:"schemaVersion" binding:"required,eq=1"`
}

type pluginOperationResponse struct {
	State string `json:"state"`
}

func (h *Handler) registerPluginRoutes(group *gin.RouterGroup) {
	if group == nil || h.AuthService == nil || h.CompanyService == nil || h.PluginService == nil {
		return
	}
	// The catalog read and its refresh are any member's, for a runtime they
	// registered (ADR 0014): the computer is theirs, so asking it for its
	// list again needs no Company role. The service's context query hides
	// every other member's machine.
	reads := group.Group(
		"/companies/:companyId/plugins",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	reads.GET("", h.listPlugins)
	reads.GET("/:pluginId", h.getPlugin)
	reads.POST("/refresh", h.refreshPlugins)
	// Install and uninstall take plugins.manage, which every role holds:
	// they reach only the caller's own computer.
	routes := group.Group(
		"/companies/:companyId/plugins",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionPluginsManage),
	)
	routes.POST("/:pluginId/install", h.installPlugin)
	routes.POST("/:pluginId/uninstall", h.uninstallPlugin)
}

func (h *Handler) listPlugins(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writePluginError(c, agent.ErrUnauthenticated)
		return
	}
	input, err := parsePluginListInput(c)
	if err != nil {
		writePluginError(c, err)
		return
	}
	result, err := h.PluginService.List(c.Request.Context(), principal, input)
	if err != nil {
		writePluginError(c, err)
		return
	}
	if result.Plugins == nil {
		result.Plugins = []agent.PluginItemV1{}
	}
	if result.CategoryCounts == nil {
		result.CategoryCounts = []agent.PluginCategoryCount{}
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getPlugin(c *gin.Context) {
	principal, pluginID, ok := pluginRequestContext(c)
	if !ok {
		return
	}
	runtimeID, err := parsePluginDetailRuntime(c)
	if err != nil {
		writePluginError(c, err)
		return
	}
	result, err := h.PluginService.Get(
		c.Request.Context(),
		principal,
		agent.PluginDetailInput{PluginID: pluginID, RuntimeID: runtimeID},
	)
	if err != nil {
		writePluginError(c, err)
		return
	}
	if result.DefaultPrompts == nil {
		result.DefaultPrompts = []string{}
	}
	if result.Apps == nil {
		result.Apps = []agent.PluginDetailApp{}
	}
	if result.UsedBy == nil {
		result.UsedBy = []agent.PluginAgentUse{}
	}
	c.JSON(http.StatusOK, result)
}

// parsePluginDetailRuntime admits exactly one optional query parameter, the
// catalog's runtime_id.
func parsePluginDetailRuntime(c *gin.Context) (*uuid.UUID, error) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return nil, agent.ErrInvalidInput
	}
	for key, values := range query {
		if key != "runtime_id" || len(values) != 1 || values[0] == "" {
			return nil, agent.ErrInvalidInput
		}
	}
	values, exists := query["runtime_id"]
	if !exists {
		return nil, nil
	}
	runtimeID, err := util.ParseGoUUID(values[0])
	if err != nil {
		return nil, agent.ErrInvalidInput
	}
	return &runtimeID, nil
}

func (h *Handler) refreshPlugins(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writePluginError(c, agent.ErrUnauthenticated)
		return
	}
	if _, err := bindRequest[pluginActionRequest](c, maxPluginMutationBodyBytes); err != nil {
		writeRequestError(c, err)
		return
	}
	if err := h.PluginService.Refresh(c.Request.Context(), principal); err != nil {
		writePluginError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, pluginOperationResponse{State: "queued"})
}

func (h *Handler) installPlugin(c *gin.Context) {
	principal, pluginID, ok := pluginRequestContext(c)
	if !ok {
		return
	}
	if _, err := bindRequest[pluginActionRequest](c, maxPluginMutationBodyBytes); err != nil {
		writeRequestError(c, err)
		return
	}
	if err := h.PluginService.Install(c.Request.Context(), principal, pluginID); err != nil {
		writePluginError(c, err)
		return
	}
	c.JSON(http.StatusOK, pluginOperationResponse{State: "complete"})
}

func (h *Handler) uninstallPlugin(c *gin.Context) {
	principal, pluginID, ok := pluginRequestContext(c)
	if !ok {
		return
	}
	if _, err := bindRequest[pluginActionRequest](c, maxPluginMutationBodyBytes); err != nil {
		writeRequestError(c, err)
		return
	}
	if err := h.PluginService.Uninstall(c.Request.Context(), principal, pluginID); err != nil {
		writePluginError(c, err)
		return
	}
	c.JSON(http.StatusOK, pluginOperationResponse{State: "complete"})
}

func pluginRequestContext(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writePluginError(c, agent.ErrUnauthenticated)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	pluginID, err := util.ParseGoUUID(c.Param("pluginId"))
	if err != nil {
		writePluginError(c, agent.ErrPluginNotFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, pluginID, true
}

func parsePluginListInput(c *gin.Context) (agent.PluginListInput, error) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return agent.PluginListInput{}, agent.ErrInvalidInput
	}
	allowed := map[string]bool{
		"query": true, "category": true, "provider": true, "status": true, "runtime_id": true,
		"featured": true, "cursor": true, "limit": true,
	}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 || key != "query" && values[0] == "" {
			return agent.PluginListInput{}, agent.ErrInvalidInput
		}
	}
	input := agent.PluginListInput{}
	if values, exists := query["runtime_id"]; exists {
		runtimeID, err := util.ParseGoUUID(values[0])
		if err != nil {
			return agent.PluginListInput{}, agent.ErrInvalidInput
		}
		input.RuntimeID = &runtimeID
	}
	if values, exists := query["provider"]; exists {
		input.Provider = agent.ConnectorProvider(values[0])
		if input.Provider != agent.ConnectorProviderCodex &&
			input.Provider != agent.ConnectorProviderClaude {
			return agent.PluginListInput{}, agent.ErrInvalidInput
		}
	}
	if values, exists := query["query"]; exists {
		input.Query = strings.TrimSpace(values[0])
	}
	if values, exists := query["category"]; exists {
		input.Category = strings.TrimSpace(values[0])
		if input.Category == "" {
			return agent.PluginListInput{}, agent.ErrInvalidInput
		}
	}
	if values, exists := query["status"]; exists {
		status := agent.PluginStatus(values[0])
		switch status {
		case agent.PluginStatusAvailable, agent.PluginStatusInstalled,
			agent.PluginStatusDisabled, agent.PluginStatusError:
			input.Status = status
		default:
			return agent.PluginListInput{}, agent.ErrInvalidInput
		}
	}
	if values, exists := query["featured"]; exists {
		if values[0] != "true" {
			return agent.PluginListInput{}, agent.ErrInvalidInput
		}
		input.Featured = true
	}
	if values, exists := query["cursor"]; exists {
		cursor, err := agent.ParsePluginCursor(values[0])
		if err != nil {
			return agent.PluginListInput{}, err
		}
		input.Cursor = cursor
	}
	if values, exists := query["limit"]; exists {
		limit, err := strconv.Atoi(values[0])
		if err != nil || strconv.Itoa(limit) != values[0] || limit < 1 || limit > 100 {
			return agent.PluginListInput{}, agent.ErrInvalidInput
		}
		input.Limit = limit
	}
	return input, nil
}

var pluginErrorMappings = []errorMapping{
	{agent.ErrUnauthenticated, http.StatusUnauthorized, "authentication is required", "authentication_required"},
	{
		agent.ErrForbidden,
		http.StatusForbidden,
		"Your Company role does not allow this action",
		"company_permission_required",
	},
	{agent.ErrInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
	{agent.ErrPluginNotFound, http.StatusNotFound, "Plugin not found", "plugin_not_found"},
	// A runtime_id that is not one of the caller's runtimes gets the runtime
	// routes' own answer (ADR 0014), so the catalog is not an oracle either.
	{agent.ErrRuntimeNotFound, http.StatusNotFound, "Runtime not found", "runtime_not_found"},
	{
		agent.ErrPluginProviderUnavailable,
		http.StatusConflict,
		"Company Plugin provider is unavailable",
		"plugin_provider_unavailable",
	},
	{
		agent.ErrPluginActionUnavailable,
		http.StatusServiceUnavailable,
		"Plugin action is unavailable",
		"plugin_action_unavailable",
	},
}

func writePluginError(c *gin.Context, err error) {
	writeMappedError(
		c,
		err,
		pluginErrorMappings,
		errorFallback{detail: "could not access Plugins", code: "plugin_request_failed"},
	)
}
