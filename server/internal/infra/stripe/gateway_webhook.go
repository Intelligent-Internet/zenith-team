package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	stripeapi "github.com/stripe/stripe-go/v86"
	stripewebhook "github.com/stripe/stripe-go/v86/webhook"
)

var ErrWebhookProviderState = errors.New("Stripe webhook provider state is invalid")

const maxWebhookTypeBytes = 128

// WebhookEventResource contains only signed routing identifiers. Provider
// object fields from a webhook are deliberately discarded and re-read with
// the reconciliation key before they cross into the Commerce domain.
type WebhookEventResource struct {
	ID           string
	AccountID    string
	Type         string
	ObjectID     string
	LiveMode     bool
	ProviderTime time.Time
	Object       WebhookObjectResource
}

type WebhookObjectResource struct {
	CheckoutSessionID string
	PaymentIntentID   string
	CustomerID        string
	PriceID           string
	SubscriptionID    string
	InvoiceID         string
	ChargeID          string
	DisputeID         string
	Status            string
	PaymentStatus     string
	InvoiceStatus     string
	Amount            int64
	AmountRefunded    int64
	Currency          string
	CancelAtPeriodEnd bool
	PeriodStart       *time.Time
	PeriodEnd         *time.Time
	PaidAt            *time.Time
	InvoicePayments   []WebhookInvoicePaymentResource
}

type WebhookInvoicePaymentResource struct {
	ID              string
	PaymentIntentID string
	Amount          int64
	PaidAt          *time.Time
}

func (provider *GatewayProvider) ConstructWebhookEvent(
	payload []byte,
	signature string,
) (WebhookEventResource, error) {
	if provider == nil || provider.reconcileClient == nil || len(payload) == 0 ||
		strings.TrimSpace(signature) == "" {
		return WebhookEventResource{}, errors.New("invalid Stripe webhook")
	}
	event, err := stripewebhook.ConstructEvent(payload, signature, provider.webhookSecret)
	if err != nil {
		return WebhookEventResource{}, fmt.Errorf("verify Stripe webhook signature: %w", err)
	}
	var object struct {
		ID string `json:"id"`
	}
	if event.Data == nil || json.Unmarshal(event.Data.Raw, &object) != nil ||
		!validWebhookEnvelope(event.ID, event.Account, string(event.Type), object.ID, event.Created) ||
		event.Livemode != provider.liveMode || event.Account == provider.platformAccountID {
		return WebhookEventResource{}, errors.New("Stripe webhook envelope is invalid")
	}
	return WebhookEventResource{
		ID: event.ID, AccountID: event.Account, Type: string(event.Type), ObjectID: object.ID,
		LiveMode: event.Livemode, ProviderTime: time.Unix(event.Created, 0).UTC(),
	}, nil
}

func (provider *GatewayProvider) ReconcileWebhookEvent(
	ctx context.Context,
	event WebhookEventResource,
) (WebhookEventResource, error) {
	if provider == nil || provider.reconcileClient == nil ||
		!validWebhookEnvelope(event.ID, event.AccountID, event.Type, event.ObjectID, 1) ||
		event.LiveMode != provider.liveMode || event.AccountID == provider.platformAccountID {
		return WebhookEventResource{}, invalidWebhookProviderState(
			"invalid Stripe webhook reconciliation request",
		)
	}

	var (
		object WebhookObjectResource
		err    error
	)
	switch event.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded",
		"checkout.session.async_payment_failed", "checkout.session.expired":
		object, err = provider.reconcileCheckout(ctx, event.AccountID, event.ObjectID)
	case "customer.subscription.created", "customer.subscription.updated",
		"customer.subscription.deleted":
		object, err = provider.reconcileSubscription(ctx, event.AccountID, event.ObjectID)
	case "invoice.paid", "invoice.payment_failed":
		object, err = provider.reconcileInvoiceSubscription(ctx, event.AccountID, event.ObjectID)
	case "charge.refunded":
		object, err = provider.reconcileCharge(ctx, event.AccountID, event.ObjectID)
	case "charge.dispute.created", "charge.dispute.closed":
		object, err = provider.reconcileDispute(ctx, event.AccountID, event.ObjectID)
	default:
		return event, nil
	}
	if err != nil {
		return WebhookEventResource{}, err
	}
	event.Object = object
	return event, nil
}

