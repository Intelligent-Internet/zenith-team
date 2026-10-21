package handler

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/infra/livekit"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
)

type startHuddleRequest struct {
	RequestKey string `json:"requestKey" binding:"required,uuid"`
}

type joinHuddleRequest struct {
	RequestKey string `json:"requestKey" binding:"required,uuid"`
}
type inviteHuddleRequest struct {
	InviteeUserID string `json:"inviteeUserId" binding:"required,uuid"`
	RequestKey    string `json:"requestKey"    binding:"required,uuid"`
}
type respondHuddleInvitationRequest struct {
	Accept           bool  `json:"accept"`
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}

func (h *Handler) registerHuddleRoutes(api *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.HuddleService == nil {
		return
	}
	if h.LiveKitAPIKey != "" && h.LiveKitAPISecret != "" {
		api.POST("/internal/livekit/webhook", h.liveKitWebhook)
	}
	group := api.Group(
		"/companies/:companyId",
		middleware.PrivateNoStore(),
		middleware.RequestTimeout(5*time.Second),
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireHumanActor(),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	group.POST(
		"/conversations/:conversationId/huddles",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionConversationsDiscuss),
		h.startHuddle,
	)
	group.GET(
		"/huddles/:huddleId/connections",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionConversationsDiscuss),
		h.listHuddleConnections,
	)
	group.POST(
		"/huddles/:huddleId/connections",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionConversationsDiscuss),
		h.joinHuddle,
	)
	group.DELETE(
		"/huddles/:huddleId/connections/:connectionId",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionConversationsDiscuss),
		h.leaveHuddle,
	)
	group.GET(
		"/huddles/capabilities",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionConversationsDiscuss),
		h.getHuddleCapabilities,
	)
	group.GET(
		"/huddles/invitations",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionConversationsDiscuss),
		h.listHuddleInvitations,
	)
	group.GET(
		"/huddles/invitations/history",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionConversationsDiscuss),
		h.listHuddleInvitationHistory,
	)
	group.POST(
		"/huddles/:huddleId/invitations",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionConversationsDiscuss),
		h.inviteHuddle,
	)
	group.POST(
		"/huddles/:huddleId/invitations/:invitationId/respond",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionConversationsDiscuss),
		h.respondHuddleInvitation,
	)
}

