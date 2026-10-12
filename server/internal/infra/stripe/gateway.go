package stripe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	stripeapi "github.com/stripe/stripe-go/v86"
)

type GatewayOptions struct {
	CommerceKey       string
	ReconcileKey      string
	WebhookSecret     string
	LiveMode          bool
	PlatformAccountID string
	HTTPClient        *http.Client
}

var (
	ErrGatewayCredentialInvalid    = errors.New("stripe gateway credential invalid")
	ErrGatewayPermissionRequired   = errors.New("stripe gateway permission required")
	ErrGatewayRateLimited          = errors.New("stripe gateway rate limited")
	ErrGatewayTaxSetupRequired     = errors.New("stripe gateway tax setup required")
	ErrGatewayTaxLocationInvalid   = errors.New("stripe gateway tax location invalid")
	ErrGatewayIdempotencyConflict  = errors.New("stripe gateway idempotency conflict")
	ErrGatewayRequestInvalid       = errors.New("stripe gateway request invalid")
	ErrGatewayProviderStateInvalid = errors.New("stripe gateway provider state invalid")
)

// GatewayProvider owns Stripe APIs used after a Connected Account has
// completed onboarding. The write and reconciliation clients deliberately use
// different restricted keys so a leaked read key cannot create charges.
type GatewayProvider struct {
	commerceClient    *stripeapi.Client
	reconcileClient   *stripeapi.Client
	webhookSecret     string
	liveMode          bool
	platformAccountID string
}

type ProductInput struct {
	AccountID      string
	IdempotencyKey string
	Name           string
	Metadata       map[string]string
}

type ProductResource struct {
	ID string
}

type PriceInput struct {
	AccountID      string
	IdempotencyKey string
	ProductID      string
	Amount         int64
	Currency       string
	Interval       string
	Metadata       map[string]string
}

type PriceResource struct {
	ID string
}

type PortalProduct struct {
	ProductID string
	PriceIDs  []string
}

type PortalConfigurationInput struct {
	AccountID            string
	IdempotencyKey       string
	Name                 string
	DefaultReturnURL     string
	SubscriptionProducts []PortalProduct
	Metadata             map[string]string
}

type PortalConfigurationResource struct {
	ID string
}

type CustomerInput struct {
	AccountID      string
	IdempotencyKey string
	Metadata       map[string]string
}

type CustomerResource struct {
	ID string
}

