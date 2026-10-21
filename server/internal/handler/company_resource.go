package handler

import (
	"context"
	"net/http"
	"net/url"

	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
)

const maxCompanyResourceBodyBytes = 32 << 10

type CompanyResourceService interface {
	ListResources(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		company.ResourceListOptions,
	) ([]company.ResourceResponse, error)
	CreateLocalDirectoryResource(
		context.Context,
		uuid.UUID,
		uuid.UUID,
		company.CreateLocalDirectoryResourceInput,
	) (company.ResourceCreateResult, error)
	FetchResource(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (company.ResourceResponse, error)
	UpdateResource(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		company.ResourceUpdateInput,
	) (company.ResourceResponse, error)
	DeleteResource(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, company.RevisionInput) error
}

type companyResourceCollectionResponse struct {
	Items []company.ResourceResponse `json:"items"`
}

type updateCompanyResourceRequest struct {
	ExpectedRevision int64            `json:"expectedRevision" binding:"revision"`
	Label            optional[string] `json:"label"            binding:"required_without=Position"`
	Position         optional[int32]  `json:"position"`
}

func (h *Handler) registerCompanyResourceRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyResourceService == nil {
		return
	}
	routes := group.Group("/companies/:companyId/resources", middleware.RequireUserAuth(h.AuthService))
	routes.GET("", h.listCompanyResources)
	routes.POST("", h.createCompanyResource)
	routes.GET("/:resourceId", h.getCompanyResource)
	routes.PATCH("/:resourceId", h.updateCompanyResource)
	routes.DELETE("/:resourceId", h.deleteCompanyResource)
}

func (h *Handler) listCompanyResources(c *gin.Context) {
	principal, companyID, _, ok := companyResourceRequestIdentity(c, false)
	if !ok {
		return
	}
	options, err := parseCompanyResourceListOptions(c.Request.URL.RawQuery)
	if err != nil {
		writeCompanyResourceError(c, company.ErrResourceInvalidInput)
		return
	}
	items, err := h.CompanyResourceService.ListResources(
		c.Request.Context(),
		principal,
		companyID,
		options,
	)
	if err != nil {
		writeCompanyResourceError(c, err)
		return
	}
	if items == nil {
		items = []company.ResourceResponse{}
	}
	c.JSON(http.StatusOK, companyResourceCollectionResponse{Items: items})
}

func parseCompanyResourceListOptions(rawQuery string) (company.ResourceListOptions, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return company.ResourceListOptions{}, company.ErrResourceInvalidInput
	}
	for key, values := range query {
		if key != "daemon_id" || len(values) != 1 || values[0] == "" {
			return company.ResourceListOptions{}, company.ErrResourceInvalidInput
		}
	}
	options := company.ResourceListOptions{}
	if values, exists := query["daemon_id"]; exists {
		options.DaemonID = &values[0]
	}
	return options, nil
}

func (h *Handler) getCompanyResource(c *gin.Context) {
	principal, companyID, resourceID, ok := companyResourceRequestIdentity(c, true)
	if !ok {
		return
	}
	resource, err := h.CompanyResourceService.FetchResource(c.Request.Context(), principal, companyID, resourceID)
	if err != nil {
		writeCompanyResourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, resource)
}

// createCompanyResourceRequest binds the attach call. `localPath` is the
// absolute path on the machine `daemonId` names — the pair is what makes the
// row addressable, since one company can hold one directory per machine.
type createCompanyResourceRequest struct {
	DaemonID  string `json:"daemonId"  binding:"required,max=200"`
	LocalPath string `json:"localPath" binding:"required,max=4096"`
	Label     string `json:"label"     binding:"required,max=120"`
	Position  int32  `json:"position"  binding:"min=0"`
}

