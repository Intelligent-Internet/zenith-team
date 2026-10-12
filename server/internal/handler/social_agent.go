package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/social"
	"boardly.dev/boardly/server/internal/util"
)

func (h *Handler) socialRunContext(c *gin.Context) (auth.UserPrincipal, social.RunContext, bool) {
	p, _ := auth.UserPrincipalFromContext(c.Request.Context())
	conversation, err := util.ParseGoUUID(c.Param("conversationId"))
	if err != nil || h.RunProofResolver == nil || c.GetHeader(runTokenHeader) == "" {
		writeSocialError(c, execution.ErrRunProofInvalid)
		return p, social.RunContext{}, false
	}
	origin, err := h.resolveRunOrigin(c, c.GetHeader(runTokenHeader))
	if err != nil {
		writeSocialError(c, err)
		return p, social.RunContext{}, false
	}
	if !runProofBearer(p, origin) || origin.ConversationID != conversation ||
		origin.ConversationKind != "direct" ||
		origin.OriginUserID == nil ||
		*origin.OriginUserID != p.UserID {
		writeSocialError(c, execution.ErrRunProofInvalid)
		return p, social.RunContext{}, false
	}
	return p, social.RunContext{RunID: origin.RunID, ConversationID: conversation, AgentID: origin.AgentID}, true
}

type socialPostRequest struct {
	QuestionID string `json:"questionId" binding:"required,uuid"`
}

func (h *Handler) readAgentSocial(c *gin.Context) {
	p, run, ok := h.socialRunContext(c)
	if !ok {
		return
	}
	result, err := h.SocialService.ReadForRun(c.Request.Context(), p, run)
	if err != nil {
		writeSocialError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) publishAgentSocial(c *gin.Context) {
	p, run, ok := h.socialRunContext(c)
	if !ok {
		return
	}
	_, err := util.ParseGoUUID(c.GetHeader(idempotencyHeader))
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	input, err := bindRequest[socialPostRequest](c, 256<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	questionID, _ := util.ParseGoUUID(input.QuestionID)
	result, err := h.SocialService.PublishForRun(c.Request.Context(), p, run, questionID)
	if err != nil {
		writeSocialError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
