package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"

	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
)

const (
	maxConversationBodyBytes = 512 << 10
)

var errInvalidConversationTranscriptQuery = errors.New("invalid Conversation transcript query")

type ConversationService interface {
	FetchModelSettings(context.Context, auth.UserPrincipal, uuid.UUID) (conversation.ModelSettings, error)
	UpdateModelSettings(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		conversation.ModelSettingsInput,
	) (conversation.ModelSettings, error)
	ListStates(context.Context, auth.UserPrincipal) ([]conversation.UserState, error)
	ListPins(context.Context, auth.UserPrincipal) ([]conversation.UserState, error)
	CreateDirect(
		context.Context,
		auth.UserPrincipal,
		conversation.CreateDirectInput,
	) (conversation.CreateDirectResult, error)
	RenameDirect(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		conversation.RenameDirectInput,
	) (conversation.Response, error)
	ListDirect(
		context.Context,
		auth.UserPrincipal,
		bool,
	) (conversation.DirectPage, error)
	FetchDirectDetail(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		conversation.MessageWindow,
	) (conversation.DirectDetail, error)
	ListMessages(
		context.Context,
		auth.UserPrincipal,
		conversation.MessageTarget,
		conversation.MessageListOptions,
	) (conversation.MessagePage, error)
	Transcript(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		conversation.TranscriptListOptions,
	) (conversation.TranscriptPage, error)
	Send(
		context.Context,
		auth.UserPrincipal,
		conversation.MessageTarget,
		conversation.SendInput,
	) (conversation.SendResult, error)
	CreateWhiteboard(
		context.Context,
		auth.UserPrincipal,
		conversation.MessageTarget,
		string,
	) (conversation.CreatedWhiteboard, error)
	FetchRunQueue(context.Context, auth.UserPrincipal, uuid.UUID) (conversation.RunQueueProjection, error)
	ReorderRunQueue(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		conversation.RunQueueOrderInput,
	) (conversation.RunQueueProjection, error)
	DeleteRunQueueItem(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		conversation.DeleteRunQueueItemInput,
	) (conversation.RunQueueProjection, error)
	FetchState(context.Context, auth.UserPrincipal, uuid.UUID) (conversation.UserState, error)
	UpdateState(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		conversation.UpdateUserStateInput,
	) (conversation.UserState, error)
}

type MessageContextService interface {
	FetchMessageContext(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		int,
	) (conversation.MessageContextPage, error)
}

type createDirectConversationRequest struct {
	RequestKey optional[string] `json:"requestKey"`
	AgentID    string           `json:"agentId"    binding:"required,uuid"`
	Title      optional[string] `json:"title"`
}

type renameConversationRequest struct {
	Title            string `json:"title"            binding:"required,max=120"`
	ExpectedRevision int64  `json:"expectedRevision" binding:"revision"`
}

type sendConversationMessageRequest struct {
	ConnectorReferences []conversation.ConnectorReference `json:"connectorReferences" binding:"max=32"`
	SkillReferences     []conversation.SkillReference     `json:"skillReferences"     binding:"max=32"`
	RequestKey          string                            `json:"requestKey"          binding:"required,uuid"`
	Content             string                            `json:"content"`
	AttachmentIDs       json.RawMessage                   `json:"attachmentIds"`
	ArtifactIDs         []string                          `json:"artifactIds"         binding:"max=10,unique,dive,uuid"`
	// SuppressUserMessage starts the Agent without writing the prompt into the
	// transcript, for instructions the person never typed.
	SuppressUserMessage bool `json:"suppressUserMessage"`
}

// updateConversationStateRequest is the one revision field the `revision` rule
// does not fit: a Conversation's user state is created on first write, so 0 is
// the revision a caller legitimately sends for a row that does not exist yet.
// The bound is `>= 0`, which the OpenAPI schema states as minimum 0, and
// conversation.normalizeUpdateUserStateInput remains its sole enforcement.
type updateConversationStateRequest struct {
	ExpectedRevision optional[int64] `json:"expectedRevision" binding:"omitnil,required"`
	Pinned           optional[bool]  `json:"pinned"           binding:"required_without=Archived"`
	Archived         optional[bool]  `json:"archived"`
}

type reorderRunQueueRequest struct {
	Items []reorderRunQueueItemRequest `json:"items" binding:"required,min=1,max=20,dive"`
}

