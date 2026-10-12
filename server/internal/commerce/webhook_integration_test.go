//go:build integration

package commerce_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/commerce"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestCommerceWebhookIsReplaySafeAndOnlyFullRefundRevokes(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)
	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); err != nil {
		t.Fatalf("CreateAccountSession() error = %v", err)
	}

	accountID, providerAccountID, siteID, offerID, customerID := seedWebhookCommerceRuntime(t, test)
	subscriptionOfferID := dbtest.NewUUID(t)
	dbtest.MustExec(t, test.ctx, test.pool, `
		INSERT INTO commerce_offers (
			id, company_id, site_id, manifest_id, environment, alias, display_name,
			offer_type, plan_group_alias, entitlement_key, amount, currency,
			recurring_interval, automatic_tax, success_path, cancel_path,
			provider_product_id, provider_price_id, status
		)
		SELECT $1, company_id, site_id, manifest_id, environment,
			'pro-monthly', 'Pro', 'subscription', 'pro', 'pro.access', 1200,
			'gbp', 'month', false, '/paid', '/pricing',
			'prod_BoardlyPro123', 'price_BoardlyMonthly123', 'active'
		FROM commerce_offers WHERE id=$2`, subscriptionOfferID, offerID)
	provider := &scriptedWebhookProvider{events: make(map[string]commerce.WebhookEvent)}
	provider.events["checkout"] = commerce.WebhookEvent{
		ID: "evt_BoardlyCheckoutPaid123", AccountID: providerAccountID,
		Type: "checkout.session.completed", ObjectID: "cs_test_BoardlyCheckout123",
		ProviderTime: test.now,
		Object: commerce.WebhookObject{
			CheckoutSessionID: "cs_test_BoardlyCheckout123",
			PaymentIntentID:   "pi_BoardlyPayment123", CustomerID: "cus_BoardlyCustomer123",
			PriceID: "price_BoardlyOnce123", Status: "complete", PaymentStatus: "paid",
			Amount: 1299, Currency: "gbp",
		},
	}
	conflictingCheckout := provider.events["checkout"]
	conflictingCheckout.ObjectID = "cs_test_BoardlyConflict123"
	provider.events["checkout-conflict"] = conflictingCheckout
	provider.events["subscription-checkout"] = commerce.WebhookEvent{
		ID: "evt_BoardlySubscriptionCheckout123", AccountID: providerAccountID,
		Type: "checkout.session.completed", ObjectID: "cs_test_BoardlySubscription123",
		ProviderTime: test.now,
		Object: commerce.WebhookObject{
			CheckoutSessionID: "cs_test_BoardlySubscription123",
			SubscriptionID:    "sub_BoardlySubscription123",
			CustomerID:        "cus_BoardlyCustomer123",
			PriceID:           "price_BoardlyMonthly123",
			Status:            "complete", PaymentStatus: "paid",
			Amount: 1200, Currency: "gbp",
		},
	}
	periodStart := test.now
	periodEnd := test.now.AddDate(0, 1, 0)
	provider.events["disabled-subscription-renewal"] = commerce.WebhookEvent{
		ID: "evt_BoardlySubscriptionRenewal123", AccountID: providerAccountID,
		Type: "invoice.paid", ObjectID: "in_BoardlyRenewal123",
		ProviderTime: test.now.Add(30 * time.Second),
		Object: commerce.WebhookObject{
			CustomerID: "cus_BoardlyCustomer123",
			PriceID:    "price_BoardlyMonthly123", SubscriptionID: "sub_BoardlySubscription123",
			InvoiceID: "in_BoardlyRenewal123", Status: "active", InvoiceStatus: "paid",
			Amount: 1200, Currency: "gbp", PeriodStart: &periodStart, PeriodEnd: &periodEnd,
			PaidAt: &periodStart,
			InvoicePayments: []commerce.WebhookInvoicePayment{
				{
					ID: "inpay_BoardlyRenewal123", PaymentIntentID: "pi_BoardlyRenewal123",
					Amount: 700, PaidAt: &periodStart,
				},
				{
					ID:              "inpay_BoardlyRenewalPartial123",
					PaymentIntentID: "pi_BoardlyRenewalPartial123",
					Amount:          500, PaidAt: &periodStart,
				},
			},
		},
	}
	nextPeriodStart := periodEnd
	nextPeriodEnd := nextPeriodStart.AddDate(0, 1, 0)
	provider.events["newer-subscription-renewal"] = commerce.WebhookEvent{
		ID: "evt_BoardlySubscriptionRenewal456", AccountID: providerAccountID,
		Type: "invoice.paid", ObjectID: "in_BoardlyRenewal456",
		ProviderTime: test.now.Add(2 * time.Minute),
		Object: commerce.WebhookObject{
			CustomerID: "cus_BoardlyCustomer123",
			PriceID:    "price_BoardlyMonthly123", SubscriptionID: "sub_BoardlySubscription123",
			InvoiceID: "in_BoardlyRenewal456", Status: "active", InvoiceStatus: "paid",
			Amount: 1200, Currency: "gbp", PeriodStart: &nextPeriodStart, PeriodEnd: &nextPeriodEnd,
			PaidAt: &nextPeriodStart,
			InvoicePayments: []commerce.WebhookInvoicePayment{{
				ID: "inpay_BoardlyRenewal456", PaymentIntentID: "pi_BoardlyRenewal456",
				Amount: 1200, PaidAt: &nextPeriodStart,
			}},
		},
	}
	provider.events["first-subscription-allocation-refund"] = commerce.WebhookEvent{
		ID: "evt_BoardlySubscriptionAllocationRefund123", AccountID: providerAccountID,
		Type: "charge.refunded", ObjectID: "ch_BoardlySubscriptionAllocation123",
		ProviderTime: test.now.Add(40 * time.Second),
		Object: commerce.WebhookObject{
			PaymentIntentID: "pi_BoardlyRenewal123", CustomerID: "cus_BoardlyCustomer123",
			ChargeID: "ch_BoardlySubscriptionAllocation123", Status: "succeeded",
			Amount: 1200, AmountRefunded: 1200, Currency: "gbp",
		},
	}
	provider.events["last-subscription-allocation-dispute"] = commerce.WebhookEvent{
		ID: "evt_BoardlySubscriptionAllocationDispute123", AccountID: providerAccountID,
		Type: "charge.dispute.closed", ObjectID: "dp_BoardlySubscriptionAllocation123",
		ProviderTime: test.now.Add(50 * time.Second),
		Object: commerce.WebhookObject{
			PaymentIntentID: "pi_BoardlyRenewalPartial123",
			Status:          "lost", Amount: 500, Currency: "gbp",
		},
	}
	replayedSubscriptionRenewal := provider.events["disabled-subscription-renewal"]
	replayedSubscriptionRenewal.ID = "evt_BoardlySubscriptionRenewalReplay123"
	replayedSubscriptionRenewal.ProviderTime = test.now.Add(time.Minute)
	provider.events["replayed-subscription-renewal"] = replayedSubscriptionRenewal
	provider.events["older-subscription-invoice-refund"] = commerce.WebhookEvent{
		ID: "evt_BoardlyOlderSubscriptionRefund123", AccountID: providerAccountID,
		Type: "charge.refunded", ObjectID: "ch_BoardlySubscriptionRenewal123",
		ProviderTime: test.now.Add(3 * time.Minute),
		Object: commerce.WebhookObject{
			PaymentIntentID: "pi_BoardlyRenewal123", CustomerID: "cus_BoardlyCustomer123",
			ChargeID: "ch_BoardlySubscriptionRenewal123", Status: "succeeded",
			Amount: 1200, AmountRefunded: 1200, Currency: "gbp",
		},
	}
	provider.events["subscription-canceled"] = commerce.WebhookEvent{
		ID: "evt_BoardlySubscriptionCanceled123", AccountID: providerAccountID,
		Type: "customer.subscription.deleted", ObjectID: "sub_BoardlySubscription123",
		ProviderTime: test.now.Add(5 * time.Minute),
		Object: commerce.WebhookObject{
			CustomerID: "cus_BoardlyCustomer123", PriceID: "price_BoardlyMonthly123",
			SubscriptionID: "sub_BoardlySubscription123", Status: "canceled",
			InvoiceStatus: "paid", PeriodStart: &periodStart, PeriodEnd: &periodEnd,
		},
	}
	provider.events["older-subscription-paid"] = commerce.WebhookEvent{
		ID: "evt_BoardlyOlderSubscriptionPaid123", AccountID: providerAccountID,
		Type: "invoice.paid", ObjectID: "in_BoardlyOlder123",
		ProviderTime: test.now.Add(4 * time.Minute),
		Object: commerce.WebhookObject{
			CustomerID: "cus_BoardlyCustomer123",
			PriceID:    "price_BoardlyMonthly123", SubscriptionID: "sub_BoardlySubscription123",
			InvoiceID: "in_BoardlyOlder123", Status: "active", InvoiceStatus: "paid",
			Amount: 1200, Currency: "gbp", PeriodStart: &periodStart, PeriodEnd: &periodEnd,
			PaidAt: &periodStart,
			InvoicePayments: []commerce.WebhookInvoicePayment{{
				ID: "inpay_BoardlyOlder123", PaymentIntentID: "pi_BoardlyRenewal456",
				Amount: 1200, PaidAt: &periodStart,
			}},
		},
	}
	provider.events["partial-refund"] = commerce.WebhookEvent{
		ID: "evt_BoardlyPartialRefund123", AccountID: providerAccountID,
		Type: "charge.refunded", ObjectID: "ch_BoardlyCharge123",
		ProviderTime: test.now.Add(time.Minute),
		Object: commerce.WebhookObject{
			PaymentIntentID: "pi_BoardlyPayment123", CustomerID: "cus_BoardlyCustomer123",
			ChargeID: "ch_BoardlyCharge123", Status: "succeeded",
			Amount: 1299, AmountRefunded: 400, Currency: "gbp",
		},
	}
	provider.events["full-refund"] = commerce.WebhookEvent{
		ID: "evt_BoardlyFullRefund123", AccountID: providerAccountID,
		Type: "charge.refunded", ObjectID: "ch_BoardlyCharge123",
		ProviderTime: test.now.Add(2 * time.Minute),
		Object: commerce.WebhookObject{
			PaymentIntentID: "pi_BoardlyPayment123", CustomerID: "cus_BoardlyCustomer123",
			ChargeID: "ch_BoardlyCharge123", Status: "succeeded",
			Amount: 1299, AmountRefunded: 1299, Currency: "gbp",
		},
	}
	lateCheckout := provider.events["checkout"]
	lateCheckout.ID = "evt_BoardlyCheckoutReplayLater123"
	provider.events["checkout-after-refund"] = lateCheckout
	repurchase := provider.events["checkout"]
	repurchase.ID = "evt_BoardlyRepurchase123"
	repurchase.ObjectID = "cs_test_BoardlyRepurchase123"
	repurchase.ProviderTime = test.now.Add(3 * time.Minute)
	repurchase.Object.CheckoutSessionID = "cs_test_BoardlyRepurchase123"
	repurchase.Object.PaymentIntentID = "pi_BoardlyRepurchase123"
	provider.events["repurchase"] = repurchase
	lateOriginalRefund := provider.events["full-refund"]
	lateOriginalRefund.ID = "evt_BoardlyOriginalRefundAfterRepurchase123"
	lateOriginalRefund.ProviderTime = test.now.Add(4 * time.Minute)
	provider.events["original-refund-after-repurchase"] = lateOriginalRefund
	service := newWebhookCommerceService(t, test, provider)

	first, err := service.ProcessWebhook(test.ctx, []byte("checkout"), "stripe-signature")
	if err != nil || !first.Processed || first.Duplicate {
		t.Fatalf("ProcessWebhook(first) = %#v, %v", first, err)
	}
	replay, err := service.ProcessWebhook(test.ctx, []byte("checkout"), "stripe-signature")
	if err != nil || !replay.Processed || !replay.Duplicate {
		t.Fatalf("ProcessWebhook(replay) = %#v, %v", replay, err)
	}
	if _, err := service.ProcessWebhook(
		test.ctx,
		[]byte("checkout-conflict"),
		"stripe-signature",
	); !errors.Is(err, commerce.ErrWebhookConflict) {
		t.Fatalf("ProcessWebhook(conflicting payload) error = %v, want conflict", err)
	}
	if _, err := service.ProcessWebhook(
		test.ctx,
		[]byte("subscription-checkout"),
		"stripe-signature",
	); err != nil {
		t.Fatalf("ProcessWebhook(subscription Checkout) error = %v", err)
	}
	var subscriptionEntitlements int
	if err := test.pool.QueryRow(test.ctx, `
		SELECT count(*) FROM commerce_entitlements
		WHERE site_id=$1 AND customer_id=$2 AND entitlement_key='pro.access'`,
		siteID, customerID).Scan(&subscriptionEntitlements); err != nil {
		t.Fatal(err)
	}
	if subscriptionEntitlements != 0 {
		t.Fatalf(
			"subscription Checkout granted %d entitlements before a paid Invoice",
			subscriptionEntitlements,
		)
	}
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE commerce_offers SET status='disabled', version=version+1 WHERE id=$1`,
		subscriptionOfferID)
	if _, err := service.ProcessWebhook(
		test.ctx,
		[]byte("disabled-subscription-renewal"),
		"stripe-signature",
	); err != nil {
		t.Fatalf("ProcessWebhook(disabled Offer renewal) error = %v", err)
	}
	var renewedStatus string
	var renewedOfferID uuid.UUID
	if err := test.pool.QueryRow(test.ctx, `
		SELECT status, offer_id FROM commerce_entitlements
		WHERE site_id=$1 AND customer_id=$2 AND entitlement_key='pro.access'`,
		siteID, customerID).Scan(&renewedStatus, &renewedOfferID); err != nil {
		t.Fatal(err)
	}
	if renewedStatus != "active" || renewedOfferID != subscriptionOfferID {
		t.Fatalf("renewed entitlement = status:%q offer:%s", renewedStatus, renewedOfferID)
	}
	var partialPaymentCount int
	if err := test.pool.QueryRow(test.ctx, `
		SELECT count(*) FROM commerce_payments
		WHERE provider_payment_intent_id IN (
			'pi_BoardlyRenewal123', 'pi_BoardlyRenewalPartial123'
		)`).Scan(&partialPaymentCount); err != nil {
		t.Fatal(err)
	}
	if partialPaymentCount != 2 {
		t.Fatalf("partial Invoice payment projections = %d, want 2", partialPaymentCount)
	}
	if _, err := service.ProcessWebhook(
		test.ctx,
		[]byte("first-subscription-allocation-refund"),
		"stripe-signature",
	); err != nil {
		t.Fatalf("ProcessWebhook(first subscription allocation refund) error = %v", err)
	}
	if err := test.pool.QueryRow(test.ctx, `
		SELECT status FROM commerce_entitlements
		WHERE site_id=$1 AND customer_id=$2 AND entitlement_key='pro.access'`,
		siteID, customerID).Scan(&renewedStatus); err != nil {
		t.Fatal(err)
	}
	if renewedStatus != "active" {
		t.Fatalf("first subscription allocation refund denied entitlement: %q", renewedStatus)
	}
	if _, err := service.ProcessWebhook(
		test.ctx,
		[]byte("last-subscription-allocation-dispute"),
		"stripe-signature",
	); err != nil {
		t.Fatalf("ProcessWebhook(last subscription allocation dispute) error = %v", err)
	}
	var revokedReason *string
	if err := test.pool.QueryRow(test.ctx, `
		SELECT status, revoked_reason FROM commerce_entitlements
		WHERE site_id=$1 AND customer_id=$2 AND entitlement_key='pro.access'`,
		siteID, customerID).Scan(&renewedStatus, &revokedReason); err != nil {
		t.Fatal(err)
	}
	if renewedStatus != "denied" || revokedReason == nil || *revokedReason != "dispute_lost" {
		t.Fatalf(
			"all terminal subscription allocations = status:%q reason:%v",
			renewedStatus,
			revokedReason,
		)
	}
	if _, err := service.ProcessWebhook(
		test.ctx,
		[]byte("replayed-subscription-renewal"),
		"stripe-signature",
	); err != nil {
		t.Fatalf("ProcessWebhook(replayed subscription renewal) error = %v", err)
	}
	if err := test.pool.QueryRow(test.ctx, `
		SELECT status FROM commerce_entitlements
		WHERE site_id=$1 AND customer_id=$2 AND entitlement_key='pro.access'`,
		siteID, customerID).Scan(&renewedStatus); err != nil {
		t.Fatal(err)
	}
	if renewedStatus != "denied" {
		t.Fatalf("replayed terminal Invoice restored entitlement: %q", renewedStatus)
	}
	if _, err := service.ProcessWebhook(
		test.ctx,
		[]byte("newer-subscription-renewal"),
		"stripe-signature",
	); err != nil {
		t.Fatalf("ProcessWebhook(newer subscription renewal) error = %v", err)
	}
	if _, err := service.ProcessWebhook(
		test.ctx,
		[]byte("older-subscription-invoice-refund"),
		"stripe-signature",
	); err != nil {
		t.Fatalf("ProcessWebhook(older subscription refund) error = %v", err)
	}
	var allocatedPaymentStatus string
	if err := test.pool.QueryRow(test.ctx, `
		SELECT status FROM commerce_payments
		WHERE provider_payment_intent_id='pi_BoardlyRenewal123'`,
	).Scan(&allocatedPaymentStatus); err != nil {
		t.Fatalf("read partially allocated subscription payment: %v", err)
	}
	if allocatedPaymentStatus != "refunded" {
		t.Fatalf("partially allocated subscription payment status = %q", allocatedPaymentStatus)
	}
	var activeSubscriptionPayment string
	if err := test.pool.QueryRow(test.ctx, `
		SELECT payments.provider_payment_intent_id
		FROM commerce_entitlements AS entitlements
		JOIN commerce_payments AS payments ON payments.id=entitlements.payment_id
		WHERE entitlements.site_id=$1 AND entitlements.customer_id=$2
		  AND entitlements.entitlement_key='pro.access'
		  AND entitlements.status='active'`, siteID, customerID).Scan(
		&activeSubscriptionPayment,
	); err != nil {
		t.Fatalf("read subscription entitlement after old refund: %v", err)
	}
	if activeSubscriptionPayment != "pi_BoardlyRenewal456" {
		t.Fatalf(
			"active subscription payment = %q, want latest renewal",
			activeSubscriptionPayment,
		)
	}
	if _, err := service.ProcessWebhook(
		test.ctx,
		[]byte("subscription-canceled"),
		"stripe-signature",
	); err != nil {
		t.Fatalf("ProcessWebhook(subscription canceled) error = %v", err)
	}
	if _, err := service.ProcessWebhook(
		test.ctx,
		[]byte("older-subscription-paid"),
		"stripe-signature",
	); err != nil {
		t.Fatalf("ProcessWebhook(older subscription paid) error = %v", err)
	}
	var reusedPaymentIntentAllocations int
	if err := test.pool.QueryRow(test.ctx, `
		SELECT count(*) FROM commerce_payments
		WHERE provider_payment_intent_id='pi_BoardlyRenewal456'
		  AND provider_invoice_id IN ('in_BoardlyRenewal456', 'in_BoardlyOlder123')`,
	).Scan(&reusedPaymentIntentAllocations); err != nil {
		t.Fatal(err)
	}
	if reusedPaymentIntentAllocations != 2 {
		t.Fatalf(
			"same PaymentIntent Invoice allocations = %d, want 2",
			reusedPaymentIntentAllocations,
		)
	}
	var projectedSubscriptionStatus string
	if err := test.pool.QueryRow(test.ctx, `
		SELECT entitlements.status, subscriptions.status
		FROM commerce_entitlements AS entitlements
		JOIN commerce_subscriptions AS subscriptions
		  ON subscriptions.id=entitlements.subscription_id
		WHERE entitlements.site_id=$1 AND entitlements.customer_id=$2
		  AND entitlements.entitlement_key='pro.access'`,
		siteID, customerID).Scan(&renewedStatus, &projectedSubscriptionStatus); err != nil {
		t.Fatal(err)
	}
	if renewedStatus != "denied" || projectedSubscriptionStatus != "canceled" {
		t.Fatalf(
			"out-of-order paid Invoice restored canceled state: entitlement=%q subscription=%q",
			renewedStatus,
			projectedSubscriptionStatus,
		)
	}

	assertWebhookProjection(t, test, accountID, siteID, offerID, customerID, "paid", "active", "")
	if _, err := service.ProcessWebhook(
		test.ctx,
		[]byte("partial-refund"),
		"stripe-signature",
	); err != nil {
		t.Fatalf("ProcessWebhook(partial refund) error = %v", err)
	}
	assertWebhookProjection(t, test, accountID, siteID, offerID, customerID, "paid", "active", "")

	if _, err := service.ProcessWebhook(
		test.ctx,
		[]byte("full-refund"),
		"stripe-signature",
	); err != nil {
		t.Fatalf("ProcessWebhook(full refund) error = %v", err)
	}
	assertWebhookProjection(
		t,
		test,
		accountID,
		siteID,
		offerID,
		customerID,
		"refunded",
		"denied",
		"refunded",
	)
	var refundedAt time.Time
	if err := test.pool.QueryRow(test.ctx, `
		SELECT refunded_at
		FROM commerce_payments
		WHERE environment='sandbox'
		  AND provider_payment_intent_id='pi_BoardlyPayment123'`).Scan(&refundedAt); err != nil {
		t.Fatalf("read refund occurrence time: %v", err)
	}
	if want := provider.events["full-refund"].ProviderTime; !refundedAt.Equal(want) {
		t.Fatalf("refunded_at = %s, want provider event time %s", refundedAt, want)
	}
	if _, err := service.ProcessWebhook(
		test.ctx,
		[]byte("checkout-after-refund"),
		"stripe-signature",
	); err != nil {
		t.Fatalf("ProcessWebhook(late Checkout replay) error = %v", err)
	}
	assertWebhookProjection(
		t,
		test,
		accountID,
		siteID,
		offerID,
		customerID,
		"refunded",
		"denied",
		"refunded",
	)
	if _, err := service.ProcessWebhook(
		test.ctx,
		[]byte("repurchase"),
		"stripe-signature",
	); err != nil {
		t.Fatalf("ProcessWebhook(repurchase) error = %v", err)
	}
	var repurchasePaymentID string
	var repurchasePaymentStatus string
	var repurchaseEntitlementStatus string
	if err := test.pool.QueryRow(test.ctx, `
		SELECT payments.provider_payment_intent_id, payments.status, entitlements.status
		FROM commerce_entitlements AS entitlements
		JOIN commerce_payments AS payments ON payments.id=entitlements.payment_id
		WHERE entitlements.site_id=$1 AND entitlements.customer_id=$2
		  AND entitlements.entitlement_key='report.access'
		  AND entitlements.status='active'`, siteID, customerID).Scan(
		&repurchasePaymentID,
		&repurchasePaymentStatus,
		&repurchaseEntitlementStatus,
	); err != nil {
		t.Fatalf("read repurchase projection: %v", err)
	}
	if repurchasePaymentID != "pi_BoardlyRepurchase123" ||
		repurchasePaymentStatus != "paid" || repurchaseEntitlementStatus != "active" {
		t.Fatalf(
			"repurchase projection = payment:%q status:%q entitlement:%q",
			repurchasePaymentID,
			repurchasePaymentStatus,
			repurchaseEntitlementStatus,
		)
	}
	if _, err := service.ProcessWebhook(
		test.ctx,
		[]byte("original-refund-after-repurchase"),
		"stripe-signature",
	); err != nil {
		t.Fatalf("ProcessWebhook(original refund after repurchase) error = %v", err)
	}
	var activePaymentID string
	if err := test.pool.QueryRow(test.ctx, `
		SELECT payments.provider_payment_intent_id
		FROM commerce_entitlements AS entitlements
		JOIN commerce_payments AS payments ON payments.id=entitlements.payment_id
		WHERE entitlements.site_id=$1 AND entitlements.customer_id=$2
		  AND entitlements.entitlement_key='report.access'
		  AND entitlements.status='active'`, siteID, customerID).Scan(&activePaymentID); err != nil {
		t.Fatalf("read effective entitlement after old refund: %v", err)
	}
	if activePaymentID != "pi_BoardlyRepurchase123" {
		t.Fatalf("active entitlement payment = %q, want repurchase", activePaymentID)
	}
	if got := provider.reconcileCalls(); got != 15 {
		t.Fatalf("Stripe reconciliation calls = %d, want 15", got)
	}
}

func TestCommerceWebhookMovesSubscriptionEntitlementAcrossOffers(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)
	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); err != nil {
		t.Fatalf("CreateAccountSession() error = %v", err)
	}
	_, providerAccountID, siteID, baseOfferID, customerID := seedWebhookCommerceRuntime(t, test)
	offerIDs := []uuid.UUID{dbtest.NewUUID(t), dbtest.NewUUID(t)}
	for index, offer := range []struct {
		alias           string
		name            string
		planGroup       string
		entitlement     string
		providerPrice   string
		providerProduct string
	}{
		{
			alias: "starter-month", name: "Starter", planGroup: "starter",
			entitlement: "reports.starter", providerPrice: "price_BoardlyStarter123",
			providerProduct: "prod_BoardlyStarter123",
		},
		{
			alias: "pro-month", name: "Pro", planGroup: "pro",
			entitlement: "reports.pro", providerPrice: "price_BoardlyPro123",
			providerProduct: "prod_BoardlyPro123",
		},
	} {
		dbtest.MustExec(t, test.ctx, test.pool, `
			INSERT INTO commerce_offers (
				id, company_id, site_id, manifest_id, environment, alias,
				display_name, offer_type, plan_group_alias, entitlement_key,
				amount, currency, recurring_interval, automatic_tax,
				success_path, cancel_path, provider_product_id,
				provider_price_id, status
			)
			SELECT $1, company_id, site_id, manifest_id, environment, $2,
				$3, 'subscription', $4, $5, 1200, 'gbp', 'month', false,
				'/paid', '/pricing', $6, $7, 'active'
			FROM commerce_offers WHERE id=$8`,
			offerIDs[index], offer.alias, offer.name, offer.planGroup,
			offer.entitlement, offer.providerProduct, offer.providerPrice, baseOfferID)
	}
	periodStart := test.now
	periodEnd := periodStart.AddDate(0, 1, 0)
	provider := &scriptedWebhookProvider{events: map[string]commerce.WebhookEvent{}}
	for index, event := range []struct {
		payload       string
		eventID       string
		invoiceID     string
		paymentIntent string
		priceID       string
	}{
		{
			payload: "starter", eventID: "evt_BoardlyStarterInvoice123",
			invoiceID: "in_BoardlyStarter123", paymentIntent: "pi_BoardlyStarter123",
			priceID: "price_BoardlyStarter123",
		},
		{
			payload: "pro", eventID: "evt_BoardlyProInvoice123",
			invoiceID: "in_BoardlyPro123", paymentIntent: "pi_BoardlyPro123",
			priceID: "price_BoardlyPro123",
		},
	} {
		provider.events[event.payload] = commerce.WebhookEvent{
			ID: event.eventID, AccountID: providerAccountID,
			Type: "invoice.paid", ObjectID: event.invoiceID,
			ProviderTime: test.now.Add(time.Duration(index) * time.Minute),
			Object: commerce.WebhookObject{
				CustomerID:     "cus_BoardlyCustomer123",
				PriceID:        event.priceID,
				SubscriptionID: "sub_BoardlyPlanChange123",
				InvoiceID:      event.invoiceID,
				Status:         "active",
				InvoiceStatus:  "paid",
				Amount:         1200,
				Currency:       "gbp",
				PeriodStart:    &periodStart,
				PeriodEnd:      &periodEnd,
				PaidAt:         &periodStart,
				InvoicePayments: []commerce.WebhookInvoicePayment{{
					ID:              "inpay_" + event.invoiceID[3:],
					PaymentIntentID: event.paymentIntent, Amount: 1200, PaidAt: &periodStart,
				}},
			},
		}
	}
	service := newWebhookCommerceService(t, test, provider)
	for _, payload := range []string{"starter", "pro"} {
		if _, err := service.ProcessWebhook(
			test.ctx,
			[]byte(payload),
			"stripe-signature",
		); err != nil {
			t.Fatalf("ProcessWebhook(%s plan) error = %v", payload, err)
		}
	}

	var projectedOfferID uuid.UUID
	if err := test.pool.QueryRow(test.ctx, `
		SELECT offer_id FROM commerce_subscriptions
		WHERE environment='sandbox' AND provider_subscription_id=$1`,
		"sub_BoardlyPlanChange123",
	).Scan(&projectedOfferID); err != nil {
		t.Fatalf("read changed Commerce subscription: %v", err)
	}
	if projectedOfferID != offerIDs[1] {
		t.Fatalf("changed subscription Offer = %s, want %s", projectedOfferID, offerIDs[1])
	}
	rows, err := test.pool.Query(test.ctx, `
		SELECT entitlement_key, status, COALESCE(revoked_reason, '')
		FROM commerce_entitlements
		WHERE site_id=$1 AND customer_id=$2 AND source_type='subscription'
		ORDER BY entitlement_key`, siteID, customerID)
	if err != nil {
		t.Fatalf("read plan-change entitlements: %v", err)
	}
	defer rows.Close()
	want := [][3]string{
		{"reports.pro", "active", ""},
		{"reports.starter", "denied", "plan_changed"},
	}
	var got [][3]string
	for rows.Next() {
		var row [3]string
		if err := rows.Scan(&row[0], &row[1], &row[2]); err != nil {
			t.Fatalf("scan plan-change entitlement: %v", err)
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan-change entitlements: %v", err)
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("plan-change entitlements = %#v, want %#v", got, want)
	}
}

func TestCommerceWebhookSerializesCrossTypeProviderFetchForEqualTimestampUpdates(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)
	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); err != nil {
		t.Fatalf("CreateAccountSession() error = %v", err)
	}
	_, providerAccountID, siteID, baseOfferID, _ := seedWebhookCommerceRuntime(t, test)
	subscriptionOfferID := dbtest.NewUUID(t)
	dbtest.MustExec(t, test.ctx, test.pool, `
		INSERT INTO commerce_offers (
			id, company_id, site_id, manifest_id, environment, alias,
			display_name, offer_type, plan_group_alias, entitlement_key,
			amount, currency, recurring_interval, automatic_tax,
			success_path, cancel_path, provider_product_id,
			provider_price_id, status
		)
		SELECT $1, company_id, site_id, manifest_id, environment,
			'serialized-month', 'Serialized monthly', 'subscription',
			'serialized', 'reports.serialized', 1200, 'gbp', 'month', false,
			'/paid', '/pricing', 'prod_BoardlySerialized123',
			'price_BoardlySerialized123', 'active'
		FROM commerce_offers WHERE id=$2`, subscriptionOfferID, baseOfferID)

	periodStart := test.now
	periodEnd := periodStart.AddDate(0, 1, 0)
	active := commerce.WebhookEvent{
		ID: "evt_BoardlyEqualActive123", AccountID: providerAccountID,
		Type: "invoice.paid", ObjectID: "in_BoardlyEqualSecond123",
		ProviderTime: test.now,
		Object: commerce.WebhookObject{
			CustomerID: "cus_BoardlyCustomer123", PriceID: "price_BoardlySerialized123",
			SubscriptionID: "sub_BoardlyEqualSecond123",
			InvoiceID:      "in_BoardlyEqualSecond123", Status: "active",
			InvoiceStatus: "paid", PeriodStart: &periodStart, PeriodEnd: &periodEnd,
		},
	}
	canceled := active
	canceled.ID = "evt_BoardlyEqualCanceled123"
	canceled.Type = "customer.subscription.updated"
	canceled.ObjectID = active.Object.SubscriptionID
	canceled.Object.Status = "canceled"
	canceled.Object.InvoiceStatus = ""
	provider := newSerializedWebhookProvider(active, canceled)
	service := newWebhookCommerceService(t, test, provider)
	t.Cleanup(func() { provider.releaseActiveOnce.Do(func() { close(provider.releaseActive) }) })

	activeResult := make(chan error, 1)
	go func() {
		_, err := service.ProcessWebhook(test.ctx, []byte("active"), "stripe-signature")
		activeResult <- err
	}()
	select {
	case <-provider.activeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("active webhook did not reach provider reconciliation")
	}
	var activeLease bool
	if err := test.pool.QueryRow(test.ctx, `
		SELECT lease_expires_at > clock_timestamp()
		FROM commerce_webhook_account_leases
		WHERE company_id=$1 AND environment='sandbox' AND provider_account_id=$2`,
		test.ownerA.CompanyID, providerAccountID).Scan(&activeLease); err != nil {
		t.Fatalf("read durable Commerce webhook account lease: %v", err)
	}
	if !activeLease {
		t.Fatal("Commerce webhook provider fetch has no active account lease")
	}
	connection, err := test.pool.Acquire(test.ctx)
	if err != nil {
		t.Fatalf("acquire advisory-lock probe connection: %v", err)
	}
	var advisoryAvailable bool
	lockKey := "boardly/commerce-webhook-account/v1/sandbox/" + providerAccountID
	if err := connection.QueryRow(
		test.ctx,
		"SELECT pg_try_advisory_lock(hashtextextended($1, 0))",
		lockKey,
	).Scan(&advisoryAvailable); err != nil {
		connection.Release()
		t.Fatalf("probe Commerce webhook advisory lock: %v", err)
	}
	if advisoryAvailable {
		var unlocked bool
		if err := connection.QueryRow(
			test.ctx,
			"SELECT pg_advisory_unlock(hashtextextended($1, 0))",
			lockKey,
		).Scan(&unlocked); err != nil || !unlocked {
			connection.Release()
			t.Fatalf("release Commerce webhook advisory probe = %t, %v", unlocked, err)
		}
	}
	connection.Release()
	if !advisoryAvailable {
		t.Fatal("Stripe provider fetch held a PostgreSQL transaction advisory lock")
	}

	canceledResult := make(chan error, 1)
	go func() {
		_, err := service.ProcessWebhook(test.ctx, []byte("canceled"), "stripe-signature")
		canceledResult <- err
	}()
	select {
	case err := <-canceledResult:
		if !errors.Is(err, commerce.ErrWebhookBusy) {
			t.Fatalf("ProcessWebhook(contending canceled) error = %v, want busy", err)
		}
	case <-time.After(time.Second):
		t.Fatal("contending webhook blocked on the account lock")
	}
	select {
	case <-provider.canceledStarted:
		t.Fatal("contending webhook reached the provider before acquiring the account lock")
	default:
	}
	var receiptState, receiptError string
	if err := test.pool.QueryRow(test.ctx, `
		SELECT state, COALESCE(last_error_code, '')
		FROM commerce_webhook_receipts
		WHERE environment='sandbox' AND provider_event_id=$1`, canceled.ID).Scan(
		&receiptState,
		&receiptError,
	); err != nil {
		t.Fatalf("read contending Commerce webhook receipt: %v", err)
	}
	if receiptState != "failed" || receiptError != "account_busy" {
		t.Fatalf(
			"contending webhook receipt = %q/%q, want failed/account_busy",
			receiptState,
			receiptError,
		)
	}

	provider.releaseActiveOnce.Do(func() { close(provider.releaseActive) })
	select {
	case err := <-activeResult:
		if err != nil {
			t.Fatalf("ProcessWebhook(active) error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active webhook did not finish")
	}
	var accountLeaseCount int
	if err := test.pool.QueryRow(test.ctx, `
		SELECT count(*)
		FROM commerce_webhook_account_leases
		WHERE company_id=$1 AND environment='sandbox' AND provider_account_id=$2`,
		test.ownerA.CompanyID, providerAccountID).Scan(&accountLeaseCount); err != nil {
		t.Fatalf("count released Commerce webhook account lease: %v", err)
	}
	if accountLeaseCount != 0 {
		t.Fatalf("released Commerce webhook account lease count = %d, want 0", accountLeaseCount)
	}
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE commerce_webhook_receipts
		SET next_attempt_at=clock_timestamp()
		WHERE environment='sandbox' AND provider_event_id=$1`, canceled.ID)
	reconciled, err := service.ReconcileWebhookReceipts(test.ctx, 1)
	if err != nil || reconciled.Processed != 1 || reconciled.DeadLettered != 0 {
		t.Fatalf("ReconcileWebhookReceipts() = %+v, %v", reconciled, err)
	}
	select {
	case <-provider.canceledStarted:
	default:
		t.Fatal("rescheduled webhook did not reach provider reconciliation")
	}

	var subscriptionStatus, entitlementStatus, revokedReason string
	if err := test.pool.QueryRow(test.ctx, `
		SELECT subscriptions.status, entitlements.status,
			COALESCE(entitlements.revoked_reason, '')
		FROM commerce_subscriptions AS subscriptions
		JOIN commerce_entitlements AS entitlements
		  ON entitlements.subscription_id=subscriptions.id
		WHERE subscriptions.site_id=$1
		  AND subscriptions.provider_subscription_id=$2`,
		siteID, active.Object.SubscriptionID).Scan(
		&subscriptionStatus,
		&entitlementStatus,
		&revokedReason,
	); err != nil {
		t.Fatalf("read equal-timestamp subscription projection: %v", err)
	}
	if subscriptionStatus != "canceled" || entitlementStatus != "denied" ||
		revokedReason != "subscription_ended" {
		t.Fatalf(
			"equal-timestamp projection = subscription:%q entitlement:%q reason:%q, "+
				"want canceled/denied/subscription_ended",
			subscriptionStatus,
			entitlementStatus,
			revokedReason,
		)
	}
}