type CheckoutInput struct {
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

type HostedSession struct {
	ID  string
	URL string
}

type CheckoutRetrievalInput struct {
	AccountID   string
	SessionID   string
	CustomerID  string
	OperationID string
}

type PortalInput struct {
	AccountID       string
	IdempotencyKey  string
	CustomerID      string
	ConfigurationID string
	ReturnURL       string
}

func NewGatewayProvider(options GatewayOptions) (*GatewayProvider, error) {
	prefix := "rk_test_"
	if options.LiveMode {
		prefix = "rk_live_"
	}
	if !validRestrictedKey(options.CommerceKey, prefix) ||
		!validRestrictedKey(options.ReconcileKey, prefix) ||
		options.CommerceKey == options.ReconcileKey ||
		len(options.WebhookSecret) > maxStripeKeyBytes ||
		!strings.HasPrefix(options.WebhookSecret, "whsec_") ||
		strings.TrimSpace(options.WebhookSecret) != options.WebhookSecret ||
		!validAccountID(options.PlatformAccountID) ||
		options.HTTPClient == nil || options.HTTPClient.Timeout <= 0 {
		return nil, errors.New("Stripe Commerce gateway configuration is invalid")
	}
	backends := stripeapi.NewBackends(options.HTTPClient)
	return &GatewayProvider{
		commerceClient:    stripeapi.NewClient(options.CommerceKey, stripeapi.WithBackends(backends)),
		reconcileClient:   stripeapi.NewClient(options.ReconcileKey, stripeapi.WithBackends(backends)),
		webhookSecret:     options.WebhookSecret,
		liveMode:          options.LiveMode,
		platformAccountID: options.PlatformAccountID,
	}, nil
}

func (provider *GatewayProvider) CreateProduct(
	ctx context.Context,
	input ProductInput,
) (ProductResource, error) {
	if !provider.validWriteRequest(input.AccountID, input.IdempotencyKey) ||
		!validProviderText(input.Name, 255) || !validGatewayMetadata(input.Metadata) {
		return ProductResource{}, fmt.Errorf("%w: invalid Stripe Product request", ErrGatewayRequestInvalid)
	}
	params := &stripeapi.ProductCreateParams{Name: stripeapi.String(input.Name)}
	setDirectChargeParams(&params.Params, input.AccountID, input.IdempotencyKey)
	addMetadata(params.AddMetadata, input.Metadata)
	product, err := provider.commerceClient.V1Products.Create(ctx, params)
	if err != nil {
		return ProductResource{}, gatewayError("Stripe create Product", err)
	}
	if product == nil || !validProviderID(product.ID, "prod_") ||
		product.Livemode != provider.liveMode {
		return ProductResource{}, fmt.Errorf(
			"%w: Stripe Product response is invalid",
			ErrGatewayProviderStateInvalid,
		)
	}
	return ProductResource{ID: product.ID}, nil
}

func (provider *GatewayProvider) CreatePrice(
	ctx context.Context,
	input PriceInput,
) (PriceResource, error) {
	if !provider.validWriteRequest(input.AccountID, input.IdempotencyKey) ||
		!validProviderID(input.ProductID, "prod_") || input.Amount < 1 ||
		!validCurrency(input.Currency) || !validGatewayMetadata(input.Metadata) ||
		input.Interval != "" && input.Interval != "month" && input.Interval != "year" {
		return PriceResource{}, fmt.Errorf("%w: invalid Stripe Price request", ErrGatewayRequestInvalid)
	}
	params := &stripeapi.PriceCreateParams{
		Currency: stripeapi.String(input.Currency), Product: stripeapi.String(input.ProductID),
		UnitAmount: stripeapi.Int64(input.Amount),
	}
	if input.Interval != "" {
		params.Recurring = &stripeapi.PriceCreateRecurringParams{
			Interval:  stripeapi.String(input.Interval),
			UsageType: stripeapi.String(string(stripeapi.PriceRecurringUsageTypeLicensed)),
		}
	}
	setDirectChargeParams(&params.Params, input.AccountID, input.IdempotencyKey)
	addMetadata(params.AddMetadata, input.Metadata)
	price, err := provider.commerceClient.V1Prices.Create(ctx, params)
	if err != nil {
		return PriceResource{}, gatewayError("Stripe create Price", err)
	}
	if price == nil || !validProviderID(price.ID, "price_") ||
		price.Livemode != provider.liveMode {
		return PriceResource{}, fmt.Errorf(
			"%w: Stripe Price response is invalid",
			ErrGatewayProviderStateInvalid,
		)
	}
	return PriceResource{ID: price.ID}, nil
}

func (provider *GatewayProvider) CreatePortalConfiguration(
	ctx context.Context,
	input PortalConfigurationInput,
) (PortalConfigurationResource, error) {
	if !provider.validWriteRequest(input.AccountID, input.IdempotencyKey) ||
		!validProviderText(input.Name, 255) ||
		!validReturnURL(input.DefaultReturnURL, provider.liveMode) ||
		len(input.SubscriptionProducts) > 10 || !validGatewayMetadata(input.Metadata) {
		return PortalConfigurationResource{}, fmt.Errorf(
			"%w: invalid Stripe Portal configuration request",
			ErrGatewayRequestInvalid,
		)
	}
	products := make(
		[]*stripeapi.BillingPortalConfigurationCreateFeaturesSubscriptionUpdateProductParams,
		0,
		len(input.SubscriptionProducts),
	)
	for _, product := range input.SubscriptionProducts {
		if !validProviderID(product.ProductID, "prod_") ||
			len(product.PriceIDs) == 0 || len(product.PriceIDs) > 20 {
			return PortalConfigurationResource{}, fmt.Errorf(
				"%w: invalid Stripe Portal Product request",
				ErrGatewayRequestInvalid,
			)
		}
		prices := make([]*string, 0, len(product.PriceIDs))
		for _, priceID := range product.PriceIDs {
			if !validProviderID(priceID, "price_") {
				return PortalConfigurationResource{}, fmt.Errorf(
					"%w: invalid Stripe Portal Price request",
					ErrGatewayRequestInvalid,
				)
			}
			prices = append(prices, stripeapi.String(priceID))
		}
		products = append(products,
			&stripeapi.BillingPortalConfigurationCreateFeaturesSubscriptionUpdateProductParams{
				Product: stripeapi.String(product.ProductID),
				Prices:  prices,
				AdjustableQuantity: &stripeapi.BillingPortalConfigurationCreateFeaturesSubscriptionUpdateProductAdjustableQuantityParams{
					Enabled: stripeapi.Bool(false),
				},
			},
		)
	}
	params := &stripeapi.BillingPortalConfigurationCreateParams{
		Name:             stripeapi.String(input.Name),
		DefaultReturnURL: stripeapi.String(input.DefaultReturnURL),
		LoginPage: &stripeapi.BillingPortalConfigurationCreateLoginPageParams{
			Enabled: stripeapi.Bool(false),
		},
		Features: &stripeapi.BillingPortalConfigurationCreateFeaturesParams{
			InvoiceHistory: &stripeapi.BillingPortalConfigurationCreateFeaturesInvoiceHistoryParams{
				Enabled: stripeapi.Bool(true),
			},
			PaymentMethodUpdate: &stripeapi.BillingPortalConfigurationCreateFeaturesPaymentMethodUpdateParams{
				Enabled: stripeapi.Bool(true),
			},
			SubscriptionCancel: &stripeapi.BillingPortalConfigurationCreateFeaturesSubscriptionCancelParams{
				Enabled: stripeapi.Bool(true),
				Mode: stripeapi.String(
					string(
						stripeapi.BillingPortalConfigurationFeaturesSubscriptionCancelModeAtPeriodEnd,
					),
				),
				ProrationBehavior: stripeapi.String(
					string(
						stripeapi.BillingPortalConfigurationFeaturesSubscriptionCancelProrationBehaviorNone,
					),
				),
			},
			SubscriptionUpdate: &stripeapi.BillingPortalConfigurationCreateFeaturesSubscriptionUpdateParams{
				Enabled: stripeapi.Bool(len(products) > 0),
				DefaultAllowedUpdates: []*string{
					stripeapi.String(
						string(
							stripeapi.BillingPortalConfigurationFeaturesSubscriptionUpdateDefaultAllowedUpdatePrice,
						),
					),
				},
				BillingCycleAnchor: stripeapi.String(
					string(
						stripeapi.BillingPortalConfigurationFeaturesSubscriptionUpdateBillingCycleAnchorUnchanged,
					),
				),
				ProrationBehavior: stripeapi.String(
					string(
						stripeapi.BillingPortalConfigurationFeaturesSubscriptionUpdateProrationBehaviorAlwaysInvoice,
					),
				),
				Products: products,
				ScheduleAtPeriodEnd: &stripeapi.BillingPortalConfigurationCreateFeaturesSubscriptionUpdateScheduleAtPeriodEndParams{
					Conditions: []*stripeapi.BillingPortalConfigurationCreateFeaturesSubscriptionUpdateScheduleAtPeriodEndConditionParams{
						{
							Type: stripeapi.String(
								string(
									stripeapi.BillingPortalConfigurationFeaturesSubscriptionUpdateScheduleAtPeriodEndConditionTypeDecreasingItemAmount,
								),
							),
						},
						{
							Type: stripeapi.String(
								string(
									stripeapi.BillingPortalConfigurationFeaturesSubscriptionUpdateScheduleAtPeriodEndConditionTypeShorteningInterval,
								),
							),
						},
					},
				},
			},
		},
	}
	setDirectChargeParams(&params.Params, input.AccountID, input.IdempotencyKey)
	addMetadata(params.AddMetadata, input.Metadata)
	configuration, err := provider.commerceClient.V1BillingPortalConfigurations.Create(ctx, params)
	if err != nil {
		return PortalConfigurationResource{}, gatewayError("Stripe create Portal configuration", err)
	}
	if configuration == nil || !validProviderID(configuration.ID, "bpc_") ||
		!configuration.Active || configuration.Livemode != provider.liveMode {
		return PortalConfigurationResource{}, fmt.Errorf(
			"%w: Stripe Portal configuration response is invalid",
			ErrGatewayProviderStateInvalid,
		)
	}
	return PortalConfigurationResource{ID: configuration.ID}, nil
}

func (provider *GatewayProvider) CreateCustomer(
	ctx context.Context,
	input CustomerInput,
) (CustomerResource, error) {
	if !provider.validWriteRequest(input.AccountID, input.IdempotencyKey) ||
		!validGatewayMetadata(input.Metadata) {
		return CustomerResource{}, fmt.Errorf(
			"%w: invalid Stripe Customer request",
			ErrGatewayRequestInvalid,
		)
	}
	params := &stripeapi.CustomerCreateParams{}
	setDirectChargeParams(&params.Params, input.AccountID, input.IdempotencyKey)
	addMetadata(params.AddMetadata, input.Metadata)
	customer, err := provider.commerceClient.V1Customers.Create(ctx, params)
	if err != nil {
		return CustomerResource{}, gatewayError("Stripe create Customer", err)
	}
	if customer == nil || !validProviderID(customer.ID, "cus_") ||
		customer.Livemode != provider.liveMode {
		return CustomerResource{}, fmt.Errorf(
			"%w: Stripe Customer response is invalid",
			ErrGatewayProviderStateInvalid,
		)
	}
	return CustomerResource{ID: customer.ID}, nil
}

func (provider *GatewayProvider) CreateCheckoutSession(
	ctx context.Context,
	input CheckoutInput,
) (HostedSession, error) {
	operationID := input.Metadata["boardly_operation_id"]
	if !provider.validWriteRequest(input.AccountID, input.IdempotencyKey) ||
		!validProviderID(input.CustomerID, "cus_") ||
		!validProviderID(input.PriceID, "price_") ||
		!validReturnURL(input.SuccessURL, provider.liveMode) ||
		!validReturnURL(input.CancelURL, provider.liveMode) ||
		!validGatewayMetadata(input.Metadata) || !validOperationReference(operationID) {
		return HostedSession{}, fmt.Errorf(
			"%w: invalid Stripe Checkout Session request",
			ErrGatewayRequestInvalid,
		)
	}
	mode := stripeapi.CheckoutSessionModePayment
	params := &stripeapi.CheckoutSessionCreateParams{
		Customer: stripeapi.String(input.CustomerID),
		Mode:     stripeapi.String(string(mode)),
		LineItems: []*stripeapi.CheckoutSessionCreateLineItemParams{{
			Price: stripeapi.String(input.PriceID), Quantity: stripeapi.Int64(1),
		}},
		AutomaticTax: &stripeapi.CheckoutSessionCreateAutomaticTaxParams{
			Enabled: stripeapi.Bool(input.AutomaticTax),
		},
		ClientReferenceID: stripeapi.String(operationID),
		SuccessURL:        stripeapi.String(input.SuccessURL),
		CancelURL:         stripeapi.String(input.CancelURL),
	}
	if input.AutomaticTax {
		params.CustomerUpdate = &stripeapi.CheckoutSessionCreateCustomerUpdateParams{
			Address: stripeapi.String("auto"),
		}
	}
	if input.Subscription {
		mode = stripeapi.CheckoutSessionModeSubscription
		params.Mode = stripeapi.String(string(mode))
		params.SubscriptionData = &stripeapi.CheckoutSessionCreateSubscriptionDataParams{}
		addMetadata(params.SubscriptionData.AddMetadata, input.Metadata)
	} else {
		params.PaymentIntentData = &stripeapi.CheckoutSessionCreatePaymentIntentDataParams{}
		addMetadata(params.PaymentIntentData.AddMetadata, input.Metadata)
	}
	setDirectChargeParams(&params.Params, input.AccountID, input.IdempotencyKey)
	addMetadata(params.AddMetadata, input.Metadata)
	session, err := provider.commerceClient.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return HostedSession{}, gatewayError("Stripe create Checkout Session", err)
	}
	if session == nil || !validProviderID(session.ID, "cs_") ||
		session.Livemode != provider.liveMode || !validHostedURL(session.URL, "checkout.stripe.com") {
		return HostedSession{}, fmt.Errorf(
			"%w: Stripe Checkout Session response is invalid",
			ErrGatewayProviderStateInvalid,
		)
	}
	return HostedSession{ID: session.ID, URL: session.URL}, nil
}