type reorderRunQueueItemRequest struct {
	ID               string          `json:"id"               binding:"required,uuid"`
	ExpectedRevision optional[int64] `json:"expectedRevision" binding:"omitnil,required,revision"`
}

type deleteRunQueueItemRequest struct {
	ExpectedRevision optional[int64] `json:"expectedRevision" binding:"omitnil,required,revision"`
}

// messagePageResponse carries a message window with its navigation scalars.
// Whether older or newer rows exist is derived by the client from the
// window's own seq range against the bounds, never carried.
type messagePageResponse struct {
	Items                []messageWithAttachmentsResponse `json:"items"`
	EarliestAvailableSeq int64                            `json:"earliestAvailableSeq"`
	ThroughSeq           int64                            `json:"throughSeq"`
}

type messageContextPageResponse struct {
	Items                []messageWithAttachmentsResponse `json:"items"`
	TargetSeq            int64                            `json:"targetSeq"`
	EarliestAvailableSeq int64                            `json:"earliestAvailableSeq"`
	ThroughSeq           int64                            `json:"throughSeq"`
}

type conversationTranscriptResponse struct {
	Items      []conversation.TranscriptEntry `json:"items"`
	ThroughSeq int64                          `json:"throughSeq"`
	HasMore    bool                           `json:"hasMore"`
}

type conversationTranscriptQuery struct {
	afterSeq int64
	limit    int32
}

func newMessageContextPageResponse(page conversation.MessageContextPage) messageContextPageResponse {
	return messageContextPageResponse{
		Items:                messageWithAttachmentsResponses(page.Items),
		TargetSeq:            page.TargetSeq,
		EarliestAvailableSeq: page.EarliestAvailableSeq,
		ThroughSeq:           page.ThroughSeq,
	}
}

// sendConversationMessageResponse reports what one send became. runId is
// present only for the `message` disposition: runs.id is a database-generated UUID the
// promotion transaction takes by inserting the Run, so a prompt parked behind
// a live Run has no run identity yet and the field is absent rather than null.
// A client following a queued send follows queueItem.id.
type sendConversationMessageResponse struct {
	Disposition conversation.SendDisposition    `json:"disposition"`
	Message     *messageWithAttachmentsResponse `json:"message,omitempty"`
	RunID       *uuid.UUID                      `json:"runId,omitempty"`
	QueueItem   *conversation.RunQueueItem      `json:"queueItem,omitempty"`
}

func (h *Handler) registerConversationRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.ConversationService == nil {
		return
	}
	messages := group.Group("", middleware.RequireUserAuth(h.AuthService))
	messages.GET("/conversations/:conversationId/model-settings", h.getConversationModelSettings)
	messages.PUT("/conversations/:conversationId/model-settings", h.updateConversationModelSettings)
	messages.GET("/conversations/:conversationId/state", h.getConversationState)
	messages.GET("/conversations/:conversationId/run-queue", h.getConversationRunQueue)
	messages.PUT("/conversations/:conversationId/run-queue/order", h.reorderConversationRunQueue)
	messages.DELETE(
		"/conversations/:conversationId/run-queue/:queueItemId",
		h.deleteConversationRunQueueItem,
	)
	messages.PATCH("/conversations/:conversationId/state", h.updateConversationState)
	messages.GET("/conversations/:conversationId/messages", h.listConversationMessages)
	if h.MessageContextService != nil {
		messages.GET("/conversations/:conversationId/messages/:messageId/context", h.getMessageContext)
	}
	messages.POST("/conversations/:conversationId/messages", h.sendConversationMessage)
	messages.GET("/tasks/:taskId/messages", h.listTaskMessages)
	messages.POST("/tasks/:taskId/messages", h.sendTaskMessage)
	if h.DeliverableService != nil {
		messages.POST("/conversations/:conversationId/whiteboards", h.createChatWhiteboard)
	}

	if h.CompanyService != nil {
		conversations := group.Group(
			"/companies/:companyId/conversations",
			middleware.RequireUserAuth(h.AuthService),
			middleware.RequireCompanyMember(h.CompanyService),
		)
		conversations.GET("", h.listCompanyConversations)
		conversations.GET("/pins", h.listConversationPins)
		conversations.GET("/states", h.listConversationStates)
		conversations.POST("", h.createCompanyConversation)
		conversations.GET("/:conversationId", h.getCompanyConversationDetail)
		conversations.PATCH("/:conversationId", h.renameCompanyConversation)
		conversations.GET("/:conversationId/transcript", h.getConversationTranscript)
	}
}

