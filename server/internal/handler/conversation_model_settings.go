package handler

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/util"
)

type updateConversationModelSettingsRequest struct {
	AgentID          string          `json:"agentId"          binding:"required,uuid"`
	ExpectedRevision optional[int64] `json:"expectedRevision" binding:"omitnil,required,revision"`
	Model            json.RawMessage `json:"model"            binding:"required"`
	ThinkingLevel    json.RawMessage `json:"thinkingLevel"    binding:"required"`
}

func (h *Handler) getConversationModelSettings(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeConversationError(c, conversation.ErrUnauthenticated)
		return
	}
	id, err := util.ParseGoUUID(c.Param("conversationId"))
	if err != nil {
		writeConversationError(c, conversation.ErrNotFound)
		return
	}
	result, err := h.ConversationService.FetchModelSettings(c.Request.Context(), p, id)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) updateConversationModelSettings(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeConversationError(c, conversation.ErrUnauthenticated)
		return
	}
	id, err := util.ParseGoUUID(c.Param("conversationId"))
	if err != nil {
		writeConversationError(c, conversation.ErrNotFound)
		return
	}
	request, err := bindRequest[updateConversationModelSettingsRequest](c, maxConversationBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	// Both nullable values are required: replacement is atomic, never a mixed pair.
	model, err := parseRequiredNullableAgentString(request.Model)
	if err != nil {
		writeConversationError(c, conversation.ErrInvalidInput)
		return
	}
	thinking, err := parseRequiredNullableAgentString(request.ThinkingLevel)
	if err != nil {
		writeConversationError(c, conversation.ErrInvalidInput)
		return
	}
	agentID, err := util.ParseGoUUID(request.AgentID)
	if err != nil {
		writeConversationError(c, conversation.ErrInvalidInput)
		return
	}
	result, err := h.ConversationService.UpdateModelSettings(
		c.Request.Context(),
		p,
		id,
		conversation.ModelSettingsInput{
			AgentID:          agentID,
			ExpectedRevision: *request.ExpectedRevision.ptr(),
			Model:            model,
			ThinkingLevel:    thinking,
		},
	)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
