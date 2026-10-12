package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/util"
)

type createProjectRequest struct {
	Files       *company.ProjectFilesInput `json:"files"`
	Key         string                     `json:"key"         binding:"required"`
	Title       string                     `json:"title"       binding:"required"`
	Description string                     `json:"description"`
}

// updateProjectRequest carries no files: they are replaced through PUT
// /projects/{id}/files, and the strict decoder refuses a PATCH naming them.
// A visibility change also names the access revision it was decided on; the
// service requires ExpectedAccessRevision with Visibility and only then.
type updateProjectRequest struct {
	ExpectedRevision       int64            `json:"expectedRevision"       binding:"revision"`
	ExpectedAccessRevision optional[int64]  `json:"expectedAccessRevision"`
	Title                  optional[string] `json:"title"                  binding:"required_without_all=Description Visibility"`
	Description            optional[string] `json:"description"`
	Visibility             optional[string] `json:"visibility"             binding:"omitempty,oneof=company restricted"`
}
type archiveProjectRequest struct {
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}
type replaceProjectFilesRequest struct {
	LogoID           *uuid.UUID  `json:"logoId"`
	DocumentIDs      []uuid.UUID `json:"documentIds"      binding:"required,max=10"`
	ExpectedRevision int64       `json:"expectedRevision" binding:"revision"`
}
type setProjectGrantRequest struct {
	Permissions      []string `json:"permissions"      binding:"required,max=10"`
	ExpectedRevision int64    `json:"expectedRevision" binding:"revision"`
}
type projectAccessRevisionRequest struct {
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}

// previewProjectGrantRequest is the body of both grant previews, a person's
// and a Group's: the set the save would send, empty for a removal.
type previewProjectGrantRequest struct {
	Permissions []string `json:"permissions" binding:"required,max=10"`
}

type projectCollectionResponse struct {
	Items []company.ProjectResponse `json:"items"`
}

func (h *Handler) registerProjectRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.ProjectService == nil {
		return
	}
	// Creating a Project and the Managed projects view are Company-level and
	// need projects.manage. Everything under /:projectId answers from the
	// caller's Project access (ADR 0017): a Project they cannot see is 404,
	// before the body is read. A missing permission on a visible one is 403
	// project_permission_required, also before the body on the routes with
	// one fixed permission (archive, files, a grant's PUT and DELETE); PATCH
	// needs a permission per field, so the service checks it after parsing the
	// body, before the revision. Join needs no Project permission, only the
	// Company projects.manage, checked first. The service checks again inside
	// the transaction. Grants, visibility, files, Join, archive and the manage
	// view are management or changes of who may see what, so a Run proof never
	// reaches them.
	routes := group.Group(
		"/companies/:companyId/projects",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	manage := middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionProjectsManage)
	human := middleware.RequireHumanActor()
	visible := middleware.RequireProjectVisible(h.ProjectService)
	permitted := func(permission auth.ProjectPermission) gin.HandlerFunc {
		return middleware.RequireProjectPermission(h.ProjectService, permission)
	}
	// Only the list takes a query string; every other Project route answers
	// any query with 422 validation_failed (conventions.md), after the Company
	// membership check and before the Project guards.
	none := refuseProjectQuery
	routes.GET("", h.listProjects)
	routes.POST("", none, manage, h.createProject)
	// The access report lists people outside Projects they were working in;
	// it is management, so a Company permission and a person, never a Run.
	routes.GET("/access-report", none, human, manage, h.getProjectAccessReport)
	routes.GET("/:projectId", none, visible, h.getProject)
	routes.PATCH("/:projectId", none, human, visible, h.updateProject)
	routes.POST("/:projectId/archive", none, human, permitted(auth.ProjectPermissionArchive), h.archiveProject)
	routes.PUT("/:projectId/files", none, human, permitted(auth.ProjectPermissionFilesManage), h.replaceProjectFiles)
	routes.GET("/:projectId/grants", none, human, visible, h.listProjectGrants)
	routes.PUT("/:projectId/grants/:userId", none, human, permitted(auth.ProjectPermissionGrantsManage), h.setProjectGrant)
	routes.DELETE("/:projectId/grants/:userId", none, human, permitted(auth.ProjectPermissionGrantsManage),
		h.removeProjectGrant)
	// What a person's grant change or removal would do, so the app asks
	// first only when it stops Runs or takes the Project away.
	routes.POST("/:projectId/grants/:userId/preview", none, human, permitted(auth.ProjectPermissionGrantsManage),
		h.previewProjectGrant)
	// A Group's grant on the Project (Access TECH D4): the same guards as a
	// person's, and a preview of what the change would do.
	routes.PUT("/:projectId/group-grants/:groupId", none, human, permitted(auth.ProjectPermissionGrantsManage),
		h.setProjectGroupGrant)
	routes.DELETE("/:projectId/group-grants/:groupId", none, human, permitted(auth.ProjectPermissionGrantsManage),
		h.removeProjectGroupGrant)
	routes.POST("/:projectId/group-grants/:groupId/preview", none, human,
		permitted(auth.ProjectPermissionGrantsManage), h.previewProjectGroupGrant)
	routes.POST("/:projectId/join", none, human, manage, visible, h.joinProject)
}

