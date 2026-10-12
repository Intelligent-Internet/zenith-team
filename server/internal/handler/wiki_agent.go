package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/util"
	"boardly.dev/boardly/server/internal/wiki"
)

// registerWikiAgentRoutes mounts the read-only Wiki an Agent's Run uses and
// the members' evidence views. The agent-wiki routes are Run-proven, so they
// do not sit under /wiki/ (that group is human-only) and do not require a
// human actor; the evidence views are human-only.
func (h *Handler) registerWikiAgentRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.WikiAgentReadService == nil {
		return
	}
	agent := group.Group("/companies/:companyId/runs/:runId/agent-wiki",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	agent.GET("/search", h.searchWikiForRun)
	agent.GET("/pages/:pageId", h.readWikiPageForRun)

	human := group.Group("/companies/:companyId",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
		middleware.RequireHumanActor(),
	)
	human.GET("/runs/:runId/wiki-reads", h.listRunWikiReads)
	human.GET("/wiki/pages/:pageId/agent-reads", h.listWikiPageAgentReads)
}

// wikiQueryInt reads an optional integer query parameter.
func wikiQueryInt(c *gin.Context, name string, fallback int) (int, bool) {
	value := c.Query(name)
	if value == "" {
		return fallback, true
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		writeWikiError(c, wiki.ErrPageInvalidInput)
		return 0, false
	}
	return int(parsed), true
}

func (h *Handler) searchWikiForRun(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	principal, runID, ok := h.contentRunPrincipal(c)
	if !ok {
		return
	}
	limit, ok := wikiQueryInt(c, "limit", wiki.DefaultAgentSearchLimit)
	if !ok {
		return
	}
	result, err := h.WikiAgentReadService.Search(c.Request.Context(), principal, runID, c.Query("q"), limit)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) readWikiPageForRun(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	principal, runID, ok := h.contentRunPrincipal(c)
	if !ok {
		return
	}
	pageID, ok := wikiID(c, "pageId")
	if !ok {
		return
	}
	offset, ok := wikiQueryInt(c, "offset", 0)
	if !ok {
		return
	}
	result, err := h.WikiAgentReadService.Read(c.Request.Context(), principal, runID, pageID, offset)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) listRunWikiReads(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	// A malformed id names a Run that cannot exist: 404, like the Run routes.
	runID, err := util.ParseGoUUID(c.Param("runId"))
	if err != nil {
		writeWikiError(c, wiki.ErrRunNotFound)
		return
	}
	result, err := h.WikiAgentReadService.ListRunReads(c.Request.Context(), principal, runID)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) listWikiPageAgentReads(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	pageID, ok := wikiID(c, "pageId")
	if !ok {
		return
	}
	limit, ok := wikiQueryInt(c, "limit", wiki.DefaultPageAgentReadsLimit)
	if !ok {
		return
	}
	result, err := h.WikiAgentReadService.ListPageAgentReads(c.Request.Context(), principal, pageID, limit)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
