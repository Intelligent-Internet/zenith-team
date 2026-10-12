package stripe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordedStripeRequest struct {
	method         string
	path           string
	query          url.Values
	authorization  string
	stripeAccount  string
	idempotencyKey string
	form           url.Values
}

type stripeRoundTripper struct {
	testing          *testing.T
	mu               sync.Mutex
	seen             []recordedStripeRequest
	accountListPages map[string]string
	accountCreateErr string
}

func (transport *stripeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.testing.Helper()
	body := []byte(nil)
	if request.Body != nil {
		var err error
		body, err = io.ReadAll(request.Body)
		if err != nil {
			transport.testing.Fatalf("read Stripe request body: %v", err)
		}
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		transport.testing.Fatalf("parse Stripe request body: %v", err)
	}
	transport.mu.Lock()
	transport.seen = append(transport.seen, recordedStripeRequest{
		method: request.Method, path: request.URL.Path,
		query:          request.URL.Query(),
		authorization:  request.Header.Get("Authorization"),
		stripeAccount:  request.Header.Get("Stripe-Account"),
		idempotencyKey: request.Header.Get("Idempotency-Key"),
		form:           form,
	})
	transport.mu.Unlock()

	now := time.Now().Unix()
	statusCode := http.StatusOK
	var response string
	switch request.URL.Path {
	case "/v1/account":
		response = `{"id":"acct_BoardlySandboxPlatform","object":"account","country":"GB"}`
	case "/v1/accounts":
		if request.Method == http.MethodGet {
			response = transport.accountListPages[request.URL.Query().Get("starting_after")]
			if response == "" {
				response = `{"object":"list","data":[],"has_more":false,"url":"/v1/accounts"}`
			}
			break
		}
		if transport.accountCreateErr != "" {
			statusCode = http.StatusBadRequest
			response = transport.accountCreateErr
			break
		}
		response = `{
			"id":"acct_BoardlyConnected123","object":"account",
			"country":null,"default_currency":null,"details_submitted":false,
			"charges_enabled":false,"payouts_enabled":false,
			"controller":{"fees":{"payer":"account"},"losses":{"payments":"stripe"},
				"requirement_collection":"stripe","stripe_dashboard":{"type":"full"}},
			"capabilities":{},
			"requirements":{"currently_due":[],"eventually_due":[],"past_due":[]}
		}`
	case "/v1/account_sessions":
		response = `{"id":"accs_test","object":"account_session",` +
			`"account":"acct_BoardlyConnected123","client_secret":"acs_secret_memory_only",` +
			`"expires_at":` + integerString(now+3600) + `,"livemode":false}`
	case "/v1/account_links":
		response = `{"object":"account_link","created":` + integerString(now) +
			`,"expires_at":` + integerString(now+3600) +
			`,"url":"https://connect.stripe.com/setup/s/test_token"}`
	default:
		transport.testing.Fatalf("unexpected Stripe path %q", request.URL.Path)
	}
	return &http.Response{
		StatusCode: statusCode,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(response)),
		Request:    request,
	}, nil
}

