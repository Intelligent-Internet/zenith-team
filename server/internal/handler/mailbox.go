package handler

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/infra/email"
	"github.com/Intelligent-Internet/zenith-team/server/internal/mailbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

const (
	maxMailboxBodyBytes    = 4 << 10
	maxMailboxWebhookBytes = 1 << 20
	mailboxCursorSize      = 25
	mailboxCursorV1        = byte(1)
)

var errInvalidMailboxCursor = errors.New("invalid Mailbox cursor")

type MailboxService interface {
	GetState(context.Context, auth.UserPrincipal) (mailbox.State, error)
	Create(context.Context, auth.UserPrincipal, string) (mailbox.State, error)
	List(context.Context, auth.UserPrincipal, mailbox.ListOptions) (mailbox.Page, error)
	GetDetail(context.Context, auth.UserPrincipal, uuid.UUID) (mailbox.Detail, error)
	MarkRead(context.Context, auth.UserPrincipal, uuid.UUID) (mailbox.Message, error)
	GetAttachment(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
	) (mailbox.AttachmentDownload, error)
	Ingest(context.Context, email.ReceivedEvent) (mailbox.IngestResult, error)
}

type MailboxWebhookVerifier interface {
	Verify([]byte, email.WebhookHeaders) error
}

type MailboxWebhookObserver interface {
	ObserveMailboxWebhook(string)
}

type mailboxCreateRequest struct {
	LocalPart string `json:"localPart" binding:"required,max=64"`
}

type mailboxPageResponse struct {
	Messages   []mailbox.Message `json:"messages"`
	NextCursor *string           `json:"nextCursor"`
}

type mailboxWebhookResponse struct {
	Received bool `json:"received"`
}

func (h *Handler) registerMailboxRoutes(group *gin.RouterGroup) {
	if h.MailboxService != nil && h.MailboxWebhookVerifier != nil {
		group.POST("/webhooks/resend/mailbox", h.receiveMailboxWebhook)
	}
	if h.AuthService == nil || h.CompanyService == nil {
		return
	}
	routes := group.Group(
		"/companies/:companyId/mailbox",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionMailboxManage),
	)
	routes.GET("", h.getMailboxState)
	if h.MailboxService == nil {
		return
	}
	routes.GET("/messages", h.listMailboxMessages)
	routes.GET("/messages/:messageId", h.getMailboxMessage)
	routes.POST("/messages/:messageId/read", h.markMailboxMessageRead)
	routes.GET(
		"/messages/:messageId/attachments/:attachmentId",
		h.getMailboxAttachment,
	)
	routes.POST("", h.createMailbox)
	h.registerMailboxDraftRoutes(group)
}

