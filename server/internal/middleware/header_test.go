package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

// A service below the handler holds only the request's context.Context, and
// its log lines must quote the same id as the error envelope.
func TestRequestIDReachesTheRequestContext(t *testing.T) {
	t.Parallel()

	var fromGin, fromContext string
	router := gin.New()
	router.Use(RequestID())
	router.GET("/probe", func(c *gin.Context) {
		fromGin = util.RequestIDFromContext(c)
		fromContext = util.ContextRequestID(c.Request.Context())
		c.Status(http.StatusNoContent)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/probe", nil))

	if fromGin == "" || fromContext != fromGin {
		t.Fatalf("request context id = %q, gin id = %q, want the same non-empty id", fromContext, fromGin)
	}
}