func TestConnectProviderClassifiesConnectSetupRequiredBeforeAccountCreation(t *testing.T) {
	t.Parallel()

	transport := &stripeRoundTripper{
		testing: t,
		accountCreateErr: `{"error":{"message":` +
			`"You can only create new accounts if you've signed up for Connect, which you can do at https://dashboard.stripe.com/connect.",` +
			`"type":"invalid_request_error"}}`,
	}
	provider, err := NewConnectProvider(Options{
		SecretKey:         "sk_test_FakeConnectSecret",
		PlatformAccountID: "acct_BoardlySandboxPlatform",
		PlatformCountry:   "GB",
		HTTPClient:        &http.Client{Transport: transport, Timeout: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("NewConnectProvider() error = %v", err)
	}

	_, err = provider.CreateAccount(context.Background(), CreateAccountInput{
		IdempotencyKey:            "boardly:connect-account:v1:setup-required",
		ControllerTemplateVersion: 1,
		Metadata: map[string]string{
			"boardly_company_id": "0197f590-a482-78f4-827b-d1db1598af01",
		},
	})
	if !errors.Is(err, ErrConnectSetupRequired) {
		t.Fatalf("CreateAccount() error = %v, want Connect setup required", err)
	}
}

func TestConnectProviderKeepsOtherInvalidRequestsUnclassified(t *testing.T) {
	t.Parallel()

	transport := &stripeRoundTripper{
		testing: t,
		accountCreateErr: `{"error":{"message":"Unsupported controller combination.",` +
			`"param":"controller","type":"invalid_request_error"}}`,
	}
	provider, err := NewConnectProvider(Options{
		SecretKey:         "sk_test_FakeConnectSecret",
		PlatformAccountID: "acct_BoardlySandboxPlatform",
		PlatformCountry:   "GB",
		HTTPClient:        &http.Client{Transport: transport, Timeout: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("NewConnectProvider() error = %v", err)
	}

	_, err = provider.CreateAccount(context.Background(), CreateAccountInput{
		IdempotencyKey:            "boardly:connect-account:v1:invalid-controller",
		ControllerTemplateVersion: 1,
		Metadata: map[string]string{
			"boardly_company_id": "0197f590-a482-78f4-827b-d1db1598af01",
		},
	})
	if err == nil || errors.Is(err, ErrConnectSetupRequired) {
		t.Fatalf("CreateAccount() error = %v, want unclassified provider error", err)
	}
}

func TestConnectProviderSendsExactControllerAndOnboardingContracts(t *testing.T) {
	t.Parallel()
	transport := &stripeRoundTripper{testing: t}
	provider, err := NewConnectProvider(Options{
		SecretKey:         "sk_test_FakeConnectSecret",
		PlatformAccountID: "acct_BoardlySandboxPlatform",
		PlatformCountry:   "GB",
		HTTPClient: &http.Client{
			Transport: transport,
			Timeout:   5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("NewConnectProvider() error = %v", err)
	}
	ctx := context.Background()
	account, err := provider.CreateAccount(ctx, CreateAccountInput{
		IdempotencyKey:            "boardly:connect-account:v1:stable-operation",
		ControllerTemplateVersion: 1,
		Metadata: map[string]string{
			"boardly_company_id": "0197f590-a482-78f4-827b-d1db1598af01",
		},
	})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if account.ID != "acct_BoardlyConnected123" ||
		account.Controller.LossesPayments != "stripe" ||
		account.Controller.FeesPayer != "account" ||
		account.Controller.RequirementCollection != "stripe" ||
		account.Controller.DashboardType != "full" {
		t.Fatalf("Connected Account projection = %+v", account)
	}
	if account.Country != "" || account.CardPaymentsStatus != CapabilityStatusUnrequested ||
		account.TransfersStatus != CapabilityStatusUnrequested {
		t.Fatalf("Connected Account prefilled country/capabilities: %+v", account)
	}

	session, err := provider.CreateAccountSession(ctx, AccountSessionInput{
		AccountID:      account.ID,
		IdempotencyKey: "boardly:connect-account-session:v1:request",
	})
	if err != nil {
		t.Fatalf("CreateAccountSession() error = %v", err)
	}
	if session.ClientSecret != "acs_secret_memory_only" || !session.ExpiresAt.After(time.Now()) {
		t.Fatalf("AccountSession = %+v", session)
	}

	link, err := provider.CreateOnboardingLink(ctx, OnboardingLinkInput{
		AccountID:      account.ID,
		IdempotencyKey: "boardly:connect-account-link:v1:request",
		ReturnURL:      "https://app.boardly.test/connect/stripe/return",
		RefreshURL:     "https://app.boardly.test/connect/stripe/refresh",
	})
	if err != nil {
		t.Fatalf("CreateOnboardingLink() error = %v", err)
	}
	if link.URL != "https://connect.stripe.com/setup/s/test_token" || !link.ExpiresAt.After(time.Now()) {
		t.Fatalf("Account Link = %+v", link)
	}

	transport.mu.Lock()
	requests := append([]recordedStripeRequest(nil), transport.seen...)
	transport.mu.Unlock()
	if len(requests) != 4 {
		t.Fatalf("Stripe request count = %d, want 4", len(requests))
	}
	platform := requests[0]
	if platform.method != http.MethodGet || platform.path != "/v1/account" ||
		platform.authorization != "Bearer sk_test_FakeConnectSecret" {
		t.Fatalf("Stripe platform preflight request = %+v", platform)
	}
	create := requests[1]
	if create.method != http.MethodPost || create.path != "/v1/accounts" ||
		create.authorization != "Bearer sk_test_FakeConnectSecret" ||
		create.idempotencyKey != "boardly:connect-account:v1:stable-operation" {
		t.Fatalf("Stripe Account request = %+v", create)
	}
	wantCreateFields := map[string]string{
		"controller[fees][payer]":            "account",
		"controller[losses][payments]":       "stripe",
		"controller[requirement_collection]": "stripe",
		"controller[stripe_dashboard][type]": "full",
		"metadata[boardly_company_id]":       "0197f590-a482-78f4-827b-d1db1598af01",
	}
	for key, value := range wantCreateFields {
		if create.form.Get(key) != value {
			t.Errorf("Stripe Account field %q = %q, want %q", key, create.form.Get(key), value)
		}
	}
	if create.form.Has("country") || create.form.Has("capabilities[card_payments][requested]") ||
		create.form.Has("capabilities[transfers][requested]") || create.form.Has("type") {
		t.Fatalf("Stripe Account request preselected immutable fields: %v", create.form)
	}

	onboarding := requests[2]
	if onboarding.form.Get("account") != account.ID ||
		onboarding.form.Get("components[account_management][enabled]") != "true" ||
		onboarding.form.Get(
			"components[account_management][features][external_account_collection]",
		) != "true" ||
		onboarding.form.Get(
			"components[account_management][features][disable_stripe_user_authentication]",
		) != "false" ||
		onboarding.form.Get("components[account_onboarding][enabled]") != "true" ||
		onboarding.form.Get(
			"components[account_onboarding][features][external_account_collection]",
		) != "true" ||
		onboarding.form.Get(
			"components[account_onboarding][features][disable_stripe_user_authentication]",
		) != "false" {
		t.Fatalf("Stripe AccountSession form = %v", onboarding.form)
	}

	hosted := requests[3]
	if hosted.form.Get("account") != account.ID || hosted.form.Get("type") != "account_onboarding" ||
		hosted.form.Get("collection_options[fields]") != "eventually_due" ||
		hosted.form.Get("collection_options[future_requirements]") != "include" ||
		hosted.form.Get("return_url") != "https://app.boardly.test/connect/stripe/return" ||
		hosted.form.Get("refresh_url") != "https://app.boardly.test/connect/stripe/refresh" {
		t.Fatalf("Stripe Account Link form = %v", hosted.form)
	}
}

func TestValidReturnURLAllowsLoopbackHTTPOnlyInSandbox(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		value    string
		liveMode bool
		want     bool
	}{
		{name: "sandbox HTTPS", value: "https://app.boardly.test/revenue", want: true},
		{name: "live HTTPS", value: "https://app.boardly.test/revenue", liveMode: true, want: true},
		{name: "sandbox localhost HTTP", value: "http://localhost:5173/revenue", want: true},
		{name: "sandbox IPv4 loopback HTTP", value: "http://127.0.0.1:5173/connect/stripe/return", want: true},
		{name: "sandbox IPv6 loopback HTTP", value: "http://[::1]:5173/revenue", want: true},
		{name: "live loopback HTTP", value: "http://127.0.0.1:5173/connect/stripe/return", liveMode: true},
		{name: "sandbox non-loopback HTTP", value: "http://app.boardly.test/revenue"},
		{name: "credentials", value: "https://user@app.boardly.test/revenue"},
		{name: "fragment", value: "https://app.boardly.test/revenue#secret"},
		{name: "query", value: "https://app.boardly.test/revenue?next=elsewhere"},
		{name: "line break", value: "https://app.boardly.test/revenue\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := validReturnURL(test.value, test.liveMode); got != test.want {
				t.Errorf(
					"validReturnURL(%q, %t) = %t, want %t",
					test.value,
					test.liveMode,
					got,
					test.want,
				)
			}
		})
	}
}

func TestConnectProviderBuildsAccountScopedDashboardLinks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		liveMode bool
		wantURL  string
	}{
		{
			name:    "sandbox",
			wantURL: "https://dashboard.stripe.com/test/acct_BoardlyConnected123/dashboard",
		},
		{
			name:     "live",
			liveMode: true,
			wantURL:  "https://dashboard.stripe.com/acct_BoardlyConnected123/dashboard",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			provider := &ConnectProvider{liveMode: test.liveMode}
			link, err := provider.CreateDashboardLink(DashboardLinkInput{
				AccountID: "acct_BoardlyConnected123",
			})
			if err != nil || link.URL != test.wantURL {
				t.Fatalf("CreateDashboardLink() = %+v, %v; want %q", link, err, test.wantURL)
			}
		})
	}

	provider := &ConnectProvider{}
	for _, accountID := range []string{
		"", "acct_short", "acct_BoardlyConnected123?evil=1", "acct_BoardlyConnected123/../x",
	} {
		if _, err := provider.CreateDashboardLink(DashboardLinkInput{AccountID: accountID}); err == nil {
			t.Fatalf("CreateDashboardLink(%q) unexpectedly succeeded", accountID)
		}
	}
}

