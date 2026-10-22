package handler

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/artifact"
	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

func (h *Handler) registerArtifactContentRoutes(api *gin.RouterGroup) {
	if h.ArtifactService == nil || h.AuthService == nil || h.CompanyService == nil {
		return
	}
	routes := api.Group(
		"/companies/:companyId",
		middleware.NoStore(),
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
		middleware.RequireHumanActor(),
	)
	routes.GET("/artifacts/:artifactId/access", h.getArtifactContentAccess)
	routes.GET("/artifacts/:artifactId/content", h.getRetainedArtifactContent)
	if h.AttachmentService != nil {
		routes.GET(
			"/artifacts/:artifactId/snapshots/:snapshotId/items/:itemId/download",
			h.downloadRetainedArtifactItem,
		)
	}
	routes.PATCH("/artifacts/:artifactId/access", h.changeArtifactContentAccess)
	routes.GET("/pages", h.listPages)
}

type artifactContentGrantRequest struct {
	UserID     string          `json:"userId"     binding:"required,uuid"`
	Permission json.RawMessage `json:"permission" binding:"required"`
}
type artifactContentAccessRequest struct {
	RevokeConversationID *string                      `json:"revokeConversationId,omitempty" binding:"omitempty,uuid"`
	ExpectedRevision     int64                        `json:"expectedRevision"               binding:"revision"`
	Visibility           *string                      `json:"visibility,omitempty"           binding:"omitempty,oneof=private company"`
	Grant                *artifactContentGrantRequest `json:"grant,omitempty"`
}

func (h *Handler) getArtifactContentAccess(c *gin.Context) {
	p, id, ok := artifactContentPath(c)
	if !ok {
		return
	}
	result, err := h.ArtifactService.Access(c.Request.Context(), p, id)
	if err != nil {
		writeArtifactContentError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) changeArtifactContentAccess(c *gin.Context) {
	p, id, ok := artifactContentPath(c)
	if !ok {
		return
	}
	request, err := bindRequest[artifactContentAccessRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	input := artifact.AccessChange{
		ArtifactID:       id,
		ExpectedRevision: request.ExpectedRevision,
		Visibility:       request.Visibility,
	}
	if request.RevokeConversationID != nil {
		id, err := util.ParseGoUUID(*request.RevokeConversationID)
		if err != nil {
			writeArtifactContentError(c, artifact.ErrInvalid)
			return
		}
		input.RevokeConversationID = &id
	}
	if request.Grant != nil {
		recipient, err := util.ParseGoUUID(request.Grant.UserID)
		if err != nil {
			writeArtifactContentError(c, artifact.ErrInvalid)
			return
		}
		var permission *string
		if err := json.Unmarshal(request.Grant.Permission, &permission); err != nil {
			writeArtifactContentError(c, artifact.ErrInvalid)
			return
		}
		if permission != nil {
			switch *permission {
			case "view", "edit", "manage":
			default:
				writeArtifactContentError(c, artifact.ErrInvalid)
				return
			}
		}
		input.Grant = &artifact.GrantChange{UserID: recipient, Permission: permission}
	}
	result, err := h.ArtifactService.ChangeAccess(c.Request.Context(), p, input)
	if err != nil {
		writeArtifactContentError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) listPages(c *gin.Context) {
	p, ok := taskPrincipal(c)
	if !ok {
		return
	}
	items, err := h.ArtifactService.Pages(c.Request.Context(), p)
	if err != nil {
		writeArtifactContentError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Items []artifact.Page `json:"items"`
	}{items})
}

var artifactContentErrorMappings = []errorMapping{
	{artifact.ErrNotFound, http.StatusNotFound, "Artifact is unavailable", "artifact_not_found"},
	{artifact.ErrForbidden, http.StatusForbidden, "You cannot change this Artifact's access", "artifact_forbidden"},
	{artifact.ErrInvalid, http.StatusUnprocessableEntity, "Invalid Artifact access change", "artifact_invalid"},
	{artifact.ErrConflict, http.StatusConflict, "Access changed. Refresh and try again.", "artifact_conflict"},
}

func writeArtifactContentError(c *gin.Context, err error) {
	writeMappedError(c, err, artifactContentErrorMappings, internalErrorFallback)
}

func artifactContentPath(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	p, ok := taskPrincipal(c)
	if !ok {
		return p, uuid.Nil, false
	}
	id, err := util.ParseGoUUID(c.Param("artifactId"))
	if err != nil {
		writeArtifactContentError(c, artifact.ErrNotFound)
		return p, uuid.Nil, false
	}
	return p, id, true
}

func (h *Handler) getRetainedArtifactContent(c *gin.Context) {
	p, id, ok := artifactContentPath(c)
	if !ok {
		return
	}
	input := artifact.ContentInput{ArtifactID: id}
	if raw := c.Query("snapshotId"); raw != "" {
		snapshot, err := util.ParseGoUUID(raw)
		if err != nil {
			writeArtifactContentError(c, artifact.ErrInvalid)
			return
		}
		input.SnapshotID = &snapshot
	}
	result, err := h.ArtifactService.Content(c.Request.Context(), p, input)
	if err != nil {
		writeArtifactContentError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) downloadRetainedArtifactItem(c *gin.Context) {
	p, id, ok := artifactContentPath(c)
	if !ok {
		return
	}
	snapshot, err := util.ParseGoUUID(c.Param("snapshotId"))
	if err != nil {
		writeArtifactContentError(c, artifact.ErrNotFound)
		return
	}
	item, err := util.ParseGoUUID(c.Param("itemId"))
	if err != nil {
		writeArtifactContentError(c, artifact.ErrNotFound)
		return
	}
	attachmentID, err := h.ArtifactService.ContentAttachment(
		c.Request.Context(),
		p,
		artifact.ContentAttachmentInput{
			ArtifactID: id, SnapshotID: snapshot, ItemID: item,
		},
	)
	if err != nil {
		writeArtifactContentError(c, err)
		return
	}
	result, err := h.AttachmentService.DownloadResolved(c.Request.Context(), attachment.ResolvedDownloadInput{
		ID: attachmentID, CompanyID: p.CompanyID, RequestedByUserID: p.UserID, ResourceAccessResolved: true,
	})
	if err != nil {
		writeAttachmentError(c, err)
		return
	}
	streamAttachmentDownload(c, result)
}
