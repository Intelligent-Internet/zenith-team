// Package commerce owns Company seller-revenue state: Stripe Connect account
// projections and acceptance of Boardly's versioned Commerce Addendum. It
// stores no bank details, KYC payloads, provider secrets, or ephemeral Stripe
// session credentials.
package commerce

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const controllerTemplateVersion int32 = 1

const maxProviderSecretBytes = 4096

var (
	ErrUnauthenticated            = errors.New("commerce authentication required")
	ErrForbidden                  = errors.New("commerce role permission required")
	ErrAccessForbidden            = errors.New("commerce company access required")
	ErrInvalidInput               = errors.New("invalid commerce input")
	ErrTermsUnavailable           = errors.New("Commerce Addendum is unavailable")
	ErrTermsOutdated              = errors.New("Commerce Addendum acceptance is outdated")
	ErrTermsConflict              = errors.New("Commerce Addendum receipt conflicts with configured document")
	ErrAccountConflict            = errors.New("commerce account conflicts with current state")
	ErrAccountRecovery            = errors.New("commerce account provisioning requires reconciliation")
	ErrProviderSetupRequired      = errors.New("Stripe Connect platform setup required")
	ErrProviderCredentialsInvalid = errors.New("commerce provider credentials invalid")
	ErrProviderPermissionRequired = errors.New("commerce provider permission required")
	ErrProviderRateLimited        = errors.New("commerce provider rate limited")
	ErrTaxSetupRequired           = errors.New("commerce tax setup required")
	ErrCheckoutTaxLocationInvalid = errors.New("commerce checkout tax location invalid")
	ErrProviderRequestInvalid     = errors.New("commerce provider request invalid")
	ErrProviderUnavailable        = errors.New("commerce provider unavailable")
	ErrInvalidProviderState       = errors.New("invalid commerce provider state")
	ErrInvalidStore               = errors.New("invalid commerce store data")

	termsVersionPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	providerIDPattern     = regexp.MustCompile(`^acct_[A-Za-z0-9]{8,255}$`)
	countryPattern        = regexp.MustCompile(`^[A-Z]{2}$`)
	currencyPattern       = regexp.MustCompile(`^[a-z]{3}$`)
	publishableKeyPattern = regexp.MustCompile(`^pk_(test|live)_[A-Za-z0-9_]{8,255}$`)
)

type Environment string

const (
	EnvironmentSandbox Environment = "sandbox"
	EnvironmentLive    Environment = "live"
)

func (environment Environment) valid() bool {
	return environment == EnvironmentSandbox || environment == EnvironmentLive
}

type Status string

const (
	StatusNotStarted     Status = "not_started"
	StatusProvisioning   Status = "provisioning"
	StatusOnboarding     Status = "onboarding"
	StatusPendingReview  Status = "pending_review"
	StatusReady          Status = "ready"
	StatusActionRequired Status = "action_required"
	StatusRestricted     Status = "restricted"
	StatusDraining       Status = "draining"
	StatusDisconnected   Status = "disconnected"
)

type CapabilityStatus string

const (
	CapabilityStatusActive      CapabilityStatus = "active"
	CapabilityStatusInactive    CapabilityStatus = "inactive"
	CapabilityStatusPending     CapabilityStatus = "pending"
	CapabilityStatusUnrequested CapabilityStatus = "unrequested"
)

func (status CapabilityStatus) valid() bool {
	return status == CapabilityStatusActive || status == CapabilityStatusInactive ||
		status == CapabilityStatusPending || status == CapabilityStatusUnrequested
}

type ControllerSnapshot struct {
	LossesPayments        string
	FeesPayer             string
	RequirementCollection string
	DashboardType         string
}

type ProviderRequirements struct {
	CurrentlyDue    int
	EventuallyDue   int
	PastDue         int
	CurrentDeadline *time.Time
	DisabledReason  string
}