// createCompanyResource binds a directory on one machine to this company, so
// runs claimed by that machine's daemon execute in place instead of in a fresh
// worktree.
//
// The database supplies the row id. Repeating the same daemon, path, label,
// and position returns the existing row with 200; changing any canonical field
// for a daemon that is already bound is a conflict.
//
// The path is not validated against the filesystem here — the server cannot
// see the caller's disk. The desktop client checks the directory exists and is
// writable before it calls, and the daemon re-validates at claim time, which is
// the check that actually matters because it runs on the machine that will
// execute there.
func (h *Handler) createCompanyResource(c *gin.Context) {
	principal, companyID, _, ok := companyResourceRequestIdentity(c, false)
	if !ok {
		return
	}
	request, err := bindRequest[createCompanyResourceRequest](c, maxCompanyResourceBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.CompanyResourceService.CreateLocalDirectoryResource(
		c.Request.Context(),
		principal.UserID,
		companyID,
		company.CreateLocalDirectoryResourceInput{
			DaemonID: request.DaemonID,
			Path:     request.LocalPath,
			Label:    request.Label,
			Position: request.Position,
		},
	)
	if err != nil {
		writeCompanyResourceError(c, err)
		return
	}
	if result.Created {
		c.JSON(http.StatusCreated, result.Resource)
		return
	}
	c.JSON(http.StatusOK, result.Resource)
}

func (h *Handler) updateCompanyResource(c *gin.Context) {
	principal, companyID, resourceID, ok := companyResourceRequestIdentity(c, true)
	if !ok {
		return
	}
	request, err := bindRequest[updateCompanyResourceRequest](c, maxCompanyResourceBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	input := company.ResourceUpdateInput{
		ExpectedRevision: request.ExpectedRevision,
		Label:            request.Label.ptr(),
		Position:         request.Position.ptr(),
	}
	resource, err := h.CompanyResourceService.UpdateResource(
		c.Request.Context(),
		principal,
		companyID,
		resourceID,
		input,
	)
	if err != nil {
		writeCompanyResourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, resource)
}

type deleteCompanyResourceRequest struct {
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}

func (h *Handler) deleteCompanyResource(c *gin.Context) {
	principal, companyID, resourceID, ok := companyResourceRequestIdentity(c, true)
	if !ok {
		return
	}
	request, err := bindRequest[deleteCompanyResourceRequest](c, maxCompanyResourceBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if err := h.CompanyResourceService.DeleteResource(c.Request.Context(), principal, companyID, resourceID,
		company.RevisionInput{ExpectedRevision: request.ExpectedRevision}); err != nil {
		writeCompanyResourceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func companyResourceRequestIdentity(
	c *gin.Context,
	requireResource bool,
) (auth.UserPrincipal, uuid.UUID, uuid.UUID, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeCompanyResourceError(c, company.ErrUnauthenticated)
		return auth.UserPrincipal{}, uuid.Nil, uuid.Nil, false
	}
	companyID, err := util.ParseGoUUID(c.Param("companyId"))
	if err != nil {
		writeCompanyResourceError(c, company.ErrResourceNotFound)
		return auth.UserPrincipal{}, uuid.Nil, uuid.Nil, false
	}
	if !requireResource {
		return principal, companyID, uuid.Nil, true
	}
	resourceID, err := util.ParseGoUUID(c.Param("resourceId"))
	if err != nil {
		writeCompanyResourceError(c, company.ErrResourceNotFound)
		return auth.UserPrincipal{}, uuid.Nil, uuid.Nil, false
	}
	return principal, companyID, resourceID, true
}

var companyResourceErrorMappings = []errorMapping{
	{company.ErrUnauthenticated, http.StatusUnauthorized, "authentication is required", "authentication_required"},
	{
		company.ErrResourceInvalidInput,
		http.StatusUnprocessableEntity,
		"request validation failed",
		"validation_failed",
	},
	{
		company.ErrResourceConflict,
		http.StatusConflict,
		"Company resource mutation conflicts with current state",
		"company_resource_conflict",
	},
	{company.ErrResourceNotFound, http.StatusNotFound, "Company resource not found", "company_resource_not_found"},
	{
		company.ErrForbidden,
		http.StatusForbidden,
		"Your Company role does not allow this action",
		"company_permission_required",
	},
}

func writeCompanyResourceError(c *gin.Context, err error) {
	writeMappedError(c, err, companyResourceErrorMappings, internalErrorFallback)
}
