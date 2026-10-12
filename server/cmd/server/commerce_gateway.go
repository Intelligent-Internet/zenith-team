package main

import (
	"context"
	"errors"

	"boardly.dev/boardly/server/internal/commerce"
	stripeinfra "boardly.dev/boardly/server/internal/infra/stripe"
)

type commerceStripeGateway struct {
	provider *stripeinfra.GatewayProvider
}

var (
	_ commerce.GatewayProvider          = (*commerceStripeGateway)(nil)
	_ commerce.CheckoutRecoveryProvider = (*commerceStripeGateway)(nil)
)

type commerceStripeWebhook struct {
	provider *stripeinfra.GatewayProvider
}

var _ commerce.WebhookProvider = (*commerceStripeWebhook)(nil)

func (webhook *commerceStripeWebhook) ConstructWebhookEvent(
	payload []byte,
	signature string,
) (commerce.WebhookEvent, error) {
	event, err := webhook.provider.ConstructWebhookEvent(payload, signature)
	return commerceWebhookEvent(event), err
}

func (webhook *commerceStripeWebhook) ReconcileWebhookEvent(
	ctx context.Context,
	event commerce.WebhookEvent,
) (commerce.WebhookEvent, error) {
	reconciled, err := webhook.provider.ReconcileWebhookEvent(
		ctx,
		stripeinfra.WebhookEventResource{
			ID: event.ID, AccountID: event.AccountID, Type: event.Type,
			ObjectID: event.ObjectID, LiveMode: event.LiveMode,
			ProviderTime: event.ProviderTime,
		},
	)
	return commerceWebhookEvent(reconciled), commerceWebhookError(err)
}

func commerceWebhookError(err error) error {
	if err == nil || !errors.Is(err, stripeinfra.ErrWebhookProviderState) {
		return err
	}
	return errors.Join(commerce.ErrInvalidProviderState, err)
}

func commerceWebhookEvent(event stripeinfra.WebhookEventResource) commerce.WebhookEvent {
	invoicePayments := make([]commerce.WebhookInvoicePayment, len(event.Object.InvoicePayments))
	for index, payment := range event.Object.InvoicePayments {
		invoicePayments[index] = commerce.WebhookInvoicePayment{
			ID:              payment.ID,
			PaymentIntentID: payment.PaymentIntentID,
			Amount:          payment.Amount,
			PaidAt:          payment.PaidAt,
		}
	}
	return commerce.WebhookEvent{
		ID: event.ID, AccountID: event.AccountID, Type: event.Type,
		ObjectID: event.ObjectID, LiveMode: event.LiveMode,
		ProviderTime: event.ProviderTime,
		Object: commerce.WebhookObject{
			CheckoutSessionID: event.Object.CheckoutSessionID,
			PaymentIntentID:   event.Object.PaymentIntentID,
			CustomerID:        event.Object.CustomerID, PriceID: event.Object.PriceID,
			SubscriptionID: event.Object.SubscriptionID, InvoiceID: event.Object.InvoiceID,
			ChargeID: event.Object.ChargeID, DisputeID: event.Object.DisputeID,
			Status: event.Object.Status, PaymentStatus: event.Object.PaymentStatus,
			InvoiceStatus: event.Object.InvoiceStatus,
			Amount:        event.Object.Amount, AmountRefunded: event.Object.AmountRefunded,
			Currency:          event.Object.Currency,
			CancelAtPeriodEnd: event.Object.CancelAtPeriodEnd,
			PeriodStart:       event.Object.PeriodStart, PeriodEnd: event.Object.PeriodEnd,
			PaidAt:          event.Object.PaidAt,
			InvoicePayments: invoicePayments,
		},
	}
}

func (gateway *commerceStripeGateway) CreateProduct(
	ctx context.Context,
	input commerce.CatalogProductInput,
) (commerce.CatalogProduct, error) {
	resource, err := gateway.provider.CreateProduct(ctx, stripeinfra.ProductInput{
		AccountID: input.AccountID, IdempotencyKey: input.IdempotencyKey,
		Name: input.Name, Metadata: input.Metadata,
	})
	return commerce.CatalogProduct{ID: resource.ID}, commerceGatewayError(err)
}

func (gateway *commerceStripeGateway) CreatePrice(
	ctx context.Context,
	input commerce.CatalogPriceInput,
) (commerce.CatalogPrice, error) {
	resource, err := gateway.provider.CreatePrice(ctx, stripeinfra.PriceInput{
		AccountID: input.AccountID, IdempotencyKey: input.IdempotencyKey,
		ProductID: input.ProductID, Amount: input.Amount, Currency: input.Currency,
		Interval: input.Interval, Metadata: input.Metadata,
	})
	return commerce.CatalogPrice{ID: resource.ID}, commerceGatewayError(err)
}

