package middleware

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/util"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
)

// The two logging middlewares live together because they are one story: the
// per-request line and the line a panic produces instead of it.

// RequestLogger emits one structured line per non-probe request, at a
// severity matching the outcome: 5xx are errors, 4xx warnings, the rest
// informational. A 5xx whose client already disconnected is logged as 499
// with client_closed at warn (see observedStatus). The line carries the
// request identity, the caller's address (see clientIP) and user agent, the
// authenticated principal when one was resolved, the caller's self-reported
// client metadata and daemon id, and why a credential was refused
// (SetAuthFailure). None of them is a secret: no token, cookie or body.
func RequestLogger(logger *slog.Logger, trustedProxies []netip.Prefix) gin.HandlerFunc {
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
			"client_ip", clientIP(c.Request, trustedProxies),
		}
		if userAgent := c.Request.UserAgent(); userAgent != "" {
			attrs = append(attrs, "user_agent", userAgent)
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
		if daemonID := c.GetHeader(HeaderDaemonID); daemonID != "" {
			attrs = append(attrs, "daemon_id", daemonID)
		}
		if reason := c.GetString(authFailureKey); reason != "" {
			attrs = append(attrs, "auth_failure", reason)
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

// authFailureKey is the gin context key SetAuthFailure writes.
const authFailureKey = "authFailure"

// SetAuthFailure records why the request's credential was refused, for the
// request log line. reason is a fixed label (auth.RefusalReason, or a
// missing/malformed header here); the response stays the generic 401, so
// the label tells an operator which client to chase without telling the
// caller anything new.
func SetAuthFailure(c *gin.Context, reason string) {
	if reason != "" {
		c.Set(authFailureKey, reason)
	}
}

// clientIP is the caller's address for the request log. X-Forwarded-For is
// honored only on a connection from a trusted proxy
// (ZENITH_TEAM_TRUSTED_PROXIES), and then read right to left past the
// proxies: each hop appends the address it saw, so the first untrusted entry
// from the right is the last one a trusted proxy vouched for. Entries to its
// left are whatever the client sent and are never taken.
func clientIP(request *http.Request, trustedProxies []netip.Prefix) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !trustedProxy(addr, trustedProxies) {
		return host
	}
	hops := strings.Split(strings.Join(request.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		addr = hop
		if !trustedProxy(hop, trustedProxies) {
			break
		}
	}
	return addr.Unmap().String()
}

func trustedProxy(addr netip.Addr, trustedProxies []netip.Prefix) bool {
	return slices.ContainsFunc(trustedProxies, func(prefix netip.Prefix) bool {
		return prefix.Contains(addr.Unmap())
	})
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
