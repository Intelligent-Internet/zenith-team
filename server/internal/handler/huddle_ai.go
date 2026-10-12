package handler

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type setHuddleAIRequest struct {
	AllowCompanyContext *bool  `json:"allowCompanyContext"`
	AgentID             string `json:"agentId" binding:"omitempty,uuid"`
	Enabled             *bool  `json:"enabled" binding:"required"`
	ExpectedRevision    int64  `json:"expectedRevision" binding:"revision"`
	RequestKey          string `json:"requestKey" binding:"required,uuid"`
}
type huddleAIHeartbeatRequest struct {
	PollWork        bool   `json:"pollWork"`
	ClaimWorkNotice string `json:"claimWorkNotice" binding:"max=160"`
	CompletedSeq    int64  `json:"completedSeq" binding:"gte=0,lte=9007199254740991"`
	ChatCursor      int64  `json:"chatCursor" binding:"gte=0,lte=9007199254740991"`
	State           string `json:"state" binding:"omitempty,oneof=listening speaking"`
}
type huddleAIUsageRequest struct {
	ModelSessionID    string  `json:"modelSessionId" binding:"required,max=200"`
	ModelSeconds      float64 `json:"modelSeconds" binding:"gte=0,lte=7200"`
	InputTokens       int64   `json:"inputTokens" binding:"gte=0,lte=100000000"`
	CachedInputTokens int64   `json:"cachedInputTokens" binding:"gte=0,lte=100000000"`
	OutputTokens      int64   `json:"outputTokens" binding:"gte=0,lte=100000000"`
	Final             bool    `json:"final"`
	Closed            bool    `json:"closed"`
	StopReason        string  `json:"stopReason" binding:"omitempty,oneof=provider_error provider_closed control_unavailable authority_revoked lease_expired reasoning_budget microphone_error worker_shutdown internal_error agent_left"`
}

