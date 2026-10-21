package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
)

type huddleProposalRequest struct {
	RequestKey string `json:"requestKey" binding:"required,max=128"`
	ProjectID  string `json:"projectId"  binding:"required,uuid"`
	TaskID     string `json:"taskId"     binding:"omitempty,uuid"`
	Title      string `json:"title"      binding:"required,max=200"`
	Brief      string `json:"brief"      binding:"required,max=4000"`
}

type updateHuddleProposalRequest struct {
	ExpectedRevision int64  `json:"expectedRevision" binding:"revision"`
	Action           string `json:"action"           binding:"required,oneof=edit reject"`
	Title            string `json:"title"            binding:"required_if=Action edit,excluded_unless=Action edit,max=200"`
	Brief            string `json:"brief"            binding:"required_if=Action edit,excluded_unless=Action edit,max=4000"`
}

type submitHuddleProposalRequest struct {
	ExpectedRevision     int64              `json:"expectedRevision"     binding:"revision"`
	Confirm              bool               `json:"confirm"              binding:"required"`
	MilestoneType        task.MilestoneType `json:"milestoneType"`
	ExpectedTaskRevision int64              `json:"expectedTaskRevision" binding:"omitempty,revision"`
}

func (h *Handler) submitHuddleProposal(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	huddleID, ok := parseUUIDOrBadRequest(c, c.Param("huddleId"), "huddleId")
	if !ok {
		return
	}
	proposalID, ok := parseUUIDOrBadRequest(c, c.Param("proposalId"), "proposalId")
	if !ok {
		return
	}
	body, err := bindRequest[submitHuddleProposalRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.TaskService.SubmitHuddleProposal(
		c.Request.Context(),
		p,
		huddleID,
		proposalID,
		task.SubmitHuddleProposalInput{
			ExpectedRevision:     body.ExpectedRevision,
			Confirm:              body.Confirm,
			MilestoneType:        body.MilestoneType,
			ExpectedTaskRevision: body.ExpectedTaskRevision,
		},
	)
	if err != nil {
		mappings := append(
			append(append([]errorMapping{}, huddleErrorMappings...), huddleAIErrorMappings...),
			taskErrorMappings...)
		writeMappedError(c, err, mappings, internalErrorFallback)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) proposeHuddleWork(c *gin.Context) {
	id, token, ok := huddleAIAttempt(c)
	if !ok {
		return
	}
	body, err := bindRequest[huddleProposalRequest](c, 32768)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	projectID, ok := parseUUIDOrBadRequest(c, body.ProjectID, "projectId")
	if !ok {
		return
	}
	in := conversation.HuddleProposalInput{
		RequestKey: body.RequestKey,
		ProjectID:  projectID,
		Title:      body.Title,
		Brief:      body.Brief,
	}
	if body.TaskID != "" {
		taskID, ok := parseUUIDOrBadRequest(c, body.TaskID, "taskId")
		if !ok {
			return
		}
		in.TaskID = uuid.NullUUID{UUID: taskID, Valid: true}
	}
	result, err := h.HuddleAIService.ProposeWork(c.Request.Context(), id, token, in)
	if err != nil {
		writeHuddleAIError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) updateHuddleProposal(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	huddleID, ok := parseUUIDOrBadRequest(c, c.Param("huddleId"), "huddleId")
	if !ok {
		return
	}
	proposalID, ok := parseUUIDOrBadRequest(c, c.Param("proposalId"), "proposalId")
	if !ok {
		return
	}
	body, err := bindRequest[updateHuddleProposalRequest](c, 32768)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.HuddleAIService.UpdateWorkProposal(
		c.Request.Context(),
		p,
		huddleID,
		conversation.UpdateHuddleProposalInput{
			ProposalID:       proposalID,
			ExpectedRevision: body.ExpectedRevision,
			Action:           body.Action,
			Title:            body.Title,
			Brief:            body.Brief,
		},
	)
	if err != nil {
		writeHuddleAIError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
