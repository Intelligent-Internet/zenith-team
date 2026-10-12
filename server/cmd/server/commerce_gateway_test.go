package main

import (
	"errors"
	"testing"

	"boardly.dev/boardly/server/internal/commerce"
	stripeinfra "boardly.dev/boardly/server/internal/infra/stripe"
)

func TestCommerceGatewayErrorMapsProviderCategoriesWithoutLosingCause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		providerTarget error
		domainTarget   error
	}{
		{
			name: "credential invalid", providerTarget: stripeinfra.ErrGatewayCredentialInvalid,
			domainTarget: commerce.ErrProviderCredentialsInvalid,
		},
		{
			name: "permission required", providerTarget: stripeinfra.ErrGatewayPermissionRequired,
			domainTarget: commerce.ErrProviderPermissionRequired,
		},
		{
			name: "rate limited", providerTarget: stripeinfra.ErrGatewayRateLimited,
			domainTarget: commerce.ErrProviderRateLimited,
		},
		{
			name: "tax setup required", providerTarget: stripeinfra.ErrGatewayTaxSetupRequired,
			domainTarget: commerce.ErrTaxSetupRequired,
		},
		{
			name: "tax location invalid", providerTarget: stripeinfra.ErrGatewayTaxLocationInvalid,
			domainTarget: commerce.ErrCheckoutTaxLocationInvalid,
		},
		{
			name: "request invalid", providerTarget: stripeinfra.ErrGatewayRequestInvalid,
			domainTarget: commerce.ErrProviderRequestInvalid,
		},
		{
			name: "idempotency conflict", providerTarget: stripeinfra.ErrGatewayIdempotencyConflict,
			domainTarget: commerce.ErrGatewayRecovery,
		},
		{
			name: "provider state invalid", providerTarget: stripeinfra.ErrGatewayProviderStateInvalid,
			domainTarget: commerce.ErrInvalidProviderState,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mapped := commerceGatewayError(errors.Join(test.providerTarget, errors.New("provider cause")))
			if !errors.Is(mapped, test.providerTarget) || !errors.Is(mapped, test.domainTarget) {
				t.Fatalf("commerceGatewayError() = %v, want provider and domain causes", mapped)
			}
		})
	}
}
