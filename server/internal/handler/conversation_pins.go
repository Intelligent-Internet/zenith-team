package handler

import (
	"net/http"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"github.com/gin-gonic/gin"
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