// RetrieveCheckoutSession recovers an already-created Checkout redirect with
// the read-only key. It accepts the persisted provider ID only and verifies
// that Stripe still binds the open Session to the expected customer and
// operation before returning the short-lived hosted URL.
func (provider *GatewayProvider) RetrieveCheckoutSession(
	ctx context.Context,
	input CheckoutRetrievalInput,
) (HostedSession, error) {
	if !provider.validReadRequest(input.AccountID) ||
		!validProviderID(input.SessionID, "cs_") ||
		!validProviderID(input.CustomerID, "cus_") ||
		!validOperationReference(input.OperationID) {
		return HostedSession{}, fmt.Errorf(
			"%w: invalid Stripe Checkout recovery request",
			ErrGatewayRequestInvalid,
		)
	}
	params := &stripeapi.CheckoutSessionRetrieveParams{}
	params.SetStripeAccount(input.AccountID)
	session, err := provider.reconcileClient.V1CheckoutSessions.Retrieve(
		ctx,
		input.SessionID,
		params,
	)
	if err != nil {
		return HostedSession{}, gatewayError("Stripe retrieve Checkout Session", err)
	}
	if session == nil || session.ID != input.SessionID ||
		session.Livemode != provider.liveMode ||
		session.Status != stripeapi.CheckoutSessionStatusOpen ||
		session.Customer == nil || session.Customer.ID != input.CustomerID ||
		session.ClientReferenceID != input.OperationID ||
		session.Metadata["boardly_customer_id"] == "" ||
		session.Metadata["boardly_operation_id"] != input.OperationID ||
		!validHostedURL(session.URL, "checkout.stripe.com") {
		return HostedSession{}, fmt.Errorf(
			"%w: Stripe Checkout recovery response is invalid",
			ErrGatewayProviderStateInvalid,
		)
	}
	return HostedSession{ID: session.ID, URL: session.URL}, nil
}

