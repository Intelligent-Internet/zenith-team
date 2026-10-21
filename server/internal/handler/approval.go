package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/approval"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/social"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

const (
	maxHITLBodyBytes  = 64 << 10
	runTokenHeader    = "X-Zenith-Team-Run-Token"
	idempotencyHeader = "Idempotency-Key"
)

// ApprovalService is the approval surface the approval and company-plan
// routes call (satisfied by *approval.DefaultApprovalService).
type ApprovalService interface {
	RequestCompanyPlanApply(
		context.Context,
		auth.UserPrincipal,
		execution.RunOrigin,
		uuid.UUID,
		json.RawMessage,
	) (approval.Approval, bool, error)
	List(context.Context, auth.UserPrincipal, approval.ApprovalListOptions) (approval.ApprovalPage, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (approval.Approval, error)
	Stats(context.Context, auth.UserPrincipal, approval.ApprovalStatsOptions) (approval.ApprovalStats, error)
	Approve(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		approval.ApprovalDecisionInput,
	) (approval.Approval, error)
	Reject(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		approval.ApprovalDecisionInput,
	) (approval.Approval, error)
	Cancel(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		approval.ApprovalDecisionInput,
	) (approval.Approval, error)
}

// QuestionService is the Question surface the question routes call
// (satisfied by *approval.DefaultQuestionService).
type QuestionService interface {
	Ask(
		context.Context,
		auth.UserPrincipal,
		execution.RunOrigin,
		uuid.UUID,
		approval.QuestionAskInput,
	) (approval.Question, bool, error)
	List(context.Context, auth.UserPrincipal, approval.QuestionListOptions) (approval.QuestionPage, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (approval.Question, error)
	Answer(context.Context, auth.UserPrincipal, uuid.UUID, approval.QuestionAnswerInput) (approval.Question, error)
	Withdraw(context.Context, auth.UserPrincipal, uuid.UUID, int64) (approval.Question, error)
}

type approvalDecisionRequest struct {
	ExpectedRevision int64  `json:"expectedRevision" binding:"revision"`
	DecisionNote     string `json:"decisionNote"`
}

type questionAskRequest struct {
	Title   string                    `json:"title"   binding:"required"`
	Prompts []approval.QuestionPrompt `json:"prompts" binding:"required,min=1,max=10"`
}

type questionAnswerRequest struct {
	ExpectedRevision int64                     `json:"expectedRevision" binding:"revision"`
	Answers          []approval.QuestionAnswer `json:"answers"          binding:"required"`
}

type revisionRequest struct {
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}

type approvalPageResponse struct {
	Items      []approval.Approval `json:"items"`
	NextCursor *string             `json:"nextCursor"`
}

type questionPageResponse struct {
	Items      []approval.Question `json:"items"`
	NextCursor *string             `json:"nextCursor"`
}

type hitlCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

func (h *Handler) registerApprovalRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil {
		return
	}
	companyRoutes := group.Group(
		"/companies/:companyId",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	if h.ApprovalService != nil {
		routes := companyRoutes.Group("/approvals")
		routes.GET("", h.listApprovals)
		routes.GET("/stats", h.getApprovalStats)
		routes.GET("/:approvalId", h.getApproval)
		routes.POST("/:approvalId/approve", h.approveApproval)
		routes.POST("/:approvalId/reject", h.rejectApproval)
		routes.POST("/:approvalId/cancel", h.cancelApproval)
	}
	if h.QuestionService != nil {
		routes := companyRoutes.Group("/questions")
		routes.POST("", h.askQuestion)
		routes.GET("", h.listQuestions)
		routes.GET("/:questionId", h.getQuestion)
		routes.POST("/:questionId/answer", middleware.RequireHumanActor(), h.answerQuestion)
		routes.POST("/:questionId/withdraw", h.withdrawQuestion)
	}
}

func (h *Handler) askQuestion(c *gin.Context) {
	principal, ok := hitlPrincipal(c)
	if !ok {
		return
	}
	origin, requestKey, hasProof, ok := h.resolveHITLRaise(
		c, principal, approval.ErrQuestionInvalidInput,
	)
	if !ok {
		return
	}
	if !hasProof {
		writeQuestionError(c, execution.ErrRunProofInvalid)
		return
	}
	request, err := bindRequest[questionAskRequest](c, 256<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	for index := range request.Prompts {
		prompt := &request.Prompts[index]
		if prompt.SocialPost == nil {
			continue
		}
		if h.SocialService == nil || origin.ConversationKind != "direct" ||
			origin.OriginUserID == nil || *origin.OriginUserID != principal.UserID {
			writeQuestionError(c, approval.ErrQuestionInvalidInput)
			return
		}
		draft := prompt.SocialPost
		preview, err := h.SocialService.PreviewForRun(
			c.Request.Context(),
			principal,
			social.RunContext{
				RunID:          origin.RunID,
				AgentID:        origin.AgentID,
				ConversationID: origin.ConversationID,
			},
			social.PostInput{
				ConnectionID:  draft.ConnectionID,
				TargetID:      draft.TargetID,
				Text:          draft.Text,
				AttachmentIDs: draft.AttachmentIDs,
			},
		)
		if err != nil {
			writeSocialError(c, err)
			return
		}
		prompt.SocialPost = &preview
	}
	result, created, err := h.QuestionService.Ask(
		c.Request.Context(),
		principal,
		origin,
		requestKey,
		approval.QuestionAskInput{Title: request.Title, Prompts: request.Prompts},
	)
	if err != nil {
		writeQuestionError(c, err)
		return
	}
	// The status code is the whole created-signal: first creation is 201, an
	// exact idempotent replay answers the same durable Question with 200.
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	c.JSON(status, result)
}

func (h *Handler) listApprovals(c *gin.Context) {
	options, err := parseApprovalListOptions(c.Request.URL.Query())
	if err != nil {
		writeApprovalError(c, err)
		return
	}
	principal, ok := hitlPrincipal(c)
	if !ok {
		return
	}
	page, err := h.ApprovalService.List(c.Request.Context(), principal, options)
	if err != nil {
		writeApprovalError(c, err)
		return
	}
	response := approvalPageResponse{Items: page.Items}
	if response.Items == nil {
		response.Items = []approval.Approval{}
	}
	if page.Next != nil {
		cursor := encodeFeedCursor(page.Next.CreatedAt, page.Next.ID)
		response.NextCursor = &cursor
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) getApproval(c *gin.Context) {
	principal, id, ok := hitlIdentity(c, "approvalId", approval.ErrApprovalNotFound, writeApprovalError)
	if !ok {
		return
	}
	value, err := h.ApprovalService.Get(c.Request.Context(), principal, id)
	if err != nil {
		writeApprovalError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}

func (h *Handler) getApprovalStats(c *gin.Context) {
	options, err := parseApprovalStatsOptions(c.Request.URL.Query(), time.Now().UTC())
	if err != nil {
		writeApprovalError(c, err)
		return
	}
	principal, ok := hitlPrincipal(c)
	if !ok {
		return
	}
	value, err := h.ApprovalService.Stats(c.Request.Context(), principal, options)
	if err != nil {
		writeApprovalError(c, err)
		return
	}
	if value.Items == nil {
		value.Items = []approval.ApprovalStatsItem{}
	}
	c.JSON(http.StatusOK, value)
}

func (h *Handler) approveApproval(c *gin.Context) {
	h.resolveApproval(c, h.ApprovalService.Approve)
}

func (h *Handler) rejectApproval(c *gin.Context) {
	h.resolveApproval(c, h.ApprovalService.Reject)
}

func (h *Handler) cancelApproval(c *gin.Context) {
	h.resolveApproval(c, h.ApprovalService.Cancel)
}

func (h *Handler) resolveApproval(
	c *gin.Context,
	resolve func(context.Context, auth.UserPrincipal, uuid.UUID, approval.ApprovalDecisionInput) (approval.Approval, error),
) {
	request, err := bindRequest[approvalDecisionRequest](c, maxHITLBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, id, ok := hitlIdentity(c, "approvalId", approval.ErrApprovalNotFound, writeApprovalError)
	if !ok {
		return
	}
	value, err := resolve(c.Request.Context(), principal, id, approval.ApprovalDecisionInput{
		ExpectedRevision: request.ExpectedRevision,
		DecisionNote:     request.DecisionNote,
	})
	if err != nil {
		writeApprovalError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}

func (h *Handler) listQuestions(c *gin.Context) {
	options, err := parseQuestionListOptions(c.Request.URL.Query())
	if err != nil {
		writeQuestionError(c, err)
		return
	}
	principal, ok := hitlPrincipal(c)
	if !ok {
		return
	}
	page, err := h.QuestionService.List(c.Request.Context(), principal, options)
	if err != nil {
		writeQuestionError(c, err)
		return
	}
	response := questionPageResponse{Items: page.Items}
	if response.Items == nil {
		response.Items = []approval.Question{}
	}
	if page.Next != nil {
		cursor := encodeFeedCursor(page.Next.CreatedAt, page.Next.ID)
		response.NextCursor = &cursor
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) getQuestion(c *gin.Context) {
	principal, id, ok := hitlIdentity(c, "questionId", approval.ErrQuestionNotFound, writeQuestionError)
	if !ok {
		return
	}
	value, err := h.QuestionService.Get(c.Request.Context(), principal, id)
	if err != nil {
		writeQuestionError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}

func (h *Handler) answerQuestion(c *gin.Context) {
	request, err := bindRequest[questionAnswerRequest](c, maxHITLBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, id, ok := hitlIdentity(c, "questionId", approval.ErrQuestionNotFound, writeQuestionError)
	if !ok {
		return
	}
	value, err := h.QuestionService.Answer(c.Request.Context(), principal, id, approval.QuestionAnswerInput{
		ExpectedRevision: request.ExpectedRevision,
		Answers:          request.Answers,
	})
	if err != nil {
		writeQuestionError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}

func (h *Handler) withdrawQuestion(c *gin.Context) {
	request, err := bindRequest[revisionRequest](c, maxHITLBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, id, ok := hitlIdentity(c, "questionId", approval.ErrQuestionNotFound, writeQuestionError)
	if !ok {
		return
	}
	value, err := h.QuestionService.Withdraw(
		c.Request.Context(), principal, id, request.ExpectedRevision,
	)
	if err != nil {
		writeQuestionError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}

func (h *Handler) resolveHITLRaise(
	c *gin.Context,
	principal auth.UserPrincipal,
	invalidInput error,
) (execution.RunOrigin, uuid.UUID, bool, bool) {
	token := c.GetHeader(runTokenHeader)
	if token == "" {
		return execution.RunOrigin{}, uuid.Nil, false, true
	}
	requestKey, err := util.ParseGoUUID(c.GetHeader(idempotencyHeader))
	if err != nil {
		writeHITLRaiseError(c, invalidInput)
		return execution.RunOrigin{}, uuid.Nil, true, false
	}
	if h.RunProofResolver == nil {
		writeHITLRaiseError(c, errHITLConfiguration)
		return execution.RunOrigin{}, uuid.Nil, true, false
	}
	origin, err := h.resolveRunOrigin(c, token)
	if err != nil {
		writeHITLRaiseError(c, err)
		return execution.RunOrigin{}, uuid.Nil, true, false
	}
	if !runProofBearer(principal, origin) {
		writeHITLRaiseError(c, execution.ErrRunProofInvalid)
		return execution.RunOrigin{}, uuid.Nil, true, false
	}
	return origin, requestKey, true, true
}

func hitlPrincipal(c *gin.Context) (auth.UserPrincipal, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok || principal.CompanyID == uuid.Nil {
		writeHITLRaiseError(c, errHITLAuthentication)
		return auth.UserPrincipal{}, false
	}
	return principal, true
}

func hitlIdentity(
	c *gin.Context,
	parameter string,
	notFound error,
	write func(*gin.Context, error),
) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := hitlPrincipal(c)
	if !ok {
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	id, err := util.ParseGoUUID(c.Param(parameter))
	if err != nil {
		write(c, notFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, id, true
}

func parseApprovalListOptions(query url.Values) (approval.ApprovalListOptions, error) {
	base, statuses, err := parseHITLListQuery(query, map[string]bool{
		"pending": true, "approved": true, "rejected": true, "cancelled": true, "expired": true,
	})
	if err != nil {
		if errors.Is(err, errInvalidHITLCursor) {
			return approval.ApprovalListOptions{}, err
		}
		return approval.ApprovalListOptions{}, approval.ErrApprovalInvalidInput
	}
	result := approval.ApprovalListOptions{
		Limit: base.limit, AgentID: base.agentID, TaskID: base.taskID, Decide: base.decide,
		Statuses: make([]approval.ApprovalStatus, len(statuses)),
	}
	for index, status := range statuses {
		result.Statuses[index] = approval.ApprovalStatus(status)
	}
	if base.cursor != nil {
		result.Before = &approval.ApprovalCursor{CreatedAt: base.cursor.CreatedAt, ID: base.cursor.ID}
	}
	return result, nil
}

func parseQuestionListOptions(query url.Values) (approval.QuestionListOptions, error) {
	base, statuses, err := parseHITLListQuery(query, map[string]bool{
		"pending": true, "answered": true, "withdrawn": true, "expired": true,
	})
	if err != nil {
		if errors.Is(err, errInvalidHITLCursor) {
			return approval.QuestionListOptions{}, err
		}
		return approval.QuestionListOptions{}, approval.ErrQuestionInvalidInput
	}
	result := approval.QuestionListOptions{
		Limit: base.limit, AgentID: base.agentID, TaskID: base.taskID, Decide: base.decide,
		Statuses: make([]approval.QuestionStatus, len(statuses)),
	}
	for index, status := range statuses {
		result.Statuses[index] = approval.QuestionStatus(status)
	}
	if base.cursor != nil {
		result.Before = &approval.QuestionCursor{CreatedAt: base.cursor.CreatedAt, ID: base.cursor.ID}
	}
	return result, nil
}

type hitlListQuery struct {
	limit   int32
	agentID *uuid.UUID
	taskID  *uuid.UUID
	cursor  *hitlCursor
	// decide is view=decide: only what the caller may decide.
	decide bool
}

func parseHITLListQuery(query url.Values, allowedStatuses map[string]bool) (hitlListQuery, []string, error) {
	for key := range query {
		switch key {
		case "limit", "cursor", "status", "agent_id", "task_id", "view":
		default:
			return hitlListQuery{}, nil, errors.New("unknown query parameter")
		}
	}
	result := hitlListQuery{limit: 50}
	if values, exists := query["limit"]; exists {
		if len(values) != 1 || values[0] == "" {
			return hitlListQuery{}, nil, errors.New("invalid limit")
		}
		value, err := strconv.ParseInt(values[0], 10, 32)
		if err != nil || strconv.FormatInt(value, 10) != values[0] || value < 1 || value > 100 {
			return hitlListQuery{}, nil, errors.New("invalid limit")
		}
		result.limit = int32(value)
	}
	// The status set travels comma-separated in ONE value, like every
	// multi-value filter in the API; a repeated parameter is invalid.
	statuses := []string{"pending"}
	if values, exists := query["status"]; exists {
		if len(values) != 1 || values[0] == "" {
			return hitlListQuery{}, nil, errors.New("invalid status")
		}
		statuses = strings.Split(values[0], ",")
	}
	seen := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		if !allowedStatuses[status] {
			return hitlListQuery{}, nil, errors.New("invalid status")
		}
		if _, duplicate := seen[status]; duplicate {
			return hitlListQuery{}, nil, errors.New("duplicate status")
		}
		seen[status] = struct{}{}
	}
	if values, exists := query["view"]; exists {
		if len(values) != 1 || values[0] != "decide" {
			return hitlListQuery{}, nil, errors.New("invalid view")
		}
		result.decide = true
	}
	var err error
	result.agentID, err = parseOptionalHITLUUID(query, "agent_id")
	if err != nil {
		return hitlListQuery{}, nil, err
	}
	result.taskID, err = parseOptionalHITLUUID(query, "task_id")
	if err != nil {
		return hitlListQuery{}, nil, err
	}
	if values, exists := query["cursor"]; exists {
		if len(values) != 1 {
			return hitlListQuery{}, nil, errors.New("invalid cursor")
		}
		createdAt, id, ok := decodeFeedCursor(values[0])
		if !ok {
			return hitlListQuery{}, nil, errInvalidHITLCursor
		}
		result.cursor = &hitlCursor{CreatedAt: createdAt, ID: id}
	}
	return result, statuses, nil
}

func parseApprovalStatsOptions(query url.Values, now time.Time) (approval.ApprovalStatsOptions, error) {
	for key := range query {
		if key != "since" && key != "agent_id" {
			return approval.ApprovalStatsOptions{}, approval.ErrApprovalInvalidInput
		}
	}
	result := approval.ApprovalStatsOptions{Since: now.Add(-30 * 24 * time.Hour)}
	if values, exists := query["since"]; exists {
		if len(values) != 1 {
			return approval.ApprovalStatsOptions{}, approval.ErrApprovalInvalidInput
		}
		value, err := time.Parse(time.RFC3339Nano, values[0])
		if err != nil {
			return approval.ApprovalStatsOptions{}, approval.ErrApprovalInvalidInput
		}
		result.Since = value.UTC()
	}
	agentID, err := parseOptionalHITLUUID(query, "agent_id")
	if err != nil {
		return approval.ApprovalStatsOptions{}, approval.ErrApprovalInvalidInput
	}
	result.AgentID = agentID
	return result, nil
}

func parseOptionalHITLUUID(query url.Values, key string) (*uuid.UUID, error) {
	values, exists := query[key]
	if !exists {
		return nil, nil
	}
	if len(values) != 1 {
		return nil, errors.New("invalid UUID query parameter")
	}
	value, err := util.ParseGoUUID(values[0])
	if err != nil {
		return nil, err
	}
	return &value, nil
}

var (
	errHITLAuthentication = errors.New("HITL authentication required")
	errHITLConfiguration  = errors.New("HITL service configuration invalid")
	errInvalidHITLCursor  = errors.New("invalid HITL cursor")
)

var approvalErrorMappings = append([]errorMapping{
	{
		approval.ErrApprovalInvalidInput,
		http.StatusUnprocessableEntity,
		"Request validation failed",
		"validation_failed",
	},
	{approval.ErrApprovalNotFound, http.StatusNotFound, "Approval not found", "approval_not_found"},
	{
		approval.ErrApprovalForbidden,
		http.StatusForbidden,
		"Approval resolver is not permitted",
		"approval_resolver_forbidden",
	},
	{approval.ErrApprovalConflict, http.StatusConflict, "Approval changed; refresh and retry", "approval_conflict"},
	{approval.ErrApprovalExpired, http.StatusGone, "Approval expired", "approval_expired"},
	projectPermissionRequiredMapping,
	noEligibleResolverMapping,
	runTokenInvalidMapping,
	{errInvalidHITLCursor, http.StatusUnprocessableEntity, "cursor is invalid", "invalid_cursor"},
	// Raising a Company Plan Approval prepares the plan, and approving one
	// applies it; both refuse a plan exactly as the direct apply does.
	{company.ErrPlanInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
	{
		company.ErrPlanConflict,
		http.StatusConflict,
		"Work Plan mutation conflicts with current state",
		"work_plan_conflict",
	},
	{company.ErrPlanNotFound, http.StatusNotFound, "Work Plan entity not found", "work_plan_not_found"},
}, companyPlanTaskAuthorityMappings...)

var questionErrorMappings = []errorMapping{
	{
		approval.ErrQuestionInvalidInput,
		http.StatusUnprocessableEntity,
		"Request validation failed",
		"validation_failed",
	},
	{approval.ErrQuestionNotFound, http.StatusNotFound, "Question not found", "question_not_found"},
	{
		approval.ErrQuestionForbidden,
		http.StatusForbidden,
		"Question resolver is not permitted",
		"question_resolver_forbidden",
	},
	{approval.ErrQuestionConflict, http.StatusConflict, "Question changed; refresh and retry", "question_conflict"},
	{approval.ErrQuestionExpired, http.StatusGone, "Question expired", "question_expired"},
	projectPermissionRequiredMapping,
	noEligibleResolverMapping,
	runTokenInvalidMapping,
	{errInvalidHITLCursor, http.StatusUnprocessableEntity, "cursor is invalid", "invalid_cursor"},
}

var hitlRaiseErrorMappings = []errorMapping{
	{
		approval.ErrApprovalInvalidInput,
		http.StatusUnprocessableEntity,
		"Request validation failed",
		"validation_failed",
	},
	{
		approval.ErrQuestionInvalidInput,
		http.StatusUnprocessableEntity,
		"Request validation failed",
		"validation_failed",
	},
	runTokenInvalidMapping,
	{errHITLAuthentication, http.StatusUnauthorized, "authentication is required", "authentication_required"},
}

func writeApprovalError(c *gin.Context, err error) {
	writeMappedError(c, err, approvalErrorMappings, internalErrorFallback)
}

func writeQuestionError(c *gin.Context, err error) {
	writeMappedError(c, err, questionErrorMappings, internalErrorFallback)
}

func writeHITLRaiseError(c *gin.Context, err error) {
	writeMappedError(c, err, hitlRaiseErrorMappings, internalErrorFallback)
}
