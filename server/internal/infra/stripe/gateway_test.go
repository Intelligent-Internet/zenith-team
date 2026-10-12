package stripe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type gatewayStripeTransport struct {
	testing *testing.T
	mu      sync.Mutex
	seen    []recordedStripeRequest
}

func (transport *gatewayStripeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.testing.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		transport.testing.Fatalf("read Stripe gateway request: %v", err)
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		transport.testing.Fatalf("parse Stripe gateway request: %v", err)
	}
	transport.mu.Lock()
	transport.seen = append(transport.seen, recordedStripeRequest{
		method: request.Method, path: request.URL.Path,
		authorization:  request.Header.Get("Authorization"),
		stripeAccount:  request.Header.Get("Stripe-Account"),
		idempotencyKey: request.Header.Get("Idempotency-Key"),
		query:          request.URL.Query(), form: form,
	})
	transport.mu.Unlock()

	var response string
	switch request.URL.Path {
	case "/v1/products":
		response = `{"id":"prod_BoardlyPlan123","object":"product","livemode":false,"name":"Boardly Pro"}`
	case "/v1/prices":
		response = `{"id":"price_BoardlyMonthly123","object":"price","active":true,"currency":"gbp","livemode":false,"product":"prod_BoardlyPlan123","recurring":{"interval":"month","interval_count":1,"usage_type":"licensed"},"type":"recurring","unit_amount":1200}`
	case "/v1/billing_portal/configurations":
		response = `{"id":"bpc_BoardlyConfig123","object":"billing_portal.configuration","active":true,"livemode":false}`
	case "/v1/customers":
		response = `{"id":"cus_BoardlyOpaque123","object":"customer","livemode":false}`
	case "/v1/checkout/sessions":
		response = `{"id":"cs_test_Boardly123","object":"checkout.session","client_reference_id":"0198c09b-4600-7000-8000-000000000015","customer":"cus_BoardlyOpaque123","livemode":false,"metadata":{"boardly_customer_id":"0198c09b-4600-7000-8000-000000000013","boardly_operation_id":"0198c09b-4600-7000-8000-000000000015"},"mode":"subscription","status":"open","url":"https://checkout.stripe.com/c/pay/cs_test_Boardly123"}`
	case "/v1/checkout/sessions/cs_test_Boardly123":
		response = `{"id":"cs_test_Boardly123","object":"checkout.session","client_reference_id":"0198c09b-4600-7000-8000-000000000015","customer":"cus_BoardlyOpaque123","livemode":false,"metadata":{"boardly_customer_id":"0198c09b-4600-7000-8000-000000000013","boardly_operation_id":"0198c09b-4600-7000-8000-000000000015"},"mode":"subscription","status":"open","url":"https://checkout.stripe.com/c/pay/cs_test_Boardly123"}`
	case "/v1/billing_portal/sessions":
		response = `{"id":"bps_Boardly123","object":"billing_portal.session","livemode":false,"url":"https://billing.stripe.com/p/session/test_Boardly123"}`
	default:
		transport.testing.Fatalf("unexpected Stripe gateway path %q", request.URL.Path)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(response)),
		Request:    request,
	}, nil
}

