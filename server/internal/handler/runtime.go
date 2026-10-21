package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type RuntimeService interface {
	ReportTools(context.Context, auth.UserPrincipal, agent.ToolObservationReport) error
	List(context.Context, auth.UserPrincipal, agent.RuntimeListOptions) (agent.RuntimePage, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (agent.Runtime, error)
	Rename(context.Context, auth.UserPrincipal, uuid.UUID, agent.RuntimeRenameInput) (agent.Runtime, error)
	Remove(context.Context, auth.UserPrincipal, uuid.UUID, agent.RuntimeRemovalInput) (agent.RuntimeRemoval, error)
	RegisterRuntimes(context.Context, agent.RuntimeRegistration) ([]agent.Runtime, error)
	DeregisterRuntimes(context.Context, auth.UserPrincipal, agent.RuntimeDeregistration) error
	Heartbeat(context.Context, auth.UserPrincipal, uuid.UUID, string) error
	InitiateModelDiscovery(context.Context, auth.UserPrincipal, uuid.UUID) (agent.ModelDiscoveryRequest, error)
	RequestRefresh(context.Context, auth.UserPrincipal, uuid.UUID) error
	GetModelDiscovery(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
	) (agent.ModelDiscoveryRequest, error)
	ClaimModelDiscovery(context.Context, uuid.UUID) (*agent.ModelDiscoveryRequest, error)
	FinishModelDiscovery(
		context.Context,
		uuid.UUID,
		uuid.UUID,
		agent.ModelDiscoveryResult,
	) (agent.ModelDiscoveryRequest, error)
}

// renameRuntimeRequest is the machine rename body. customName is required
// so that a client cannot rename by accident with an empty object; the
// empty string is the explicit "clear". The 100-rune cap is checked here
// on the raw value and again on the trimmed value before the write.
// applyToMachine renames every runtime the same daemon registered.
type renameRuntimeRequest struct {
	CustomName     *string `json:"customName"     binding:"required,max=100"`
	ApplyToMachine bool    `json:"applyToMachine"`
}

// removeRuntimeRequest says where the caller's bindings on the removed
// runtime go (ADR 0014): another runtime the caller registered, or an
// explicit null to drop them. The key is required — a client never drops
// bindings by sending an empty object — and nullable, which optional[T]
// cannot say, so the raw value is read by parseRuntimeRemovalInput: absent
// is 422, null drops, a string must be a UUID of another runtime. Each
// refusal says which of the three it was.
type removeRuntimeRequest struct {
	TargetRuntimeID json.RawMessage `json:"targetRuntimeId"`
}

func (h *Handler) registerRuntimeRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.RuntimeService == nil {
		return
	}
	routes := group.Group(
		"/companies/:companyId/runtimes",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	routes.GET("", h.listRuntimes)
	routes.GET("/:runtimeId", h.getRuntime)
	// Every runtime route is member-gated (ADR 0014): a runtime is its
	// registrant's alone, whatever their Company role, and the registrant
	// may be a plain member. The registrant decision needs the runtime row,
	// so it lives inside each RuntimeService transaction, which answers
	// another member's machine as not found.
	routes.PATCH("/:runtimeId", h.renameRuntime)
	routes.POST("/:runtimeId/refresh", h.refreshRuntime)
	routes.DELETE("/:runtimeId", h.deleteRuntime)
	routes.POST("/:runtimeId/remove", h.removeRuntime)
	// Model discovery is the machine's to grant, not a permission's
	// (ADR 0013): the registrant asks their own runtime.
	modelRoutes := group.Group(
		"/companies/:companyId/runtimes/:runtimeId/models",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	modelRoutes.POST("", h.initiateRuntimeModelDiscovery)
	modelRoutes.GET("/:requestId", h.getRuntimeModelDiscovery)
}

func (h *Handler) listRuntimes(c *gin.Context) {
	options, err := parseRuntimeListOptions(c.Request.URL.RawQuery)
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	companyID, ok := parseUUIDOrBadRequest(c, c.Param("companyId"), "companyId")
	if !ok {
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	if principal.CompanyID != companyID {
		writeBoundaryError(c, boundaryErrorCompanyWasNotFound)
		return
	}
	page, err := h.RuntimeService.List(c.Request.Context(), principal, options)
	if err != nil {
		writeBoundaryError(c, boundaryErrorRuntimeListingUnavailable)
		return
	}
	c.JSON(http.StatusOK, page)
}

// getRuntime serves one runtime of the caller's company. A malformed path id
// is answered exactly like an unknown one so the response never says
// whether an id exists elsewhere.
func (h *Handler) getRuntime(c *gin.Context) {
	companyID, ok := parseUUIDOrBadRequest(c, c.Param("companyId"), "companyId")
	if !ok {
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	if principal.CompanyID != companyID {
		writeBoundaryError(c, boundaryErrorCompanyWasNotFound)
		return
	}
	runtimeID, err := util.ParseGoUUID(c.Param("runtimeId"))
	if err != nil {
		writeRuntimeError(c, agent.ErrRuntimeNotFound)
		return
	}
	runtime, err := h.RuntimeService.Get(c.Request.Context(), principal, runtimeID)
	if err != nil {
		writeRuntimeError(c, err)
		return
	}
	c.JSON(http.StatusOK, runtime)
}

// renameRuntime sets or clears a machine's custom name. A malformed path id
// is answered like an unknown one, before the body is read.
func (h *Handler) renameRuntime(c *gin.Context) {
	companyID, ok := parseUUIDOrBadRequest(c, c.Param("companyId"), "companyId")
	if !ok {
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	if principal.CompanyID != companyID {
		writeBoundaryError(c, boundaryErrorCompanyWasNotFound)
		return
	}
	runtimeID, err := util.ParseGoUUID(c.Param("runtimeId"))
	if err != nil {
		writeRuntimeError(c, agent.ErrRuntimeNotFound)
		return
	}
	request, err := bindRequest[renameRuntimeRequest](c, 16<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	runtime, err := h.RuntimeService.Rename(c.Request.Context(), principal, runtimeID, agent.RuntimeRenameInput{
		CustomName:     *request.CustomName,
		ApplyToMachine: request.ApplyToMachine,
	})
	if err != nil {
		writeRuntimeError(c, err)
		return
	}
	c.JSON(http.StatusOK, runtime)
}

// removalRequestIdentity is the boundary the two removal routes share: the
// member principal the middleware resolved, fenced to the path's company,
// and the parsed runtime id. A malformed id is answered like an unknown one.
func removalRequestIdentity(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	companyID, ok := parseUUIDOrBadRequest(c, c.Param("companyId"), "companyId")
	if !ok {
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	if principal.CompanyID != companyID {
		writeBoundaryError(c, boundaryErrorCompanyWasNotFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	runtimeID, err := util.ParseGoUUID(c.Param("runtimeId"))
	if err != nil {
		writeRuntimeError(c, agent.ErrRuntimeNotFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, runtimeID, true
}

// deleteRuntime is the removal with no target: no body, the caller's
// bindings on the machine are dropped and their queued work there is
// cancelled. removeRuntime is the same removal with the bindings moved.
func (h *Handler) deleteRuntime(c *gin.Context) {
	principal, runtimeID, ok := removalRequestIdentity(c)
	if !ok {
		return
	}
	if c.Request.ContentLength != 0 {
		if _, err := bindRequest[struct{}](c, 16<<10); err != nil {
			writeRequestError(c, err)
			return
		}
	}
	if _, err := h.RuntimeService.Remove(
		c.Request.Context(), principal, runtimeID, agent.RuntimeRemovalInput{},
	); err != nil {
		writeRuntimeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// removeRuntime is the removal with a destination: the runtime the caller's
// bindings move to, or null to drop them. The binder proves the shape; the
// id is parsed here, and a target naming the runtime being removed is
// refused before any service call.
func (h *Handler) removeRuntime(c *gin.Context) {
	principal, runtimeID, ok := removalRequestIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[removeRuntimeRequest](c, 16<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	input, err := parseRuntimeRemovalInput(request, runtimeID)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	receipt, err := h.RuntimeService.Remove(c.Request.Context(), principal, runtimeID, input)
	if err != nil {
		writeRuntimeError(c, err)
		return
	}
	c.JSON(http.StatusOK, receipt)
}

func parseRuntimeRemovalInput(
	request removeRuntimeRequest,
	runtimeID uuid.UUID,
) (agent.RuntimeRemovalInput, error) {
	raw := bytes.TrimSpace(request.TargetRuntimeID)
	if len(raw) == 0 {
		return agent.RuntimeRemovalInput{}, &requestError{
			key: boundaryErrorValidationFailed,
			detail: "targetRuntimeId is required: another of your runtimes to move your agents to," +
				" or null to remove the agents set up there",
		}
	}
	if bytes.Equal(raw, []byte("null")) {
		return agent.RuntimeRemovalInput{}, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return agent.RuntimeRemovalInput{}, invalidRemovalTarget(err)
	}
	target, err := util.ParseGoUUID(value)
	if err != nil {
		return agent.RuntimeRemovalInput{}, invalidRemovalTarget(err)
	}
	if target == runtimeID {
		return agent.RuntimeRemovalInput{}, &requestError{
			key:    boundaryErrorValidationFailed,
			detail: "targetRuntimeId must be another runtime, not the one being removed",
		}
	}
	return agent.RuntimeRemovalInput{TargetRuntimeID: &target}, nil
}

func invalidRemovalTarget(cause error) *requestError {
	return &requestError{
		key:    boundaryErrorValidationFailed,
		detail: "targetRuntimeId must be a runtime id or null",
		cause:  cause,
	}
}

func parseRuntimeListOptions(rawQuery string) (agent.RuntimeListOptions, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return agent.RuntimeListOptions{}, err
	}
	for key, values := range query {
		if (key != "status" && key != "provider") || len(values) != 1 || values[0] == "" {
			return agent.RuntimeListOptions{}, errors.New("invalid runtime list query")
		}
	}
	options := agent.RuntimeListOptions{}
	if values, exists := query["status"]; exists {
		if values[0] != "online" && values[0] != "offline" {
			return agent.RuntimeListOptions{}, errors.New("invalid runtime status filter")
		}
		options.Status = &values[0]
	}
	if values, exists := query["provider"]; exists {
		if !protocol.IsSupportedProvider(values[0]) {
			return agent.RuntimeListOptions{}, errors.New("invalid runtime provider filter")
		}
		options.Provider = &values[0]
	}
	return options, nil
}

func (h *Handler) initiateRuntimeModelDiscovery(c *gin.Context) {
	if c.Request.ContentLength != 0 {
		if _, err := bindRequest[struct{}](c, 16<<10); err != nil {
			writeRequestError(c, err)
			return
		}
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	runtimeID, err := util.ParseGoUUID(c.Param("runtimeId"))
	if err != nil {
		writeRuntimeError(c, agent.ErrRuntimeNotFound)
		return
	}
	request, err := h.RuntimeService.InitiateModelDiscovery(c.Request.Context(), principal, runtimeID)
	if err != nil {
		writeRuntimeError(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusAccepted, request)
}

// refreshRuntime accepts the request and answers 202 with no body: the
// daemon's fresh registration is the result, delivered through the roster.
func (h *Handler) refreshRuntime(c *gin.Context) {
	if c.Request.ContentLength != 0 {
		if _, err := bindRequest[struct{}](c, 16<<10); err != nil {
			writeRequestError(c, err)
			return
		}
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	runtimeID, err := util.ParseGoUUID(c.Param("runtimeId"))
	if err != nil {
		writeRuntimeError(c, agent.ErrRuntimeNotFound)
		return
	}
	if err := h.RuntimeService.RequestRefresh(c.Request.Context(), principal, runtimeID); err != nil {
		writeRuntimeError(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Status(http.StatusAccepted)
}

func (h *Handler) getRuntimeModelDiscovery(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	runtimeID, err := util.ParseGoUUID(c.Param("runtimeId"))
	if err != nil {
		writeRuntimeError(c, agent.ErrRuntimeNotFound)
		return
	}
	requestID, err := util.ParseGoUUID(c.Param("requestId"))
	if err != nil {
		writeRuntimeError(c, agent.ErrModelDiscoveryNotFound)
		return
	}
	request, err := h.RuntimeService.GetModelDiscovery(
		c.Request.Context(), principal, runtimeID, requestID,
	)
	if err != nil {
		writeRuntimeError(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, request)
}

var runtimeErrorMappings = []errorMapping{
	{agent.ErrRuntimeNotFound, http.StatusNotFound, "Runtime not found", "runtime_not_found"},
	{agent.ErrRuntimeOffline, http.StatusConflict, "Runtime is offline", "runtime_offline"},
	{
		agent.ErrBindingProviderMismatch,
		http.StatusUnprocessableEntity,
		"the target runtime runs a different provider than the agents bound here",
		"agent_binding_provider_mismatch",
	},
	{
		agent.ErrRuntimeInvalidInput,
		http.StatusUnprocessableEntity,
		"request validation failed",
		"validation_failed",
	},
	{
		agent.ErrModelDiscoveryNotFound,
		http.StatusNotFound,
		"Model discovery not found",
		"model_discovery_not_found",
	},
	{
		agent.ErrModelDiscoveryNotRunning,
		http.StatusConflict,
		"Model discovery is not running",
		"model_discovery_not_running",
	},
	{
		agent.ErrModelDiscoveryInvalid,
		http.StatusUnprocessableEntity,
		"request validation failed",
		"validation_failed",
	},
	{
		agent.ErrRuntimeRemoveBlockedOnline,
		http.StatusConflict,
		"Quit Zenith Team on that computer first, or stop its zen daemon, then try again",
		"runtime_remove_blocked",
	},
}

func writeRuntimeError(c *gin.Context, err error) {
	writeMappedError(c, err, runtimeErrorMappings, internalErrorFallback)
}
