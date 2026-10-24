package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
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
