package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/util"
)

func (h *Handler) listChannelInvocations(c *gin.Context) {
	p, id, ok := discussionIdentity(c)
	if !ok {
		return
	}
	result, err := h.ChannelInvocationService.List(c.Request.Context(), p, id)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) getChannelAgentActivity(c *gin.Context) {
	p, id, ok := discussionIdentity(c)
	if !ok {
		return
	}
	agentID, err := util.ParseGoUUID(c.Param("agentId"))
	if err != nil {
		writeDiscussionError(c, conversation.ErrDiscussionNotFound)
		return
	}
	result, err := h.ChannelInvocationService.Activity(c.Request.Context(), p, conversation.ChannelAgentTarget{ChannelID: id, AgentID: agentID})
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

type channelInvocationRetryRequest struct {
	RequestKey string `json:"requestKey" binding:"required,uuid"`
}

func (h *Handler) retryChannelInvocation(c *gin.Context) {
	p, id, ok := discussionIdentity(c)
	if !ok {
		return
	}
	invocationID, err := util.ParseGoUUID(c.Param("invocationId"))
	if err != nil {
		writeDiscussionError(c, conversation.ErrDiscussionNotFound)
		return
	}
	input, err := bindRequest[channelInvocationRetryRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	requestKey, err := util.ParseGoUUID(input.RequestKey)
	if err != nil {
		writeDiscussionError(c, conversation.ErrDiscussionInvalid)
		return
	}
	result, err := h.ChannelInvocationService.Retry(c.Request.Context(), p, conversation.ChannelInvocationRetry{ChannelID: id, InvocationID: invocationID, RequestKey: requestKey})
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
