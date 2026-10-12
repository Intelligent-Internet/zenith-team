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

	stripeapi "github.com/stripe/stripe-go/v86"
	stripewebhook "github.com/stripe/stripe-go/v86/webhook"
)

type webhookStripeTransport struct {
	testing *testing.T
	mu      sync.Mutex
	seen    []recordedStripeRequest
}

type paginatedInvoicePaymentTransport struct {
	testing  *testing.T
	requests []recordedStripeRequest
}

func (transport *paginatedInvoicePaymentTransport) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	transport.testing.Helper()
	transport.requests = append(transport.requests, recordedStripeRequest{
		method:        request.Method,
		path:          request.URL.Path,
		authorization: request.Header.Get("Authorization"),
		stripeAccount: request.Header.Get("Stripe-Account"),
		query:         request.URL.Query(),
	})
	var response string
	switch request.URL.Path {
	case "/v1/subscriptions/sub_BoardlyPartial123":
		response = `{
			"id":"sub_BoardlyPartial123","object":"subscription","livemode":false,
			"status":"active","cancel_at_period_end":false,"customer":"cus_BoardlyOpaque123",
			"items":{"object":"list","data":[{"id":"si_BoardlyPartial123","object":"subscription_item","quantity":1,"current_period_start":1787270400,"current_period_end":1789948800,"price":"price_BoardlyMonthly123"}],"has_more":false},
			"latest_invoice":{"id":"in_BoardlyPartial123","object":"invoice","livemode":false,"status":"paid","amount_paid":1200,"currency":"gbp","customer":"cus_BoardlyOpaque123","status_transitions":{"paid_at":1787270410},"payments":{"object":"list","data":[],"has_more":true}}
		}`
	case "/v1/invoice_payments":
		if request.URL.Query().Get("invoice") != "in_BoardlyPartial123" {
			transport.testing.Fatalf(
				"Invoice Payment invoice filter = %q",
				request.URL.Query().Get("invoice"),
			)
		}
		if request.URL.Query().Get("starting_after") == "" {
			response = `{
				"object":"list","url":"/v1/invoice_payments","has_more":true,
				"data":[{"id":"inpay_BoardlyPartial1","object":"invoice_payment","livemode":false,"status":"paid","amount_paid":700,"amount_requested":700,"created":1787270405,"currency":"gbp","invoice":"in_BoardlyPartial123","status_transitions":{"paid_at":1787270406},"payment":{"type":"payment_intent","payment_intent":"pi_BoardlyPartial1"}}]
			}`
		} else {
			if request.URL.Query().Get("starting_after") != "inpay_BoardlyPartial1" {
				transport.testing.Fatalf(
					"Invoice Payment cursor = %q",
					request.URL.Query().Get("starting_after"),
				)
			}
			response = `{
				"object":"list","url":"/v1/invoice_payments","has_more":false,
				"data":[{"id":"inpay_BoardlyPartial2","object":"invoice_payment","livemode":false,"status":"paid","amount_paid":500,"amount_requested":500,"created":1787270407,"currency":"gbp","invoice":"in_BoardlyPartial123","status_transitions":{"paid_at":1787270408},"payment":{"type":"payment_intent","payment_intent":"pi_BoardlyPartial2"}}]
			}`
		}
	default:
		transport.testing.Fatalf("unexpected Stripe reconciliation path %q", request.URL.Path)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(response)),
		Request:    request,
	}, nil
}

