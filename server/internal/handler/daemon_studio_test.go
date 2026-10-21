package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
)

func TestStudioProjectRequiresDaemonIdentity(t *testing.T) {
	t.Parallel()
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/studio-project", nil)
	// Reject before any dependency or database access.
	(&Handler{}).daemonStudioProject(c)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestStudioProjectErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []mappedErrorCase{
		{"scope", conversation.ErrStudioProjectNotFound, 404, "Studio project Run not found", "studio_project_run_not_found"},
		{"input", conversation.ErrStudioProjectInvalid, 422, "invalid Studio project operation", "studio_project_invalid"},
		{"binding", conversation.ErrStudioProjectConflict, 409, "Studio project is already bound", "studio_project_conflict"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assertMappedErrorResponse(
				t,
				func(c *gin.Context) { writeMappedError(c, test.err, studioProjectErrorMappings, internalErrorFallback) },
				test.status,
				test.detail,
				test.code,
			)
		})
	}
}
