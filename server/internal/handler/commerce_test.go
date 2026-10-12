package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/commerce"
	"boardly.dev/boardly/server/internal/execution"
)

type commerceServiceStub struct {
	summary              func(context.Context, auth.UserPrincipal) (commerce.Summary, error)
	integrationReadiness func(
		context.Context,
		auth.UserPrincipal,
	) (commerce.IntegrationReadiness, error)
	acceptTerms    func(context.Context, auth.UserPrincipal, commerce.AcceptTermsInput) (commerce.AcceptanceResult, error)
	accountSession func(context.Context, auth.UserPrincipal, commerce.Environment) (commerce.AccountSession, error)
	onboardingLink func(context.Context, auth.UserPrincipal, commerce.Environment) (commerce.OnboardingLink, error)
	dashboardLink  func(context.Context, auth.UserPrincipal, commerce.Environment) (commerce.DashboardLink, error)
	submitManifest func(
		context.Context,
		auth.UserPrincipal,
		commerce.TrustedManifestContext,
		commerce.SubmitManifestInput,
	) (commerce.ManifestSubmission, error)
	manifestStatus func(context.Context, auth.UserPrincipal, commerce.ManifestStatusInput) (commerce.ManifestStatus, error)
	listManifests  func(
		context.Context,
		auth.UserPrincipal,
		commerce.ManifestListOptions,
	) (commerce.ManifestPage, error)
	approveManifest func(
		context.Context,
		auth.UserPrincipal,
		commerce.ApproveManifestInput,
	) (commerce.ManifestStatus, error)
	createSiteChallenge func(
		context.Context,
		commerce.SiteChallengeRequest,
	) (commerce.SiteChallenge, error)
	registerLocalSigningKey func(
		context.Context,
		auth.UserPrincipal,
		commerce.TrustedLocalSigningKeyContext,
		commerce.LocalSigningKeyRequest,
	) (commerce.LocalSigningKeyRegistration, error)
	syncSigningKeys func(
		context.Context,
		auth.UserPrincipal,
		commerce.SyncSigningKeysRequest,
	) ([]commerce.SigningKeySummary, error)
	revokeSigningKey func(
		context.Context,
		auth.UserPrincipal,
		commerce.RevokeSigningKeyRequest,
	) (commerce.SigningKeySummary, error)
	createCheckout     func(context.Context, commerce.SiteSignedRequest, commerce.CheckoutRequest) (commerce.HostedRedirect, error)
	createPortal       func(context.Context, commerce.SiteSignedRequest, commerce.PortalRequest) (commerce.HostedRedirect, error)
	verifyEntitlements func(
		context.Context,
		commerce.SiteSignedRequest,
	) (commerce.EntitlementVerification, error)
	sessionJWKSet  func() (commerce.SessionJWKSet, error)
	processWebhook func(context.Context, []byte, string) (commerce.WebhookReceiptResult, error)
}

func (stub *commerceServiceStub) IntegrationReadiness(
	ctx context.Context,
	principal auth.UserPrincipal,
) (commerce.IntegrationReadiness, error) {
	return stub.integrationReadiness(ctx, principal)
}

func (stub *commerceServiceStub) Summary(
	ctx context.Context,
	principal auth.UserPrincipal,
) (commerce.Summary, error) {
	return stub.summary(ctx, principal)
}

func (stub *commerceServiceStub) AcceptTerms(
	ctx context.Context,
	principal auth.UserPrincipal,
	input commerce.AcceptTermsInput,
) (commerce.AcceptanceResult, error) {
	return stub.acceptTerms(ctx, principal, input)
}

func (stub *commerceServiceStub) CreateAccountSession(
	ctx context.Context,
	principal auth.UserPrincipal,
	environment commerce.Environment,
) (commerce.AccountSession, error) {
	return stub.accountSession(ctx, principal, environment)
}

func (stub *commerceServiceStub) CreateOnboardingLink(
	ctx context.Context,
	principal auth.UserPrincipal,
	environment commerce.Environment,
) (commerce.OnboardingLink, error) {
	return stub.onboardingLink(ctx, principal, environment)
}

