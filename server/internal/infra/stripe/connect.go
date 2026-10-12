// Package stripe is the server-side Stripe API adapter. Secret keys stay inside
// this package and are never returned, logged, or attached to an Executor
// environment.
package stripe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	stripeapi "github.com/stripe/stripe-go/v86"
)

const maxStripeKeyBytes = 4096

const connectSetupRequiredMessage = "You can only create new accounts if you've signed up for Connect, which you can do at https://dashboard.stripe.com/connect."

var ErrConnectSetupRequired = errors.New("Stripe Connect setup required")

type Options struct {
	SecretKey         string
	LiveMode          bool
	PlatformAccountID string
	PlatformCountry   string
	HTTPClient        *http.Client
}

type ConnectProvider struct {
	client            *stripeapi.Client
	liveMode          bool
	platformAccountID string
	platformCountry   string
	platformMu        sync.Mutex
	platformVerified  bool
}

type CapabilityStatus string

const (
	CapabilityStatusActive      CapabilityStatus = "active"
	CapabilityStatusInactive    CapabilityStatus = "inactive"
	CapabilityStatusPending     CapabilityStatus = "pending"
	CapabilityStatusUnrequested CapabilityStatus = "unrequested"
)

type ControllerSnapshot struct {
	LossesPayments        string
	FeesPayer             string
	RequirementCollection string
	DashboardType         string
}

type Requirements struct {
	CurrentlyDue    int
	EventuallyDue   int
	PastDue         int
	CurrentDeadline *time.Time
	DisabledReason  string
}

type Account struct {
	ID                 string
	Metadata           map[string]string
	Country            string
	DefaultCurrency    string
	DetailsSubmitted   bool
	ChargesEnabled     bool
	PayoutsEnabled     bool
	CardPaymentsStatus CapabilityStatus
	TransfersStatus    CapabilityStatus
	Requirements       Requirements
	Controller         ControllerSnapshot
}

type CreateAccountInput struct {
	IdempotencyKey            string
	Metadata                  map[string]string
	ControllerTemplateVersion int32
}

type FindAccountInput struct {
	Metadata      map[string]string
	CreatedAfter  time.Time
	CreatedBefore time.Time
	StartingAfter string
	Limit         int64
}

type FindAccountResult struct {
	Account    Account
	Found      bool
	HasMore    bool
	NextCursor string
}

type AccountSessionInput struct {
	AccountID      string
	IdempotencyKey string
}

type OnboardingLinkInput struct {
	AccountID      string
	IdempotencyKey string
	ReturnURL      string
	RefreshURL     string
}

type DashboardLinkInput struct {
	AccountID string
}

type Session struct {
	ClientSecret string
	ExpiresAt    time.Time
}

type Link struct {
	URL       string
	ExpiresAt time.Time
}

type DashboardLink struct {
	URL string
}

func NewConnectProvider(options Options) (*ConnectProvider, error) {
	expectedPrefix := "sk_test_"
	restrictedPrefix := "rk_test_"
	if options.LiveMode {
		expectedPrefix = "sk_live_"
		restrictedPrefix = "rk_live_"
	}
	if len(options.SecretKey) > maxStripeKeyBytes || strings.TrimSpace(options.SecretKey) != options.SecretKey ||
		(!strings.HasPrefix(options.SecretKey, expectedPrefix) &&
			!strings.HasPrefix(options.SecretKey, restrictedPrefix)) {
		return nil, errors.New("Stripe secret key is invalid or in the wrong mode")
	}
	if !validAccountID(options.PlatformAccountID) || !validCountry(options.PlatformCountry) {
		return nil, errors.New("Stripe platform identity is invalid")
	}
	if options.HTTPClient == nil || options.HTTPClient.Timeout <= 0 {
		return nil, errors.New("Stripe HTTP client with a timeout is required")
	}
	client := stripeapi.NewClient(
		options.SecretKey,
		stripeapi.WithBackends(stripeapi.NewBackends(options.HTTPClient)),
	)
	return &ConnectProvider{
		client: client, liveMode: options.LiveMode,
		platformAccountID: options.PlatformAccountID, platformCountry: options.PlatformCountry,
	}, nil
}