func (h *Handler) getConversationTranscript(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	conversationID, err := util.ParseGoUUID(c.Param("conversationId"))
	if err != nil {
		writeConversationError(c, conversation.ErrNotFound)
		return
	}
	if !h.runOwnReadAdmitted(c, principal, runOwnsConversation(conversationID)) {
		return
	}
	query, err := parseConversationTranscriptQuery(c)
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	page, err := h.ConversationService.Transcript(
		c.Request.Context(),
		principal,
		conversationID,
		conversation.TranscriptListOptions{AfterSeq: query.afterSeq, Limit: query.limit},
	)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	c.JSON(http.StatusOK, conversationTranscriptResponse{
		Items: page.Items, ThroughSeq: page.ThroughSeq, HasMore: page.HasMore,
	})
}

func parseConversationTranscriptQuery(c *gin.Context) (conversationTranscriptQuery, error) {
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return conversationTranscriptQuery{}, errInvalidConversationTranscriptQuery
	}
	for key, entries := range values {
		if (key != "after_seq" && key != "limit") || len(entries) != 1 || entries[0] == "" {
			return conversationTranscriptQuery{}, errInvalidConversationTranscriptQuery
		}
	}

	query := conversationTranscriptQuery{limit: 200}
	if entries, exists := values["after_seq"]; exists {
		sequence, err := parseCanonicalNonnegativeInt(entries[0])
		if err != nil {
			return conversationTranscriptQuery{}, errInvalidConversationTranscriptQuery
		}
		query.afterSeq = sequence
	}
	if entries, exists := values["limit"]; exists {
		limit, err := strconv.Atoi(entries[0])
		if err != nil || strconv.Itoa(limit) != entries[0] || limit < 1 || limit > 200 {
			return conversationTranscriptQuery{}, errInvalidConversationTranscriptQuery
		}
		query.limit = int32(limit)
	}
	return query, nil
}

func (h *Handler) getMessageContext(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeConversationError(c, conversation.ErrUnauthenticated)
		return
	}
	conversationID, conversationErr := util.ParseGoUUID(c.Param("conversationId"))
	messageID, messageErr := util.ParseGoUUID(c.Param("messageId"))
	if errors.Join(conversationErr, messageErr) != nil {
		writeConversationError(c, conversation.ErrNotFound)
		return
	}
	limit, err := parseMessageContextLimit(c.Request.URL.RawQuery)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	page, err := h.MessageContextService.FetchMessageContext(
		c.Request.Context(), principal, conversationID, messageID, limit,
	)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	c.JSON(http.StatusOK, newMessageContextPageResponse(page))
}

func parseMessageContextLimit(rawQuery string) (int, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return 0, conversation.ErrInvalidInput
	}
	if len(query) == 0 {
		return 50, nil
	}
	values, exists := query["limit"]
	if !exists || len(query) != 1 || len(values) != 1 || values[0] == "" {
		return 0, conversation.ErrInvalidInput
	}
	limit, err := strconv.Atoi(values[0])
	if err != nil || strconv.Itoa(limit) != values[0] || limit < 1 || limit > 100 {
		return 0, conversation.ErrInvalidInput
	}
	return limit, nil
}

func (h *Handler) listConversationMessages(c *gin.Context) {
	h.listMessages(c, conversation.TargetConversation, "conversationId")
}

// runOwnsConversation reports whether a Run's proof names this chat as the
// Run's own, for the chat reads the Run channel offers (runOwnReadAdmitted).
// A Run tagged in a channel or group DM owns no chat read: the channel
// offers it none, so its proof reads no chat here either.
func runOwnsConversation(conversationID uuid.UUID) func(execution.RunOrigin) bool {
	return func(origin execution.RunOrigin) bool {
		return origin.InvocationChannelID == nil && origin.ConversationID == conversationID
	}
}

func (h *Handler) listTaskMessages(c *gin.Context) {
	h.listMessages(c, conversation.TargetTask, "taskId")
}

