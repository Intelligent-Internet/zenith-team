package commerce

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestWebhookReceiptDedupesPolledAndSignedDeliveryByProviderEventID(t *testing.T) {
	t.Parallel()
	companyID := uuid.New()
	providerTime := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	event := WebhookEvent{
		ID: "evt_BoardlyCrossChannel123", AccountID: "acct_BoardlyConnected123",
		Type: "checkout.session.completed", ObjectID: "cs_test_BoardlyCheckout123",
		ProviderTime: providerTime,
	}
	polledDigest := sha256.Sum256([]byte("polled Event envelope"))
	receipt := generated.CommerceWebhookReceipt{
		CompanyID: companyID, Environment: string(EnvironmentSandbox),
		ProviderEventID: event.ID, ProviderAccountID: event.AccountID,
		EventType: event.Type, ProviderObjectID: pgtype.Text{String: event.ObjectID, Valid: true},
		PayloadSha256: polledDigest[:], ProviderCreatedAt: requiredTimestamp(providerTime),
	}
	if !sameWebhookReceipt(receipt, companyID, EnvironmentSandbox, event) {
		t.Fatal("sameWebhookReceipt() rejected the same immutable provider Event envelope")
	}
	event.ObjectID = "cs_test_BoardlyDifferent123"
	if sameWebhookReceipt(receipt, companyID, EnvironmentSandbox, event) {
		t.Fatal("sameWebhookReceipt() accepted a conflicting provider Event envelope")
	}
}

func TestSubscriptionEntitlementDecisionRequiresPaidInvoice(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status        string
		invoiceStatus string
		want          entitlementDecision
	}{
		{status: "active", invoiceStatus: "paid", want: entitlementActivate},
		{status: "active", invoiceStatus: "open", want: entitlementPreserve},
		{status: "past_due", invoiceStatus: "open", want: entitlementPreserve},
		{status: "incomplete", invoiceStatus: "open", want: entitlementPreserve},
		{status: "canceled", invoiceStatus: "paid", want: entitlementDeny},
		{status: "incomplete_expired", invoiceStatus: "void", want: entitlementDeny},
		{status: "unpaid", invoiceStatus: "open", want: entitlementDeny},
		{status: "paused", invoiceStatus: "paid", want: entitlementDeny},
	}
	for _, test := range tests {
		if got := subscriptionEntitlementDecision(test.status, test.invoiceStatus); got != test.want {
			t.Errorf("subscriptionEntitlementDecision(%q, %q) = %q, want %q",
				test.status, test.invoiceStatus, got, test.want)
		}
	}
}

func TestPaymentRevocationOnlyUsesFullRefundOrLostDispute(t *testing.T) {
	t.Parallel()
	if refundRevokesEntitlement(1299, 0) || refundRevokesEntitlement(1299, 1298) ||
		!refundRevokesEntitlement(1299, 1299) || !refundRevokesEntitlement(1299, 1400) {
		t.Fatal("refundRevokesEntitlement() did not preserve partial refunds and revoke full refunds")
	}
	for _, status := range []string{"warning_needs_response", "needs_response", "under_review", "won"} {
		if disputeRevokesEntitlement(status) {
			t.Errorf("disputeRevokesEntitlement(%q) = true", status)
		}
	}
	if !disputeRevokesEntitlement("lost") {
		t.Fatal("disputeRevokesEntitlement(lost) = false")
	}
}

func TestEntitlementGrantIDEnforcesSourceRelationships(t *testing.T) {
	t.Parallel()
	paymentID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca62101")
	subscriptionID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca62102")

	tests := []struct {
		name           string
		paymentID      uuid.UUID
		subscriptionID uuid.UUID
		sourceType     string
		want           uuid.UUID
		wantError      bool
	}{
		{
			name: "one-time payment", paymentID: paymentID,
			sourceType: "one_time", want: paymentID,
		},
		{
			name: "subscription renewal", paymentID: paymentID,
			subscriptionID: subscriptionID, sourceType: "subscription",
			want: subscriptionID,
		},
		{
			name:           "subscription cancellation without payment",
			subscriptionID: subscriptionID, sourceType: "subscription",
			want: subscriptionID,
		},
		{name: "one-time without payment", sourceType: "one_time", wantError: true},
		{
			name: "one-time with subscription", paymentID: paymentID,
			subscriptionID: subscriptionID, sourceType: "one_time", wantError: true,
		},
		{
			name: "subscription without subscription ID", paymentID: paymentID,
			sourceType: "subscription", wantError: true,
		},
		{name: "unknown source", paymentID: paymentID, sourceType: "coupon", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := entitlementGrantID(
				test.paymentID,
				test.subscriptionID,
				test.sourceType,
			)
			if (err != nil) != test.wantError || got != test.want {
				t.Fatalf("entitlementGrantID() = %s, %v; want %s, error=%t",
					got, err, test.want, test.wantError)
			}
		})
	}
}