func (stub *commerceServiceStub) CreateDashboardLink(
	ctx context.Context,
	principal auth.UserPrincipal,
	environment commerce.Environment,
) (commerce.DashboardLink, error) {
	return stub.dashboardLink(ctx, principal, environment)
}

func (stub *commerceServiceStub) SubmitManifest(
	ctx context.Context,
	principal auth.UserPrincipal,
	trusted commerce.TrustedManifestContext,
	input commerce.SubmitManifestInput,
) (commerce.ManifestSubmission, error) {
	return stub.submitManifest(ctx, principal, trusted, input)
}

func (stub *commerceServiceStub) ManifestStatus(
	ctx context.Context,
	principal auth.UserPrincipal,
	input commerce.ManifestStatusInput,
) (commerce.ManifestStatus, error) {
	return stub.manifestStatus(ctx, principal, input)
}

func (stub *commerceServiceStub) ListManifests(
	ctx context.Context,
	principal auth.UserPrincipal,
	options commerce.ManifestListOptions,
) (commerce.ManifestPage, error) {
	return stub.listManifests(ctx, principal, options)
}

func (stub *commerceServiceStub) ApproveAndPublishManifest(
	ctx context.Context,
	principal auth.UserPrincipal,
	input commerce.ApproveManifestInput,
) (commerce.ManifestStatus, error) {
	return stub.approveManifest(ctx, principal, input)
}

func (stub *commerceServiceStub) CreateSiteChallenge(
	ctx context.Context,
	input commerce.SiteChallengeRequest,
) (commerce.SiteChallenge, error) {
	return stub.createSiteChallenge(ctx, input)
}

func (stub *commerceServiceStub) RegisterLocalSigningKey(
	ctx context.Context,
	principal auth.UserPrincipal,
	trusted commerce.TrustedLocalSigningKeyContext,
	input commerce.LocalSigningKeyRequest,
) (commerce.LocalSigningKeyRegistration, error) {
	return stub.registerLocalSigningKey(ctx, principal, trusted, input)
}

func (stub *commerceServiceStub) SyncSigningKeys(
	ctx context.Context,
	principal auth.UserPrincipal,
	input commerce.SyncSigningKeysRequest,
) ([]commerce.SigningKeySummary, error) {
	return stub.syncSigningKeys(ctx, principal, input)
}

func (stub *commerceServiceStub) RevokeSigningKey(
	ctx context.Context,
	principal auth.UserPrincipal,
	input commerce.RevokeSigningKeyRequest,
) (commerce.SigningKeySummary, error) {
	return stub.revokeSigningKey(ctx, principal, input)
}

func (stub *commerceServiceStub) CreateCheckout(
	ctx context.Context,
	signedRequest commerce.SiteSignedRequest,
	input commerce.CheckoutRequest,
) (commerce.HostedRedirect, error) {
	return stub.createCheckout(ctx, signedRequest, input)
}

func (stub *commerceServiceStub) CreatePortal(
	ctx context.Context,
	signedRequest commerce.SiteSignedRequest,
	input commerce.PortalRequest,
) (commerce.HostedRedirect, error) {
	return stub.createPortal(ctx, signedRequest, input)
}

func (stub *commerceServiceStub) VerifyEntitlements(
	ctx context.Context,
	signedRequest commerce.SiteSignedRequest,
) (commerce.EntitlementVerification, error) {
	return stub.verifyEntitlements(ctx, signedRequest)
}

func (stub *commerceServiceStub) SessionJWKSet() (commerce.SessionJWKSet, error) {
	return stub.sessionJWKSet()
}

func (stub *commerceServiceStub) ProcessWebhook(
	ctx context.Context,
	payload []byte,
	signature string,
) (commerce.WebhookReceiptResult, error) {
	return stub.processWebhook(ctx, payload, signature)
}