// refuseProjectQuery stops a request carrying any query string with 422
// validation_failed: the Project routes other than the list accept none, and
// an unknown parameter is never ignored (conventions.md).
func refuseProjectQuery(c *gin.Context) {
	if c.Request.URL.RawQuery == "" {
		return
	}
	writeProjectError(c, company.ErrProjectInvalidInput)
	c.Abort()
}

func (h *Handler) listProjects(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		writeProjectError(c, company.ErrProjectInvalidInput)
		return
	}
	include, manageView := false, false
	for key, values := range query {
		if len(values) != 1 {
			writeProjectError(c, company.ErrProjectInvalidInput)
			return
		}
		switch {
		case key == "include_archived" && (values[0] == "true" || values[0] == "false"):
			include = values[0] == "true"
		case key == "view" && values[0] == "manage":
			manageView = true
		default:
			writeProjectError(c, company.ErrProjectInvalidInput)
			return
		}
	}
	// The manage view lists Projects the caller cannot read; like every
	// management route it is a person's, never a Run's.
	if manageView && middleware.RefuseRunProof(c) {
		return
	}
	run, ok := h.projectRunScope(c, principal)
	if !ok {
		return
	}
	var result []company.ProjectResponse
	switch {
	case manageView:
		result, err = h.ProjectService.ListForManageView(c.Request.Context(), principal, include)
	case run != nil:
		result, err = h.ProjectService.ListForRun(c.Request.Context(), principal, include, run)
	default:
		result, err = h.ProjectService.List(c.Request.Context(), principal, include)
	}
	if err != nil {
		writeProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, projectCollectionResponse{Items: result})
}

func (h *Handler) getProjectAccessReport(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	report, err := h.ProjectService.AccessReport(c.Request.Context(), principal)
	if err != nil {
		writeProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, report)
}

