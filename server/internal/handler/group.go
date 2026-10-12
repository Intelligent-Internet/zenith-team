package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/util"
)

// Groups (Access TECH D3): every active member reads a Company's Groups, as
// they read the Team page; owners and admins (members.manage) create,
// rename, fill, empty and delete them. Every route is a person's: a Run
// proof is refused before authentication (route_actor.go), and
// RequireHumanActor stays as the second guard it is on the other Team
// routes. A Group's grant on a Project is a Project route (project.go).

type createGroupRequest struct {
	Name        string `json:"name"        binding:"required"`
	Description string `json:"description"`
}

type updateGroupRequest struct {
	ExpectedRevision int64            `json:"expectedRevision" binding:"revision"`
	Name             optional[string] `json:"name"             binding:"required_without=Description"`
	Description      optional[string] `json:"description"`
}

type groupRevisionRequest struct {
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}

// changeGroupMembersRequest is the one way to change a Group's members. The
// service refuses more than 500 ids in all, an id named twice over and the
// nil id.
type changeGroupMembersRequest struct {
	AddUserIDs       []uuid.UUID `json:"addUserIds"       binding:"required,max=500"`
	RemoveUserIDs    []uuid.UUID `json:"removeUserIds"    binding:"required,max=500"`
	ExpectedRevision int64       `json:"expectedRevision" binding:"revision"`
}

// previewGroupChangeRequest is the change a dialog is about to send:
// the members body without its revision, or a delete.
type previewGroupChangeRequest struct {
	AddUserIDs    []uuid.UUID `json:"addUserIds"    binding:"required,max=500"`
	RemoveUserIDs []uuid.UUID `json:"removeUserIds" binding:"required,max=500"`
	Delete        bool        `json:"delete"`
}

type groupCollectionResponse struct {
	Items []company.Group `json:"items"`
}

func (h *Handler) registerGroupRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.GroupService == nil {
		return
	}
	routes := group.Group("/companies/:companyId/groups", middleware.RequireUserAuth(h.AuthService))
	member := middleware.RequireCompanyMember(h.CompanyService)
	manage := middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionMembersManage)
	human := middleware.RequireHumanActor()
	routes.GET("", member, h.listGroups)
	routes.POST("", manage, human, h.createGroup)
	routes.GET("/:groupId", member, h.getGroup)
	routes.PATCH("/:groupId", manage, human, h.updateGroup)
	routes.DELETE("/:groupId", manage, human, h.deleteGroup)
	routes.POST("/:groupId/members", manage, human, h.changeGroupMembers)
	routes.POST("/:groupId/preview", manage, human, h.previewGroupChange)
}

// groupRequestIdentity reads the caller and the :groupId. An id that does not
// parse names no Group of the Company: 404 group_not_found.
func groupRequestIdentity(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	groupID, err := util.ParseGoUUID(c.Param("groupId"))
	if err != nil {
		writeGroupError(c, company.ErrGroupNotFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, groupID, true
}

func (h *Handler) listGroups(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	groups, err := h.GroupService.List(c.Request.Context(), principal)
	if err != nil {
		writeGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, groupCollectionResponse{Items: groups})
}

func (h *Handler) createGroup(c *gin.Context) {
	request, err := bindRequest[createGroupRequest](c, 4<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	value, err := h.GroupService.Create(c.Request.Context(), principal, company.GroupInput{
		Name: request.Name, Description: request.Description,
	})
	if err != nil {
		writeGroupError(c, err)
		return
	}
	c.JSON(http.StatusCreated, value)
}

func (h *Handler) getGroup(c *gin.Context) {
	principal, groupID, ok := groupRequestIdentity(c)
	if !ok {
		return
	}
	value, err := h.GroupService.Get(c.Request.Context(), principal, groupID)
	if err != nil {
		writeGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}

func (h *Handler) updateGroup(c *gin.Context) {
	principal, groupID, ok := groupRequestIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[updateGroupRequest](c, 4<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	value, err := h.GroupService.Update(c.Request.Context(), principal, groupID, company.GroupUpdateInput{
		Name:             request.Name.ptr(),
		Description:      request.Description.ptr(),
		ExpectedRevision: request.ExpectedRevision,
	})
	if err != nil {
		writeGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}

func (h *Handler) deleteGroup(c *gin.Context) {
	principal, groupID, ok := groupRequestIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[groupRevisionRequest](c, 4<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if err := h.GroupService.Delete(c.Request.Context(), principal, groupID, request.ExpectedRevision); err != nil {
		writeGroupError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) changeGroupMembers(c *gin.Context) {
	principal, groupID, ok := groupRequestIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[changeGroupMembersRequest](c, 64<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	value, err := h.GroupService.ChangeMembers(c.Request.Context(), principal, groupID, company.GroupMembersInput{
		AddUserIDs:       request.AddUserIDs,
		RemoveUserIDs:    request.RemoveUserIDs,
		ExpectedRevision: request.ExpectedRevision,
	})
	if err != nil {
		writeGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}

func (h *Handler) previewGroupChange(c *gin.Context) {
	principal, groupID, ok := groupRequestIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[previewGroupChangeRequest](c, 64<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	preview, err := h.GroupService.Preview(c.Request.Context(), principal, groupID, company.GroupChange{
		AddUserIDs: request.AddUserIDs, RemoveUserIDs: request.RemoveUserIDs, Delete: request.Delete,
	})
	if err != nil {
		writeGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, preview)
}

// The two Group answers the Project routes share (Access TECH D4).
var (
	groupNotFoundMapping = errorMapping{
		company.ErrGroupNotFound, http.StatusNotFound, "Group not found", "group_not_found",
	}
	groupLimitReachedMapping = errorMapping{
		company.ErrGroupLimitReached,
		http.StatusConflict,
		"Group limit reached: 100 groups, 500 people in a group, or 50 groups on a project",
		"group_limit_reached",
	}
)

var groupErrorMappings = []errorMapping{
	{
		company.ErrGroupInvalidInput,
		http.StatusUnprocessableEntity,
		"request validation failed",
		"validation_failed",
	},
	// The Company and member locks answer as on every other Team route.
	{company.ErrNotFound, http.StatusNotFound, "Company not found", "company_not_found"},
	{
		company.ErrForbidden,
		http.StatusForbidden,
		"Your Company role does not allow this action",
		"company_permission_required",
	},
	groupNotFoundMapping,
	{
		company.ErrGroupConflict,
		http.StatusConflict,
		"The group changed or another group has this name; reload it and try again",
		"group_conflict",
	},
	{
		company.ErrGroupMemberRequired,
		http.StatusConflict,
		"Only active Team members can be in a group",
		"group_member_required",
	},
	groupLimitReachedMapping,
	// No laundering names no Project.
	{
		company.ErrGroupGrantPermissionRequired,
		http.StatusForbidden,
		"This change moves access on a project where you cannot change access yourself",
		"group_grant_permission_required",
	},
}

func writeGroupError(c *gin.Context, err error) {
	writeMappedError(c, err, groupErrorMappings, internalErrorFallback)
}
