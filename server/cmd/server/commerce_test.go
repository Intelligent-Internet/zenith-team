package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/commerce"
	"boardly.dev/boardly/server/internal/config"
	"boardly.dev/boardly/server/internal/handler"
	stripeinfra "boardly.dev/boardly/server/internal/infra/stripe"
)

// The handler tells "Commerce is not enabled here" from a nil interface. An
// unconfigured deployment has a nil *DefaultCommerceService; stored as is,
// that is a non-nil interface and every Commerce route calls a nil receiver.
func TestUnconfiguredCommerceReachesTheHandlerAsANilInterface(t *testing.T) {
	t.Parallel()
	var unconfigured *commerce.DefaultCommerceService
	if got := handlerCommerceService(unconfigured); got != nil {
		t.Fatalf("handlerCommerceService(nil) = %#v, want a nil interface", got)
	}
	configured := &commerce.DefaultCommerceService{}
	if got := handlerCommerceService(configured); got != handler.CommerceService(configured) {
		t.Fatalf("handlerCommerceService(configured) = %#v, want the service", got)
	}
}

type commerceTermsCatalogEnsurerStub struct {
	calls int
	err   error
}

func (stub *commerceTermsCatalogEnsurerStub) EnsureTermsCatalog(context.Context) error {
	stub.calls++
	return stub.err
}

func TestNewCommerceServiceKeepsDisabledConfigurationUnwired(t *testing.T) {
	t.Parallel()

	service, err := newCommerceService(config.CommerceConfig{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("newCommerceService() error = %v", err)
	}
	if service != nil {
		t.Fatal("disabled Commerce configuration produced a service")
	}
}

func TestEnsureCommerceTermsCatalogBlocksStartupOnConflict(t *testing.T) {
	t.Parallel()

	conflict := errors.New("terms conflict")
	service := &commerceTermsCatalogEnsurerStub{err: conflict}
	err := ensureCommerceTermsCatalog(context.Background(), service)
	if err == nil || !errors.Is(err, conflict) || !strings.Contains(err.Error(), "catalog") {
		t.Fatalf("ensureCommerceTermsCatalog() error = %v", err)
	}
	if service.calls != 1 {
		t.Fatalf("EnsureTermsCatalog() calls = %d, want 1", service.calls)
	}
}

func TestEnsureCommerceTermsCatalogAllowsDisabledCommerce(t *testing.T) {
	t.Parallel()

	if err := ensureCommerceTermsCatalog(context.Background(), nil); err != nil {
		t.Fatalf("ensureCommerceTermsCatalog(disabled) error = %v", err)
	}
}

func TestCommerceStripeErrorMapsConnectSetupRequiredWithoutLosingProviderCause(t *testing.T) {
	t.Parallel()

	providerCause := errors.Join(
		stripeinfra.ErrConnectSetupRequired,
		errors.New("Stripe request rejected"),
	)
	mapped := commerceStripeError(providerCause)
	if !errors.Is(mapped, commerce.ErrProviderSetupRequired) ||
		!errors.Is(mapped, stripeinfra.ErrConnectSetupRequired) {
		t.Fatalf("commerceStripeError() = %v, want domain and provider causes", mapped)
	}
}

func TestCommerceWebhookErrorMapsInvalidProviderStateWithoutLosingCause(t *testing.T) {
	t.Parallel()

	providerCause := errors.Join(
		stripeinfra.ErrWebhookProviderState,
		errors.New("Stripe response failed validation"),
	)
	mapped := commerceWebhookError(providerCause)
	if !errors.Is(mapped, commerce.ErrInvalidProviderState) ||
		!errors.Is(mapped, stripeinfra.ErrWebhookProviderState) {
		t.Fatalf("commerceWebhookError() = %v, want domain and provider causes", mapped)
	}
}
