package commerce

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const (
	gatewayOperationLease              = time.Minute
	maximumProviderIdempotencyRetryAge = 23 * time.Hour
	gatewayRateLimitWindow             = time.Minute
	checkoutRateLimit                  = 10
	portalRateLimit                    = 10
	entitlementRateLimit               = 120
	siteCheckoutRateLimit              = 1000
	sitePortalRateLimit                = 1000
	siteEntitlementRateLimit           = 10000
	portalAuthenticationMaximumAge     = 5 * time.Minute
)

var (
	ErrRateLimited      = errors.New("Commerce Site request rate limit exceeded")
	ErrCustomerNotFound = errors.New("Commerce customer not found")
)

type RateLimitError struct {
	RetryAfter time.Duration
}

func (err *RateLimitError) Error() string {
	return ErrRateLimited.Error()
}

func (err *RateLimitError) Unwrap() error {
	return ErrRateLimited
}

type CheckoutRequest struct {
	OfferAlias  string
	OperationID uuid.UUID
}

type PortalRequest struct {
	OperationID uuid.UUID
}

type HostedRedirect struct {
	URL string `json:"url"`
}

type Entitlement struct {
	Key           string     `json:"key"`
	Status        string     `json:"status"`
	StartsAt      *time.Time `json:"startsAt,omitempty"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
	RevokedReason *string    `json:"revokedReason,omitempty"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

type EntitlementVerification struct {
	Entitlements []Entitlement `json:"entitlements"`
	Assertion    string        `json:"assertion"`
	ExpiresAt    time.Time     `json:"expiresAt"`
}

type gatewayCustomerAuthority struct {
	site            gatewaySite
	customer        generated.CommerceCustomer
	subjectHMAC     []byte
	environment     Environment
	provider        GatewayProvider
	accountProvider Provider
	subject         string
	sessionID       string
	authenticatedAt time.Time
	nonce           string
}

type gatewaySite struct {
	ID                            uuid.UUID
	CompanyID                     uuid.UUID
	Origin                        string
	PortalReturnPath              string
	ProviderPortalConfigurationID pgtype.Text
	ActiveManifestID              uuid.NullUUID
	ProviderAccountID             pgtype.Text
	ChargesEnabled                bool
	AccountStatus                 string
}

type leasedGatewayOperation struct {
	row     generated.CommerceGatewayOperation
	leaseID uuid.UUID
	action  gatewayOperationAction
}

type gatewayOperationAction string

const (
	gatewayOperationWrite   gatewayOperationAction = "write"
	gatewayOperationReplay  gatewayOperationAction = "replay"
	gatewayOperationRecover gatewayOperationAction = "recover"
)

func (s *DefaultCommerceService) CreateCheckout(
	ctx context.Context,
	request SiteSignedRequest,
	input CheckoutRequest,
) (HostedRedirect, error) {
	if !s.catalogCheckoutEnabled {
		return HostedRedirect{}, ErrCatalogDisabled
	}
	if !manifestAliasPattern.MatchString(input.OfferAlias) ||
		input.OperationID == uuid.Nil {
		return HostedRedirect{}, ErrInvalidInput
	}
	authority, err := s.authenticateGatewayCustomer(
		ctx,
		request,
		siteRequestActionCheckout,
		input.OperationID,
		true,
	)
	if err != nil {
		return HostedRedirect{}, err
	}
	if authority.site.AccountStatus != string(StatusReady) || !authority.site.ChargesEnabled {
		return HostedRedirect{}, ErrProviderSetupRequired
	}
	if err := s.consumeGatewayRateLimits(
		ctx,
		authority,
		"checkout",
		checkoutRateLimit,
		siteCheckoutRateLimit,
	); err != nil {
		return HostedRedirect{}, err
	}
	if err := s.consumeSiteChallenge(ctx, authority, "checkout", input.OperationID); err != nil {
		return HostedRedirect{}, err
	}
	if err := s.validateCurrentCheckoutAccount(ctx, authority); err != nil {
		return HostedRedirect{}, err
	}
	offer, err := s.store.Queries().GetActiveCommerceOfferByAlias(
		ctx,
		generated.GetActiveCommerceOfferByAliasParams{
			CompanyID: authority.site.CompanyID, SiteID: authority.site.ID,
			Alias: input.OfferAlias,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return HostedRedirect{}, ErrManifestNotFound
	}
	if err != nil {
		return HostedRedirect{}, fmt.Errorf("get active Commerce Offer: %w", err)
	}
	authority, err = s.ensureGatewayCustomerAuthority(ctx, authority)
	if err != nil {
		return HostedRedirect{}, err
	}
	if !offer.ProviderPriceID.Valid || !offer.ProviderAccountID.Valid ||
		offer.ProviderAccountID.String != authority.site.ProviderAccountID.String {
		return HostedRedirect{}, ErrInvalidStore
	}
	operation, err := s.beginGatewayOperation(
		ctx,
		authority,
		"checkout",
		input.OperationID,
		offer.ManifestID,
		offer.ID,
	)
	if err != nil {
		return HostedRedirect{}, err
	}
	if operation.action == gatewayOperationReplay {
		recovery, ok := authority.provider.(CheckoutRecoveryProvider)
		if !ok {
			return HostedRedirect{}, ErrGatewayRecovery
		}
		providerCtx, cancel := context.WithTimeout(ctx, s.providerCallTimeout)
		session, recoveryErr := recovery.RetrieveCheckoutSession(
			providerCtx,
			GatewayCheckoutRecoveryInput{
				AccountID:   authority.site.ProviderAccountID.String,
				SessionID:   operation.row.ProviderResourceID.String,
				CustomerID:  authority.customer.ProviderCustomerID.String,
				OperationID: input.OperationID.String(),
			},
		)
		cancel()
		if recoveryErr != nil {
			return HostedRedirect{}, errors.Join(ErrGatewayRecovery, recoveryErr)
		}
		return HostedRedirect{URL: session.URL}, nil
	}
	providerCtx, cancel := context.WithTimeout(ctx, s.providerCallTimeout)
	session, providerErr := authority.provider.CreateCheckoutSession(
		providerCtx,
		GatewayCheckoutInput{
			AccountID:      authority.site.ProviderAccountID.String,
			IdempotencyKey: operation.row.IdempotencyKey,
			CustomerID:     authority.customer.ProviderCustomerID.String,
			PriceID:        offer.ProviderPriceID.String,
			Subscription:   offer.OfferType == string(OfferTypeSubscription),
			AutomaticTax:   offer.AutomaticTax,
			SuccessURL:     offer.Origin + offer.SuccessPath,
			CancelURL:      offer.Origin + offer.CancelPath,
			Metadata: map[string]string{
				"boardly_company_id":   authority.site.CompanyID.String(),
				"boardly_site_id":      authority.site.ID.String(),
				"boardly_customer_id":  authority.customer.ID.String(),
				"boardly_offer_id":     offer.ID.String(),
				"boardly_operation_id": input.OperationID.String(),
			},
		},
	)
	cancel()
	if providerErr != nil {
		releaseErr := s.releaseGatewayOperation(ctx, operation, "provider_unavailable")
		return HostedRedirect{}, errors.Join(
			providerError("create Stripe Checkout Session", providerErr),
			releaseErr,
		)
	}
	if operation.action == gatewayOperationWrite {
		if err := s.completeGatewayOperation(ctx, operation, session.ID); err != nil {
			return HostedRedirect{}, err
		}
	}
	return HostedRedirect{URL: session.URL}, nil
}

func (s *DefaultCommerceService) CreatePortal(
	ctx context.Context,
	request SiteSignedRequest,
	input PortalRequest,
) (HostedRedirect, error) {
	if input.OperationID == uuid.Nil {
		return HostedRedirect{}, ErrInvalidInput
	}
	authority, err := s.authenticateGatewayCustomer(
		ctx,
		request,
		siteRequestActionPortal,
		input.OperationID,
		true,
	)
	if err != nil {
		return HostedRedirect{}, err
	}
	if s.now().Sub(authority.authenticatedAt) > portalAuthenticationMaximumAge {
		return HostedRedirect{}, ErrUnauthenticated
	}
	// Billing management remains available while an account is restricted or
	// draining, but a disconnected account has no provider authority with which
	// to create a Portal session.
	if authority.site.AccountStatus == string(StatusDisconnected) {
		return HostedRedirect{}, ErrProviderSetupRequired
	}
	if !authority.site.ProviderPortalConfigurationID.Valid ||
		!authority.site.ProviderAccountID.Valid || !authority.site.ActiveManifestID.Valid {
		return HostedRedirect{}, ErrInvalidStore
	}
	if authority.customer.ID == uuid.Nil || !authority.customer.ProviderCustomerID.Valid {
		return HostedRedirect{}, ErrCustomerNotFound
	}
	if err := s.consumeGatewayRateLimits(
		ctx,
		authority,
		"portal",
		portalRateLimit,
		sitePortalRateLimit,
	); err != nil {
		return HostedRedirect{}, err
	}
	if err := s.consumeSiteChallenge(ctx, authority, "portal", input.OperationID); err != nil {
		return HostedRedirect{}, err
	}
	manifestID := authority.site.ActiveManifestID.UUID
	operation, err := s.beginGatewayOperation(
		ctx,
		authority,
		"portal",
		input.OperationID,
		manifestID,
		uuid.Nil,
	)
	if err != nil {
		return HostedRedirect{}, err
	}
	if operation.action == gatewayOperationReplay &&
		!s.now().Before(operation.row.CreatedAt.Time.Add(maximumProviderIdempotencyRetryAge)) {
		return HostedRedirect{}, ErrGatewayRecovery
	}
	providerCtx, cancel := context.WithTimeout(ctx, s.providerCallTimeout)
	session, providerErr := authority.provider.CreatePortalSession(
		providerCtx,
		GatewayPortalInput{
			AccountID:       authority.site.ProviderAccountID.String,
			IdempotencyKey:  operation.row.IdempotencyKey,
			CustomerID:      authority.customer.ProviderCustomerID.String,
			ConfigurationID: authority.site.ProviderPortalConfigurationID.String,
			ReturnURL:       authority.site.Origin + authority.site.PortalReturnPath,
		},
	)
	cancel()
	if providerErr != nil {
		var releaseErr error
		if operation.action == gatewayOperationWrite {
			releaseErr = s.releaseGatewayOperation(ctx, operation, "provider_unavailable")
		}
		return HostedRedirect{}, errors.Join(
			providerError("create Stripe Portal Session", providerErr),
			releaseErr,
		)
	}
	if operation.action == gatewayOperationWrite {
		if err := s.completeGatewayOperation(ctx, operation, session.ID); err != nil {
			return HostedRedirect{}, err
		}
	}
	return HostedRedirect{URL: session.URL}, nil
}

func (s *DefaultCommerceService) VerifyEntitlements(
	ctx context.Context,
	request SiteSignedRequest,
) (EntitlementVerification, error) {
	authority, err := s.authenticateGatewayCustomer(
		ctx,
		request,
		siteRequestActionEntitlements,
		uuid.Nil,
		false,
	)
	if err != nil {
		return EntitlementVerification{}, err
	}
	if err := s.consumeGatewayRateLimits(
		ctx,
		authority,
		"entitlements",
		entitlementRateLimit,
		siteEntitlementRateLimit,
	); err != nil {
		return EntitlementVerification{}, err
	}
	result := EntitlementVerification{Entitlements: []Entitlement{}}
	if authority.customer.ID != uuid.Nil {
		rows, listErr := s.store.Queries().ListCommerceEntitlements(
			ctx,
			generated.ListCommerceEntitlementsParams{
				CompanyID: authority.site.CompanyID, SiteID: authority.site.ID,
				CustomerID: authority.customer.ID,
			},
		)
		if listErr != nil {
			return EntitlementVerification{}, fmt.Errorf(
				"list Commerce entitlements: %w",
				listErr,
			)
		}
		result.Entitlements = make([]Entitlement, 0, len(rows))
		for _, row := range rows {
			status := row.Status
			if status == "active" && row.ExpiresAt.Valid && !row.ExpiresAt.Time.After(s.now()) {
				status = "denied"
			}
			item := Entitlement{
				Key: row.EntitlementKey, Status: status,
				StartsAt:  manifestUTCTimePointer(row.StartsAt),
				ExpiresAt: manifestUTCTimePointer(row.ExpiresAt),
				UpdatedAt: row.UpdatedAt.Time.UTC(),
			}
			if row.RevokedReason.Valid {
				value := row.RevokedReason.String
				item.RevokedReason = &value
			}
			result.Entitlements = append(result.Entitlements, item)
		}
	}
	if s.sessionAssertionSigner == nil {
		return EntitlementVerification{}, ErrProviderUnavailable
	}
	issued, err := s.sessionAssertionSigner.Issue(SessionAssertionInput{
		Environment: authority.environment, Audience: authority.site.Origin,
		SiteID: authority.site.ID, Subject: authority.subject, SessionID: authority.sessionID,
		Entitlements: result.Entitlements,
	})
	if err != nil {
		return EntitlementVerification{}, err
	}
	result.Assertion = issued.Assertion
	result.ExpiresAt = issued.ExpiresAt
	return result, nil
}

func (s *DefaultCommerceService) SessionJWKSet() (SessionJWKSet, error) {
	if s == nil || s.sessionAssertionSigner == nil {
		return SessionJWKSet{}, ErrProviderUnavailable
	}
	return s.sessionAssertionSigner.JWKSet(), nil
}

func (s *DefaultCommerceService) authenticateGatewayCustomer(
	ctx context.Context,
	request SiteSignedRequest,
	action siteRequestAction,
	operationID uuid.UUID,
	requireNonce bool,
) (gatewayCustomerAuthority, error) {
	if len(s.customerHMACKey) < sha256.Size {
		return gatewayCustomerAuthority{}, ErrProviderUnavailable
	}
	siteID, keyID, err := inspectSiteRequestToken(request.Token)
	if err != nil {
		return gatewayCustomerAuthority{}, ErrUnauthenticated
	}
	row, err := s.store.Queries().GetCommerceSiteRequestSigningKey(
		ctx,
		generated.GetCommerceSiteRequestSigningKeyParams{
			SiteID: siteID,
			KeyID:  keyID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return gatewayCustomerAuthority{}, ErrUnauthenticated
	}
	if err != nil {
		return gatewayCustomerAuthority{}, fmt.Errorf("resolve Commerce Site authority: %w", err)
	}
	jwk, err := unmarshalSitePublicJWK(row.PublicJwk)
	if err != nil || jwk.KeyID != row.KeyID || jwk.KeyID != row.Fingerprint {
		return gatewayCustomerAuthority{}, ErrUnauthenticated
	}
	environment := Environment(row.Environment)
	configuration := s.environments[environment]
	if !configuration.Configured || configuration.Gateway == nil {
		return gatewayCustomerAuthority{}, ErrProviderUnavailable
	}
	identity, err := verifySiteRequestToken(
		request.Token,
		jwk,
		siteRequestExpectation{
			SiteID: row.SiteID, Environment: environment, Origin: row.Origin,
			Action: action, Method: request.Method, Path: request.Path,
			OperationID: operationID, BodySHA256: request.BodySHA256,
			RequireNonce: requireNonce,
		},
		s.now(),
	)
	if err != nil {
		return gatewayCustomerAuthority{}, ErrUnauthenticated
	}
	resolvedSite := gatewaySiteFromSigningKey(row)
	subjectHMAC := customerSubjectHMAC(s.customerHMACKey, resolvedSite.ID, identity.Subject)
	customer, err := s.store.Queries().GetCommerceCustomerBySubject(
		ctx,
		generated.GetCommerceCustomerBySubjectParams{
			CompanyID: resolvedSite.CompanyID, SiteID: resolvedSite.ID, SubjectHmac: subjectHMAC,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return gatewayCustomerAuthority{
			site: resolvedSite, subjectHMAC: subjectHMAC,
			environment: environment, provider: configuration.Gateway,
			accountProvider: configuration.Provider,
			subject:         identity.Subject, sessionID: identity.SessionID,
			authenticatedAt: identity.AuthenticatedAt, nonce: identity.Nonce,
		}, nil
	}
	if err != nil {
		return gatewayCustomerAuthority{}, fmt.Errorf("resolve Commerce customer: %w", err)
	}
	return gatewayCustomerAuthority{
		site: resolvedSite, customer: customer, subjectHMAC: subjectHMAC,
		environment: environment, provider: configuration.Gateway,
		accountProvider: configuration.Provider,
		subject:         identity.Subject, sessionID: identity.SessionID,
		authenticatedAt: identity.AuthenticatedAt, nonce: identity.Nonce,
	}, nil
}

func gatewaySiteFromSigningKey(row generated.GetCommerceSiteRequestSigningKeyRow) gatewaySite {
	return gatewaySite{
		ID: row.SiteID, CompanyID: row.CompanyID, Origin: row.Origin,
		PortalReturnPath:              row.PortalReturnPath,
		ProviderPortalConfigurationID: row.ProviderPortalConfigurationID,
		ActiveManifestID:              row.ActiveManifestID, ProviderAccountID: row.ProviderAccountID,
		ChargesEnabled: row.ChargesEnabled, AccountStatus: row.AccountStatus,
	}
}

func (s *DefaultCommerceService) validateCurrentCheckoutAccount(
	ctx context.Context,
	authority gatewayCustomerAuthority,
) error {
	if authority.accountProvider == nil || !authority.site.ProviderAccountID.Valid {
		return ErrProviderUnavailable
	}
	providerCtx, cancel := context.WithTimeout(ctx, s.providerCallTimeout)
	account, err := authority.accountProvider.RetrieveAccount(
		providerCtx,
		authority.site.ProviderAccountID.String,
	)
	cancel()
	if err != nil {
		return providerError("retrieve Stripe Account before Checkout", err)
	}
	if account.ID != authority.site.ProviderAccountID.String {
		return ErrInvalidProviderState
	}
	if err := validateProviderAccount(account); err != nil {
		return err
	}
	if statusFromProviderAccount(account, s.allowedCountries) != StatusReady ||
		!account.ChargesEnabled || !account.PayoutsEnabled {
		return ErrProviderSetupRequired
	}
	return nil
}

func (s *DefaultCommerceService) ensureGatewayCustomerAuthority(
	ctx context.Context,
	authority gatewayCustomerAuthority,
) (gatewayCustomerAuthority, error) {
	customer := authority.customer
	var err error
	if customer.ID == uuid.Nil {
		customer, err = s.store.Queries().InsertCommerceCustomer(
			ctx,
			generated.InsertCommerceCustomerParams{
				CompanyID: authority.site.CompanyID, SiteID: authority.site.ID,
				Environment: string(authority.environment), SubjectHmac: authority.subjectHMAC,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			customer, err = s.store.Queries().GetCommerceCustomerBySubject(
				ctx,
				generated.GetCommerceCustomerBySubjectParams{
					CompanyID: authority.site.CompanyID, SiteID: authority.site.ID,
					SubjectHmac: authority.subjectHMAC,
				},
			)
		}
	}
	if err != nil {
		return gatewayCustomerAuthority{}, fmt.Errorf("resolve Commerce customer: %w", err)
	}
	if !customer.ProviderCustomerID.Valid {
		customer, err = s.ensureProviderCustomer(
			ctx,
			authority.provider,
			authority.site,
			customer,
		)
		if err != nil {
			return gatewayCustomerAuthority{}, err
		}
	}
	authority.customer = customer
	return authority, nil
}

func (s *DefaultCommerceService) consumeGatewayRateLimits(
	ctx context.Context,
	authority gatewayCustomerAuthority,
	action string,
	maximumCustomerRequests int32,
	maximumSiteRequests int32,
) error {
	if len(authority.subjectHMAC) != sha256.Size {
		return ErrInvalidStore
	}
	return s.consumeDualRateLimits(
		ctx,
		authority.site.CompanyID,
		authority.site.ID,
		rateLimitScope{
			subjectHMAC: authority.subjectHMAC,
			action:      action, maximumRequests: maximumCustomerRequests,
		},
		rateLimitScope{
			subjectHMAC: rateLimitScopeHMAC(s.customerHMACKey, authority.site.ID, "site"),
			action:      "site_" + action, maximumRequests: maximumSiteRequests,
		},
	)
}

type rateLimitScope struct {
	subjectHMAC     []byte
	action          string
	maximumRequests int32
}

func (s *DefaultCommerceService) consumeDualRateLimits(
	ctx context.Context,
	companyID uuid.UUID,
	siteID uuid.UUID,
	narrow rateLimitScope,
	broad rateLimitScope,
) error {
	if len(narrow.subjectHMAC) != sha256.Size || len(broad.subjectHMAC) != sha256.Size {
		return ErrInvalidStore
	}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		if err := consumeCommerceRateLimitInTx(ctx, queries, companyID, siteID, narrow); err != nil {
			return err
		}
		return consumeCommerceRateLimitInTx(ctx, queries, companyID, siteID, broad)
	})
	if err != nil {
		return err
	}
	return s.pruneCommerceRateLimits(ctx)
}

func consumeCommerceRateLimitInTx(
	ctx context.Context,
	queries *generated.Queries,
	companyID uuid.UUID,
	siteID uuid.UUID,
	scope rateLimitScope,
) error {
	_, err := queries.ConsumeCommerceRateLimit(ctx, generated.ConsumeCommerceRateLimitParams{
		CompanyID: companyID, SiteID: siteID, SubjectHmac: scope.subjectHMAC,
		Action: scope.action, WindowMilliseconds: gatewayRateLimitWindow.Milliseconds(),
		MaximumRequests: scope.maximumRequests,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return &RateLimitError{RetryAfter: gatewayRateLimitWindow}
	}
	if err != nil {
		return fmt.Errorf("consume Commerce rate limit: %w", err)
	}
	return nil
}

func (s *DefaultCommerceService) pruneCommerceRateLimits(ctx context.Context) error {
	if _, err := s.store.Queries().PruneCommerceRateLimits(
		ctx,
		2*gatewayRateLimitWindow.Milliseconds(),
	); err != nil {
		return fmt.Errorf("prune Commerce rate limits: %w", err)
	}
	return nil
}

func (s *DefaultCommerceService) consumeSiteChallenge(
	ctx context.Context,
	authority gatewayCustomerAuthority,
	action string,
	operationID uuid.UUID,
) error {
	nonceDigest := sha256.Sum256([]byte(authority.nonce))
	_, err := s.store.Queries().ConsumeCommerceSiteChallenge(
		ctx,
		generated.ConsumeCommerceSiteChallengeParams{
			CompanyID: authority.site.CompanyID, SiteID: authority.site.ID,
			Environment: string(authority.environment), Action: action,
			OperationID: operationID, NonceSha256: nonceDigest[:],
			Now: pgtype.Timestamptz{Time: s.now().UTC(), Valid: true},
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnauthenticated
	}
	if err != nil {
		return fmt.Errorf("consume Commerce Site challenge: %w", err)
	}
	return nil
}

func (s *DefaultCommerceService) ensureProviderCustomer(
	ctx context.Context,
	provider GatewayProvider,
	site gatewaySite,
	customer generated.CommerceCustomer,
) (generated.CommerceCustomer, error) {
	if provider == nil || !site.ProviderAccountID.Valid {
		return generated.CommerceCustomer{}, ErrProviderUnavailable
	}
	if customer.ProviderFirstAttemptedAt.Valid &&
		!s.now().Before(customer.ProviderFirstAttemptedAt.Time.Add(maximumProviderIdempotencyRetryAge)) {
		return generated.CommerceCustomer{}, ErrGatewayRecovery
	}
	prepared, err := s.store.Queries().RecordCommerceCustomerProviderAttempt(
		ctx,
		generated.RecordCommerceCustomerProviderAttemptParams{
			CompanyID:       site.CompanyID,
			SiteID:          site.ID,
			CustomerID:      customer.ID,
			ExpectedVersion: customer.Version,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		current, readErr := s.store.Queries().GetCommerceCustomerBySubject(
			ctx,
			generated.GetCommerceCustomerBySubjectParams{
				CompanyID:   site.CompanyID,
				SiteID:      site.ID,
				SubjectHmac: customer.SubjectHmac,
			},
		)
		if readErr != nil {
			return generated.CommerceCustomer{}, fmt.Errorf(
				"reload Commerce customer before provider attempt: %w",
				readErr,
			)
		}
		if current.ProviderCustomerID.Valid {
			return current, nil
		}
		if current.ProviderFirstAttemptedAt.Valid &&
			!s.now().Before(
				current.ProviderFirstAttemptedAt.Time.Add(maximumProviderIdempotencyRetryAge),
			) {
			return generated.CommerceCustomer{}, ErrGatewayRecovery
		}
		return generated.CommerceCustomer{}, ErrGatewayBusy
	}
	if err != nil {
		return generated.CommerceCustomer{}, fmt.Errorf(
			"record Commerce customer provider attempt: %w",
			err,
		)
	}
	customer = prepared
	providerCtx, cancel := context.WithTimeout(ctx, s.providerCallTimeout)
	created, err := provider.CreateCustomer(providerCtx, GatewayCustomerInput{
		AccountID:      site.ProviderAccountID.String,
		IdempotencyKey: "boardly:customer:v1:" + customer.ID.String(),
		Metadata: map[string]string{
			"boardly_company_id":  site.CompanyID.String(),
			"boardly_site_id":     site.ID.String(),
			"boardly_customer_id": customer.ID.String(),
		},
	})
	cancel()
	if err != nil {
		return generated.CommerceCustomer{}, providerError("create Stripe Customer", err)
	}
	finalizeCtx, finalizeCancel := boundedFinalizationContext(ctx)
	defer finalizeCancel()
	updated, err := s.store.Queries().SetCommerceCustomerProviderID(
		finalizeCtx,
		generated.SetCommerceCustomerProviderIDParams{
			ProviderCustomerID: created.ID, CompanyID: site.CompanyID, SiteID: site.ID,
			CustomerID: customer.ID, ExpectedVersion: customer.Version,
		},
	)
	if err == nil {
		return updated, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return generated.CommerceCustomer{}, fmt.Errorf("persist Commerce customer mapping: %w", err)
	}
	updated, err = s.store.Queries().GetCommerceCustomerBySubject(
		finalizeCtx,
		generated.GetCommerceCustomerBySubjectParams{
			CompanyID: site.CompanyID, SiteID: site.ID, SubjectHmac: customer.SubjectHmac,
		},
	)
	if err != nil {
		return generated.CommerceCustomer{}, fmt.Errorf("reload Commerce customer mapping: %w", err)
	}
	if !updated.ProviderCustomerID.Valid ||
		updated.ProviderCustomerID.String != created.ID {
		return generated.CommerceCustomer{}, ErrManifestConflict
	}
	return updated, nil
}

func (s *DefaultCommerceService) beginGatewayOperation(
	ctx context.Context,
	authority gatewayCustomerAuthority,
	operationType string,
	operationID uuid.UUID,
	manifestID uuid.UUID,
	offerID uuid.UUID,
) (leasedGatewayOperation, error) {
	requestDigest := gatewayOperationDigest(
		operationType,
		authority.site.ID,
		authority.customer.ID,
		offerID,
	)
	row, err := s.store.Queries().InsertCommerceGatewayOperation(
		ctx,
		generated.InsertCommerceGatewayOperationParams{
			CompanyID: authority.site.CompanyID, SiteID: authority.site.ID,
			CustomerID: nullableUUID(authority.customer.ID), ManifestID: nullableUUID(manifestID),
			OfferID: nullableUUID(offerID), Environment: string(authority.environment),
			OperationType: operationType, OperationID: operationID,
			RequestSha256: requestDigest[:],
			IdempotencyKey: "boardly:" + operationType + ":v1:" +
				authority.site.ID.String() + ":" + operationID.String(),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = s.store.Queries().GetCommerceGatewayOperation(
			ctx,
			generated.GetCommerceGatewayOperationParams{
				CompanyID: authority.site.CompanyID, SiteID: authority.site.ID,
				OperationType: operationType, OperationID: operationID,
			},
		)
	}
	if err != nil {
		return leasedGatewayOperation{}, fmt.Errorf("ensure Commerce gateway operation: %w", err)
	}
	if subtle.ConstantTimeCompare(row.RequestSha256, requestDigest[:]) != 1 ||
		!row.CustomerID.Valid || row.CustomerID.UUID != authority.customer.ID ||
		!equalNullableUUID(row.ManifestID, nullableUUID(manifestID)) ||
		!equalNullableUUID(row.OfferID, nullableUUID(offerID)) {
		return leasedGatewayOperation{}, ErrManifestConflict
	}
	action, err := gatewayOperationDisposition(row, s.now())
	if err != nil {
		return leasedGatewayOperation{}, err
	}
	if action == gatewayOperationReplay {
		return leasedGatewayOperation{row: row, action: action}, nil
	}
	if action == gatewayOperationRecover {
		return leasedGatewayOperation{}, ErrGatewayRecovery
	}
	leaseID := uuid.New()
	row, err = s.store.Queries().ClaimCommerceGatewayOperation(
		ctx,
		generated.ClaimCommerceGatewayOperationParams{
			LeaseID: leaseID, LeaseDurationMilliseconds: gatewayOperationLease.Milliseconds(),
			ID: row.ID, CompanyID: authority.site.CompanyID, SiteID: authority.site.ID,
			RequestSha256: requestDigest[:],
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return leasedGatewayOperation{}, ErrGatewayBusy
	}
	if err != nil {
		return leasedGatewayOperation{}, fmt.Errorf("claim Commerce gateway operation: %w", err)
	}
	row, err = s.store.Queries().RecordCommerceGatewayProviderAttempt(
		ctx,
		generated.RecordCommerceGatewayProviderAttemptParams{
			ID: row.ID, CompanyID: authority.site.CompanyID, SiteID: authority.site.ID,
			LeaseID: leaseID, ExpectedVersion: row.Version,
		},
	)
	if err != nil {
		return leasedGatewayOperation{}, fmt.Errorf("record Commerce provider attempt: %w", err)
	}
	return leasedGatewayOperation{row: row, leaseID: leaseID, action: gatewayOperationWrite}, nil
}

func gatewayOperationDisposition(
	row generated.CommerceGatewayOperation,
	now time.Time,
) (gatewayOperationAction, error) {
	switch row.State {
	case "succeeded":
		if !row.ProviderResourceID.Valid || row.ProviderResourceID.String == "" {
			return "", ErrInvalidStore
		}
		return gatewayOperationReplay, nil
	case "pending", "leased", "recovery_required":
		if row.ProviderAttemptCount >= 64 {
			return gatewayOperationRecover, nil
		}
		if row.ProviderAttemptCount > 0 &&
			!now.Before(row.CreatedAt.Time.Add(maximumProviderIdempotencyRetryAge)) {
			return gatewayOperationRecover, nil
		}
		return gatewayOperationWrite, nil
	default:
		return "", ErrInvalidStore
	}
}

func (s *DefaultCommerceService) completeGatewayOperation(
	ctx context.Context,
	operation leasedGatewayOperation,
	providerResourceID string,
) error {
	ctx, cancel := boundedFinalizationContext(ctx)
	defer cancel()
	_, err := s.store.Queries().CompleteCommerceGatewayOperation(
		ctx,
		generated.CompleteCommerceGatewayOperationParams{
			ProviderResourceID: providerResourceID,
			ID:                 operation.row.ID, CompanyID: operation.row.CompanyID,
			SiteID: operation.row.SiteID, LeaseID: operation.leaseID,
			ExpectedVersion: operation.row.Version,
		},
	)
	if err != nil {
		return fmt.Errorf("complete Commerce gateway operation: %w", err)
	}
	return nil
}

func (s *DefaultCommerceService) releaseGatewayOperation(
	ctx context.Context,
	operation leasedGatewayOperation,
	code string,
) error {
	ctx, cancel := boundedFinalizationContext(ctx)
	defer cancel()
	_, err := s.store.Queries().ReleaseCommerceGatewayOperationForRetry(
		ctx,
		generated.ReleaseCommerceGatewayOperationForRetryParams{
			LastErrorCode: code, ID: operation.row.ID, CompanyID: operation.row.CompanyID,
			SiteID: operation.row.SiteID, LeaseID: operation.leaseID,
			ExpectedVersion: operation.row.Version,
		},
	)
	if err != nil {
		return fmt.Errorf("release Commerce gateway operation: %w", err)
	}
	return nil
}

func customerSubjectHMAC(key []byte, siteID uuid.UUID, subject string) []byte {
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write(siteID[:])
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(subject))
	return digest.Sum(nil)
}

func gatewayOperationDigest(
	operationType string,
	siteID uuid.UUID,
	customerID uuid.UUID,
	offerID uuid.UUID,
) [sha256.Size]byte {
	digest := sha256.New()
	_, _ = digest.Write([]byte("boardly-commerce-gateway-v1\x00"))
	_, _ = digest.Write([]byte(operationType))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(siteID[:])
	_, _ = digest.Write(customerID[:])
	_, _ = digest.Write(offerID[:])
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func equalNullableUUID(left, right uuid.NullUUID) bool {
	return left.Valid == right.Valid && (!left.Valid || left.UUID == right.UUID)
}