func (h *Handler) listMessages(c *gin.Context, kind conversation.TargetKind, parameter string) {
	principal, target, ok := conversationMessageIdentity(c, kind, parameter)
	if !ok {
		return
	}
	// A Run reads its own chat's messages (`zen conversation messages`). The
	// Task message route accepts no Run, so RunProofPolicy refuses it first.
	if kind == conversation.TargetConversation &&
		!h.runOwnReadAdmitted(c, principal, runOwnsConversation(target.ID)) {
		return
	}
	options, err := parseMessageListOptions(c.Request.URL.RawQuery)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	page, err := h.ConversationService.ListMessages(c.Request.Context(), principal, target, options)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	if page.Items == nil {
		page.Items = []conversation.MessageView{}
	}
	c.JSON(http.StatusOK, messagePageResponse{
		Items:                messageWithAttachmentsResponses(page.Items),
		EarliestAvailableSeq: page.EarliestAvailableSeq, ThroughSeq: page.ThroughSeq,
	})
}

func (h *Handler) sendConversationMessage(c *gin.Context) {
	h.sendMessage(c, conversation.TargetConversation, "conversationId")
}

func (h *Handler) sendTaskMessage(c *gin.Context) {
	h.sendMessage(c, conversation.TargetTask, "taskId")
}

type createChatWhiteboardRequest struct {
	Title string `json:"title" binding:"required"`
}