func (provider *ConnectProvider) CreateAccount(
	ctx context.Context,
	input CreateAccountInput,
) (Account, error) {
	if provider == nil || provider.client == nil || input.ControllerTemplateVersion != 1 ||
		!validIdempotencyKey(input.IdempotencyKey) || len(input.Metadata) == 0 {
		return Account{}, errors.New("invalid Stripe Connected Account request")
	}
	if err := ctx.Err(); err != nil {
		return Account{}, err
	}
	if err := provider.verifyPlatform(ctx); err != nil {
		return Account{}, err
	}
	params := &stripeapi.AccountCreateParams{
		Controller: &stripeapi.AccountCreateControllerParams{
			Fees: &stripeapi.AccountCreateControllerFeesParams{
				Payer: stripeapi.String(string(stripeapi.AccountControllerFeesPayerAccount)),
			},
			Losses: &stripeapi.AccountCreateControllerLossesParams{
				Payments: stripeapi.String(string(stripeapi.AccountControllerLossesPaymentsStripe)),
			},
			RequirementCollection: stripeapi.String(
				string(stripeapi.AccountControllerRequirementCollectionStripe),
			),
			StripeDashboard: &stripeapi.AccountCreateControllerStripeDashboardParams{
				Type: stripeapi.String(string(stripeapi.AccountControllerStripeDashboardTypeFull)),
			},
		},
	}
	params.SetIdempotencyKey(input.IdempotencyKey)
	for key, value := range input.Metadata {
		if !validMetadata(key, value) {
			return Account{}, errors.New("invalid Stripe Connected Account metadata")
		}
		params.AddMetadata(key, value)
	}
	if err := ctx.Err(); err != nil {
		return Account{}, err
	}
	account, err := provider.client.V1Accounts.Create(ctx, params)
	if err != nil {
		if connectSetupRequired(err) {
			return Account{}, fmt.Errorf(
				"Stripe create Account: %w",
				errors.Join(ErrConnectSetupRequired, err),
			)
		}
		return Account{}, fmt.Errorf("Stripe create Account: %w", err)
	}
	return providerAccount(account)
}

func (provider *ConnectProvider) RetrieveAccount(
	ctx context.Context,
	accountID string,
) (Account, error) {
	if provider == nil || provider.client == nil || !validAccountID(accountID) {
		return Account{}, errors.New("invalid Stripe Connected Account retrieval")
	}
	if err := provider.verifyPlatform(ctx); err != nil {
		return Account{}, err
	}
	account, err := provider.client.V1Accounts.GetByID(ctx, accountID, nil)
	if err != nil {
		return Account{}, fmt.Errorf("Stripe retrieve Account: %w", err)
	}
	return providerAccount(account)
}

// FindAccount scans exactly one bounded Accounts v1 page. The caller owns the
// durable cursor and repeats this operation across leases when Stripe reports
// more pages; this method never auto-paginates.
func (provider *ConnectProvider) FindAccount(
	ctx context.Context,
	input FindAccountInput,
) (FindAccountResult, error) {
	if provider == nil || provider.client == nil || input.CreatedAfter.IsZero() ||
		input.CreatedBefore.IsZero() || !input.CreatedBefore.After(input.CreatedAfter) ||
		input.Limit < 1 || input.Limit > 100 || len(input.Metadata) == 0 ||
		(input.StartingAfter != "" && !validAccountID(input.StartingAfter)) {
		return FindAccountResult{}, errors.New("invalid Stripe Account recovery request")
	}
	for key, value := range input.Metadata {
		if !validMetadata(key, value) {
			return FindAccountResult{}, errors.New("invalid Stripe Account recovery metadata")
		}
	}
	if err := provider.verifyPlatform(ctx); err != nil {
		return FindAccountResult{}, err
	}
	params := &stripeapi.AccountListParams{
		CreatedRange: &stripeapi.RangeQueryParams{
			GreaterThanOrEqual: input.CreatedAfter.Unix(),
			LesserThanOrEqual:  input.CreatedBefore.Unix(),
		},
	}
	params.Limit = stripeapi.Int64(input.Limit)
	if input.StartingAfter != "" {
		params.StartingAfter = stripeapi.String(input.StartingAfter)
	}
	page := provider.client.V1Accounts.List(ctx, params)
	if err := page.Err(); err != nil {
		return FindAccountResult{}, fmt.Errorf("Stripe list Accounts for recovery: %w", err)
	}
	accounts := page.Data()
	result := FindAccountResult{HasMore: page.Meta().HasMore}
	for _, account := range accounts {
		if account == nil || !validAccountID(account.ID) {
			return FindAccountResult{}, errors.New("Stripe Account recovery page is invalid")
		}
		if !metadataContains(account.Metadata, input.Metadata) {
			continue
		}
		if result.Found {
			return FindAccountResult{}, errors.New("Stripe Account recovery metadata is not unique")
		}
		mapped, err := providerAccount(account)
		if err != nil {
			return FindAccountResult{}, err
		}
		result.Account = mapped
		result.Found = true
	}
	if result.HasMore {
		if len(accounts) == 0 {
			return FindAccountResult{}, errors.New("Stripe Account recovery page has no cursor")
		}
		result.NextCursor = accounts[len(accounts)-1].ID
	}
	return result, nil
}

