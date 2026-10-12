package commerce

import (
	"context"
	"time"
)

type CatalogProductInput struct {
	AccountID      string
	IdempotencyKey string
	Name           string
	Metadata       map[string]string
}

type CatalogProduct struct {
	ID string
}

type CatalogPriceInput struct {
	AccountID      string
	IdempotencyKey string
	ProductID      string
	Amount         int64
	Currency       string
	Interval       string
	Metadata       map[string]string
}

type CatalogPrice struct {
	ID string
}

type CatalogPortalProduct struct {
	ProductID string
	PriceIDs  []string
}

type CatalogPortalInput struct {
	AccountID            string
	IdempotencyKey       string
	Name                 string
	DefaultReturnURL     string
	SubscriptionProducts []CatalogPortalProduct
	Metadata             map[string]string
}

type CatalogPortal struct {
	ID string
}

type GatewayCustomerInput struct {
	AccountID      string
	IdempotencyKey string
	Metadata       map[string]string
}

type GatewayCustomer struct {
	ID string
}

type GatewayCheckoutInput struct {
	AccountID      string
	IdempotencyKey string
	CustomerID     string
	PriceID        string
	Subscription   bool
	AutomaticTax   bool
	SuccessURL     string
	CancelURL      string
	Metadata       map[string]string
}

type GatewayPortalInput struct {
	AccountID       string
	IdempotencyKey  string
	CustomerID      string
	ConfigurationID string
	ReturnURL       string
}

type GatewayCheckoutRecoveryInput struct {
	AccountID   string
	SessionID   string
	CustomerID  string
	OperationID string
}

type GatewayHostedSession struct {
	ID  string
	URL string
}

// GatewayProvider is the post-onboarding provider boundary. No SDK types,
// secret keys, raw tokens, or webhook payloads may cross this interface.
type GatewayProvider interface {
	CreateProduct(context.Context, CatalogProductInput) (CatalogProduct, error)
	CreatePrice(context.Context, CatalogPriceInput) (CatalogPrice, error)
	CreatePortalConfiguration(context.Context, CatalogPortalInput) (CatalogPortal, error)
	CreateCustomer(context.Context, GatewayCustomerInput) (GatewayCustomer, error)
	CreateCheckoutSession(context.Context, GatewayCheckoutInput) (GatewayHostedSession, error)
	CreatePortalSession(context.Context, GatewayPortalInput) (GatewayHostedSession, error)
}

// CheckoutRecoveryProvider is an optional read-side capability implemented by
// the production Stripe adapter. Keeping it separate lets domain fakes model
// catalog-only behavior without inheriting reconciliation operations.
type CheckoutRecoveryProvider interface {
	RetrieveCheckoutSession(
		context.Context,
		GatewayCheckoutRecoveryInput,
	) (GatewayHostedSession, error)
}

type SiteVerificationInput struct {
	Origin         string
	SiteAlias      string
	ManifestDigest string
}

type VerifiedSite struct {
	SigningKeys []SitePublicJWK
}

// SiteAuthority verifies the public well-known proof and signing keys for a
// remotely reachable Site. Loopback Sites use the daemon-authenticated key
// registration seam instead.
type SiteAuthority interface {
	VerifySite(context.Context, SiteVerificationInput) (VerifiedSite, error)
}

type WebhookEvent struct {
	ID           string
	AccountID    string
	Type         string
	ObjectID     string
	LiveMode     bool
	ProviderTime time.Time
	Object       WebhookObject
}

type WebhookObject struct {
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
	InvoicePayments   []WebhookInvoicePayment
}

type WebhookInvoicePayment struct {
	ID              string
	PaymentIntentID string
	Amount          int64
	PaidAt          *time.Time
}

type WebhookProvider interface {
	ConstructWebhookEvent([]byte, string) (WebhookEvent, error)
	ReconcileWebhookEvent(context.Context, WebhookEvent) (WebhookEvent, error)
}
