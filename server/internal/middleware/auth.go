package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/util"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
)

// RequireUserAuth authenticates the request (bearer header or session
// cookie) and installs the user principal on the request context. A daemon's
// own session passes only as a Run (admitDaemonSession).
func RequireUserAuth(service auth.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		accessToken, kind, ok := accessCredential(c)
		if !ok {
			util.WriteError(
				c,
				http.StatusUnauthorized,
				"authentication is required",
				"authentication_required",
			)
			c.Abort()
			return
		}
		authenticate(c, service, accessToken, kind, false)
	}
}

// RequireDaemonAuth authenticates the daemon plane. Header-only on purpose:
// the daemon never holds cookies, and skipping the cookie path keeps CSRF
// entirely out of this plane. It admits the daemon's own session and,
// as older daemons send, a person's.
func RequireDaemonAuth(service auth.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearerToken(c)
		if !ok {
			util.WriteError(
				c,
				http.StatusUnauthorized,
				"authentication is required",
				"authentication_required",
			)
			c.Abort()
			return
		}
		authenticate(c, service, token, auth.TokenKindBearer, true)
	}
}

// authenticate is the shared tail: resolve the principal, map the failure,
// fence a daemon session off the daemon plane, and install the principal on
// the request context.
func authenticate(
	c *gin.Context,
	service auth.AuthService,
	accessToken string,
	kind auth.TokenKind,
	daemonPlane bool,
) {
	principal, err := service.Authenticate(c.Request.Context(), accessToken, kind)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidSession) {
			util.WriteError(c, http.StatusUnauthorized, "session is invalid or expired", "session_invalid")
		} else {
			util.WriteError(c, http.StatusInternalServerError, "internal server error", "internal_error")
		}
		c.Abort()
		return
	}
	if principal.DaemonSession() && !daemonPlane && !admitDaemonSession(c, principal) {
		return
	}
	c.Request = c.Request.WithContext(auth.WithUserPrincipal(c.Request.Context(), principal))
	c.Next()
}

// accessCredential extracts the request's access credential: the bearer
// header when present, else the session cookie.
func accessCredential(c *gin.Context) (string, auth.TokenKind, bool) {
	if authorization := c.GetHeader("Authorization"); authorization != "" {
		token, ok := bearerToken(c)
		if !ok {
			return "", "", false
		}
		return token, auth.TokenKindBearer, true
	}
	token, err := c.Cookie(AuthCookieName)
	if err != nil || token == "" {
		return "", "", false
	}
	return token, auth.TokenKindSession, true
}

func bearerToken(c *gin.Context) (string, bool) {
	authorization := c.GetHeader("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(authorization, prefix)
	if token == "" || strings.TrimSpace(token) != token || strings.ContainsAny(token, " \t\r\n,") {
		return "", false
	}
	return token, true
}

// OptionalUserAuth resolves an existing credential without requiring visitors
// to sign in before reading a public invitation.
func OptionalUserAuth(service auth.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, kind, ok := accessCredential(c)
		if !ok {
			c.Next()
			return
		}
		authenticate(c, service, token, kind, false)
	}
}
