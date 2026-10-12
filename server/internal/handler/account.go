package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/account"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/middleware"
)

type AccountService interface {
	Delete(context.Context, auth.UserPrincipal, string) (account.DeleteResult, error)
}

type deleteAccountRequest struct {
	Confirmation string `json:"confirmation"`
}

func (h *Handler) registerAccountRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.AccountService == nil {
		return
	}
	group.DELETE("/me", middleware.RequireUserAuth(h.AuthService), h.deleteAccount)
}

func (h *Handler) deleteAccount(c *gin.Context) {
	request, err := decodeAuthJSON[deleteAccountRequest](c)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	if _, err := h.AccountService.Delete(c.Request.Context(), principal, request.Confirmation); err != nil {
		writeAccountError(c, err)
		return
	}
	clearAuthCookies(c, h.AuthCookies)
	c.Status(http.StatusNoContent)
}

var accountErrorMappings = []errorMapping{
	{
		account.ErrInvalidConfirmation,
		http.StatusUnprocessableEntity,
		"request validation failed",
		"validation_failed",
	},
	{account.ErrUnauthenticated, http.StatusUnauthorized, "authentication is required", "authentication_required"},
	{account.ErrNotFound, http.StatusUnauthorized, "session is invalid or expired", "session_invalid"},
	{account.ErrConflict, http.StatusConflict, "account lifecycle conflict", "account_conflict"},
}

func writeAccountError(c *gin.Context, err error) {
	writeMappedError(c, err, accountErrorMappings, internalErrorFallback)
}
