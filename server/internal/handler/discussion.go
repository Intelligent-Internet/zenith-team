package handler

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

const maxDiscussionRequestBytes = 128 << 10

type createHumanConversationRequest struct {
	AgentIDs    []string `json:"agentIds"    binding:"required_without_all=PeerUserID PeerUserIDs,omitempty,min=1,max=20,unique,dive,uuid"`
	PeerUserID  string   `json:"peerUserId"  binding:"required_without_all=PeerUserIDs AgentIDs,excluded_with=PeerUserIDs,omitempty,uuid"`
	PeerUserIDs []string `json:"peerUserIds" binding:"required_without_all=PeerUserID AgentIDs,excluded_with=PeerUserID,omitempty,min=1,max=49,unique,dive,uuid"`
}
type inviteHumanConversationRequest struct {
	MemberUserIDs []string `json:"memberUserIds" binding:"required_without=AgentIDs,omitempty,min=1,max=50,unique,dive,uuid"`
	AgentIDs      []string `json:"agentIds"      binding:"required_without=MemberUserIDs,omitempty,min=1,max=20,unique,dive,uuid"`
}

type createChannelRequest struct {
	RequestKey    string   `json:"requestKey"    binding:"required,uuid"`
	Name          string   `json:"name"          binding:"required"`
	Topic         string   `json:"topic"`
	MemberUserIDs []string `json:"memberUserIds" binding:"max=49,unique,dive,uuid"`
	AgentIDs      []string `json:"agentIds"      binding:"max=20,unique,dive,uuid"`
}
type updateChannelRequest struct {
	ExpectedRevision optional[int64]  `json:"expectedRevision" binding:"omitnil,required,revision"`
	Name             optional[string] `json:"name"             binding:"required_without=Topic"`
	Topic            optional[string] `json:"topic"`
}
type channelRevisionRequest struct {
	ExpectedRevision optional[int64] `json:"expectedRevision" binding:"omitnil,required,revision"`
}
type sendDiscussionRequest struct {
	RequestKey    string                       `json:"requestKey"    binding:"required,uuid"`
	Body          conversation.DiscussionBody  `json:"body"          binding:"required"`
	AttachmentIDs []string                     `json:"attachmentIds" binding:"max=20,unique,dive,uuid"`
	ArtifactIDs   []string                     `json:"artifactIds"   binding:"max=10,unique,dive,uuid"`
	ThreadRootID  *string                      `json:"threadRootId"  binding:"omitnil,uuid"`
	Whiteboard    *discussionWhiteboardRequest `json:"whiteboard"`
}

// discussionWhiteboardRequest makes a Whiteboard Artifact with the message.
type discussionWhiteboardRequest struct {
	Title string `json:"title" binding:"required,max=200"`
}
type queryDiscussionStatesRequest struct {
	ConversationIDs []string `json:"conversationIds" binding:"required,min=1,max=50,unique,dive,uuid"`
}
type discussionStatesResponse struct {
	Items []conversation.DiscussionState `json:"items"`
}

// Revision zero describes an owner without personal state. The domain checks
// this zero-inclusive bound beside the generation-aware state CAS.
type updateDiscussionStateRequest struct {
	NotificationLevel *string         `json:"notificationLevel" binding:"omitempty,oneof=all mentions muted"`
	ExpectedRevision  optional[int64] `json:"expectedRevision"  binding:"omitnil,required"`
	Pinned            optional[bool]  `json:"pinned"            binding:"required_without_all=Hidden NotificationLevel"`
	Hidden            optional[bool]  `json:"hidden"`
}