func (transport *webhookStripeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.testing.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		transport.testing.Fatalf("read Stripe reconciliation request: %v", err)
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		transport.testing.Fatalf("parse Stripe reconciliation request: %v", err)
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
	case "/v1/checkout/sessions/cs_test_BoardlyCheckout123":
		response = `{
			"id":"cs_test_BoardlyCheckout123","object":"checkout.session","livemode":false,
			"status":"complete","payment_status":"paid","mode":"payment",
			"amount_total":1299,"currency":"gbp","customer":"cus_BoardlyOpaque123",
			"payment_intent":"pi_BoardlyPayment123",
			"line_items":{"object":"list","data":[{"id":"li_BoardlyLine123","object":"item","quantity":1,"price":"price_BoardlyOnce123"}],"has_more":false}
		}`
	case "/v1/subscriptions/sub_BoardlySubscription123":
		response = `{
			"id":"sub_BoardlySubscription123","object":"subscription","livemode":false,
			"status":"active","cancel_at_period_end":false,"customer":"cus_BoardlyOpaque123",
			"items":{"object":"list","data":[{"id":"si_BoardlyItem123","object":"subscription_item","quantity":1,"current_period_start":1787270400,"current_period_end":1789948800,"price":"price_BoardlyMonthly123"}],"has_more":false},
			"latest_invoice":{"id":"in_BoardlyInvoice123","object":"invoice","livemode":false,"status":"paid","amount_paid":1200,"currency":"gbp","customer":"cus_BoardlyOpaque123","status_transitions":{"paid_at":1787270410},"payments":{"object":"list","data":[{"id":"inpay_Boardly123","object":"invoice_payment","livemode":false,"status":"paid","amount_paid":1200,"amount_requested":1200,"currency":"gbp","invoice":"in_BoardlyInvoice123","payment":{"type":"payment_intent","payment_intent":"pi_BoardlyInvoicePayment123"}}],"has_more":false}}
		}`
	case "/v1/checkout/sessions/cs_test_BoardlySubscription123":
		response = `{
			"id":"cs_test_BoardlySubscription123","object":"checkout.session","livemode":false,
			"status":"complete","payment_status":"paid","mode":"subscription",
			"amount_total":1200,"currency":"gbp","customer":"cus_BoardlyOpaque123",
			"subscription":"sub_BoardlySubscription123",
			"line_items":{"object":"list","data":[{"id":"li_BoardlySubLine123","object":"item","quantity":1,"price":"price_BoardlyMonthly123"}],"has_more":false}
		}`
	case "/v1/checkout/sessions/cs_test_BoardlyExpired123":
		response = `{
			"id":"cs_test_BoardlyExpired123","object":"checkout.session","livemode":false,
			"status":"expired","payment_status":"unpaid","mode":"payment",
			"amount_total":1299,"currency":"gbp","customer":"cus_BoardlyOpaque123",
			"payment_intent":null,
			"line_items":{"object":"list","data":[{"id":"li_BoardlyExpiredLine123","object":"item","quantity":1,"price":"price_BoardlyOnce123"}],"has_more":false}
		}`
	case "/v1/invoices/in_BoardlyEventInvoice123":
		response = `{
			"id":"in_BoardlyEventInvoice123","object":"invoice","livemode":false,"status":"paid",
			"customer":"cus_BoardlyOpaque123","parent":{"type":"subscription_details","subscription_details":{"subscription":"sub_BoardlySubscription123"}}
		}`
	case "/v1/charges/ch_BoardlyCharge123":
		response = `{
			"id":"ch_BoardlyCharge123","object":"charge","livemode":false,"status":"succeeded",
			"paid":true,"refunded":true,"amount":1299,"amount_refunded":1299,"currency":"gbp",
			"customer":"cus_BoardlyOpaque123","payment_intent":"pi_BoardlyPayment123"
		}`
	case "/v1/disputes/dp_BoardlyDispute123":
		response = `{
			"id":"dp_BoardlyDispute123","object":"dispute","livemode":false,"status":"lost",
			"amount":1299,"currency":"gbp","payment_intent":"pi_BoardlyPayment123",
			"charge":"ch_BoardlyCharge123"
		}`
	default:
		transport.testing.Fatalf("unexpected Stripe reconciliation path %q", request.URL.Path)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(response)),
		Request:    request,
	}, nil
}

func TestGatewayProviderVerifiesRawWebhookBeforeReturningIdentifiers(t *testing.T) {
	t.Parallel()
	provider := newWebhookGatewayProvider(t, &webhookStripeTransport{testing: t})
	payload := []byte(`{
		"id":"evt_BoardlyEvent123","object":"event","api_version":"` + stripeapi.APIVersion + `",
		"created":1787270400,"livemode":false,"pending_webhooks":1,
		"type":"checkout.session.completed","account":"acct_BoardlyConnected123",
		"data":{"object":{"id":"cs_test_BoardlyCheckout123","object":"checkout.session","customer":"untrusted_webhook_value"}}
	}`)
	signed := stripewebhook.GenerateTestSignedPayload(&stripewebhook.UnsignedPayload{
		Payload: payload, Secret: "whsec_BoardlyWebhook123", Timestamp: time.Now(),
	})
	event, err := provider.ConstructWebhookEvent(signed.Payload, signed.Header)
	if err != nil {
		t.Fatalf("ConstructWebhookEvent() error = %v", err)
	}
	if event.ID != "evt_BoardlyEvent123" || event.AccountID != "acct_BoardlyConnected123" ||
		event.Type != "checkout.session.completed" || event.ObjectID != "cs_test_BoardlyCheckout123" ||
		event.LiveMode || event.Object.CustomerID != "" {
		t.Fatalf("ConstructWebhookEvent() = %+v", event)
	}
	if _, err := provider.ConstructWebhookEvent(signed.Payload, signed.Header+"00"); err == nil {
		t.Fatal("ConstructWebhookEvent() accepted an invalid signature")
	}
}