func TestCommerceRoutesUseOwnerPrincipalAndNoStoreResponses(t *testing.T) {
	t.Parallel()
	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598af01")
	companyID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598af02")
	principal := commerceOwnerPrincipal(userID, companyID)
	now := time.Date(2026, time.August, 20, 8, 0, 0, 0, time.UTC)
	service := &commerceServiceStub{
		summary: func(_ context.Context, got auth.UserPrincipal) (commerce.Summary, error) {
			if got != principal {
				t.Fatalf("Summary principal = %+v, want %+v", got, principal)
			}
			return commerce.Summary{
				CompanyID: companyID,
				Terms: commerce.TermsState{
					CurrentVersion: "2026-08-20", Digest: strings.Repeat("a", 64),
					DocumentURL: "https://boardly.test/commerce-terms",
					PrivacyURL:  "https://boardly.test/privacy",
				},
				Accounts: []commerce.Account{{
					Environment: commerce.EnvironmentSandbox, Configured: true,
					PublishableKey: "pk_test_publiconly123", Status: commerce.StatusNotStarted,
					SyncStatus: "not_started",
				}},
			}, nil
		},
		acceptTerms: func(
			_ context.Context,
			got auth.UserPrincipal,
			input commerce.AcceptTermsInput,
		) (commerce.AcceptanceResult, error) {
			if got != principal || input.TermsVersion != "2026-08-20" || !input.Accepted {
				t.Fatalf("AcceptTerms input = %+v / %+v", got, input)
			}
			return commerce.AcceptanceResult{
				Acceptance: commerce.Acceptance{TermsVersion: input.TermsVersion, AcceptedAt: now},
				Created:    true,
			}, nil
		},
		accountSession: func(
			_ context.Context,
			got auth.UserPrincipal,
			environment commerce.Environment,
		) (commerce.AccountSession, error) {
			if got != principal || environment != commerce.EnvironmentSandbox {
				t.Fatalf("CreateAccountSession input = %+v / %q", got, environment)
			}
			return commerce.AccountSession{
				PublishableKey: "pk_test_publiconly123",
				ClientSecret:   "acs_secret_in_memory_only",
				ExpiresAt:      now.Add(time.Hour),
			}, nil
		},
		onboardingLink: func(
			_ context.Context,
			got auth.UserPrincipal,
			environment commerce.Environment,
		) (commerce.OnboardingLink, error) {
			if got != principal || environment != commerce.EnvironmentSandbox {
				t.Fatalf("CreateOnboardingLink input = %+v / %q", got, environment)
			}
			return commerce.OnboardingLink{
				URL:       "https://connect.stripe.com/setup/s/test_token",
				ExpiresAt: now.Add(time.Hour),
			}, nil
		},
		dashboardLink: func(
			_ context.Context,
			got auth.UserPrincipal,
			environment commerce.Environment,
		) (commerce.DashboardLink, error) {
			if got != principal || environment != commerce.EnvironmentSandbox {
				t.Fatalf("CreateDashboardLink input = %+v / %q", got, environment)
			}
			return commerce.DashboardLink{
				URL: "https://dashboard.stripe.com/test/acct_BoardlyConnected123/dashboard",
			}, nil
		},
	}
	handler := commerceTestHandler(userID, companyID, principal, service)
	base := "/api/v1/companies/" + companyID.String() + "/commerce"

	requests := []struct {
		method string
		path   string
		body   []byte
		status int
	}{
		{http.MethodGet, base + "/summary", nil, http.StatusOK},
		{
			http.MethodPost, base + "/terms-acceptances",
			[]byte(`{"schemaVersion":1,"termsVersion":"2026-08-20","accepted":true}`),
			http.StatusCreated,
		},
		{
			http.MethodPost, base + "/accounts/sandbox/account-session",
			[]byte(`{"schemaVersion":1}`), http.StatusOK,
		},
		{
			http.MethodPost, base + "/accounts/sandbox/onboarding-link",
			[]byte(`{"schemaVersion":1}`), http.StatusOK,
		},
		{
			http.MethodPost, base + "/accounts/sandbox/dashboard-link",
			[]byte(`{"schemaVersion":1}`), http.StatusOK,
		},
	}
	for _, request := range requests {
		var responseBody []byte
		var status int
		var cacheControl string
		if request.body == nil {
			response := serveBearerRequest(handler, request.method, request.path, nil)
			responseBody, status = response.Body.Bytes(), response.Code
			cacheControl = response.Header().Get("Cache-Control")
		} else {
			response := serveBearerJSON(handler, request.method, request.path, request.body)
			responseBody, status = response.Body.Bytes(), response.Code
			cacheControl = response.Header().Get("Cache-Control")
		}
		if status != request.status {
			t.Fatalf("%s %s status/body = %d / %s", request.method, request.path, status, responseBody)
		}
		if cacheControl != "private, no-store" {
			t.Fatalf("%s Cache-Control = %q", request.path, cacheControl)
		}
		if strings.Contains(string(responseBody), "acct_") &&
			!strings.HasSuffix(request.path, "/dashboard-link") {
			t.Fatalf("%s leaked provider account ID: %s", request.path, responseBody)
		}
	}

	response := serveBearerJSON(
		handler,
		http.MethodPost,
		base+"/accounts/sandbox/account-session",
		[]byte(`{"schemaVersion":1}`),
	)
	var accountSession map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &accountSession); err != nil {
		t.Fatal(err)
	}
	if accountSession["schemaVersion"] != float64(1) ||
		accountSession["publishableKey"] != "pk_test_publiconly123" ||
		accountSession["clientSecret"] != "acs_secret_in_memory_only" {
		t.Fatalf("AccountSession response = %#v", accountSession)
	}
}