func (h *Handler) registerDiscussionRoutes(api *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.HumanConversationService == nil ||
		h.DiscussionService == nil {
		return
	}
	group := api.Group(
		"/companies/:companyId",
		middleware.PrivateNoStore(),
		middleware.RequestTimeout(5*time.Second),
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireHumanActor(),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	discuss := middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionConversationsDiscuss)
	manage := middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionChannelsManage)
	group.POST("/human-conversations", discuss, h.createHumanConversation)
	group.GET("/human-conversations", h.listHumanConversations)
	group.GET("/human-conversations/:conversationId", h.getHumanConversation)
	group.POST("/human-conversations/:conversationId/members", discuss, h.inviteHumanConversationMembers)
	group.POST("/channels", manage, h.createChannel)
	group.GET("/channels", h.listChannels)
	group.GET("/channels/:conversationId", h.getChannel)
	group.GET("/channels/:conversationId/agent-invocations", h.listChannelInvocations)
	group.GET("/channels/:conversationId/agents/:agentId/activity", h.getChannelAgentActivity)
	group.POST("/channels/:conversationId/agent-invocations/:invocationId/retry", discuss, h.retryChannelInvocation)
	group.PATCH("/channels/:conversationId", manage, h.updateChannel)
	group.PUT("/channels/:conversationId/members/:userId", manage, h.addChannelMember)
	// Self-removal uses leave authority; the service checks manage authority
	// for a different target after establishing current resource access.
	group.DELETE("/channels/:conversationId/members/:userId", h.removeChannelMember)
	group.PUT("/channels/:conversationId/agents/:agentId", manage, h.addChannelAgent)
	group.DELETE("/channels/:conversationId/agents/:agentId", manage, h.removeChannelAgent)
	group.POST("/channels/:conversationId/leave", h.leaveChannel)
	group.POST("/channels/:conversationId/archive", manage, h.archiveChannel)
	group.POST("/channels/:conversationId/restore", manage, h.restoreChannel)
	group.GET("/conversations/:conversationId/discussion-messages", h.listDiscussionMessages)
	group.POST("/conversations/:conversationId/discussion-messages", discuss, h.sendDiscussionMessage)
	group.GET("/conversations/:conversationId/threads/:rootId/messages", h.listDiscussionThread)
	group.GET(
		"/conversations/:conversationId/discussion-messages/:messageId/context",
		h.getDiscussionMessageContext,
	)
	group.PATCH("/conversations/:conversationId/discussion-messages/:messageId", h.editDiscussionMessage)
	group.DELETE("/conversations/:conversationId/discussion-messages/:messageId", h.deleteDiscussionMessage)
	group.GET("/conversations/:conversationId/discussion-pins", h.listDiscussionPins)
	group.PUT("/conversations/:conversationId/discussion-pins/:messageId", h.pinDiscussionMessage)
	group.DELETE("/conversations/:conversationId/discussion-pins/:messageId", h.unpinDiscussionMessage)
	group.GET("/conversations/:conversationId/discussion-messages/:messageId/reactions", h.listDiscussionReactions)
	group.PUT(
		"/conversations/:conversationId/discussion-messages/:messageId/reactions/:emoji",
		h.addDiscussionReaction,
	)
	group.DELETE(
		"/conversations/:conversationId/discussion-messages/:messageId/reactions/:emoji",
		h.removeDiscussionReaction,
	)
	group.PUT("/conversations/:conversationId/threads/:rootId/state", h.updateDiscussionThreadState)
	group.POST("/conversation-states/query", h.queryDiscussionStates)
	group.POST("/member-presence", h.discussionPresence)
	group.PUT("/conversations/:conversationId/discussion-read-state", h.updateDiscussionReadState)
	group.PATCH("/conversations/:conversationId/discussion-state", h.updateDiscussionState)
}

func discussionIdentity(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return p, uuid.Nil, false
	}
	var id uuid.UUID
	if raw := c.Param("conversationId"); raw != "" {
		var err error
		id, err = util.ParseGoUUID(raw)
		if err != nil {
			writeDiscussionError(c, conversation.ErrDiscussionNotFound)
			return p, id, false
		}
	}
	return p, id, true
}