func TestGatewayProviderCreatesDirectChargeCatalogAndHostedSessions(t *testing.T) {
	t.Parallel()
	transport := &gatewayStripeTransport{testing: t}
	provider, err := NewGatewayProvider(GatewayOptions{
		CommerceKey:       "rk_test_BoardlyCommerce123",
		ReconcileKey:      "rk_test_BoardlyReconcile123",
		WebhookSecret:     "whsec_BoardlyWebhook123",
		PlatformAccountID: "acct_BoardlySandboxPlatform",
		HTTPClient: &http.Client{
			Transport: transport,
			Timeout:   5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("NewGatewayProvider() error = %v", err)
	}
	ctx := context.Background()
	accountID := "acct_BoardlyConnected123"
	metadata := map[string]string{
		"boardly_company_id":  "0198c09b-4600-7000-8000-000000000011",
		"boardly_manifest_id": "0198c09b-4600-7000-8000-000000000012",
	}
	product, err := provider.CreateProduct(ctx, ProductInput{
		AccountID: accountID, IdempotencyKey: "boardly:catalog:product:v1:operation",
		Name: "Boardly Pro", Metadata: metadata,
	})
	if err != nil || product.ID != "prod_BoardlyPlan123" {
		t.Fatalf("CreateProduct() = %+v, %v", product, err)
	}
	price, err := provider.CreatePrice(ctx, PriceInput{
		AccountID: accountID, IdempotencyKey: "boardly:catalog:price:v1:operation",
		ProductID: product.ID, Amount: 1200, Currency: "gbp", Interval: "month",
		Metadata: metadata,
	})
	if err != nil || price.ID != "price_BoardlyMonthly123" {
		t.Fatalf("CreatePrice() = %+v, %v", price, err)
	}
	portalConfiguration, err := provider.CreatePortalConfiguration(ctx, PortalConfigurationInput{
		AccountID: accountID, IdempotencyKey: "boardly:catalog:portal:v1:operation",
		Name: "Boardly website", DefaultReturnURL: "https://shop.example.test/account",
		SubscriptionProducts: []PortalProduct{{ProductID: product.ID, PriceIDs: []string{price.ID}}},
		Metadata:             metadata,
	})
	if err != nil || portalConfiguration.ID != "bpc_BoardlyConfig123" {
		t.Fatalf("CreatePortalConfiguration() = %+v, %v", portalConfiguration, err)
	}
	customer, err := provider.CreateCustomer(ctx, CustomerInput{
		AccountID: accountID, IdempotencyKey: "boardly:customer:v1:operation",
		Metadata: map[string]string{"boardly_customer_id": "0198c09b-4600-7000-8000-000000000013"},
	})
	if err != nil || customer.ID != "cus_BoardlyOpaque123" {
		t.Fatalf("CreateCustomer() = %+v, %v", customer, err)
	}
	checkout, err := provider.CreateCheckoutSession(ctx, CheckoutInput{
		AccountID: accountID, IdempotencyKey: "boardly:checkout:v1:operation",
		CustomerID: customer.ID, PriceID: price.ID, Subscription: true,
		AutomaticTax: true,
		SuccessURL:   "https://shop.example.test/billing/processing",
		CancelURL:    "https://shop.example.test/pricing",
		Metadata: map[string]string{
			"boardly_customer_id":  "0198c09b-4600-7000-8000-000000000013",
			"boardly_offer_id":     "0198c09b-4600-7000-8000-000000000014",
			"boardly_operation_id": "0198c09b-4600-7000-8000-000000000015",
		},
	})
	if err != nil || checkout.ID != "cs_test_Boardly123" || checkout.URL == "" {
		t.Fatalf("CreateCheckoutSession() = %+v, %v", checkout, err)
	}
	portal, err := provider.CreatePortalSession(ctx, PortalInput{
		AccountID: accountID, IdempotencyKey: "boardly:portal:v1:operation",
		CustomerID: customer.ID, ConfigurationID: portalConfiguration.ID,
		ReturnURL: "https://shop.example.test/account",
	})
	if err != nil || portal.ID != "bps_Boardly123" || portal.URL == "" {
		t.Fatalf("CreatePortalSession() = %+v, %v", portal, err)
	}

	transport.mu.Lock()
	requests := append([]recordedStripeRequest(nil), transport.seen...)
	transport.mu.Unlock()
	if len(requests) != 6 {
		t.Fatalf("Stripe gateway request count = %d, want 6", len(requests))
	}
	for _, request := range requests {
		if request.authorization != "Bearer rk_test_BoardlyCommerce123" {
			t.Errorf("%s authorization = %q", request.path, request.authorization)
		}
		if request.form.Get("application_fee_amount") != "" ||
			request.form.Get("application_fee_percent") != "" ||
			request.form.Get("transfer_data[destination]") != "" ||
			request.form.Get("on_behalf_of") != "" ||
			request.form.Get("trial_period_days") != "" {
			t.Errorf("%s contains forbidden platform-charge fields: %v", request.path, request.form)
		}
	}
	assertGatewayRequest(
		t,
		requests[0],
		"/v1/products",
		accountID,
		"boardly:catalog:product:v1:operation",
		map[string]string{
			"name": "Boardly Pro", "metadata[boardly_manifest_id]": metadata["boardly_manifest_id"],
		},
	)
	assertGatewayRequest(
		t,
		requests[1],
		"/v1/prices",
		accountID,
		"boardly:catalog:price:v1:operation",
		map[string]string{
			"product": product.ID, "unit_amount": "1200", "currency": "gbp",
			"recurring[interval]": "month", "recurring[usage_type]": "licensed",
		},
	)
	assertGatewayRequest(
		t,
		requests[2],
		"/v1/billing_portal/configurations",
		accountID,
		"boardly:catalog:portal:v1:operation",
		map[string]string{
			"default_return_url":                                                         "https://shop.example.test/account",
			"features[payment_method_update][enabled]":                                   "true",
			"features[invoice_history][enabled]":                                         "true",
			"features[subscription_cancel][enabled]":                                     "true",
			"features[subscription_cancel][mode]":                                        "at_period_end",
			"features[subscription_update][enabled]":                                     "true",
			"features[subscription_update][default_allowed_updates][0]":                  "price",
			"features[subscription_update][products][0][product]":                        product.ID,
			"features[subscription_update][products][0][prices][0]":                      price.ID,
			"features[subscription_update][products][0][adjustable_quantity][enabled]":   "false",
			"features[subscription_update][schedule_at_period_end][conditions][0][type]": "decreasing_item_amount",
			"features[subscription_update][schedule_at_period_end][conditions][1][type]": "shortening_interval",
		},
	)
	assertGatewayRequest(
		t,
		requests[4],
		"/v1/checkout/sessions",
		accountID,
		"boardly:checkout:v1:operation",
		map[string]string{
			"customer": customer.ID, "line_items[0][price]": price.ID,
			"line_items[0][quantity]": "1", "mode": "subscription",
			"client_reference_id":      "0198c09b-4600-7000-8000-000000000015",
			"automatic_tax[enabled]":   "true",
			"customer_update[address]": "auto",
			"success_url":              "https://shop.example.test/billing/processing",
			"cancel_url":               "https://shop.example.test/pricing",
			"subscription_data[metadata][boardly_offer_id]": "0198c09b-4600-7000-8000-000000000014",
		},
	)
	if requests[4].form.Has("line_items[0][adjustable_quantity][enabled]") ||
		requests[4].form.Has("allow_promotion_codes") || requests[4].form.Has("discounts[0][coupon]") {
		t.Fatalf("Checkout enables unsupported mutable pricing: %v", requests[4].form)
	}
	assertGatewayRequest(
		t,
		requests[5],
		"/v1/billing_portal/sessions",
		accountID,
		"boardly:portal:v1:operation",
		map[string]string{
			"customer": customer.ID, "configuration": portalConfiguration.ID,
			"return_url": "https://shop.example.test/account",
		},
	)
}

type gatewayStripeErrorTransport struct {
	status int
	body   string
}

func (transport gatewayStripeErrorTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: transport.status,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"Request-Id":   []string{"req_BoardlyGatewayError"},
		},
		Body:    io.NopCloser(strings.NewReader(transport.body)),
		Request: request,
	}, nil
}

func TestGatewayProviderClassifiesStripeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		status         int
		errorType      string
		code           string
		expectedTarget error
	}{
		{
			name: "customer tax location invalid", status: http.StatusBadRequest,
			errorType: "invalid_request_error", code: "customer_tax_location_invalid",
			expectedTarget: ErrGatewayTaxLocationInvalid,
		},
		{
			name: "stripe tax inactive", status: http.StatusBadRequest,
			errorType: "invalid_request_error", code: "stripe_tax_inactive",
			expectedTarget: ErrGatewayTaxSetupRequired,
		},
		{
			name: "expired credential", status: http.StatusUnauthorized,
			errorType: "invalid_request_error", code: "api_key_expired",
			expectedTarget: ErrGatewayCredentialInvalid,
		},
		{
			name: "restricted key permission missing", status: http.StatusForbidden,
			errorType: "invalid_request_error", code: "more_permissions_required",
			expectedTarget: ErrGatewayPermissionRequired,
		},
		{
			name: "rate limited", status: http.StatusTooManyRequests,
			errorType: "rate_limit", code: "rate_limit",
			expectedTarget: ErrGatewayRateLimited,
		},
		{
			name: "idempotency conflict", status: http.StatusConflict,
			errorType: "idempotency_error", code: "idempotency_key_in_use",
			expectedTarget: ErrGatewayIdempotencyConflict,
		},
		{
			name: "invalid provider request", status: http.StatusNotFound,
			errorType: "invalid_request_error", code: "resource_missing",
			expectedTarget: ErrGatewayRequestInvalid,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			provider, err := NewGatewayProvider(GatewayOptions{
				CommerceKey:       "rk_test_BoardlyCommerce123",
				ReconcileKey:      "rk_test_BoardlyReconcile123",
				WebhookSecret:     "whsec_BoardlyWebhook123",
				PlatformAccountID: "acct_BoardlySandboxPlatform",
				HTTPClient: &http.Client{
					Transport: gatewayStripeErrorTransport{
						status: test.status,
						body: `{"error":{"type":"` + test.errorType + `","code":"` +
							test.code + `","message":"provider rejected request"}}`,
					},
					Timeout: time.Second,
				},
			})
			if err != nil {
				t.Fatalf("NewGatewayProvider() error = %v", err)
			}

			_, err = provider.CreateCheckoutSession(t.Context(), CheckoutInput{
				AccountID:      "acct_BoardlyConnected123",
				IdempotencyKey: "boardly:checkout:v1:classification",
				CustomerID:     "cus_BoardlyOpaque123",
				PriceID:        "price_BoardlyMonthly123",
				AutomaticTax:   true,
				SuccessURL:     "https://shop.example.test/billing/processing",
				CancelURL:      "https://shop.example.test/pricing",
				Metadata: map[string]string{
					"boardly_customer_id":  "0198c09b-4600-7000-8000-000000000013",
					"boardly_offer_id":     "0198c09b-4600-7000-8000-000000000014",
					"boardly_operation_id": "0198c09b-4600-7000-8000-000000000015",
				},
			})
			if !errors.Is(err, test.expectedTarget) {
				t.Fatalf("CreateCheckoutSession() error = %v, want %v", err, test.expectedTarget)
			}
		})
	}
}