func (h *Handler) createProject(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	request, err := bindRequest[createProjectRequest](c, 128<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	value, err := h.ProjectService.Create(
		c.Request.Context(),
		principal,
		company.ProjectCreateInput{Key: request.Key, Title: request.Title, Description: request.Description, Files: request.Files},
	)
	if err != nil {
		writeProjectError(c, err)
		return
	}
	c.JSON(http.StatusCreated, value)
}

// projectRequestIdentity reads the caller and the :projectId the route guard
// already resolved.
func projectRequestIdentity(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	id, err := util.ParseGoUUID(c.Param("projectId"))
	if err != nil {
		writeProjectError(c, company.ErrProjectNotFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, id, true
}

func (h *Handler) getProject(c *gin.Context) {
	principal, id, ok := projectRequestIdentity(c)
	if !ok {
		return
	}
	run, ok := h.projectRunScope(c, principal)
	if !ok {
		return
	}
	var value company.ProjectResponse
	var err error
	if run != nil {
		value, err = h.ProjectService.GetForRun(c.Request.Context(), principal, id, run)
	} else {
		value, err = h.ProjectService.Get(c.Request.Context(), principal, id)
	}
	if err != nil {
		writeProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}

// projectRunScope resolves the Run proof a Project read carries, as
// taskRunScope does for Task reads (ADR 0017). `zen project list` and `get`
// reach these two routes through a Run's channel; a proof that is not live
// in this Company, or whose session is not the Run's authorizer, is
// run_token_invalid. A member's own read carries no proof and gets a nil
// scope.
func (h *Handler) projectRunScope(c *gin.Context, principal auth.UserPrincipal) (*task.RunScope, bool) {
	return h.runReadScope(c, principal, writeProjectError)
}

func (h *Handler) updateProject(c *gin.Context) {
	principal, id, ok := projectRequestIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[updateProjectRequest](c, 128<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	input := company.ProjectUpdateInput{
		ExpectedRevision: request.ExpectedRevision, ExpectedAccessRevision: request.ExpectedAccessRevision.ptr(),
	}
	input.Title = request.Title.ptr()
	input.Description = request.Description.ptr()
	if visibility := request.Visibility.ptr(); visibility != nil {
		value := auth.ProjectVisibility(*visibility)
		input.Visibility = &value
	}
	value, err := h.ProjectService.Update(c.Request.Context(), principal, id, input)
	if err != nil {
		writeProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}

func (h *Handler) archiveProject(c *gin.Context) {
	principal, id, ok := projectRequestIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[archiveProjectRequest](c, 4<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	value, err := h.ProjectService.Archive(c.Request.Context(), principal, id, request.ExpectedRevision)
	if err != nil {
		writeProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}

func (h *Handler) replaceProjectFiles(c *gin.Context) {
	principal, id, ok := projectRequestIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[replaceProjectFilesRequest](c, 16<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	value, err := h.ProjectService.ReplaceFiles(
		c.Request.Context(),
		principal,
		id,
		company.ProjectFilesInput{LogoID: request.LogoID, DocumentIDs: request.DocumentIDs},
		request.ExpectedRevision,
	)
	if err != nil {
		writeProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}

func (h *Handler) listProjectGrants(c *gin.Context) {
	principal, id, ok := projectRequestIdentity(c)
	if !ok {
		return
	}
	grants, err := h.ProjectService.ListGrants(c.Request.Context(), principal, id)
	if err != nil {
		writeProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, grants)
}

// projectGrantTarget reads the :userId of a grant route. An id that does not
// parse is malformed input (422), like any other path or body shape error.
func projectGrantTarget(c *gin.Context) (uuid.UUID, bool) {
	userID, err := util.ParseGoUUID(c.Param("userId"))
	if err != nil {
		writeProjectError(c, company.ErrProjectInvalidInput)
		return uuid.Nil, false
	}
	return userID, true
}

func (h *Handler) setProjectGrant(c *gin.Context) {
	principal, id, ok := projectRequestIdentity(c)
	if !ok {
		return
	}
	userID, ok := projectGrantTarget(c)
	if !ok {
		return
	}
	request, err := bindRequest[setProjectGrantRequest](c, 4<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.ProjectService.SetGrants(c.Request.Context(), principal, id, company.ProjectGrantInput{
		UserID: userID, Permissions: request.Permissions, ExpectedRevision: request.ExpectedRevision,
	})
	if err != nil {
		writeProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) removeProjectGrant(c *gin.Context) {
	principal, id, ok := projectRequestIdentity(c)
	if !ok {
		return
	}
	userID, ok := projectGrantTarget(c)
	if !ok {
		return
	}
	request, err := bindRequest[projectAccessRevisionRequest](c, 4<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.ProjectService.RemoveGrants(c.Request.Context(), principal, id, userID, request.ExpectedRevision)
	if err != nil {
		writeProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) previewProjectGrant(c *gin.Context) {
	principal, id, ok := projectRequestIdentity(c)
	if !ok {
		return
	}
	userID, ok := projectGrantTarget(c)
	if !ok {
		return
	}
	request, err := bindRequest[previewProjectGrantRequest](c, 4<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	preview, err := h.ProjectService.PreviewGrant(c.Request.Context(), principal, id, userID, request.Permissions)
	if err != nil {
		writeProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, preview)
}

// projectGroupGrantTarget reads the :groupId of a Group grant route. An id
// that does not parse names no Group of the Company: 404 group_not_found.
func projectGroupGrantTarget(c *gin.Context) (uuid.UUID, bool) {
	groupID, err := util.ParseGoUUID(c.Param("groupId"))
	if err != nil {
		writeProjectError(c, company.ErrGroupNotFound)
		return uuid.Nil, false
	}
	return groupID, true
}

func (h *Handler) setProjectGroupGrant(c *gin.Context) {
	principal, id, ok := projectRequestIdentity(c)
	if !ok {
		return
	}
	groupID, ok := projectGroupGrantTarget(c)
	if !ok {
		return
	}
	request, err := bindRequest[setProjectGrantRequest](c, 4<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	input := company.ProjectGroupGrantInput{
		GroupID: groupID, Permissions: request.Permissions, ExpectedRevision: request.ExpectedRevision,
	}
	result, err := h.ProjectService.SetGroupGrant(c.Request.Context(), principal, id, input)
	if err != nil {
		writeProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) removeProjectGroupGrant(c *gin.Context) {
	principal, id, ok := projectRequestIdentity(c)
	if !ok {
		return
	}
	groupID, ok := projectGroupGrantTarget(c)
	if !ok {
		return
	}
	request, err := bindRequest[projectAccessRevisionRequest](c, 4<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.ProjectService.RemoveGroupGrant(
		c.Request.Context(), principal, id, groupID, request.ExpectedRevision,
	)
	if err != nil {
		writeProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) previewProjectGroupGrant(c *gin.Context) {
	principal, id, ok := projectRequestIdentity(c)
	if !ok {
		return
	}
	groupID, ok := projectGroupGrantTarget(c)
	if !ok {
		return
	}
	request, err := bindRequest[previewProjectGrantRequest](c, 4<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	preview, err := h.ProjectService.PreviewGroupGrant(
		c.Request.Context(),
		principal,
		id,
		groupID,
		request.Permissions,
	)
	if err != nil {
		writeProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, preview)
}

func (h *Handler) joinProject(c *gin.Context) {
	principal, id, ok := projectRequestIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[projectAccessRevisionRequest](c, 4<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	value, err := h.ProjectService.Join(c.Request.Context(), principal, id, request.ExpectedRevision)
	if err != nil {
		writeProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}

// projectPermissionUnknownMapping refuses a grant, a Group grant or a
// preview naming a permission outside the catalog, such as
// project.results.review, which a desktop built before migration 000102
// still sends. writeProjectError names the permission in the detail.
var projectPermissionUnknownMapping = errorMapping{
	auth.ErrProjectPermissionUnknown,
	http.StatusUnprocessableEntity,
	"Unknown Project permission",
	"project_permission_unknown",
}

var projectErrorMappings = []errorMapping{
	projectPermissionUnknownMapping,
	{
		company.ErrProjectInvalidInput,
		http.StatusUnprocessableEntity,
		"request validation failed",
		"validation_failed",
	},
	{
		company.ErrProjectForbidden,
		http.StatusForbidden,
		"Your Company role does not allow this action",
		"company_permission_required",
	},
	// A Project the caller cannot see answers exactly like a missing one.
	{company.ErrProjectNotFound, http.StatusNotFound, "Project not found", "project_not_found"},
	{auth.ErrProjectNotVisible, http.StatusNotFound, "Project not found", "project_not_found"},
	projectPermissionRequiredMapping,
	// A read sent through a Run's channel: a proof that is not the member's
	// live Run, and a Project the delegate rule keeps the Run out of (the
	// Run-scope sentence, which names nothing).
	runTokenInvalidMapping,
	taskOutsideRunScopeMapping,
	{
		company.ErrProjectConflict,
		http.StatusConflict,
		"Project revision or key conflicts with current state",
		"project_conflict",
	},
	{company.ErrProjectArchived, http.StatusConflict, "Project is archived", "project_archived"},
	{company.ErrProjectOpenTasks, http.StatusConflict, "Project has open Tasks", "project_open_tasks"},
	{
		company.ErrProjectMemberRequired,
		http.StatusConflict,
		"Project access can be given only to an active Company member",
		"project_member_required",
	},
	{
		company.ErrProjectGrantRevisionConflict,
		http.StatusConflict,
		"Project access changed; reload it and try again",
		"project_grant_revision_conflict",
	},
	// The self-grant cap names no permission it checked.
	{
		company.ErrProjectGrantExceedsOwn,
		http.StatusForbidden,
		"You can only give or change access you have on this Project",
		"project_grant_exceeds_own",
	},
	{
		company.ErrProjectVisibilityFloor,
		http.StatusConflict,
		"Everyone keeps read, task and run access on a Project open to the Company; make it restricted first",
		"project_visibility_floor",
	},
	// A Group grant (Access TECH D4) names a Group of the Company, and a
	// Project holds a limited number of them. Both answer as on the Group
	// routes.
	groupNotFoundMapping,
	groupLimitReachedMapping,
}

func writeProjectError(c *gin.Context, err error) {
	var unknown *auth.ProjectPermissionUnknownError
	if errors.As(err, &unknown) {
		mapping := projectPermissionUnknownMapping
		mapping.detail = fmt.Sprintf("Unknown Project permission %q", unknown.Name)
		writeMappedError(c, err, []errorMapping{mapping}, internalErrorFallback)
		return
	}
	writeMappedError(c, err, projectErrorMappings, internalErrorFallback)
}