func TestCommerceAccountStatusUsesMemberPrincipalAndExposesOnlyReadiness(t *testing.T) {
	t.Parallel()
	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598af31")
	companyID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598af32")
	principal := commerceOwnerPrincipal(userID, companyID)
	principal.Role = auth.CompanyRoleMember
	now := time.Date(2026, time.August, 24, 9, 0, 0, 0, time.UTC)
	service := &commerceServiceStub{
		integrationReadiness: func(
			_ context.Context,
			got auth.UserPrincipal,
		) (commerce.IntegrationReadiness, error) {
			if got != principal {
				t.Fatalf("IntegrationReadiness principal = %+v, want %+v", got, principal)
			}
			country := "GB"
			return commerce.IntegrationReadiness{
				TermsAccepted: true,
				Accounts: []commerce.IntegrationAccount{{
					Environment: commerce.EnvironmentLive,
					Configured:  true, Status: commerce.StatusReady, ManifestReady: true,
					ChargesEnabled: true, PayoutsEnabled: true, Country: &country,
					SyncStatus: "current", LastReconciledAt: &now,
				}},
			}, nil
		},
	}
	handler := commerceTestHandler(userID, companyID, principal, service)
	response := serveBearerRequest(
		handler,
		http.MethodGet,
		"/api/v1/companies/"+companyID.String()+"/commerce/account-status",
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
	body := response.Body.String()
	for _, required := range []string{
		`"termsAccepted":true`, `"environment":"live"`, `"status":"ready"`,
		`"manifestReady":true`, `"chargesEnabled":true`, `"payoutsEnabled":true`,
		`"country":"GB"`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("account status missing %s: %s", required, body)
		}
	}
	for _, forbidden := range []string{"companyId", "publishableKey", "providerAccount", "acct_"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("account status leaked %q: %s", forbidden, body)
		}
	}
}

