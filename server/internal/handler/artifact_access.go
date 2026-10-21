package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

type artifactGrantRequest struct {
	Permission       execution.ArtifactPermission `json:"permission"       binding:"required,oneof=view edit manage"`
	ExpectedRevision int64                        `json:"expectedRevision" binding:"revision"`
}

type unownedArtifactPageResponse struct {
	Items []execution.UnownedArtifact `json:"items"`
}

func artifactPathRequest(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	outputID, err := util.ParseGoUUID(c.Param("artifactId"))
	if err != nil {
		writeDeliverableError(c, execution.ErrDeliverableNotFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, outputID, true
}

func (h *Handler) fetchArtifact(c *gin.Context) {
	principal, outputID, ok := artifactPathRequest(c)
	if !ok {
		return
	}
	if h.OAuthWebAppOrigin == "" {
		writeDeliverableError(c, execution.ErrDeliverableInvalidConfiguration)
		return
	}
	result, err := h.DeliverableService.GetArtifact(c.Request.Context(), principal, outputID)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	result.URL = strings.TrimRight(h.OAuthWebAppOrigin, "/") +
		"/companies/" + principal.CompanyID.String() + "/artifacts/" + outputID.String()
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, result)
}

func (h *Handler) listUnownedArtifacts(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	items, err := h.DeliverableService.ListUnownedArtifacts(c.Request.Context(), principal)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, unownedArtifactPageResponse{Items: items})
}

func (h *Handler) claimUnownedArtifact(c *gin.Context) {
	principal, outputID, ok := artifactPathRequest(c)
	if !ok {
		return
	}
	if h.OAuthWebAppOrigin == "" {
		writeDeliverableError(c, execution.ErrDeliverableInvalidConfiguration)
		return
	}
	request, err := bindRequest[revisionRequest](c, 8<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.DeliverableService.ClaimUnownedArtifact(
		c.Request.Context(), principal, outputID, request.ExpectedRevision)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	result.URL = strings.TrimRight(h.OAuthWebAppOrigin, "/") +
		"/companies/" + principal.CompanyID.String() + "/artifacts/" + outputID.String()
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, result)
}

func (h *Handler) putArtifactGrant(c *gin.Context) {
	request, err := bindRequest[artifactGrantRequest](c, 8<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	h.changeArtifactGrant(c, &request.Permission, request.ExpectedRevision)
}

func (h *Handler) deleteArtifactGrant(c *gin.Context) {
	expectedRevision, err := strconv.ParseInt(c.Query("expectedRevision"), 10, 64)
	if err != nil || expectedRevision < 1 {
		writeDeliverableError(c, execution.ErrDeliverableInvalidInput)
		return
	}
	h.changeArtifactGrant(c, nil, expectedRevision)
}

func (h *Handler) changeArtifactGrant(
	c *gin.Context, permission *execution.ArtifactPermission, expectedRevision int64,
) {
	principal, outputID, ok := artifactPathRequest(c)
	if !ok {
		return
	}
	recipientID, err := util.ParseGoUUID(c.Param("userId"))
	if err != nil {
		writeDeliverableError(c, execution.ErrDeliverableInvalidInput)
		return
	}
	result, err := h.DeliverableService.ChangeArtifactGrant(
		c.Request.Context(), principal, outputID, recipientID, permission, expectedRevision,
	)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, result)
}

func (h *Handler) downloadArtifactFile(c *gin.Context) {
	principal, outputID, ok := artifactPathRequest(c)
	if !ok {
		return
	}
	ordinal, err := strconv.Atoi(c.Param("ordinal"))
	if err != nil {
		writeDeliverableError(c, execution.ErrDeliverableNotFound)
		return
	}
	artifact, err := h.DeliverableService.GetArtifact(c.Request.Context(), principal, outputID)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	sourceRunID := uuid.Nil
	if artifact.RunID != nil {
		sourceRunID = *artifact.RunID
	}
	attachmentID, err := h.DeliverableService.ResolveArtifactFileAttachment(
		c.Request.Context(), principal, sourceRunID, outputID, ordinal,
	)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	h.downloadResolvedArtifact(c, principal.CompanyID, attachmentID)
}

