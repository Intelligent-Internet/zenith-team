package commerce

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/approval"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const (
	maximumApprovalSessionAge = 15 * time.Minute
	catalogOperationLease     = 10 * time.Minute
)

var errStaleCatalogPublisher = errors.New("stale Commerce catalog publisher")

type ApproveManifestInput struct {
	ManifestID uuid.UUID
	Digest     string
}

type catalogPublishState struct {
	manifest  generated.CommerceManifest
	site      generated.CommerceSite
	account   generated.CommerceAccount
	document  ManifestDocument
	offers    []generated.CommerceOffer
	operation generated.CommerceGatewayOperation
	leaseID   uuid.UUID
}

type manifestProductGroup struct {
	key          string
	displayName  string
	offerAliases []string
}

func (s *DefaultCommerceService) ApproveAndPublishManifest(
	ctx context.Context,
	principal auth.UserPrincipal,
	input ApproveManifestInput,
) (ManifestStatus, error) {
	if err := validatePrincipalPermission(principal, auth.PermissionCommerceManage); err != nil {
		return ManifestStatus{}, err
	}
	if input.ManifestID == uuid.Nil || len(input.Digest) != sha256.Size*2 ||
		strings.ToLower(input.Digest) != input.Digest {
		return ManifestStatus{}, ErrInvalidInput
	}
	digest, err := hex.DecodeString(input.Digest)
	if err != nil || len(digest) != sha256.Size {
		return ManifestStatus{}, ErrInvalidInput
	}
	if !s.catalogCheckoutEnabled {
		return ManifestStatus{}, ErrCatalogDisabled
	}
	state, err := s.prepareCatalogPublish(ctx, principal, input.ManifestID, digest)
	if err != nil {
		return ManifestStatus{}, err
	}
	if state.manifest.State == "active" {
		activated, resolveErr := s.activateCatalog(
			ctx,
			principal,
			state,
			VerifiedSite{},
			"",
		)
		if resolveErr != nil {
			return ManifestStatus{}, resolveErr
		}
		return manifestStatusFromRow(activated), nil
	}
	configuration := s.environments[Environment(state.site.Environment)]
	if configuration.Gateway == nil || s.siteAuthority == nil {
		return ManifestStatus{}, catalogPublishError(
			ErrProviderUnavailable,
			s.failCatalogPublish(ctx, state, "provider_unavailable"),
		)
	}

	verified, err := s.verifyCatalogSiteAuthority(ctx, state)
	if err != nil || len(verified.SigningKeys) != 1 {
		return ManifestStatus{}, catalogPublishError(
			errors.Join(ErrSiteAuthority, err),
			s.failCatalogPublish(ctx, state, "site_verification_failed"),
		)
	}
	if state.operation.State != "succeeded" {
		state.operation, err = s.recordCatalogProviderAttempt(ctx, state)
		if err != nil {
			return ManifestStatus{}, err
		}
	}
	portal, err := s.applyCatalog(ctx, configuration.Gateway, state)
	if err != nil {
		return ManifestStatus{}, catalogPublishError(
			providerError("publish Stripe catalog", err),
			s.failCatalogPublish(ctx, state, "provider_unavailable"),
		)
	}
	if state.operation.State != "succeeded" {
		state.operation, err = s.completeCatalogOperation(ctx, state, portal.ID)
		if err != nil {
			return ManifestStatus{}, err
		}
	}
	activated, err := s.activateCatalog(
		ctx,
		principal,
		state,
		verified,
		portal.ID,
	)
	if err != nil {
		if isSiteAuthorityConflict(err) {
			return ManifestStatus{}, catalogPublishError(
				ErrSiteAuthority,
				s.failCatalogPublish(ctx, state, "site_authority_conflict"),
			)
		}
		return ManifestStatus{}, err
	}
	return manifestStatusFromRow(activated), nil
}

