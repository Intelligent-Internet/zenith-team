package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

func TestPasswordWorkOverloadReturnsRetryAfter(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	util.SetRequestID(c, "password-work-test")
	writeAuthError(c, fmt.Errorf("hash password: %w", auth.ErrPasswordWorkBusy))
	assertErrorResponse(t, recorder, http.StatusServiceUnavailable, "authentication_busy")
	if got := recorder.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
}
