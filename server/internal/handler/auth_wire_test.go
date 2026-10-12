package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/middleware"

	"github.com/google/uuid"
)

func TestEmailVerificationWireUsesCanonicalFields(t *testing.T) {
	t.Parallel()

	requestJSON, err := json.Marshal(emailVerificationRequest{
		Email:          "owner@example.com",
		VerificationID: "c5148cb1-a73d-4a5b-8dc4-935d41119803",
		Code:           "123456",
		Client:         "web",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if !strings.Contains(string(requestJSON), `"verificationId"`) ||
		!strings.Contains(string(requestJSON), `"code":"123456"`) ||
		strings.Contains(string(requestJSON), `"otp"`) {
		t.Fatalf("request JSON = %s", requestJSON)
	}

	responseJSON, err := json.Marshal(pendingEmailVerificationToResponse(auth.PendingEmailVerification{
		Email:             "owner@example.com",
		VerificationID:    uuid.MustParse("c5148cb1-a73d-4a5b-8dc4-935d41119803"),
		ExpiresAt:         time.Unix(1, 0).UTC(),
		ResendAvailableAt: time.Unix(2, 0).UTC(),
		AttemptsRemaining: 2,
		MaxAttempts:       3,
	}))
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	// `result` is the discriminator against the session branch; both bodies
	// share one status code per operation, so clients branch on it.
	for _, field := range []string{
		`"result":"pending_verification"`, `"status":"pending_verification"`,
		`"verificationId"`, `"expiresAt"`, `"resendAvailableAt"`,
		`"attemptsRemaining":2`, `"maxAttempts":3`,
	} {
		if !strings.Contains(string(responseJSON), field) {
			t.Fatalf("response JSON %s is missing %s", responseJSON, field)
		}
	}
}

// TestEmailVerificationValidatesVerificationIDAsAUUIDString pins
// verificationId on verify, resend and status to the same rule the desktop
// OAuth flowId follows: it is a string validated as a UUID, so a non-UUID is
// 422 validation_failed naming the field, not a 400 decode failure — and a
// canonical one reaches the service parsed.
func TestEmailVerificationValidatesVerificationIDAsAUUIDString(t *testing.T) {
	t.Parallel()

	verificationID := uuid.MustParse("c5148cb1-a73d-4a5b-8dc4-935d41119803")
	serviceCalls := 0
	handler := New(Handler{
		AuthService: &authServiceStub{
			verifyEmail: func(_ context.Context, input auth.VerifyEmailInput) (auth.Result, error) {
				serviceCalls++
				if input.VerificationID != verificationID {
					t.Fatalf("VerifyEmail verificationId = %s, want %s",
						input.VerificationID, verificationID)
				}
				return auth.Result{}, auth.ErrInvalidInput
			},
			resendEmail: func(
				_ context.Context, input auth.ResendEmailVerificationInput,
			) (auth.PendingEmailVerification, error) {
				serviceCalls++
				if input.VerificationID != verificationID {
					t.Fatalf("ResendEmailVerification verificationId = %s, want %s", input.VerificationID, verificationID)
				}
				return auth.PendingEmailVerification{}, auth.ErrInvalidInput
			},
			emailStatus: func(
				_ context.Context, input auth.EmailVerificationStatusInput,
			) (auth.EmailVerificationStatusResult, error) {
				serviceCalls++
				if input.VerificationID != verificationID {
					t.Fatalf("EmailVerificationStatus verificationId = %s, want %s", input.VerificationID, verificationID)
				}
				return auth.EmailVerificationStatusResult{}, auth.ErrInvalidInput
			},
		},
	})

	for _, route := range []struct {
		path string
		body string
	}{
		{
			path: "/api/v1/auth/email-verification/verify",
			body: `{"email":"owner@example.com","verificationId":%q,"code":"123456","client":"desktop"}`,
		},
		{
			path: "/api/v1/auth/email-verification/resend",
			body: `{"email":"owner@example.com","verificationId":%q,"client":"desktop"}`,
		},
		{
			path: "/api/v1/auth/email-verification/status",
			body: `{"email":"owner@example.com","verificationId":%q,"client":"desktop"}`,
		},
	} {
		callsBefore := serviceCalls
		for _, malformed := range []string{"not-a-uuid", "zzz", ""} {
			request := httptest.NewRequest(http.MethodPost, route.path,
				bytes.NewBufferString(fmt.Sprintf(route.body, malformed)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body["detail"] != "verificationId must be a canonical UUID" &&
				body["detail"] != "verificationId is required" {
				t.Fatalf("%s verificationId %q detail = %q", route.path, malformed, body["detail"])
			}
		}
		if serviceCalls != callsBefore {
			t.Fatalf("%s: a non-UUID verificationId reached the service", route.path)
		}

		request := httptest.NewRequest(http.MethodPost, route.path,
			bytes.NewBufferString(fmt.Sprintf(route.body, verificationID.String())))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
		if serviceCalls != callsBefore+1 {
			t.Fatalf("%s: a canonical verificationId did not reach the service", route.path)
		}
	}
}

func TestAuthProvidersReturnClientSpecificCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		service     *googleOAuthServiceStub
		wantEnabled bool
		wantWeb     bool
		wantDesktop bool
	}{
		{name: "not configured"},
		{
			name:        "web only",
			service:     &googleOAuthServiceStub{webEnabled: true},
			wantEnabled: true,
			wantWeb:     true,
		},
		{
			name:        "desktop only",
			service:     &googleOAuthServiceStub{desktopEnabled: true},
			wantEnabled: true,
			wantDesktop: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := New(Handler{GoogleOAuthService: test.service})
			request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers", nil)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
			}
			var body struct {
				Items []struct {
					Provider       string `json:"provider"`
					Enabled        bool   `json:"enabled"`
					WebEnabled     bool   `json:"webEnabled"`
					DesktopEnabled bool   `json:"desktopEnabled"`
				} `json:"items"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode providers response: %v", err)
			}
			if len(body.Items) != 1 || body.Items[0].Provider != "google" ||
				body.Items[0].Enabled != test.wantEnabled ||
				body.Items[0].WebEnabled != test.wantWeb ||
				body.Items[0].DesktopEnabled != test.wantDesktop {
				t.Fatalf("providers response = %s", response.Body.String())
			}
		})
	}
}

// TestAuthReadsRejectAnyQueryParameter pins GET /me and GET /auth/providers
// to the convention every other read follows: neither takes a query string,
// so any parameter at all is 422 validation_failed rather than silently
// ignored. /me is answered before the service is reached; /providers is
// public and has no service to reach.
func TestAuthReadsRejectAnyQueryParameter(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2d")
	service := authenticatedAuthStub(userID)
	service.currentUser = func(context.Context, uuid.UUID) (auth.User, error) {
		t.Fatal("a read with a query string reached CurrentUser")
		return auth.User{}, nil
	}
	handler := New(Handler{AuthService: service, GoogleOAuthService: &googleOAuthServiceStub{webEnabled: true}})

	for _, query := range []string{"?unknown=true", "?x=", "?client=web"} {
		me := serveBearerRequest(handler, http.MethodGet, "/api/v1/me"+query, nil)
		assertErrorResponse(t, me, http.StatusUnprocessableEntity, "validation_failed")

		request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers"+query, nil)
		providers := httptest.NewRecorder()
		handler.ServeHTTP(providers, request)
		assertErrorResponse(t, providers, http.StatusUnprocessableEntity, "validation_failed")
	}
}

// TestDesktopHandoffStartAnswersTheCredentialItWasGiven pins the three
// answers of the cookie-only handoff start: no credential is 401
// authentication_required; a valid bearer credential is 422
// client_transport_mismatch, the code for a bearer client on a cookie flow,
// rather than a claim that the credential was missing; and a cookie session
// from the web origin mints the code.
func TestDesktopHandoffStartAnswersTheCredentialItWasGiven(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2d")
	expiresAt := time.Date(2026, time.August, 16, 10, 13, 31, 114_882_000, time.UTC)
	startCalls := 0
	handler := New(Handler{
		AllowedOrigins: []string{"https://app.boardly.test"},
		AuthService:    authenticatedAuthStub(userID),
		DesktopHandoffService: &desktopHandoffServiceStub{
			start: func(_ context.Context, gotUserID uuid.UUID) (auth.DesktopHandoffStartResult, error) {
				startCalls++
				if gotUserID != userID {
					t.Fatalf("Start user = %s, want %s", gotUserID, userID)
				}
				return auth.DesktopHandoffStartResult{Code: "handoff-code", ExpiresAt: expiresAt}, nil
			},
		},
	})
	const path = "/api/v1/auth/desktop/handoff/start"

	anonymous := httptest.NewRequest(http.MethodPost, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, anonymous)
	assertErrorResponse(t, response, http.StatusUnauthorized, "authentication_required")

	bearer := serveBearerRequest(handler, http.MethodPost, path, nil)
	assertErrorResponse(t, bearer, http.StatusUnprocessableEntity, "client_transport_mismatch")

	cookieNoOrigin := httptest.NewRequest(http.MethodPost, path, nil)
	cookieNoOrigin.AddCookie(&http.Cookie{Name: middleware.AuthCookieName, Value: "web-access"})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, cookieNoOrigin)
	assertErrorResponse(t, response, http.StatusUnprocessableEntity, "client_transport_mismatch")
	if startCalls != 0 {
		t.Fatalf("Start calls for rejected requests = %d, want 0", startCalls)
	}

	cookie := httptest.NewRequest(http.MethodPost, path, nil)
	cookie.AddCookie(&http.Cookie{Name: middleware.AuthCookieName, Value: "web-access"})
	cookie.Header.Set("Origin", "https://app.boardly.test")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, cookie)
	if response.Code != http.StatusOK || startCalls != 1 {
		t.Fatalf("cookie handoff start = %d %s (Start calls %d)", response.Code, response.Body.String(), startCalls)
	}
	if body := response.Body.String(); !strings.Contains(body, `"code":"handoff-code"`) ||
		!strings.Contains(body, `"expiresAt":"2026-08-16T10:13:31.114882Z"`) {
		t.Fatalf("handoff start body = %s", body)
	}
}

func TestSessionMintingHandlersReturnAuthSuccess(t *testing.T) {
	t.Parallel()

	result := authWireResult()
	companies := []company.Response{authWireCompany()}
	handler := New(Handler{
		AuthService: &authServiceStub{
			signup: func(context.Context, auth.SignupInput) (auth.SignupResult, error) {
				return auth.SignupResult{Auth: &result}, nil
			},
			login: func(context.Context, auth.LoginInput) (auth.LoginResult, error) {
				return auth.LoginResult{Auth: &result}, nil
			},
			verifyEmail: func(context.Context, auth.VerifyEmailInput) (auth.Result, error) {
				return result, nil
			},
		},
		CompanyService: &companyServiceStub{
			list: func(_ context.Context, principal auth.UserPrincipal) ([]company.Response, error) {
				if principal.UserID != result.User.ID ||
					principal.SessionID != result.Tokens.SessionID ||
					principal.AuthKind != auth.TokenKindBearer {
					t.Fatalf("company-list principal = %+v", principal)
				}
				return companies, nil
			},
		},
		DesktopHandoffService: &desktopHandoffServiceStub{
			complete: func(context.Context, auth.DesktopHandoffCompleteInput) (auth.Result, error) {
				return result, nil
			},
		},
		GoogleOAuthService: &googleOAuthServiceStub{
			enabled: true,
			completeDesktop: func(context.Context, auth.DesktopOAuthCompleteInput) (auth.Result, error) {
				return result, nil
			},
		},
	})

	tests := []struct {
		name   string
		path   string
		body   string
		status int
	}{
		{
			name:   "signup",
			path:   "/api/v1/auth/signup",
			body:   `{"email":"owner@example.com","password":"password-value","displayName":"Owner","client":"desktop"}`,
			status: http.StatusCreated,
		},
		{
			name:   "login",
			path:   "/api/v1/auth/login",
			body:   `{"email":"owner@example.com","password":"password-value","client":"desktop"}`,
			status: http.StatusOK,
		},
		{
			name:   "email verification",
			path:   "/api/v1/auth/email-verification/verify",
			body:   `{"email":"owner@example.com","verificationId":"c5148cb1-a73d-4a5b-8dc4-935d41119803","code":"123456","client":"desktop"}`,
			status: http.StatusOK,
		},
		{
			name:   "desktop OAuth",
			path:   "/api/v1/auth/google/desktop/complete",
			body:   `{"flowId":"c5148cb1-a73d-4a5b-8dc4-935d41119803","redirectUri":"http://127.0.0.1:49152/callback","state":"state","code":"code","verifier":"verifier"}`,
			status: http.StatusOK,
		},
		{
			name:   "desktop handoff",
			path:   "/api/v1/auth/desktop/handoff/complete",
			body:   `{"code":"handoff-code"}`,
			status: http.StatusOK,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf(
					"status = %d, want %d; body = %s",
					response.Code,
					test.status,
					response.Body.String(),
				)
			}
			assertAuthSuccessBody(t, response.Body.Bytes(), result, companies)
		})
	}
}

func TestWebLoginReturnsCookieAuthSuccess(t *testing.T) {
	t.Parallel()

	result := authWireResult()
	result.Client = auth.ClientWeb
	companies := []company.Response{authWireCompany()}
	csrf, err := auth.NewCSRF([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("new CSRF: %v", err)
	}
	handler := New(Handler{
		AllowedOrigins: []string{"https://app.boardly.test"},
		CSRF:           csrf,
		AuthService: &authServiceStub{
			login: func(context.Context, auth.LoginInput) (auth.LoginResult, error) {
				return auth.LoginResult{Auth: &result}, nil
			},
		},
		CompanyService: &companyServiceStub{
			list: func(_ context.Context, principal auth.UserPrincipal) ([]company.Response, error) {
				if principal.AuthKind != auth.TokenKindSession {
					t.Fatalf("company-list auth kind = %q, want session", principal.AuthKind)
				}
				return companies, nil
			},
		},
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/login",
		bytes.NewBufferString(`{"email":"owner@example.com","password":"password-value","client":"web"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://app.boardly.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	assertAuthSuccessBody(t, response.Body.Bytes(), result, companies)
	if strings.Contains(response.Body.String(), "access-token") ||
		strings.Contains(response.Body.String(), "refresh-token") {
		t.Fatalf("cookie response exposed bearer credentials: %s", response.Body.String())
	}
	if len(response.Result().Cookies()) < 4 {
		t.Fatalf("cookies = %d, want auth, refresh and CSRF cookies", len(response.Result().Cookies()))
	}
}

func TestRefreshReturnsSessionRotationWithoutCompanies(t *testing.T) {
	t.Parallel()

	result := authWireResult()
	rotationID := uuid.MustParse("0199a143-7ac0-7d4f-b93e-4be838f5772e")
	handler := New(Handler{
		AuthService: &authServiceStub{
			refresh: func(_ context.Context, input auth.RefreshInput) (auth.RefreshResult, error) {
				if input.RotationID == nil || *input.RotationID != rotationID {
					t.Fatalf("Refresh() rotation id = %v, want %s", input.RotationID, rotationID)
				}
				return auth.RefreshResult{Auth: &result}, nil
			},
		},
		CompanyService: &companyServiceStub{
			list: func(context.Context, auth.UserPrincipal) ([]company.Response, error) {
				t.Fatal("refresh listed companies")
				return nil, nil
			},
		},
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/refresh",
		bytes.NewBufferString(`{"client":"desktop","refreshToken":"refresh-current","rotationId":"0199a143-7ac0-7d4f-b93e-4be838f5772e"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := body["companies"]; ok {
		t.Fatalf("refresh response unexpectedly contains companies: %s", response.Body.String())
	}
	if body["user"] == nil || body["session"] == nil {
		t.Fatalf("refresh response = %s, want user and session", response.Body.String())
	}
}

// Logout is idempotent on both transports: signing out with no live credential
// — no refresh cookie, or no refreshToken — is a 204, not an error, and the
// service is never asked to revoke anything. refresh_token_required belongs to
// /auth/refresh alone.
func TestLogoutWithoutACredentialIsANoContentOnBothTransports(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name        string
		body        string
		origin      string
		wantCookies int
	}{
		{name: "desktop without refreshToken", body: `{"client":"desktop"}`},
		{name: "desktop with an empty refreshToken", body: `{"client":"desktop","refreshToken":""}`},
		{
			name: "web without a cookie", body: `{"client":"web"}`,
			origin: "https://app.boardly.test", wantCookies: 4,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			handler := New(Handler{
				AllowedOrigins: []string{"https://app.boardly.test"},
				AuthService:    unreachableAuthService(t),
			})
			request := httptest.NewRequest(
				http.MethodPost, "/api/v1/auth/logout", strings.NewReader(testCase.body),
			)
			request.Header.Set("Content-Type", "application/json")
			if testCase.origin != "" {
				request.Header.Set("Origin", testCase.origin)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
				t.Fatalf(
					"status = %d, want 204 with an empty body; body = %s",
					response.Code, response.Body.String(),
				)
			}
			if got := len(response.Result().Cookies()); got != testCase.wantCookies {
				t.Fatalf("cookies cleared = %d, want %d", got, testCase.wantCookies)
			}
		})
	}
}

// `client` is a validated enum on every operation that accepts it: absent,
// empty or unrecognized is 422 validation_failed, and only a well-formed value
// that contradicts the Origin header reaches 422 client_transport_mismatch.
// Signup, login and the three email-verification routes used to reach the
// transport check first, so a missing field reported a mismatch.
func TestClientIsValidatedBeforeTheTransportCheck(t *testing.T) {
	t.Parallel()

	const verificationID = "c5148cb1-a73d-4a5b-8dc4-935d41119803"
	routes := []struct {
		name string
		path string
		body func(client string) string
	}{
		{
			name: "signup", path: "/api/v1/auth/signup",
			body: func(client string) string {
				return `{"email":"owner@example.com","password":"password-value",` +
					`"displayName":"Owner"` + client + `}`
			},
		},
		{
			name: "login", path: "/api/v1/auth/login",
			body: func(client string) string {
				return `{"email":"owner@example.com","password":"password-value"` + client + `}`
			},
		},
		{
			name: "verify email", path: "/api/v1/auth/email-verification/verify",
			body: func(client string) string {
				return `{"email":"owner@example.com","verificationId":"` + verificationID +
					`","code":"123456"` + client + `}`
			},
		},
		{
			name: "resend verification", path: "/api/v1/auth/email-verification/resend",
			body: func(client string) string {
				return `{"email":"owner@example.com","verificationId":"` + verificationID + `"` +
					client + `}`
			},
		},
		{
			name: "verification status", path: "/api/v1/auth/email-verification/status",
			body: func(client string) string {
				return `{"email":"owner@example.com","verificationId":"` + verificationID + `"` +
					client + `}`
			},
		},
		{
			name: "refresh", path: "/api/v1/auth/refresh",
			body: func(client string) string { return `{"refreshToken":"refresh-current"` + client + `}` },
		},
		{
			name: "logout", path: "/api/v1/auth/logout",
			body: func(client string) string { return `{"refreshToken":"refresh-current"` + client + `}` },
		},
	}
	cases := []struct {
		name   string
		client string
		origin string
		code   string
	}{
		{name: "absent", client: ``, code: "validation_failed"},
		{name: "empty", client: `,"client":""`, code: "validation_failed"},
		{name: "unrecognized", client: `,"client":"phone"`, code: "validation_failed"},
		{name: "web without Origin", client: `,"client":"web"`, code: "client_transport_mismatch"},
		{
			name: "desktop with Origin", client: `,"client":"desktop"`,
			origin: "https://app.boardly.test", code: "client_transport_mismatch",
		},
	}
	for _, route := range routes {
		for _, testCase := range cases {
			t.Run(route.name+" "+testCase.name, func(t *testing.T) {
				t.Parallel()
				handler := New(Handler{
					AllowedOrigins: []string{"https://app.boardly.test"},
					AuthService:    unreachableAuthService(t),
				})
				request := httptest.NewRequest(
					http.MethodPost, route.path, strings.NewReader(route.body(testCase.client)),
				)
				request.Header.Set("Content-Type", "application/json")
				if testCase.origin != "" {
					request.Header.Set("Origin", testCase.origin)
				}
				response := httptest.NewRecorder()

				handler.ServeHTTP(response, request)

				assertErrorResponse(t, response, http.StatusUnprocessableEntity, testCase.code)
			})
		}
	}
}

// unreachableAuthService fails the test from whichever operation the boundary
// lets through: it exists for requests the handler must refuse on its own.
func unreachableAuthService(t *testing.T) *authServiceStub {
	t.Helper()
	return &authServiceStub{
		signup: func(context.Context, auth.SignupInput) (auth.SignupResult, error) {
			t.Fatal("Signup() called for a request the boundary must refuse")
			return auth.SignupResult{}, nil
		},
		login: func(context.Context, auth.LoginInput) (auth.LoginResult, error) {
			t.Fatal("Login() called for a request the boundary must refuse")
			return auth.LoginResult{}, nil
		},
		verifyEmail: func(context.Context, auth.VerifyEmailInput) (auth.Result, error) {
			t.Fatal("VerifyEmail() called for a request the boundary must refuse")
			return auth.Result{}, nil
		},
		resendEmail: func(
			context.Context, auth.ResendEmailVerificationInput,
		) (auth.PendingEmailVerification, error) {
			t.Fatal("ResendEmailVerification() called for a request the boundary must refuse")
			return auth.PendingEmailVerification{}, nil
		},
		emailStatus: func(
			context.Context, auth.EmailVerificationStatusInput,
		) (auth.EmailVerificationStatusResult, error) {
			t.Fatal("EmailVerificationStatus() called for a request the boundary must refuse")
			return auth.EmailVerificationStatusResult{}, nil
		},
		refresh: func(context.Context, auth.RefreshInput) (auth.RefreshResult, error) {
			t.Fatal("Refresh() called for a request the boundary must refuse")
			return auth.RefreshResult{}, nil
		},
		logout: func(context.Context, string) (auth.SessionMutationResult, error) {
			t.Fatal("Logout() called for a request the boundary must refuse")
			return auth.SessionMutationResult{}, nil
		},
	}
}

func assertAuthSuccessBody(
	t *testing.T,
	encoded []byte,
	result auth.Result,
	companies []company.Response,
) {
	t.Helper()
	var body struct {
		Result    string            `json:"result"`
		User      auth.UserResponse `json:"user"`
		Session   map[string]any    `json:"session"`
		Companies struct {
			Items []company.Response `json:"items"`
		} `json:"companies"`
	}
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatalf("decode AuthSuccess: %v", err)
	}
	if body.Result != auth.AuthResultSession {
		t.Fatalf("AuthSuccess result = %q, want %q", body.Result, auth.AuthResultSession)
	}
	if body.User.ID != result.User.ID || body.User.EmailVerifiedAt == nil ||
		!body.User.HasPassword || len(body.User.LinkedProviders) != 1 ||
		body.User.LinkedProviders[0] != "google" || len(body.Companies.Items) != len(companies) {
		t.Fatalf("AuthSuccess = %s", encoded)
	}
	if body.Session["id"] != result.Tokens.SessionID.String() ||
		body.Session["issuedAt"] != result.Tokens.IssuedAt.Format(time.RFC3339Nano) ||
		body.Session["transport"] == nil {
		t.Fatalf("session = %#v", body.Session)
	}
}

func authWireResult() auth.Result {
	issuedAt := time.Date(2026, time.August, 19, 10, 0, 0, 123_456_000, time.UTC)
	verifiedAt := issuedAt.Add(-2 * time.Hour)
	return auth.Result{
		User: auth.User{
			ID:    uuid.MustParse("17f9771b-bae4-4d68-91eb-d2cc51c6588e"),
			Email: "owner@example.com", PasswordHash: "encoded-password",
			EmailVerifiedAt: &verifiedAt, LinkedProviders: []string{"google"},
			DisplayName: "Owner", Biography: "",
			CreatedAt: issuedAt.Add(-time.Hour), UpdatedAt: issuedAt.Add(-time.Hour),
			Version: 1,
		},
		Client: auth.ClientDesktop,
		Tokens: auth.IssuedTokens{
			SessionID:   uuid.MustParse("76dbe42b-9ef6-4a13-a106-89686d2b1d95"),
			IssuedAt:    issuedAt,
			AccessToken: "access-token", RefreshToken: "refresh-token",
			AccessExpiresAt:  issuedAt.Add(15 * time.Minute),
			RefreshExpiresAt: issuedAt.Add(30 * 24 * time.Hour),
		},
	}
}

func authWireCompany() company.Response {
	createdAt := time.Date(2026, time.August, 19, 9, 0, 0, 0, time.UTC)
	return company.Response{
		ID:   uuid.MustParse("b04f1042-8c75-4e62-9d7f-f0cd090d0b45"),
		Name: "Boardly", Slug: "boardly", TaskPrefix: "BRD", Role: auth.CompanyRoleOwner,
		Revision: 1, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
}
