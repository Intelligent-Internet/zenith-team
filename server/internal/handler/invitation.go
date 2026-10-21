package handler

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

type createInvitationsRequest struct {
	Emails []string `json:"emails" binding:"required,min=1,max=20,dive,required,max=254"`
	Role   string   `json:"role"   binding:"omitempty,oneof=admin member"`
}
type createShareLinkRequest struct {
	Role string `json:"role" binding:"omitempty,oneof=admin member"`
}
type resetShareLinkRequest struct {
	ExpectedRevision int64  `json:"expectedRevision" binding:"revision"`
	Role             string `json:"role"             binding:"omitempty,oneof=admin member"`
}
type invitationMutationRequest struct {
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}
type invitationCollectionResponse struct {
	Items   []company.InvitationResponse `json:"items"`
	Enabled bool                         `json:"enabled"`
}
type invitationResultsResponse struct {
	Items []company.InvitationResult `json:"items"`
}

func (h *Handler) registerInvitationRoutes(api *gin.RouterGroup) {
	if h.InvitationService == nil || h.AuthService == nil || h.CompanyService == nil {
		return
	}
	public := api.Group("/invitations", middleware.NoStore(), invitationRequestLimit(), func(c *gin.Context) {
		if c.Request.URL.RawQuery != "" {
			writeBoundaryError(c, boundaryErrorValidationFailed)
			c.Abort()
			return
		}
		c.Header("Referrer-Policy", "no-referrer")
	})
	public.GET("/:token", middleware.OptionalUserAuth(h.AuthService), h.previewInvitation)
	public.POST("/:token/accept", middleware.RequireUserAuth(h.AuthService), h.acceptInvitation)
	public.POST("/:token/request-renewal", h.requestInvitationRenewal)
	// members.invite reads and writes the invitation surface; the writes are
	// a person's decision, so a Run acting through its proof is refused.
	inviters := api.Group(
		"/companies/:companyId/invitations",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionMembersInvite),
	)
	inviters.GET("", h.listInvitations)
	inviters.POST("", middleware.RequireHumanActor(), h.createInvitations)
	inviters.POST("/:invitationId/resend", middleware.RequireHumanActor(), h.resendInvitation)
	inviters.DELETE("/:invitationId", middleware.RequireHumanActor(), h.cancelInvitation)
	links := api.Group(
		"/companies/:companyId/share-link",
		middleware.NoStore(),
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionMembersInvite),
	)
	links.GET("", h.getShareLink)
	links.POST("", middleware.RequireHumanActor(), h.createShareLink)
	links.POST("/reset", middleware.RequireHumanActor(), h.resetShareLink)
	links.DELETE("", middleware.RequireHumanActor(), h.disableShareLink)
}

