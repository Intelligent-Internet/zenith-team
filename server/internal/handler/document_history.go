package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/util"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
)

func historyPath(c *gin.Context) (auth.UserPrincipal, canvas.DocumentHistoryRef, bool) {
	if c.Param("artifactId") != "" {
		p, output, item, ok := outputDocumentPath(c)
		return p, canvas.DocumentHistoryRef{OutputID: output, ItemID: item}, ok
	}
	p, project, ok := canvasPath(c)
	if !ok {
		return p, canvas.DocumentHistoryRef{}, false
	}
	version, ok := canvasID(c, "versionId")
	return p, canvas.DocumentHistoryRef{ProjectID: project, VersionID: version}, ok
}

func historySequence(c *gin.Context) (int64, bool) {
	sequence, err := strconv.ParseInt(c.Param("sequence"), 10, 64)
	if err != nil || sequence < 1 {
		writeCanvasError(c, canvas.ErrInvalid)
		return 0, false
	}
	return sequence, true
}

func (h *Handler) documentHistory(c *gin.Context) {
	p, ref, ok := historyPath(c)
	if !ok {
		return
	}
	var input canvas.DocumentHistoryInput
	if raw := c.Query("actor_id"); raw != "" {
		id, err := util.ParseGoUUID(raw)
		if err != nil {
			writeCanvasError(c, canvas.ErrInvalid)
			return
		}
		input.ActorID = &id
	}
	for name, dst := range map[string]**time.Time{"after": &input.After, "before": &input.Before} {
		if raw := c.Query(name); raw != "" {
			value, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				writeCanvasError(c, canvas.ErrInvalid)
				return
			}
			*dst = &value
		}
	}
	if raw := c.Query("before_sequence"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			writeCanvasError(c, canvas.ErrInvalid)
			return
		}
		input.BeforeSequence = value
	}
	if input.After != nil && input.Before != nil && !input.After.Before(*input.Before) {
		writeCanvasError(c, canvas.ErrInvalid)
		return
	}
	result, err := h.PageService.DocumentHistory(c.Request.Context(), p, ref, input)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) documentHistoryState(c *gin.Context) {
	p, ref, ok := historyPath(c)
	if !ok {
		return
	}
	sequence, ok := historySequence(c)
	if !ok {
		return
	}
	result, err := h.PageService.DocumentHistoryState(c.Request.Context(), p, ref, sequence)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) restoreDocumentHistory(c *gin.Context) {
	p, ref, ok := historyPath(c)
	if !ok {
		return
	}
	sequence, ok := historySequence(c)
	if !ok {
		return
	}
	input, err := bindRequest[struct {
		ExpectedSequence int64 `json:"expectedSequence" binding:"required,min=1"`
	}](c, 1024)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.PageService.RestoreDocumentHistory(
		c.Request.Context(),
		p,
		ref,
		canvas.DocumentHistoryRestore{Sequence: sequence, ExpectedSequence: input.ExpectedSequence},
	)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) nameDocumentHistory(c *gin.Context) {
	p, ref, ok := historyPath(c)
	if !ok {
		return
	}
	sequence, ok := historySequence(c)
	if !ok {
		return
	}
	input, err := bindRequest[struct {
		Label string `json:"label" binding:"max=200"`
	}](c, 2048)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if err = h.PageService.NameDocumentHistory(
		c.Request.Context(),
		p,
		ref,
		canvas.DocumentHistoryName{Sequence: sequence, Label: input.Label},
	); err != nil {
		writeCanvasError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