func (s *DefaultCommerceService) prepareCatalogPublish(
	ctx context.Context,
	principal auth.UserPrincipal,
	manifestID uuid.UUID,
	digest []byte,
) (catalogPublishState, error) {
	state := catalogPublishState{}
	leaseID := uuid.New()
	state.leaseID = leaseID
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		if err := lockManagingMember(ctx, queries, principal); err != nil {
			return err
		}
		if err := authorizeRecentApproval(ctx, queries, principal); err != nil {
			return err
		}
		manifest, err := queries.GetCommerceManifestForUpdate(
			ctx,
			generated.GetCommerceManifestForUpdateParams{
				CompanyID: principal.CompanyID, ManifestID: manifestID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrManifestNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Commerce manifest for approval: %w", err)
		}
		if subtle.ConstantTimeCompare(manifest.Digest, digest) != 1 {
			return ErrManifestConflict
		}
		if _, err := queries.LockActiveCommerceManifestForSite(
			ctx,
			generated.LockActiveCommerceManifestForSiteParams{
				CompanyID:  principal.CompanyID,
				SiteID:     manifest.SiteID,
				ManifestID: manifest.ID,
			},
		); err != nil {
			return fmt.Errorf("lock active Commerce manifest for approval: %w", err)
		}
		site, err := queries.GetCommerceSiteForUpdate(
			ctx,
			generated.GetCommerceSiteForUpdateParams{
				CompanyID: principal.CompanyID, SiteID: manifest.SiteID,
			},
		)
		if err != nil {
			return fmt.Errorf("lock Commerce Site for approval: %w", err)
		}
		document, err := ParseManifest(
			manifest.CanonicalJson,
			Environment(site.Environment),
		)
		if err != nil || subtle.ConstantTimeCompare(document.Digest[:], digest) != 1 {
			return ErrInvalidStore
		}
		if err := approval.CheckCommerceManifestPublishInTx(
			ctx,
			queries,
			approval.CommerceManifestPublishCheck{
				CompanyID:     principal.CompanyID,
				ManifestID:    manifest.ID,
				Digest:        hex.EncodeToString(manifest.Digest),
				Now:           s.now(),
				AllowApproved: manifest.State == "active",
			},
		); err != nil {
			if errors.Is(err, approval.ErrApprovalInvalidInput) {
				return errors.Join(ErrInvalidStore, err)
			}
			return errors.Join(ErrManifestNotApprovable, err)
		}
		if manifest.State == "active" {
			state.manifest, state.site, state.document = manifest, site, document
			return nil
		}
		if manifest.State != "draft" && manifest.State != "apply_failed" &&
			manifest.State != "publishing" {
			return ErrManifestNotApprovable
		}
		if validLoopbackHTTPOrigin(document.Manifest.Site.Origin) {
			signingKeys, err := queries.ListCommerceSiteSigningKeys(
				ctx,
				generated.ListCommerceSiteSigningKeysParams{
					CompanyID: principal.CompanyID,
					SiteID:    site.ID,
				},
			)
			if err != nil {
				return fmt.Errorf("list local Commerce Site signing keys: %w", err)
			}
			if !slices.ContainsFunc(signingKeys, func(key generated.CommerceSiteSigningKey) bool {
				return key.State == string(SigningKeyStatePending) ||
					key.State == string(SigningKeyStateActive)
			}) {
				return ErrSiteSigningKeyRequired
			}
		}
		account, err := queries.GetCommerceAccountForUpdate(
			ctx,
			generated.GetCommerceAccountForUpdateParams{
				CompanyID: principal.CompanyID, Environment: site.Environment,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProviderSetupRequired
		}
		if err != nil {
			return fmt.Errorf("lock Commerce account for approval: %w", err)
		}
		if err := validateReadyCommerceAccount(account, s.allowedCountries); err != nil {
			return err
		}
		accepted, err := queries.GetCommerceTermsAcceptance(
			ctx,
			generated.GetCommerceTermsAcceptanceParams{
				CompanyID: principal.CompanyID, TermsVersion: s.terms.Version,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTermsOutdated
		}
		if err != nil || !equalDigest(accepted.TermsSha256, s.terms.SHA256) {
			if err != nil {
				return fmt.Errorf("verify Commerce terms for approval: %w", err)
			}
			return ErrTermsConflict
		}
		if manifest.State != "publishing" {
			manifest, err = queries.MarkCommerceManifestPublishing(
				ctx,
				generated.MarkCommerceManifestPublishingParams{
					ApprovedByUserID: principal.UserID, ApprovalSessionID: principal.SessionID,
					CompanyID: principal.CompanyID, ManifestID: manifest.ID, SiteID: site.ID,
					Digest: digest, ExpectedVersion: manifest.Version,
				},
			)
			if err != nil {
				return fmt.Errorf("approve Commerce manifest: %w", err)
			}
		}
		offers, err := ensurePublishingOffers(
			ctx,
			queries,
			principal.CompanyID,
			site,
			manifest,
			document.Manifest.Offers,
		)
		if err != nil {
			return err
		}
		operation, err := ensureCatalogOperation(ctx, queries, principal.CompanyID, site, manifest)
		if err != nil {
			return err
		}
		action, err := gatewayOperationDisposition(operation, s.now())
		if err != nil {
			return err
		}
		if action == gatewayOperationRecover {
			return ErrGatewayRecovery
		}
		if action == gatewayOperationReplay {
			state.manifest, state.site, state.account = manifest, site, account
			state.document, state.offers, state.operation = document, offers, operation
			return nil
		}
		operation, err = queries.ClaimCommerceGatewayOperation(
			ctx,
			generated.ClaimCommerceGatewayOperationParams{
				LeaseID: leaseID, LeaseDurationMilliseconds: catalogOperationLease.Milliseconds(),
				ID: operation.ID, CompanyID: principal.CompanyID, SiteID: site.ID,
				RequestSha256: digest,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrGatewayBusy
		}
		if err != nil {
			return fmt.Errorf("claim Commerce catalog operation: %w", err)
		}
		state.manifest, state.site, state.account = manifest, site, account
		state.document, state.offers, state.operation = document, offers, operation
		return nil
	})
	return state, err
}

func (s *DefaultCommerceService) verifyCatalogSiteAuthority(
	ctx context.Context,
	state catalogPublishState,
) (VerifiedSite, error) {
	site := state.document.Manifest.Site
	if validLoopbackHTTPOrigin(site.Origin) {
		if state.site.Environment != string(EnvironmentSandbox) || state.site.Origin != site.Origin {
			return VerifiedSite{}, ErrInvalidStore
		}
		rows, err := s.store.Queries().ListCommerceSiteSigningKeys(
			ctx,
			generated.ListCommerceSiteSigningKeysParams{
				CompanyID: state.site.CompanyID,
				SiteID:    state.site.ID,
			},
		)
		if err != nil {
			return VerifiedSite{}, fmt.Errorf("list local Commerce Site signing keys: %w", err)
		}
		for _, wantedState := range []string{"pending", "active"} {
			for _, row := range rows {
				if row.State != wantedState {
					continue
				}
				jwk, parseErr := unmarshalSitePublicJWK(row.PublicJwk)
				if parseErr != nil || jwk.KeyID != row.Fingerprint {
					return VerifiedSite{}, ErrInvalidStore
				}
				return VerifiedSite{SigningKeys: []SitePublicJWK{jwk}}, nil
			}
		}
		return VerifiedSite{}, ErrSiteAuthority
	}
	return s.siteAuthority.VerifySite(ctx, SiteVerificationInput{
		Origin:    site.Origin,
		SiteAlias: site.Alias, ManifestDigest: hex.EncodeToString(state.manifest.Digest),
	})
}

func (s *DefaultCommerceService) recordCatalogProviderAttempt(
	ctx context.Context,
	state catalogPublishState,
) (generated.CommerceGatewayOperation, error) {
	operation, err := s.store.Queries().RecordCommerceGatewayProviderAttempt(
		ctx,
		generated.RecordCommerceGatewayProviderAttemptParams{
			ID: state.operation.ID, CompanyID: state.manifest.CompanyID, SiteID: state.site.ID,
			LeaseID: state.leaseID, ExpectedVersion: state.operation.Version,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.CommerceGatewayOperation{}, ErrGatewayBusy
	}
	if err != nil {
		return generated.CommerceGatewayOperation{}, fmt.Errorf(
			"record Commerce catalog provider attempt: %w",
			err,
		)
	}
	return operation, nil
}

func ensurePublishingOffers(
	ctx context.Context,
	queries *generated.Queries,
	companyID uuid.UUID,
	site generated.CommerceSite,
	manifest generated.CommerceManifest,
	offers []ManifestOffer,
) ([]generated.CommerceOffer, error) {
	result := make([]generated.CommerceOffer, 0, len(offers))
	for _, offer := range offers {
		row, err := queries.InsertCommercePublishingOffer(
			ctx,
			generated.InsertCommercePublishingOfferParams{
				CompanyID: companyID, SiteID: site.ID,
				ManifestID: manifest.ID, Environment: site.Environment, Alias: offer.Alias,
				DisplayName: offer.DisplayName, OfferType: string(offer.Type),
				PlanGroupAlias: nullableText(offer.PlanGroup),
				EntitlementKey: offer.Entitlement, Amount: offer.Amount, Currency: offer.Currency,
				RecurringInterval: nullableText(offer.Interval), AutomaticTax: offer.AutomaticTax,
				SuccessPath: offer.SuccessPath, CancelPath: offer.CancelPath,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			row, err = queries.GetCommerceOfferByManifestAlias(
				ctx,
				generated.GetCommerceOfferByManifestAliasParams{
					CompanyID: companyID, ManifestID: manifest.ID, Alias: offer.Alias,
				},
			)
		}
		if err != nil {
			return nil, fmt.Errorf("insert Commerce Offer %q: %w", offer.Alias, err)
		}
		if !offerMatchesManifest(row, offer, site) {
			return nil, ErrManifestConflict
		}
		result = append(result, row)
	}
	return result, nil
}

func ensureCatalogOperation(
	ctx context.Context,
	queries *generated.Queries,
	companyID uuid.UUID,
	site generated.CommerceSite,
	manifest generated.CommerceManifest,
) (generated.CommerceGatewayOperation, error) {
	row, err := queries.InsertCommerceGatewayOperation(
		ctx,
		generated.InsertCommerceGatewayOperationParams{
			CompanyID: companyID, SiteID: site.ID,
			ManifestID: nullableUUID(manifest.ID), Environment: site.Environment,
			OperationType: "catalog_publish", OperationID: manifest.ID,
			RequestSha256:  manifest.Digest,
			IdempotencyKey: "boardly:catalog-publish:v1:" + manifest.ID.String(),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = queries.GetCommerceGatewayOperation(
			ctx,
			generated.GetCommerceGatewayOperationParams{
				CompanyID: companyID, SiteID: site.ID,
				OperationType: "catalog_publish", OperationID: manifest.ID,
			},
		)
	}
	if err != nil {
		return generated.CommerceGatewayOperation{}, fmt.Errorf(
			"ensure Commerce catalog operation: %w",
			err,
		)
	}
	if subtle.ConstantTimeCompare(row.RequestSha256, manifest.Digest) != 1 ||
		!row.ManifestID.Valid || row.ManifestID.UUID != manifest.ID {
		return generated.CommerceGatewayOperation{}, ErrManifestConflict
	}
	return row, nil
}

func (s *DefaultCommerceService) applyCatalog(
	ctx context.Context,
	provider GatewayProvider,
	state catalogPublishState,
) (CatalogPortal, error) {
	if state.operation.ProviderResourceID.Valid {
		providerResourceID := state.operation.ProviderResourceID.String
		if !strings.HasPrefix(providerResourceID, "bpc_") {
			return CatalogPortal{}, ErrInvalidStore
		}
		return CatalogPortal{ID: providerResourceID}, nil
	}
	accountID := state.account.ProviderAccountID.String
	metadata := map[string]string{
		"boardly_company_id":  state.manifest.CompanyID.String(),
		"boardly_manifest_id": state.manifest.ID.String(),
		"boardly_site_id":     state.site.ID.String(),
	}
	rowByAlias := make(map[string]generated.CommerceOffer, len(state.offers))
	for _, row := range state.offers {
		rowByAlias[row.Alias] = row
	}
	portalProducts := make([]CatalogPortalProduct, 0)
	for _, group := range manifestProductGroups(state.document.Manifest.Offers) {
		productID := existingGroupProduct(group, rowByAlias)
		if productID == "" {
			product, err := provider.CreateProduct(ctx, CatalogProductInput{
				AccountID:      accountID,
				IdempotencyKey: catalogIdempotencyKey("product", state.manifest.ID, group.key),
				Name:           group.displayName,
				Metadata:       cloneStringMap(metadata, "boardly_catalog_group", group.key),
			})
			if err != nil {
				return CatalogPortal{}, err
			}
			productID = product.ID
		}
		priceIDs := make([]string, 0, len(group.offerAliases))
		for _, alias := range group.offerAliases {
			row := rowByAlias[alias]
			priceID := ""
			if row.ProviderPriceID.Valid {
				priceID = row.ProviderPriceID.String
			}
			if priceID == "" {
				price, err := provider.CreatePrice(ctx, CatalogPriceInput{
					AccountID:      accountID,
					IdempotencyKey: catalogIdempotencyKey("price", state.manifest.ID, alias),
					ProductID:      productID, Amount: row.Amount, Currency: row.Currency,
					Interval: row.RecurringInterval.String,
					Metadata: cloneStringMap(metadata, "boardly_offer_id", row.ID.String()),
				})
				if err != nil {
					return CatalogPortal{}, err
				}
				priceID = price.ID
			}
			updated, err := s.persistOfferResources(ctx, row, productID, priceID)
			if err != nil {
				return CatalogPortal{}, err
			}
			rowByAlias[alias] = updated
			priceIDs = append(priceIDs, priceID)
		}
		if strings.HasPrefix(group.key, "subscription:") {
			portalProducts = append(portalProducts, CatalogPortalProduct{
				ProductID: productID, PriceIDs: priceIDs,
			})
		}
	}
	return provider.CreatePortalConfiguration(ctx, CatalogPortalInput{
		AccountID:      accountID,
		IdempotencyKey: catalogIdempotencyKey("portal", state.manifest.ID, state.site.SiteAlias),
		Name:           "Zenith Team " + state.site.SiteAlias,
		DefaultReturnURL: state.document.Manifest.Site.Origin +
			state.document.Manifest.Site.PortalReturnPath,
		SubscriptionProducts: portalProducts,
		Metadata:             metadata,
	})
}

func (s *DefaultCommerceService) persistOfferResources(
	ctx context.Context,
	row generated.CommerceOffer,
	productID string,
	priceID string,
) (generated.CommerceOffer, error) {
	if row.ProviderProductID.Valid && row.ProviderPriceID.Valid {
		if row.ProviderProductID.String != productID || row.ProviderPriceID.String != priceID {
			return generated.CommerceOffer{}, ErrManifestConflict
		}
		return row, nil
	}
	ctx, cancel := boundedFinalizationContext(ctx)
	defer cancel()
	updated, err := s.store.Queries().SetCommerceOfferProviderResources(
		ctx,
		generated.SetCommerceOfferProviderResourcesParams{
			ProviderProductID: productID, ProviderPriceID: priceID,
			CompanyID: row.CompanyID, OfferID: row.ID, ManifestID: row.ManifestID,
			ExpectedVersion: row.Version,
		},
	)
	if err == nil {
		return updated, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return generated.CommerceOffer{}, fmt.Errorf("persist Commerce Offer resources: %w", err)
	}
	updated, err = s.store.Queries().GetCommerceOfferByManifestAlias(
		ctx,
		generated.GetCommerceOfferByManifestAliasParams{
			CompanyID: row.CompanyID, ManifestID: row.ManifestID, Alias: row.Alias,
		},
	)
	if err != nil {
		return generated.CommerceOffer{}, fmt.Errorf("reload Commerce Offer resources: %w", err)
	}
	if !updated.ProviderProductID.Valid || !updated.ProviderPriceID.Valid ||
		updated.ProviderProductID.String != productID || updated.ProviderPriceID.String != priceID {
		return generated.CommerceOffer{}, ErrManifestConflict
	}
	return updated, nil
}

func (s *DefaultCommerceService) completeCatalogOperation(
	ctx context.Context,
	state catalogPublishState,
	providerResourceID string,
) (generated.CommerceGatewayOperation, error) {
	ctx, cancel := boundedFinalizationContext(ctx)
	defer cancel()
	operation, err := s.store.Queries().CompleteCommerceGatewayOperation(
		ctx,
		generated.CompleteCommerceGatewayOperationParams{
			ProviderResourceID: providerResourceID,
			ID:                 state.operation.ID,
			CompanyID:          state.manifest.CompanyID,
			SiteID:             state.site.ID,
			LeaseID:            state.leaseID,
			ExpectedVersion:    state.operation.Version,
		},
	)
	if err != nil {
		return generated.CommerceGatewayOperation{}, fmt.Errorf(
			"complete Commerce catalog operation: %w",
			err,
		)
	}
	return operation, nil
}

func (s *DefaultCommerceService) activateCatalog(
	ctx context.Context,
	principal auth.UserPrincipal,
	state catalogPublishState,
	verified VerifiedSite,
	portalConfigurationID string,
) (generated.CommerceManifest, error) {
	var activated generated.CommerceManifest
	var pending []events.Event
	var err error
	for range 3 {
		activated = generated.CommerceManifest{}
		err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
			pending = pending[:0]
			if err := lockManagingMember(ctx, queries, principal); err != nil {
				return err
			}
			if err := authorizeRecentApproval(ctx, queries, principal); err != nil {
				return err
			}
			manifest, err := queries.GetCommerceManifestForUpdate(
				ctx,
				generated.GetCommerceManifestForUpdateParams{
					CompanyID: principal.CompanyID, ManifestID: state.manifest.ID,
				},
			)
			if err != nil {
				return fmt.Errorf("relock Commerce manifest after provider I/O: %w", err)
			}
			if manifest.State == "active" {
				activated = manifest
				_, _, resolveErr := approval.ResolveCommerceManifestPublishInTx(
					ctx,
					queries,
					&pending,
					approval.CommerceManifestPublishResolution{
						CompanyID:       principal.CompanyID,
						ManifestID:      manifest.ID,
						DecidedByUserID: principal.UserID,
						Digest:          hex.EncodeToString(manifest.Digest),
						Now:             s.now(),
					},
				)
				if resolveErr != nil {
					return commerceManifestApprovalPublishError(resolveErr)
				}
				return nil
			}
			if manifest.State != "publishing" ||
				subtle.ConstantTimeCompare(manifest.Digest, state.manifest.Digest) != 1 {
				return ErrManifestConflict
			}
			if _, err := queries.LockActiveCommerceManifestForSite(
				ctx,
				generated.LockActiveCommerceManifestForSiteParams{
					CompanyID:  principal.CompanyID,
					SiteID:     manifest.SiteID,
					ManifestID: manifest.ID,
				},
			); err != nil {
				return fmt.Errorf("relock active Commerce manifest after provider I/O: %w", err)
			}
			site, err := queries.GetCommerceSiteForUpdate(
				ctx,
				generated.GetCommerceSiteForUpdateParams{
					CompanyID: principal.CompanyID, SiteID: state.site.ID,
				},
			)
			if err != nil {
				return fmt.Errorf("relock Commerce Site after provider I/O: %w", err)
			}
			account, err := queries.GetCommerceAccountForUpdate(
				ctx,
				generated.GetCommerceAccountForUpdateParams{
					CompanyID: principal.CompanyID, Environment: site.Environment,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrProviderSetupRequired
			}
			if err != nil {
				return fmt.Errorf("relock Commerce account after provider I/O: %w", err)
			}
			if account.ID != state.account.ID {
				return ErrAccountConflict
			}
			if err := validateReadyCommerceAccount(account, s.allowedCountries); err != nil {
				return err
			}
			if _, err := queries.DisableActiveCommerceOffers(
				ctx,
				generated.DisableActiveCommerceOffersParams{
					CompanyID: principal.CompanyID, SiteID: site.ID, ManifestID: manifest.ID,
				},
			); err != nil {
				return fmt.Errorf("disable superseded Commerce Offers: %w", err)
			}
			if _, err := queries.SupersedeActiveCommerceManifests(
				ctx,
				generated.SupersedeActiveCommerceManifestsParams{
					CompanyID: principal.CompanyID, SiteID: site.ID, ManifestID: manifest.ID,
				},
			); err != nil {
				return fmt.Errorf("supersede active Commerce manifest: %w", err)
			}
			if len(verified.SigningKeys) != 1 {
				return ErrSiteAuthority
			}
			if _, err := rotateSiteSigningKeyInTx(
				ctx,
				queries,
				site,
				verified.SigningKeys[0],
			); err != nil {
				return err
			}
			activated, err = queries.ActivateCommerceManifest(
				ctx,
				generated.ActivateCommerceManifestParams{
					CompanyID: principal.CompanyID, ManifestID: manifest.ID,
					ExpectedManifestVersion: manifest.Version, SiteID: site.ID,
					CommerceAccountID:             account.ID,
					Origin:                        state.document.Manifest.Site.Origin,
					VerificationTokenSha256:       state.document.Digest[:],
					PortalReturnPath:              state.document.Manifest.Site.PortalReturnPath,
					ProviderPortalConfigurationID: portalConfigurationID,
					ExpectedSiteVersion:           site.Version,
					ExpectedOfferCount:            int32(len(state.offers)),
				},
			)
			if err != nil {
				return fmt.Errorf("activate Commerce manifest: %w", err)
			}
			if _, _, err := approval.ResolveCommerceManifestPublishInTx(
				ctx,
				queries,
				&pending,
				approval.CommerceManifestPublishResolution{
					CompanyID:       principal.CompanyID,
					ManifestID:      activated.ID,
					DecidedByUserID: principal.UserID,
					Digest:          hex.EncodeToString(activated.Digest),
					Now:             s.now(),
				},
			); err != nil {
				return commerceManifestApprovalPublishError(err)
			}
			return nil
		})
		if err == nil {
			for _, group := range events.GroupByTopic(pending) {
				s.bus.Publish(ctx, group.Topic, group.Events...)
			}
			return activated, nil
		}
		if !isPostgresDeadlock(err) {
			return generated.CommerceManifest{}, err
		}
	}
	return generated.CommerceManifest{}, errors.Join(ErrGatewayBusy, err)
}

func commerceManifestApprovalPublishError(err error) error {
	if errors.Is(err, approval.ErrApprovalInvalidInput) {
		return errors.Join(ErrInvalidStore, err)
	}
	return errors.Join(ErrManifestNotApprovable, err)
}

func (s *DefaultCommerceService) failCatalogPublish(
	ctx context.Context,
	state catalogPublishState,
	code string,
) error {
	ctx, cancel := boundedFinalizationContext(ctx)
	defer cancel()
	if state.operation.ID == uuid.Nil || state.manifest.ID == uuid.Nil {
		return ErrInvalidStore
	}
	return s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		manifest, err := queries.GetCommerceManifestForUpdate(
			ctx,
			generated.GetCommerceManifestForUpdateParams{
				CompanyID:  state.manifest.CompanyID,
				ManifestID: state.manifest.ID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return errStaleCatalogPublisher
		}
		if err != nil {
			return fmt.Errorf("lock failed Commerce manifest: %w", err)
		}
		if manifest.Version != state.manifest.Version || manifest.State != "publishing" ||
			subtle.ConstantTimeCompare(manifest.Digest, state.manifest.Digest) != 1 {
			return errStaleCatalogPublisher
		}
		operation, err := queries.GetCommerceGatewayOperationForUpdate(
			ctx,
			generated.GetCommerceGatewayOperationForUpdateParams{
				ID: state.operation.ID, CompanyID: state.manifest.CompanyID,
				SiteID: state.site.ID, OperationType: "catalog_publish",
				OperationID: state.manifest.ID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return errStaleCatalogPublisher
		}
		if err != nil {
			return fmt.Errorf("lock failed Commerce catalog operation: %w", err)
		}
		if operation.Version != state.operation.Version || operation.State != state.operation.State {
			return errStaleCatalogPublisher
		}
		switch operation.State {
		case "leased":
			if !operation.LeaseID.Valid || operation.LeaseID.UUID != state.leaseID {
				return errStaleCatalogPublisher
			}
			_, err = queries.ReleaseCommerceGatewayOperationForRetry(
				ctx,
				generated.ReleaseCommerceGatewayOperationForRetryParams{
					LastErrorCode: code, ID: operation.ID, CompanyID: state.manifest.CompanyID,
					SiteID: state.site.ID, LeaseID: state.leaseID,
					ExpectedVersion: operation.Version,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return errStaleCatalogPublisher
			}
			if err != nil {
				return fmt.Errorf("release failed Commerce catalog operation: %w", err)
			}
		case "succeeded":
			if state.operation.State != "succeeded" {
				return errStaleCatalogPublisher
			}
		default:
			return errStaleCatalogPublisher
		}
		_, err = queries.MarkCommerceManifestApplyFailed(
			ctx,
			generated.MarkCommerceManifestApplyFailedParams{
				ApplyErrorCode: code, CompanyID: state.manifest.CompanyID, SiteID: state.site.ID,
				ManifestID: state.manifest.ID, Digest: state.manifest.Digest,
				ExpectedVersion: state.manifest.Version,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return errStaleCatalogPublisher
		}
		if err != nil {
			return fmt.Errorf("mark Commerce manifest apply failed: %w", err)
		}
		return nil
	})
}

func isSiteAuthorityConflict(err error) bool {
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != "23505" {
		return false
	}
	switch databaseError.ConstraintName {
	case "commerce_sites_active_origin_key":
		return true
	default:
		return false
	}
}

func isPostgresDeadlock(err error) bool {
	var databaseError *pgconn.PgError
	return errors.As(err, &databaseError) && databaseError.Code == "40P01"
}

func catalogPublishError(original, persistence error) error {
	if persistence == nil || errors.Is(persistence, errStaleCatalogPublisher) {
		return original
	}
	return errors.Join(original, persistence)
}

func authorizeRecentApproval(
	ctx context.Context,
	queries *generated.Queries,
	principal auth.UserPrincipal,
) error {
	_, err := queries.AuthorizeRecentCommerceApprovalSession(
		ctx,
		generated.AuthorizeRecentCommerceApprovalSessionParams{
			SessionID: principal.SessionID, ActorUserID: principal.UserID,
			MaximumAgeSeconds: int64(maximumApprovalSessionAge / time.Second),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrApprovalExpired
	}
	if err != nil {
		return fmt.Errorf("authorize recent Commerce approval session: %w", err)
	}
	return nil
}

func manifestProductGroups(offers []ManifestOffer) []manifestProductGroup {
	byKey := make(map[string]*manifestProductGroup)
	for _, offer := range offers {
		key := "one_time:" + offer.Alias
		if offer.Type == OfferTypeSubscription {
			key = "subscription:" + offer.PlanGroup
		}
		group := byKey[key]
		if group == nil {
			group = &manifestProductGroup{key: key, displayName: offer.DisplayName}
			byKey[key] = group
		}
		group.offerAliases = append(group.offerAliases, offer.Alias)
	}
	result := make([]manifestProductGroup, 0, len(byKey))
	for _, group := range byKey {
		slices.Sort(group.offerAliases)
		result = append(result, *group)
	}
	slices.SortFunc(result, func(left, right manifestProductGroup) int {
		return strings.Compare(left.key, right.key)
	})
	return result
}

func existingGroupProduct(
	group manifestProductGroup,
	rows map[string]generated.CommerceOffer,
) string {
	productID := ""
	for _, alias := range group.offerAliases {
		row := rows[alias]
		if !row.ProviderProductID.Valid {
			continue
		}
		if productID != "" && productID != row.ProviderProductID.String {
			return ""
		}
		productID = row.ProviderProductID.String
	}
	return productID
}

func catalogIdempotencyKey(resource string, manifestID uuid.UUID, discriminator string) string {
	digest := sha256.Sum256([]byte(discriminator))
	return "boardly:catalog:" + resource + ":v1:" + manifestID.String() + ":" +
		hex.EncodeToString(digest[:8])
}

func cloneStringMap(source map[string]string, key, value string) map[string]string {
	result := make(map[string]string, len(source)+1)
	for sourceKey, sourceValue := range source {
		result[sourceKey] = sourceValue
	}
	result[key] = value
	return result
}

func nullableText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func nullableUUID(value uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: value, Valid: value != uuid.Nil}
}

func offerMatchesManifest(
	row generated.CommerceOffer,
	offer ManifestOffer,
	site generated.CommerceSite,
) bool {
	return row.CompanyID == site.CompanyID && row.SiteID == site.ID &&
		row.Environment == site.Environment && row.Alias == offer.Alias &&
		row.DisplayName == offer.DisplayName && row.OfferType == string(offer.Type) &&
		row.PlanGroupAlias == nullableText(offer.PlanGroup) &&
		row.EntitlementKey == offer.Entitlement && row.Amount == offer.Amount &&
		row.Currency == offer.Currency && row.RecurringInterval == nullableText(offer.Interval) &&
		row.AutomaticTax == offer.AutomaticTax && row.SuccessPath == offer.SuccessPath &&
		row.CancelPath == offer.CancelPath
}
