package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/mcp/tools"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/platformbundle"
)

// registerPlatformSkillRoutes serves the platform skills — the release-owned
// procedures every attended Run can load through search_skills /
// load_skill_reference. The list is read from the same compiled and pinned
// files the MCP server searches, so the app never names a skill that does
// not ship. Any member may read it: it carries no Company data.
func (h *Handler) registerPlatformSkillRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil {
		return
	}
	group.GET(
		"/companies/:companyId/platform-skills",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
		h.listPlatformSkills,
	)
}

type platformSkillCollectionResponse struct {
	Items []tools.PlatformSkillSummary `json:"items"`
}

func (h *Handler) listPlatformSkills(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	if _, ok := auth.UserPrincipalFromContext(c.Request.Context()); !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	items, err := platformbundle.PlatformSkillSummaries()
	if err != nil {
		writeBoundaryError(c, boundaryErrorInternal)
		return
	}
	c.JSON(http.StatusOK, platformSkillCollectionResponse{Items: items})
}