func (provider *ConnectProvider) CreateAccountSession(
	ctx context.Context,
	input AccountSessionInput,
) (Session, error) {
	if provider == nil || provider.client == nil || !validAccountID(input.AccountID) ||
		!validIdempotencyKey(input.IdempotencyKey) {
		return Session{}, errors.New("invalid Stripe AccountSession request")
	}
	if err := provider.verifyPlatform(ctx); err != nil {
		return Session{}, err
	}
	params := &stripeapi.AccountSessionCreateParams{
		Account: stripeapi.String(input.AccountID),
		Components: &stripeapi.AccountSessionCreateComponentsParams{
			AccountManagement: &stripeapi.AccountSessionCreateComponentsAccountManagementParams{
				Enabled: stripeapi.Bool(true),
				Features: &stripeapi.AccountSessionCreateComponentsAccountManagementFeaturesParams{
					ExternalAccountCollection:       stripeapi.Bool(true),
					DisableStripeUserAuthentication: stripeapi.Bool(false),
				},
			},
			AccountOnboarding: &stripeapi.AccountSessionCreateComponentsAccountOnboardingParams{
				Enabled: stripeapi.Bool(true),
				Features: &stripeapi.AccountSessionCreateComponentsAccountOnboardingFeaturesParams{
					ExternalAccountCollection:       stripeapi.Bool(true),
					DisableStripeUserAuthentication: stripeapi.Bool(false),
				},
			},
		},
	}
	params.SetIdempotencyKey(input.IdempotencyKey)
	session, err := provider.client.V1AccountSessions.Create(ctx, params)
	if err != nil {
		return Session{}, fmt.Errorf("Stripe create AccountSession: %w", err)
	}
	if session == nil || session.Account != input.AccountID || session.Livemode != provider.liveMode {
		return Session{}, errors.New("Stripe AccountSession returned mismatched account or mode")
	}
	return Session{
		ClientSecret: session.ClientSecret,
		ExpiresAt:    time.Unix(session.ExpiresAt, 0).UTC(),
	}, nil
}

func (provider *ConnectProvider) CreateOnboardingLink(
	ctx context.Context,
	input OnboardingLinkInput,
) (Link, error) {
	if provider == nil || provider.client == nil || !validAccountID(input.AccountID) ||
		!validIdempotencyKey(input.IdempotencyKey) ||
		!validReturnURL(input.ReturnURL, provider.liveMode) ||
		!validReturnURL(input.RefreshURL, provider.liveMode) {
		return Link{}, errors.New("invalid Stripe Account Link request")
	}
	if err := provider.verifyPlatform(ctx); err != nil {
		return Link{}, err
	}
	params := &stripeapi.AccountLinkCreateParams{
		Account:    stripeapi.String(input.AccountID),
		ReturnURL:  stripeapi.String(input.ReturnURL),
		RefreshURL: stripeapi.String(input.RefreshURL),
		Type:       stripeapi.String(string(stripeapi.AccountLinkTypeAccountOnboarding)),
		CollectionOptions: &stripeapi.AccountLinkCreateCollectionOptionsParams{
			Fields:             stripeapi.String("eventually_due"),
			FutureRequirements: stripeapi.String("include"),
		},
	}
	params.SetIdempotencyKey(input.IdempotencyKey)
	link, err := provider.client.V1AccountLinks.Create(ctx, params)
	if err != nil {
		return Link{}, fmt.Errorf("Stripe create Account Link: %w", err)
	}
	if link == nil {
		return Link{}, errors.New("Stripe Account Link response is empty")
	}
	return Link{
		URL: link.URL, ExpiresAt: time.Unix(link.ExpiresAt, 0).UTC(),
	}, nil
}

func (provider *ConnectProvider) CreateDashboardLink(
	input DashboardLinkInput,
) (DashboardLink, error) {
	if provider == nil || !validAccountID(input.AccountID) {
		return DashboardLink{}, errors.New("invalid Stripe Dashboard link request")
	}
	path := "/" + input.AccountID + "/dashboard"
	if !provider.liveMode {
		path = "/test" + path
	}
	link := DashboardLink{URL: "https://dashboard.stripe.com" + path}
	if !validDashboardLink(link.URL, input.AccountID, provider.liveMode) {
		return DashboardLink{}, errors.New("invalid Stripe Dashboard link")
	}
	return link, nil
}

