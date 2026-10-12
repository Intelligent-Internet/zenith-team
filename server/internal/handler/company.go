package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/util"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
)

const maxCompanyBodyBytes = 64 << 10

type CompanyService interface {
	RemoveMember(context.Context, auth.UserPrincipal, company.MembershipMutation) error
	Leave(context.Context, auth.UserPrincipal, int64) error
	Delete(context.Context, auth.UserPrincipal, int64) error
	Create(context.Context, auth.UserPrincipal, company.CreateCompanyInput) (company.Response, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (company.Response, error)
	List(context.Context, auth.UserPrincipal) ([]company.Response, error)
	Bootstrap(context.Context, auth.UserPrincipal) (company.Bootstrap, error)
	Statistics(context.Context, auth.UserPrincipal) (company.Statistics, error)
	Authorize(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error)
	RequirePermission(context.Context, auth.UserPrincipal, uuid.UUID, auth.Permission) (auth.UserPrincipal, error)
	ChangeRole(context.Context, auth.UserPrincipal, company.ChangeRoleInput) (company.ActiveMemberResponse, error)
	TransferOwnership(
		context.Context,
		auth.UserPrincipal,
		company.MembershipMutation,
	) ([]company.ActiveMemberResponse, error)
	UpdateProfile(context.Context, auth.UserPrincipal, company.UpdateProfileInput) (company.Response, error)
	ListMembers(context.Context, auth.UserPrincipal) ([]company.ActiveMemberResponse, error)
}

type companyCollectionResponse struct {
	Items []company.Response `json:"items"`
}

type activeMemberCollectionResponse struct {
	Items []company.ActiveMemberResponse `json:"items"`
}

type createCompanyRequest struct {
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	TaskPrefix      string `json:"taskPrefix"`
	BusinessContext string `json:"businessContext"`
	WebsiteURL      string `json:"websiteUrl"`
}

type updateCompanyProfileRequest struct {
	ExpectedRevision   optional[int64] `json:"expectedRevision" binding:"omitnil,required,revision"`
	Name               json.RawMessage `json:"name"`
	ContactEmail       json.RawMessage `json:"contactEmail"`
	PhoneCountryISO2   json.RawMessage `json:"phoneCountryIso2"`
	PhoneNumber        json.RawMessage `json:"phoneNumber"`
	AvatarAttachmentID json.RawMessage `json:"avatarAttachmentId"`
}

func (h *Handler) registerCompanyRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil {
		return
	}
	routes := group.Group("/companies", middleware.RequireUserAuth(h.AuthService))
	routes.GET("", h.listCompanies)
	routes.POST("", h.createCompany)
	routes.GET("/:companyId", h.getCompany)
	// Company lifecycle and team mutations are a person's decisions: a Run
	// acting through its proof holds its authorizer's role but may not use it
	// to delete, reshape or leave the Company.
	routes.DELETE(
		"/:companyId",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionCompanyDelete),
		middleware.RequireHumanActor(),
		h.deleteCompany,
	)
	routes.GET(
		"/:companyId/bootstrap",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionAgentsManage),
		h.getCompanyBootstrap,
	)
	routes.GET(
		"/:companyId/statistics",
		middleware.RequireCompanyMember(h.CompanyService),
		h.getCompanyStatistics,
	)
	routes.PATCH(
		"/:companyId/profile",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionCompanyProfileUpdate),
		h.updateCompanyProfile,
	)
	routes.GET("/:companyId/members", middleware.RequireCompanyMember(h.CompanyService), h.listCompanyMembers)
	// Role changes and removals resolve their permission in the service: the
	// answer depends on whether the target is an owner.
	routes.PATCH(
		"/:companyId/members/:userId",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionMembersManage),
		middleware.RequireHumanActor(),
		h.changeCompanyMemberRole,
	)
	routes.DELETE(
		"/:companyId/members/:userId",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionMembersManage),
		middleware.RequireHumanActor(),
		h.removeCompanyMember,
	)
	routes.POST(
		"/:companyId/members/:userId/transfer-ownership",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionMembersOwnersManage),
		middleware.RequireHumanActor(),
		h.transferCompanyOwnership,
	)
	routes.POST(
		"/:companyId/leave",
		middleware.RequireCompanyMember(h.CompanyService),
		middleware.RequireHumanActor(),
		h.leaveCompany,
	)
}