func (provider *GatewayProvider) reconcileCheckout(
	ctx context.Context,
	accountID string,
	sessionID string,
) (WebhookObjectResource, error) {
	if !validProviderID(sessionID, "cs_") {
		return WebhookObjectResource{}, invalidWebhookProviderState(
			"invalid Stripe Checkout Session ID",
		)
	}
	params := &stripeapi.CheckoutSessionRetrieveParams{}
	params.SetStripeAccount(accountID)
	for _, expansion := range []string{
		"customer", "payment_intent", "subscription", "line_items.data.price",
	} {
		params.AddExpand(expansion)
	}
	session, err := provider.reconcileClient.V1CheckoutSessions.Retrieve(ctx, sessionID, params)
	if err != nil {
		return WebhookObjectResource{}, fmt.Errorf("Stripe retrieve Checkout Session: %w", err)
	}
	if session == nil || session.ID != sessionID || session.Livemode != provider.liveMode ||
		session.Customer == nil || !validProviderID(session.Customer.ID, "cus_") ||
		session.LineItems == nil || session.LineItems.HasMore || len(session.LineItems.Data) != 1 ||
		session.LineItems.Data[0] == nil || session.LineItems.Data[0].Quantity != 1 ||
		session.LineItems.Data[0].Price == nil ||
		!validProviderID(session.LineItems.Data[0].Price.ID, "price_") ||
		!validCurrency(string(session.Currency)) || session.AmountTotal < 0 {
		return WebhookObjectResource{}, invalidWebhookProviderState(
			"Stripe Checkout Session response is invalid",
		)
	}
	object := WebhookObjectResource{
		CheckoutSessionID: session.ID,
		CustomerID:        session.Customer.ID,
		PriceID:           session.LineItems.Data[0].Price.ID,
		Status:            string(session.Status),
		PaymentStatus:     string(session.PaymentStatus),
		Amount:            session.AmountTotal,
		Currency:          string(session.Currency),
	}
	switch session.Mode {
	case stripeapi.CheckoutSessionModePayment:
		if session.PaymentIntent == nil {
			if session.Status != stripeapi.CheckoutSessionStatusExpired ||
				session.PaymentStatus == stripeapi.CheckoutSessionPaymentStatusPaid {
				return WebhookObjectResource{}, invalidWebhookProviderState(
					"Stripe Checkout payment response is invalid",
				)
			}
			break
		}
		if !validProviderID(session.PaymentIntent.ID, "pi_") {
			return WebhookObjectResource{}, invalidWebhookProviderState(
				"Stripe Checkout payment response is invalid",
			)
		}
		object.PaymentIntentID = session.PaymentIntent.ID
	case stripeapi.CheckoutSessionModeSubscription:
		if session.Subscription == nil || !validProviderID(session.Subscription.ID, "sub_") {
			return WebhookObjectResource{}, invalidWebhookProviderState(
				"Stripe Checkout subscription response is invalid",
			)
		}
		object.SubscriptionID = session.Subscription.ID
	default:
		return WebhookObjectResource{}, invalidWebhookProviderState(
			"Stripe Checkout mode is unsupported",
		)
	}
	return object, nil
}