type ProviderAccount struct {
	ID                 string
	Metadata           map[string]string
	Country            string
	DefaultCurrency    string
	DetailsSubmitted   bool
	ChargesEnabled     bool
	PayoutsEnabled     bool
	CardPaymentsStatus CapabilityStatus
	TransfersStatus    CapabilityStatus
	Requirements       ProviderRequirements
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
	Account    ProviderAccount
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

type ProviderSession struct {
	ClientSecret string
	ExpiresAt    time.Time
}

type ProviderLink struct {
	URL       string
	ExpiresAt time.Time
}

type ProviderDashboardLink struct {
	URL string
}

// Provider is consumed by commerce and implemented by the Stripe adapter.
// Each configured instance is bound to exactly one Stripe environment/key.
type Provider interface {
	CreateAccount(context.Context, CreateAccountInput) (ProviderAccount, error)
	FindAccount(context.Context, FindAccountInput) (FindAccountResult, error)
	RetrieveAccount(context.Context, string) (ProviderAccount, error)
	CreateAccountSession(context.Context, AccountSessionInput) (ProviderSession, error)
	CreateOnboardingLink(context.Context, OnboardingLinkInput) (ProviderLink, error)
	CreateDashboardLink(DashboardLinkInput) (ProviderDashboardLink, error)
}

type TermsDocument struct {
	Version     string
	SHA256      [sha256.Size]byte
	DocumentURL string
	PrivacyURL  string
}

type EnvironmentOptions struct {
	Configured     bool
	PublishableKey string
	ReturnURL      string
	RefreshURL     string
	Provider       Provider
	Gateway        GatewayProvider
	Webhook        WebhookProvider
}

type ServiceOptions struct {
	Store                  *db.Store
	Bus                    *events.Bus
	InboxPresenter         inbox.Presenter
	Terms                  TermsDocument
	Environments           map[Environment]EnvironmentOptions
	AllowedCountries       []string
	CatalogCheckoutEnabled bool
	SiteAuthority          SiteAuthority
	CustomerHMACKey        []byte
	ProviderCallTimeout    time.Duration
	Now                    func() time.Time
	Random                 io.Reader
}

// CommerceService owns seller Commerce setup, catalog publication, gateway
// operations, and webhook reconciliation for a Company.
type CommerceService interface { //nolint:revive // AGENTS.md mandates <Domain>Service; bare Service is forbidden.
	EnsureTermsCatalog(context.Context) error
	Summary(context.Context, auth.UserPrincipal) (Summary, error)
	IntegrationReadiness(context.Context, auth.UserPrincipal) (IntegrationReadiness, error)
	AcceptTerms(context.Context, auth.UserPrincipal, AcceptTermsInput) (AcceptanceResult, error)
	CreateAccountSession(context.Context, auth.UserPrincipal, Environment) (AccountSession, error)
	CreateOnboardingLink(context.Context, auth.UserPrincipal, Environment) (OnboardingLink, error)
	CreateDashboardLink(context.Context, auth.UserPrincipal, Environment) (DashboardLink, error)
	SubmitManifest(
		context.Context,
		auth.UserPrincipal,
		TrustedManifestContext,
		SubmitManifestInput,
	) (ManifestSubmission, error)
	ManifestStatus(context.Context, auth.UserPrincipal, ManifestStatusInput) (ManifestStatus, error)
	ListManifests(
		context.Context,
		auth.UserPrincipal,
		ManifestListOptions,
	) (ManifestPage, error)
	ApproveAndPublishManifest(
		context.Context,
		auth.UserPrincipal,
		ApproveManifestInput,
	) (ManifestStatus, error)
	CreateSiteChallenge(context.Context, SiteChallengeRequest) (SiteChallenge, error)
	RegisterLocalSigningKey(
		context.Context,
		auth.UserPrincipal,
		TrustedLocalSigningKeyContext,
		LocalSigningKeyRequest,
	) (LocalSigningKeyRegistration, error)
	SyncSigningKeys(
		context.Context,
		auth.UserPrincipal,
		SyncSigningKeysRequest,
	) ([]SigningKeySummary, error)
	RevokeSigningKey(
		context.Context,
		auth.UserPrincipal,
		RevokeSigningKeyRequest,
	) (SigningKeySummary, error)
	CreateCheckout(context.Context, SiteSignedRequest, CheckoutRequest) (HostedRedirect, error)
	CreatePortal(context.Context, SiteSignedRequest, PortalRequest) (HostedRedirect, error)
	VerifyEntitlements(context.Context, SiteSignedRequest) (EntitlementVerification, error)
	SessionJWKSet() (SessionJWKSet, error)
	ProcessWebhook(context.Context, []byte, string) (WebhookReceiptResult, error)
	ReconcileWebhookReceipts(context.Context, int32) (WebhookReconciliationResult, error)
}

var _ CommerceService = (*DefaultCommerceService)(nil)

type DefaultCommerceService struct {
	store                  *db.Store
	bus                    *events.Bus
	inboxPresenter         inbox.Presenter
	terms                  TermsDocument
	environments           map[Environment]EnvironmentOptions
	allowedCountries       map[string]struct{}
	catalogCheckoutEnabled bool
	siteAuthority          SiteAuthority
	customerHMACKey        []byte
	sessionAssertionSigner *sessionAssertionSigner
	random                 io.Reader
	providerCallTimeout    time.Duration
	now                    func() time.Time
}

type TermsState struct {
	CurrentVersion string     `json:"currentVersion"`
	Digest         string     `json:"digest"`
	DocumentURL    string     `json:"documentUrl"`
	PrivacyURL     string     `json:"privacyUrl"`
	Accepted       bool       `json:"accepted"`
	AcceptedAt     *time.Time `json:"acceptedAt"`
}

type RequirementSummary struct {
	CurrentlyDue    int32      `json:"currentlyDue"`
	EventuallyDue   int32      `json:"eventuallyDue"`
	PastDue         int32      `json:"pastDue"`
	CurrentDeadline *time.Time `json:"currentDeadline"`
}

type Account struct {
	Environment      Environment        `json:"environment"`
	Configured       bool               `json:"configured"`
	PublishableKey   string             `json:"publishableKey,omitempty"`
	Status           Status             `json:"status"`
	ChargesEnabled   bool               `json:"chargesEnabled"`
	PayoutsEnabled   bool               `json:"payoutsEnabled"`
	ActionRequired   bool               `json:"actionRequired"`
	Country          *string            `json:"country"`
	DefaultCurrency  *string            `json:"defaultCurrency"`
	Requirements     RequirementSummary `json:"requirements"`
	SyncStatus       string             `json:"syncStatus"`
	SyncErrorCode    *string            `json:"syncErrorCode,omitempty"`
	LastReconciledAt *time.Time         `json:"lastReconciledAt"`
	RowVersion       int64              `json:"rowVersion"`
}

type Summary struct {
	CompanyID uuid.UUID  `json:"companyId"`
	Terms     TermsState `json:"terms"`
	Accounts  []Account  `json:"accounts"`
}

// IntegrationAccount is the provider-identity-free readiness projection a
// Task Run may inspect before preparing a website Commerce manifest.
type IntegrationAccount struct {
	Environment      Environment        `json:"environment"`
	Configured       bool               `json:"configured"`
	Status           Status             `json:"status"`
	ManifestReady    bool               `json:"manifestReady"`
	ChargesEnabled   bool               `json:"chargesEnabled"`
	PayoutsEnabled   bool               `json:"payoutsEnabled"`
	ActionRequired   bool               `json:"actionRequired"`
	Country          *string            `json:"country"`
	Requirements     RequirementSummary `json:"requirements"`
	SyncStatus       string             `json:"syncStatus"`
	LastReconciledAt *time.Time         `json:"lastReconciledAt"`
}

// IntegrationReadiness contains only the facts an authenticated Company
// member needs to decide whether a Task can submit a Commerce manifest. It
// deliberately excludes Stripe IDs, publishable keys, and legal documents.
type IntegrationReadiness struct {
	TermsAccepted bool                 `json:"termsAccepted"`
	Accounts      []IntegrationAccount `json:"accounts"`
}

type AcceptTermsInput struct {
	TermsVersion string
	Accepted     bool
}

type Acceptance struct {
	TermsVersion string    `json:"termsVersion"`
	AcceptedAt   time.Time `json:"acceptedAt"`
}

type AcceptanceResult struct {
	Acceptance Acceptance `json:"acceptance"`
	Created    bool       `json:"-"`
}

type AccountSession struct {
	PublishableKey string    `json:"publishableKey"`
	ClientSecret   string    `json:"clientSecret"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

type OnboardingLink struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type DashboardLink struct {
	URL string `json:"url"`
}

func NewCommerceService(options ServiceOptions) (*DefaultCommerceService, error) {
	if options.Store == nil {
		return nil, errors.New("Commerce database store is required")
	}
	if err := validateTermsDocument(options.Terms); err != nil {
		return nil, err
	}
	environments := make(map[Environment]EnvironmentOptions, 2)
	for _, environment := range []Environment{EnvironmentSandbox, EnvironmentLive} {
		configuration := options.Environments[environment]
		if err := validateEnvironmentOptions(environment, configuration); err != nil {
			return nil, err
		}
		environments[environment] = configuration
	}
	hasGateway := false
	hasCatalogGateway := false
	for _, configuration := range environments {
		hasGateway = hasGateway || configuration.Gateway != nil || configuration.Webhook != nil
		hasCatalogGateway = hasCatalogGateway || configuration.Gateway != nil
	}
	if hasGateway && (options.SiteAuthority == nil || len(options.CustomerHMACKey) < 32) {
		return nil, errors.New(
			"Commerce gateway requires Site authority and a 32-byte customer HMAC key",
		)
	}
	if hasCatalogGateway && options.Bus == nil {
		return nil, errors.New("Commerce catalog gateway requires an event bus")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	var assertionSigner *sessionAssertionSigner
	if len(options.CustomerHMACKey) >= 32 {
		var signerErr error
		assertionSigner, signerErr = newSessionAssertionSigner(options.CustomerHMACKey, options.Now)
		if signerErr != nil {
			return nil, signerErr
		}
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	allowedCountries, err := validateAllowedCountries(options.AllowedCountries)
	if err != nil {
		return nil, err
	}
	providerCallTimeout := options.ProviderCallTimeout
	if providerCallTimeout == 0 {
		providerCallTimeout = defaultAccountProviderCallTimeout
	}
	if providerCallTimeout < minAccountProviderCallTimeout ||
		providerCallTimeout > maxAccountProviderCallTimeout ||
		providerCallTimeout%time.Millisecond != 0 {
		return nil, errors.New("Commerce provider call timeout must be whole milliseconds from 1s to 30s")
	}
	return &DefaultCommerceService{
		store: options.Store, bus: options.Bus, inboxPresenter: options.InboxPresenter,
		terms:        options.Terms,
		environments: environments, allowedCountries: allowedCountries,
		catalogCheckoutEnabled: options.CatalogCheckoutEnabled,
		siteAuthority:          options.SiteAuthority,
		customerHMACKey:        append([]byte(nil), options.CustomerHMACKey...),
		sessionAssertionSigner: assertionSigner,
		random:                 options.Random,
		providerCallTimeout:    providerCallTimeout, now: options.Now,
	}, nil
}

// EnsureTermsCatalog pins the configured legal document digest to its version
// before this process may serve Commerce traffic. The insert-only catalog is
// the cluster authority, so an exact replay succeeds while a replica with a
// different digest for an existing version fails closed.
func (s *DefaultCommerceService) EnsureTermsCatalog(ctx context.Context) error {
	if ctx == nil {
		return ErrTermsUnavailable
	}
	return s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		return s.ensureTermsCatalog(ctx, queries)
	})
}

func (s *DefaultCommerceService) Summary(ctx context.Context, principal auth.UserPrincipal) (Summary, error) {
	if err := validatePrincipalPermission(principal, auth.PermissionCommerceRead); err != nil {
		return Summary{}, err
	}
	queries := s.store.Queries()
	if err := authorizeRead(ctx, queries, principal, auth.PermissionCommerceRead); err != nil {
		return Summary{}, err
	}
	terms, err := s.termsState(ctx, principal.CompanyID)
	if err != nil {
		return Summary{}, err
	}
	rows, err := queries.ListCommerceAccountsForOwner(
		ctx,
		generated.ListCommerceAccountsForOwnerParams{
			ActorRoles:  auth.RolesWith(auth.PermissionCommerceRead),
			ActorUserID: principal.UserID,
			SessionID:   principal.SessionID,
			CompanyID:   principal.CompanyID,
		},
	)
	if err != nil {
		return Summary{}, fmt.Errorf("list Commerce accounts: %w", err)
	}
	byEnvironment := make(map[Environment]generated.CommerceAccount, len(rows))
	for _, row := range rows {
		environment := Environment(row.Environment)
		if !environment.valid() {
			return Summary{}, ErrInvalidStore
		}
		byEnvironment[environment] = row
	}

	result := Summary{
		CompanyID: principal.CompanyID, Terms: terms,
		Accounts: make([]Account, 0, 2),
	}
	for _, environment := range []Environment{EnvironmentSandbox, EnvironmentLive} {
		configuration := s.environments[environment]
		row, exists := byEnvironment[environment]
		if !exists {
			result.Accounts = append(result.Accounts, Account{
				Environment: environment, Configured: configuration.Configured,
				PublishableKey: configuration.PublishableKey, Status: StatusNotStarted,
				SyncStatus: "not_started",
			})
			continue
		}
		row, syncErrorCode, err := s.reconcileForSummary(ctx, principal, row, configuration)
		if err != nil {
			return Summary{}, err
		}
		account := accountFromRow(row, configuration)
		if syncErrorCode != "" {
			account.SyncStatus = "stale"
			account.SyncErrorCode = &syncErrorCode
			if account.Status == StatusReady {
				account.Status = StatusPendingReview
				account.ActionRequired = false
			}
		}
		if !terms.Accepted && account.Status == StatusReady {
			account.Status = StatusActionRequired
			account.ActionRequired = true
		}
		result.Accounts = append(result.Accounts, account)
	}
	if err := authorizeRead(ctx, queries, principal, auth.PermissionCommerceRead); err != nil {
		return Summary{}, err
	}
	return result, nil
}

func (s *DefaultCommerceService) IntegrationReadiness(
	ctx context.Context,
	principal auth.UserPrincipal,
) (IntegrationReadiness, error) {
	if err := validateMemberPrincipal(principal); err != nil {
		return IntegrationReadiness{}, err
	}
	if err := authorizeMemberRead(ctx, s.store.Queries(), principal); err != nil {
		return IntegrationReadiness{}, err
	}
	terms, err := s.termsState(ctx, principal.CompanyID)
	if err != nil {
		return IntegrationReadiness{}, err
	}

	result := IntegrationReadiness{
		TermsAccepted: terms.Accepted,
		Accounts:      make([]IntegrationAccount, 0, 2),
	}
	for _, environment := range []Environment{EnvironmentSandbox, EnvironmentLive} {
		configuration := s.environments[environment]
		row, rowErr := s.store.Queries().GetCommerceAccountByCompanyEnvironment(
			ctx,
			generated.GetCommerceAccountByCompanyEnvironmentParams{
				CompanyID: principal.CompanyID, Environment: string(environment),
			},
		)
		if errors.Is(rowErr, pgx.ErrNoRows) {
			result.Accounts = append(result.Accounts, IntegrationAccount{
				Environment: environment,
				Configured:  configuration.Configured,
				Status:      StatusNotStarted,
				SyncStatus:  "not_started",
			})
			continue
		}
		if rowErr != nil {
			return IntegrationReadiness{}, fmt.Errorf(
				"get Commerce integration account: %w",
				rowErr,
			)
		}
		account := accountFromRow(row, configuration)
		result.Accounts = append(
			result.Accounts,
			integrationAccountFromAccount(
				account,
				terms.Accepted,
				s.catalogCheckoutEnabled && configuration.Gateway != nil,
				s.allowedCountries,
			),
		)
	}
	if err := authorizeMemberRead(ctx, s.store.Queries(), principal); err != nil {
		return IntegrationReadiness{}, err
	}
	return result, nil
}

func integrationAccountFromAccount(
	account Account,
	termsAccepted bool,
	gatewayEnabled bool,
	allowedCountries map[string]struct{},
) IntegrationAccount {
	countryAllowed := false
	if account.Country != nil {
		_, countryAllowed = allowedCountries[*account.Country]
	}
	return IntegrationAccount{
		Environment: account.Environment, Configured: account.Configured,
		Status: account.Status,
		ManifestReady: gatewayEnabled && account.Configured && termsAccepted &&
			account.Status == StatusReady && account.ChargesEnabled && account.PayoutsEnabled &&
			countryAllowed && account.SyncStatus == "current",
		ChargesEnabled: account.ChargesEnabled, PayoutsEnabled: account.PayoutsEnabled,
		ActionRequired: account.ActionRequired, Country: account.Country,
		Requirements: account.Requirements, SyncStatus: account.SyncStatus,
		LastReconciledAt: account.LastReconciledAt,
	}
}

func (s *DefaultCommerceService) AcceptTerms(
	ctx context.Context,
	principal auth.UserPrincipal,
	input AcceptTermsInput,
) (AcceptanceResult, error) {
	if err := validatePrincipalPermission(principal, auth.PermissionCommerceManage); err != nil {
		return AcceptanceResult{}, err
	}
	if !input.Accepted || input.TermsVersion != s.terms.Version {
		if termsVersionPattern.MatchString(input.TermsVersion) && input.Accepted {
			return AcceptanceResult{}, ErrTermsOutdated
		}
		return AcceptanceResult{}, ErrInvalidInput
	}
	var result AcceptanceResult
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		if err := lockManagingMember(ctx, queries, principal); err != nil {
			return err
		}
		if err := s.ensureTermsCatalog(ctx, queries); err != nil {
			return err
		}
		row, insertErr := queries.InsertCommerceTermsAcceptance(
			ctx,
			generated.InsertCommerceTermsAcceptanceParams{
				CompanyID: principal.CompanyID, TermsVersion: s.terms.Version,
				TermsSha256:      s.terms.SHA256[:],
				AcceptedByUserID: principal.UserID,
			},
		)
		if errors.Is(insertErr, pgx.ErrNoRows) {
			row, insertErr = queries.GetCommerceTermsAcceptance(
				ctx,
				generated.GetCommerceTermsAcceptanceParams{
					CompanyID: principal.CompanyID, TermsVersion: s.terms.Version,
				},
			)
			result.Created = false
		} else {
			result.Created = insertErr == nil
		}
		if insertErr != nil {
			return fmt.Errorf("accept Commerce terms: %w", insertErr)
		}
		if !equalDigest(row.TermsSha256, s.terms.SHA256) {
			return ErrTermsConflict
		}
		result.Acceptance = Acceptance{
			TermsVersion: row.TermsVersion,
			AcceptedAt:   row.AcceptedAt.Time.UTC(),
		}
		return nil
	})
	if err != nil {
		return AcceptanceResult{}, err
	}
	return result, nil
}

func (s *DefaultCommerceService) ensureTermsCatalog(
	ctx context.Context,
	queries *generated.Queries,
) error {
	row, err := queries.InsertCommerceTermsCatalog(
		ctx,
		generated.InsertCommerceTermsCatalogParams{
			TermsVersion: s.terms.Version,
			TermsSha256:  s.terms.SHA256[:],
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = queries.GetCommerceTermsCatalog(ctx, s.terms.Version)
	}
	if err != nil {
		return fmt.Errorf("ensure Commerce terms catalog: %w", err)
	}
	if row.TermsVersion != s.terms.Version || !equalDigest(row.TermsSha256, s.terms.SHA256) {
		return ErrTermsConflict
	}
	return nil
}

func (s *DefaultCommerceService) CreateAccountSession(
	ctx context.Context,
	principal auth.UserPrincipal,
	environment Environment,
) (AccountSession, error) {
	account, configuration, err := s.ensureAccount(ctx, principal, environment)
	if err != nil {
		return AccountSession{}, err
	}
	if err := s.authorizeAccountRelease(ctx, principal, account); err != nil {
		return AccountSession{}, err
	}
	requestID, err := uuid.NewV7()
	if err != nil {
		return AccountSession{}, errors.New("generate Commerce AccountSession request ID")
	}
	session, err := configuration.Provider.CreateAccountSession(ctx, AccountSessionInput{
		AccountID:      account.ProviderAccountID.String,
		IdempotencyKey: "boardly:connect-account-session:v1:" + requestID.String(),
	})
	if err != nil {
		return AccountSession{}, providerError("create Stripe AccountSession", err)
	}
	if !validEphemeralSecret(session.ClientSecret) || !validFutureExpiry(session.ExpiresAt, s.now()) {
		return AccountSession{}, ErrInvalidProviderState
	}
	if err := s.authorizeAccountRelease(ctx, principal, account); err != nil {
		return AccountSession{}, err
	}
	return AccountSession{
		PublishableKey: configuration.PublishableKey,
		ClientSecret:   session.ClientSecret,
		ExpiresAt:      session.ExpiresAt.UTC(),
	}, nil
}

func (s *DefaultCommerceService) CreateOnboardingLink(
	ctx context.Context,
	principal auth.UserPrincipal,
	environment Environment,
) (OnboardingLink, error) {
	account, configuration, err := s.ensureAccount(ctx, principal, environment)
	if err != nil {
		return OnboardingLink{}, err
	}
	if err := s.authorizeAccountRelease(ctx, principal, account); err != nil {
		return OnboardingLink{}, err
	}
	requestID, err := uuid.NewV7()
	if err != nil {
		return OnboardingLink{}, errors.New("generate Commerce Account Link request ID")
	}
	link, err := configuration.Provider.CreateOnboardingLink(ctx, OnboardingLinkInput{
		AccountID:      account.ProviderAccountID.String,
		IdempotencyKey: "boardly:connect-account-link:v1:" + requestID.String(),
		ReturnURL:      configuration.ReturnURL, RefreshURL: configuration.RefreshURL,
	})
	if err != nil {
		return OnboardingLink{}, providerError("create Stripe Account Link", err)
	}
	if !validStripeConnectURL(link.URL) || !validFutureExpiry(link.ExpiresAt, s.now()) {
		return OnboardingLink{}, ErrInvalidProviderState
	}
	if err := s.authorizeAccountRelease(ctx, principal, account); err != nil {
		return OnboardingLink{}, err
	}
	return OnboardingLink{URL: link.URL, ExpiresAt: link.ExpiresAt.UTC()}, nil
}

func (s *DefaultCommerceService) CreateDashboardLink(
	ctx context.Context,
	principal auth.UserPrincipal,
	environment Environment,
) (DashboardLink, error) {
	account, configuration, err := s.dashboardAccountForOwner(ctx, principal, environment)
	if err != nil {
		return DashboardLink{}, err
	}
	link, err := configuration.Provider.CreateDashboardLink(DashboardLinkInput{
		AccountID: account.ProviderAccountID.String,
	})
	if err != nil {
		return DashboardLink{}, providerError("create Stripe Dashboard link", err)
	}
	if !validStripeDashboardURL(link.URL, environment, account.ProviderAccountID.String) {
		return DashboardLink{}, ErrInvalidProviderState
	}
	current, _, err := s.dashboardAccountForOwner(ctx, principal, environment)
	if err != nil {
		return DashboardLink{}, err
	}
	if current.ID != account.ID || current.ProviderAccountID.String != account.ProviderAccountID.String {
		return DashboardLink{}, ErrAccountConflict
	}
	return DashboardLink(link), nil
}

func (s *DefaultCommerceService) dashboardAccountForOwner(
	ctx context.Context,
	principal auth.UserPrincipal,
	environment Environment,
) (generated.CommerceAccount, EnvironmentOptions, error) {
	if err := validatePrincipalPermission(principal, auth.PermissionCommerceManage); err != nil {
		return generated.CommerceAccount{}, EnvironmentOptions{}, err
	}
	if !environment.valid() {
		return generated.CommerceAccount{}, EnvironmentOptions{}, ErrInvalidInput
	}
	configuration := s.environments[environment]
	if !configuration.Configured || configuration.Provider == nil {
		return generated.CommerceAccount{}, EnvironmentOptions{}, ErrProviderUnavailable
	}
	accepted, err := s.currentTermsAccepted(ctx, principal)
	if err != nil {
		return generated.CommerceAccount{}, EnvironmentOptions{}, err
	}
	if !accepted {
		return generated.CommerceAccount{}, EnvironmentOptions{}, ErrTermsOutdated
	}
	account, err := s.store.Queries().GetCommerceAccountForOwner(
		ctx,
		generated.GetCommerceAccountForOwnerParams{
			ActorUserID: principal.UserID, SessionID: principal.SessionID,
			CompanyID: principal.CompanyID, Environment: string(environment),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.CommerceAccount{}, EnvironmentOptions{}, ErrProviderSetupRequired
	}
	if err != nil {
		return generated.CommerceAccount{}, EnvironmentOptions{}, fmt.Errorf(
			"authorize Commerce Dashboard link: %w",
			err,
		)
	}
	if validateReadyCommerceAccount(account, s.allowedCountries) != nil ||
		!account.PayoutsEnabled || account.ControllerDashboardType.String != "full" ||
		account.DisconnectedAt.Valid {
		return generated.CommerceAccount{}, EnvironmentOptions{}, ErrProviderSetupRequired
	}
	return account, configuration, nil
}

func (s *DefaultCommerceService) termsState(
	ctx context.Context,
	companyID uuid.UUID,
) (TermsState, error) {
	state := TermsState{
		CurrentVersion: s.terms.Version, Digest: hex.EncodeToString(s.terms.SHA256[:]),
		DocumentURL: s.terms.DocumentURL, PrivacyURL: s.terms.PrivacyURL,
	}
	row, err := s.store.Queries().GetCommerceTermsAcceptance(
		ctx,
		generated.GetCommerceTermsAcceptanceParams{
			CompanyID: companyID, TermsVersion: s.terms.Version,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return TermsState{}, fmt.Errorf("get Commerce terms acceptance: %w", err)
	}
	if !equalDigest(row.TermsSha256, s.terms.SHA256) {
		return TermsState{}, ErrTermsConflict
	}
	acceptedAt := row.AcceptedAt.Time.UTC()
	state.Accepted = true
	state.AcceptedAt = &acceptedAt
	return state, nil
}

func (s *DefaultCommerceService) currentTermsAccepted(
	ctx context.Context,
	principal auth.UserPrincipal,
) (bool, error) {
	if err := authorizeRead(ctx, s.store.Queries(), principal, auth.PermissionCommerceManage); err != nil {
		return false, err
	}
	row, err := s.store.Queries().GetCommerceTermsAcceptance(
		ctx,
		generated.GetCommerceTermsAcceptanceParams{
			CompanyID: principal.CompanyID, TermsVersion: s.terms.Version,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get Commerce terms acceptance: %w", err)
	}
	if !equalDigest(row.TermsSha256, s.terms.SHA256) {
		return false, ErrTermsConflict
	}
	return true, nil
}

func (s *DefaultCommerceService) persistReconciledProjection(
	ctx context.Context,
	principal auth.UserPrincipal,
	current generated.CommerceAccount,
	providerAccount ProviderAccount,
) (generated.CommerceAccount, error) {
	if err := validateProviderAccount(providerAccount); err != nil {
		return generated.CommerceAccount{}, err
	}
	if !current.ProviderAccountID.Valid || current.ProviderAccountID.String != providerAccount.ID {
		return generated.CommerceAccount{}, ErrInvalidProviderState
	}
	projection := projectionParams(providerAccount, s.allowedCountries)
	var updated generated.CommerceAccount
	transactionErr := s.store.WithinTx(
		ctx,
		pgx.TxOptions{},
		func(queries *generated.Queries) error {
			if err := lockManagingMember(ctx, queries, principal); err != nil {
				return err
			}
			var updateErr error
			updated, updateErr = queries.UpdateCommerceAccountProjection(
				ctx,
				generated.UpdateCommerceAccountProjectionParams{
					Status:             string(projection.status),
					Country:            projection.country,
					DefaultCurrency:    projection.defaultCurrency,
					DetailsSubmitted:   providerAccount.DetailsSubmitted,
					ChargesEnabled:     providerAccount.ChargesEnabled,
					PayoutsEnabled:     providerAccount.PayoutsEnabled,
					CardPaymentsStatus: projection.cardPaymentsStatus,
					TransfersStatus:    projection.transfersStatus,
					RequirementsCurrentlyDueCount: int32(
						providerAccount.Requirements.CurrentlyDue,
					),
					RequirementsEventuallyDueCount: int32(
						providerAccount.Requirements.EventuallyDue,
					),
					RequirementsPastDueCount: int32(providerAccount.Requirements.PastDue),
					RequirementsDeadline:     optionalTimestamp(projection.deadline),
					DisabledReason:           projection.disabledReason,
					CompanyID:                principal.CompanyID,
					AccountID:                current.ID,
					Environment:              current.Environment,
					ProviderAccountID:        providerAccount.ID,
					ExpectedVersion:          current.Version,
				},
			)
			if errors.Is(updateErr, pgx.ErrNoRows) {
				updated, updateErr = queries.GetCommerceAccountForUpdate(
					ctx,
					generated.GetCommerceAccountForUpdateParams{
						CompanyID: principal.CompanyID, Environment: current.Environment,
					},
				)
				if updateErr == nil && exactProviderProjectionMatches(
					updated,
					current,
					providerAccount,
					projection,
				) {
					return nil
				}
				if updateErr == nil {
					return ErrAccountConflict
				}
			}
			if updateErr != nil {
				return fmt.Errorf("persist Commerce account projection: %w", updateErr)
			}
			return nil
		},
	)
	if errors.Is(transactionErr, db.ErrCommitUnknown) {
		recoveryCtx, cancel := boundedFinalizationContext(ctx)
		defer cancel()
		verified, verifyErr := s.store.Queries().GetCommerceAccountForOwner(
			recoveryCtx,
			generated.GetCommerceAccountForOwnerParams{
				ActorUserID: principal.UserID,
				SessionID:   principal.SessionID,
				CompanyID:   principal.CompanyID,
				Environment: current.Environment,
			},
		)
		if errors.Is(verifyErr, pgx.ErrNoRows) {
			return generated.CommerceAccount{}, ErrAccessForbidden
		}
		if verifyErr != nil || !exactProviderProjectionMatches(
			verified,
			current,
			providerAccount,
			projection,
		) {
			return generated.CommerceAccount{}, errors.Join(transactionErr, verifyErr)
		}
		updated = verified
	} else if transactionErr != nil {
		return generated.CommerceAccount{}, transactionErr
	}
	return updated, nil
}

func (s *DefaultCommerceService) authorizeAccountRelease(
	ctx context.Context,
	principal auth.UserPrincipal,
	account generated.CommerceAccount,
) error {
	accepted, err := s.currentTermsAccepted(ctx, principal)
	if err != nil {
		return err
	}
	if !accepted {
		return ErrTermsOutdated
	}
	current, err := s.store.Queries().GetCommerceAccountForOwner(
		ctx,
		generated.GetCommerceAccountForOwnerParams{
			ActorUserID: principal.UserID,
			SessionID:   principal.SessionID,
			CompanyID:   principal.CompanyID,
			Environment: account.Environment,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAccessForbidden
	}
	if err != nil {
		return fmt.Errorf("authorize Commerce account credential release: %w", err)
	}
	if current.ID != account.ID || !current.ProviderAccountID.Valid ||
		!account.ProviderAccountID.Valid ||
		current.ProviderAccountID.String != account.ProviderAccountID.String ||
		current.Status == string(StatusDraining) || current.Status == string(StatusDisconnected) {
		return ErrAccountConflict
	}
	return nil
}

func (s *DefaultCommerceService) reconcileForSummary(
	ctx context.Context,
	principal auth.UserPrincipal,
	row generated.CommerceAccount,
	configuration EnvironmentOptions,
) (generated.CommerceAccount, string, error) {
	if !row.ProviderAccountID.Valid {
		return row, "", nil
	}
	if !configuration.Configured || configuration.Provider == nil {
		current, authorizeErr := s.authorizedSummaryRow(ctx, principal, row)
		return current, "provider_unavailable", authorizeErr
	}
	operation, err := s.store.Queries().GetCommerceAccountProvisioningOperation(
		ctx,
		generated.GetCommerceAccountProvisioningOperationParams{
			CompanyID: row.CompanyID, AccountID: row.ID, Environment: row.Environment,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		current, authorizeErr := s.authorizedSummaryRow(ctx, principal, row)
		return current, "provider_state_invalid", authorizeErr
	}
	if err != nil {
		return generated.CommerceAccount{}, "",
			fmt.Errorf("get Commerce operation for summary reconciliation: %w", err)
	}
	intent := accountProvisioningIntentFromOperation(operation)
	if operation.State != operationStateSucceeded ||
		!operation.ProviderResourceID.Valid ||
		operation.ProviderResourceID.String != row.ProviderAccountID.String {
		current, authorizeErr := s.authorizedSummaryRow(ctx, principal, row)
		return current, "provider_state_invalid", authorizeErr
	}
	providerAccount, err := configuration.Provider.RetrieveAccount(ctx, row.ProviderAccountID.String)
	if err != nil {
		current, authorizeErr := s.authorizedSummaryRow(ctx, principal, row)
		return current, "provider_unavailable", authorizeErr
	}
	if providerAccount.ID != row.ProviderAccountID.String ||
		validateProviderAccountBinding(providerAccount, intent) != nil {
		current, authorizeErr := s.authorizedSummaryRow(ctx, principal, row)
		return current, "provider_state_invalid", authorizeErr
	}
	updated, err := s.persistReconciledProjection(ctx, principal, row, providerAccount)
	if errors.Is(err, ErrInvalidProviderState) {
		current, authorizeErr := s.authorizedSummaryRow(ctx, principal, row)
		return current, "provider_state_invalid", authorizeErr
	}
	if err != nil {
		return generated.CommerceAccount{}, "", err
	}
	return updated, "", nil
}

func (s *DefaultCommerceService) authorizedSummaryRow(
	ctx context.Context,
	principal auth.UserPrincipal,
	expected generated.CommerceAccount,
) (generated.CommerceAccount, error) {
	current, err := s.store.Queries().GetCommerceAccountForOwner(
		ctx,
		generated.GetCommerceAccountForOwnerParams{
			ActorUserID: principal.UserID,
			SessionID:   principal.SessionID,
			CompanyID:   principal.CompanyID,
			Environment: expected.Environment,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.CommerceAccount{}, ErrAccessForbidden
	}
	if err != nil {
		return generated.CommerceAccount{}, fmt.Errorf("reauthorize Commerce summary: %w", err)
	}
	if current.ID != expected.ID || current.CompanyID != principal.CompanyID {
		return generated.CommerceAccount{}, ErrAccountConflict
	}
	return current, nil
}

type providerProjection struct {
	status             Status
	country            pgtype.Text
	defaultCurrency    pgtype.Text
	cardPaymentsStatus pgtype.Text
	transfersStatus    pgtype.Text
	deadline           *time.Time
	disabledReason     pgtype.Text
}

func projectionParams(
	account ProviderAccount,
	allowedCountries map[string]struct{},
) providerProjection {
	return providerProjection{
		status: statusFromProviderAccount(account, allowedCountries), country: optionalText(account.Country),
		defaultCurrency:    optionalText(account.DefaultCurrency),
		cardPaymentsStatus: optionalText(string(account.CardPaymentsStatus)),
		transfersStatus:    optionalText(string(account.TransfersStatus)),
		deadline:           utcTimePointer(account.Requirements.CurrentDeadline),
		disabledReason:     optionalText(account.Requirements.DisabledReason),
	}
}

func statusFromProviderAccount(
	account ProviderAccount,
	allowedCountries map[string]struct{},
) Status {
	if strings.HasPrefix(account.Requirements.DisabledReason, "rejected.") {
		return StatusRestricted
	}
	_, countryAllowed := allowedCountries[account.Country]
	if account.Country != "" && !countryAllowed {
		return StatusRestricted
	}
	if account.Requirements.CurrentlyDue > 0 || account.Requirements.PastDue > 0 ||
		account.Requirements.DisabledReason != "" {
		return StatusActionRequired
	}
	if account.ChargesEnabled && account.PayoutsEnabled &&
		account.CardPaymentsStatus == CapabilityStatusActive && countryAllowed {
		return StatusReady
	}
	if account.DetailsSubmitted || account.CardPaymentsStatus == CapabilityStatusPending ||
		account.TransfersStatus == CapabilityStatusPending {
		return StatusPendingReview
	}
	return StatusOnboarding
}

func accountFromRow(row generated.CommerceAccount, configuration EnvironmentOptions) Account {
	environment := Environment(row.Environment)
	status := Status(row.Status)
	publicStatus := status
	if status == StatusProvisioning {
		publicStatus = StatusOnboarding
	}
	account := Account{
		Environment: environment, Configured: configuration.Configured,
		PublishableKey: configuration.PublishableKey, Status: publicStatus,
		ChargesEnabled: row.ChargesEnabled, PayoutsEnabled: row.PayoutsEnabled,
		ActionRequired: publicStatus == StatusActionRequired || publicStatus == StatusRestricted,
		Country:        textPointer(row.Country), DefaultCurrency: textPointer(row.DefaultCurrency),
		Requirements: RequirementSummary{
			CurrentlyDue:    row.RequirementsCurrentlyDueCount,
			EventuallyDue:   row.RequirementsEventuallyDueCount,
			PastDue:         row.RequirementsPastDueCount,
			CurrentDeadline: utcTimePointer(row.RequirementsDeadline),
		},
		SyncStatus: "pending", LastReconciledAt: utcTimePointer(row.LastReconciledAt),
		RowVersion: row.Version,
	}
	if row.ProviderAccountID.Valid {
		account.SyncStatus = "current"
	}
	return account
}

func validateProviderAccount(account ProviderAccount) error {
	if !providerIDPattern.MatchString(account.ID) ||
		account.Controller.LossesPayments != "stripe" ||
		account.Controller.FeesPayer != "account" ||
		account.Controller.RequirementCollection != "stripe" ||
		account.Controller.DashboardType != "full" ||
		(account.Country != "" && !countryPattern.MatchString(account.Country)) ||
		(account.DefaultCurrency != "" && !currencyPattern.MatchString(account.DefaultCurrency)) ||
		!account.CardPaymentsStatus.valid() || !account.TransfersStatus.valid() ||
		account.Requirements.CurrentlyDue < 0 || account.Requirements.CurrentlyDue > 1024 ||
		account.Requirements.EventuallyDue < 0 || account.Requirements.EventuallyDue > 1024 ||
		account.Requirements.PastDue < 0 || account.Requirements.PastDue > 1024 ||
		len(account.Requirements.DisabledReason) > 255 ||
		strings.TrimSpace(account.Requirements.DisabledReason) != account.Requirements.DisabledReason {
		return ErrInvalidProviderState
	}
	if account.Requirements.CurrentDeadline != nil && account.Requirements.CurrentDeadline.IsZero() {
		return ErrInvalidProviderState
	}
	return nil
}

func validateTermsDocument(document TermsDocument) error {
	if !termsVersionPattern.MatchString(document.Version) || document.SHA256 == [sha256.Size]byte{} ||
		!validHTTPSURL(document.DocumentURL) || !validHTTPSURL(document.PrivacyURL) {
		return errors.New("Commerce Addendum configuration is invalid")
	}
	return nil
}

func validateAllowedCountries(countries []string) (map[string]struct{}, error) {
	if len(countries) == 0 || len(countries) > 64 {
		return nil, errors.New("Commerce connected-account country policy is invalid")
	}
	result := make(map[string]struct{}, len(countries))
	for _, country := range countries {
		if !countryPattern.MatchString(country) {
			return nil, errors.New("Commerce connected-account country policy is invalid")
		}
		if _, exists := result[country]; exists {
			return nil, errors.New("Commerce connected-account country policy has duplicates")
		}
		result[country] = struct{}{}
	}
	return result, nil
}

func validateEnvironmentOptions(environment Environment, configuration EnvironmentOptions) error {
	if !configuration.Configured {
		if configuration.PublishableKey != "" || configuration.ReturnURL != "" ||
			configuration.RefreshURL != "" || configuration.Provider != nil {
			return fmt.Errorf("Commerce %s environment has partial configuration", environment)
		}
		return nil
	}
	if configuration.Provider == nil || !publishableKeyPattern.MatchString(configuration.PublishableKey) ||
		!validEnvironmentReturnURL(configuration.ReturnURL, environment) ||
		!validEnvironmentReturnURL(configuration.RefreshURL, environment) {
		return fmt.Errorf("Commerce %s environment configuration is invalid", environment)
	}
	expectedPrefix := "pk_test_"
	if environment == EnvironmentLive {
		expectedPrefix = "pk_live_"
	}
	if !strings.HasPrefix(configuration.PublishableKey, expectedPrefix) {
		return fmt.Errorf("Commerce %s publishable key is in the wrong mode", environment)
	}
	return nil
}

func validatePrincipalPermission(principal auth.UserPrincipal, permission auth.Permission) error {
	if principal.UserID == uuid.Nil || principal.SessionID == uuid.Nil ||
		principal.CompanyID == uuid.Nil ||
		(principal.AuthKind != auth.TokenKindSession && principal.AuthKind != auth.TokenKindBearer) {
		return ErrUnauthenticated
	}
	if !principal.Can(permission) {
		return ErrForbidden
	}
	return nil
}

func authorizeRead(
	ctx context.Context,
	queries *generated.Queries,
	principal auth.UserPrincipal,
	permission auth.Permission,
) error {
	role, err := authorizePrincipal(ctx, queries, principal)
	if err != nil {
		return err
	}
	if role != principal.Role {
		return ErrAccessForbidden
	}
	if !role.Can(permission) {
		return ErrForbidden
	}
	return nil
}

func authorizeMemberRead(
	ctx context.Context,
	queries *generated.Queries,
	principal auth.UserPrincipal,
) error {
	role, err := authorizePrincipal(ctx, queries, principal)
	if err != nil {
		return err
	}
	if role != principal.Role {
		return ErrAccessForbidden
	}
	return nil
}

func authorizePrincipal(
	ctx context.Context,
	queries *generated.Queries,
	principal auth.UserPrincipal,
) (auth.CompanyRole, error) {
	role, err := queries.AuthorizeCommercePrincipal(
		ctx,
		generated.AuthorizeCommercePrincipalParams{
			ActorUserID: principal.UserID,
			SessionID:   principal.SessionID,
			CompanyID:   principal.CompanyID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrAccessForbidden
	}
	if err != nil {
		return "", fmt.Errorf("authorize Commerce principal: %w", err)
	}
	return auth.CompanyRole(role), nil
}

// lockManagingMember is lockMember plus the commerce.manage check every
// Commerce write makes.
func lockManagingMember(
	ctx context.Context,
	queries *generated.Queries,
	principal auth.UserPrincipal,
) error {
	role, err := lockMember(ctx, queries, principal)
	if err != nil {
		return err
	}
	if !role.Can(auth.PermissionCommerceManage) {
		return ErrForbidden
	}
	return nil
}

// lockMember is the Company-first lifecycle fence for every Commerce write.
// Middleware establishes the principal; this transaction only rechecks that
// the same membership remains active while product state changes.
func lockMember(
	ctx context.Context,
	queries *generated.Queries,
	principal auth.UserPrincipal,
) (auth.CompanyRole, error) {
	if _, err := queries.LockCompanyForMembershipMutation(
		ctx,
		principal.CompanyID,
	); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrAccessForbidden
	} else if err != nil {
		return "", fmt.Errorf("lock Company for Commerce mutation: %w", err)
	}
	membership, err := queries.GetCompanyMemberForUpdate(
		ctx,
		generated.GetCompanyMemberForUpdateParams{
			CompanyID: principal.CompanyID,
			UserID:    principal.UserID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && membership.Status != "active" {
		return "", ErrAccessForbidden
	}
	if err != nil {
		return "", fmt.Errorf("lock acting Company member for Commerce mutation: %w", err)
	}
	role := auth.CompanyRole(membership.Role)
	if role != principal.Role {
		return "", ErrAccessForbidden
	}
	authorizedRole, err := authorizePrincipal(ctx, queries, principal)
	if err != nil {
		return "", err
	}
	if authorizedRole != role {
		return "", ErrAccessForbidden
	}
	return role, nil
}

func validHTTPSURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
		parsed.Fragment == "" && len(raw) <= 2048
}

func validEnvironmentReturnURL(raw string, environment Environment) bool {
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n") {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawFragment != "" ||
		parsed.RawPath != "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if environment != EnvironmentSandbox || parsed.Scheme != "http" {
		return false
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return true
	}
	address := net.ParseIP(hostname)
	return address != nil && address.IsLoopback()
}

func validStripeConnectURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() == "connect.stripe.com" &&
		parsed.User == nil && parsed.Fragment == "" && len(raw) <= 4096
}

func validStripeDashboardURL(raw string, environment Environment, accountID string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "dashboard.stripe.com" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.RawFragment != "" || !providerIDPattern.MatchString(accountID) {
		return false
	}
	wantPath := "/" + accountID + "/dashboard"
	if environment == EnvironmentSandbox {
		wantPath = "/test" + wantPath
	}
	return parsed.Path == wantPath && parsed.RawPath == ""
}

func validEphemeralSecret(value string) bool {
	return value != "" && len(value) <= maxProviderSecretBytes &&
		strings.TrimSpace(value) == value && strings.IndexFunc(value, func(character rune) bool {
		return character < 0x21 || character > 0x7e
	}) == -1
}

func validFutureExpiry(expiry, now time.Time) bool {
	return !expiry.IsZero() && expiry.After(now) && expiry.Before(now.Add(24*time.Hour))
}

func equalDigest(value []byte, expected [sha256.Size]byte) bool {
	return len(value) == sha256.Size && string(value) == string(expected[:])
}

func providerError(operation string, err error) error {
	if providerErrorIsClassified(err) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %w", operation, errors.Join(ErrProviderUnavailable, err))
}

func providerErrorIsClassified(err error) bool {
	return errors.Is(err, ErrProviderSetupRequired) ||
		errors.Is(err, ErrProviderCredentialsInvalid) ||
		errors.Is(err, ErrProviderPermissionRequired) ||
		errors.Is(err, ErrProviderRateLimited) ||
		errors.Is(err, ErrTaxSetupRequired) ||
		errors.Is(err, ErrCheckoutTaxLocationInvalid) ||
		errors.Is(err, ErrProviderRequestInvalid) ||
		errors.Is(err, ErrGatewayRecovery) ||
		errors.Is(err, ErrInvalidProviderState) ||
		errors.Is(err, ErrProviderUnavailable)
}

func exactText(value pgtype.Text, expected string) bool {
	return value.Valid && value.String == expected
}

func optionalText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func textPointer(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func utcTimePointer(value any) *time.Time {
	timestamp, valid := timestampValue(value)
	if !valid {
		return nil
	}
	result := timestamp.UTC()
	return &result
}
