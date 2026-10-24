package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
)

func (h *Handler) getPresentationPublication(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	id, ok := canvasID(c, "presentationId")
	if !ok {
		return
	}
	result, err := h.PresentationService.Publication(c.Request.Context(), p, project, id)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) publishPresentation(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	id, ok := canvasID(c, "presentationId")
	if !ok {
		return
	}
	in, err := bindRequest[canvas.PublicationInput](c, 4<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.PresentationService.PublishPresentation(c.Request.Context(), p, project, id, in)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) revokePresentationPublication(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	id, ok := canvasID(c, "presentationId")
	if !ok {
		return
	}
	publication, ok := canvasID(c, "publicationId")
	if !ok {
		return
	}
	if err := h.PresentationService.RevokePublication(
		c.Request.Context(),
		p,
		project,
		id,
		publication,
	); err != nil {
		writeCanvasError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) publicPresentation(c *gin.Context) {
	token, ok := canvasID(c, "token")
	if !ok {
		return
	}
	html, err := h.PresentationService.PublicPresentationHTML(c.Request.Context(), token)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Robots-Tag", "noindex, nofollow, noarchive")
	c.Header(
		"Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; sandbox",
	)
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}
