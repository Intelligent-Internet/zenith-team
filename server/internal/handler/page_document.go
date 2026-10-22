package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

func pageDocumentPath(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	p, ok := taskPrincipal(c)
	if !ok {
		return p, uuid.Nil, false
	}
	page, ok := canvasID(c, "pageId")
	return p, page, ok
}

func (h *Handler) readPageDocument(c *gin.Context) {
	p, page, ok := pageDocumentPath(c)
	if !ok {
		return
	}
	input, ok := documentReadRequest(c)
	if !ok {
		return
	}
	result, err := h.PageService.ReadPageDocument(
		c.Request.Context(),
		p,
		page,
		input,
	)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	if len(input.StateVector) > 0 && !input.Projection {
		result.Content = nil
		result.Markdown = ""
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) syncPageDocument(c *gin.Context) {
	p, page, ok := pageDocumentPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[blockDocumentRequest](c, 12<<20)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if !wiki.ValidBlockStateVector(input.StateVector) {
		writeCanvasError(c, canvas.ErrInvalid)
		return
	}
	result, err := h.PageService.SyncPageDocument(
		c.Request.Context(),
		p,
		page,
		canvas.DocumentSyncInput{
			Update:        input.Update,
			BaseSequence:  input.BaseSequence,
			SchemaVersion: input.SchemaVersion,
		},
	)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	result.State, result.StateVector, err = wiki.BlockStateDelta(result.State, input.StateVector)
	if err != nil {
		writeCanvasError(c, canvas.ErrInvalid)
		return
	}
	if len(input.StateVector) > 0 {
		result.Content = nil
		result.Markdown = ""
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) reservePageBlockIDs(c *gin.Context) {
	p, page, ok := pageDocumentPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[struct {
		Count int32 `json:"count" binding:"required,min=1,max=256"`
	}](c, 1024)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	ids, err := h.PageService.ReservePageBlockIDs(
		c.Request.Context(),
		p,
		page,
		input.Count,
	)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		IDs []uuid.UUID `json:"ids"`
	}{ids})
}

func (h *Handler) checkpointPageDocument(c *gin.Context) {
	p, page, ok := pageDocumentPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[struct {
		Sequence int64 `json:"sequence" binding:"required,min=1"`
	}](c, 1024)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	id, err := h.PageService.CheckpointPageDocument(
		c.Request.Context(),
		p,
		page,
		input.Sequence,
	)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		ID uuid.UUID `json:"id"`
	}{id})
}

func (h *Handler) pageDocumentPresence(c *gin.Context) {
	p, page, ok := pageDocumentPath(c)
	if !ok {
		return
	}
	request, err := bindRequest[canvasPresenceRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if request.Cursor != nil &&
		(request.Cursor.ClientID > 4294967295 || len(request.Cursor.Anchor) > 512 || len(request.Cursor.Head) > 512) {
		writeCanvasError(c, canvas.ErrInvalid)
		return
	}
	doc, err := h.PageService.ReadPageDocument(
		c.Request.Context(),
		p,
		page,
		canvas.DocumentReadInput{Since: 0, Projection: true, Preview: true},
	)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	user, err := h.AuthService.CurrentUser(c.Request.Context(), p.UserID)
	if err != nil {
		writeAuthError(c, err)
		return
	}
	h.writeCanvasPresence(
		c,
		canvasPresenceActor{
			principal: p,
			room:      p.CompanyID.String() + ":document:" + doc.PageID.String(),
			name:      user.DisplayName,
			request:   request,
		},
	)
}

func (h *Handler) pageReview(c *gin.Context) {
	p, page, ok := pageDocumentPath(c)
	if !ok {
		return
	}
	items, err := h.PageService.PageReview(c.Request.Context(), p, page)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Items []canvas.Thread `json:"items"`
	}{items})
}

func (h *Handler) mutatePageReview(c *gin.Context) {
	p, page, ok := pageDocumentPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[canvas.Command](c, 64<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.PageService.MutatePageReview(c.Request.Context(), p, page, input)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) pageChanges(c *gin.Context) {
	p, page, ok := pageDocumentPath(c)
	if !ok {
		return
	}
	result, err := h.PageService.PageChanges(c.Request.Context(), p, uuid.Nil, page, c.Query("cursor"))
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) pageChange(c *gin.Context) {
	p, page, ok := pageDocumentPath(c)
	if !ok {
		return
	}
	id, ok := canvasID(c, "changeId")
	if !ok {
		return
	}
	result, err := h.PageService.PageChangeDetail(c.Request.Context(), p, uuid.Nil, page, id)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) reviewPageChange(c *gin.Context) {
	p, page, ok := pageDocumentPath(c)
	if !ok {
		return
	}
	id, ok := canvasID(c, "changeId")
	if !ok {
		return
	}
	input, err := bindRequest[canvas.PageChangeReviewInput](c, 128<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.PageService.ReviewPageChange(c.Request.Context(), p, uuid.Nil, page, id, input)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func optionalPageProjectPath(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	if c.Param("projectId") != "" {
		return canvasPath(c)
	}
	p, ok := taskPrincipal(c)
	return p, uuid.Nil, ok
}
