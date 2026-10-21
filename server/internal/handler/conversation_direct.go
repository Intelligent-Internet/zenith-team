package handler

import (
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// The flat conversation wire shapes. A list row and a detail read name the
// same conversation fields at the top level rather than under a `conversation`
// key, so a client can write a detail response into a list row's cache slot,
// and an event that patches one patches the other. The run detail is counted
// rather than embedded — the runs themselves live on the runs endpoint.

// directConversationKind is the public Conversation vocabulary. Schedule
// Conversations are an internal materialization detail and have their own
// Schedule routes; widening the ordinary Conversation union would make them
// addressable through APIs that deliberately exclude them.
type directConversationKind string

// These name the whole union. Nothing in the package reads them by name — the
// response builders convert the domain kind straight across — but the OpenAPI
// response contract test collects them off the type (constantsForNamedType)
// and fails when a value here is missing from the documented `kind` enum.
//
//nolint:unused // consumed through go/types by the OpenAPI contract test, never by name
const (
	directConversationKindTask   directConversationKind = "task"
	directConversationKindDirect directConversationKind = "direct"
)

type directConversationResponse struct {
	ID              uuid.UUID              `json:"id"`
	CompanyID       uuid.UUID              `json:"companyId"`
	Kind            directConversationKind `json:"kind"`
	TaskID          *uuid.UUID             `json:"taskId"`
	AgentID         *uuid.UUID             `json:"agentId,omitempty"`
	CreatedByUserID *uuid.UUID             `json:"createdByUserId,omitempty"`
	Title           *string                `json:"title"`
	CreatedAt       time.Time              `json:"createdAt"`
	UpdatedAt       time.Time              `json:"updatedAt"`
	Revision        int64                  `json:"revision"`
}

func newDirectConversationResponse(value conversation.Response) directConversationResponse {
	return directConversationResponse{
		ID: value.ID, CompanyID: value.CompanyID, Kind: directConversationKind(value.Kind),
		TaskID: value.TaskID, AgentID: value.AgentID, CreatedByUserID: value.CreatedByUserID,
		Title: value.Title, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
		Revision: value.Revision,
	}
}

// conversationListItemResponse is one flat list row: the conversation's own
// fields, the caller's pinnedAt, and the shared activity `summary` beside
// them. `summary` is the same object the conversation.created and
// conversation.updated events carry, so a frame is written into this slot
// unchanged. Archive state is included for callers browsing saved chats.
type conversationListItemResponse struct {
	ID         uuid.UUID                `json:"id"`
	CompanyID  uuid.UUID                `json:"companyId"`
	Kind       directConversationKind   `json:"kind"`
	TaskID     *uuid.UUID               `json:"taskId"`
	Agent      *conversation.AgentRef   `json:"agent"`
	Title      *string                  `json:"title"`
	ArchivedAt *time.Time               `json:"archivedAt"`
	PinnedAt   *time.Time               `json:"pinnedAt"`
	Summary    conversation.ListSummary `json:"summary"`
	CreatedAt  time.Time                `json:"createdAt"`
	UpdatedAt  time.Time                `json:"updatedAt"`
	Revision   int64                    `json:"revision"`
}

func newConversationListItemResponse(item conversation.ListItem) conversationListItemResponse {
	return conversationListItemResponse{
		ID: item.Conversation.ID, CompanyID: item.Conversation.CompanyID,
		Kind: directConversationKind(item.Conversation.Kind), TaskID: item.Conversation.TaskID,
		Agent: item.Agent, Title: item.Conversation.Title,
		PinnedAt:   item.UserState.PinnedAt,
		ArchivedAt: item.UserState.ArchivedAt,
		Summary:    item.Summary,
		CreatedAt:  item.Conversation.CreatedAt, UpdatedAt: item.Conversation.UpdatedAt,
		Revision: item.Conversation.Revision,
	}
}

type conversationListResponse struct {
	Items []conversationListItemResponse `json:"items"`
}

// messageWithAttachmentsResponse is a message with its attachments nested
// inside it: the message fields flat at the top, its ready attachments beside
// them, so a client never joins two arrays by id.
type messageWithAttachmentsResponse struct {
	conversation.MessageResponse
	Attachments         []attachment.Response `json:"attachments"`
	AttachmentsWithheld bool                  `json:"attachmentsWithheld"`
}

func messageWithAttachmentsResponses(
	views []conversation.MessageView,
) []messageWithAttachmentsResponse {
	out := make([]messageWithAttachmentsResponse, len(views))
	for index, view := range views {
		attachments := view.Attachments
		if attachments == nil {
			attachments = []attachment.Response{}
		}
		out[index] = messageWithAttachmentsResponse{
			MessageResponse: view.Message, Attachments: attachments,
			AttachmentsWithheld: view.AttachmentsWithheld,
		}
	}
	return out
}

// conversationDetailResponse is the combined read: one flat conversation, a
// bounded window of its messages, and the panel's co-resident state. userState,
// runs and runQueue stay nested because each is a separable projection with its
// own endpoint, so a refresh of one writes into the slot this read filled.
// Whether the window missed older or newer rows is derived from the window's
// own seq range against the bounds, never carried.
type conversationDetailResponse struct {
	directConversationResponse
	Agent                *conversation.AgentRef           `json:"agent"`
	Messages             []messageWithAttachmentsResponse `json:"messages"`
	EarliestAvailableSeq int64                            `json:"earliestAvailableSeq"`
	ThroughSeq           int64                            `json:"throughSeq"`
	ResetRequired        bool                             `json:"resetRequired"`
	UserState            conversation.UserState           `json:"userState"`
	Runs                 conversation.RunsSummary         `json:"runs"`
	RunQueue             conversation.RunQueueProjection  `json:"runQueue"`
}

func newConversationDetailResponse(detail conversation.DirectDetail) conversationDetailResponse {
	return conversationDetailResponse{
		directConversationResponse: newDirectConversationResponse(detail.Conversation),
		Agent:                      detail.Agent,
		Messages:                   messageWithAttachmentsResponses(detail.Messages),
		EarliestAvailableSeq:       detail.EarliestAvailableSeq,
		ThroughSeq:                 detail.ThroughSeq,
		ResetRequired:              detail.ResetRequired,
		UserState:                  detail.UserState,
		Runs:                       detail.Runs,
		RunQueue:                   detail.RunQueue,
	}
}

func (h *Handler) listCompanyConversations(c *gin.Context) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		writeConversationError(c, conversation.ErrInvalidInput)
		return
	}
	for key, values := range query {
		if key != "include_archived" || len(values) != 1 || (values[0] != "true" && values[0] != "false") {
			writeConversationError(c, conversation.ErrInvalidInput)
			return
		}
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeConversationError(c, conversation.ErrUnauthenticated)
		return
	}
	page, err := h.ConversationService.ListDirect(
		c.Request.Context(),
		principal,
		query.Get("include_archived") == "true",
	)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	items := make([]conversationListItemResponse, len(page.Conversations))
	for index, item := range page.Conversations {
		items[index] = newConversationListItemResponse(item)
	}
	c.JSON(http.StatusOK, conversationListResponse{Items: items})
}

