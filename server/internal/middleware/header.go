package middleware

import (
	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/util"

	"github.com/google/uuid"
)

// RequestID accepts an inbound X-Request-ID only when it is a canonical
// UUID, so a client cannot inject arbitrary text into logs, and mints a
// UUIDv7 otherwise. The value is echoed back on the response.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := validInboundRequestID(c.GetHeader("X-Request-ID"))
		if requestID == "" {
			requestID = uuid.Must(uuid.NewV7()).String()
		}
		util.SetRequestID(c, requestID)
		c.Request = c.Request.WithContext(util.WithRequestID(c.Request.Context(), requestID))
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}

// OAuthSecurityHeaders applies the no-store / no-referrer policy to the
// credential-bearing auth endpoints. webCallbackPath is the Google OAuth web
// callback; clearWebBinding removes its browser-binding cookie once the
// callback has been served (internal/handler owns that cookie).
func OAuthSecurityHeaders(
	cookieSecure bool,
	webCallbackPath string,
	clearWebBinding func(c *gin.Context, secure bool),
) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isOAuthHTTPPath(c.Request.URL.Path, webCallbackPath) || isPasswordResetHTTPPath(c.Request.URL.Path) {
			c.Header("Cache-Control", "no-store")
			c.Header("Referrer-Policy", "no-referrer")
		}
		if c.Request.URL.Path == webCallbackPath && clearWebBinding != nil {
			clearWebBinding(c, cookieSecure)
		}
		c.Next()
	}
}

func isPasswordResetHTTPPath(path string) bool {
	return path == "/api/v1/auth/password-reset/request" ||
		path == "/api/v1/auth/password-reset/confirm"
}

func isOAuthHTTPPath(path, webCallbackPath string) bool {
	switch path {
	case "/api/v1/auth/providers",
		"/api/v1/auth/google/web/start",
		webCallbackPath,
		"/api/v1/auth/google/desktop/start",
		"/api/v1/auth/google/desktop/complete",
		"/api/v1/auth/desktop/handoff/start",
		"/api/v1/auth/desktop/handoff/complete":
		return true
	default:
		return false
	}
}

// NoStore marks every response on the route group as uncacheable.
func NoStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}

func validInboundRequestID(raw string) string {
	if len(raw) != 36 {
		return ""
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.String()
}

// PrivateNoStore keeps authorized resource projections out of HTTP caches.
func PrivateNoStore() gin.HandlerFunc {
	return func(c *gin.Context) { c.Header("Cache-Control", "private, no-store"); c.Next() }
}
