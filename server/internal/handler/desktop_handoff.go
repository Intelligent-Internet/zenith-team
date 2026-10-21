package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// DesktopHandoffService mints and redeems the short-lived, single-use code a
// cookie-authenticated Web session uses to hand a login off to the Desktop
// app. *auth.DefaultDesktopHandoffService satisfies this with zero glue.
type DesktopHandoffService interface {
	Start(context.Context, uuid.UUID) (auth.DesktopHandoffStartResult, error)
	Complete(context.Context, auth.DesktopHandoffCompleteInput) (auth.Result, error)
}

type desktopHandoffStartResponse struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expiresAt"`
}

type desktopHandoffCompleteRequest struct {
	Code           string                   `json:"code"`
	ClientMetadata auth.OAuthClientMetadata `json:"clientMetadata"`
}

func (h *Handler) registerDesktopHandoffRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.DesktopHandoffService == nil {
		return
	}
	authRoutes := group.Group("/auth")
	authRoutes.POST("/desktop/handoff/start", middleware.RequireUserAuth(h.AuthService), h.startDesktopHandoff)
	authRoutes.POST("/desktop/handoff/complete", h.completeDesktopHandoff)
}

// start is called by the cookie-authenticated Web app: requireUserAuth has
// already resolved the caller's principal, but a bearer-authenticated
// Desktop app could present its own access token here, so this handler
// additionally requires the session-cookie AuthKind and a Web transport
// before minting a code for the principal's user. A bearer principal holds
// a valid credential in the wrong container, so it is answered with
// 422 client_transport_mismatch; 401 is reserved for no credential at all.
func (h *Handler) startDesktopHandoff(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	if principal.AuthKind != auth.TokenKindSession || !clientTransportMatches(c, auth.ClientWeb) {
		writeBoundaryError(c, boundaryErrorClientTransportMismatch)
		return
	}
	result, err := h.DesktopHandoffService.Start(c.Request.Context(), principal.UserID)
	if err != nil {
		writeAuthError(c, err)
		return
	}
	c.JSON(http.StatusOK, desktopHandoffStartResponse{
		Code:      result.Code,
		ExpiresAt: protocol.FormatWireTime(result.ExpiresAt),
	})
}

// complete is called unauthenticated by the Desktop app to redeem a handoff
// code for a bearer session; it reuses Handler.writeAuthSuccess so the
// resulting body is identical in shape to every other bearer session
// response (login, signup, OAuth desktop complete, ...).
func (h *Handler) completeDesktopHandoff(c *gin.Context) {
	request, err := decodeAuthJSON[desktopHandoffCompleteRequest](c)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if !clientTransportMatches(c, auth.ClientDesktop) {
		writeBoundaryError(c, boundaryErrorClientTransportMismatch)
		return
	}
	result, err := h.DesktopHandoffService.Complete(c.Request.Context(), auth.DesktopHandoffCompleteInput{
		Code:           request.Code,
		ClientMetadata: request.ClientMetadata,
	})
	if err != nil {
		writeAuthError(c, err)
		return
	}
	h.writeAuthSuccess(c, http.StatusOK, auth.ClientDesktop, result)
}
