package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/commerce"
	"boardly.dev/boardly/server/internal/config"
	stripeinfra "boardly.dev/boardly/server/internal/infra/stripe"
)

type commerceDevelopmentEventTransport struct {
	testing           *testing.T
	query             url.Values
	permissionMissing bool
}

func (transport *commerceDevelopmentEventTransport) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	transport.testing.Helper()
	transport.query = request.URL.Query()
	if transport.permissionMissing {
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
	response := `{
		"object":"list","url":"/v1/events","has_more":true,"data":[
			{"id":"evt_BoardlyNextCursor123","object":"event","created":1787270460,
			 "livemode":false,"account":"acct_BoardlyConnected123",
			 "type":"charge.refunded","data":{"object":{"id":"ch_BoardlyCharge123"}}}
		]
	}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(response)),
		Request:    request,
	}, nil
}

func TestCommerceDevelopmentEventFallbackIsLocalSandboxOnly(t *testing.T) {
	t.Parallel()
	sandbox := config.CommerceConfig{
		Configured: true,
		Enabled:    true,
		Sandbox:    config.CommerceEnvironmentConfig{Enabled: true},
	}
	live := config.CommerceConfig{
		Configured: true,
		Enabled:    true,
		Live:       config.CommerceEnvironmentConfig{Enabled: true},
	}
	sandboxAndLive := sandbox
	sandboxAndLive.Live.Enabled = true
	tests := []struct {
		name        string
		environment string
		commerce    config.CommerceConfig
		want        bool
	}{
		{name: "local sandbox", environment: "local", commerce: sandbox, want: true},
		{
			name: "local mixed config still polls sandbox only", environment: "local",
			commerce: sandboxAndLive, want: true,
		},
		{name: "production never polls", environment: "production", commerce: sandbox},
		{name: "non-local never polls", environment: "staging", commerce: sandbox},
		{name: "live never polls", environment: "local", commerce: live},
		{
			name: "kill switch disables polling", environment: "local",
			commerce: config.CommerceConfig{
				Configured: true,
				Sandbox:    config.CommerceEnvironmentConfig{Enabled: true},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := commerceDevelopmentEventFallbackEnabled(
				test.environment,
				test.commerce,
			); got != test.want {
				t.Fatalf("commerceDevelopmentEventFallbackEnabled() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestCommerceDevelopmentEventAdapterPreservesPaginationCursor(t *testing.T) {
	t.Parallel()
	transport := &commerceDevelopmentEventTransport{testing: t}
	provider, err := stripeinfra.NewDevelopmentEventProvider(
		stripeinfra.DevelopmentEventOptions{
			ReconcileKey:      "rk_test_BoardlyReconcile123",
			PlatformAccountID: "acct_BoardlyPlatform123",
			HTTPClient: &http.Client{
				Transport: transport,
				Timeout:   2 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatalf("NewDevelopmentEventProvider() error = %v", err)
	}
	adapter := &commerceStripeDevelopmentEvents{provider: provider}
	result, err := adapter.ListEvents(context.Background(), commerce.DevelopmentEventListInput{
		AccountID:        "acct_BoardlyConnected123",
		CreatedAtOrAfter: time.Unix(1787270000, 0).UTC(),
		StartingAfter:    "evt_BoardlyPreviousCursor123",
		Limit:            10,
		Types:            []string{"charge.refunded"},
	})
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if transport.query.Get("starting_after") != "evt_BoardlyPreviousCursor123" ||
		!result.HasMore || result.NextCursor != "evt_BoardlyNextCursor123" {
		t.Fatalf("pagination mapping = query %v, result %+v", transport.query, result)
	}
}

func TestCommerceDevelopmentEventAdapterMapsMissingProviderPermission(t *testing.T) {
	t.Parallel()
	provider, err := stripeinfra.NewDevelopmentEventProvider(
		stripeinfra.DevelopmentEventOptions{
			ReconcileKey:      "rk_test_BoardlyReconcile123",
			PlatformAccountID: "acct_BoardlyPlatform123",
			HTTPClient: &http.Client{
				Transport: &commerceDevelopmentEventTransport{
					testing: t, permissionMissing: true,
				},
				Timeout: 2 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatalf("NewDevelopmentEventProvider() error = %v", err)
	}
	adapter := &commerceStripeDevelopmentEvents{provider: provider}
	_, err = adapter.ListEvents(context.Background(), commerce.DevelopmentEventListInput{
		AccountID:        "acct_BoardlyConnected123",
		CreatedAtOrAfter: time.Now().Add(-time.Minute),
		Limit:            10,
		Types:            []string{"checkout.session.completed"},
	})
	if !errors.Is(err, commerce.ErrDevelopmentEventProviderPermission) {
		t.Fatalf("ListEvents() error = %v, want Commerce permission sentinel", err)
	}
}