// createChatWhiteboard makes a Whiteboard in an Agent or Task chat and answers
// with it as the chat's Artifacts list it, so the panel opens it at once.
func (h *Handler) createChatWhiteboard(c *gin.Context) {
	principal, target, ok := conversationMessageIdentity(c, conversation.TargetConversation, "conversationId")
	if !ok {
		return
	}
	request, err := bindRequest[createChatWhiteboardRequest](c, 4<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	created, err := h.ConversationService.CreateWhiteboard(c.Request.Context(), principal, target, request.Title)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	principal.CompanyID = created.CompanyID
	items, err := h.DeliverableService.ResolveArtifacts(
		c.Request.Context(), principal, []uuid.UUID{created.ArtifactID},
	)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	if len(items) != 1 {
		writeDeliverableError(c, execution.ErrDeliverableNotFound)
		return
	}
	c.JSON(http.StatusCreated, items[0])
}

func (h *Handler) updateConversationState(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeConversationError(c, conversation.ErrUnauthenticated)
		return
	}
	conversationID, err := util.ParseGoUUID(c.Param("conversationId"))
	if err != nil {
		writeConversationError(c, conversation.ErrNotFound)
		return
	}
	request, err := bindRequest[updateConversationStateRequest](c, maxConversationBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	state, err := h.ConversationService.UpdateState(
		c.Request.Context(),
		principal,
		conversationID,
		conversation.UpdateUserStateInput{
			ExpectedRevision: *request.ExpectedRevision.ptr(),
			Pinned:           request.Pinned.ptr(),
			Archived:         request.Archived.ptr(),
		},
	)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

func (h *Handler) getConversationState(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeConversationError(c, conversation.ErrUnauthenticated)
		return
	}
	conversationID, err := util.ParseGoUUID(c.Param("conversationId"))
	if err != nil {
		writeConversationError(c, conversation.ErrNotFound)
		return
	}
	state, err := h.ConversationService.FetchState(c.Request.Context(), principal, conversationID)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

func (h *Handler) sendMessage(c *gin.Context, kind conversation.TargetKind, parameter string) {
	principal, target, ok := conversationMessageIdentity(c, kind, parameter)
	if !ok {
		return
	}
	request, err := bindRequest[sendConversationMessageRequest](c, maxConversationBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	requestKey, requestKeyErr := util.ParseGoUUID(request.RequestKey)
	attachmentIDs, attachmentErr := parseConversationAttachmentIDs(request.AttachmentIDs)
	if errors.Join(requestKeyErr, attachmentErr) != nil {
		writeConversationError(c, conversation.ErrInvalidInput)
		return
	}
	result, err := h.ConversationService.Send(c.Request.Context(), principal, target, conversation.SendInput{
		RequestKey:          requestKey,
		Content:             request.Content,
		AttachmentIDs:       attachmentIDs,
		ArtifactIDs:         discussionUUIDs(request.ArtifactIDs),
		SkillReferences:     request.SkillReferences,
		ConnectorReferences: request.ConnectorReferences,
		SuppressUserMessage: request.SuppressUserMessage,
	})
	if err != nil {
		writeConversationError(c, err)
		return
	}
	status := http.StatusOK
	var message *messageWithAttachmentsResponse
	switch result.Disposition {
	case conversation.SendDispositionMessage:
		if !result.Suppressed {
			flattened := messageWithAttachmentsResponses([]conversation.MessageView{result.View})
			message = &flattened[0]
		}
		if result.Created {
			status = http.StatusCreated
		}
	case conversation.SendDispositionQueued:
		status = http.StatusAccepted
	}
	c.JSON(status, sendConversationMessageResponse{
		Disposition: result.Disposition,
		Message:     message, RunID: result.RunID,
		QueueItem: result.QueueItem,
	})
}

func (h *Handler) getConversationRunQueue(c *gin.Context) {
	principal, conversationID, ok := conversationRunQueueIdentity(c)
	if !ok {
		return
	}
	projection, err := h.ConversationService.FetchRunQueue(
		c.Request.Context(), principal, conversationID,
	)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	c.JSON(http.StatusOK, projection)
}

func (h *Handler) reorderConversationRunQueue(c *gin.Context) {
	principal, conversationID, ok := conversationRunQueueIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[reorderRunQueueRequest](c, maxConversationBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	items := make([]conversation.RunQueueOrderItem, len(request.Items))
	for index, item := range request.Items {
		id, idErr := util.ParseGoUUID(item.ID)
		if idErr != nil {
			writeConversationError(c, conversation.ErrInvalidInput)
			return
		}
		items[index] = conversation.RunQueueOrderItem{
			ID: id, ExpectedRevision: *item.ExpectedRevision.ptr(),
		}
	}
	projection, err := h.ConversationService.ReorderRunQueue(
		c.Request.Context(), principal, conversationID, conversation.RunQueueOrderInput{Items: items},
	)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	c.JSON(http.StatusOK, projection)
}

func (h *Handler) deleteConversationRunQueueItem(c *gin.Context) {
	principal, conversationID, ok := conversationRunQueueIdentity(c)
	if !ok {
		return
	}
	queueItemID, err := util.ParseGoUUID(c.Param("queueItemId"))
	if err != nil {
		writeConversationError(c, conversation.ErrNotFound)
		return
	}
	request, err := bindRequest[deleteRunQueueItemRequest](c, maxConversationBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	projection, err := h.ConversationService.DeleteRunQueueItem(
		c.Request.Context(), principal, conversationID, queueItemID,
		conversation.DeleteRunQueueItemInput{ExpectedRevision: *request.ExpectedRevision.ptr()},
	)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	c.JSON(http.StatusOK, projection)
}

func conversationRunQueueIdentity(
	c *gin.Context,
) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeConversationError(c, conversation.ErrUnauthenticated)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	conversationID, err := util.ParseGoUUID(c.Param("conversationId"))
	if err != nil {
		writeConversationError(c, conversation.ErrNotFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, conversationID, true
}

func conversationMessageIdentity(
	c *gin.Context,
	kind conversation.TargetKind,
	parameter string,
) (auth.UserPrincipal, conversation.MessageTarget, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeConversationError(c, conversation.ErrUnauthenticated)
		return auth.UserPrincipal{}, conversation.MessageTarget{}, false
	}
	id, err := util.ParseGoUUID(c.Param(parameter))
	if err != nil {
		writeConversationError(c, conversation.ErrNotFound)
		return auth.UserPrincipal{}, conversation.MessageTarget{}, false
	}
	return principal, conversation.MessageTarget{Kind: kind, ID: id}, true
}

func parseConversationAttachmentIDs(raw json.RawMessage) ([]uuid.UUID, error) {
	if len(raw) == 0 {
		return []uuid.UUID{}, nil
	}
	if string(raw) == "null" {
		return nil, conversation.ErrInvalidInput
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || len(values) > 20 {
		return nil, conversation.ErrInvalidInput
	}
	result := make([]uuid.UUID, len(values))
	for index, value := range values {
		id, err := util.ParseGoUUID(value)
		if err != nil {
			return nil, conversation.ErrInvalidInput
		}
		result[index] = id
	}
	return result, nil
}

func parseMessageListOptions(rawQuery string) (conversation.MessageListOptions, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return conversation.MessageListOptions{}, conversation.ErrInvalidInput
	}
	for key, values := range query {
		if (key != "after_seq" && key != "through_seq" && key != "limit") || len(values) != 1 ||
			values[0] == "" {
			return conversation.MessageListOptions{}, conversation.ErrInvalidInput
		}
	}
	options := conversation.MessageListOptions{Limit: 50}
	if values, exists := query["after_seq"]; exists {
		value, err := parseCanonicalNonnegativeInt(values[0])
		if err != nil {
			return conversation.MessageListOptions{}, conversation.ErrInvalidInput
		}
		options.AfterSeq = value
	}
	if values, exists := query["through_seq"]; exists {
		value, err := parseCanonicalNonnegativeInt(values[0])
		if err != nil {
			return conversation.MessageListOptions{}, conversation.ErrInvalidInput
		}
		options.ThroughSeq = &value
	}
	if values, exists := query["limit"]; exists {
		value, err := parseCanonicalNonnegativeInt(values[0])
		if err != nil || value < 1 || value > 100 {
			return conversation.MessageListOptions{}, conversation.ErrInvalidInput
		}
		options.Limit = int(value)
	}
	if options.ThroughSeq != nil && *options.ThroughSeq < options.AfterSeq {
		return conversation.MessageListOptions{}, conversation.ErrInvalidInput
	}
	// A bound with no floor is a scroll-back: "the newest Limit messages at or
	// below through_seq". Reading that forward from zero would answer with the
	// oldest page instead, and a caller cannot compute the floor itself —
	// invisible run detail shares the sequence, so the seq range holding N
	// messages is not knowable until they are read.
	_, hasAfterSeq := query["after_seq"]
	options.FromNewest = options.ThroughSeq != nil && !hasAfterSeq
	return options, nil
}

func parseCanonicalNonnegativeInt(value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 || parsed > protocol.MaxJSONSafeInteger || strconv.FormatInt(parsed, 10) != value {
		return 0, conversation.ErrInvalidInput
	}
	return parsed, nil
}

var conversationErrorMappings = []errorMapping{
	{
		conversation.ErrMessageConnectorSelection,
		http.StatusConflict,
		"A selected plugin is unavailable for one or more tagged agents. Check your connections or change the selected agents/plugins.",
		"message_connector_unavailable",
	},
	{
		conversation.ErrMessageSkillSelection,
		http.StatusConflict,
		"A selected skill changed, is unavailable, or conflicts with another selected skill name. Select the skills again.",
		"message_skill_unavailable",
	},
	{
		conversation.ErrModelSettingsForbidden,
		http.StatusForbidden,
		"only the Task assignee with execution permission may change its chat model",
		"conversation_model_settings_forbidden",
	},
	{
		conversation.ErrUnauthenticated,
		http.StatusUnauthorized,
		"authentication is required",
		"authentication_required",
	},
	{
		conversation.ErrInvalidInput,
		http.StatusUnprocessableEntity,
		"request validation failed",
		"validation_failed",
	},
	{
		conversation.ErrConflict,
		http.StatusConflict,
		"Conversation mutation conflicts with current state",
		"conversation_conflict",
	},
	{
		conversation.ErrInitializationPending,
		http.StatusConflict,
		"This conversation is waiting for your company research to start. You can explore the workspace meanwhile.",
		"conversation_initialization_pending",
	},
	// The kernel's binding refusal passes through the conversation service
	// unwrapped in meaning: the sender has not set this Agent up on one of
	// their own runtimes (ADR 0014).
	{
		execution.ErrRunAgentNotBound,
		http.StatusConflict,
		"set this Agent up on one of your computers first",
		"agent_binding_required",
	},
	{
		conversation.ErrRunQueueConflict,
		http.StatusConflict,
		"Run queue conflicts with current state",
		"run_queue_conflict",
	},
	{
		conversation.ErrRunQueueFull,
		http.StatusConflict,
		"Run queue is full",
		"run_queue_full",
	},
	{conversation.ErrNotFound, http.StatusNotFound, "Conversation not found", "conversation_not_found"},
	// Posting into a Task chat needs project.tasks.write (ADR 0017).
	projectPermissionRequiredMapping,
}

func writeConversationError(c *gin.Context, err error) {
	// A Task chat message whose Run the assignee may not start (ADR 0017).
	if writeTaskExecuteRefusal(c, err) {
		return
	}
	writeMappedError(c, err, conversationErrorMappings, internalErrorFallback)
}
