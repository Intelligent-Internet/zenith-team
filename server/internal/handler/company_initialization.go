package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
)

type advanceInitializationRequest struct {
	ConversationID   *uuid.UUID `json:"conversationId"`
	ModelDiscoveryID *uuid.UUID `json:"modelDiscoveryId"`
	Retry            bool       `json:"retry"`
}

func (h *Handler) registerInitializationRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.InitializationService == nil {
		return
	}
	routes := group.Group(
		"/companies/:companyId/initialization",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionAgentsManage),
		middleware.RequireHumanActor(),
	)
	routes.GET("", h.getInitialization)
	routes.POST("", h.advanceInitialization)
}

func (h *Handler) getInitialization(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.InitializationService.Get(c.Request.Context(), p)
	if err != nil {
		writeInitializationError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) advanceInitialization(c *gin.Context) {
	request, err := bindRequest[advanceInitializationRequest](c, 8<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	input := company.InitializationInput{Retry: request.Retry}
	if request.ConversationID != nil {
		input.ConversationID = *request.ConversationID
	}
	if request.ModelDiscoveryID != nil {
		input.ModelDiscoveryID = *request.ModelDiscoveryID
	}
	result, err := h.InitializationService.Advance(c.Request.Context(), p, input)
	if err != nil {
		writeInitializationError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

var initializationErrorMappings = []errorMapping{
	{
		company.ErrInitializationConflict,
		http.StatusConflict,
		"Initialization cannot advance; refresh and retry",
		"initialization_conflict",
	},
	{
		company.ErrInitializationModel,
		http.StatusConflict,
		"Luna is not available on this Codex runtime. Refresh model discovery and try again.",
		"initialization_model_unavailable",
	},
}

func writeInitializationError(c *gin.Context, err error) {
	mappings := append([]errorMapping{}, initializationErrorMappings...)
	mappings = append(mappings, companyErrorMappings...)
	mappings = append(mappings, runtimeErrorMappings...)
	mappings = append(mappings, conversationErrorMappings...)
	writeMappedError(c, err, mappings, internalErrorFallback)
}