func (h *Handler) getMailboxState(c *gin.Context) {
	setMailboxPrivateResponse(c)
	principal, ok := mailboxPrincipal(c)
	if !ok {
		return
	}
	if h.MailboxService == nil {
		c.JSON(http.StatusOK, mailbox.State{Status: mailbox.StatusDisabled})
		return
	}
	state, err := h.MailboxService.GetState(c.Request.Context(), principal)
	if err != nil {
		writeMailboxError(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

func (h *Handler) createMailbox(c *gin.Context) {
	setMailboxPrivateResponse(c)
	principal, ok := mailboxPrincipal(c)
	if !ok {
		return
	}
	request, err := bindRequest[mailboxCreateRequest](c, maxMailboxBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	state, err := h.MailboxService.Create(c.Request.Context(), principal, request.LocalPart)
	if err != nil {
		writeMailboxError(c, err)
		return
	}
	c.JSON(http.StatusCreated, state)
}

func (h *Handler) listMailboxMessages(c *gin.Context) {
	setMailboxPrivateResponse(c)
	principal, ok := mailboxPrincipal(c)
	if !ok {
		return
	}
	options, err := parseMailboxListOptions(c)
	if err != nil {
		if errors.Is(err, errInvalidMailboxCursor) {
			writeBoundaryError(c, boundaryErrorInvalidCursor)
		} else {
			writeMailboxError(c, mailbox.ErrInvalidInput)
		}
		return
	}
	page, err := h.MailboxService.List(c.Request.Context(), principal, options)
	if err != nil {
		writeMailboxError(c, err)
		return
	}
	if page.Messages == nil {
		page.Messages = []mailbox.Message{}
	}
	response := mailboxPageResponse{Messages: page.Messages}
	if page.NextCursor != nil {
		cursor, err := encodeMailboxCursor(*page.NextCursor)
		if err != nil {
			writeMailboxError(c, err)
			return
		}
		response.NextCursor = &cursor
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) getMailboxMessage(c *gin.Context) {
	setMailboxPrivateResponse(c)
	principal, messageID, ok := mailboxMessageIdentity(c)
	if !ok {
		return
	}
	detail, err := h.MailboxService.GetDetail(c.Request.Context(), principal, messageID)
	if err != nil {
		writeMailboxError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (h *Handler) markMailboxMessageRead(c *gin.Context) {
	setMailboxPrivateResponse(c)
	if c.Request.ContentLength != 0 {
		if _, err := bindRequest[struct{}](c, maxMailboxBodyBytes); err != nil {
			writeRequestError(c, err)
			return
		}
	}
	principal, messageID, ok := mailboxMessageIdentity(c)
	if !ok {
		return
	}
	message, err := h.MailboxService.MarkRead(c.Request.Context(), principal, messageID)
	if err != nil {
		writeMailboxError(c, err)
		return
	}
	c.JSON(http.StatusOK, message)
}

func (h *Handler) getMailboxAttachment(c *gin.Context) {
	setMailboxPrivateResponse(c)
	principal, messageID, ok := mailboxMessageIdentity(c)
	if !ok {
		return
	}
	attachmentID, err := util.ParseGoUUID(c.Param("attachmentId"))
	if err != nil {
		writeMailboxError(c, mailbox.ErrInvalidInput)
		return
	}
	download, err := h.MailboxService.GetAttachment(
		c.Request.Context(), principal, messageID, attachmentID,
	)
	if err != nil {
		writeMailboxError(c, err)
		return
	}
	c.JSON(http.StatusOK, download)
}

func (h *Handler) receiveMailboxWebhook(c *gin.Context) {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxMailboxWebhookBytes))
	if err != nil || len(body) == 0 {
		h.observeInvalidMailboxWebhook()
		writeBoundaryError(c, boundaryErrorMailboxWebhookInvalid)
		return
	}
	headers := email.WebhookHeaders{
		ID: c.GetHeader("svix-id"), Timestamp: c.GetHeader("svix-timestamp"),
		Signature: c.GetHeader("svix-signature"),
	}
	if err := h.MailboxWebhookVerifier.Verify(body, headers); err != nil {
		h.observeInvalidMailboxWebhook()
		writeBoundaryError(c, boundaryErrorMailboxWebhookInvalid)
		return
	}
	event, err := email.DecodeResendReceivedEvent(body, headers.ID)
	if err != nil {
		h.observeInvalidMailboxWebhook()
		writeBoundaryError(c, boundaryErrorMailboxWebhookInvalid)
		return
	}
	if _, err := h.MailboxService.Ingest(c.Request.Context(), event); err != nil {
		if errors.Is(err, mailbox.ErrInvalidInput) {
			writeBoundaryError(c, boundaryErrorMailboxWebhookInvalid)
			return
		}
		writeBoundaryError(c, boundaryErrorMailboxWebhookFailed)
		return
	}
	c.JSON(http.StatusOK, mailboxWebhookResponse{Received: true})
}

func (h *Handler) observeInvalidMailboxWebhook() {
	if h.MailboxWebhookObserver != nil {
		h.MailboxWebhookObserver.ObserveMailboxWebhook("invalid")
	}
}

func mailboxPrincipal(c *gin.Context) (auth.UserPrincipal, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeMailboxError(c, mailbox.ErrUnauthenticated)
		return auth.UserPrincipal{}, false
	}
	return principal, true
}

func mailboxMessageIdentity(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := mailboxPrincipal(c)
	if !ok {
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	messageID, err := util.ParseGoUUID(c.Param("messageId"))
	if err != nil {
		writeMailboxError(c, mailbox.ErrInvalidInput)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, messageID, true
}

func parseMailboxListOptions(c *gin.Context) (mailbox.ListOptions, error) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return mailbox.ListOptions{}, mailbox.ErrInvalidInput
	}
	for key, values := range query {
		if (key != "cursor" && key != "limit") || len(values) != 1 || values[0] == "" {
			return mailbox.ListOptions{}, mailbox.ErrInvalidInput
		}
	}
	options := mailbox.ListOptions{Limit: 50}
	if values, exists := query["limit"]; exists {
		limit, err := strconv.Atoi(values[0])
		if err != nil || strconv.Itoa(limit) != values[0] || limit < 1 || limit > 100 {
			return mailbox.ListOptions{}, mailbox.ErrInvalidInput
		}
		options.Limit = limit
	}
	if values, exists := query["cursor"]; exists {
		cursor, err := decodeMailboxCursor(values[0])
		if err != nil {
			return mailbox.ListOptions{}, err
		}
		options.Before = &cursor
	}
	return options, nil
}

func encodeMailboxCursor(cursor mailbox.Cursor) (string, error) {
	if cursor.ReceivedAt.IsZero() || cursor.ReceivedAt.UnixMicro() <= 0 ||
		!util.ValidUUID(cursor.ID) {
		return "", mailbox.ErrInvalidInput
	}
	payload := make([]byte, mailboxCursorSize)
	payload[0] = mailboxCursorV1
	binary.BigEndian.PutUint64(payload[1:9], uint64(cursor.ReceivedAt.UnixMicro()))
	copy(payload[9:], cursor.ID[:])
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeMailboxCursor(value string) (mailbox.Cursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(payload) != mailboxCursorSize || payload[0] != mailboxCursorV1 {
		return mailbox.Cursor{}, errInvalidMailboxCursor
	}
	micros := binary.BigEndian.Uint64(payload[1:9])
	if micros == 0 || micros > 1<<63-1 {
		return mailbox.Cursor{}, errInvalidMailboxCursor
	}
	id, err := uuid.FromBytes(payload[9:])
	if err != nil || !util.ValidUUID(id) {
		return mailbox.Cursor{}, errInvalidMailboxCursor
	}
	return mailbox.Cursor{ReceivedAt: time.UnixMicro(int64(micros)).UTC(), ID: id}, nil
}

func setMailboxPrivateResponse(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
}

var mailboxErrorMappings = []errorMapping{
	runTokenInvalidMapping,
	{
		mailbox.ErrAgentMailCompanyEmailRequired,
		http.StatusConflict,
		"Company Email must be configured before creating drafts",
		"agent_mail_company_email_required",
	},
	{
		mailbox.ErrAgentMailForbidden,
		http.StatusForbidden,
		"Agent Mail access is forbidden",
		"agent_mail_forbidden",
	},
	{
		mailbox.ErrAgentMailDraftNotFound,
		http.StatusNotFound,
		"Agent Mail draft was not found",
		"agent_mail_draft_not_found",
	},
	// Changing or sending a Task chat's drafts needs project.tasks.write
	// (ADR 0017).
	projectPermissionRequiredMapping,
	{
		mailbox.ErrAgentMailDraftConflict,
		http.StatusConflict,
		"Agent Mail draft changed; refresh and retry",
		"agent_mail_draft_conflict",
	},
	{
		mailbox.ErrAgentMailInvalidRecipients,
		http.StatusUnprocessableEntity,
		"Agent Mail recipients are invalid",
		"agent_mail_invalid_recipients",
	},
	{
		mailbox.ErrAgentMailSendLimitExceeded,
		http.StatusTooManyRequests,
		"Agent Mail recipient limit was exceeded",
		"agent_mail_send_limit_exceeded",
	},
	{
		mailbox.ErrAgentMailInvalidInput,
		http.StatusUnprocessableEntity,
		"request validation failed",
		"validation_failed",
	},
	{mailbox.ErrUnauthenticated, http.StatusUnauthorized, "authentication is required", "authentication_required"},
	{
		mailbox.ErrForbidden,
		http.StatusForbidden,
		"Your Company role does not allow this action",
		"company_permission_required",
	},
	{mailbox.ErrInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
	{
		mailbox.ErrAlreadyConfigured,
		http.StatusConflict,
		"Company mailbox is already configured",
		"mailbox_already_configured",
	},
	{
		mailbox.ErrAddressUnavailable,
		http.StatusConflict,
		"Mailbox address is unavailable",
		"mailbox_address_unavailable",
	},
	{mailbox.ErrMessageNotFound, http.StatusNotFound, "Mailbox message was not found", "mailbox_message_not_found"},
	{mailbox.ErrMessageExpired, http.StatusGone, "Mailbox message has expired", "mailbox_message_expired"},
	{
		mailbox.ErrProviderRateLimited,
		http.StatusTooManyRequests,
		"Mailbox provider is rate limited",
		"mailbox_provider_rate_limited",
	},
	{
		mailbox.ErrProviderUnavailable,
		http.StatusServiceUnavailable,
		"Mailbox provider is unavailable",
		"mailbox_provider_unavailable",
	},
}

func writeMailboxError(c *gin.Context, err error) {
	var rateLimitError *mailbox.ProviderRateLimitError
	if errors.As(err, &rateLimitError) {
		seconds := int64((rateLimitError.RetryAfter + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		if seconds > 3600 {
			seconds = 3600
		}
		c.Header("Retry-After", strconv.FormatInt(seconds, 10))
	}
	writeMappedError(c, err, mailboxErrorMappings, internalErrorFallback)
}