func (h *Handler) createCompanyConversation(c *gin.Context) {
	request, err := bindRequest[createDirectConversationRequest](c, maxConversationBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	agentID, err := util.ParseGoUUID(request.AgentID)
	if err != nil {
		writeConversationError(c, conversation.ErrInvalidInput)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeConversationError(c, conversation.ErrUnauthenticated)
		return
	}
	var requestKey *uuid.UUID
	if raw := request.RequestKey.ptr(); raw != nil {
		parsed, parseErr := util.ParseGoUUID(*raw)
		if parseErr != nil {
			writeConversationError(c, conversation.ErrInvalidInput)
			return
		}
		requestKey = &parsed
	}

	result, err := h.ConversationService.CreateDirect(
		c.Request.Context(), principal,
		conversation.CreateDirectInput{AgentID: agentID, Title: request.Title.ptr(), RequestKey: requestKey},
	)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, newDirectConversationResponse(result.Conversation))
}

func (h *Handler) renameCompanyConversation(c *gin.Context) {
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
	request, err := bindRequest[renameConversationRequest](c, maxConversationBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	title := protocol.TrimContractWhitespace(request.Title)
	if title == "" {
		writeConversationError(c, conversation.ErrInvalidInput)
		return
	}
	result, err := h.ConversationService.RenameDirect(
		c.Request.Context(),
		principal,
		conversationID,
		conversation.RenameDirectInput{Title: title, ExpectedRevision: request.ExpectedRevision},
	)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	c.JSON(http.StatusOK, newDirectConversationResponse(result))
}

func (h *Handler) getCompanyConversationDetail(c *gin.Context) {
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
	if !h.runOwnReadAdmitted(c, principal, runOwnsConversation(conversationID)) {
		return
	}
	window, err := parseConversationDetailWindow(c.Request.URL.RawQuery)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	detail, err := h.ConversationService.FetchDirectDetail(
		c.Request.Context(), principal, conversationID, window,
	)
	if err != nil {
		writeConversationError(c, err)
		return
	}
	c.JSON(http.StatusOK, newConversationDetailResponse(detail))
}

// parseConversationDetailWindow reads the message-window controls off the
// detail read's query string. Absent, the window is the newest 50 messages.
func parseConversationDetailWindow(rawQuery string) (conversation.MessageWindow, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return conversation.MessageWindow{}, conversation.ErrInvalidInput
	}
	for key, values := range query {
		if (key != "after_seq" && key != "through_seq" && key != "limit") ||
			len(values) != 1 || values[0] == "" {
			return conversation.MessageWindow{}, conversation.ErrInvalidInput
		}
	}
	window := conversation.MessageWindow{Limit: 50}
	if values, exists := query["limit"]; exists {
		value, err := parseCanonicalNonnegativeInt(values[0])
		if err != nil || value < 1 || value > 100 {
			return conversation.MessageWindow{}, conversation.ErrInvalidInput
		}
		window.Limit = int(value)
	}
	if values, exists := query["after_seq"]; exists {
		value, err := parseCanonicalNonnegativeInt(values[0])
		if err != nil {
			return conversation.MessageWindow{}, conversation.ErrInvalidInput
		}
		window.AfterSeq = &value
	}
	if values, exists := query["through_seq"]; exists {
		value, err := parseCanonicalNonnegativeInt(values[0])
		if err != nil {
			return conversation.MessageWindow{}, conversation.ErrInvalidInput
		}
		window.ThroughSeq = &value
	}
	return window, nil
}
