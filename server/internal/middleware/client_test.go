package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestClientMetadataCapturesHeadersAndTreatsMissingAsUnknown(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	var platform, version, os string
	router := gin.New()
	router.Use(ClientMetadata())
	router.GET("/probe", func(c *gin.Context) {
		platform, version, os = ClientMetadataFromContext(c.Request.Context())
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/probe", nil)
	request.Header.Set(HeaderClientPlatform, "cli")
	request.Header.Set(HeaderClientVersion, "1.2.3")
	router.ServeHTTP(httptest.NewRecorder(), request)
	if platform != "cli" || version != "1.2.3" || os != "" {
		t.Fatalf("captured metadata = %q/%q/%q, want cli/1.2.3/(unknown)", platform, version, os)
	}

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/probe", nil))
	if platform != "" || version != "" || os != "" {
		t.Fatalf("headerless metadata = %q/%q/%q, want all unknown", platform, version, os)
	}
}

func TestSetClientMetadataFeedsNonHeaderTransports(t *testing.T) {
	ctx := SetClientMetadata(context.Background(), "desktop", "", "macos")
	platform, version, os := ClientMetadataFromContext(ctx)
	if platform != "desktop" || version != "" || os != "macos" {
		t.Fatalf("metadata = %q/%q/%q, want desktop/(unknown)/macos", platform, version, os)
	}
}
