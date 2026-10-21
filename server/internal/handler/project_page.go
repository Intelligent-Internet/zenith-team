package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

func (h *Handler) registerPageRoutes(api *gin.RouterGroup) {
	if h.PageService == nil || h.PresentationService == nil || h.AuthService == nil || h.CompanyService == nil {
		return
	}
	human := api.Group(
		"/companies/:companyId/projects/:projectId",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
		middleware.RequireHumanActor(),
	)
	edit := middleware.RequireProjectPermission(h.ProjectService, auth.ProjectPermissionTasksWrite)
	human.GET("/overview", h.getProjectOverview)
	human.POST("/overview", edit, h.updateProjectOverview)
	human.GET("/pages", h.listProjectPages)
	human.POST("/pages", edit, h.createProjectPage)
	human.GET("/pages/:pageId", h.getProjectPage)
	human.PATCH("/pages/:pageId", edit, h.updateProjectPage)
	human.GET("/pages/:pageId/threads", h.listProjectPageThreads)
	human.GET("/pages/:pageId/changes", h.listProjectPageChanges)
	human.GET("/pages/:pageId/changes/:changeId", h.getProjectPageChange)
	human.POST("/pages/:pageId/changes/:changeId", edit, h.reviewProjectPageChange)
	human.GET("/pages/:pageId/presentation-preview", edit, h.previewProjectPresentation)
	human.GET("/pages/:pageId/presentations", h.listProjectPresentations)
	human.POST("/pages/:pageId/presentations", edit, h.saveProjectPresentation)
	human.GET("/presentations", h.listProjectPresentations)
	human.GET("/presentations/:presentationId", h.getProjectPresentation)
	human.GET("/presentations/:presentationId/export", h.exportProjectPresentation)
	agent := api.Group(
		"/companies/:companyId/runs/:runId",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	agent.GET("/agent-project", h.readProjectForRun)
	agent.GET("/agent-sources", h.readPageSource)
	agent.POST("/agent-pages", h.createProjectPageForRun)
	agent.GET("/agent-pages/:pageId", h.readProjectPageForRun)
	agent.POST("/agent-pages/:pageId", h.editProjectPageForRun)
	agent.GET("/agent-pages/:pageId/presentation-preview", h.previewProjectPresentationForRun)
	agent.POST("/agent-pages/:pageId/presentations", h.prepareProjectPresentationForRun)
}

func (h *Handler) listProjectPages(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	var taskID *uuid.UUID
	if value := c.Query("taskId"); value != "" {
		id, err := util.ParseGoUUID(value)
		if err != nil {
			writeCanvasError(c, canvas.ErrInvalid)
			return
		}
		taskID = &id
	}
	result, err := h.PageService.Pages(c.Request.Context(), p, project, taskID)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getProjectPage(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	page, ok := canvasID(c, "pageId")
	if !ok {
		return
	}
	result, err := h.PageService.Page(c.Request.Context(), p, project, page)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) updateProjectPage(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	page, ok := canvasID(c, "pageId")
	if !ok {
		return
	}
	in, err := bindRequest[canvas.PageMetadataInput](c, 4<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.PageService.UpdatePage(c.Request.Context(), p, project, page, in)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) listProjectPageChanges(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	page, ok := canvasID(c, "pageId")
	if !ok {
		return
	}
	result, err := h.PageService.PageChanges(c.Request.Context(), p, project, page, c.Query("cursor"))
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) createProjectPageForRun(c *gin.Context) {
	p, run, ok := h.contentRunPrincipal(c)
	if !ok {
		return
	}
	in, err := bindRequest[canvas.PageCreateInput](c, 256<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.PageService.CreatePageForRun(c.Request.Context(), p, run, in)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) readProjectPageForRun(c *gin.Context) {
	p, run, ok := h.contentRunPrincipal(c)
	if !ok {
		return
	}
	page, ok := canvasID(c, "pageId")
	if !ok {
		return
	}
	result, err := h.PageService.ReadPageForRun(c.Request.Context(), p, run, page)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) editProjectPageForRun(c *gin.Context) {
	p, run, ok := h.contentRunPrincipal(c)
	if !ok {
		return
	}
	page, ok := canvasID(c, "pageId")
	if !ok {
		return
	}
	in, err := bindRequest[canvas.PageEditInput](c, 256<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.PageService.EditPageForRun(c.Request.Context(), p, run, page, in)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getProjectOverview(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	result, err := h.PageService.Overview(c.Request.Context(), p, project)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) updateProjectOverview(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	in, err := bindRequest[canvas.OverviewInput](c, 16<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.PageService.UpdateOverview(c.Request.Context(), p, project, in)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) previewProjectPresentation(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	page, ok := canvasID(c, "pageId")
	if !ok {
		return
	}
	sequence := int64(-1)
	if value := c.Query("sequence"); value != "" {
		var err error
		sequence, err = strconv.ParseInt(value, 10, 64)
		if err != nil || sequence < 0 {
			writeCanvasError(c, canvas.ErrInvalid)
			return
		}
	}
	result, err := h.PresentationService.PreviewPresentation(c.Request.Context(), p, project, page, sequence)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) listProjectPresentations(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	var page uuid.UUID
	if c.Param("pageId") != "" {
		page, ok = canvasID(c, "pageId")
		if !ok {
			return
		}
	}
	result, err := h.PresentationService.Presentations(c.Request.Context(), p, project, page)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) saveProjectPresentation(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	page, ok := canvasID(c, "pageId")
	if !ok {
		return
	}
	in, err := bindRequest[pageCaptureRequest](c, 16<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.PresentationService.CapturePresentation(
		c.Request.Context(),
		p,
		project,
		canvas.PresentationInput{
			PageID:      page,
			RequestKey:  in.RequestKey,
			Sequence:    in.Sequence,
			Fingerprint: in.Fingerprint,
		},
	)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getProjectPresentation(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	id, ok := canvasID(c, "presentationId")
	if !ok {
		return
	}
	result, err := h.PresentationService.Presentation(c.Request.Context(), p, project, id)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) exportProjectPresentation(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	id, ok := canvasID(c, "presentationId")
	if !ok {
		return
	}
	body, err := h.PresentationService.PresentationExport(c.Request.Context(), p, project, id)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.Header("Content-Disposition", "attachment; filename=\"project-presentation.md\"")
	c.Data(http.StatusOK, "text/markdown; charset=utf-8", []byte(body))
}

func (h *Handler) getProjectPageChange(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	page, ok := canvasID(c, "pageId")
	if !ok {
		return
	}
	id, ok := canvasID(c, "changeId")
	if !ok {
		return
	}
	result, err := h.PageService.PageChangeDetail(c.Request.Context(), p, project, page, id)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) reviewProjectPageChange(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	page, ok := canvasID(c, "pageId")
	if !ok {
		return
	}
	id, ok := canvasID(c, "changeId")
	if !ok {
		return
	}
	in, err := bindRequest[canvas.PageChangeReviewInput](c, 128<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.PageService.ReviewPageChange(c.Request.Context(), p, project, page, id, in)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) readProjectForRun(c *gin.Context) {
	p, run, ok := h.contentRunPrincipal(c)
	if !ok {
		return
	}
	result, err := h.PageService.ReadProjectForRun(c.Request.Context(), p, run)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) previewProjectPresentationForRun(c *gin.Context) {
	p, run, ok := h.contentRunPrincipal(c)
	if !ok {
		return
	}
	page, ok := canvasID(c, "pageId")
	if !ok {
		return
	}
	var result canvas.Presentation
	var err error
	if value := c.Query("changeId"); value != "" {
		change, parseErr := util.ParseGoUUID(value)
		if parseErr != nil {
			writeCanvasError(c, canvas.ErrInvalid)
			return
		}
		result, err = h.PresentationService.PreviewProposalForRun(c.Request.Context(), p, run, page, change)
	} else {
		result, err = h.PresentationService.PreviewPresentationForRun(c.Request.Context(), p, run, page)
	}
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) prepareProjectPresentationForRun(c *gin.Context) {
	p, run, ok := h.contentRunPrincipal(c)
	if !ok {
		return
	}
	page, ok := canvasID(c, "pageId")
	if !ok {
		return
	}
	in, err := bindRequest[pageCaptureRequest](c, 16<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.PresentationService.PreparePresentationForRun(
		c.Request.Context(),
		p,
		run,
		canvas.PresentationInput{
			ChangeID:    in.ChangeID,
			PageID:      page,
			RequestKey:  in.RequestKey,
			Sequence:    in.Sequence,
			Fingerprint: in.Fingerprint,
		},
	)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) readPageSource(c *gin.Context) {
	p, run, ok := h.contentRunPrincipal(c)
	if !ok {
		return
	}
	version, err := util.ParseGoUUID(c.Query("versionId"))
	if err != nil {
		writeCanvasError(c, canvas.ErrInvalid)
		return
	}
	block := c.Query("blockId")
	if block != "" {
		if _, err = util.ParseGoUUID(block); err != nil {
			writeCanvasError(c, canvas.ErrInvalid)
			return
		}
	}
	result, err := h.PresentationService.ReadPageSource(c.Request.Context(), p, run, version, block)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

type pageCaptureRequest struct {
	ChangeID    *uuid.UUID `json:"changeId,omitempty"`
	RequestKey  uuid.UUID  `json:"requestKey"         binding:"required"`
	Sequence    int64      `json:"sequence"           binding:"min=0"`
	Fingerprint string     `json:"fingerprint"        binding:"required,len=64"`
}

func (h *Handler) createProjectPage(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	in, err := bindRequest[canvas.PageCreateInput](c, 256<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.PageService.CreatePage(c.Request.Context(), p, project, in)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) listProjectPageThreads(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	page, ok := canvasID(c, "pageId")
	if !ok {
		return
	}
	result, err := h.PageService.PageThreads(c.Request.Context(), p, project, page)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Items []canvas.Thread `json:"items"`
	}{result})
}
