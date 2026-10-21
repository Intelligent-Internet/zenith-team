package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

const maxReferenceDocumentBodyBytes = 8 << 10

// ReferenceService keeps the founder-supplied documents a Company hands its
// Agents. There is no read surface: the Agent receives them in its frozen Run
// brief, and the daemon materializes them into the Company workspace.
type ReferenceService interface {
	AddReferenceDocuments(
		context.Context,
		auth.UserPrincipal,
		[]uuid.UUID,
	) ([]company.ReferenceDocument, error)
}

type addReferenceDocumentsRequest struct {
	AttachmentIDs []string `json:"attachmentIds" binding:"required,min=1,max=10,dive,uuid"`
}

type addReferenceDocumentsResponse struct {
	Registered []string `json:"registered"`
}

func (h *Handler) registerReferenceDocumentRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.ReferenceService == nil {
		return
	}
	// company.resources.manage, and no Run proof: this is a person handing
	// over their own files from their own browser, not an Agent asserting
	// something on their behalf.
	group.POST(
		"/companies/:companyId/reference-documents",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionCompanyResourcesManage),
		middleware.RequireHumanActor(),
		h.addReferenceDocuments,
	)
}

func (h *Handler) addReferenceDocuments(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	request, err := bindRequest[addReferenceDocumentsRequest](c, maxReferenceDocumentBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	attachmentIDs := make([]uuid.UUID, 0, len(request.AttachmentIDs))
	for _, raw := range request.AttachmentIDs {
		attachmentID, err := util.ParseGoUUID(raw)
		if err != nil {
			writeBoundaryError(c, boundaryErrorValidationFailed)
			return
		}
		attachmentIDs = append(attachmentIDs, attachmentID)
	}
	registered, err := h.ReferenceService.AddReferenceDocuments(
		c.Request.Context(),
		principal,
		attachmentIDs,
	)
	if err != nil {
		writeCompanyError(c, err)
		return
	}
	ids := make([]string, 0, len(registered))
	for _, document := range registered {
		ids = append(ids, document.AttachmentID.String())
	}
	c.JSON(http.StatusOK, addReferenceDocumentsResponse{Registered: ids})
}