func (gateway *commerceStripeGateway) CreatePortalConfiguration(
	ctx context.Context,
	input commerce.CatalogPortalInput,
) (commerce.CatalogPortal, error) {
	products := make([]stripeinfra.PortalProduct, 0, len(input.SubscriptionProducts))
	for _, product := range input.SubscriptionProducts {
		products = append(products, stripeinfra.PortalProduct{
			ProductID: product.ProductID, PriceIDs: product.PriceIDs,
		})
	}
	resource, err := gateway.provider.CreatePortalConfiguration(
		ctx,
		stripeinfra.PortalConfigurationInput{
			AccountID: input.AccountID, IdempotencyKey: input.IdempotencyKey,
			Name: input.Name, DefaultReturnURL: input.DefaultReturnURL,
			SubscriptionProducts: products, Metadata: input.Metadata,
		},
	)
	return commerce.CatalogPortal{ID: resource.ID}, commerceGatewayError(err)
}

func (gateway *commerceStripeGateway) CreateCustomer(
	ctx context.Context,
	input commerce.GatewayCustomerInput,
) (commerce.GatewayCustomer, error) {
	resource, err := gateway.provider.CreateCustomer(ctx, stripeinfra.CustomerInput{
		AccountID: input.AccountID, IdempotencyKey: input.IdempotencyKey,
		Metadata: input.Metadata,
	})
	return commerce.GatewayCustomer{ID: resource.ID}, commerceGatewayError(err)
}

func (gateway *commerceStripeGateway) CreateCheckoutSession(
	ctx context.Context,
	input commerce.GatewayCheckoutInput,
) (commerce.GatewayHostedSession, error) {
	resource, err := gateway.provider.CreateCheckoutSession(ctx, stripeinfra.CheckoutInput{
		AccountID: input.AccountID, IdempotencyKey: input.IdempotencyKey,
		CustomerID: input.CustomerID, PriceID: input.PriceID,
		Subscription: input.Subscription, AutomaticTax: input.AutomaticTax,
		SuccessURL: input.SuccessURL, CancelURL: input.CancelURL, Metadata: input.Metadata,
	})
	return commerce.GatewayHostedSession{
		ID:  resource.ID,
		URL: resource.URL,
	}, commerceGatewayError(err)
}

func (gateway *commerceStripeGateway) CreatePortalSession(
	ctx context.Context,
	input commerce.GatewayPortalInput,
) (commerce.GatewayHostedSession, error) {
	resource, err := gateway.provider.CreatePortalSession(ctx, stripeinfra.PortalInput{
		AccountID: input.AccountID, IdempotencyKey: input.IdempotencyKey,
		CustomerID: input.CustomerID, ConfigurationID: input.ConfigurationID,
		ReturnURL: input.ReturnURL,
	})
	return commerce.GatewayHostedSession{
		ID:  resource.ID,
		URL: resource.URL,
	}, commerceGatewayError(err)
}

func (gateway *commerceStripeGateway) RetrieveCheckoutSession(
	ctx context.Context,
	input commerce.GatewayCheckoutRecoveryInput,
) (commerce.GatewayHostedSession, error) {
	resource, err := gateway.provider.RetrieveCheckoutSession(
		ctx,
		stripeinfra.CheckoutRetrievalInput{
			AccountID: input.AccountID, SessionID: input.SessionID,
			CustomerID: input.CustomerID, OperationID: input.OperationID,
		},
	)
	return commerce.GatewayHostedSession{
		ID:  resource.ID,
		URL: resource.URL,
	}, commerceGatewayError(err)
}

func commerceGatewayError(err error) error {
	if err == nil {
		return nil
	}

	var target error
	switch {
	case errors.Is(err, stripeinfra.ErrGatewayCredentialInvalid):
		target = commerce.ErrProviderCredentialsInvalid
	case errors.Is(err, stripeinfra.ErrGatewayPermissionRequired):
		target = commerce.ErrProviderPermissionRequired
	case errors.Is(err, stripeinfra.ErrGatewayRateLimited):
		target = commerce.ErrProviderRateLimited
	case errors.Is(err, stripeinfra.ErrGatewayTaxSetupRequired):
		target = commerce.ErrTaxSetupRequired
	case errors.Is(err, stripeinfra.ErrGatewayTaxLocationInvalid):
		target = commerce.ErrCheckoutTaxLocationInvalid
	case errors.Is(err, stripeinfra.ErrGatewayIdempotencyConflict):
		target = commerce.ErrGatewayRecovery
	case errors.Is(err, stripeinfra.ErrGatewayRequestInvalid):
		target = commerce.ErrProviderRequestInvalid
	case errors.Is(err, stripeinfra.ErrGatewayProviderStateInvalid):
		target = commerce.ErrInvalidProviderState
	default:
		return err
	}
	return errors.Join(target, err)
}