func (provider *GatewayProvider) reconcileSubscription(
	ctx context.Context,
	accountID string,
	subscriptionID string,
) (WebhookObjectResource, error) {
	if !validProviderID(subscriptionID, "sub_") {
		return WebhookObjectResource{}, invalidWebhookProviderState(
			"invalid Stripe Subscription ID",
		)
	}
	params := &stripeapi.SubscriptionRetrieveParams{}
	params.SetStripeAccount(accountID)
	for _, expansion := range []string{
		"customer", "items.data.price", "latest_invoice",
		"latest_invoice.payments.data.payment.payment_intent",
	} {
		params.AddExpand(expansion)
	}
	subscription, err := provider.reconcileClient.V1Subscriptions.Retrieve(
		ctx,
		subscriptionID,
		params,
	)
	if err != nil {
		return WebhookObjectResource{}, fmt.Errorf("Stripe retrieve Subscription: %w", err)
	}
	return provider.subscriptionObject(ctx, accountID, subscriptionID, subscription)
}

func (provider *GatewayProvider) reconcileInvoiceSubscription(
	ctx context.Context,
	accountID string,
	invoiceID string,
) (WebhookObjectResource, error) {
	if !validProviderID(invoiceID, "in_") {
		return WebhookObjectResource{}, invalidWebhookProviderState(
			"invalid Stripe Invoice ID",
		)
	}
	params := &stripeapi.InvoiceRetrieveParams{}
	params.SetStripeAccount(accountID)
	params.AddExpand("parent.subscription_details.subscription")
	invoice, err := provider.reconcileClient.V1Invoices.Retrieve(ctx, invoiceID, params)
	if err != nil {
		return WebhookObjectResource{}, fmt.Errorf("Stripe retrieve Invoice: %w", err)
	}
	if invoice == nil || invoice.ID != invoiceID || invoice.Livemode != provider.liveMode ||
		invoice.Parent == nil || invoice.Parent.SubscriptionDetails == nil ||
		invoice.Parent.SubscriptionDetails.Subscription == nil ||
		!validProviderID(invoice.Parent.SubscriptionDetails.Subscription.ID, "sub_") {
		return WebhookObjectResource{}, invalidWebhookProviderState(
			"Stripe Invoice response is invalid",
		)
	}
	// The event Invoice may already be stale. Always project the Subscription's
	// current Price and latest Invoice instead of trusting the event snapshot.
	return provider.reconcileSubscription(
		ctx,
		accountID,
		invoice.Parent.SubscriptionDetails.Subscription.ID,
	)
}

func (provider *GatewayProvider) subscriptionObject(
	ctx context.Context,
	accountID string,
	subscriptionID string,
	subscription *stripeapi.Subscription,
) (WebhookObjectResource, error) {
	if subscription == nil || subscription.ID != subscriptionID ||
		subscription.Livemode != provider.liveMode || subscription.Customer == nil ||
		!validProviderID(subscription.Customer.ID, "cus_") || subscription.Items == nil ||
		subscription.Items.HasMore || len(subscription.Items.Data) != 1 ||
		subscription.Items.Data[0] == nil || subscription.Items.Data[0].Quantity != 1 ||
		subscription.Items.Data[0].Price == nil ||
		!validProviderID(subscription.Items.Data[0].Price.ID, "price_") {
		return WebhookObjectResource{}, invalidWebhookProviderState(
			"Stripe Subscription response is invalid",
		)
	}
	item := subscription.Items.Data[0]
	if item.CurrentPeriodStart <= 0 || item.CurrentPeriodEnd <= item.CurrentPeriodStart {
		return WebhookObjectResource{}, invalidWebhookProviderState(
			"Stripe Subscription period is invalid",
		)
	}
	periodStart := time.Unix(item.CurrentPeriodStart, 0).UTC()
	periodEnd := time.Unix(item.CurrentPeriodEnd, 0).UTC()
	object := WebhookObjectResource{
		CustomerID: subscription.Customer.ID, PriceID: item.Price.ID,
		SubscriptionID: subscription.ID, Status: string(subscription.Status),
		CancelAtPeriodEnd: subscription.CancelAtPeriodEnd,
		PeriodStart:       &periodStart, PeriodEnd: &periodEnd,
	}
	if subscription.LatestInvoice == nil {
		return object, nil
	}
	invoice := subscription.LatestInvoice
	if !validProviderID(invoice.ID, "in_") || invoice.Livemode != provider.liveMode ||
		invoice.Customer == nil || invoice.Customer.ID != subscription.Customer.ID ||
		!validCurrency(string(invoice.Currency)) || invoice.AmountPaid < 0 {
		return WebhookObjectResource{}, invalidWebhookProviderState(
			"Stripe Subscription Invoice response is invalid",
		)
	}
	object.InvoiceID = invoice.ID
	object.InvoiceStatus = string(invoice.Status)
	object.Amount = invoice.AmountPaid
	object.Currency = string(invoice.Currency)
	if invoice.StatusTransitions != nil && invoice.StatusTransitions.PaidAt > 0 {
		paidAt := time.Unix(invoice.StatusTransitions.PaidAt, 0).UTC()
		object.PaidAt = &paidAt
	}
	payments, err := provider.invoicePayments(ctx, accountID, invoice)
	if err != nil {
		return WebhookObjectResource{}, err
	}
	object.InvoicePayments = payments
	return object, nil
}

