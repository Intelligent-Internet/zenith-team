package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/mailbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

const (
	maxMailboxDraftBodyBytes    = 8 << 20
	maxMailboxDraftSourceRunIDs = 100
)

type MailboxDraftService interface {
	Create(
		context.Context,
		auth.UserPrincipal,
		mailbox.TrustedDraftContext,
		[]mailbox.DraftInput,
	) (mailbox.DraftCreateResult, error)
	ListGroups(
		context.Context, auth.UserPrincipal, uuid.UUID, []uuid.UUID,
	) ([]mailbox.DraftGroup, error)
	UpdateDraft(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		mailbox.UpdateDraftInput,
	) (mailbox.Draft, error)
	DeleteDraft(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int64) error
	QueueDraft(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		int64,
	) (mailbox.Draft, error)
	QueueGroup(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		[]mailbox.DraftRevision,
	) (mailbox.DraftGroup, error)
}

type createMailboxDraftGroupRequest struct {
	Drafts []mailbox.DraftInput `json:"drafts" binding:"required,min=1,max=100,dive"`
}

type updateMailboxDraftRequest struct {
	Subject          string                       `json:"subject"          binding:"required,max=200"`
	Text             string                       `json:"text"             binding:"required,max=65536"`
	Deliveries       []mailbox.DraftDeliveryInput `json:"deliveries"       binding:"required,min=1,max=100,dive"`
	ExpectedRevision int64                        `json:"expectedRevision" binding:"revision"`
}

type mailboxDraftRevisionRequest struct {
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}

type mailboxDraftVersionRequest struct {
	ID               uuid.UUID `json:"id"               binding:"required"`
	ExpectedRevision int64     `json:"expectedRevision" binding:"revision"`
}

type queueMailboxDraftGroupRequest struct {
	Drafts []mailboxDraftVersionRequest `json:"drafts" binding:"required,min=1,max=100,dive"`
}

type mailboxDraftGroupsResponse struct {
	Groups []mailbox.DraftGroup `json:"groups"`
}

func (h *Handler) registerMailboxDraftRoutes(group *gin.RouterGroup) {
	if h.MailboxDraftService == nil || h.AuthService == nil || h.CompanyService == nil {
		return
	}
	routes := group.Group(
		"/companies/:companyId/conversations/:conversationId",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionMailboxManage),
	)
	routes.GET("/mail-draft-groups", h.listMailboxDraftGroups)
	routes.POST("/mail-draft-groups", h.createMailboxDraftGroup)
	routes.POST("/mail-draft-groups/:groupId/send", h.queueMailboxDraftGroup)
	routes.PUT("/mail-drafts/:draftId", h.updateMailboxDraft)
	routes.DELETE("/mail-drafts/:draftId", h.deleteMailboxDraft)
	routes.POST("/mail-drafts/:draftId/send", h.queueMailboxDraft)
}

