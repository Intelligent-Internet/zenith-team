package stripe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type sandboxEventTransport struct {
	testing *testing.T
	mode    string
	seen    recordedStripeRequest
}

func (transport *sandboxEventTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.testing.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		transport.testing.Fatalf("read Stripe Events request: %v", err)
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		transport.testing.Fatalf("parse Stripe Events request: %v", err)
	}
	transport.seen = recordedStripeRequest{
		method: request.Method, path: request.URL.Path,
		authorization: request.Header.Get("Authorization"),
		stripeAccount: request.Header.Get("Stripe-Account"),
		query:         request.URL.Query(), form: form,
	}
	if transport.mode == "permission" {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"error":{
				"code":"more_permissions_required",
				"message":"Events Read is required",
				"type":"invalid_request_error"
			}}`)),
			Request: request,
		}, nil
	}
	account := "acct_BoardlyConnected123"
	liveMode := false
	if transport.mode == "wrong_account" {
		account = "acct_BoardlyOtherAccount123"
	}
	if transport.mode == "live" {
		liveMode = true
	}
	response := `{
		"object":"list","url":"/v1/events","has_more":true,"data":[
			{"id":"evt_BoardlyNewer123","object":"event","created":1787270460,
			 "livemode":` + boolJSON(liveMode) + `,"account":"` + account + `",
			 "type":"charge.refunded","data":{"object":{"id":"ch_BoardlyCharge123"}}},
			{"id":"evt_BoardlyOlder123","object":"event","created":1787270400,
			 "livemode":false,"account":"` + account + `",
			 "type":"checkout.session.completed","data":{"object":{"id":"cs_test_BoardlyCheckout123"}}}
		]
	}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(response)),
		Request:    request,
	}, nil
}

func TestDevelopmentEventProviderListsOneBoundedConnectedAccountPage(t *testing.T) {
	t.Parallel()
	transport := &sandboxEventTransport{testing: t}
	provider := mustNewDevelopmentEventProvider(t, transport)
	createdAtOrAfter := time.Unix(1787270100, 0).UTC()
	result, err := provider.ListEvents(context.Background(), DevelopmentEventListInput{
		AccountID:        "acct_BoardlyConnected123",
		CreatedAtOrAfter: createdAtOrAfter,
		StartingAfter:    "evt_BoardlyPreviousPage123",
		Limit:            100,
		Types: []string{
			"checkout.session.completed",
			"charge.refunded",
		},
	})
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if !result.HasMore || len(result.Events) != 2 ||
		result.Events[0].ID != "evt_BoardlyNewer123" ||
		result.Events[1].ID != "evt_BoardlyOlder123" {
		t.Fatalf("ListEvents() = %+v", result)
	}
	for _, event := range result.Events {
		if event.AccountID != "acct_BoardlyConnected123" || event.LiveMode {
			t.Fatalf("event escaped sandbox account boundary: %+v", event)
		}
	}
	request := transport.seen
	values := request.form
	if len(values) == 0 {
		values = request.query
	}
	if request.method != http.MethodGet || request.path != "/v1/events" ||
		request.authorization != "Bearer rk_test_BoardlyReconcile123" ||
		request.stripeAccount != "acct_BoardlyConnected123" ||
		values.Get("created[gte]") != "1787270100" || values.Get("limit") != "100" ||
		values.Get("starting_after") != "evt_BoardlyPreviousPage123" ||
		values.Get("types[0]") != "checkout.session.completed" ||
		values.Get("types[1]") != "charge.refunded" ||
		result.NextCursor != "evt_BoardlyOlder123" {
		t.Fatalf("Stripe Events request = %+v, values=%v", request, values)
	}
}

func TestDevelopmentEventProviderRejectsLiveAndCrossAccountEvents(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"live", "wrong_account"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			provider := mustNewDevelopmentEventProvider(
				t,
				&sandboxEventTransport{testing: t, mode: mode},
			)
			_, err := provider.ListEvents(context.Background(), DevelopmentEventListInput{
				AccountID: "acct_BoardlyConnected123", CreatedAtOrAfter: time.Now().Add(-time.Minute),
				Limit: 10, Types: []string{"checkout.session.completed", "charge.refunded"},
			})
			if err == nil {
				t.Fatalf("ListEvents() accepted %s event", mode)
			}
		})
	}
}

func TestDevelopmentEventProviderRejectsLiveCredentials(t *testing.T) {
	t.Parallel()
	_, err := NewDevelopmentEventProvider(DevelopmentEventOptions{
		ReconcileKey:      "rk_live_BoardlyReconcile123",
		PlatformAccountID: "acct_BoardlyPlatform123",
		HTTPClient:        &http.Client{Timeout: 2 * time.Second},
	})
	if err == nil {
		t.Fatal("NewDevelopmentEventProvider() accepted a live key")
	}
}

func TestDevelopmentEventProviderClassifiesMissingEventsReadPermission(t *testing.T) {
	t.Parallel()
	provider := mustNewDevelopmentEventProvider(
		t,
		&sandboxEventTransport{testing: t, mode: "permission"},
	)
	_, err := provider.ListEvents(context.Background(), DevelopmentEventListInput{
		AccountID:        "acct_BoardlyConnected123",
		CreatedAtOrAfter: time.Now().Add(-time.Minute),
		Limit:            10,
		Types:            []string{"checkout.session.completed"},
	})
	if !errors.Is(err, ErrDevelopmentEventPermissionRequired) {
		t.Fatalf("ListEvents() error = %v, want permission sentinel", err)
	}
}

func mustNewDevelopmentEventProvider(
	t *testing.T,
	transport http.RoundTripper,
) *DevelopmentEventProvider {
	t.Helper()
	provider, err := NewDevelopmentEventProvider(DevelopmentEventOptions{
		ReconcileKey:      "rk_test_BoardlyReconcile123",
		PlatformAccountID: "acct_BoardlyPlatform123",
		HTTPClient:        &http.Client{Transport: transport, Timeout: 2 * time.Second},
	})
	if err != nil {
		t.Fatalf("NewDevelopmentEventProvider() error = %v", err)
	}
	return provider
}

func boolJSON(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