func (h *Handler) createHumanConversation(c *gin.Context) {
	p, _, ok := discussionIdentity(c)
	if !ok {
		return
	}
	body, err := bindRequest[createHumanConversationRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	peers := body.PeerUserIDs
	if body.PeerUserID != "" {
		peers = []string{body.PeerUserID}
	}
	ids := make([]uuid.UUID, len(peers))
	for i, peer := range peers {
		ids[i] = uuid.MustParse(peer)
	}
	agentIDs := make([]uuid.UUID, len(body.AgentIDs))
	for i, agent := range body.AgentIDs {
		agentIDs[i] = uuid.MustParse(agent)
	}
	result, err := h.HumanConversationService.CreateHumanDirect(
		c.Request.Context(),
		p,
		conversation.CreateHumanDirectInput{PeerUserIDs: ids, AgentIDs: agentIDs},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, result)
}

func (h *Handler) listHumanConversations(c *gin.Context) {
	p, _, ok := discussionIdentity(c)
	if !ok {
		return
	}
	options, _, err := parseDiscussionQuery(c, "limit", "cursor", "include_hidden")
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	result, err := h.HumanConversationService.ListHumanDirects(c.Request.Context(), p, options)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getHumanConversation(c *gin.Context) {
	p, id, ok := discussionIdentity(c)
	if !ok {
		return
	}
	if _, _, err := parseDiscussionQuery(c); err != nil {
		writeDiscussionError(c, err)
		return
	}
	result, err := h.HumanConversationService.FetchHumanDirect(c.Request.Context(), p, id)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) createChannel(c *gin.Context) {
	p, _, ok := discussionIdentity(c)
	if !ok {
		return
	}
	body, err := bindRequest[createChannelRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.HumanConversationService.CreateChannel(
		c.Request.Context(),
		p,
		conversation.CreateChannelInput{
			RequestKey:    uuid.MustParse(body.RequestKey),
			Name:          body.Name,
			Topic:         body.Topic,
			MemberUserIDs: discussionUUIDs(body.MemberUserIDs),
			AgentIDs:      discussionUUIDs(body.AgentIDs),
		},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	c.JSON(status, result)
}

func (h *Handler) listChannels(c *gin.Context) {
	p, _, ok := discussionIdentity(c)
	if !ok {
		return
	}
	options, manage, err := parseDiscussionQuery(c, "limit", "cursor", "q", "archived", "view", "include_hidden")
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	if manage {
		result, err := h.HumanConversationService.ListChannelManagement(c.Request.Context(), p, options)
		if err != nil {
			writeDiscussionError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
		return
	}
	result, err := h.HumanConversationService.ListChannels(c.Request.Context(), p, options)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getChannel(c *gin.Context) {
	p, id, ok := discussionIdentity(c)
	if !ok {
		return
	}
	_, manage, err := parseDiscussionQuery(c, "view")
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	if manage {
		result, err := h.HumanConversationService.FetchChannelManagement(c.Request.Context(), p, id)
		if err != nil {
			writeDiscussionError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
		return
	}
	result, err := h.HumanConversationService.FetchChannel(c.Request.Context(), p, id)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) updateChannel(c *gin.Context) {
	p, id, ok := discussionIdentity(c)
	if !ok {
		return
	}
	body, err := bindRequest[updateChannelRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.HumanConversationService.UpdateChannel(
		c.Request.Context(),
		p,
		id,
		conversation.ChannelUpdateInput{
			ExpectedRevision: body.ExpectedRevision.value,
			Name:             body.Name.ptr(),
			Topic:            body.Topic.ptr(),
		},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) addChannelMember(c *gin.Context)    { h.setChannelMember(c, true) }
func (h *Handler) removeChannelMember(c *gin.Context) { h.setChannelMember(c, false) }
func (h *Handler) setChannelMember(c *gin.Context, active bool) {
	p, id, ok := discussionIdentity(c)
	if !ok {
		return
	}
	target, err := util.ParseGoUUID(c.Param("userId"))
	if err != nil {
		writeDiscussionError(c, conversation.ErrDiscussionNotFound)
		return
	}
	body, err := bindRequest[channelRevisionRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.HumanConversationService.SetChannelMember(
		c.Request.Context(),
		p,
		id,
		conversation.ChannelMemberInput{
			ExpectedRevision: body.ExpectedRevision.value,
			UserID:           target,
			Active:           active,
		},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) addChannelAgent(c *gin.Context)    { h.setChannelAgent(c, true) }
func (h *Handler) removeChannelAgent(c *gin.Context) { h.setChannelAgent(c, false) }
func (h *Handler) setChannelAgent(c *gin.Context, active bool) {
	p, id, ok := discussionIdentity(c)
	if !ok {
		return
	}
	target, err := util.ParseGoUUID(c.Param("agentId"))
	if err != nil {
		writeDiscussionError(c, conversation.ErrDiscussionNotFound)
		return
	}
	body, err := bindRequest[channelRevisionRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.HumanConversationService.SetChannelAgent(
		c.Request.Context(),
		p,
		id,
		conversation.ChannelAgentInput{
			ExpectedRevision: body.ExpectedRevision.value,
			AgentID:          target,
			Active:           active,
		},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) leaveChannel(c *gin.Context) {
	p, id, ok := discussionIdentity(c)
	if !ok {
		return
	}
	body, err := bindRequest[channelRevisionRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if err = h.HumanConversationService.LeaveChannel(
		c.Request.Context(),
		p,
		id,
		body.ExpectedRevision.value,
	); err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *Handler) archiveChannel(c *gin.Context) { h.setChannelArchived(c, true) }
func (h *Handler) restoreChannel(c *gin.Context) { h.setChannelArchived(c, false) }
func (h *Handler) setChannelArchived(c *gin.Context, archived bool) {
	p, id, ok := discussionIdentity(c)
	if !ok {
		return
	}
	body, err := bindRequest[channelRevisionRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.HumanConversationService.SetChannelArchived(
		c.Request.Context(),
		p,
		id,
		conversation.ChannelArchiveInput{ExpectedRevision: body.ExpectedRevision.value, Archived: archived},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) sendDiscussionMessage(c *gin.Context) {
	p, id, ok := discussionIdentity(c)
	if !ok {
		return
	}
	body, err := bindRequest[sendDiscussionRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	var rootID *uuid.UUID
	if body.ThreadRootID != nil {
		parsed := uuid.MustParse(*body.ThreadRootID)
		rootID = &parsed
	}
	attachmentIDs := make([]uuid.UUID, len(body.AttachmentIDs))
	for index, raw := range body.AttachmentIDs {
		attachmentIDs[index] = uuid.MustParse(raw)
	}
	var whiteboard *conversation.DiscussionWhiteboard
	if body.Whiteboard != nil {
		whiteboard = &conversation.DiscussionWhiteboard{Title: body.Whiteboard.Title}
	}
	result, err := h.DiscussionService.Send(
		c.Request.Context(),
		p,
		id,
		conversation.DiscussionSendInput{
			RequestKey:    uuid.MustParse(body.RequestKey),
			Body:          body.Body,
			AttachmentIDs: attachmentIDs,
			ArtifactIDs:   discussionUUIDs(body.ArtifactIDs),
			ThreadRootID:  rootID,
			Whiteboard:    whiteboard,
		},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	c.JSON(status, result)
}

func (h *Handler) listDiscussionMessages(c *gin.Context) {
	p, id, ok := discussionIdentity(c)
	if !ok {
		return
	}
	options, _, err := parseDiscussionQuery(c, "limit", "cursor")
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	result, err := h.DiscussionService.ListMessages(
		c.Request.Context(),
		p,
		id,
		conversation.DiscussionPageOptions{Limit: options.Limit, Cursor: options.Cursor},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) queryDiscussionStates(c *gin.Context) {
	p, _, ok := discussionIdentity(c)
	if !ok {
		return
	}
	body, err := bindRequest[queryDiscussionStatesRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.DiscussionService.QueryStates(c.Request.Context(), p, discussionUUIDs(body.ConversationIDs))
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, discussionStatesResponse{Items: result})
}

func (h *Handler) updateDiscussionState(c *gin.Context) {
	p, id, ok := discussionIdentity(c)
	if !ok {
		return
	}
	body, err := bindRequest[updateDiscussionStateRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.DiscussionService.UpdateState(
		c.Request.Context(),
		p,
		id,
		conversation.DiscussionStateInput{
			ExpectedRevision:  body.ExpectedRevision.value,
			Pinned:            body.Pinned.ptr(),
			Hidden:            body.Hidden.ptr(),
			NotificationLevel: body.NotificationLevel,
		},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func discussionUUIDs(values []string) []uuid.UUID {
	ids := make([]uuid.UUID, len(values))
	for i, value := range values {
		ids[i] = uuid.MustParse(value)
	}
	return ids
}

func parseDiscussionQuery(c *gin.Context, allowed ...string) (conversation.DiscussionListOptions, bool, error) {
	result := conversation.DiscussionListOptions{Limit: 50}
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return result, false, conversation.ErrDiscussionInvalid
	}
	for key, values := range query {
		if !slices.Contains(allowed, key) || len(values) != 1 || values[0] == "" {
			return result, false, conversation.ErrDiscussionInvalid
		}
	}
	if limit := query.Get("limit"); limit != "" {
		result.Limit, err = strconv.Atoi(limit)
		if err != nil || result.Limit < 1 || result.Limit > 100 || strconv.Itoa(result.Limit) != limit {
			return result, false, conversation.ErrDiscussionInvalid
		}
	}
	result.Cursor = query.Get("cursor")

	result.Query = query.Get("q")
	if len(result.Query) > 400 {
		return result, false, conversation.ErrDiscussionInvalid
	}
	for _, key := range []string{"include_hidden", "archived"} {
		value := query.Get(key)
		if value != "" && value != "true" && value != "false" {
			return result, false, conversation.ErrDiscussionInvalid
		}
	}
	result.IncludeHidden = query.Get("include_hidden") == "true"
	result.Archived = query.Get("archived") == "true"
	view := query.Get("view")
	if view != "" && view != "chat" && view != "manage" {
		return result, false, conversation.ErrDiscussionInvalid
	}
	return result, view == "manage", nil
}

var discussionErrorMappings = []errorMapping{
	{
		conversation.ErrMessageConnectorSelection,
		http.StatusConflict,
		"A selected plugin is unavailable for one or more tagged agents. Check your connections or change the selected agents/plugins.",
		"message_connector_unavailable",
	},
	{
		conversation.ErrMessageSkillSelection,
		http.StatusConflict,
		"A selected skill changed, is unavailable, or conflicts with another selected skill name. Select the skills again.",
		"message_skill_unavailable",
	},
	{
		conversation.ErrDiscussionNotFound,
		http.StatusNotFound,
		"Conversation or message was not found.",
		"discussion_not_found",
	},
	{
		conversation.ErrDiscussionForbidden,
		http.StatusForbidden,
		"This action is not allowed.",
		"discussion_operation_forbidden",
	},
	{
		conversation.ErrDiscussionInvalid,
		http.StatusUnprocessableEntity,
		"request validation failed",
		"validation_failed",
	},
	{
		conversation.ErrDiscussionAgentRequired,
		http.StatusUnprocessableEntity,
		"Add the Agent to this conversation before you tag it.",
		"discussion_agent_required",
	},
	{
		conversation.ErrDiscussionRevisionConflict,
		http.StatusConflict,
		"Conversation or message changed. Reload and try again.",
		"discussion_revision_conflict",
	},
	{
		conversation.ErrDiscussionRequestConflict,
		http.StatusConflict,
		"Request key was already used for different content.",
		"discussion_request_conflict",
	},
	{
		conversation.ErrDiscussionCursorExpired,
		http.StatusConflict,
		"Reload this conversation before continuing.",
		"discussion_cursor_expired",
	},
	{conversation.ErrDiscussionInvalidCursor, http.StatusUnprocessableEntity, "invalid cursor", "invalid_cursor"},
	{
		conversation.ErrDiscussionReadOnly,
		http.StatusConflict,
		"This conversation is read-only.",
		"discussion_read_only",
	},
	{
		conversation.ErrDiscussionLimit,
		http.StatusConflict,
		"The conversation limit for this action was reached.",
		"discussion_limit_reached",
	},
	// Tagging an Agent runs it on the tagger's own computer (ADR 0014).
	{
		execution.ErrRunAgentNotBound,
		http.StatusConflict,
		"set this Agent up on one of your computers first",
		"agent_binding_required",
	},
	{
		conversation.ErrDiscussionUnavailable,
		http.StatusServiceUnavailable,
		"Conversation is temporarily unavailable. Try again.",
		"discussion_unavailable",
	},
}

func writeDiscussionError(c *gin.Context, err error) {
	if db.IsTransient(err) {
		err = conversation.ErrDiscussionUnavailable
	}
	if errors.Is(err, conversation.ErrDiscussionUnavailable) {
		c.Header("Retry-After", "1")
	}
	writeMappedError(c, err, discussionErrorMappings, internalErrorFallback)
}

type discussionPresenceRequest struct {
	Active     bool     `json:"active"`
	ManualAway *bool    `json:"manualAway"`
	UserIDs    []string `json:"userIds"    binding:"max=50,unique,dive,uuid"`
}

type discussionPresenceResponse struct {
	Items []conversation.DiscussionMemberPresence `json:"items"`
}

func (h *Handler) discussionPresence(c *gin.Context) {
	p, _, ok := discussionIdentity(c)
	if !ok {
		return
	}
	body, err := bindRequest[discussionPresenceRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	ids := make([]uuid.UUID, 0, len(body.UserIDs))
	for _, id := range body.UserIDs {
		ids = append(ids, uuid.MustParse(id))
	}
	items, err := h.DiscussionService.Presence(
		c.Request.Context(),
		p,
		conversation.DiscussionPresenceInput{Active: body.Active, ManualAway: body.ManualAway, UserIDs: ids},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, discussionPresenceResponse{Items: items})
}

func (h *Handler) inviteHumanConversationMembers(c *gin.Context) {
	p, id, ok := discussionIdentity(c)
	if !ok {
		return
	}
	body, err := bindRequest[inviteHumanConversationRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.HumanConversationService.InviteHumanDirectMembers(
		c.Request.Context(),
		p,
		id,
		conversation.InviteHumanDirectInput{
			MemberUserIDs: discussionUUIDs(body.MemberUserIDs),
			AgentIDs:      discussionUUIDs(body.AgentIDs),
		},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
