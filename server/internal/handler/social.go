package handler

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/social"
)

type socialPlatformCollection struct {
	Items []social.Platform `json:"items"`
}
type socialConnectionCollection struct {
	Items []social.Connection `json:"items"`
}
type socialOAuthStartResponse struct {
	AuthorizationURL string `json:"authorizationUrl"`
}

func (h *Handler) listSocialPlatforms(c *gin.Context) {
	c.JSON(http.StatusOK, socialPlatformCollection{Items: h.SocialService.Platforms()})
}

func (h *Handler) registerSocialRoutes(api *gin.RouterGroup) {
	if h.AuthService == nil || h.SocialService == nil {
		return
	}
	group := api.Group("/me/social", middleware.RequireUserAuth(h.AuthService), middleware.RequireHumanActor())
	group.GET("/platforms", h.listSocialPlatforms)
	group.GET("/connections", h.listSocialConnections)
	group.POST("/oauth/:platform", h.startSocialOAuth)
	group.POST("/oauth/:platform/desktop", h.startSocialDesktopOAuth)
	group.DELETE("/connections/:connectionId", h.disconnectSocial)
	api.GET("/social/oauth/:platform/callback", h.completeSocialOAuth)
	api.GET("/social/oauth/:platform/launch", h.launchSocialDesktopOAuth)
	if h.CompanyService != nil {
		run := api.Group(
			"/companies/:companyId/conversations/:conversationId/social",
			middleware.RequireUserAuth(h.AuthService),
			middleware.RequireCompanyMember(h.CompanyService),
		)
		run.GET("", h.readAgentSocial)
		run.POST("/posts", h.publishAgentSocial)
	}
}

type socialDesktopRequest struct {
	RedirectURI string `json:"redirectUri" binding:"required,max=200"`
	State       string `json:"state"       binding:"required,len=43"`
}

func (h *Handler) startSocialDesktopOAuth(c *gin.Context) {
	request, err := bindRequest[socialDesktopRequest](c, 1024)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	p, _ := auth.UserPrincipalFromContext(c.Request.Context())
	result, err := h.SocialService.StartDesktop(
		c.Request.Context(),
		p,
		c.Param("platform"),
		social.DesktopInput{RedirectURI: request.RedirectURI, State: request.State},
	)
	if err != nil {
		writeSocialError(c, err)
		return
	}
	c.JSON(http.StatusOK, socialOAuthStartResponse{AuthorizationURL: result.AuthorizationURL})
}

func (h *Handler) launchSocialDesktopOAuth(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	result, err := h.SocialService.LaunchDesktop(
		c.Request.Context(),
		c.Param("platform"),
		c.Query("state"),
		c.Query("ticket"),
	)
	if err != nil {
		writeSocialError(c, err)
		return
	}
	h.setSocialBinding(c, c.Param("platform"), result.Binding, 600)
	c.Header("Location", result.AuthorizationURL)
	c.Status(http.StatusSeeOther)
}

func (h *Handler) listSocialConnections(c *gin.Context) {
	principal, _ := auth.UserPrincipalFromContext(c.Request.Context())
	items, err := h.SocialService.List(c.Request.Context(), principal)
	if err != nil {
		writeSocialError(c, err)
		return
	}
	c.JSON(http.StatusOK, socialConnectionCollection{Items: items})
}

func (h *Handler) startSocialOAuth(c *gin.Context) {
	principal, _ := auth.UserPrincipalFromContext(c.Request.Context())
	result, err := h.SocialService.Start(c.Request.Context(), principal, c.Param("platform"))
	if err != nil {
		writeSocialError(c, err)
		return
	}
	h.setSocialBinding(c, c.Param("platform"), result.Binding, 600)
	c.JSON(http.StatusOK, socialOAuthStartResponse{AuthorizationURL: result.AuthorizationURL})
}

func (h *Handler) completeSocialOAuth(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	platform := c.Param("platform")
	switch platform {
	case "x", "facebook", "instagram", "linkedin":
	default:
		writeBoundaryError(c, boundaryErrorRouteNotFound)
		return
	}
	binding, _ := c.Cookie("boardly_social_" + platform)
	h.setSocialBinding(c, platform, "", -1)
	desktopReturn, err := h.SocialService.Complete(
		c.Request.Context(),
		social.CompleteInput{
			Platform:      platform,
			State:         c.Query("state"),
			Binding:       binding,
			Code:          c.Query("code"),
			ProviderError: c.Query("error"),
		},
	)
	status := "connected"
	if err != nil {
		status = "failed"
	}
	// Web origin is deployment configuration; desktop returns only to the validated,
	// single-use loopback destination saved before authorization.
	c.Header(
		"Location",
		strings.TrimRight(
			h.OAuthWebAppOrigin,
			"/",
		)+"/social?oauth="+url.QueryEscape(
			status,
		)+"&platform="+url.QueryEscape(
			platform,
		),
	)
	if desktopReturn != "" {
		c.Header("Location", desktopReturn+"&oauth="+status+"&platform="+url.QueryEscape(platform))
	}
	c.Status(http.StatusSeeOther)
}

func (h *Handler) setSocialBinding(c *gin.Context, platform, value string, maxAge int) {
	http.SetCookie(
		c.Writer,
		&http.Cookie{
			Name:     "boardly_social_" + platform,
			Value:    value,
			Path:     "/api/v1/social/oauth/" + platform + "/callback",
			MaxAge:   maxAge,
			HttpOnly: true,
			Secure:   h.AuthCookies.Secure,
			SameSite: http.SameSiteLaxMode,
		},
	)
}

func (h *Handler) disconnectSocial(c *gin.Context) {
	revision, err := strconv.ParseInt(c.Query("expected_revision"), 10, 64)
	if err != nil || revision < 1 {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, _ := auth.UserPrincipalFromContext(c.Request.Context())
	if err := h.SocialService.Disconnect(
		c.Request.Context(),
		principal,
		c.Param("connectionId"),
		revision,
	); err != nil {
		writeSocialError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

var socialErrorMappings = []errorMapping{
	{
		social.ErrApprovalRequired,
		http.StatusConflict,
		"Review the post with ask_human and confirm Publish now before publishing",
		"social_approval_required",
	},
	{
		social.ErrInvalid,
		http.StatusUnprocessableEntity,
		"invalid post, target or media; check platform limits",
		"social_invalid_post",
	},
	{
		social.ErrReconnect,
		http.StatusConflict,
		"reconnect this account to renew its publishing permissions",
		"social_reconnect_required",
	},
	runTokenInvalidMapping,
	{
		social.ErrUnavailable,
		http.StatusServiceUnavailable,
		"this social connection is not available yet",
		"social_unavailable",
	},
	{social.ErrNotFound, http.StatusNotFound, "social connection not found", "social_not_found"},
	{
		social.ErrInvalidFlow,
		http.StatusUnprocessableEntity,
		"authorization expired; start connecting again",
		"social_invalid_flow",
	},
	{
		social.ErrProvider,
		http.StatusBadGateway,
		"the social platform could not complete the request; try again",
		"social_provider_failed",
	},
	{
		social.ErrConflict,
		http.StatusConflict,
		"this connection changed; refresh before trying again",
		"social_conflict",
	},
}

func writeSocialError(c *gin.Context, err error) {
	writeMappedError(c, err, socialErrorMappings, internalErrorFallback)
}