func (h *Handler) registerHuddleAIRoutes(api *gin.RouterGroup) {
	if h.HuddleAIService == nil {
		return
	}
	internal := api.Group("/internal/huddle-ai/:attemptId", middleware.PrivateNoStore(), middleware.RequestTimeout(5*time.Second))
	internal.POST("/claim", h.claimHuddleAI)
	internal.POST("/heartbeat", h.heartbeatHuddleAI)
	internal.GET("/chat", h.huddleAIChat)
	internal.POST("/usage", h.huddleAIUsage)
	internal.POST("/tools", h.readHuddleContext)
	internal.POST("/proposals", h.proposeHuddleWork)
	internal.POST("/leave", h.leaveHuddleAI)
	internal.POST("/transcript", h.appendHuddleTranscript)
	if h.AuthService == nil || h.CompanyService == nil {
		return
	}
	group := api.Group("/companies/:companyId/huddles/:huddleId/ai", middleware.PrivateNoStore(), middleware.RequestTimeout(5*time.Second), middleware.RequireUserAuth(h.AuthService), middleware.RequireHumanActor(), middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionConversationsDiscuss))
	group.GET("", h.getHuddleAI)
	group.PUT("", h.setHuddleAI)
	group.PUT("/proposals/:proposalId", h.updateHuddleProposal)
	if h.TaskService != nil {
		group.POST("/proposals/:proposalId/submit", h.submitHuddleProposal)
	}
}
func (h *Handler) getHuddleAI(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	id, err := uuid.Parse(c.Param("huddleId"))
	if err != nil {
		writeHuddleError(c, conversation.ErrHuddleInvalid)
		return
	}
	result, err := h.HuddleAIService.Get(c.Request.Context(), p, id)
	if err != nil {
		writeHuddleAIError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) setHuddleAI(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	id, err := uuid.Parse(c.Param("huddleId"))
	if err != nil {
		writeHuddleError(c, conversation.ErrHuddleInvalid)
		return
	}
	body, err := bindRequest[setHuddleAIRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	var agentID uuid.UUID
	if body.AgentID != "" {
		agentID = uuid.MustParse(body.AgentID)
	}
	allowContext := *body.Enabled
	if body.AllowCompanyContext != nil {
		allowContext = *body.AllowCompanyContext
	}
	result, err := h.HuddleAIService.SetEnabled(c.Request.Context(), p, id, conversation.SetHuddleAIInput{
		AllowCompanyContext: allowContext,
		AgentID:             agentID, Enabled: *body.Enabled,
		ExpectedRevision: body.ExpectedRevision, RequestKey: uuid.MustParse(body.RequestKey),
	})
	if err != nil {
		writeHuddleAIError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func huddleAIAttempt(c *gin.Context) (uuid.UUID, string, bool) {
	id, err := uuid.Parse(c.Param("attemptId"))
	if err != nil {
		writeHuddleError(c, conversation.ErrHuddleInvalid)
		return uuid.Nil, "", false
	}
	token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if token == "" || token == c.GetHeader("Authorization") {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return uuid.Nil, "", false
	}
	return id, token, true
}
func (h *Handler) claimHuddleAI(c *gin.Context) {
	id, token, ok := huddleAIAttempt(c)
	if !ok {
		return
	}
	if h.HuddleAIWorkloadSecret == "" || subtle.ConstantTimeCompare([]byte(token), []byte(h.HuddleAIWorkloadSecret)) != 1 {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.HuddleAIService.Claim(c.Request.Context(), id)
	if err != nil {
		writeHuddleAIError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) heartbeatHuddleAI(c *gin.Context) {
	id, token, ok := huddleAIAttempt(c)
	if !ok {
		return
	}
	body, err := bindRequest[huddleAIHeartbeatRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.HuddleAIService.Heartbeat(c.Request.Context(), id, token, conversation.HuddleAIHeartbeat{
		ChatCursor: body.ChatCursor, State: body.State, CompletedSeq: body.CompletedSeq,
		PollWork: body.PollWork, ClaimWorkNotice: body.ClaimWorkNotice,
	})
	if err != nil {
		writeHuddleAIError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) huddleAIChat(c *gin.Context) {
	id, token, ok := huddleAIAttempt(c)
	if !ok {
		return
	}
	result, err := h.HuddleAIService.Chat(c.Request.Context(), id, token)
	if err != nil {
		writeHuddleAIError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) huddleAIUsage(c *gin.Context) {
	id, token, ok := huddleAIAttempt(c)
	if !ok {
		return
	}
	body, err := bindRequest[huddleAIUsageRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	err = h.HuddleAIService.Finish(c.Request.Context(), id, token, conversation.HuddleAIUsage{StopReason: body.StopReason, ModelSessionID: body.ModelSessionID, ModelSeconds: body.ModelSeconds, InputTokens: body.InputTokens, CachedInputTokens: body.CachedInputTokens, OutputTokens: body.OutputTokens, Final: body.Final, Closed: body.Closed})
	if err != nil {
		writeHuddleAIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

var huddleAIErrorMappings = []errorMapping{
	{conversation.ErrHuddleProposalConflict, http.StatusConflict, "Work proposal changed or is no longer editable. Reload and retry.", "huddle_proposal_conflict"},
	{conversation.ErrHuddleProposalLimit, http.StatusTooManyRequests, "This call has reached its work proposal limit.", "huddle_proposal_limit"},
	{conversation.ErrHuddleToolLimit, http.StatusTooManyRequests, "Voice lookup limit reached. Try again later.", "huddle_tool_limit"},
	{conversation.ErrHuddleAIConflict, http.StatusConflict, "Huddle AI settings changed. Reload and retry.", "huddle_ai_conflict"},
}

func writeHuddleAIError(c *gin.Context, err error) {
	writeMappedError(c, err, append(append(append([]errorMapping{}, huddleErrorMappings...), huddleBudgetErrorMappings...), huddleAIErrorMappings...), internalErrorFallback)
}
