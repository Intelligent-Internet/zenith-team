package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/util"
	"boardly.dev/boardly/server/pkg/protocol"
)

const maxPluginDiscoveryBodyBytes = 8 << 20

type pluginDiscoveryRequest struct {
	SchemaVersion  int                        `json:"schemaVersion"  binding:"required,eq=1"`
	Generation     string                     `json:"generation"     binding:"required"`
	ManifestDigest string                     `json:"manifestDigest" binding:"required,len=64"`
	ObservedAt     string                     `json:"observedAt"     binding:"required"`
	Items          []pluginObservationRequest `json:"items"          binding:"required,max=10000,dive"`
}

type pluginObservationRequest struct {
	NativeID    string `json:"nativeId"              binding:"required,max=240"`
	Name        string `json:"name"                  binding:"required,max=160"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
	IconURL     string `json:"iconUrl,omitempty"`
	ShareURL    string `json:"shareUrl,omitempty"`

	Status          agent.PluginStatus   `json:"status"                 binding:"required"`
	Installed       bool                 `json:"installed"`
	Enabled         bool                 `json:"enabled"`
	Featured        bool                 `json:"featured"`
	Action          agent.PluginAction   `json:"action"                 binding:"required"`
	CatalogRank     int                  `json:"catalogRank"            binding:"min=0,max=9999"`
	InstallCount    *int64               `json:"installCount,omitempty" binding:"omitempty,min=0,max=9007199254740991"`
	ComponentCounts *pluginCountsRequest `json:"componentCounts"        binding:"required"`

	ConnectorIdentities []pluginConnectorIdentityRequest `json:"connectorIdentities,omitempty" binding:"max=4096,dive"`

	// The Codex presentation, sent only after a heartbeat ack advertised
	// plugin-presentation-v1. The service checks content and Codex gating.
	LongDescription   string   `json:"longDescription,omitempty"   binding:"max=4000"`
	DefaultPrompts    []string `json:"defaultPrompts,omitempty"    binding:"max=3,dive,min=1,max=128"`
	BrandColor        string   `json:"brandColor,omitempty"        binding:"omitempty,len=7"`
	DeveloperName     string   `json:"developerName,omitempty"     binding:"max=120"`
	WebsiteURL        string   `json:"websiteUrl,omitempty"        binding:"max=2048"`
	PrivacyPolicyURL  string   `json:"privacyPolicyUrl,omitempty"  binding:"max=2048"`
	TermsOfServiceURL string   `json:"termsOfServiceUrl,omitempty" binding:"max=2048"`
}

type pluginConnectorIdentityRequest struct {
	Source   agent.ConnectorSource `json:"source"   binding:"required"`
	NativeID string                `json:"nativeId" binding:"required,max=240"`
}

type pluginCountsRequest struct {
	Apps       int `json:"apps"       binding:"min=0,max=2048"`
	MCPServers int `json:"mcpServers" binding:"min=0,max=2048"`
	Skills     int `json:"skills"     binding:"min=0,max=2048"`
}

func (h *Handler) registerPluginDaemonRoutes(group *gin.RouterGroup) {
	if group == nil || h.AuthService == nil || h.Queries == nil || h.PluginService == nil {
		return
	}
	routes := group.Group("/daemon", middleware.RequireDaemonAuth(h.AuthService))
	routes.PUT(
		"/companies/:companyId/runtimes/:runtimeId/plugins/discovery",
		h.publishPluginDiscovery,
	)
}

func (h *Handler) publishPluginDiscovery(c *gin.Context) {
	companyID, err := util.ParseGoUUID(c.Param("companyId"))
	if err != nil {
		writePluginDiscoveryError(c, agent.ErrRuntimeNotFound)
		return
	}
	runtime, ok := h.requireDaemonRuntimeAccess(c, c.Param("runtimeId"))
	if !ok {
		return
	}
	if runtime.CompanyID != companyID {
		writePluginDiscoveryError(c, agent.ErrRuntimeNotFound)
		return
	}
	principal, ok := daemonPrincipal(c)
	if !ok {
		return
	}
	principal.CompanyID = companyID

	request, err := bindRequest[pluginDiscoveryRequest](c, maxPluginDiscoveryBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	generation, generationErr := util.ParseGoUUID(request.Generation)
	observedAt, observedAtErr := protocol.ParseWireTime(request.ObservedAt)
	if generationErr != nil || observedAtErr != nil {
		writePluginDiscoveryError(c, agent.ErrInvalidInput)
		return
	}
	items := make([]agent.PluginObservationInput, len(request.Items))
	for index, item := range request.Items {
		connectorIdentities := make([]agent.PluginConnectorIdentity, len(item.ConnectorIdentities))
		for identityIndex, identity := range item.ConnectorIdentities {
			connectorIdentities[identityIndex] = agent.PluginConnectorIdentity{
				Source:   identity.Source,
				NativeID: identity.NativeID,
			}
		}
		items[index] = agent.PluginObservationInput{
			NativeID: item.NativeID, Name: item.Name, Description: item.Description,
			Category: item.Category, IconURL: item.IconURL, ShareURL: item.ShareURL,
			Status: item.Status, Installed: item.Installed, Enabled: item.Enabled,
			Featured: item.Featured, Action: item.Action, CatalogRank: item.CatalogRank,
			ComponentCounts: agent.PluginComponentCounts{
				Apps:       item.ComponentCounts.Apps,
				MCPServers: item.ComponentCounts.MCPServers,
				Skills:     item.ComponentCounts.Skills,
			},
			ConnectorIdentities: connectorIdentities,
			InstallCount:        item.InstallCount,
			LongDescription:     item.LongDescription,
			DefaultPrompts:      item.DefaultPrompts,
			BrandColor:          item.BrandColor,
			DeveloperName:       item.DeveloperName,
			WebsiteURL:          item.WebsiteURL,
			PrivacyPolicyURL:    item.PrivacyPolicyURL,
			TermsOfServiceURL:   item.TermsOfServiceURL,
		}
	}
	result, err := h.PluginService.PublishDiscovery(
		c.Request.Context(),
		principal,
		agent.PluginDiscoveryInput{
			CompanyID: companyID, RuntimeID: runtime.ID, DaemonID: runtime.DaemonID,
			Generation: generation, ManifestDigest: request.ManifestDigest,
			ObservedAt: observedAt, Items: items,
		},
	)
	if err != nil {
		writePluginDiscoveryError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

var pluginDiscoveryErrorMappings = []errorMapping{
	{agent.ErrUnauthenticated, http.StatusUnauthorized, "authentication is required", "authentication_required"},
	{agent.ErrInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
	{agent.ErrRuntimeNotFound, http.StatusNotFound, "Runtime not found", "runtime_not_found"},
	{
		agent.ErrPluginDiscoveryConflict,
		http.StatusConflict,
		"Plugin discovery conflicts with current generation",
		"plugin_discovery_conflict",
	},
}

func writePluginDiscoveryError(c *gin.Context, err error) {
	writeMappedError(c, err, pluginDiscoveryErrorMappings, internalErrorFallback)
}