func TestCommercePoisonWebhookDeadLettersWithoutBlockingNewEvents(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)
	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); err != nil {
		t.Fatalf("CreateAccountSession() error = %v", err)
	}
	_, providerAccountID, _, _, _ := seedWebhookCommerceRuntime(t, test)
	provider := &scriptedWebhookProvider{
		events:   make(map[string]commerce.WebhookEvent),
		failures: make(map[string]int),
	}
	poison := checkoutWebhookEvent(
		"evt_BoardlyPoison123",
		providerAccountID,
		"cs_test_BoardlyPoison123",
		test.now,
	)
	poison.Object.CustomerID = ""
	provider.events["poison"] = poison
	provider.events["valid"] = checkoutWebhookEvent(
		"evt_BoardlyValidAfterPoison123",
		providerAccountID,
		"cs_test_BoardlyValidAfterPoison123",
		test.now.Add(time.Second),
	)
	service := newWebhookCommerceService(t, test, provider)

	poisonResult, err := service.ProcessWebhook(
		test.ctx,
		[]byte("poison"),
		"stripe-signature",
	)
	if err != nil || poisonResult.Processed || poisonResult.Duplicate {
		t.Fatalf("ProcessWebhook(poison) = %#v, %v", poisonResult, err)
	}
	var poisonState string
	if err := test.pool.QueryRow(test.ctx, `
		SELECT state FROM commerce_webhook_receipts
		WHERE environment='sandbox' AND provider_event_id=$1`, poison.ID).Scan(
		&poisonState,
	); err != nil {
		t.Fatal(err)
	}
	if poisonState != "dead_letter" {
		t.Fatalf("poison receipt state = %q, want dead_letter", poisonState)
	}
	duplicate, err := service.ProcessWebhook(
		test.ctx,
		[]byte("poison"),
		"stripe-signature",
	)
	if err != nil || duplicate.Processed || !duplicate.Duplicate {
		t.Fatalf("ProcessWebhook(dead-letter replay) = %#v, %v", duplicate, err)
	}
	if calls := provider.reconcileCalls(); calls != 1 {
		t.Fatalf("dead-letter replay provider calls = %d, want 1 total", calls)
	}

	valid, err := service.ProcessWebhook(test.ctx, []byte("valid"), "stripe-signature")
	if err != nil || !valid.Processed {
		t.Fatalf("ProcessWebhook(valid after poison) = %#v, %v", valid, err)
	}
	if calls := provider.reconcileCalls(); calls != 2 {
		t.Fatalf("provider calls after valid webhook = %d, want 2", calls)
	}
}