func (provider *GatewayProvider) CreatePortalSession(
	ctx context.Context,
	input PortalInput,
) (HostedSession, error) {
	if !provider.validWriteRequest(input.AccountID, input.IdempotencyKey) ||
		!validProviderID(input.CustomerID, "cus_") ||
		!validProviderID(input.ConfigurationID, "bpc_") ||
		!validReturnURL(input.ReturnURL, provider.liveMode) {
		return HostedSession{}, fmt.Errorf(
			"%w: invalid Stripe Portal Session request",
			ErrGatewayRequestInvalid,
		)
	}
	params := &stripeapi.BillingPortalSessionCreateParams{
		Customer:      stripeapi.String(input.CustomerID),
		Configuration: stripeapi.String(input.ConfigurationID),
		ReturnURL:     stripeapi.String(input.ReturnURL),
	}
	setDirectChargeParams(&params.Params, input.AccountID, input.IdempotencyKey)
	session, err := provider.commerceClient.V1BillingPortalSessions.Create(ctx, params)
	if err != nil {
		return HostedSession{}, gatewayError("Stripe create Portal Session", err)
	}
	if session == nil || !validProviderID(session.ID, "bps_") ||
		session.Livemode != provider.liveMode || !validHostedURL(session.URL, "billing.stripe.com") {
		return HostedSession{}, fmt.Errorf(
			"%w: Stripe Portal Session response is invalid",
			ErrGatewayProviderStateInvalid,
		)
	}
	return HostedSession{ID: session.ID, URL: session.URL}, nil
}

