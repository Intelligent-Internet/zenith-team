package handler

import (
	"net/http"

	"boardly.dev/boardly/server/internal/agent/voice"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type huddleToolRequest struct {
	RequestID    string `json:"requestId" binding:"required,max=128"`
	Tool         string `json:"tool" binding:"required,oneof=get_company_context list_projects search_tasks get_task get_work_status get_work_result"`
	ProposalID   string `json:"proposalId" binding:"required_if=Tool get_work_result,excluded_unless=Tool get_work_status Tool get_work_result,omitempty,uuid"`
	Query        string `json:"query" binding:"excluded_unless=Tool search_tasks,max=200"`
	Status       string `json:"status" binding:"excluded_unless=Tool search_tasks,omitempty,oneof=backlog todo in_progress blocked in_review done cancelled failed error"`
	ProjectID    string `json:"projectId" binding:"excluded_unless=Tool search_tasks,omitempty,uuid"`
	TaskID       string `json:"taskId" binding:"required_if=Tool get_task,excluded_unless=Tool get_task,omitempty,uuid"`
	AfterID      string `json:"afterId" binding:"excluded_unless=Tool list_projects,omitempty,uuid"`
	BeforeNumber int64  `json:"beforeNumber" binding:"excluded_unless=Tool search_tasks,min=0,max=9007199254740991"`
}

func (h *Handler) readHuddleContext(c *gin.Context) {
	id, token, ok := huddleAIAttempt(c)
	if !ok {
		return
	}
	body, err := bindRequest[huddleToolRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	in := voice.ContextRequest{RequestID: body.RequestID, Tool: body.Tool, Query: body.Query, Status: body.Status, BeforeNumber: body.BeforeNumber}
	for raw, target := range map[string]*uuid.UUID{body.ProjectID: &in.ProjectID, body.TaskID: &in.TaskID, body.AfterID: &in.AfterID, body.ProposalID: &in.ProposalID} {
		if raw == "" {
			continue
		}
		value, ok := parseUUIDOrBadRequest(c, raw, "identifier")
		if !ok {
			return
		}
		*target = value
	}
	result, err := h.HuddleAIService.ReadContext(c.Request.Context(), id, token, in)
	if err != nil {
		writeHuddleAIError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