func (h *Handler) previewInvitation(c *gin.Context) {
	var result company.InvitationPreview
	var err error
	if p, ok := auth.UserPrincipalFromContext(c.Request.Context()); ok {
		result, err = h.InvitationService.Inspect(c.Request.Context(), p, c.Param("token"))
	} else {
		result, err = h.InvitationService.Preview(c.Request.Context(), c.Param("token"))
	}
	if err != nil {
		writeInvitationError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) acceptInvitation(c *gin.Context) {
	if _, err := bindRequest[struct{}](c, 1024); err != nil {
		writeRequestError(c, err)
		return
	}
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.InvitationService.Accept(c.Request.Context(), p, c.Param("token"))
	if err != nil {
		writeInvitationError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) requestInvitationRenewal(c *gin.Context) {
	if _, err := bindRequest[struct{}](c, 1024); err != nil {
		writeRequestError(c, err)
		return
	}
	if err := h.InvitationService.RequestRenewal(c.Request.Context(), c.Param("token")); err != nil {
		writeInvitationError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) listInvitations(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	items, err := h.InvitationService.List(c.Request.Context(), p)
	if err != nil {
		writeInvitationError(c, err)
		return
	}
	c.JSON(http.StatusOK, invitationCollectionResponse{Items: items, Enabled: h.InvitationEmailEnabled})
}

func (h *Handler) createInvitations(c *gin.Context) {
	request, err := bindRequest[createInvitationsRequest](c, 8192)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	role := auth.CompanyRole(request.Role)
	if role == "" {
		role = auth.CompanyRoleMember
	}
	items, err := h.InvitationService.Create(
		c.Request.Context(),
		p,
		company.InvitationCreateInput{Emails: request.Emails, Role: role},
	)
	if err != nil {
		writeInvitationError(c, err)
		return
	}
	c.JSON(http.StatusOK, invitationResultsResponse{Items: items})
}
func (h *Handler) resendInvitation(c *gin.Context) { h.mutateInvitation(c, true) }
func (h *Handler) cancelInvitation(c *gin.Context) { h.mutateInvitation(c, false) }
func (h *Handler) mutateInvitation(c *gin.Context, resend bool) {
	id, err := util.ParseGoUUID(c.Param("invitationId"))
	if err != nil {
		writeInvitationError(c, company.ErrInvitationNotFound)
		return
	}
	request, err := bindRequest[invitationMutationRequest](c, 1024)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	input := company.InvitationMutation{ID: id, ExpectedRevision: request.ExpectedRevision}
	var result company.InvitationResponse
	if resend {
		result, err = h.InvitationService.Resend(c.Request.Context(), p, input)
	} else {
		result, err = h.InvitationService.Cancel(c.Request.Context(), p, input)
	}
	if err != nil {
		writeInvitationError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

var invitationErrorMappings = append([]errorMapping{
	{
		company.ErrShareLinkMembershipRemoved,
		403,
		"Ask an owner or admin for a new email invitation to rejoin",
		"team_share_link_membership_removed",
	},
	{
		company.ErrShareLinkVerificationRequired,
		403,
		"Verify your email before joining",
		"team_share_link_verification_required",
	},
	{company.ErrShareLinkUnavailable, 503, "Invite links are not configured", "team_share_link_unavailable"},
	{company.ErrInvitationNotFound, 404, "Invitation not found", "team_invitation_not_found"},
	{
		company.ErrInvitationAccountMismatch,
		403,
		"Sign in with the verified invited email",
		"team_invitation_account_mismatch",
	},
	{company.ErrInvitationInactive, 410, "This invitation is no longer active", "team_invitation_inactive"},
	{
		company.ErrInvitationConflict,
		409,
		"Invitation changed; refresh and try again",
		"team_invitation_revision_conflict",
	},
	{company.ErrInvitationRateLimited, 429, "Please wait before trying again", "team_invitation_rate_limited"},
	{company.ErrInvitationUnavailable, 503, "Invitation email is not configured", "team_invitation_unavailable"},
}, companyErrorMappings...)

func writeInvitationError(c *gin.Context, err error) {
	if errors.Is(err, company.ErrInvitationRateLimited) {
		c.Header("Retry-After", "60")
	}
	writeMappedError(c, err, invitationErrorMappings, internalErrorFallback)
}

// The bounded per-instance IP budget also covers unknown token guesses. The
// database separately fences company sends, resends and renewal requests.
func invitationRequestLimit() gin.HandlerFunc {
	type window struct {
		expires time.Time
		count   int
	}
	var mu sync.Mutex
	windows := map[string]window{}
	return func(c *gin.Context) {
		now := time.Now()
		key := c.ClientIP()
		mu.Lock()
		if len(windows) >= 10000 {
			for address, w := range windows {
				if !w.expires.After(now) {
					delete(windows, address)
				}
			}
		}
		w, exists := windows[key]
		allowed := exists || len(windows) < 10000
		if !w.expires.After(now) {
			w = window{expires: now.Add(time.Minute)}
		}
		if w.count >= 120 {
			allowed = false
		}
		if allowed {
			w.count++
			windows[key] = w
		}
		mu.Unlock()
		if !allowed {
			writeInvitationError(c, company.ErrInvitationRateLimited)
			c.Abort()
		}
	}
}

func (h *Handler) getShareLink(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.InvitationService.GetShareLink(c.Request.Context(), p)
	if err != nil {
		writeInvitationError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) createShareLink(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	request, err := bindRequest[createShareLinkRequest](c, 1024)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.InvitationService.CreateShareLink(c.Request.Context(), p, auth.CompanyRole(request.Role))
	if err != nil {
		writeInvitationError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) resetShareLink(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	request, err := bindRequest[resetShareLinkRequest](c, 1024)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.InvitationService.ResetShareLink(
		c.Request.Context(),
		p,
		request.ExpectedRevision,
		auth.CompanyRole(request.Role),
	)
	if err != nil {
		writeInvitationError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) disableShareLink(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	request, err := bindRequest[invitationMutationRequest](c, 1024)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.InvitationService.DisableShareLink(c.Request.Context(), p, request.ExpectedRevision)
	if err != nil {
		writeInvitationError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
