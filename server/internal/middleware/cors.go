package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/util"
)

// CORSOptions configures the credentialed allowlist.
type CORSOptions struct {
	// AllowedOrigins is an exact-match allowlist; there is no wildcard.
	AllowedOrigins []string
	// BypassPaths skip CORS entirely. The Client WebSocket route is the only
	// current entry: it distinguishes strict cookie+Origin browser admission
	// from no-cookie desktop token admission, and generic HTTP CORS cannot
	// safely make that credential-aware decision.
	BypassPaths []string
}

// CORS applies the credentialed allowlist, rejecting unknown and literal
// "null" origins with the standard error envelope.
func CORS(options CORSOptions) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(options.AllowedOrigins))
	for _, origin := range options.AllowedOrigins {
		allowed[origin] = struct{}{}
	}
	bypass := make(map[string]struct{}, len(options.BypassPaths))
	for _, path := range options.BypassPaths {
		bypass[path] = struct{}{}
	}
	return func(c *gin.Context) {
		if _, skip := bypass[c.Request.URL.Path]; skip {
			c.Next()
			return
		}
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}
		c.Header("Vary", "Origin")
		if _, ok := allowed[origin]; !ok || origin == "null" {
			util.WriteError(c, http.StatusForbidden, "origin is not allowed", "origin_forbidden")
			c.Abort()
			return
		}

		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header(
			"Access-Control-Allow-Headers",
			"Authorization, Content-Type, Idempotency-Key, X-Boardly-Client, X-Boardly-Run-Token, X-CSRF-Token, X-Request-ID",
		)
		c.Header("Access-Control-Expose-Headers", "X-Request-ID")
		if c.Request.Method == http.MethodOptions {
			// Reuse preflight approval without caching authenticated responses.
			c.Header("Access-Control-Max-Age", "600")
			c.Status(http.StatusNoContent)
			c.Abort()
			return
		}
		c.Next()
	}
}