func TestCommerceWebhookRetryWaitsUntilNextAttempt(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)
	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); err != nil {
		t.Fatalf("CreateAccountSession() error = %v", err)
	}
	_, providerAccountID, _, _, _ := seedWebhookCommerceRuntime(t, test)
	event := checkoutWebhookEvent(
		"evt_BoardlyRetry123",
		providerAccountID,
		"cs_test_BoardlyRetry123",
		test.now,
	)
	provider := &scriptedWebhookProvider{
		events:   map[string]commerce.WebhookEvent{"retry": event},
		failures: map[string]int{event.ID: 1},
	}
	service := newWebhookCommerceService(t, test, provider)

	if _, err := service.ProcessWebhook(
		test.ctx,
		[]byte("retry"),
		"stripe-signature",
	); !errors.Is(err, commerce.ErrProviderUnavailable) {
		t.Fatalf("ProcessWebhook(transient provider error) = %v, want provider unavailable", err)
	}
	var state string
	var attempts int32
	var nextAttempt time.Time
	if err := test.pool.QueryRow(test.ctx, `
		SELECT state, attempt_count, next_attempt_at
		FROM commerce_webhook_receipts
		WHERE environment='sandbox' AND provider_event_id=$1`, event.ID).Scan(
		&state,
		&attempts,
		&nextAttempt,
	); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || attempts != 1 || !nextAttempt.After(time.Now().UTC()) {
		t.Fatalf(
			"retry receipt = state:%s attempts:%d next:%s",
			state,
			attempts,
			nextAttempt,
		)
	}
	result, err := service.ReconcileWebhookReceipts(test.ctx, 10)
	if err != nil || result.Processed != 0 || result.DeadLettered != 0 ||
		provider.reconcileCalls() != 1 {
		t.Fatalf("early reconciliation = %#v, %v, calls:%d", result, err, provider.reconcileCalls())
	}
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE commerce_webhook_receipts
		SET next_attempt_at=clock_timestamp() - interval '1 second'
		WHERE environment='sandbox' AND provider_event_id=$1`, event.ID)
	result, err = service.ReconcileWebhookReceipts(test.ctx, 10)
	if err != nil || result.Processed != 1 || result.DeadLettered != 0 ||
		provider.reconcileCalls() != 2 {
		t.Fatalf("due reconciliation = %#v, %v, calls:%d", result, err, provider.reconcileCalls())
	}
}

func TestCommerceWebhookReconciliationDeadLettersExhaustedAndExpiredReceipts(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)
	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); err != nil {
		t.Fatalf("CreateAccountSession() error = %v", err)
	}
	_, providerAccountID, _, _, _ := seedWebhookCommerceRuntime(t, test)
	provider := &scriptedWebhookProvider{
		events:   make(map[string]commerce.WebhookEvent),
		failures: make(map[string]int),
	}
	service := newWebhookCommerceService(t, test, provider)
	dbtest.MustExec(t, test.ctx, test.pool, `
		INSERT INTO commerce_webhook_receipts (
			id, company_id, environment, provider_event_id, provider_account_id,
			event_type, provider_object_id, payload_sha256, livemode,
			provider_created_at, state, attempt_count, next_attempt_at,
			last_error_code, created_at
		) VALUES
			($1, $2, 'sandbox', 'evt_BoardlyExhausted123', $3,
			 'checkout.session.completed', 'cs_BoardlyExhausted123', $4, false,
			 clock_timestamp(), 'failed', 32, clock_timestamp() - interval '1 second',
			 'provider_unavailable', clock_timestamp()),
			($5, $2, 'sandbox', 'evt_BoardlyExpired123', $3,
			 'checkout.session.completed', 'cs_BoardlyExpired123', $6, false,
			 clock_timestamp(), 'failed', 1, clock_timestamp() - interval '1 second',
			 'provider_unavailable', clock_timestamp() - interval '23 hours 1 minute')`,
		dbtest.NewUUID(t),
		test.fixture.CompanyA,
		providerAccountID,
		bytesOf(1, sha256.Size),
		dbtest.NewUUID(t),
		bytesOf(2, sha256.Size),
	)

	result, err := service.ReconcileWebhookReceipts(test.ctx, 10)
	if err != nil || result.Processed != 0 || result.DeadLettered != 2 {
		t.Fatalf("ReconcileWebhookReceipts() = %#v, %v", result, err)
	}
	if provider.reconcileCalls() != 0 {
		t.Fatalf("terminal receipts made %d provider calls, want 0", provider.reconcileCalls())
	}
	rows, err := test.pool.Query(test.ctx, `
		SELECT state, last_error_code
		FROM commerce_webhook_receipts
		WHERE provider_event_id IN ('evt_BoardlyExhausted123', 'evt_BoardlyExpired123')
		ORDER BY provider_event_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	wantCodes := []string{"attempts_exhausted", "receipt_expired"}
	index := 0
	for rows.Next() {
		var state, errorCode string
		if err := rows.Scan(&state, &errorCode); err != nil {
			t.Fatal(err)
		}
		if state != "dead_letter" || errorCode != wantCodes[index] {
			t.Fatalf("terminal receipt %d = %s/%s", index, state, errorCode)
		}
		index++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if index != len(wantCodes) {
		t.Fatalf("terminal receipt rows = %d, want %d", index, len(wantCodes))
	}
}

