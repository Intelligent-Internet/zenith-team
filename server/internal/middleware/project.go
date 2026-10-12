package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/util"
)

// RequireProjectVisible admits a caller who can see the :projectId Project:
// a reader, or a projects.manage holder through the management bridge
// (ADR 0017). Anything else is 404 project_not_found, the answer a missing
// Project gets, so the refusal never tells a Project exists. Mount it after
// RequireCompanyMember, which resolves the Company on the principal.
func RequireProjectVisible(service company.ProjectService) gin.HandlerFunc {
	return requireProjectAccess(service, "")
}

// RequireProjectPermission admits a caller who holds the permission on the
// :projectId Project. A Project they cannot see is 404 project_not_found; a
// visible one without the permission is 403 project_permission_required.
// Both land before the handler reads the body.
func RequireProjectPermission(service company.ProjectService, permission auth.ProjectPermission) gin.HandlerFunc {
	return requireProjectAccess(service, permission)
}

func requireProjectAccess(service company.ProjectService, permission auth.ProjectPermission) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
		if !ok {
			util.WriteError(
				c,
				http.StatusUnauthorized,
				"authentication is required",
				"authentication_required",
			)
			c.Abort()
			return
		}
		projectID, err := util.ParseGoUUID(c.Param("projectId"))
		if err != nil {
			util.WriteError(c, http.StatusNotFound, "Project not found", "project_not_found")
			c.Abort()
			return
		}
		access, err := service.Authorize(c.Request.Context(), principal, projectID)
		switch {
		case errors.Is(err, auth.ErrProjectNotVisible), errors.Is(err, company.ErrProjectNotFound):
			util.WriteError(c, http.StatusNotFound, "Project not found", "project_not_found")
			c.Abort()
			return
		case err != nil:
			util.WriteError(c, http.StatusInternalServerError, "internal server error", "internal_error")
			c.Abort()
			return
		case permission != "" && !access.Can(permission):
			util.WriteError(
				c,
				http.StatusForbidden,
				"Your access to this Project does not allow this action",
				"project_permission_required",
			)
			c.Abort()
			return
		}
		c.Next()
	}
}