type deleteCompanyRequest struct {
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}

func (h *Handler) deleteCompany(c *gin.Context) {
	request, err := bindRequest[deleteCompanyRequest](c, maxCompanyBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	if err := h.CompanyService.Delete(
		c.Request.Context(),
		principal,
		request.ExpectedRevision,
	); err != nil {
		writeCompanyError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) getCompanyBootstrap(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.CompanyService.Bootstrap(c.Request.Context(), principal)
	if err != nil {
		writeCompanyError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) listCompanies(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	items, err := h.CompanyService.List(c.Request.Context(), principal)
	if err != nil {
		writeCompanyError(c, err)
		return
	}
	if items == nil {
		items = []company.Response{}
	}
	c.JSON(http.StatusOK, companyCollectionResponse{Items: items})
}

func (h *Handler) getCompanyStatistics(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	statistics, err := h.CompanyService.Statistics(c.Request.Context(), principal)
	if err != nil {
		writeCompanyError(c, err)
		return
	}
	c.JSON(http.StatusOK, statistics)
}

func (h *Handler) getCompany(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	companyID, err := util.ParseGoUUID(c.Param("companyId"))
	if err != nil {
		writeBoundaryError(c, boundaryErrorCompanyNotFound)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.CompanyService.Get(c.Request.Context(), principal, companyID)
	if err != nil {
		writeCompanyError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) listCompanyMembers(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	items, err := h.CompanyService.ListMembers(c.Request.Context(), principal)
	if err != nil {
		writeCompanyError(c, err)
		return
	}
	if items == nil {
		items = []company.ActiveMemberResponse{}
	}
	c.JSON(http.StatusOK, activeMemberCollectionResponse{Items: items})
}

func (h *Handler) updateCompanyProfile(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	request, err := bindRequest[updateCompanyProfileRequest](c, maxCompanyBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	input, err := request.domainInput()
	if err != nil {
		// A profile field that is absent or of the wrong type is a field
		// rule that did not hold, which is 422 like every other one; the
		// strict decoder has already answered malformed JSON and unknown
		// fields with 400.
		writeBoundaryErrorDetail(c, boundaryErrorValidationFailed, err.Error())
		return
	}
	result, err := h.CompanyService.UpdateProfile(c.Request.Context(), principal, input)
	if err != nil {
		var conflict *company.ProfileRevisionConflictError
		if errors.As(err, &conflict) {
			requestID := util.RequestIDFromContext(c)
			c.JSON(http.StatusConflict, gin.H{
				"detail":         "Company profile changed elsewhere",
				"code":           "company_profile_revision_conflict",
				"requestId":      requestID,
				"currentCompany": conflict.Current,
			})
			return
		}
		writeCompanyError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (request updateCompanyProfileRequest) domainInput() (company.UpdateProfileInput, error) {
	expectedRevision := *request.ExpectedRevision.ptr()
	name, err := decodeRequiredProfileString("name", request.Name)
	if err != nil {
		return company.UpdateProfileInput{}, err
	}
	contactEmail, err := decodeRequiredNullableProfileString("contactEmail", request.ContactEmail)
	if err != nil {
		return company.UpdateProfileInput{}, err
	}
	phoneCountryISO2, err := decodeRequiredNullableProfileString("phoneCountryIso2", request.PhoneCountryISO2)
	if err != nil {
		return company.UpdateProfileInput{}, err
	}
	phoneNumber, err := decodeRequiredNullableProfileString("phoneNumber", request.PhoneNumber)
	if err != nil {
		return company.UpdateProfileInput{}, err
	}
	var avatarAttachmentID *uuid.NullUUID
	if len(request.AvatarAttachmentID) > 0 {
		value, err := decodeRequiredNullableProfileString("avatarAttachmentId", request.AvatarAttachmentID)
		if err != nil {
			return company.UpdateProfileInput{}, err
		}
		avatarAttachmentID = &uuid.NullUUID{}
		if value != nil {
			id, err := util.ParseGoUUID(*value)
			if err != nil {
				return company.UpdateProfileInput{}, errors.New("avatarAttachmentId must be a UUID or null")
			}
			*avatarAttachmentID = uuid.NullUUID{UUID: id, Valid: true}
		}
	}
	return company.UpdateProfileInput{
		ExpectedRevision:   expectedRevision,
		Name:               name,
		ContactEmail:       contactEmail,
		PhoneCountryISO2:   phoneCountryISO2,
		PhoneNumber:        phoneNumber,
		AvatarAttachmentID: avatarAttachmentID,
	}, nil
}

func (h *Handler) createCompany(c *gin.Context) {
	request, err := bindRequest[createCompanyRequest](c, maxCompanyBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.CompanyService.Create(c.Request.Context(), principal, company.CreateCompanyInput{
		Name:            request.Name,
		Slug:            request.Slug,
		TaskPrefix:      request.TaskPrefix,
		BusinessContext: request.BusinessContext,
		WebsiteURL:      request.WebsiteURL,
	})
	if err != nil {
		writeCompanyError(c, err)
		return
	}
	c.JSON(http.StatusCreated, result)
}

var companyErrorMappings = []errorMapping{
	{company.ErrLastOwner, http.StatusConflict, "The last owner cannot leave or be removed", "team_last_owner"},
	{
		company.ErrMemberRevisionConflict,
		http.StatusConflict,
		"Membership changed; refresh and try again",
		"team_member_revision_conflict",
	},
	{company.ErrUnauthenticated, http.StatusUnauthorized, "authentication is required", "authentication_required"},
	{company.ErrInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
	{
		company.ErrDeleteConflict,
		http.StatusConflict,
		"Company changed; refresh and confirm deletion again",
		"company_delete_conflict",
	},
	{
		company.ErrConflict,
		http.StatusConflict,
		"Company creation conflicts with an existing resource",
		"company_create_conflict",
	},
	{company.ErrNotFound, http.StatusNotFound, "Company not found", "company_not_found"},
	{
		company.ErrForbidden,
		http.StatusForbidden,
		"Your Company role does not allow this action",
		"company_permission_required",
	},
}

func writeCompanyError(c *gin.Context, err error) {
	writeMappedError(c, err, companyErrorMappings, internalErrorFallback)
}

type membershipMutationRequest struct {
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}

func (h *Handler) removeCompanyMember(c *gin.Context) {
	request, err := bindRequest[membershipMutationRequest](c, maxCompanyBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	userID, err := util.ParseGoUUID(c.Param("userId"))
	if err != nil || userID == uuid.Nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	if err = h.CompanyService.RemoveMember(
		c.Request.Context(),
		principal,
		company.MembershipMutation{UserID: userID, ExpectedRevision: request.ExpectedRevision},
	); err != nil {
		writeCompanyError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) leaveCompany(c *gin.Context) {
	request, err := bindRequest[membershipMutationRequest](c, maxCompanyBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	if err = h.CompanyService.Leave(c.Request.Context(), principal, request.ExpectedRevision); err != nil {
		writeCompanyError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type changeCompanyMemberRoleRequest struct {
	ExpectedRevision int64  `json:"expectedRevision" binding:"revision"`
	Role             string `json:"role"             binding:"required,oneof=owner admin member"`
}

func (h *Handler) changeCompanyMemberRole(c *gin.Context) {
	request, err := bindRequest[changeCompanyMemberRoleRequest](c, maxCompanyBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	userID, err := util.ParseGoUUID(c.Param("userId"))
	if err != nil || userID == uuid.Nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	member, err := h.CompanyService.ChangeRole(
		c.Request.Context(),
		principal,
		company.ChangeRoleInput{
			UserID:           userID,
			Role:             auth.CompanyRole(request.Role),
			ExpectedRevision: request.ExpectedRevision,
		},
	)
	if err != nil {
		writeCompanyError(c, err)
		return
	}
	c.JSON(http.StatusOK, member)
}

func (h *Handler) transferCompanyOwnership(c *gin.Context) {
	request, err := bindRequest[membershipMutationRequest](c, maxCompanyBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	userID, err := util.ParseGoUUID(c.Param("userId"))
	if err != nil || userID == uuid.Nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	items, err := h.CompanyService.TransferOwnership(
		c.Request.Context(),
		principal,
		company.MembershipMutation{UserID: userID, ExpectedRevision: request.ExpectedRevision},
	)
	if err != nil {
		writeCompanyError(c, err)
		return
	}
	c.JSON(http.StatusOK, activeMemberCollectionResponse{Items: items})
}
