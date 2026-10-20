package handler

import (
	"boardly.dev/boardly/server/internal/task"
	"crypto/subtle"
	"net/http"
	"strconv"
	"time"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type setHuddleNotesRequest struct {
	Enabled          *bool  `json:"enabled" binding:"required"`
	ExpectedRevision int64  `json:"expectedRevision" binding:"revision"`
	RequestKey       string `json:"requestKey" binding:"required,uuid"`
}
type huddleNotesPolicyRequest struct {
	Enabled          *bool `json:"enabled" binding:"required"`
	RetentionDays    int32 `json:"retentionDays" binding:"gte=1,lte=365"`
	MonthlySeconds   int64 `json:"monthlySeconds" binding:"gte=0,lte=100000000"`
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}
type huddleNotesBatchRequest struct {
	Sequence int64  `json:"sequence" binding:"gte=0,lte=10000"`
	Speaker  string `json:"speaker" binding:"max=200"`
	Text     string `json:"text" binding:"max=16000"`
	StartMs  int64  `json:"startMs" binding:"gte=0,lte=3600000"`
	EndMs    int64  `json:"endMs" binding:"gte=0,lte=3600000"`
	Ready    bool   `json:"ready"`
	Finished bool   `json:"finished"`
}

func (h *Handler) registerHuddleNotesRoutes(api *gin.RouterGroup) {
	if h.HuddleNotesService == nil {
		return
	}
	internal := api.Group("/internal/huddle-notes/:attemptId", middleware.PrivateNoStore(), middleware.RequestTimeout(5*time.Second))
	internal.POST("/claim", h.claimHuddleNotes)
	internal.POST("/append", h.appendHuddleNotes)
	if h.AuthService == nil || h.CompanyService == nil {
		return
	}
	group := api.Group("/companies/:companyId/huddles", middleware.PrivateNoStore(), middleware.RequestTimeout(5*time.Second), middleware.RequireUserAuth(h.AuthService), middleware.RequireHumanActor(), middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionConversationsDiscuss))
	group.GET("/notes-policy", h.getHuddleNotesPolicy)
	group.PUT("/notes-policy", h.setHuddleNotesPolicy)
	group.GET("/:huddleId/notes", h.getHuddleNotes)
	group.PUT("/:huddleId/notes", h.setHuddleNotes)
	group.PATCH("/:huddleId/notes/recap", h.editHuddleNotesRecap)
	if h.TaskService != nil {
		group.POST("/:huddleId/notes/actions/:actionIndex/submit", h.submitHuddleNotes)
	}
}
func (h *Handler) getHuddleNotes(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	id, err := uuid.Parse(c.Param("huddleId"))
	if err != nil {
		writeHuddleError(c, conversation.ErrHuddleInvalid)
		return
	}
	after := uuid.Nil
	if c.Query("cursor") != "" {
		after, err = uuid.Parse(c.Query("cursor"))
		if err != nil {
			writeHuddleError(c, conversation.ErrHuddleInvalid)
			return
		}
	}
	result, err := h.HuddleNotesService.GetPage(c.Request.Context(), p, id, after)
	if err != nil {
		writeHuddleNotesError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) setHuddleNotes(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	id, err := uuid.Parse(c.Param("huddleId"))
	if err != nil {
		writeHuddleError(c, conversation.ErrHuddleInvalid)
		return
	}
	body, err := bindRequest[setHuddleNotesRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.HuddleNotesService.SetEnabled(c.Request.Context(), p, id, conversation.SetHuddleNotesInput{Enabled: *body.Enabled, ExpectedRevision: body.ExpectedRevision, RequestKey: uuid.MustParse(body.RequestKey)})
	if err != nil {
		writeHuddleNotesError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) getHuddleNotesPolicy(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.HuddleNotesService.Policy(c.Request.Context(), p, nil)
	if err != nil {
		writeHuddleNotesError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) setHuddleNotesPolicy(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	body, err := bindRequest[huddleNotesPolicyRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.HuddleNotesService.Policy(c.Request.Context(), p, &conversation.HuddleNotesPolicy{Enabled: *body.Enabled, RetentionDays: body.RetentionDays, MonthlySeconds: body.MonthlySeconds, Revision: body.ExpectedRevision})
	if err != nil {
		writeHuddleNotesError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) claimHuddleNotes(c *gin.Context) {
	id, token, ok := huddleAIAttempt(c)
	if !ok {
		return
	}
	if h.HuddleAIWorkloadSecret == "" || subtle.ConstantTimeCompare([]byte(token), []byte(h.HuddleAIWorkloadSecret)) != 1 {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.HuddleNotesService.Claim(c.Request.Context(), id)
	if err != nil {
		writeHuddleNotesError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *Handler) appendHuddleNotes(c *gin.Context) {
	id, token, ok := huddleAIAttempt(c)
	if !ok {
		return
	}
	body, err := bindRequest[huddleNotesBatchRequest](c, 24*1024)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.HuddleNotesService.Append(c.Request.Context(), id, token, conversation.HuddleNotesBatch{Sequence: body.Sequence, Speaker: body.Speaker, Text: body.Text, StartMs: body.StartMs, EndMs: body.EndMs, Ready: body.Ready, Finished: body.Finished})
	if err != nil {
		writeHuddleNotesError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

var huddleNotesErrorMappings = []errorMapping{
	{conversation.ErrHuddleNotesUnavailable, http.StatusConflict, "Notes are unavailable. Check call status, Company settings and usage allowance.", "huddle_notes_unavailable"},
	{conversation.ErrHuddleNotesConflict, http.StatusConflict, "Notes changed. Reload and retry.", "huddle_notes_conflict"},
}

func writeHuddleNotesError(c *gin.Context, err error) {
	writeMappedError(c, err, append(append([]errorMapping{}, huddleErrorMappings...), huddleNotesErrorMappings...), internalErrorFallback)
}

type editHuddleNotesRecapRequest struct {
	ExpectedRevision int64                         `json:"expectedRevision" binding:"revision"`
	Recap            conversation.HuddleNotesRecap `json:"recap" binding:"required"`
}

func (h *Handler) editHuddleNotesRecap(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	id, err := uuid.Parse(c.Param("huddleId"))
	if err != nil {
		writeHuddleError(c, conversation.ErrHuddleInvalid)
		return
	}
	body, err := bindRequest[editHuddleNotesRecapRequest](c, 128*1024)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.HuddleNotesService.EditRecap(c.Request.Context(), p, conversation.EditHuddleNotesRecapInput{HuddleID: id, ExpectedRevision: body.ExpectedRevision, Recap: body.Recap})
	if err != nil {
		writeHuddleNotesError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

type submitHuddleNotesRequest struct {
	ExpectedRevision int64              `json:"expectedRevision" binding:"revision"`
	ProjectID        string             `json:"projectId" binding:"required,uuid"`
	AgentID          string             `json:"agentId" binding:"omitempty,uuid"`
	Title            string             `json:"title" binding:"required,max=200"`
	MilestoneType    task.MilestoneType `json:"milestoneType" binding:"required"`
	Confirm          bool               `json:"confirm" binding:"required"`
}

func (h *Handler) submitHuddleNotes(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	id, err := uuid.Parse(c.Param("huddleId"))
	if err != nil {
		writeHuddleError(c, conversation.ErrHuddleInvalid)
		return
	}
	index, err := strconv.ParseInt(c.Param("actionIndex"), 10, 32)
	if err != nil || index < 0 || index >= 20 {
		writeHuddleError(c, conversation.ErrHuddleInvalid)
		return
	}
	body, err := bindRequest[submitHuddleNotesRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	agentID := uuid.Nil
	if body.AgentID != "" {
		agentID = uuid.MustParse(body.AgentID)
	}
	result, err := h.TaskService.SubmitHuddleNotes(c.Request.Context(), p, task.SubmitHuddleNotesInput{HuddleID: id, ActionIndex: int32(index), ExpectedRevision: body.ExpectedRevision, ProjectID: uuid.MustParse(body.ProjectID), AgentID: agentID, Title: body.Title, MilestoneType: body.MilestoneType, Confirm: body.Confirm})
	if err != nil {
		writeMappedError(c, err, append(append(append([]errorMapping{}, huddleErrorMappings...), huddleNotesErrorMappings...), taskErrorMappings...), internalErrorFallback)
		return
	}
	c.JSON(http.StatusOK, result)
}
