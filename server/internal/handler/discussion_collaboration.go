package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

type editDiscussionRequest struct {
	ExpectedRevision optional[int64]             `json:"expectedRevision" binding:"omitnil,required,revision"`
	Body             conversation.DiscussionBody `json:"body"             binding:"required"`
}
type updateDiscussionThreadRequest struct {
	ReadThroughSeq   *int64          `json:"readThroughSeq"   binding:"omitempty,min=0,max=9007199254740991"`
	ManualUnreadSeq  *int64          `json:"manualUnreadSeq"  binding:"omitempty,min=0,max=9007199254740991"`
	GrantGeneration  int64           `json:"grantGeneration"  binding:"min=0,max=9007199254740991"`
	ExpectedRevision optional[int64] `json:"expectedRevision" binding:"omitnil,required"`
	Follow           string          `json:"follow"           binding:"omitempty,oneof=following unfollowed"`
}

func discussionMessageIdentity(
	c *gin.Context,
	param string,
) (auth.UserPrincipal, conversation.DiscussionMessageTarget, bool) {
	p, id, ok := discussionIdentity(c)
	target := conversation.DiscussionMessageTarget{ConversationID: id}
	if !ok {
		return p, target, false
	}
	messageID, err := util.ParseGoUUID(c.Param(param))
	if err != nil {
		writeDiscussionError(c, conversation.ErrDiscussionNotFound)
		return p, target, false
	}
	target.MessageID = messageID
	return p, target, true
}

func (h *Handler) listDiscussionThread(c *gin.Context) {
	p, target, ok := discussionMessageIdentity(c, "rootId")
	if !ok {
		return
	}
	options, _, err := parseDiscussionQuery(c, "limit", "cursor")
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	result, err := h.DiscussionService.ListThread(
		c.Request.Context(),
		p,
		target,
		conversation.DiscussionPageOptions{Limit: options.Limit, Cursor: options.Cursor},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getDiscussionMessageContext(c *gin.Context) {
	p, target, ok := discussionMessageIdentity(c, "messageId")
	if !ok {
		return
	}
	if _, _, err := parseDiscussionQuery(c); err != nil {
		writeDiscussionError(c, err)
		return
	}
	result, err := h.DiscussionService.MessageContext(c.Request.Context(), p, target)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) editDiscussionMessage(c *gin.Context) {
	p, target, ok := discussionMessageIdentity(c, "messageId")
	if !ok {
		return
	}
	body, err := bindRequest[editDiscussionRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.DiscussionService.EditMessage(
		c.Request.Context(),
		p,
		target,
		conversation.DiscussionEditInput{ExpectedRevision: body.ExpectedRevision.value, Body: body.Body},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) deleteDiscussionMessage(c *gin.Context) {
	p, target, ok := discussionMessageIdentity(c, "messageId")
	if !ok {
		return
	}
	body, err := bindRequest[channelRevisionRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.DiscussionService.DeleteMessage(c.Request.Context(), p, target, body.ExpectedRevision.value)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) listDiscussionPins(c *gin.Context) {
	p, id, ok := discussionIdentity(c)
	if !ok {
		return
	}
	options, _, err := parseDiscussionQuery(c, "limit", "cursor")
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	result, err := h.DiscussionService.ListPins(
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
func (h *Handler) pinDiscussionMessage(c *gin.Context)   { h.setDiscussionMessagePin(c, true) }
func (h *Handler) unpinDiscussionMessage(c *gin.Context) { h.setDiscussionMessagePin(c, false) }
func (h *Handler) setDiscussionMessagePin(c *gin.Context, active bool) {
	p, target, ok := discussionMessageIdentity(c, "messageId")
	if !ok {
		return
	}
	result, err := h.DiscussionService.SetMessagePin(c.Request.Context(), p, target, active)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) listDiscussionReactions(c *gin.Context) {
	p, target, ok := discussionMessageIdentity(c, "messageId")
	if !ok {
		return
	}
	options, _, err := parseDiscussionQuery(c, "limit", "cursor")
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	result, err := h.DiscussionService.ListReactions(
		c.Request.Context(),
		p,
		target,
		conversation.DiscussionPageOptions{Limit: options.Limit, Cursor: options.Cursor},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) addDiscussionReaction(c *gin.Context)    { h.setDiscussionReaction(c, true) }
func (h *Handler) removeDiscussionReaction(c *gin.Context) { h.setDiscussionReaction(c, false) }
func (h *Handler) setDiscussionReaction(c *gin.Context, active bool) {
	p, target, ok := discussionMessageIdentity(c, "messageId")
	if !ok {
		return
	}
	result, err := h.DiscussionService.SetReaction(
		c.Request.Context(),
		p,
		target,
		conversation.DiscussionReactionInput{Emoji: c.Param("emoji"), Active: active},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) updateDiscussionThreadState(c *gin.Context) {
	p, target, ok := discussionMessageIdentity(c, "rootId")
	if !ok {
		return
	}
	body, err := bindRequest[updateDiscussionThreadRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.DiscussionService.UpdateThreadState(
		c.Request.Context(),
		p,
		target,
		conversation.DiscussionThreadStateInput{
			ExpectedRevision: body.ExpectedRevision.value,
			Follow:           body.Follow,
			ReadThroughSeq:   body.ReadThroughSeq,
			ManualUnreadSeq:  body.ManualUnreadSeq,
			GrantGeneration:  body.GrantGeneration,
		},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

type discussionReadRequest struct {
	ReadThroughSeq   *int64 `json:"readThroughSeq"   binding:"omitempty,min=0,max=9007199254740991"`
	ManualUnreadSeq  *int64 `json:"manualUnreadSeq"  binding:"omitempty,min=0,max=9007199254740991"`
	ExpectedRevision *int64 `json:"expectedRevision" binding:"omitempty,min=0,max=9007199254740991"`
	GrantGeneration  int64  `json:"grantGeneration"  binding:"required,revision"`
}

func (h *Handler) updateDiscussionReadState(c *gin.Context) {
	p, id, ok := discussionIdentity(c)
	if !ok {
		return
	}
	body, err := bindRequest[discussionReadRequest](c, maxDiscussionRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.DiscussionService.UpdateReadState(
		c.Request.Context(),
		p,
		id,
		conversation.DiscussionReadInput{
			ReadThroughSeq:   body.ReadThroughSeq,
			ManualUnreadSeq:  body.ManualUnreadSeq,
			ExpectedRevision: body.ExpectedRevision,
			GrantGeneration:  body.GrantGeneration,
		},
	)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
