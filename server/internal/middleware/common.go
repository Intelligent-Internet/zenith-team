// Package middleware holds every Gin middleware, one file per functionality:
// request identity and header policy (header.go), client metadata
// (client.go), request logging and panic recovery (logging.go), HTTP metrics
// (metrics.go), CORS (cors.go), CSRF (csrf.go), the credential guards
// (auth.go), the company access guards (company.go), and the Project access
// guards (project.go).
//
// Each middleware is a plain function that takes what it needs as arguments
// — a logger, a config, or a domain service the caller already holds — and
// returns the gin.HandlerFunc. This package declares no service interface of
// its own: the guards take the contracts the domain packages own
// (auth.AuthService, company.CompanyService, auth.CSRFProtector), so nothing
// here restates a method another package already declares.
//
// This file owns the package's shared vocabulary: the constants and the
// helpers more than one middleware needs. The JSON error envelope is
// util.WriteError — every package that answers an error calls it there.
package middleware

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// The browser session transport's cookie and header names. The CSRF guard
// checks them and internal/handler's auth handlers set and clear them, so
// they are declared once, here.
const (
	AuthCookieName        = "zenith_team_access"
	RefreshCookieName     = "zenith_team_refresh"
	CSRFCookieName        = "zenith_team_csrf"
	RefreshCSRFCookieName = "zenith_team_refresh_csrf"
	CSRFHeaderName        = "X-CSRF-Token"
)

// Client metadata header names — exported so the HTTP clients (daemon, CLI)
// and any non-header transport stay in sync without re-declaring them.
const (
	HeaderClientPlatform = "X-Client-Platform"
	HeaderClientVersion  = "X-Client-Version"
	HeaderClientOS       = "X-Client-OS"
	// HeaderDaemonID names the machine a daemon registered as (`daemonId` on
	// register); the daemon plane fences runtime-scoped routes on it. The
	// daemon package repeats the literal rather than importing it, because
	// cmd/zen must not link this package; the wire round-trip test pins
	// the two together.
	HeaderDaemonID = "X-Zenith-Team-Daemon-ID"
)

// clientMetadataKey types the client-metadata context keys so a foreign
// package cannot collide with them.
type clientMetadataKey int

const (
	ctxKeyClientPlatform clientMetadataKey = iota
	ctxKeyClientVersion
	ctxKeyClientOS
)

// isProbePath reports the unversioned operational endpoints. They are
// excluded from logging and metrics so liveness traffic cannot dominate
// either signal.
func isProbePath(path string) bool {
	return path == "/healthz" || path == "/readyz" || path == "/metrics"
}

// routeTemplate returns the matched route pattern rather than the concrete
// path, which is what keeps metric and log label cardinality bounded.
func routeTemplate(c *gin.Context) string {
	if route := c.FullPath(); route != "" {
		return route
	}
	return "unmatched"
}

// observedStatus returns the status the request log and HTTP metrics record.
// A 5xx written after the client went away is recorded as 499 (client closed
// request): net/http cancels the request context when the connection drops,
// pgx then fails the query with context.Canceled, and no handler maps that,
// so the 500 reached no client and says nothing about the server. The
// response itself is left alone.
//
// root must be the request context captured before c.Next(). RequestTimeout
// swaps in a WithTimeout child whose deferred cancel has already fired once
// Next returns, so the context read afterwards is always canceled and would
// mislabel real failures. A deadline is never a client close.
func observedStatus(root context.Context, written int) (status int, clientClosed bool) {
	if written >= http.StatusInternalServerError && errors.Is(root.Err(), context.Canceled) {
		return 499, true
	}
	return written, false
}