func (h *Handler) createMailboxDraftGroup(c *gin.Context) {
	setMailboxPrivateResponse(c)
	principal, conversationID, ok := mailboxDraftConversationIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[createMailboxDraftGroupRequest](c, maxMailboxDraftBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	for _, draft := range request.Drafts {
		if rejectInlineUI(c, storedText{"Email subject", draft.Subject}, storedText{"Email body", draft.Text}) {
			return
		}
	}
	origin, requestKey, ok := h.resolveAgentMailCreate(c, principal, conversationID)
	if !ok {
		return
	}
	result, err := h.MailboxDraftService.Create(
		c.Request.Context(), principal,
		mailbox.TrustedDraftContext{
			CompanyID: principal.CompanyID, ConversationID: conversationID,
			RunID: origin.RunID, AgentID: origin.AgentID, RequestKey: requestKey,
		},
		request.Drafts,
	)
	if err != nil {
		writeMailboxError(c, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, result.DraftGroup)
}

func (h *Handler) resolveAgentMailCreate(
	c *gin.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
) (execution.RunOrigin, uuid.UUID, bool) {
	requestKey, err := util.ParseGoUUID(c.GetHeader(idempotencyHeader))
	if err != nil {
		writeMailboxError(c, mailbox.ErrAgentMailInvalidInput)
		return execution.RunOrigin{}, uuid.Nil, false
	}
	if h.RunProofResolver == nil {
		writeMailboxError(c, errors.New("agent mail run proof resolver is not configured"))
		return execution.RunOrigin{}, uuid.Nil, false
	}
	token := c.GetHeader(runTokenHeader)
	if token == "" {
		writeMailboxError(c, execution.ErrRunProofInvalid)
		return execution.RunOrigin{}, uuid.Nil, false
	}
	origin, err := h.resolveRunOrigin(c, token)
	if err != nil {
		writeMailboxError(c, err)
		return execution.RunOrigin{}, uuid.Nil, false
	}
	if !runProofBearer(principal, origin) || origin.ConversationID != conversationID ||
		origin.AgentID == uuid.Nil || origin.RunID == uuid.Nil ||
		(origin.ConversationKind != "direct" && origin.ConversationKind != "task") {
		writeMailboxError(c, execution.ErrRunProofInvalid)
		return execution.RunOrigin{}, uuid.Nil, false
	}
	return origin, requestKey, true
}

func (h *Handler) listMailboxDraftGroups(c *gin.Context) {
	setMailboxPrivateResponse(c)
	principal, conversationID, ok := mailboxDraftConversationIdentity(c)
	if !ok {
		return
	}
	sourceRunIDs, ok := mailboxDraftSourceRunIDs(c)
	if !ok {
		return
	}
	groups, err := h.MailboxDraftService.ListGroups(
		c.Request.Context(), principal, conversationID, sourceRunIDs,
	)
	if err != nil {
		writeMailboxError(c, err)
		return
	}
	if groups == nil {
		groups = []mailbox.DraftGroup{}
	}
	c.JSON(http.StatusOK, mailboxDraftGroupsResponse{Groups: groups})
}

func mailboxDraftSourceRunIDs(c *gin.Context) ([]uuid.UUID, bool) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	values, exists := query["source_run_ids"]
	if err != nil || len(query) != 1 || !exists || len(values) != 1 || values[0] == "" {
		writeMailboxError(c, mailbox.ErrAgentMailInvalidInput)
		return nil, false
	}
	rawIDs := strings.Split(values[0], ",")
	if len(rawIDs) < 1 || len(rawIDs) > maxMailboxDraftSourceRunIDs {
		writeMailboxError(c, mailbox.ErrAgentMailInvalidInput)
		return nil, false
	}
	result := make([]uuid.UUID, len(rawIDs))
	seen := make(map[uuid.UUID]struct{}, len(rawIDs))
	for index, rawID := range rawIDs {
		id, err := util.ParseGoUUID(rawID)
		if err != nil {
			writeMailboxError(c, mailbox.ErrAgentMailInvalidInput)
			return nil, false
		}
		if _, duplicate := seen[id]; duplicate {
			writeMailboxError(c, mailbox.ErrAgentMailInvalidInput)
			return nil, false
		}
		seen[id] = struct{}{}
		result[index] = id
	}
	return result, true
}

func (h *Handler) updateMailboxDraft(c *gin.Context) {
	setMailboxPrivateResponse(c)
	principal, conversationID, draftID, ok := mailboxDraftIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[updateMailboxDraftRequest](c, maxMailboxDraftBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	draft, err := h.MailboxDraftService.UpdateDraft(
		c.Request.Context(), principal, conversationID, draftID,
		mailbox.UpdateDraftInput{
			Subject: request.Subject, Text: request.Text, Deliveries: request.Deliveries,
			ExpectedRevision: request.ExpectedRevision,
		},
	)
	if err != nil {
		writeMailboxError(c, err)
		return
	}
	c.JSON(http.StatusOK, draft)
}

func (h *Handler) deleteMailboxDraft(c *gin.Context) {
	setMailboxPrivateResponse(c)
	principal, conversationID, draftID, ok := mailboxDraftIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[mailboxDraftRevisionRequest](c, maxMailboxDraftBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if err := h.MailboxDraftService.DeleteDraft(
		c.Request.Context(), principal, conversationID, draftID, request.ExpectedRevision,
	); err != nil {
		writeMailboxError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) queueMailboxDraft(c *gin.Context) {
	setMailboxPrivateResponse(c)
	principal, conversationID, draftID, ok := mailboxDraftIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[mailboxDraftRevisionRequest](c, maxMailboxDraftBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	draft, err := h.MailboxDraftService.QueueDraft(
		c.Request.Context(), principal, conversationID, draftID, request.ExpectedRevision,
	)
	if err != nil {
		writeMailboxError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, draft)
}

func (h *Handler) queueMailboxDraftGroup(c *gin.Context) {
	setMailboxPrivateResponse(c)
	principal, conversationID, ok := mailboxDraftConversationIdentity(c)
	if !ok {
		return
	}
	groupID, err := util.ParseGoUUID(c.Param("groupId"))
	if err != nil {
		writeMailboxError(c, mailbox.ErrAgentMailInvalidInput)
		return
	}
	request, err := bindRequest[queueMailboxDraftGroupRequest](c, maxMailboxDraftBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	versions := make([]mailbox.DraftRevision, len(request.Drafts))
	for index, draft := range request.Drafts {
		versions[index] = mailbox.DraftRevision{
			ID: draft.ID, Revision: draft.ExpectedRevision,
		}
	}
	draftGroup, err := h.MailboxDraftService.QueueGroup(
		c.Request.Context(), principal, conversationID, groupID, versions,
	)
	if err != nil {
		writeMailboxError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, draftGroup)
}

func mailboxDraftConversationIdentity(
	c *gin.Context,
) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := mailboxPrincipal(c)
	if !ok {
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	conversationID, err := util.ParseGoUUID(c.Param("conversationId"))
	if err != nil {
		writeMailboxError(c, mailbox.ErrAgentMailInvalidInput)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, conversationID, true
}

func mailboxDraftIdentity(
	c *gin.Context,
) (auth.UserPrincipal, uuid.UUID, uuid.UUID, bool) {
	principal, conversationID, ok := mailboxDraftConversationIdentity(c)
	if !ok {
		return auth.UserPrincipal{}, uuid.Nil, uuid.Nil, false
	}
	draftID, err := util.ParseGoUUID(c.Param("draftId"))
	if err != nil {
		writeMailboxError(c, mailbox.ErrAgentMailInvalidInput)
		return auth.UserPrincipal{}, uuid.Nil, uuid.Nil, false
	}
	return principal, conversationID, draftID, true
}
