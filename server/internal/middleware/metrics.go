package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
)

// HTTPObserver records one completed request. internal/telemetry satisfies it.
type HTTPObserver interface {
	ObserveHTTP(method, route string, status int, duration time.Duration)
}

// HTTPMetrics observes request latency and status against the route template.
// A 5xx whose client already disconnected counts as 499 (see observedStatus).
func HTTPMetrics(observer HTTPObserver) gin.HandlerFunc {
	return func(c *gin.Context) {
		if observer == nil || isProbePath(c.Request.URL.Path) {
			c.Next()
			return
		}
		root := c.Request.Context()
		startedAt := time.Now()
		c.Next()
		status, _ := observedStatus(root, c.Writer.Status())
		observer.ObserveHTTP(c.Request.Method, routeTemplate(c), status, time.Since(startedAt))
	}
}