func TestCommerceManifestListUsesOpaqueKeysetPagination(t *testing.T) {
	t.Parallel()
	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598af41")
	companyID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598af42")
	principal := commerceOwnerPrincipal(userID, companyID)
	before := commerce.ManifestCursor{
		CreatedAt: time.Date(2026, time.August, 20, 8, 0, 0, 123_000, time.UTC),
		ID:        uuid.MustParse("0197f590-a482-78f4-827b-d1db1598af43"),
	}
	next := commerce.ManifestCursor{
		CreatedAt: time.Date(2026, time.August, 19, 8, 0, 0, 456_000, time.UTC),
		ID:        uuid.MustParse("0197f590-a482-78f4-827b-d1db1598af44"),
	}
	service := &commerceServiceStub{
		listManifests: func(
			_ context.Context,
			got auth.UserPrincipal,
			options commerce.ManifestListOptions,
		) (commerce.ManifestPage, error) {
			if got != principal || options.Limit != 2 || options.Before == nil ||
				*options.Before != before {
				t.Fatalf("ListManifests input = %+v / %+v", got, options)
			}
			return commerce.ManifestPage{
				Items: []commerce.ManifestReview{{
					ManifestStatus: commerce.ManifestStatus{
						ManifestID: next.ID,
						CreatedAt:  next.CreatedAt,
					},
				}},
				Next: &next,
			}, nil
		},
	}
	handler := commerceTestHandler(userID, companyID, principal, service)
	response := serveBearerRequest(
		handler,
		http.MethodGet,
		"/api/v1/companies/"+companyID.String()+"/commerce/manifests?limit=2&cursor="+
			encodeFeedCursor(before.CreatedAt, before.ID),
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
	}
	var body struct {
		Manifests  []commerce.ManifestReview `json:"manifests"`
		NextCursor *string                   `json:"nextCursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode manifest page: %v", err)
	}
	if len(body.Manifests) != 1 || body.Manifests[0].ManifestID != next.ID ||
		body.NextCursor == nil || *body.NextCursor != encodeFeedCursor(next.CreatedAt, next.ID) {
		t.Fatalf("manifest page = %s", response.Body.String())
	}
}

func TestCommerceTermsReplayReturnsOK(t *testing.T) {
	t.Parallel()
	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598af11")
	companyID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598af12")
	principal := commerceOwnerPrincipal(userID, companyID)
	service := &commerceServiceStub{
		acceptTerms: func(
			context.Context,
			auth.UserPrincipal,
			commerce.AcceptTermsInput,
		) (commerce.AcceptanceResult, error) {
			return commerce.AcceptanceResult{
				Acceptance: commerce.Acceptance{
					TermsVersion: "2026-08-20",
					AcceptedAt:   time.Date(2026, time.August, 20, 8, 0, 0, 0, time.UTC),
				},
				Created: false,
			}, nil
		},
	}
	handler := commerceTestHandler(userID, companyID, principal, service)
	response := serveBearerJSON(
		handler,
		http.MethodPost,
		"/api/v1/companies/"+companyID.String()+"/commerce/terms-acceptances",
		[]byte(`{"schemaVersion":1,"termsVersion":"2026-08-20","accepted":true}`),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
	}
}

func TestCommerceRoutesRejectInvalidCommandsBeforeService(t *testing.T) {
	t.Parallel()
	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598af21")
	companyID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598af22")
	principal := commerceOwnerPrincipal(userID, companyID)
	calls := 0
	service := &commerceServiceStub{
		summary: func(context.Context, auth.UserPrincipal) (commerce.Summary, error) {
			calls++
			return commerce.Summary{}, nil
		},
		integrationReadiness: func(
			context.Context,
			auth.UserPrincipal,
		) (commerce.IntegrationReadiness, error) {
			calls++
			return commerce.IntegrationReadiness{}, nil
		},
		acceptTerms: func(context.Context, auth.UserPrincipal, commerce.AcceptTermsInput) (commerce.AcceptanceResult, error) {
			calls++
			return commerce.AcceptanceResult{}, nil
		},
		accountSession: func(context.Context, auth.UserPrincipal, commerce.Environment) (commerce.AccountSession, error) {
			calls++
			return commerce.AccountSession{}, nil
		},
		onboardingLink: func(context.Context, auth.UserPrincipal, commerce.Environment) (commerce.OnboardingLink, error) {
			calls++
			return commerce.OnboardingLink{}, nil
		},
		dashboardLink: func(context.Context, auth.UserPrincipal, commerce.Environment) (commerce.DashboardLink, error) {
			calls++
			return commerce.DashboardLink{}, nil
		},
		listManifests: func(
			context.Context,
			auth.UserPrincipal,
			commerce.ManifestListOptions,
		) (commerce.ManifestPage, error) {
			calls++
			return commerce.ManifestPage{}, nil
		},
	}
	handler := commerceTestHandler(userID, companyID, principal, service)
	base := "/api/v1/companies/" + companyID.String() + "/commerce"
	tests := []struct {
		method string
		path   string
		body   []byte
		status int
		code   string
	}{
		{http.MethodGet, base + "/summary?extra=1", nil, 422, "validation_failed"},
		{
			http.MethodGet,
			base + "/account-status?environment=live",
			nil,
			422,
			"validation_failed",
		},
		{
			http.MethodPost,
			base + "/terms-acceptances",
			[]byte(`{"schemaVersion":2,"termsVersion":"2026-08-20","accepted":true}`),
			422,
			"validation_failed",
		},
		{
			http.MethodPost,
			base + "/terms-acceptances",
			[]byte(`{"schemaVersion":1,"termsVersion":"2026-08-20","accepted":false}`),
			422,
			"validation_failed",
		},
		{
			http.MethodPost,
			base + "/terms-acceptances",
			[]byte(`{"schemaVersion":1,"termsVersion":"2026-08-20","accepted":true,"digest":"untrusted"}`),
			400,
			"invalid_request",
		},
		{
			http.MethodPost,
			base + "/accounts/preview/account-session",
			[]byte(`{"schemaVersion":1}`),
			422,
			"validation_failed",
		},
		{
			http.MethodPost,
			base + "/accounts/live/onboarding-link?returnUrl=https://evil.test",
			[]byte(`{"schemaVersion":1}`),
			422,
			"validation_failed",
		},
		{
			http.MethodPost,
			base + "/accounts/live/onboarding-link",
			[]byte(`{"schemaVersion":1,"returnUrl":"https://evil.test"}`),
			400,
			"invalid_request",
		},
		{
			http.MethodPost,
			base + "/accounts/live/dashboard-link",
			[]byte(`{"schemaVersion":1,"url":"https://evil.test"}`),
			400,
			"invalid_request",
		},
		{
			http.MethodGet,
			base + "/manifests/not-a-uuid",
			nil,
			404,
			"commerce_manifest_not_found",
		},
		{http.MethodGet, base + "/manifests?extra=1", nil, 422, "validation_failed"},
		{http.MethodGet, base + "/manifests?limit=101", nil, 422, "validation_failed"},
		{http.MethodGet, base + "/manifests?cursor=not-a-cursor", nil, 422, "invalid_cursor"},
		{
			http.MethodPost,
			base + "/manifests/not-a-uuid/approve-publish",
			[]byte(`{"schemaVersion":1,"digest":"` + strings.Repeat("a", 64) + `"}`),
			404,
			"commerce_manifest_not_found",
		},
	}
	for _, test := range tests {
		var recorderStatus int
		var recorderCode string
		if test.body == nil {
			recorder := serveBearerRequest(handler, test.method, test.path, nil)
			recorderStatus = recorder.Code
			var payload map[string]string
			_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
			recorderCode = payload["code"]
		} else {
			recorder := serveBearerJSON(handler, test.method, test.path, test.body)
			recorderStatus = recorder.Code
			var payload map[string]string
			_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
			recorderCode = payload["code"]
		}
		if recorderStatus != test.status || recorderCode != test.code {
			t.Fatalf(
				"%s %s = %d/%q, want %d/%q",
				test.method,
				test.path,
				recorderStatus,
				recorderCode,
				test.status,
				test.code,
			)
		}
	}
	if calls != 0 {
		t.Fatalf("Commerce service calls = %d, want 0", calls)
	}
}

func commerceTestHandler(
	userID uuid.UUID,
	companyID uuid.UUID,
	principal auth.UserPrincipal,
	service CommerceService,
	runOrigins ...execution.RunOrigin,
) http.Handler {
	configuration := Handler{
		AuthService: authenticatedAuthStub(userID),
		CompanyService: &companyServiceStub{
			authorize: func(
				_ context.Context,
				got auth.UserPrincipal,
				gotCompanyID uuid.UUID,
			) (auth.UserPrincipal, error) {
				if got.UserID != userID || gotCompanyID != companyID {
					return auth.UserPrincipal{}, errors.New(
						"unexpected Commerce member identity",
					)
				}
				return principal, nil
			},
			requirePermission: func(
				_ context.Context,
				got auth.UserPrincipal,
				gotCompanyID uuid.UUID,
			) (auth.UserPrincipal, error) {
				if got.UserID != userID || gotCompanyID != companyID {
					return auth.UserPrincipal{}, errors.New("unexpected Commerce owner identity")
				}
				return principal, nil
			},
		},
		CommerceService: service,
	}
	if len(runOrigins) > 0 {
		configuration.RunProofResolver = runProofResolverStub{origin: runOrigins[0]}
	}
	return New(configuration)
}

func commerceOwnerPrincipal(userID, companyID uuid.UUID) auth.UserPrincipal {
	return auth.UserPrincipal{
		UserID: userID, SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind:  auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleOwner,
	}
}