func TestGatewayProviderReconcilesCurrentConnectedAccountStateWithReadKey(t *testing.T) {
	t.Parallel()
	transport := &webhookStripeTransport{testing: t}
	provider := newWebhookGatewayProvider(t, transport)
	accountID := "acct_BoardlyConnected123"
	tests := []struct {
		event WebhookEventResource
		check func(*testing.T, WebhookObjectResource)
	}{
		{
			event: WebhookEventResource{
				ID: "evt_BoardlyCheckout123", AccountID: accountID,
				Type: "checkout.session.completed", ObjectID: "cs_test_BoardlyCheckout123",
			},
			check: func(t *testing.T, object WebhookObjectResource) {
				t.Helper()
				if object.CheckoutSessionID != "cs_test_BoardlyCheckout123" ||
					object.CustomerID != "cus_BoardlyOpaque123" ||
					object.PriceID != "price_BoardlyOnce123" ||
					object.PaymentIntentID != "pi_BoardlyPayment123" || object.PaymentStatus != "paid" ||
					object.Amount != 1299 || object.Currency != "gbp" {
					t.Fatalf("reconciled Checkout = %+v", object)
				}
			},
		},
		{
			event: WebhookEventResource{
				ID: "evt_BoardlySubscriptionCheckout123", AccountID: accountID,
				Type: "checkout.session.completed", ObjectID: "cs_test_BoardlySubscription123",
			},
			check: func(t *testing.T, object WebhookObjectResource) {
				t.Helper()
				if object.SubscriptionID != "sub_BoardlySubscription123" ||
					object.PaymentIntentID != "" || object.PriceID != "price_BoardlyMonthly123" {
					t.Fatalf("reconciled subscription Checkout = %+v", object)
				}
			},
		},
		{
			event: WebhookEventResource{
				ID: "evt_BoardlyExpiredCheckout123", AccountID: accountID,
				Type: "checkout.session.expired", ObjectID: "cs_test_BoardlyExpired123",
			},
			check: func(t *testing.T, object WebhookObjectResource) {
				t.Helper()
				if object.CheckoutSessionID != "cs_test_BoardlyExpired123" ||
					object.PaymentIntentID != "" || object.Status != "expired" {
					t.Fatalf("reconciled expired Checkout = %+v", object)
				}
			},
		},
		{
			event: WebhookEventResource{
				ID: "evt_BoardlySubscription123", AccountID: accountID,
				Type: "customer.subscription.updated", ObjectID: "sub_BoardlySubscription123",
			},
			check: func(t *testing.T, object WebhookObjectResource) {
				t.Helper()
				if object.SubscriptionID != "sub_BoardlySubscription123" || object.Status != "active" ||
					object.PriceID != "price_BoardlyMonthly123" || object.InvoiceID != "in_BoardlyInvoice123" ||
					object.InvoiceStatus != "paid" || len(object.InvoicePayments) != 1 ||
					object.InvoicePayments[0].ID != "inpay_Boardly123" ||
					object.InvoicePayments[0].PaymentIntentID != "pi_BoardlyInvoicePayment123" ||
					object.PeriodStart == nil || object.PeriodEnd == nil {
					t.Fatalf("reconciled Subscription = %+v", object)
				}
			},
		},
		{
			event: WebhookEventResource{
				ID: "evt_BoardlyInvoice123", AccountID: accountID,
				Type: "invoice.paid", ObjectID: "in_BoardlyEventInvoice123",
			},
			check: func(t *testing.T, object WebhookObjectResource) {
				t.Helper()
				if object.SubscriptionID != "sub_BoardlySubscription123" ||
					object.InvoiceID != "in_BoardlyInvoice123" || object.InvoiceStatus != "paid" {
					t.Fatalf("reconciled Invoice = %+v", object)
				}
			},
		},
		{
			event: WebhookEventResource{
				ID: "evt_BoardlyRefund123", AccountID: accountID,
				Type: "charge.refunded", ObjectID: "ch_BoardlyCharge123",
			},
			check: func(t *testing.T, object WebhookObjectResource) {
				t.Helper()
				if object.ChargeID != "ch_BoardlyCharge123" || object.Status != "succeeded" ||
					object.PaymentIntentID != "pi_BoardlyPayment123" || object.AmountRefunded != object.Amount {
					t.Fatalf("reconciled Charge = %+v", object)
				}
			},
		},
		{
			event: WebhookEventResource{
				ID: "evt_BoardlyDispute123", AccountID: accountID,
				Type: "charge.dispute.closed", ObjectID: "dp_BoardlyDispute123",
			},
			check: func(t *testing.T, object WebhookObjectResource) {
				t.Helper()
				if object.DisputeID != "dp_BoardlyDispute123" || object.Status != "lost" ||
					object.PaymentIntentID != "pi_BoardlyPayment123" {
					t.Fatalf("reconciled Dispute = %+v", object)
				}
			},
		},
	}
	for _, test := range tests {
		reconciled, err := provider.ReconcileWebhookEvent(context.Background(), test.event)
		if err != nil {
			t.Fatalf("ReconcileWebhookEvent(%s) error = %v", test.event.Type, err)
		}
		test.check(t, reconciled.Object)
	}

	transport.mu.Lock()
	requests := append([]recordedStripeRequest(nil), transport.seen...)
	transport.mu.Unlock()
	if len(requests) != 8 {
		t.Fatalf("reconciliation request count = %d, want 8", len(requests))
	}
	for _, request := range requests {
		if request.method != http.MethodGet || request.authorization != "Bearer rk_test_BoardlyReconcile123" ||
			request.stripeAccount != accountID || request.idempotencyKey != "" {
			t.Errorf("reconciliation request = %+v", request)
		}
	}
}