func (provider *GatewayProvider) invoicePayments(
	ctx context.Context,
	accountID string,
	invoice *stripeapi.Invoice,
) ([]WebhookInvoicePaymentResource, error) {
	var rows []*stripeapi.InvoicePayment
	if invoice.Payments != nil && !invoice.Payments.HasMore {
		rows = invoice.Payments.Data
	} else {
		params := &stripeapi.InvoicePaymentListParams{Invoice: stripeapi.String(invoice.ID)}
		params.SetStripeAccount(accountID)
		params.Limit = stripeapi.Int64(100)
		params.AddExpand("data.invoice")
		params.AddExpand("data.payment.payment_intent")
		for payment, err := range provider.reconcileClient.V1InvoicePayments.List(ctx, params).All(ctx) {
			if err != nil {
				return nil, fmt.Errorf("Stripe list Invoice Payments: %w", err)
			}
			rows = append(rows, payment)
		}
	}
	result := make([]WebhookInvoicePaymentResource, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, payment := range rows {
		if payment == nil || !validProviderID(payment.ID, "inpay_") ||
			payment.Livemode != provider.liveMode || payment.Invoice == nil ||
			payment.Invoice.ID != invoice.ID || payment.AmountPaid < 0 ||
			payment.AmountRequested < payment.AmountPaid ||
			string(payment.Currency) != string(invoice.Currency) {
			return nil, invalidWebhookProviderState("Stripe Invoice Payment response is invalid")
		}
		switch payment.Status {
		case "open", "canceled":
			continue
		case "paid":
		default:
			return nil, invalidWebhookProviderState("Stripe Invoice Payment status is invalid")
		}
		if payment.Payment == nil {
			return nil, invalidWebhookProviderState("Stripe Invoice Payment source is invalid")
		}
		if payment.Payment.Type != stripeapi.InvoicePaymentPaymentTypePaymentIntent {
			continue
		}
		if payment.Payment.PaymentIntent == nil ||
			!validProviderID(payment.Payment.PaymentIntent.ID, "pi_") || payment.AmountPaid < 1 {
			return nil, invalidWebhookProviderState("Stripe Invoice Payment Intent is invalid")
		}
		if _, duplicate := seen[payment.ID]; duplicate {
			return nil, invalidWebhookProviderState("Stripe Invoice Payment is duplicated")
		}
		seen[payment.ID] = struct{}{}
		var paidAt *time.Time
		if payment.StatusTransitions != nil && payment.StatusTransitions.PaidAt > 0 {
			paid := time.Unix(payment.StatusTransitions.PaidAt, 0).UTC()
			paidAt = &paid
		}
		result = append(result, WebhookInvoicePaymentResource{
			ID:              payment.ID,
			PaymentIntentID: payment.Payment.PaymentIntent.ID,
			Amount:          payment.AmountPaid,
			PaidAt:          paidAt,
		})
	}
	return result, nil
}

func (provider *GatewayProvider) reconcileCharge(
	ctx context.Context,
	accountID string,
	chargeID string,
) (WebhookObjectResource, error) {
	if !validProviderID(chargeID, "ch_") {
		return WebhookObjectResource{}, invalidWebhookProviderState(
			"invalid Stripe Charge ID",
		)
	}
	params := &stripeapi.ChargeRetrieveParams{}
	params.SetStripeAccount(accountID)
	params.AddExpand("customer")
	params.AddExpand("payment_intent")
	charge, err := provider.reconcileClient.V1Charges.Retrieve(ctx, chargeID, params)
	if err != nil {
		return WebhookObjectResource{}, fmt.Errorf("Stripe retrieve Charge: %w", err)
	}
	if charge == nil || charge.ID != chargeID || charge.Livemode != provider.liveMode ||
		charge.Customer == nil || !validProviderID(charge.Customer.ID, "cus_") ||
		charge.PaymentIntent == nil || !validProviderID(charge.PaymentIntent.ID, "pi_") ||
		charge.Amount < 0 || charge.AmountRefunded < 0 || charge.AmountRefunded > charge.Amount ||
		!validCurrency(string(charge.Currency)) {
		return WebhookObjectResource{}, invalidWebhookProviderState(
			"Stripe Charge response is invalid",
		)
	}
	return WebhookObjectResource{
		ChargeID: charge.ID, CustomerID: charge.Customer.ID,
		PaymentIntentID: charge.PaymentIntent.ID, Status: string(charge.Status),
		Amount: charge.Amount, AmountRefunded: charge.AmountRefunded,
		Currency: string(charge.Currency),
	}, nil
}

func (provider *GatewayProvider) reconcileDispute(
	ctx context.Context,
	accountID string,
	disputeID string,
) (WebhookObjectResource, error) {
	if !validProviderID(disputeID, "dp_") {
		return WebhookObjectResource{}, invalidWebhookProviderState(
			"invalid Stripe Dispute ID",
		)
	}
	params := &stripeapi.DisputeRetrieveParams{}
	params.SetStripeAccount(accountID)
	params.AddExpand("payment_intent")
	params.AddExpand("charge")
	dispute, err := provider.reconcileClient.V1Disputes.Retrieve(ctx, disputeID, params)
	if err != nil {
		return WebhookObjectResource{}, fmt.Errorf("Stripe retrieve Dispute: %w", err)
	}
	if dispute == nil || dispute.ID != disputeID || dispute.Livemode != provider.liveMode ||
		dispute.PaymentIntent == nil || !validProviderID(dispute.PaymentIntent.ID, "pi_") ||
		dispute.Charge == nil || !validProviderID(dispute.Charge.ID, "ch_") ||
		dispute.Amount < 0 || !validCurrency(string(dispute.Currency)) {
		return WebhookObjectResource{}, invalidWebhookProviderState(
			"Stripe Dispute response is invalid",
		)
	}
	return WebhookObjectResource{
		DisputeID: dispute.ID, ChargeID: dispute.Charge.ID,
		PaymentIntentID: dispute.PaymentIntent.ID, Status: string(dispute.Status),
		Amount: dispute.Amount, Currency: string(dispute.Currency),
	}, nil
}

func invalidWebhookProviderState(detail string) error {
	return fmt.Errorf("%w: %s", ErrWebhookProviderState, detail)
}

func validWebhookEnvelope(eventID, accountID, eventType, objectID string, created int64) bool {
	return validProviderID(eventID, "evt_") && validAccountID(accountID) &&
		validWebhookType(eventType) && validProviderID(objectID, "") && created > 0
}

func validWebhookType(value string) bool {
	return value != "" && len(value) <= maxWebhookTypeBytes &&
		strings.IndexFunc(value, func(character rune) bool {
			return character != '.' && character != '_' &&
				(character < 'a' || character > 'z')
		}) == -1
}