func (provider *ConnectProvider) verifyPlatform(ctx context.Context) error {
	provider.platformMu.Lock()
	defer provider.platformMu.Unlock()
	if provider.platformVerified {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	account, err := provider.client.V1Accounts.Retrieve(ctx, nil)
	if err != nil {
		return fmt.Errorf("Stripe retrieve platform Account: %w", err)
	}
	if account == nil || account.ID != provider.platformAccountID ||
		account.Country != provider.platformCountry || account.Deleted {
		return errors.New("Stripe platform Account identity does not match release configuration")
	}
	provider.platformVerified = true
	return nil
}

func providerAccount(account *stripeapi.Account) (Account, error) {
	if account == nil || account.Controller == nil || account.Controller.Fees == nil ||
		account.Controller.Losses == nil || account.Controller.StripeDashboard == nil {
		return Account{}, errors.New("Stripe Account controller projection is incomplete")
	}
	result := Account{
		ID:               account.ID,
		Metadata:         cloneMetadata(account.Metadata),
		Country:          account.Country,
		DefaultCurrency:  string(account.DefaultCurrency),
		DetailsSubmitted: account.DetailsSubmitted,
		ChargesEnabled:   account.ChargesEnabled,
		PayoutsEnabled:   account.PayoutsEnabled,
		Controller: ControllerSnapshot{
			LossesPayments:        string(account.Controller.Losses.Payments),
			FeesPayer:             string(account.Controller.Fees.Payer),
			RequirementCollection: string(account.Controller.RequirementCollection),
			DashboardType:         string(account.Controller.StripeDashboard.Type),
		},
		CardPaymentsStatus: CapabilityStatusUnrequested,
		TransfersStatus:    CapabilityStatusUnrequested,
	}
	if account.Capabilities != nil {
		result.CardPaymentsStatus = capabilityStatus(account.Capabilities.CardPayments)
		result.TransfersStatus = capabilityStatus(account.Capabilities.Transfers)
	}
	if account.Requirements != nil {
		result.Requirements.CurrentlyDue = len(account.Requirements.CurrentlyDue)
		result.Requirements.EventuallyDue = len(account.Requirements.EventuallyDue)
		result.Requirements.PastDue = len(account.Requirements.PastDue)
		result.Requirements.DisabledReason = string(account.Requirements.DisabledReason)
		if account.Requirements.CurrentDeadline > 0 {
			deadline := time.Unix(account.Requirements.CurrentDeadline, 0).UTC()
			result.Requirements.CurrentDeadline = &deadline
		}
	}
	return result, nil
}

func capabilityStatus(status stripeapi.AccountCapabilityStatus) CapabilityStatus {
	if status == "" {
		return CapabilityStatusUnrequested
	}
	return CapabilityStatus(status)
}

func validAccountID(value string) bool {
	return strings.HasPrefix(value, "acct_") && len(value) >= 13 && len(value) <= 260 &&
		strings.IndexFunc(value, func(character rune) bool {
			return (character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') && character != '_'
		}) == -1
}

func validCountry(value string) bool {
	return len(value) == 2 && value[0] >= 'A' && value[0] <= 'Z' &&
		value[1] >= 'A' && value[1] <= 'Z'
}

func connectSetupRequired(err error) bool {
	var stripeErr *stripeapi.Error
	return errors.As(err, &stripeErr) &&
		stripeErr.HTTPStatusCode == http.StatusBadRequest &&
		stripeErr.Type == stripeapi.ErrorTypeInvalidRequest &&
		stripeErr.Msg == connectSetupRequiredMessage
}

func validIdempotencyKey(value string) bool {
	return value != "" && len(value) <= 255 && strings.TrimSpace(value) == value
}

func validMetadata(key, value string) bool {
	return key != "" && len(key) <= 40 && len(value) <= 500 &&
		strings.TrimSpace(key) == key && strings.TrimSpace(value) == value
}

func validReturnURL(value string, liveMode bool) bool {
	if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawFragment != "" ||
		parsed.RawPath != "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return !liveMode && parsed.Scheme == "http" && loopbackHostname(parsed.Hostname())
}

func validDashboardLink(value, accountID string, liveMode bool) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "dashboard.stripe.com" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.RawFragment != "" || !validAccountID(accountID) {
		return false
	}
	wantPath := "/" + accountID + "/dashboard"
	if !liveMode {
		wantPath = "/test" + wantPath
	}
	return parsed.Path == wantPath && parsed.RawPath == ""
}

func loopbackHostname(hostname string) bool {
	hostname = strings.ToLower(hostname)
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return true
	}
	address := net.ParseIP(hostname)
	return address != nil && address.IsLoopback()
}

func metadataContains(actual, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