func TestConnectProviderFindAccountByMetadataUsesOneBoundedPageAndCursor(t *testing.T) {
	t.Parallel()
	const firstCursor = "acct_ScanCursor00000001"
	wantedMetadata := map[string]string{
		"boardly_metadata_schema":     "commerce_account_v1",
		"boardly_operation_type":      "account_provisioning",
		"boardly_operation_id":        "0198c09b-4600-7000-8000-000000000011",
		"boardly_commerce_account_id": "0198c09b-4600-7000-8000-000000000012",
		"boardly_request_sha256":      strings.Repeat("a", 64),
	}
	transport := &stripeRoundTripper{
		testing: t,
		accountListPages: map[string]string{
			"": `{"object":"list","has_more":true,"url":"/v1/accounts","data":[{` +
				`"id":"` + firstCursor + `","object":"account","metadata":{"unrelated":"true"}}]}`,
			firstCursor: `{"object":"list","has_more":false,"url":"/v1/accounts","data":[{` +
				`"id":"acct_BoardlyRecovered123","object":"account","country":"GB",` +
				`"default_currency":"gbp","details_submitted":false,"charges_enabled":false,` +
				`"payouts_enabled":false,"controller":{"fees":{"payer":"account"},` +
				`"losses":{"payments":"stripe"},"requirement_collection":"stripe",` +
				`"stripe_dashboard":{"type":"full"}},"capabilities":{},` +
				`"requirements":{"currently_due":[],"eventually_due":[],"past_due":[]},` +
				`"metadata":{"boardly_metadata_schema":"commerce_account_v1",` +
				`"boardly_operation_type":"account_provisioning",` +
				`"boardly_operation_id":"0198c09b-4600-7000-8000-000000000011",` +
				`"boardly_commerce_account_id":"0198c09b-4600-7000-8000-000000000012",` +
				`"boardly_request_sha256":"` + strings.Repeat("a", 64) + `"}}]}`,
		},
	}
	provider, err := NewConnectProvider(Options{
		SecretKey:         "sk_test_FakeConnectSecret",
		PlatformAccountID: "acct_BoardlySandboxPlatform",
		PlatformCountry:   "GB",
		HTTPClient:        &http.Client{Transport: transport, Timeout: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("NewConnectProvider() error = %v", err)
	}
	createdAfter := time.Date(2026, time.August, 20, 8, 0, 0, 0, time.UTC)
	createdBefore := createdAfter.Add(23 * time.Hour)

	first, err := provider.FindAccount(context.Background(), FindAccountInput{
		Metadata: wantedMetadata, CreatedAfter: createdAfter,
		CreatedBefore: createdBefore, Limit: 100,
	})
	if err != nil {
		t.Fatalf("FindAccount(first page) error = %v", err)
	}
	if first.Found || !first.HasMore || first.NextCursor != firstCursor {
		t.Fatalf("FindAccount(first page) = %#v", first)
	}
	second, err := provider.FindAccount(context.Background(), FindAccountInput{
		Metadata: wantedMetadata, CreatedAfter: createdAfter, CreatedBefore: createdBefore,
		StartingAfter: first.NextCursor, Limit: 100,
	})
	if err != nil {
		t.Fatalf("FindAccount(second page) error = %v", err)
	}
	if !second.Found || second.HasMore || second.Account.ID != "acct_BoardlyRecovered123" ||
		second.Account.Metadata["boardly_request_sha256"] != strings.Repeat("a", 64) {
		t.Fatalf("FindAccount(second page) = %#v", second)
	}

	transport.mu.Lock()
	requests := append([]recordedStripeRequest(nil), transport.seen...)
	transport.mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("Stripe metadata recovery request count = %d, want platform plus two pages", len(requests))
	}
	for index, request := range requests[1:] {
		if request.method != http.MethodGet || request.path != "/v1/accounts" ||
			request.query.Get("created[gte]") != strconv.FormatInt(createdAfter.Unix(), 10) ||
			request.query.Get("created[lte]") != strconv.FormatInt(createdBefore.Unix(), 10) ||
			request.query.Get("limit") != "100" {
			t.Errorf("Stripe recovery page %d = %+v", index+1, request)
		}
	}
	if requests[2].query.Get("starting_after") != firstCursor {
		t.Fatalf(
			"Stripe recovery second cursor = %q, want %q",
			requests[2].query.Get("starting_after"),
			firstCursor,
		)
	}
}

func TestConnectProviderRejectsWrongModeAndMissingTimeout(t *testing.T) {
	t.Parallel()
	tests := []Options{
		{
			SecretKey:         "sk_live_wrong_mode",
			PlatformAccountID: "acct_Platform123",
			PlatformCountry:   "GB",
			HTTPClient:        &http.Client{Timeout: time.Second},
		},
		{
			SecretKey:         "sk_test_validenough",
			LiveMode:          true,
			PlatformAccountID: "acct_Platform123",
			PlatformCountry:   "GB",
			HTTPClient:        &http.Client{Timeout: time.Second},
		},
		{
			SecretKey:         "sk_test_validenough",
			PlatformAccountID: "acct_Platform123",
			PlatformCountry:   "GB",
			HTTPClient:        &http.Client{},
		},
		{
			SecretKey:       "sk_test_validenough",
			PlatformCountry: "GB",
			HTTPClient:      &http.Client{Timeout: time.Second},
		},
		{
			SecretKey:         "sk_test_validenough",
			PlatformAccountID: "acct_Platform123",
			PlatformCountry:   "gb",
			HTTPClient:        &http.Client{Timeout: time.Second},
		},
	}
	for _, options := range tests {
		if provider, err := NewConnectProvider(options); err == nil || provider != nil {
			t.Fatalf("NewConnectProvider(%+v) = %+v / %v", options, provider, err)
		}
	}
}

func TestConnectProviderFailsClosedWhenPlatformIdentityDoesNotMatch(t *testing.T) {
	t.Parallel()
	transport := &stripeRoundTripper{testing: t}
	provider, err := NewConnectProvider(Options{
		SecretKey:         "sk_test_FakeConnectSecret",
		PlatformAccountID: "acct_UnexpectedPlatform",
		PlatformCountry:   "GB",
		HTTPClient: &http.Client{
			Transport: transport,
			Timeout:   5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("NewConnectProvider() error = %v", err)
	}
	if _, err := provider.RetrieveAccount(
		context.Background(),
		"acct_BoardlyConnected123",
	); err == nil || !strings.Contains(err.Error(), "identity does not match") {
		t.Fatalf("RetrieveAccount() error = %v, want platform identity mismatch", err)
	}
	transport.mu.Lock()
	requestCount := len(transport.seen)
	transport.mu.Unlock()
	if requestCount != 1 {
		t.Fatalf("Stripe request count after failed preflight = %d, want 1", requestCount)
	}
}

func integerString(value int64) string {
	return strconv.FormatInt(value, 10)
}