func TestGatewayProviderLeavesCustomerAddressUnchangedWithoutAutomaticTax(t *testing.T) {
	t.Parallel()

	transport := &gatewayStripeTransport{testing: t}
	provider, err := NewGatewayProvider(GatewayOptions{
		CommerceKey:       "rk_test_BoardlyCommerce123",
		ReconcileKey:      "rk_test_BoardlyReconcile123",
		WebhookSecret:     "whsec_BoardlyWebhook123",
		PlatformAccountID: "acct_BoardlySandboxPlatform",
		HTTPClient:        &http.Client{Transport: transport, Timeout: time.Second},
	})
	if err != nil {
		t.Fatalf("NewGatewayProvider() error = %v", err)
	}

	_, err = provider.CreateCheckoutSession(t.Context(), CheckoutInput{
		AccountID:      "acct_BoardlyConnected123",
		IdempotencyKey: "boardly:checkout:v1:no-tax",
		CustomerID:     "cus_BoardlyOpaque123",
		PriceID:        "price_BoardlyMonthly123",
		SuccessURL:     "https://shop.example.test/billing/processing",
		CancelURL:      "https://shop.example.test/pricing",
		Metadata: map[string]string{
			"boardly_customer_id":  "0198c09b-4600-7000-8000-000000000013",
			"boardly_offer_id":     "0198c09b-4600-7000-8000-000000000014",
			"boardly_operation_id": "0198c09b-4600-7000-8000-000000000015",
		},
	})
	if err != nil {
		t.Fatalf("CreateCheckoutSession() error = %v", err)
	}

	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.seen) != 1 {
		t.Fatalf("Stripe Checkout request count = %d, want 1", len(transport.seen))
	}
	form := transport.seen[0].form
	if form.Get("automatic_tax[enabled]") != "false" || form.Has("customer_update[address]") {
		t.Fatalf("Checkout without automatic tax mutated customer address: %v", form)
	}
}

