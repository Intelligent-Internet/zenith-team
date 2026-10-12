package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/middleware"
)

const maxProactiveModeBodyBytes = 8 << 10

type ProactiveModeService interface {
	Get(context.Context, auth.UserPrincipal) (company.ProactiveMode, error)
	Update(
		context.Context,
		auth.UserPrincipal,
		company.UpdateProactiveModeInput,
	) (company.ProactiveMode, error)
}

type updateProactiveModeRequest struct {
	Enabled          optional[bool] `json:"enabled"          binding:"omitnil,required"`
	ExpectedRevision int64          `json:"expectedRevision" binding:"revision"`
}

func (h *Handler) registerProactiveModeRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.ProactiveModeService == nil {
		return
	}
	routes := group.Group(
		"/companies/:companyId",
		middleware.RequireUserAuth(h.AuthService),
	)
	routes.GET(
		"/proactive-mode",
		middleware.RequireCompanyMember(h.CompanyService),
		h.getProactiveMode,
	)
	routes.PATCH(
		"/proactive-mode",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionCompanyProactiveModeManage),
		h.updateProactiveMode,
	)
}

func (h *Handler) getProactiveMode(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.ProactiveModeService.Get(c.Request.Context(), principal)
	if err != nil {
		writeProactiveModeError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) updateProactiveMode(c *gin.Context) {
	request, err := bindRequest[updateProactiveModeRequest](c, maxProactiveModeBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.ProactiveModeService.Update(
		c.Request.Context(),
		principal,
		company.UpdateProactiveModeInput{
			Enabled:          request.Enabled.value,
			ExpectedRevision: request.ExpectedRevision,
		},
	)
	if err != nil {
		writeProactiveModeError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

var proactiveModeErrorMappings = []errorMapping{
	{
		company.ErrUnauthenticated,
		http.StatusUnauthorized,
		"authentication is required",
		"authentication_required",
	},
	{
		company.ErrProactiveModeInvalidInput,
		http.StatusUnprocessableEntity,
		"request validation failed",
		"validation_failed",
	},
	{
		company.ErrProactiveModeConflict,
		http.StatusConflict,
		"Proactive Mode changed; refresh and retry",
		"proactive_mode_conflict",
	},
	{
		company.ErrProactiveModeConversationRequired,
		http.StatusConflict,
		"CEO conversation is required",
		"proactive_mode_conversation_required",
	},
	{
		company.ErrNotFound,
		http.StatusNotFound,
		"Company not found",
		"company_not_found",
	},
	{
		company.ErrForbidden,
		http.StatusForbidden,
		"Your Company role does not allow this action",
		"company_permission_required",
	},
}

func writeProactiveModeError(c *gin.Context, err error) {
	writeMappedError(c, err, proactiveModeErrorMappings, internalErrorFallback)
}
