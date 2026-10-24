package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

// storedText names one agent-authored value bound for a destination that has
// no Intelligent UI renderer: Wiki pages, mail, calendar events and questions
// (including the social posts they carry).
type storedText struct {
	field string
	text  string
}

// rejectInlineUI fails the request at its boundary when stored or external
// text carries component source, so the Agent can rewrite it in the same Run
// instead of saving markup nobody can render.
func rejectInlineUI(c *gin.Context, values ...storedText) bool {
	for _, value := range values {
		if wiki.ContainsInlineUI(value.text) {
			writeBoundaryErrorDetail(
				c,
				boundaryErrorValidationFailed,
				value.field+" is stored without an Intelligent UI renderer. Write plain Markdown or text there, without components.",
			)
			return true
		}
	}
	return false
}
