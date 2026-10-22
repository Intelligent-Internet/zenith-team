package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

func TestSourceDiagnosticUsesValidationEnvelope(t *testing.T) {
	t.Parallel()
	_, err := wiki.CompileSource("<box padding=sensitive-value />", 1)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	writeCanvasError(ctx, err)
	var body struct{ Code, Detail string }
	if json.Unmarshal(response.Body.Bytes(), &body) != nil || response.Code != http.StatusUnprocessableEntity ||
		body.Code != "validation_failed" {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(body.Detail, "source.syntax") || !strings.Contains(body.Detail, "bytes") ||
		strings.Contains(body.Detail, "sensitive-value") {
		t.Fatalf("unsafe or missing repair detail: %s", body.Detail)
	}
}
