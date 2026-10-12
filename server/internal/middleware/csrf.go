package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/util"

	"boardly.dev/boardly/server/internal/auth"
)

// RequireCSRF enforces double-submit CSRF on unsafe methods for
// cookie-authenticated requests. Requests without an authentication cookie
// pass through: they are either anonymous or bearer-authenticated, and
// neither is CSRF-forgeable.
func RequireCSRF(protector auth.CSRFProtector) gin.HandlerFunc {
	return func(c *gin.Context) {
		if protector == nil || isSafeMethod(c.Request.Method) {
			c.Next()
			return
		}

		var authenticationCookie string
		csrfCookie := CSRFCookieName
		if usesRefreshCSRF(c.Request.URL.Path) {
			authenticationCookie, _ = c.Cookie(RefreshCookieName)
			csrfCookie = RefreshCSRFCookieName
		} else {
			authenticationCookie, _ = c.Cookie(AuthCookieName)
			if authenticationCookie == "" && isCredentialEntryPath(c.Request.URL.Path) {
				authenticationCookie, _ = c.Cookie(RefreshCookieName)
				csrfCookie = RefreshCSRFCookieName
			}
		}
		if authenticationCookie == "" {
			c.Next()
			return
		}
		cookieToken, cookieErr := c.Cookie(csrfCookie)
		headerToken := c.GetHeader(CSRFHeaderName)
		if cookieErr != nil || cookieToken == "" || headerToken == "" ||
			subtle.ConstantTimeCompare([]byte(cookieToken), []byte(headerToken)) != 1 ||
			!protector.Validate(authenticationCookie, headerToken) {
			util.WriteError(c, http.StatusForbidden, "CSRF validation failed", "csrf_invalid")
			c.Abort()
			return
		}
		c.Next()
	}
}

// usesRefreshCSRF names the endpoints authenticated by the refresh cookie,
// whose CSRF token is minted against that cookie instead of the access one.
func usesRefreshCSRF(path string) bool {
	return path == "/api/v1/auth/refresh" || path == "/api/v1/auth/logout"
}

// isCredentialEntryPath names the endpoints a browser may call while holding
// only the refresh cookie (a lapsed session re-authenticating).
func isCredentialEntryPath(path string) bool {
	return path == "/api/v1/auth/signup" || path == "/api/v1/auth/login" ||
		path == "/api/v1/auth/email-verification/verify" ||
		path == "/api/v1/auth/email-verification/resend" ||
		path == "/api/v1/auth/email-verification/status" ||
		path == "/api/v1/auth/password-reset/request" ||
		path == "/api/v1/auth/password-reset/confirm" ||
		path == "/api/v1/auth/google/web/start"
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}