func (provider *GatewayProvider) validWriteRequest(accountID, idempotencyKey string) bool {
	return provider != nil && provider.commerceClient != nil && provider.reconcileClient != nil &&
		validAccountID(accountID) && accountID != provider.platformAccountID &&
		validIdempotencyKey(idempotencyKey)
}

func (provider *GatewayProvider) validReadRequest(accountID string) bool {
	return provider != nil && provider.reconcileClient != nil &&
		validAccountID(accountID) && accountID != provider.platformAccountID
}

func setDirectChargeParams(params *stripeapi.Params, accountID, idempotencyKey string) {
	params.SetStripeAccount(accountID)
	params.SetIdempotencyKey(idempotencyKey)
}

func gatewayError(operation string, err error) error {
	target := gatewayErrorTarget(err)
	if target != nil {
		err = errors.Join(target, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func gatewayErrorTarget(err error) error {
	var stripeErr *stripeapi.Error
	if !errors.As(err, &stripeErr) {
		return nil
	}

	switch {
	case stripeErr.Code == stripeapi.ErrorCodeCustomerTaxLocationInvalid:
		return ErrGatewayTaxLocationInvalid
	case stripeErr.Code == stripeapi.ErrorCodeStripeTaxInactive:
		return ErrGatewayTaxSetupRequired
	case stripeErr.HTTPStatusCode == http.StatusUnauthorized,
		stripeErr.Code == stripeapi.ErrorCodeAPIKeyExpired,
		stripeErr.Code == stripeapi.ErrorCodePlatformAPIKeyExpired:
		return ErrGatewayCredentialInvalid
	case stripeErr.HTTPStatusCode == http.StatusForbidden,
		stripeErr.Code == stripeapi.ErrorCode("more_permissions_required"):
		return ErrGatewayPermissionRequired
	case stripeErr.HTTPStatusCode == http.StatusTooManyRequests,
		stripeErr.Type == stripeapi.ErrorTypeRateLimit,
		stripeErr.Code == stripeapi.ErrorCodeRateLimit,
		stripeErr.Code == stripeapi.ErrorCodeLockTimeout:
		return ErrGatewayRateLimited
	case stripeErr.HTTPStatusCode == http.StatusConflict,
		stripeErr.Type == stripeapi.ErrorTypeIdempotency,
		stripeErr.Code == stripeapi.ErrorCodeIdempotencyKeyInUse:
		return ErrGatewayIdempotencyConflict
	case stripeErr.HTTPStatusCode >= http.StatusBadRequest &&
		stripeErr.HTTPStatusCode < http.StatusInternalServerError:
		return ErrGatewayRequestInvalid
	default:
		return nil
	}
}

func validRestrictedKey(value, prefix string) bool {
	return len(value) <= maxStripeKeyBytes && len(value) >= len(prefix)+4 &&
		strings.HasPrefix(value, prefix) && strings.TrimSpace(value) == value
}

func validGatewayMetadata(metadata map[string]string) bool {
	if len(metadata) == 0 || len(metadata) > 50 {
		return false
	}
	for key, value := range metadata {
		if !validMetadata(key, value) {
			return false
		}
	}
	return true
}

func addMetadata(add func(string, string), metadata map[string]string) {
	for key, value := range metadata {
		add(key, value)
	}
}

func validProviderID(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) >= len(prefix)+8 && len(value) <= 255 &&
		strings.IndexFunc(value, func(character rune) bool {
			return character != '_' && (character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') && (character < '0' || character > '9')
		}) == -1
}

func validProviderText(value string, maxLength int) bool {
	return value != "" && len(value) <= maxLength && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n")
}

func validOperationReference(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validCurrency(value string) bool {
	return len(value) == 3 && value[0] >= 'a' && value[0] <= 'z' &&
		value[1] >= 'a' && value[1] <= 'z' && value[2] >= 'a' && value[2] <= 'z'
}

func validHostedURL(value, hostname string) bool {
	return strings.HasPrefix(value, "https://"+hostname+"/") &&
		!strings.ContainsAny(value, "\r\n")
}
