package handler

import (
	"net/http"

	"boardly.dev/boardly/server/internal/conversation"
	"github.com/gin-gonic/gin"
)

type huddleTranscriptEntryRequest struct {
	Speaker string `json:"speaker" binding:"required,oneof=participants agent"`
	Text    string `json:"text" binding:"required,max=4000"`
}

type huddleTranscriptRequest struct {
	Sequence  int64                          `json:"sequence" binding:"gte=1,lte=9007199254740991"`
	Entries   []huddleTranscriptEntryRequest `json:"entries" binding:"required,max=32,dive"`
	Truncated bool                           `json:"truncated"`
}

func (h *Handler) appendHuddleTranscript(c *gin.Context) {
	id, token, ok := huddleAIAttempt(c)
	if !ok {
		return
	}
	body, err := bindRequest[huddleTranscriptRequest](c, 24*1024)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	input := conversation.HuddleTranscriptBatch{Sequence: body.Sequence, Truncated: body.Truncated, Entries: []conversation.HuddleTranscriptEntry{}}
	for _, entry := range body.Entries {
		input.Entries = append(input.Entries, conversation.HuddleTranscriptEntry{Speaker: entry.Speaker, Text: entry.Text})
	}
	result, err := h.HuddleAIService.AppendTranscript(c.Request.Context(), id, token, input)
	if err != nil {
		writeHuddleAIError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
