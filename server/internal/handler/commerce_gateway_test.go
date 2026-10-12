package handler

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/commerce"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/execution"
)

func TestLocalCommerceSigningKeyRouteRequiresLiveRunProofAndDerivesScope(t *testing.T) {
	t.Parallel()
	companyID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60000")
	taskID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60001")
	memberID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60004")
	principal := auth.UserPrincipal{UserID: memberID, CompanyID: companyID}
	origin := execution.RunOrigin{
		RunID:              uuid.MustParse("00000000-0000-4000-8000-000000000031"),
		CompanyID:          companyID,
		TaskID:             &taskID,
		ConversationID:     uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60002"),
		AgentID:            uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60003"),
		ConversationKind:   "task",
		AuthorizedByUserID: &memberID,
	}
	handler := Handler{RunProofResolver: runProofResolverStub{origin: origin}}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set(
		runTokenHeader,
		"brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = request
	trusted, ok := handler.resolveLocalCommerceSigningKeyRun(c, principal)
	if !ok || trusted.CompanyID != companyID || trusted.TaskID != taskID || trusted.RunID != uuid.MustParse("00000000-0000-4000-8000-000000000031") {
		t.Fatalf("resolved local Commerce Run scope = %+v / %t", trusted, ok)
	}

	for name, mutate := range map[string]func(*http.Request){
		"missing proof": func(request *http.Request) { request.Header.Del(runTokenHeader) },
		"multiple proofs": func(request *http.Request) {
			request.Header.Add(runTokenHeader, "brt_"+strings.Repeat("1", 64))
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", nil)
			request.Header.Set(
				runTokenHeader,
				"brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			)
			mutate(request)
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Request = request
			if _, ok := handler.resolveLocalCommerceSigningKeyRun(c, principal); ok {
				t.Fatalf("%s accepted invalid Run proof headers", name)
			}
			if response.Code != http.StatusUnauthorized ||
				!strings.Contains(response.Body.String(), `"code":"run_token_invalid"`) {
				t.Fatalf("%s response = %d / %s", name, response.Code, response.Body.String())
			}
		})
	}
}

func TestLocalCommerceSigningKeyRequestRejectsCallerTaskID(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(
		http.MethodPost,
		"/",
		strings.NewReader(`{"schemaVersion":1,"origin":"http://localhost:3000",`+
			`"publicJwk":{"kty":"EC","crv":"P-256","x":"`+strings.Repeat("A", 43)+
			`","y":"`+strings.Repeat("B", 43)+`"},"fingerprint":"`+
			strings.Repeat("C", 43)+`","taskId":"`+uuid.NewString()+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = request
	if _, err := bindRequest[localCommerceSigningKeyRequest](c, maxCommerceCommandBodyBytes); err == nil {
		t.Fatal("local Commerce signing-key request accepted caller-authored taskId")
	}
}

func TestSiteCommerceRoutesForwardExactSignedRequestBinding(t *testing.T) {
	t.Parallel()
	operationID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60001")
	now := time.Date(2026, time.August, 21, 3, 4, 5, 0, time.UTC)
	calls := 0
	assertSignedRequest := func(got commerce.SiteSignedRequest, path, body string) {
		t.Helper()
		wantDigest := sha256.Sum256([]byte(body))
		if got.Token != "site-request-proof" || got.Method != http.MethodPost ||
			got.Path != path || got.BodySHA256 != wantDigest {
			t.Fatalf("signed request = %+v, want path %q digest %x", got, path, wantDigest)
		}
	}
	service := &commerceServiceStub{
		createCheckout: func(
			_ context.Context,
			signed commerce.SiteSignedRequest,
			input commerce.CheckoutRequest,
		) (commerce.HostedRedirect, error) {
			calls++
			body := `{"offerAlias":"pro_monthly","operationId":"` + operationID.String() + `"}`
			assertSignedRequest(signed, "/api/v1/site-commerce/checkout-sessions", body)
			if input.OperationID != operationID || input.OfferAlias != "pro_monthly" {
				t.Fatalf("CreateCheckout input = %+v", input)
			}
			return commerce.HostedRedirect{URL: "https://checkout.stripe.com/c/pay/session"}, nil
		},
		createPortal: func(
			_ context.Context,
			signed commerce.SiteSignedRequest,
			input commerce.PortalRequest,
		) (commerce.HostedRedirect, error) {
			calls++
			body := `{"operationId":"` + operationID.String() + `"}`
			assertSignedRequest(signed, "/api/v1/site-commerce/portal-sessions", body)
			if input.OperationID != operationID {
				t.Fatalf("CreatePortal input = %+v", input)
			}
			return commerce.HostedRedirect{URL: "https://billing.stripe.com/p/session"}, nil
		},
		verifyEntitlements: func(
			_ context.Context,
			signed commerce.SiteSignedRequest,
		) (commerce.EntitlementVerification, error) {
			calls++
			assertSignedRequest(signed, "/api/v1/site-commerce/entitlements/verify", `{}`)
			return commerce.EntitlementVerification{
				Entitlements: []commerce.Entitlement{{Key: "pro", Status: "active", UpdatedAt: now}},
				Assertion:    "signed-commerce-assertion", ExpiresAt: now.Add(5 * time.Minute),
			}, nil
		},
	}
	handler := New(Handler{CommerceService: service})
	tests := []struct {
		path string
		body string
		want string
	}{
		{
			path: "/api/v1/site-commerce/checkout-sessions",
			body: `{"offerAlias":"pro_monthly","operationId":"` + operationID.String() + `"}`,
			want: `"url":"https://checkout.stripe.com/c/pay/session"`,
		},
		{
			path: "/api/v1/site-commerce/portal-sessions",
			body: `{"operationId":"` + operationID.String() + `"}`,
			want: `"url":"https://billing.stripe.com/p/session"`,
		},
		{
			path: "/api/v1/site-commerce/entitlements/verify",
			body: `{}`,
			want: `"assertion":"signed-commerce-assertion"`,
		},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
		request.Header.Set("Authorization", "BoardlySite site-request-proof")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.want) {
			t.Fatalf("POST %s = %d / %s", test.path, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("POST %s Cache-Control = %q", test.path, response.Header().Get("Cache-Control"))
		}
	}
	if calls != 3 {
		t.Fatalf("site Commerce calls = %d, want 3", calls)
	}
}

func TestSiteCommerceChallengeUsesTrustedClientIPBoundary(t *testing.T) {
	t.Parallel()
	siteID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60101")
	operationID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60102")
	expiresAt := time.Date(2026, time.August, 29, 5, 1, 0, 0, time.UTC)
	service := &commerceServiceStub{
		createSiteChallenge: func(
			_ context.Context,
			input commerce.SiteChallengeRequest,
		) (commerce.SiteChallenge, error) {
			if input.SiteID != siteID || input.OperationID != operationID ||
				input.Action != commerce.SiteChallengeActionCheckout ||
				input.ClientAddress != "203.0.113.7" {
				t.Fatalf("CreateSiteChallenge input = %+v", input)
			}
			return commerce.SiteChallenge{
				Nonce: strings.Repeat("A", 43), ExpiresAt: expiresAt,
			}, nil
		},
	}
	handler := New(Handler{
		CommerceService: service,
		TrustedProxies:  []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/site-commerce/challenges",
		strings.NewReader(`{"schemaVersion":1,"siteId":"`+siteID.String()+
			`","action":"checkout","operationId":"`+operationID.String()+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.2")
	request.RemoteAddr = "10.0.0.1:4321"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated ||
		!strings.Contains(response.Body.String(), `"schemaVersion":1`) ||
		!strings.Contains(response.Body.String(), `"nonce":"`+strings.Repeat("A", 43)+`"`) {
		t.Fatalf("challenge = %d / %s", response.Code, response.Body.String())
	}
}

func TestSiteCommerceChallengePreAuthLimitBlocksBeforeService(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 29, 8, 0, 0, 0, time.UTC)
	limiter := newSiteCommerceChallengeLimiter()
	limiter.now = func() time.Time { return now }
	limiter.globalWindowStartedAt = now
	limiter.clients["203.0.113.7"] = siteCommercePreAuthLimit{
		windowStartedAt: now,
		requestCount:    siteCommerceChallengeIPLimit,
	}
	serviceCalls := 0
	service := &commerceServiceStub{
		createSiteChallenge: func(
			context.Context,
			commerce.SiteChallengeRequest,
		) (commerce.SiteChallenge, error) {
			serviceCalls++
			return commerce.SiteChallenge{}, nil
		},
	}
	handler := New(Handler{
		CommerceService:              service,
		TrustedProxies:               []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		siteCommerceChallengeLimiter: limiter,
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/site-commerce/challenges",
		strings.NewReader(`{}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.2")
	request.RemoteAddr = "10.0.0.1:4321"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertErrorResponse(t, response, http.StatusTooManyRequests, "commerce_rate_limited")
	if serviceCalls != 0 {
		t.Fatalf("pre-auth limited challenge made %d Commerce service calls", serviceCalls)
	}
}

func TestSiteCommerceProofPreAuthLimitUsesTrustedClientIPBeforeService(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 29, 8, 0, 0, 0, time.UTC)
	limiter := newSiteCommerceProofLimiter()
	limiter.now = func() time.Time { return now }
	limiter.globalWindowStartedAt = now
	limiter.clients["203.0.113.7"] = siteCommercePreAuthLimit{
		windowStartedAt: now,
		requestCount:    siteCommerceProofIPLimit,
	}
	serviceCalls := 0
	service := &commerceServiceStub{
		verifyEntitlements: func(
			context.Context,
			commerce.SiteSignedRequest,
		) (commerce.EntitlementVerification, error) {
			serviceCalls++
			return commerce.EntitlementVerification{}, nil
		},
	}
	handler := New(Handler{
		CommerceService:          service,
		TrustedProxies:           []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		siteCommerceProofLimiter: limiter,
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/site-commerce/entitlements/verify",
		strings.NewReader(`{}`),
	)
	request.Header.Set("Authorization", "BoardlySite invalid-proof")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.2")
	request.RemoteAddr = "10.0.0.1:4321"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertErrorResponse(t, response, http.StatusTooManyRequests, "commerce_rate_limited")
	if serviceCalls != 0 {
		t.Fatalf("pre-auth limited request made %d Commerce service calls", serviceCalls)
	}
}

func TestSiteCommerceProofPreAuthLimitBoundsClientCardinality(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 29, 8, 0, 0, 0, time.UTC)
	limiter := newSiteCommerceProofLimiter()
	limiter.now = func() time.Time { return now }
	limiter.globalWindowStartedAt = now
	for index := range siteCommercePreAuthMaximumClients {
		limiter.clients[strconv.Itoa(index)] = siteCommercePreAuthLimit{windowStartedAt: now}
	}
	allowed, _ := limiter.allow("new-client")
	if !allowed || limiter.overflow.requestCount != 1 {
		t.Fatalf(
			"full pre-auth limiter did not use its bounded overflow bucket: allowed=%t overflow=%d",
			allowed,
			limiter.overflow.requestCount,
		)
	}
	if len(limiter.clients) != siteCommercePreAuthMaximumClients {
		t.Fatalf(
			"pre-auth limiter clients = %d, want %d",
			len(limiter.clients),
			siteCommercePreAuthMaximumClients,
		)
	}
	limiter.now = func() time.Time { return now.Add(siteCommercePreAuthLimitWindow) }
	allowed, _ = limiter.allow("new-client")
	if !allowed || len(limiter.clients) != 1 || limiter.overflow.requestCount != 1 {
		t.Fatalf(
			"expired pre-auth clients were not pruned: allowed=%t clients=%d",
			allowed,
			len(limiter.clients),
		)
	}
}

func TestSiteCommerceRoutesRejectNonBoardlySiteAuthorization(t *testing.T) {
	t.Parallel()
	calls := 0
	service := &commerceServiceStub{
		verifyEntitlements: func(
			context.Context,
			commerce.SiteSignedRequest,
		) (commerce.EntitlementVerification, error) {
			calls++
			return commerce.EntitlementVerification{}, nil
		},
	}
	handler := New(Handler{CommerceService: service})
	for _, authorization := range []string{"", "Bearer old-token", "boardlysite wrong-case"} {
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/site-commerce/entitlements/verify",
			strings.NewReader(`{}`),
		)
		request.Header.Set("Content-Type", "application/json")
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertErrorResponse(t, response, http.StatusUnauthorized, "authentication_required")
	}
	if calls != 0 {
		t.Fatalf("invalid authorization reached service; calls = %d", calls)
	}
}

func TestCommerceSigningKeyOwnerRoutesUseStableWrappers(t *testing.T) {
	t.Parallel()
	userID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60201")
	companyID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60202")
	siteID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60203")
	keyID := strings.Repeat("A", 43)
	principal := commerceOwnerPrincipal(userID, companyID)
	key := commerce.SigningKeySummary{
		KeyID: keyID, Fingerprint: keyID, State: commerce.SigningKeyStateActive,
	}
	service := &commerceServiceStub{
		syncSigningKeys: func(
			_ context.Context,
			got auth.UserPrincipal,
			input commerce.SyncSigningKeysRequest,
		) ([]commerce.SigningKeySummary, error) {
			if got != principal || input.SiteID != siteID {
				t.Fatalf("SyncSigningKeys input = %+v / %+v", got, input)
			}
			return []commerce.SigningKeySummary{key}, nil
		},
		revokeSigningKey: func(
			_ context.Context,
			got auth.UserPrincipal,
			input commerce.RevokeSigningKeyRequest,
		) (commerce.SigningKeySummary, error) {
			if got != principal || input.SiteID != siteID || input.KeyID != keyID {
				t.Fatalf("RevokeSigningKey input = %+v / %+v", got, input)
			}
			revoked := key
			revoked.State = commerce.SigningKeyStateRevoked
			return revoked, nil
		},
	}
	handler := commerceTestHandler(userID, companyID, principal, service)
	base := "/api/v1/companies/" + companyID.String() + "/commerce/sites/" + siteID.String()
	syncResponse := serveBearerJSON(
		handler,
		http.MethodPost,
		base+"/signing-keys/sync",
		[]byte(`{"schemaVersion":1}`),
	)
	if syncResponse.Code != http.StatusOK ||
		!strings.Contains(syncResponse.Body.String(), `"signingKeys"`) {
		t.Fatalf("sync = %d / %s", syncResponse.Code, syncResponse.Body.String())
	}
	revokeResponse := serveBearerJSON(
		handler,
		http.MethodPost,
		base+"/signing-keys/"+keyID+"/revoke",
		[]byte(`{"schemaVersion":1}`),
	)
	if revokeResponse.Code != http.StatusOK ||
		!strings.Contains(revokeResponse.Body.String(), `"state":"revoked"`) {
		t.Fatalf("revoke = %d / %s", revokeResponse.Code, revokeResponse.Body.String())
	}
}

func TestCommerceGatewayErrorsHaveStablePublicMappings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{commerce.ErrSiteNotFound, 404, "commerce_site_not_found"},
		{commerce.ErrSigningKeyNotFound, 404, "commerce_signing_key_not_found"},
		{commerce.ErrManifestNotFound, 404, "commerce_manifest_not_found"},
		{commerce.ErrCatalogDisabled, 503, "commerce_checkout_disabled"},
		{commerce.ErrSiteSigningKeyRequired, 409, "commerce_site_signing_key_required"},
		{commerce.ErrSiteAuthority, 409, "commerce_site_verification_failed"},
		{commerce.ErrRateLimited, 429, "commerce_rate_limited"},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			service := &commerceServiceStub{
				verifyEntitlements: func(
					context.Context,
					commerce.SiteSignedRequest,
				) (commerce.EntitlementVerification, error) {
					return commerce.EntitlementVerification{}, test.err
				},
			}
			handler := New(Handler{CommerceService: service})
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/site-commerce/entitlements/verify",
				strings.NewReader(`{}`),
			)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "BoardlySite site-request-proof")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertErrorResponse(t, response, test.status, test.code)
		})
	}
}

func TestCommerceRateLimitErrorIncludesRetryAfter(t *testing.T) {
	t.Parallel()
	service := &commerceServiceStub{
		verifyEntitlements: func(
			context.Context,
			commerce.SiteSignedRequest,
		) (commerce.EntitlementVerification, error) {
			return commerce.EntitlementVerification{}, &commerce.RateLimitError{
				RetryAfter: 45 * time.Second,
			}
		},
	}
	handler := New(Handler{CommerceService: service})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/site-commerce/entitlements/verify",
		strings.NewReader(`{}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "BoardlySite site-request-proof")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertErrorResponse(t, response, http.StatusTooManyRequests, "commerce_rate_limited")
	if response.Header().Get("Retry-After") != "45" {
		t.Fatalf("Retry-After = %q, want 45", response.Header().Get("Retry-After"))
	}
}

func TestSiteCommerceProviderFailureDoesNotEchoSecrets(t *testing.T) {
	t.Parallel()
	service := &commerceServiceStub{
		verifyEntitlements: func(
			context.Context,
			commerce.SiteSignedRequest,
		) (commerce.EntitlementVerification, error) {
			return commerce.EntitlementVerification{}, errors.Join(
				commerce.ErrProviderUnavailable,
				errors.New("upstream rk_secret_should_never_escape"),
			)
		},
	}
	handler := New(Handler{CommerceService: service})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/site-commerce/entitlements/verify",
		strings.NewReader(`{}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "BoardlySite site-request-proof")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "rk_") {
		t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
	}
}

func TestCommerceWebhookRoutePreservesRawBodyAndSignature(t *testing.T) {
	t.Parallel()
	raw := []byte("{ \"id\" : \"evt_raw_bytes_must_not_change\" }\n")
	calls := 0
	service := &commerceServiceStub{
		processWebhook: func(
			_ context.Context,
			payload []byte,
			signature string,
		) (commerce.WebhookReceiptResult, error) {
			calls++
			if string(payload) != string(raw) || signature != "t=123,v1=signed-raw-body" {
				t.Fatalf("ProcessWebhook input = %q / %q", payload, signature)
			}
			return commerce.WebhookReceiptResult{Processed: true}, nil
		},
	}
	handler := New(Handler{CommerceService: service})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/webhooks/stripe/commerce",
		strings.NewReader(string(raw)),
	)
	request.Header.Set("Stripe-Signature", "t=123,v1=signed-raw-body")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"processed":true`) ||
		response.Header().Get("Cache-Control") != "no-store" || calls != 1 {
		t.Fatalf("Commerce webhook = %d / %s / calls:%d", response.Code, response.Body.String(), calls)
	}
}

// A deployment without Commerce still owns the company Commerce routes: a
// member asking for the summary or account status is told, in the standard
// envelope, that the lane is disabled here (503 commerce_checkout_disabled,
// docs/api/errors.md: do not retry, hide Commerce actions) — not handed a
// bare 404 that reads like a Stripe outage.
func TestCompanyCommerceRoutesAnswerDisabledWithoutAService(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	principal := phase4Principal(userID, companyID)
	principal.Role = auth.CompanyRoleOwner
	member := func(_ context.Context, user auth.UserPrincipal, id uuid.UUID) (auth.UserPrincipal, error) {
		if user.UserID != principal.UserID || id != principal.CompanyID {
			return auth.UserPrincipal{}, company.ErrNotFound
		}
		return principal, nil
	}
	handler := New(Handler{
		AuthService:    authenticatedAuthStub(principal.UserID),
		CompanyService: &companyServiceStub{authorize: member, requirePermission: member},
	})
	for _, route := range []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/companies/" + companyID.String() + "/commerce/summary"},
		{http.MethodGet, "/api/v1/companies/" + companyID.String() + "/commerce/account-status"},
		{http.MethodGet, "/api/v1/companies/" + companyID.String() + "/commerce/manifests"},
		{http.MethodPost, "/api/v1/companies/" + companyID.String() + "/commerce/terms-acceptances"},
	} {
		response := serveBearerJSON(handler, route.method, route.path, []byte(`{}`))
		assertErrorResponse(t, response, http.StatusServiceUnavailable, "commerce_checkout_disabled")
	}
	// The public site lane and the webhook stay unregistered: nothing signs
	// for a site, so a bare 404 there is honest.
	request := httptest.NewRequest(http.MethodPost, "/api/v1/site-commerce/challenges", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("site-commerce without a service = %d, want 404", response.Code)
	}
}
