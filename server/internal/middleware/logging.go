package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/util"

	"boardly.dev/boardly/server/internal/auth"
)

// The two logging middlewares live together because they are one story: the
// per-request line and the line a panic produces instead of it.

// RequestLogger emits one structured line per non-probe request, at a
// severity matching the outcome: 5xx are errors, 4xx warnings, the rest
// informational. A 5xx whose client already disconnected is logged as 499
// with client_closed at warn (see observedStatus). The line carries the
// request identity, the authenticated principal when one was resolved, and
// the caller's self-reported client metadata.
func RequestLogger(logger *slog.Logger) gin.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(c *gin.Context) {
		if isProbePath(c.Request.URL.Path) {
			c.Next()
			return
		}
		root := c.Request.Context()
		startedAt := time.Now()
		c.Next()

		status, clientClosed := observedStatus(root, c.Writer.Status())
		attrs := []any{
			"method", c.Request.Method,
			"route", routeTemplate(c),
			"status", status,
			"duration_ms", time.Since(startedAt).Milliseconds(),
			"request_id", util.RequestIDFromContext(c),
		}
		if clientClosed {
			attrs = append(attrs, "client_closed", true)
		}
		// The auth middleware swaps the request context in place, so reading
		// after Next observes the resolved principal — a trustworthy
		// identity, unlike any client-supplied header.
		if principal, ok := auth.UserPrincipalFromContext(c.Request.Context()); ok {
			attrs = append(attrs, "user_id", principal.UserID.String())
		}
		if platform, version, os := ClientMetadataFromContext(
			c.Request.Context(),
		); platform != "" || version != "" ||
			os != "" {
			if platform != "" {
				attrs = append(attrs, "client_platform", platform)
			}
			if version != "" {
				attrs = append(attrs, "client_version", version)
			}
			if os != "" {
				attrs = append(attrs, "client_os", os)
			}
		}

		switch {
		case status >= http.StatusInternalServerError:
			logger.ErrorContext(c.Request.Context(), "HTTP request", attrs...)
		case status >= http.StatusBadRequest:
			logger.WarnContext(c.Request.Context(), "HTTP request", attrs...)
		default:
			logger.InfoContext(c.Request.Context(), "HTTP request", attrs...)
		}
	}
}

// Recovery turns a handler panic into the standard error envelope, logging
// the panic type and stack. It writes only when nothing has been written
// yet, so a panic mid-response cannot corrupt an already-started body.
func Recovery(logger *slog.Logger) gin.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(c.Request.Context(), "recovered HTTP panic",
					"request_id", util.RequestIDFromContext(c),
					"panic_type", fmt.Sprintf("%T", recovered),
					"stack", string(debug.Stack()),
				)
				if !c.Writer.Written() {
					util.WriteError(
						c,
						http.StatusInternalServerError,
						"internal server error",
						"internal_error",
					)
				}
				c.Abort()
			}
		}()
		c.Next()
	}
}
