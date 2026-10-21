package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

func outputDocumentPath(c *gin.Context) (auth.UserPrincipal, uuid.UUID, uuid.UUID, bool) {
	p, output, ok := artifactPathRequest(c)
	if !ok {
		return p, output, uuid.Nil, false
	}
	item, ok := canvasID(c, "outputItemId")
	return p, output, item, ok
}

func (h *Handler) openOutputDocument(c *gin.Context) {
	p, output, item, ok := outputDocumentPath(c)
	if !ok {
		return
	}
	id, err := h.CanvasService.OpenOutputDocument(
		c.Request.Context(),
		p,
		canvas.OutputDocumentRef{OutputID: output, ItemID: item},
	)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		PageID uuid.UUID `json:"pageId"`
	}{id})
}

func (h *Handler) readOutputDocument(c *gin.Context) {
	p, output, item, ok := outputDocumentPath(c)
	if !ok {
		return
	}
	input, ok := documentReadRequest(c)
	if !ok {
		return
	}
	result, err := h.PageService.ReadOutputDocument(
		c.Request.Context(),
		p,
		canvas.OutputDocumentRef{OutputID: output, ItemID: item},
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

func (h *Handler) syncOutputDocument(c *gin.Context) {
	p, output, item, ok := outputDocumentPath(c)
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
	result, err := h.PageService.SyncOutputDocument(
		c.Request.Context(),
		p,
		canvas.OutputDocumentRef{OutputID: output, ItemID: item},
		canvas.DocumentSyncInput{Update: input.Update, BaseSequence: input.BaseSequence},
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

func (h *Handler) reserveOutputBlockIDs(c *gin.Context) {
	p, output, item, ok := outputDocumentPath(c)
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
	ids, err := h.PageService.ReserveOutputBlockIDs(
		c.Request.Context(),
		p,
		canvas.OutputDocumentRef{OutputID: output, ItemID: item},
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

func (h *Handler) checkpointOutputDocument(c *gin.Context) {
	p, output, item, ok := outputDocumentPath(c)
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
	id, err := h.PageService.CheckpointOutputDocument(
		c.Request.Context(),
		p,
		canvas.OutputDocumentRef{OutputID: output, ItemID: item},
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

func (h *Handler) outputDocumentVersion(c *gin.Context) {
	p, output, ok := artifactPathRequest(c)
	if !ok {
		return
	}
	version, ok := canvasID(c, "versionId")
	if !ok {
		return
	}
	result, err := h.PageService.OutputDocumentVersion(c.Request.Context(), p, output, version)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) outputDocumentPresence(c *gin.Context) {
	p, output, item, ok := outputDocumentPath(c)
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
	doc, err := h.PageService.ReadOutputDocument(
		c.Request.Context(),
		p,
		canvas.OutputDocumentRef{OutputID: output, ItemID: item},
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
