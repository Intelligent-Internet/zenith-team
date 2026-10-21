package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

// registerWikiAgentRoutes mounts the Wiki an Agent's Run uses and the members'
// evidence views. Every Run reads; only the founder's company-research Run
// writes, which the service proves in its own transaction. The agent-wiki
// routes are Run-proven, so they do not sit under /wiki/ (that group is
// human-only) and do not require a human actor; the evidence views are
// human-only.
func (h *Handler) registerWikiAgentRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.WikiAgentReadService == nil {
		return
	}
	agent := group.Group("/companies/:companyId/runs/:runId/agent-wiki",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	agent.GET("/search", h.searchWikiForRun)
	agent.GET("/list", h.listWikiForRun)
	agent.GET("/pages/:pageId", h.readWikiPageForRun)
	if h.WikiAgentWriteService != nil {
		agent.POST("/pages", h.writeWikiPagesForRun)
	}

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

// listWikiForRun browses the active published pages for a Run: optional
// collectionId, offset and limit.
func (h *Handler) listWikiForRun(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	principal, runID, ok := h.contentRunPrincipal(c)
	if !ok {
		return
	}
	var collectionID *uuid.UUID
	if raw := c.Query("collectionId"); raw != "" {
		parsed, err := util.ParseGoUUID(raw)
		if err != nil {
			writeWikiError(c, wiki.ErrPageInvalidInput)
			return
		}
		collectionID = &parsed
	}
	offset, ok := wikiQueryInt(c, "offset", 0)
	if !ok {
		return
	}
	limit, ok := wikiQueryInt(c, "limit", wiki.DefaultAgentListLimit)
	if !ok {
		return
	}
	result, err := h.WikiAgentReadService.List(c.Request.Context(), principal, runID, collectionID, offset, limit)
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

// maxWikiAgentWriteBytes caps one write request: twelve research pages of
// ordinary size fit, and a larger batch is the Run's to split across calls.
const maxWikiAgentWriteBytes = 1 << 20

type wikiAgentWritePage struct {
	Key      string `json:"key"      binding:"required,max=64"`
	Title    string `json:"title"    binding:"required,max=1000"`
	Summary  string `json:"summary"  binding:"max=10000"`
	Markdown string `json:"markdown"`
}

type wikiAgentWriteRequest struct {
	Pages []wikiAgentWritePage `json:"pages" binding:"required,min=1,max=12,dive"`
}

func (r wikiAgentWriteRequest) domainInput() wiki.AgentWriteInput {
	input := wiki.AgentWriteInput{Pages: make([]wiki.AgentWritePageInput, len(r.Pages))}
	for i, page := range r.Pages {
		input.Pages[i] = wiki.AgentWritePageInput{
			Key:      page.Key,
			Title:    page.Title,
			Summary:  page.Summary,
			Markdown: page.Markdown,
		}
	}
	return input
}

// writeWikiPagesForRun saves pages for the founder's company-research Run. The
// Idempotency-Key is required; replaying the same body is safe because the
// service reports unchanged pages instead of writing them again.
func (h *Handler) writeWikiPagesForRun(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	principal, runID, ok := h.contentRunPrincipal(c)
	if !ok {
		return
	}
	if _, err := util.ParseGoUUID(c.GetHeader(idempotencyHeader)); err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	request, err := bindRequest[wikiAgentWriteRequest](c, maxWikiAgentWriteBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.WikiAgentWriteService.Write(c.Request.Context(), principal, runID, request.domainInput())
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
