package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type leaveHuddleAIRequest struct{}

func (h *Handler) leaveHuddleAI(c *gin.Context) {
	attemptID, token, ok := huddleAIAttempt(c)
	if !ok {
		return
	}
	if _, err := bindRequest[leaveHuddleAIRequest](c, 4096); err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.HuddleAIService.Leave(c.Request.Context(), attemptID, token)
	if err != nil {
		writeHuddleAIError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