func TestGatewayProviderRecoversOnlyTheBoundOpenCheckoutWithReadKey(t *testing.T) {
	t.Parallel()
	transport := &gatewayStripeTransport{testing: t}
	provider, err := NewGatewayProvider(GatewayOptions{
		CommerceKey:       "rk_test_BoardlyCommerce123",
		ReconcileKey:      "rk_test_BoardlyReconcile123",
		WebhookSecret:     "whsec_BoardlyWebhook123",
		PlatformAccountID: "acct_BoardlySandboxPlatform",
		HTTPClient:        &http.Client{Transport: transport, Timeout: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}

	recovered, err := provider.RetrieveCheckoutSession(t.Context(), CheckoutRetrievalInput{
		AccountID:   "acct_BoardlyConnected123",
		SessionID:   "cs_test_Boardly123",
		CustomerID:  "cus_BoardlyOpaque123",
		OperationID: "0198c09b-4600-7000-8000-000000000015",
	})
	if err != nil || recovered.ID != "cs_test_Boardly123" || recovered.URL == "" {
		t.Fatalf("RetrieveCheckoutSession() = %+v, %v", recovered, err)
	}

	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.seen) != 1 {
		t.Fatalf("Stripe recovery requests = %d, want 1", len(transport.seen))
	}
	request := transport.seen[0]
	if request.method != http.MethodGet ||
		request.path != "/v1/checkout/sessions/cs_test_Boardly123" ||
		request.authorization != "Bearer rk_test_BoardlyReconcile123" ||
		request.stripeAccount != "acct_BoardlyConnected123" || request.idempotencyKey != "" {
		t.Fatalf("Stripe recovery request = %+v", request)
	}
}

func assertGatewayRequest(
	t *testing.T,
	request recordedStripeRequest,
	path string,
	accountID string,
	idempotencyKey string,
	fields map[string]string,
) {
	t.Helper()
	if request.method != http.MethodPost || request.path != path ||
		request.idempotencyKey != idempotencyKey {
		t.Errorf("Stripe gateway request = %+v", request)
	}
	if request.stripeAccount != accountID {
		t.Errorf("%s Stripe-Account = %q, want %q", path, request.stripeAccount, accountID)
	}
	for key, value := range fields {
		if request.form.Get(key) != value {
			t.Errorf("%s field %q = %q, want %q", path, key, request.form.Get(key), value)
		}
	}
}

func TestGatewayProviderRejectsSecretKeysAndInvalidModes(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: &gatewayStripeTransport{testing: t}, Timeout: time.Second}
	tests := []GatewayOptions{
		{
			CommerceKey:       "sk_test_full_secret",
			ReconcileKey:      "rk_test_read",
			WebhookSecret:     "whsec_valid_value",
			PlatformAccountID: "acct_BoardlySandboxPlatform",
			HTTPClient:        client,
		},
		{
			CommerceKey:       "rk_live_write",
			ReconcileKey:      "rk_live_read",
			WebhookSecret:     "whsec_valid_value",
			PlatformAccountID: "acct_BoardlySandboxPlatform",
			HTTPClient:        client,
		},
		{
			CommerceKey:       "rk_test_write",
			ReconcileKey:      "rk_live_read",
			WebhookSecret:     "whsec_valid_value",
			PlatformAccountID: "acct_BoardlySandboxPlatform",
			HTTPClient:        client,
		},
	}
	for index, options := range tests {
		if _, err := NewGatewayProvider(options); err == nil {
			t.Errorf("NewGatewayProvider(invalid case %d) succeeded", index)
		}
	}
}

func TestGatewayProviderRefusesPlatformAccountAsDirectChargeTarget(t *testing.T) {
	t.Parallel()
	transport := &gatewayStripeTransport{testing: t}
	provider, err := NewGatewayProvider(GatewayOptions{
		CommerceKey:       "rk_test_BoardlyCommerce123",
		ReconcileKey:      "rk_test_BoardlyReconcile123",
		WebhookSecret:     "whsec_BoardlyWebhook123",
		PlatformAccountID: "acct_BoardlySandboxPlatform",
		HTTPClient:        &http.Client{Transport: transport, Timeout: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = provider.CreateCustomer(t.Context(), CustomerInput{
		AccountID:      "acct_BoardlySandboxPlatform",
		IdempotencyKey: "boardly:customer:v1:operation",
		Metadata:       map[string]string{"boardly_customer_id": "0198c09b-4600-7000-8000-000000000013"},
	})
	if err == nil {
		t.Fatal("CreateCustomer() accepted the platform as the merchant account")
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.seen) != 0 {
		t.Fatalf("Stripe requests = %d, want 0", len(transport.seen))
	}
}