func (h *Handler) listHuddleConnections(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	huddleID, err := uuid.Parse(c.Param("huddleId"))
	if err != nil {
		writeHuddleError(c, conversation.ErrHuddleInvalid)
		return
	}
	result, err := h.HuddleService.ListConnections(c.Request.Context(), p, huddleID)
	if err != nil {
		writeHuddleError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

type huddleCapabilitiesResponse struct {
	Calls       bool `json:"calls"`
	VoiceAgents bool `json:"voiceAgents"`
}

// getHuddleCapabilities tells clients which optional Huddle features this
// deployment runs, so they can hide what is off.
func (h *Handler) getHuddleCapabilities(c *gin.Context) {
	c.JSON(http.StatusOK, huddleCapabilitiesResponse{
		Calls:       h.HuddleCallsEnabled,
		VoiceAgents: h.HuddleCallsEnabled && h.HuddleVoiceAgentsEnabled,
	})
}

func (h *Handler) listHuddleInvitations(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.HuddleService.ListPendingInvitations(c.Request.Context(), p)
	if err != nil {
		writeHuddleError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) listHuddleInvitationHistory(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.HuddleService.ListInvitationHistory(c.Request.Context(), p)
	if err != nil {
		writeHuddleError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) inviteHuddle(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	huddleID, err := uuid.Parse(c.Param("huddleId"))
	if err != nil {
		writeHuddleError(c, conversation.ErrHuddleInvalid)
		return
	}
	body, err := bindRequest[inviteHuddleRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	invitee, _ := uuid.Parse(body.InviteeUserID)
	key, _ := uuid.Parse(body.RequestKey)
	result, err := h.HuddleService.Invite(
		c.Request.Context(),
		p,
		huddleID,
		conversation.InviteHuddleRequest{InviteeUserID: invitee, RequestKey: key},
	)
	if err != nil {
		writeHuddleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, result)
}

func (h *Handler) respondHuddleInvitation(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	id, err := uuid.Parse(c.Param("invitationId"))
	if err != nil {
		writeHuddleError(c, conversation.ErrHuddleInvalid)
		return
	}
	body, err := bindRequest[respondHuddleInvitationRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.HuddleService.RespondInvitation(
		c.Request.Context(),
		p,
		id,
		conversation.RespondHuddleInvitationRequest{
			Accept:           body.Accept,
			ExpectedRevision: body.ExpectedRevision,
		},
	)
	if err != nil {
		writeHuddleError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) liveKitWebhook(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	event, err := livekit.VerifyWebhook(body, c.GetHeader("Authorization"), h.LiveKitAPIKey, h.LiveKitAPISecret)
	if err != nil {
		c.Status(http.StatusUnauthorized)
		return
	}
	if event.Participant != nil &&
		(strings.HasPrefix(event.Participant.Identity, "helia:") || strings.HasPrefix(event.Participant.Identity, "notes:")) {
		c.Status(http.StatusNoContent)
		return
	}
	if event.Event == "participant_joined" {
		if event.Room == nil || event.Participant == nil {
			c.Status(http.StatusBadRequest)
			return
		}
		if _, err := h.HuddleService.ConfirmConnectedByIdentityAnyCompany(
			c.Request.Context(),
			event.Room.Name,
			event.Participant.Identity,
		); err != nil {
			writeHuddleError(c, err)
			return
		}
	}
	if event.Event == "room_finished" {
		if event.Room == nil {
			c.Status(http.StatusBadRequest)
			return
		}
		if err := h.HuddleService.EndFromMediaAnyCompany(c.Request.Context(), event.Room.Name); err != nil {
			writeHuddleError(c, err)
			return
		}
	}
	if event.Event == "participant_left" {
		if event.Room == nil || event.Participant == nil {
			c.Status(http.StatusBadRequest)
			return
		}
		if err := h.HuddleService.EndParticipantFromMediaAnyCompany(
			c.Request.Context(),
			event.Room.Name,
			event.Participant.Identity,
		); err != nil {
			writeHuddleError(c, err)
			return
		}
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) joinHuddle(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	if !h.HuddleCallsEnabled {
		writeHuddleError(c, conversation.ErrHuddleNotConfigured)
		return
	}
	huddleID, err := uuid.Parse(c.Param("huddleId"))
	if err != nil {
		writeHuddleError(c, conversation.ErrHuddleInvalid)
		return
	}
	body, err := bindRequest[joinHuddleRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.HuddleService.Join(
		c.Request.Context(),
		p,
		huddleID,
		conversation.JoinHuddleRequest{RequestKey: uuid.MustParse(body.RequestKey)},
	)
	if err != nil {
		writeHuddleError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) leaveHuddle(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	huddleID, err := uuid.Parse(c.Param("huddleId"))
	if err != nil {
		writeHuddleError(c, conversation.ErrHuddleInvalid)
		return
	}
	connectionID, err := uuid.Parse(c.Param("connectionId"))
	if err != nil {
		writeHuddleError(c, conversation.ErrHuddleInvalid)
		return
	}
	result, err := h.HuddleService.Leave(c.Request.Context(), p, huddleID, connectionID)
	if err != nil {
		writeHuddleError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) startHuddle(c *gin.Context) {
	p, sourceID, ok := discussionIdentity(c)
	if !ok {
		return
	}
	// Without LiveKit a Huddle would be created with no way to connect.
	if !h.HuddleCallsEnabled {
		writeHuddleError(c, conversation.ErrHuddleNotConfigured)
		return
	}
	body, err := bindRequest[startHuddleRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	requestKey := uuid.MustParse(body.RequestKey)
	result, err := h.HuddleService.Start(
		c.Request.Context(),
		p,
		sourceID,
		conversation.StartHuddleRequest{RequestKey: requestKey},
	)
	if err != nil {
		writeHuddleError(c, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, result.Huddle)
}

var huddleErrorMappings = []errorMapping{
	{conversation.ErrHuddleNotFound, http.StatusNotFound, "Huddle not found", "huddle_not_found"},
	{
		conversation.ErrHuddleForbidden,
		http.StatusForbidden,
		"This action is not allowed.",
		"huddle_operation_forbidden",
	},
	{
		conversation.ErrHuddleInvalid,
		http.StatusUnprocessableEntity,
		"request validation failed",
		"validation_failed",
	},
	{
		conversation.ErrHuddleRequestConflict,
		http.StatusConflict,
		"Request key was already used for a different Huddle.",
		"huddle_request_conflict",
	},
	{
		conversation.ErrHuddleUnavailable,
		http.StatusServiceUnavailable,
		"Huddle is temporarily unavailable. Try again.",
		"huddle_unavailable",
	},
	{
		conversation.ErrHuddleNotConfigured,
		http.StatusServiceUnavailable,
		"Calls are not set up on this server.",
		"huddle_not_configured",
	},
	{
		conversation.ErrHuddleAlreadyConnected,
		http.StatusConflict,
		"You are already in this Huddle in another window or on another device. Leave there, then join here.",
		"huddle_already_connected",
	},
}

func writeHuddleError(c *gin.Context, err error) {
	writeMappedError(c, err, huddleErrorMappings, internalErrorFallback)
}
