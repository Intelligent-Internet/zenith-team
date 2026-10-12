package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/middleware"
)

const maxPublicationBodyBytes = 8 << 10

type PublicationService interface {
	Fetch(context.Context, auth.UserPrincipal, uuid.UUID) (execution.PublicationView, error)
	SetSubdomain(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		execution.PublicationSubdomainInput,
	) (execution.Publication, error)
	Create(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		execution.PublicationCreateInput,
	) (execution.PublicationCreateResult, error)
	Unpublish(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		execution.PublicationUnpublishInput,
	) (execution.Publication, error)
}

type publicationCreateRequest struct {
	ConfirmPublicExposure bool `json:"confirmPublicExposure" binding:"required"`
}

type publicationUnpublishRequest struct {
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}

type publicationSubdomainRequest struct {
	Subdomain        string `json:"subdomain"        binding:"required,min=2,max=63"`
	ExpectedRevision int64  `json:"expectedRevision" binding:"revision"`
}

func (h *Handler) registerPublicationRoutes(group *gin.RouterGroup) {
	if group == nil || h.AuthService == nil || h.CompanyService == nil || h.PublicationService == nil {
		return
	}
	company := group.Group(
		"/companies/:companyId",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	company.POST("/runs/:runId/result/publication", h.createPublication)
	company.POST("/runs/:runId/result/publication/unpublish", h.unpublishPublication)
	company.PUT("/runs/:runId/result/publication/subdomain", h.setPublicationSubdomain)
}

func (h *Handler) setPublicationSubdomain(c *gin.Context) {
	principal, runID, ok := publicationRequestAuthority(c)
	if !ok {
		return
	}
	request, err := bindRequest[publicationSubdomainRequest](c, maxPublicationBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	publication, err := h.PublicationService.SetSubdomain(
		c.Request.Context(),
		principal,
		runID,
		execution.PublicationSubdomainInput{
			Subdomain:        request.Subdomain,
			ExpectedRevision: request.ExpectedRevision,
		},
	)
	if err != nil {
		writePublicationError(c, err)
		return
	}
	c.JSON(http.StatusOK, publication)
}

func (h *Handler) createPublication(c *gin.Context) {
	principal, runID, ok := publicationRequestAuthority(c)
	if !ok {
		return
	}
	request, err := bindRequest[publicationCreateRequest](c, maxPublicationBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.PublicationService.Create(
		c.Request.Context(), principal, runID,
		execution.PublicationCreateInput{ConfirmPublicExposure: request.ConfirmPublicExposure},
	)
	if err != nil {
		writePublicationError(c, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, result)
}

func (h *Handler) unpublishPublication(c *gin.Context) {
	principal, runID, ok := publicationRequestAuthority(c)
	if !ok {
		return
	}
	request, err := bindRequest[publicationUnpublishRequest](c, maxPublicationBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	publication, err := h.PublicationService.Unpublish(
		c.Request.Context(), principal, runID,
		execution.PublicationUnpublishInput{ExpectedRevision: request.ExpectedRevision},
	)
	if err != nil {
		writePublicationError(c, err)
		return
	}
	c.JSON(http.StatusOK, publication)
}

func publicationRequestAuthority(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	runID, ok := parseRunPathID(c.Param("runId"))
	if !ok {
		writePublicationError(c, execution.ErrPublicationNotFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, runID, true
}

var publicationErrorMappings = []errorMapping{
	{
		execution.ErrPublicationSubdomainTaken,
		http.StatusConflict,
		"this subdomain is already claimed",
		"publication_subdomain_taken",
	},
	{
		execution.ErrPublicationSubdomainDisabled,
		http.StatusServiceUnavailable,
		"custom URL routing is not configured for this publication",
		"publication_subdomain_disabled",
	},
	{
		execution.ErrPublicationInvalidInput,
		http.StatusUnprocessableEntity,
		"request validation failed",
		"validation_failed",
	},
	{execution.ErrPublicationForbidden, http.StatusForbidden, "owner access is required", "forbidden"},
	{execution.ErrPublicationNotFound, http.StatusNotFound, "run result was not found", "not_found"},
	{
		execution.ErrPublicationConflict,
		http.StatusConflict,
		"the publication changed; refresh and try again",
		"conflict",
	},
	{
		execution.ErrPublicationNotPublishable,
		http.StatusUnprocessableEntity,
		"the primary output must be a verified ZIP archive",
		"output_not_publishable",
	},
	{
		execution.ErrPublicationApprovalRequired,
		http.StatusConflict,
		"approve this result before publishing it",
		"approval_required",
	},
	{
		execution.ErrPublicationDisabled,
		http.StatusServiceUnavailable,
		"Public artifact publication is disabled",
		"publication_disabled",
	},
}

func writePublicationError(c *gin.Context, err error) {
	writeMappedError(c, err, publicationErrorMappings,
		errorFallback{detail: "publication is unavailable", code: "publication_unavailable"})
}
