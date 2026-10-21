package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
)

type conversationStatesResponse struct {
	Items []conversation.UserState `json:"items"`
}

func (h *Handler) listConversationPins(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeConversationError(c, conversation.ErrInvalidInput)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeConversationError(c, conversation.ErrUnauthenticated)
		return
	}
	pins, err := h.ConversationService.ListPins(c.Request.Context(), principal)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	c.JSON(http.StatusOK, conversationStatesResponse{Items: pins})
}

func (h *Handler) listConversationStates(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeConversationError(c, conversation.ErrInvalidInput)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeConversationError(c, conversation.ErrUnauthenticated)
		return
	}
	states, err := h.ConversationService.ListStates(c.Request.Context(), principal)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	c.JSON(http.StatusOK, conversationStatesResponse{Items: states})
}
