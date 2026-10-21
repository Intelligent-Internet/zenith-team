package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	provideragent "github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// maxConnectorDiscoveryBodyBytes matches the daemon's own cap on the
// snapshot it sends (internal/daemon capability_types.go). A signed-in Codex
// computer carries the whole hosted app directory, over 2 MiB by itself.
const maxConnectorDiscoveryBodyBytes = 8 << 20

type connectorDiscoveryRequest struct {
	SchemaVersion  int                           `json:"schemaVersion"  binding:"required,eq=1"`
	Generation     string                        `json:"generation"     binding:"required"`
	ManifestDigest string                        `json:"manifestDigest" binding:"required,len=64"`
	ObservedAt     string                        `json:"observedAt"     binding:"required"`
	Items          []connectorObservationRequest `json:"items"          binding:"max=10000,dive"`
}

// connectorDiscoveryReaffirmRequest names the unchanged generation a daemon
// re-observed; it carries no items, so its body is tiny.
type connectorDiscoveryReaffirmRequest struct {
	SchemaVersion  int    `json:"schemaVersion"  binding:"required,eq=1"`
	Generation     string `json:"generation"     binding:"required"`
	ManifestDigest string `json:"manifestDigest" binding:"required,len=64"`
	ObservedAt     string `json:"observedAt"     binding:"required"`
}

type connectorObservationRequest struct {
	Source         agent.ConnectorSource         `json:"source"                   binding:"required"`
	NativeID       string                        `json:"nativeId"                 binding:"required,max=240"`
	Name           string                        `json:"name"                     binding:"required,max=160"`
	Description    *string                       `json:"description,omitempty"`
	Category       *string                       `json:"category,omitempty"`
	IconURL        *string                       `json:"iconUrl,omitempty"`
	InstallCount   *int64                        `json:"installCount,omitempty"   binding:"omitempty,min=0,max=9007199254740991"`
	Status         agent.ConnectorStatus         `json:"status"                   binding:"required"`
	Action         agent.ConnectorAction         `json:"action"                   binding:"required"`
	CanExecute     bool                          `json:"canExecute"`
	ContractDigest string                        `json:"contractDigest,omitempty"`
	Tools          []provideragent.ConnectorTool `json:"tools,omitempty"          binding:"max=128"`
}

func (h *Handler) registerConnectorDaemonRoutes(group *gin.RouterGroup) {
	if group == nil || h.AuthService == nil || h.Queries == nil || h.ConnectorService == nil {
		return
	}
	routes := group.Group("/daemon", middleware.RequireDaemonAuth(h.AuthService))
	routes.PUT(
		"/companies/:companyId/runtimes/:runtimeId/connectors/discovery",
		h.publishConnectorDiscovery,
	)
	routes.POST(
		"/companies/:companyId/runtimes/:runtimeId/connectors/discovery/reaffirm",
		h.reaffirmConnectorDiscovery,
	)
}

func (h *Handler) publishConnectorDiscovery(c *gin.Context) {
	principal, runtime, ok := h.connectorDiscoveryAuthority(c)
	if !ok {
		return
	}

	request, err := bindRequest[connectorDiscoveryRequest](c, maxConnectorDiscoveryBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	generation, generationErr := util.ParseGoUUID(request.Generation)
	observedAt, observedAtErr := protocol.ParseWireTime(request.ObservedAt)
	if generationErr != nil || observedAtErr != nil {
		writeConnectorDiscoveryError(c, agent.ErrInvalidInput)
		return
	}
	items := make([]agent.ConnectorObservationInput, len(request.Items))
	for index, item := range request.Items {
		items[index] = agent.ConnectorObservationInput{
			Source: item.Source, NativeID: item.NativeID, Name: item.Name,
			Description: item.Description, Category: item.Category, IconURL: item.IconURL,
			InstallCount: item.InstallCount,
			Status:       item.Status, Action: item.Action, CanExecute: item.CanExecute,
			ContractDigest: item.ContractDigest, Tools: item.Tools,
		}
	}
	result, err := h.ConnectorService.PublishDiscovery(
		c.Request.Context(),
		principal,
		agent.ConnectorDiscoveryInput{
			CompanyID: runtime.CompanyID, RuntimeID: runtime.ID, DaemonID: runtime.DaemonID,
			Generation: generation, ManifestDigest: request.ManifestDigest,
			ObservedAt: observedAt, Items: items,
		},
	)
	if err != nil {
		writeConnectorDiscoveryError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// reaffirmConnectorDiscovery renews the freshness of the Runtime's unchanged
// current generation. It shares publication's guards and error table; a
// daemon that meets 409 (or 404 from an older server) falls back to a full
// publication.
func (h *Handler) reaffirmConnectorDiscovery(c *gin.Context) {
	principal, runtime, ok := h.connectorDiscoveryAuthority(c)
	if !ok {
		return
	}

	request, err := bindRequest[connectorDiscoveryReaffirmRequest](c, 1<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	generation, generationErr := util.ParseGoUUID(request.Generation)
	observedAt, observedAtErr := protocol.ParseWireTime(request.ObservedAt)
	if generationErr != nil || observedAtErr != nil {
		writeConnectorDiscoveryError(c, agent.ErrInvalidInput)
		return
	}
	result, err := h.ConnectorService.ReaffirmDiscovery(
		c.Request.Context(),
		principal,
		agent.ConnectorDiscoveryReaffirmInput{
			CompanyID: runtime.CompanyID, RuntimeID: runtime.ID, DaemonID: runtime.DaemonID,
			Generation: generation, ManifestDigest: request.ManifestDigest, ObservedAt: observedAt,
		},
	)
	if err != nil {
		writeConnectorDiscoveryError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// connectorDiscoveryAuthority is the guard both discovery writes share: the
// calling daemon registered the Runtime, its user is an active member, and the
// path's Company is the Runtime's own, else the non-disclosing 404. The
// returned principal is scoped to that Company.
func (h *Handler) connectorDiscoveryAuthority(c *gin.Context) (auth.UserPrincipal, db.Runtime, bool) {
	companyID, err := util.ParseGoUUID(c.Param("companyId"))
	if err != nil {
		writeConnectorDiscoveryError(c, agent.ErrRuntimeNotFound)
		return auth.UserPrincipal{}, db.Runtime{}, false
	}
	runtime, ok := h.requireDaemonRuntimeAccess(c, c.Param("runtimeId"))
	if !ok {
		return auth.UserPrincipal{}, db.Runtime{}, false
	}
	if runtime.CompanyID != companyID {
		writeConnectorDiscoveryError(c, agent.ErrRuntimeNotFound)
		return auth.UserPrincipal{}, db.Runtime{}, false
	}
	principal, ok := daemonPrincipal(c)
	if !ok {
		return auth.UserPrincipal{}, db.Runtime{}, false
	}
	principal.CompanyID = companyID
	return principal, runtime, true
}

var connectorDiscoveryErrorMappings = []errorMapping{
	{agent.ErrUnauthenticated, http.StatusUnauthorized, "authentication is required", "authentication_required"},
	{agent.ErrInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
	{agent.ErrRuntimeNotFound, http.StatusNotFound, "Runtime not found", "runtime_not_found"},
	{
		agent.ErrConnectorDiscoveryConflict,
		http.StatusConflict,
		"Connector discovery conflicts with current generation",
		"connector_discovery_conflict",
	},
}

func writeConnectorDiscoveryError(c *gin.Context, err error) {
	writeMappedError(c, err, connectorDiscoveryErrorMappings, internalErrorFallback)
}
