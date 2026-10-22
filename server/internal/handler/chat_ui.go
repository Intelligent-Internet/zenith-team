package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

// previewChatUI compiles caller-supplied source only. It reads no product data
// and persists nothing; live sources are authorized separately by their APIs.
func (h *Handler) previewChatUI(c *gin.Context) {
	input, err := bindRequest[struct {
		Source          string `json:"source" binding:"max=262144"`
		LanguageVersion int    `json:"languageVersion" binding:"required,eq=1"`
	}](c, 512<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, wiki.PreviewChatSource(input.Source, input.LanguageVersion))
}
