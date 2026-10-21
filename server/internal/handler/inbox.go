package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

var errInvalidInboxCursor = errors.New("invalid Inbox cursor")

type InboxService interface {
	List(context.Context, auth.UserPrincipal, inbox.ListOptions) (inbox.Page, error)
	Counts(context.Context, auth.UserPrincipal) (inbox.Counts, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (inbox.Item, error)
	Mutate(context.Context, auth.UserPrincipal, uuid.UUID, inbox.Mutation, int64) (inbox.MutationResult, error)
	ReadAll(context.Context, auth.UserPrincipal) (inbox.ReadAllResult, error)
	ReadConversation(context.Context, auth.UserPrincipal, uuid.UUID) (inbox.ReadAllResult, error)
}

type inboxPageResponse struct {
	Items      []inbox.Item `json:"items"`
	NextCursor *string      `json:"nextCursor"`
}

type inboxMutationRequest struct {
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}

type inboxMutationResponse struct {
	inbox.Item
	Counts inbox.Counts `json:"counts"`
}

// The conversation id is validated as a uuid string rather than decoded as
// a typed field: a typed uuid.UUID fails inside the JSON decoder, which the
// boundary answers as 400 invalid_request, whereas the contract makes a
// non-uuid conversationId a field rule — 422 validation_failed, like a
// missing one.
type inboxConversationReadRequest struct {
	ConversationID string `json:"conversationId" binding:"required,uuid"`
}

func (h *Handler) registerInboxRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil ||
		h.InboxService == nil {
		return
	}
	routes := group.Group(
		"/companies/:companyId/inbox",
		middleware.NoStore(),
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	routes.GET("", h.listInbox)
	routes.GET("/counts", h.inboxCounts)
	routes.POST("/read-all", h.readAllInbox)
	routes.POST("/read", h.readConversationInbox)
	routes.GET("/:itemId", h.getInboxItem)
	routes.POST("/:itemId/:mutation", h.mutateInboxItem)
	account := group.Group(
		"/me/inbox",
		middleware.NoStore(),
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireHumanActor(),
	)
	account.GET("", h.listInbox)
	account.GET("/counts", h.inboxCounts)
	account.POST("/read-all", h.readAllInbox)
	account.GET("/:itemId", h.getInboxItem)
	account.POST("/:itemId/:mutation", h.mutateInboxItem)
}

func (h *Handler) listInbox(c *gin.Context) {
	options, err := parseInboxListOptions(c)
	if err != nil {
		if errors.Is(err, errInvalidInboxCursor) {
			writeBoundaryError(c, boundaryErrorInvalidCursor)
		} else {
			writeInboxError(c, inbox.ErrInvalidInput)
		}
		return
	}
	principal, ok := inboxPrincipal(c)
	if !ok {
		return
	}
	page, err := h.InboxService.List(c.Request.Context(), principal, options)
	if err != nil {
		writeInboxError(c, err)
		return
	}
	if page.Items == nil {
		page.Items = []inbox.Item{}
	}
	response := inboxPageResponse{Items: page.Items}
	if page.NextCursor != nil {
		cursor := encodeFeedCursor(page.NextCursor.CreatedAt, page.NextCursor.ID)
		response.NextCursor = &cursor
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) inboxCounts(c *gin.Context) {
	if inboxQueryRejected(c) {
		return
	}
	principal, ok := inboxPrincipal(c)
	if !ok {
		return
	}
	counts, err := h.InboxService.Counts(c.Request.Context(), principal)
	if err != nil {
		writeInboxError(c, err)
		return
	}
	c.JSON(http.StatusOK, counts)
}

func (h *Handler) getInboxItem(c *gin.Context) {
	if inboxQueryRejected(c) {
		return
	}
	principal, itemID, ok := inboxRequestIdentity(c)
	if !ok {
		return
	}
	item, err := h.InboxService.Get(c.Request.Context(), principal, itemID)
	if err != nil {
		writeInboxError(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}

func (h *Handler) mutateInboxItem(c *gin.Context) {
	principal, itemID, ok := inboxRequestIdentity(c)
	if !ok {
		return
	}
	mutation := inbox.Mutation(c.Param("mutation"))
	if mutation != inbox.MutationRead && mutation != inbox.MutationUnread &&
		mutation != inbox.MutationArchive && mutation != inbox.MutationRestore {
		writeInboxError(c, inbox.ErrInvalidInput)
		return
	}
	request, err := bindRequest[inboxMutationRequest](c, 4<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.InboxService.Mutate(
		c.Request.Context(), principal, itemID, mutation, request.ExpectedRevision,
	)
	if err != nil {
		writeInboxError(c, err)
		return
	}
	c.JSON(http.StatusOK, inboxMutationResponse{Item: result.Item, Counts: result.Counts})
}

func (h *Handler) readAllInbox(c *gin.Context) {
	if inboxQueryRejected(c) {
		return
	}
	principal, ok := inboxPrincipal(c)
	if !ok {
		return
	}
	result, err := h.InboxService.ReadAll(c.Request.Context(), principal)
	if err != nil {
		writeInboxError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) readConversationInbox(c *gin.Context) {
	if inboxQueryRejected(c) {
		return
	}
	request, err := bindRequest[inboxConversationReadRequest](c, 4<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := inboxPrincipal(c)
	if !ok {
		return
	}
	// The binding rule checks the spelling only: the nil uuid passes it and
	// names no Conversation, so it is refused here as the same 422.
	conversationID, err := util.ParseGoUUID(request.ConversationID)
	if err != nil {
		writeInboxError(c, inbox.ErrInvalidInput)
		return
	}
	result, err := h.InboxService.ReadConversation(c.Request.Context(), principal, conversationID)
	if err != nil {
		writeInboxError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// inboxQueryRejected answers 422 validation_failed for a query string on a
// route whose contract accepts none, and reports whether it did. Counts, the
// single-item read, read-all and read-conversation all say so in
// docs/api/inbox.md; the item mutation lists no such 422, so it stays out
// until its contract does.
func inboxQueryRejected(c *gin.Context) bool {
	if c.Request.URL.RawQuery == "" {
		return false
	}
	writeInboxError(c, inbox.ErrInvalidInput)
	return true
}

func inboxPrincipal(c *gin.Context) (auth.UserPrincipal, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeInboxError(c, inbox.ErrUnauthenticated)
		return auth.UserPrincipal{}, false
	}
	return principal, true
}

func inboxRequestIdentity(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := inboxPrincipal(c)
	if !ok {
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	// A malformed item id cannot name a row, which is indistinguishable from
	// naming nothing: 404 inbox_item_not_found, never 422.
	itemID, err := util.ParseGoUUID(c.Param("itemId"))
	if err != nil {
		writeInboxError(c, inbox.ErrNotFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, itemID, true
}

func parseInboxListOptions(c *gin.Context) (inbox.ListOptions, error) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return inbox.ListOptions{}, inbox.ErrInvalidInput
	}
	for key, values := range query {
		if (key != "status" && key != "kind" && key != "cursor" && key != "limit") ||
			len(values) != 1 || values[0] == "" {
			return inbox.ListOptions{}, inbox.ErrInvalidInput
		}
	}
	options := inbox.ListOptions{Status: inbox.StatusActive, Limit: 50}
	if values, exists := query["status"]; exists {
		options.Status = inbox.Status(values[0])
		if options.Status != inbox.StatusActive && options.Status != inbox.StatusAll &&
			options.Status != inbox.StatusUnread && options.Status != inbox.StatusArchived {
			return inbox.ListOptions{}, inbox.ErrInvalidInput
		}
	}
	if values, exists := query["kind"]; exists {
		// The filter vocabulary is the item vocabulary: any kind an item can
		// carry is filterable. The service validates against that vocabulary.
		options.Type = inbox.Type(values[0])
	}
	if values, exists := query["limit"]; exists {
		limit, err := strconv.Atoi(values[0])
		if err != nil || strconv.Itoa(limit) != values[0] || limit < 1 || limit > 100 {
			return inbox.ListOptions{}, inbox.ErrInvalidInput
		}
		options.Limit = limit
	}
	if values, exists := query["cursor"]; exists {
		createdAt, id, ok := decodeFeedCursor(values[0])
		if !ok {
			return inbox.ListOptions{}, errInvalidInboxCursor
		}
		options.Before = &inbox.Cursor{CreatedAt: createdAt, ID: id}
	}
	return options, nil
}

var inboxErrorMappings = []errorMapping{
	{inbox.ErrUnauthenticated, http.StatusUnauthorized, "authentication is required", "authentication_required"},
	{inbox.ErrInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
	{inbox.ErrNotFound, http.StatusNotFound, "Inbox item was not found", "inbox_item_not_found"},
	{inbox.ErrConversationNotFound, http.StatusNotFound, "conversation was not found", "conversation_not_found"},
	{
		inbox.ErrConflict,
		http.StatusConflict,
		"Inbox item changed; refresh and retry",
		"inbox_item_revision_conflict",
	},
}

func writeInboxError(c *gin.Context, err error) {
	writeMappedError(c, err, inboxErrorMappings, internalErrorFallback)
}