// Desktop keeps disposable file bytes on disk. Every use goes through current
// artifact authorization before reaching this helper, even for a matching tag.
func (h *Handler) downloadResolvedArtifact(c *gin.Context, companyID, attachmentID uuid.UUID) {
	revalidate := c.GetHeader("X-Zenith-Team-Artifact-Cache") == "revalidate"
	input := attachment.ResolvedDownloadInput{ID: attachmentID, CompanyID: companyID, OutputAccessResolved: true}
	if revalidate {
		prefix := `"attachment:` + attachmentID.String() + ":"
		tag := c.GetHeader("If-None-Match")
		if strings.HasPrefix(tag, prefix) && strings.HasSuffix(tag, `"`) {
			revision, err := strconv.ParseInt(
				strings.TrimSuffix(strings.TrimPrefix(tag, prefix), `"`),
				10,
				64,
			)
			if err == nil && revision > 0 {
				input.KnownRevision = revision
			}
		}
	}
	result, err := h.AttachmentService.DownloadResolved(c.Request.Context(), input)
	if err != nil {
		writeAttachmentError(c, err)
		return
	}
	if !revalidate {
		streamAttachmentDownload(c, result)
		return
	}
	c.Header(
		"ETag",
		`"attachment:`+result.Attachment.ID.String()+":"+strconv.FormatInt(result.Attachment.Revision, 10)+`"`,
	)
	if result.NotModified {
		c.Header("Cache-Control", "private, no-cache")
		c.Status(http.StatusNotModified)
		return
	}
	streamAttachmentDownloadWithPolicy(c, result, "private, no-cache")
}

func (h *Handler) setArtifactVisibility(c *gin.Context) {
	p, id, ok := artifactPathRequest(c)
	if !ok {
		return
	}
	input, err := bindRequest[execution.ArtifactVisibilityInput](c, 8<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.DeliverableService.SetArtifactVisibility(c.Request.Context(), p, id, input)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) revokeArtifactConversation(c *gin.Context) {
	p, id, ok := artifactPathRequest(c)
	if !ok {
		return
	}
	conversationID, err := util.ParseGoUUID(c.Param("conversationId"))
	if err != nil {
		writeDeliverableError(c, execution.ErrDeliverableInvalidInput)
		return
	}
	revision, err := strconv.ParseInt(c.Query("expectedRevision"), 10, 64)
	if err != nil || revision < 1 {
		writeDeliverableError(c, execution.ErrDeliverableInvalidInput)
		return
	}
	result, err := h.DeliverableService.RevokeArtifactConversation(
		c.Request.Context(),
		p,
		id,
		execution.ArtifactConversationInput{ConversationID: conversationID, ExpectedRevision: revision},
	)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getPublicArtifact(c *gin.Context) {
	token, err := util.ParseGoUUID(c.Param("token"))
	if err != nil {
		writeDeliverableError(c, execution.ErrDeliverableNotFound)
		return
	}
	result, err := h.DeliverableService.GetPublicArtifact(c.Request.Context(), token)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Robots-Tag", "noindex, nofollow")
	c.JSON(http.StatusOK, result)
}

func (h *Handler) downloadPublicArtifact(c *gin.Context) {
	token, err := util.ParseGoUUID(c.Param("token"))
	if err != nil {
		writeDeliverableError(c, execution.ErrDeliverableNotFound)
		return
	}
	ordinal, err := strconv.Atoi(c.Param("ordinal"))
	if err != nil {
		writeDeliverableError(c, execution.ErrDeliverableNotFound)
		return
	}
	companyID, attachmentID, err := h.DeliverableService.ResolvePublicArtifactFile(
		c.Request.Context(),
		token,
		ordinal,
	)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	result, err := h.AttachmentService.DownloadResolved(
		c.Request.Context(),
		attachment.ResolvedDownloadInput{ID: attachmentID, CompanyID: companyID, OutputAccessResolved: true},
	)
	if err != nil {
		writeAttachmentError(c, err)
		return
	}
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Robots-Tag", "noindex, nofollow")
	streamAttachmentDownload(c, result)
}
