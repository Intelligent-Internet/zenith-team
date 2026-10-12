package util

import (
	"github.com/gin-gonic/gin"
)

// requestIDKey is the gin context key holding the request identifier the
// RequestID middleware resolved. It stays private so SetRequestID and
// RequestIDFromContext are the only ways in and out.
const requestIDKey = "requestID"

// SetRequestID stores the identifier for this request. internal/middleware's
// RequestID is its only production caller; tests use it to seed a synthetic
// context.
func SetRequestID(c *gin.Context, requestID string) {
	c.Set(requestIDKey, requestID)
}

// RequestIDFromContext returns the identifier SetRequestID stored, or "" when
// the RequestID middleware has not run. The error envelope, the request log
// line, and handlers building their own body all read it here, so they quote
// the same id.
func RequestIDFromContext(c *gin.Context) string {
	value, _ := c.Get(requestIDKey)
	requestID, _ := value.(string)
	return requestID
}

// WriteError renders the one JSON error envelope the API answers with. Every
// package that writes an error body calls it, so the shape — golden-pinned by
// internal/handler's error-mapping suite — has a single implementation.
//
// requestId is null rather than "" when the identifier is missing, which only
// happens in tests that skip the middleware chain.
func WriteError(c *gin.Context, status int, detail, code string) {
	var requestID any
	if id := RequestIDFromContext(c); id != "" {
		requestID = id
	}
	c.JSON(status, gin.H{
		"detail":    detail,
		"code":      code,
		"requestId": requestID,
	})
}
