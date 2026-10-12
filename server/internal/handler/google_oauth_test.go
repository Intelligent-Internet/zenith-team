package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/middleware"
)

func TestGoogleOAuthWebCallbackSuccess(t *testing.T) {
	t.Parallel()

	for _, query := range []string{
		"state=flow-state&code=google-code",
		"state=flow-state&code=google-code&scope=openid+email+profile&authuser=0&hd=example.com&prompt=consent",
		"state=flow-state&code=google-code&unknown=&unknown=value&returnPath=https%3A%2F%2Fexample.com",
	} {
		t.Run(query, func(t *testing.T) {
			t.Parallel()
			csrf, err := auth.NewCSRF([]byte(strings.Repeat("s", 32)))
			if err != nil {
				t.Fatal(err)
			}
			result := authWireResult()
			result.Client = auth.ClientWeb
			result.Tokens.AccessExpiresAt = time.Now().Add(15 * time.Minute)
			result.Tokens.RefreshExpiresAt = time.Now().Add(24 * time.Hour)
			calls := 0
			handler := New(Handler{
				OAuthWebAppOrigin: "http://localhost:5173",
				CSRF:              csrf,
				GoogleOAuthService: &googleOAuthServiceStub{
					webEnabled: true,
					completeWeb: func(_ context.Context, input auth.WebOAuthCompleteInput) (auth.WebOAuthCompleteResult, error) {
						calls++
						want := auth.WebOAuthCompleteInput{
							State:          "flow-state",
							Code:           "google-code",
							BrowserBinding: "browser-binding",
						}
						if input != want {
							t.Fatalf("completion = %+v, want %+v", input, want)
						}
						return auth.WebOAuthCompleteResult{
							Result:     result,
							ReturnPath: "/auth/google/callback",
						}, nil
					},
				},
			})
			request := httptest.NewRequest(http.MethodGet, googleOAuthWebCallbackPath+"?"+query, nil)
			request.AddCookie(&http.Cookie{Name: googleOAuthWebBindingCookieName, Value: "browser-binding"})
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if calls != 1 || response.Code != http.StatusSeeOther {
				t.Fatalf(
					"calls = %d, status = %d, want 1, 303; body = %s",
					calls,
					response.Code,
					response.Body,
				)
			}
			if got := response.Header().
				Get("Location"); got != "http://localhost:5173/auth/google/callback" {
				t.Fatalf("Location = %q, want the stored app return path", got)
			}
			assertGoogleOAuthCallbackHeaders(t, response)
			cookies := make(map[string]*http.Cookie)
			for _, cookie := range response.Result().Cookies() {
				cookies[cookie.Name] = cookie
			}
			for name, value := range map[string]string{
				middleware.AuthCookieName:    result.Tokens.AccessToken,
				middleware.RefreshCookieName: result.Tokens.RefreshToken,
			} {
				cookie := cookies[name]
				if cookie == nil || cookie.Value != value || !cookie.HttpOnly || cookie.Secure {
					t.Fatalf("%s cookie = %+v, want an HTTP localhost session cookie", name, cookie)
				}
			}
		})
	}
}

func TestGoogleOAuthWebCallbackRejectsInvalidEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		query    string
		bindings []string
	}{
		{name: "missing query", bindings: []string{"binding"}},
		{name: "missing state", query: "code=code", bindings: []string{"binding"}},
		{name: "empty state", query: "state=&code=code", bindings: []string{"binding"}},
		{name: "duplicate state", query: "state=state&state=state&code=code", bindings: []string{"binding"}},
		{name: "duplicate code", query: "state=state&code=code&code=code", bindings: []string{"binding"}},
		{
			name:     "duplicate error",
			query:    "state=state&error=access_denied&error=access_denied",
			bindings: []string{"binding"},
		},
		{name: "empty code", query: "state=state&code=", bindings: []string{"binding"}},
		{
			name:     "both code and error",
			query:    "state=state&code=code&error=access_denied",
			bindings: []string{"binding"},
		},
		{name: "empty error with code", query: "state=state&code=code&error=", bindings: []string{"binding"}},
		{
			name:     "empty code with error",
			query:    "state=state&code=&error=access_denied",
			bindings: []string{"binding"},
		},
		{name: "unsupported error", query: "state=state&error=unexpected", bindings: []string{"binding"}},
		{name: "bad encoding", query: "state=state&code=code&scope=%zz", bindings: []string{"binding"}},
		{
			name:     "oversized query",
			query:    "state=state&code=code&scope=" + strings.Repeat("s", 8<<10),
			bindings: []string{"binding"},
		},
		{name: "missing binding", query: "state=state&code=code"},
		{name: "empty binding", query: "state=state&code=code", bindings: []string{""}},
		{name: "duplicate binding", query: "state=state&code=code", bindings: []string{"binding", "binding"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := New(Handler{
				OAuthWebAppOrigin: "http://localhost:5173",
				GoogleOAuthService: &googleOAuthServiceStub{
					webEnabled: true,
					completeWeb: func(context.Context, auth.WebOAuthCompleteInput) (auth.WebOAuthCompleteResult, error) {
						t.Fatal("invalid evidence reached CompleteWeb")
						return auth.WebOAuthCompleteResult{}, nil
					},
				},
			})
			request := httptest.NewRequest(http.MethodGet, googleOAuthWebCallbackPath+"?"+test.query, nil)
			for _, value := range test.bindings {
				request.AddCookie(&http.Cookie{Name: googleOAuthWebBindingCookieName, Value: value})
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			want := "http://localhost:5173/login?auth_error=oauth_flow_invalid"
			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != want {
				t.Fatalf(
					"status = %d, Location = %q, want 303, %q",
					response.Code,
					response.Header().Get("Location"),
					want,
				)
			}
			assertGoogleOAuthCallbackHeaders(t, response)
			if got := len(response.Result().Cookies()); got != 1 {
				t.Fatalf("cookie count = %d, want only the cleared binding", got)
			}
		})
	}
}

func TestGoogleOAuthWebCallbackFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantCode   string
		disabled   bool
		noOrigin   bool
		returnPath string
	}{
		{name: "denied", err: auth.ErrOAuthProviderDenied, wantCode: "oauth_provider_denied"},
		{
			name:     "expired",
			err:      fmt.Errorf("complete web: %w", auth.ErrOAuthFlowInvalid),
			wantCode: "oauth_flow_invalid",
		},
		{name: "unavailable", err: auth.ErrOAuthProviderUnavailable, wantCode: "oauth_provider_unavailable"},
		{name: "invalid identity", err: auth.ErrOAuthIdentityInvalid, wantCode: "oauth_identity_invalid"},
		{name: "unverified email", err: auth.ErrOAuthEmailUnverified, wantCode: "oauth_email_unverified"},
		{name: "account conflict", err: auth.ErrOAuthAccountConflict, wantCode: "oauth_account_conflict"},
		{name: "account disabled", err: auth.ErrAccountDisabled, wantCode: "account_disabled"},
		{name: "web disabled", disabled: true, wantCode: "oauth_not_configured"},
		{name: "no app origin", noOrigin: true},
		{name: "unexpected error", err: errors.New("database unavailable")},
		{name: "unsafe return path", returnPath: "//example.com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			csrf, err := auth.NewCSRF([]byte(strings.Repeat("s", 32)))
			if err != nil {
				t.Fatal(err)
			}
			origin := "http://localhost:5173"
			if test.noOrigin {
				origin = ""
			}
			handler := New(Handler{
				OAuthWebAppOrigin: origin,
				CSRF:              csrf,
				GoogleOAuthService: &googleOAuthServiceStub{
					webEnabled: !test.disabled,
					completeWeb: func(_ context.Context, input auth.WebOAuthCompleteInput) (auth.WebOAuthCompleteResult, error) {
						if input.State != "state" || input.ProviderError != "access_denied" ||
							input.Code != "" {
							t.Fatalf("completion = %+v, want provider denial", input)
						}
						return auth.WebOAuthCompleteResult{
							ReturnPath: test.returnPath,
						}, test.err
					},
				},
			})
			request := httptest.NewRequest(http.MethodGet,
				googleOAuthWebCallbackPath+"?state=state&error=access_denied&authuser=0", nil)
			request.AddCookie(&http.Cookie{Name: googleOAuthWebBindingCookieName, Value: "binding"})
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if test.wantCode == "" {
				assertErrorResponse(t, response, http.StatusInternalServerError, "internal_error")
				if got := response.Header().Get("Location"); got != "" {
					t.Fatalf("Location = %q, want no redirect for an internal failure", got)
				}
			} else {
				want := origin + "/login?auth_error=" + test.wantCode
				if response.Code != http.StatusSeeOther || response.Header().Get("Location") != want {
					t.Fatalf(
						"status = %d, Location = %q, want 303, %q",
						response.Code,
						response.Header().Get("Location"),
						want,
					)
				}
				assertGoogleOAuthCallbackHeaders(t, response)
			}
			if got := len(response.Result().Cookies()); got != 1 {
				t.Fatalf("cookie count = %d, want only the cleared binding", got)
			}
		})
	}
}

func assertGoogleOAuthCallbackHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Body.Len() != 0 || response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("callback exposed a body or lacked security headers: %s, %v", response.Body, response.Header())
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == googleOAuthWebBindingCookieName && cookie.Value == "" && cookie.MaxAge == -1 &&
			cookie.Path == googleOAuthWebCallbackPath && cookie.HttpOnly && cookie.SameSite == http.SameSiteLaxMode {
			return
		}
	}
	t.Fatal("callback did not clear its browser binding cookie")
}