func checkoutWebhookEvent(
	eventID string,
	accountID string,
	checkoutSessionID string,
	providerTime time.Time,
) commerce.WebhookEvent {
	return commerce.WebhookEvent{
		ID: eventID, AccountID: accountID,
		Type: "checkout.session.completed", ObjectID: checkoutSessionID,
		ProviderTime: providerTime,
		Object: commerce.WebhookObject{
			CheckoutSessionID: checkoutSessionID,
			PaymentIntentID:   "pi_BoardlyPayment123",
			CustomerID:        "cus_BoardlyCustomer123",
			PriceID:           "price_BoardlyOnce123",
			Status:            "complete",
			PaymentStatus:     "paid",
			Amount:            1299,
			Currency:          "gbp",
		},
	}
}

func bytesOf(value byte, length int) []byte {
	result := make([]byte, length)
	for index := range result {
		result[index] = value
	}
	return result
}

func seedWebhookCommerceRuntime(
	t *testing.T,
	test *commerceTest,
) (uuid.UUID, string, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	var accountID uuid.UUID
	var providerAccountID string
	if err := test.pool.QueryRow(test.ctx, `
		UPDATE commerce_accounts
		SET status='ready', details_submitted=true, charges_enabled=true,
			payouts_enabled=true, card_payments_status='active', transfers_status='active',
			version=version+1, updated_at=clock_timestamp()
		WHERE company_id=$1 AND environment='sandbox'
		RETURNING id, provider_account_id`, test.fixture.CompanyA).Scan(
		&accountID,
		&providerAccountID,
	); err != nil {
		t.Fatalf("prepare Commerce account for webhook: %v", err)
	}
	siteID := dbtest.NewUUID(t)
	offerID := dbtest.NewUUID(t)
	customerID := dbtest.NewUUID(t)
	manifestID := dbtest.NewUUID(t)
	dbtest.MustExec(t, test.ctx, test.pool, `
		INSERT INTO commerce_sites (
			id, company_id, commerce_account_id, environment, site_alias, origin,
			verification_token_sha256, portal_return_path, created_by_user_id
		) VALUES (
			$1, $2, $3, 'sandbox', 'webhook-store', 'https://store.example',
			$4, '/account', $5
		)`, siteID, test.fixture.CompanyA, accountID, make([]byte, sha256.Size), test.fixture.UserA)
	dbtest.MustExec(t, test.ctx, test.pool, `
		INSERT INTO commerce_manifests (
			id, company_id, site_id, task_id, run_id, request_key,
			schema_version, canonical_json, digest, state, applied_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, 2, '{}', $7, 'active', clock_timestamp()
		)`, manifestID, test.fixture.CompanyA, siteID, test.fixture.TaskA,
		test.fixture.RunA, dbtest.NewUUID(t), make([]byte, sha256.Size))
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE commerce_sites
		SET status='active', active_manifest_id=$1, verified_at=clock_timestamp()
		WHERE id=$2`, manifestID, siteID)
	dbtest.MustExec(t, test.ctx, test.pool, `
		INSERT INTO commerce_offers (
			id, company_id, site_id, manifest_id, environment, alias, display_name,
			offer_type, entitlement_key, amount, currency, automatic_tax,
			success_path, cancel_path, provider_product_id, provider_price_id, status
		) VALUES (
			$1, $2, $3, $4, 'sandbox', 'report', 'Premium report', 'one_time',
			'report.access', 1299, 'gbp', false, '/paid', '/pricing',
			'prod_BoardlyOnce123', 'price_BoardlyOnce123', 'active'
		)`, offerID, test.fixture.CompanyA, siteID, manifestID)
	dbtest.MustExec(t, test.ctx, test.pool, `
		INSERT INTO commerce_customers (
			id, company_id, site_id, environment, subject_hmac, provider_customer_id
		) VALUES ($1, $2, $3, 'sandbox', $4, 'cus_BoardlyCustomer123')`,
		customerID, test.fixture.CompanyA, siteID, make([]byte, sha256.Size))
	return accountID, providerAccountID, siteID, offerID, customerID
}

func newWebhookCommerceService(
	t *testing.T,
	test *commerceTest,
	webhook commerce.WebhookProvider,
) *commerce.DefaultCommerceService {
	t.Helper()
	digest := sha256.Sum256([]byte("Boardly Commerce Addendum " + commerceTermsVersion))
	service, err := commerce.NewCommerceService(commerce.ServiceOptions{
		Store: test.store, AllowedCountries: []string{"GB"},
		SiteAuthority: &unusedSiteAuthority{}, CustomerHMACKey: make([]byte, 32),
		Terms: commerce.TermsDocument{
			Version: commerceTermsVersion, SHA256: digest,
			DocumentURL: "https://boardly.test/legal/commerce/" + commerceTermsVersion,
			PrivacyURL:  "https://boardly.test/legal/privacy",
		},
		Environments: map[commerce.Environment]commerce.EnvironmentOptions{
			commerce.EnvironmentSandbox: {
				Configured: true, PublishableKey: "pk_test_boardlycommerce123",
				ReturnURL:  "https://boardly.test/connect/stripe/return",
				RefreshURL: "https://boardly.test/connect/stripe/refresh",
				Provider:   test.provider, Webhook: webhook,
			},
		},
		Now: func() time.Time { return test.now },
	})
	if err != nil {
		t.Fatalf("commerce.NewCommerceService(webhook) error = %v", err)
	}
	return service
}

func assertWebhookProjection(
	t *testing.T,
	test *commerceTest,
	accountID uuid.UUID,
	siteID uuid.UUID,
	offerID uuid.UUID,
	customerID uuid.UUID,
	wantPayment string,
	wantEntitlement string,
	wantReason string,
) {
	t.Helper()
	var paymentStatus, entitlementStatus string
	var storedAccountID, storedOfferID, storedCustomerID uuid.UUID
	var reason *string
	if err := test.pool.QueryRow(test.ctx, `
		SELECT payments.status, entitlements.status, entitlements.revoked_reason,
			sites.commerce_account_id, payments.offer_id, payments.customer_id
		FROM commerce_payments AS payments
		JOIN commerce_entitlements AS entitlements
		  ON entitlements.payment_id=payments.id
		JOIN commerce_sites AS sites ON sites.id=payments.site_id
		WHERE payments.site_id=$1 AND payments.provider_payment_intent_id=$2`,
		siteID, "pi_BoardlyPayment123").Scan(
		&paymentStatus,
		&entitlementStatus,
		&reason,
		&storedAccountID,
		&storedOfferID,
		&storedCustomerID,
	); err != nil {
		t.Fatalf("read Commerce webhook projection: %v", err)
	}
	gotReason := ""
	if reason != nil {
		gotReason = *reason
	}
	if paymentStatus != wantPayment || entitlementStatus != wantEntitlement ||
		gotReason != wantReason || storedAccountID != accountID || storedOfferID != offerID ||
		storedCustomerID != customerID {
		t.Fatalf(
			"webhook projection = payment:%q entitlement:%q reason:%q account:%s offer:%s customer:%s",
			paymentStatus,
			entitlementStatus,
			gotReason,
			storedAccountID,
			storedOfferID,
			storedCustomerID,
		)
	}
}

type scriptedWebhookProvider struct {
	mu       sync.Mutex
	events   map[string]commerce.WebhookEvent
	failures map[string]int
	calls    int
}

type serializedWebhookProvider struct {
	events            map[string]commerce.WebhookEvent
	activeStarted     chan struct{}
	canceledStarted   chan struct{}
	releaseActive     chan struct{}
	activeStartOnce   sync.Once
	canceledStartOnce sync.Once
	releaseActiveOnce sync.Once
}

func newSerializedWebhookProvider(
	active commerce.WebhookEvent,
	canceled commerce.WebhookEvent,
) *serializedWebhookProvider {
	return &serializedWebhookProvider{
		events: map[string]commerce.WebhookEvent{
			"active": active, "canceled": canceled,
		},
		activeStarted:   make(chan struct{}),
		canceledStarted: make(chan struct{}),
		releaseActive:   make(chan struct{}),
	}
}

func (provider *serializedWebhookProvider) ConstructWebhookEvent(
	payload []byte,
	signature string,
) (commerce.WebhookEvent, error) {
	if signature != "stripe-signature" {
		return commerce.WebhookEvent{}, errors.New("invalid signature")
	}
	event, exists := provider.events[string(payload)]
	if !exists {
		return commerce.WebhookEvent{}, errors.New("unknown signed payload")
	}
	return event, nil
}

func (provider *serializedWebhookProvider) ReconcileWebhookEvent(
	ctx context.Context,
	event commerce.WebhookEvent,
) (commerce.WebhookEvent, error) {
	if event.ID == provider.events["active"].ID {
		provider.activeStartOnce.Do(func() { close(provider.activeStarted) })
		select {
		case <-provider.releaseActive:
		case <-ctx.Done():
			return commerce.WebhookEvent{}, ctx.Err()
		}
		return provider.events["active"], nil
	}
	if event.ID == provider.events["canceled"].ID {
		provider.canceledStartOnce.Do(func() { close(provider.canceledStarted) })
		return provider.events["canceled"], nil
	}
	return commerce.WebhookEvent{}, errors.New("event not found")
}

func (provider *scriptedWebhookProvider) ConstructWebhookEvent(
	payload []byte,
	signature string,
) (commerce.WebhookEvent, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if signature != "stripe-signature" {
		return commerce.WebhookEvent{}, errors.New("invalid signature")
	}
	event, exists := provider.events[string(payload)]
	if !exists {
		return commerce.WebhookEvent{}, errors.New("unknown signed payload")
	}
	return event, nil
}

func (provider *scriptedWebhookProvider) ReconcileWebhookEvent(
	ctx context.Context,
	event commerce.WebhookEvent,
) (commerce.WebhookEvent, error) {
	if err := ctx.Err(); err != nil {
		return commerce.WebhookEvent{}, err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.calls++
	if provider.failures[event.ID] > 0 {
		provider.failures[event.ID]--
		return commerce.WebhookEvent{}, errors.New("temporary provider outage")
	}
	for _, candidate := range provider.events {
		if candidate.ID == event.ID {
			return candidate, nil
		}
	}
	return commerce.WebhookEvent{}, errors.New("event not found")
}

func (provider *scriptedWebhookProvider) reconcileCalls() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.calls
}

type unusedSiteAuthority struct{}

func (*unusedSiteAuthority) VerifySite(
	context.Context,
	commerce.SiteVerificationInput,
) (commerce.VerifiedSite, error) {
	return commerce.VerifiedSite{}, errors.New("not used")
}
