package util

import (
	"context"

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

// requestIDContextKey carries the same identifier on the request's
// context.Context, for code below the handler that holds no gin.Context.
type requestIDContextKey struct{}

// WithRequestID returns ctx carrying requestID. The RequestID middleware
// installs it beside SetRequestID, so a service-layer log line can quote the
// id the error envelope and the request log show.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

// ContextRequestID returns the identifier WithRequestID stored, or "" outside
// an HTTP request (workers, the daemon WS plane).
func ContextRequestID(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
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