func TestGatewayProviderReadsEveryPaginatedInvoicePayment(t *testing.T) {
	t.Parallel()
	transport := &paginatedInvoicePaymentTransport{testing: t}
	provider := newWebhookGatewayProvider(t, transport)
	result, err := provider.ReconcileWebhookEvent(t.Context(), WebhookEventResource{
		ID:        "evt_BoardlyPartial123",
		AccountID: "acct_BoardlyConnected123",
		Type:      "customer.subscription.updated",
		ObjectID:  "sub_BoardlyPartial123",
	})
	if err != nil {
		t.Fatalf("ReconcileWebhookEvent() error = %v", err)
	}
	payments := result.Object.InvoicePayments
	if len(payments) != 2 ||
		payments[0].ID != "inpay_BoardlyPartial1" ||
		payments[0].PaymentIntentID != "pi_BoardlyPartial1" || payments[0].Amount != 700 ||
		payments[1].ID != "inpay_BoardlyPartial2" ||
		payments[1].PaymentIntentID != "pi_BoardlyPartial2" || payments[1].Amount != 500 ||
		payments[0].PaidAt == nil || payments[1].PaidAt == nil {
		t.Fatalf("Invoice payments = %+v", payments)
	}
	if len(transport.requests) != 3 {
		t.Fatalf("Stripe request count = %d, want 3", len(transport.requests))
	}
}

func TestGatewayProviderRejectsWebhookModeAndAccountConfusion(t *testing.T) {
	t.Parallel()
	provider := newWebhookGatewayProvider(t, &webhookStripeTransport{testing: t})
	tests := []WebhookEventResource{
		{
			ID: "evt_BoardlyWrongMode123", AccountID: "acct_BoardlyConnected123",
			Type: "checkout.session.completed", ObjectID: "cs_test_BoardlyCheckout123", LiveMode: true,
		},
		{
			ID: "evt_BoardlyPlatform123", AccountID: "acct_BoardlySandboxPlatform",
			Type: "checkout.session.completed", ObjectID: "cs_test_BoardlyCheckout123",
		},
		{
			ID: "evt_BoardlyNoAccount123", Type: "checkout.session.completed",
			ObjectID: "cs_test_BoardlyCheckout123",
		},
	}
	for _, event := range tests {
		if _, err := provider.ReconcileWebhookEvent(context.Background(), event); !errors.Is(
			err,
			ErrWebhookProviderState,
		) {
			t.Errorf(
				"ReconcileWebhookEvent(%+v) error = %v, want provider-state sentinel",
				event,
				err,
			)
		}
	}
}

func newWebhookGatewayProvider(t *testing.T, transport http.RoundTripper) *GatewayProvider {
	t.Helper()
	provider, err := NewGatewayProvider(GatewayOptions{
		CommerceKey: "rk_test_BoardlyCommerce123", ReconcileKey: "rk_test_BoardlyReconcile123",
		WebhookSecret: "whsec_BoardlyWebhook123", PlatformAccountID: "acct_BoardlySandboxPlatform",
		HTTPClient: &http.Client{Transport: transport, Timeout: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("NewGatewayProvider() error = %v", err)
	}
	return provider
}
