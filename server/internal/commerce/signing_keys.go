package commerce

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const (
	siteChallengeTTL       = time.Minute
	challengeSiteRateLimit = 300
	challengeIPRateLimit   = 60
)

var ErrSigningKeyNotFound = errors.New("Commerce Site signing key not found")

type SigningKeyState string

const (
	SigningKeyStatePending  SigningKeyState = "pending"
	SigningKeyStateActive   SigningKeyState = "active"
	SigningKeyStateRetiring SigningKeyState = "retiring"
	SigningKeyStateRevoked  SigningKeyState = "revoked"
)

type SigningKeySummary struct {
	KeyID       string          `json:"keyId"`
	Fingerprint string          `json:"fingerprint"`
	State       SigningKeyState `json:"state"`
	ActivatedAt *time.Time      `json:"activatedAt,omitempty"`
	RetireAt    *time.Time      `json:"retireAt,omitempty"`
}

type SiteChallengeAction string

const (
	SiteChallengeActionCheckout SiteChallengeAction = "checkout"
	SiteChallengeActionPortal   SiteChallengeAction = "portal"
)

type SiteChallengeRequest struct {
	SiteID        uuid.UUID
	Action        SiteChallengeAction
	OperationID   uuid.UUID
	ClientAddress string
}

type SiteChallenge struct {
	Nonce     string    `json:"nonce"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type LocalSigningKeyRequest struct {
	SiteAlias   string
	Origin      string
	PublicKey   SitePublicKey
	Fingerprint string
}

type TrustedLocalSigningKeyContext struct {
	CompanyID uuid.UUID
	TaskID    uuid.UUID
	RunID     uuid.UUID
}

type LocalSigningKeyRegistration struct {
	SiteID      uuid.UUID
	Environment Environment
	KeyID       string
	State       SigningKeyState
}

type SyncSigningKeysRequest struct {
	SiteID uuid.UUID
}

type RevokeSigningKeyRequest struct {
	SiteID uuid.UUID
	KeyID  string
}

func (s *DefaultCommerceService) CreateSiteChallenge(
	ctx context.Context,
	input SiteChallengeRequest,
) (SiteChallenge, error) {
	if input.SiteID == uuid.Nil || input.OperationID == uuid.Nil ||
		(input.Action != SiteChallengeActionCheckout && input.Action != SiteChallengeActionPortal) ||
		!validSiteIdentityValue(input.ClientAddress, 255) {
		return SiteChallenge{}, ErrInvalidInput
	}
	site, err := s.store.Queries().GetActiveCommerceSiteByID(ctx, input.SiteID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SiteChallenge{}, ErrSiteNotFound
	}
	if err != nil {
		return SiteChallenge{}, fmt.Errorf("resolve Commerce Site challenge authority: %w", err)
	}
	now := s.now().UTC()
	environment := Environment(site.Environment)
	configuration := s.environments[environment]
	if !configuration.Configured || configuration.Gateway == nil {
		return SiteChallenge{}, ErrProviderUnavailable
	}
	if err := s.consumeDualRateLimits(
		ctx,
		site.CompanyID,
		site.ID,
		rateLimitScope{
			subjectHMAC: rateLimitScopeHMAC(
				s.customerHMACKey,
				site.ID,
				"ip:"+input.ClientAddress,
			),
			action: "challenge_ip", maximumRequests: challengeIPRateLimit,
		},
		rateLimitScope{
			subjectHMAC: rateLimitScopeHMAC(s.customerHMACKey, site.ID, "site"),
			action:      "challenge_site", maximumRequests: challengeSiteRateLimit,
		},
	); err != nil {
		return SiteChallenge{}, err
	}
	if _, err := s.store.Queries().PruneCommerceSiteChallenges(
		ctx,
		pgtype.Timestamptz{Time: now, Valid: true},
	); err != nil {
		return SiteChallenge{}, fmt.Errorf("prune Commerce Site challenges: %w", err)
	}
	rawNonce := make([]byte, 32)
	if _, err := io.ReadFull(s.random, rawNonce); err != nil {
		return SiteChallenge{}, errors.New("generate Commerce Site challenge")
	}
	nonce := base64.RawURLEncoding.EncodeToString(rawNonce)
	nonceDigest := sha256.Sum256([]byte(nonce))
	expiresAt := now.Add(siteChallengeTTL)
	_, err = s.store.Queries().InsertCommerceSiteChallenge(
		ctx,
		generated.InsertCommerceSiteChallengeParams{
			CompanyID: site.CompanyID, SiteID: site.ID, Environment: site.Environment,
			Action: string(input.Action), OperationID: input.OperationID,
			NonceSha256: nonceDigest[:],
			ExpiresAt:   pgtype.Timestamptz{Time: expiresAt, Valid: true},
		},
	)
	if err != nil {
		return SiteChallenge{}, fmt.Errorf("create Commerce Site challenge: %w", err)
	}
	return SiteChallenge{Nonce: nonce, ExpiresAt: expiresAt}, nil
}

func (s *DefaultCommerceService) RegisterLocalSigningKey(
	ctx context.Context,
	principal auth.UserPrincipal,
	trusted TrustedLocalSigningKeyContext,
	input LocalSigningKeyRequest,
) (LocalSigningKeyRegistration, error) {
	if principal.UserID == uuid.Nil || principal.CompanyID != trusted.CompanyID ||
		trusted.CompanyID == uuid.Nil ||
		trusted.TaskID == uuid.Nil || trusted.RunID == uuid.Nil ||
		!manifestAliasPattern.MatchString(input.SiteAlias) ||
		!validLoopbackHTTPOrigin(input.Origin) ||
		!base64URLSHA256Pattern.MatchString(input.Fingerprint) {
		return LocalSigningKeyRegistration{}, ErrInvalidInput
	}
	jwk, err := NewSitePublicJWK(input.PublicKey)
	if err != nil || jwk.KeyID != input.Fingerprint {
		return LocalSigningKeyRegistration{}, ErrSiteAuthority
	}
	var registration LocalSigningKeyRegistration
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		if _, err := lockDaemonCommerceMember(
			ctx,
			queries,
			principal.UserID,
			trusted.CompanyID,
		); err != nil {
			return err
		}
		site, err := queries.GetDaemonCommerceSiteByTask(
			ctx,
			generated.GetDaemonCommerceSiteByTaskParams{
				CompanyID: trusted.CompanyID,
				SiteAlias: input.SiteAlias,
				Origin:    input.Origin,
				TaskID:    trusted.TaskID,
				RunID:     trusted.RunID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSiteNotFound
		}
		if err != nil {
			return fmt.Errorf("resolve local Commerce Site: %w", err)
		}
		locked, err := queries.GetCommerceSiteForUpdate(
			ctx,
			generated.GetCommerceSiteForUpdateParams{CompanyID: trusted.CompanyID, SiteID: site.ID},
		)
		if err != nil {
			return fmt.Errorf("lock local Commerce Site: %w", err)
		}
		var key generated.CommerceSiteSigningKey
		if locked.Status == "active" {
			key, err = rotateSiteSigningKeyInTx(ctx, queries, locked, jwk)
		} else {
			key, err = registerPendingSiteSigningKeyInTx(ctx, queries, locked, jwk)
		}
		if err != nil {
			return err
		}
		registration = LocalSigningKeyRegistration{
			SiteID: site.ID, Environment: EnvironmentSandbox,
			KeyID: key.KeyID, State: SigningKeyState(key.State),
		}
		return nil
	})
	return registration, err
}

func lockDaemonCommerceMember(
	ctx context.Context,
	queries *generated.Queries,
	userID uuid.UUID,
	companyID uuid.UUID,
) (generated.CompanyMember, error) {
	if _, err := queries.LockCompanyForMembershipMutation(ctx, companyID); errors.Is(err, pgx.ErrNoRows) {
		return generated.CompanyMember{}, ErrAccessForbidden
	} else if err != nil {
		return generated.CompanyMember{}, fmt.Errorf(
			"lock Company for local Commerce key registration: %w",
			err,
		)
	}
	membership, err := queries.GetCompanyMemberForUpdate(
		ctx,
		generated.GetCompanyMemberForUpdateParams{CompanyID: companyID, UserID: userID},
	)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && membership.Status != "active" {
		return generated.CompanyMember{}, ErrAccessForbidden
	}
	if err != nil {
		return generated.CompanyMember{}, fmt.Errorf("lock local Commerce key acting member: %w", err)
	}
	return membership, nil
}

func (s *DefaultCommerceService) SyncSigningKeys(
	ctx context.Context,
	principal auth.UserPrincipal,
	input SyncSigningKeysRequest,
) ([]SigningKeySummary, error) {
	if err := validatePrincipalPermission(principal, auth.PermissionCommerceManage); err != nil {
		return nil, err
	}
	if input.SiteID == uuid.Nil || s.siteAuthority == nil {
		return nil, ErrInvalidInput
	}
	site, err := s.store.Queries().GetCommerceSiteByID(
		ctx,
		generated.GetCommerceSiteByIDParams{CompanyID: principal.CompanyID, SiteID: input.SiteID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSiteNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get Commerce Site for key sync: %w", err)
	}
	if site.Status != "active" || !validHTTPSOrigin(site.Origin) {
		return nil, ErrSiteAuthority
	}
	manifest, err := s.store.Queries().GetActiveCommerceManifestForSite(
		ctx,
		generated.GetActiveCommerceManifestForSiteParams{
			CompanyID: principal.CompanyID, SiteID: site.ID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrManifestNotFound
		}
		return nil, fmt.Errorf("get active Commerce manifest for key sync: %w", err)
	}
	verified, err := s.siteAuthority.VerifySite(ctx, SiteVerificationInput{
		Origin: site.Origin, SiteAlias: site.SiteAlias,
		ManifestDigest: hex.EncodeToString(manifest.Digest),
	})
	if err != nil || len(verified.SigningKeys) != 1 {
		return nil, ErrSiteAuthority
	}
	var summaries []SigningKeySummary
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		if err := lockManagingMember(ctx, queries, principal); err != nil {
			return err
		}
		locked, err := queries.GetCommerceSiteForUpdate(
			ctx,
			generated.GetCommerceSiteForUpdateParams{
				CompanyID: principal.CompanyID, SiteID: site.ID,
			},
		)
		if err != nil || locked.Version != site.Version || locked.Status != "active" {
			return ErrManifestConflict
		}
		current, err := queries.GetActiveCommerceManifestForSite(
			ctx,
			generated.GetActiveCommerceManifestForSiteParams{
				CompanyID: principal.CompanyID, SiteID: site.ID,
			},
		)
		if err != nil || !equalDigestBytes(current.Digest, manifest.Digest) {
			return ErrManifestConflict
		}
		if _, err := rotateSiteSigningKeyInTx(
			ctx,
			queries,
			locked,
			verified.SigningKeys[0],
		); err != nil {
			return err
		}
		keys, err := queries.ListCommerceSiteSigningKeys(
			ctx,
			generated.ListCommerceSiteSigningKeysParams{
				CompanyID: principal.CompanyID, SiteID: site.ID,
			},
		)
		if err != nil {
			return fmt.Errorf("list synced Commerce Site signing keys: %w", err)
		}
		summaries = signingKeySummaries(keys)
		return nil
	})
	return summaries, err
}

func (s *DefaultCommerceService) RevokeSigningKey(
	ctx context.Context,
	principal auth.UserPrincipal,
	input RevokeSigningKeyRequest,
) (SigningKeySummary, error) {
	if err := validatePrincipalPermission(principal, auth.PermissionCommerceManage); err != nil {
		return SigningKeySummary{}, err
	}
	if input.SiteID == uuid.Nil || !base64URLSHA256Pattern.MatchString(input.KeyID) {
		return SigningKeySummary{}, ErrInvalidInput
	}
	var revoked generated.CommerceSiteSigningKey
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		if err := lockManagingMember(ctx, queries, principal); err != nil {
			return err
		}
		if _, err := queries.GetCommerceSiteForUpdate(
			ctx,
			generated.GetCommerceSiteForUpdateParams{
				CompanyID: principal.CompanyID, SiteID: input.SiteID,
			},
		); errors.Is(err, pgx.ErrNoRows) {
			return ErrSiteNotFound
		} else if err != nil {
			return fmt.Errorf("lock Commerce Site for key revocation: %w", err)
		}
		var err error
		revoked, err = queries.RevokeCommerceSiteSigningKey(
			ctx,
			generated.RevokeCommerceSiteSigningKeyParams{
				CompanyID: principal.CompanyID, SiteID: input.SiteID, KeyID: input.KeyID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			revoked, err = queries.GetCommerceSiteSigningKeyByFingerprint(
				ctx,
				generated.GetCommerceSiteSigningKeyByFingerprintParams{
					CompanyID:   principal.CompanyID,
					SiteID:      input.SiteID,
					Fingerprint: input.KeyID,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrSigningKeyNotFound
			}
			if err != nil {
				return fmt.Errorf("reload revoked Commerce Site signing key: %w", err)
			}
			if revoked.State != string(SigningKeyStateRevoked) {
				return ErrSigningKeyNotFound
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("revoke Commerce Site signing key: %w", err)
		}
		return nil
	})
	return signingKeySummary(revoked), err
}

func registerPendingSiteSigningKeyInTx(
	ctx context.Context,
	queries *generated.Queries,
	site generated.CommerceSite,
	jwk SitePublicJWK,
) (generated.CommerceSiteSigningKey, error) {
	raw, err := marshalSitePublicJWK(jwk)
	if err != nil {
		return generated.CommerceSiteSigningKey{}, err
	}
	if _, err := queries.RevokePendingCommerceSiteSigningKeys(
		ctx,
		generated.RevokePendingCommerceSiteSigningKeysParams{
			CompanyID: site.CompanyID, SiteID: site.ID, Fingerprint: jwk.KeyID,
		},
	); err != nil {
		return generated.CommerceSiteSigningKey{}, fmt.Errorf(
			"replace pending Commerce Site signing key: %w",
			err,
		)
	}
	existing, err := queries.GetCommerceSiteSigningKeyByFingerprint(
		ctx,
		generated.GetCommerceSiteSigningKeyByFingerprintParams{
			CompanyID: site.CompanyID, SiteID: site.ID, Fingerprint: jwk.KeyID,
		},
	)
	if err == nil {
		storedJWK, parseErr := unmarshalSitePublicJWK(existing.PublicJwk)
		if existing.State != string(SigningKeyStatePending) || parseErr != nil || storedJWK != jwk {
			return generated.CommerceSiteSigningKey{}, ErrSiteAuthority
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return generated.CommerceSiteSigningKey{}, fmt.Errorf("read Commerce Site signing key: %w", err)
	}
	created, err := queries.InsertPendingCommerceSiteSigningKey(
		ctx,
		generated.InsertPendingCommerceSiteSigningKeyParams{
			CompanyID: site.CompanyID, SiteID: site.ID, Environment: site.Environment,
			KeyID: jwk.KeyID, Fingerprint: jwk.KeyID, PublicJwk: raw,
		},
	)
	if err != nil {
		return generated.CommerceSiteSigningKey{}, fmt.Errorf(
			"insert pending Commerce Site signing key: %w",
			err,
		)
	}
	return created, nil
}

func rotateSiteSigningKeyInTx(
	ctx context.Context,
	queries *generated.Queries,
	site generated.CommerceSite,
	jwk SitePublicJWK,
) (generated.CommerceSiteSigningKey, error) {
	existing, err := queries.GetCommerceSiteSigningKeyByFingerprint(
		ctx,
		generated.GetCommerceSiteSigningKeyByFingerprintParams{
			CompanyID: site.CompanyID, SiteID: site.ID, Fingerprint: jwk.KeyID,
		},
	)
	if err == nil {
		storedJWK, parseErr := unmarshalSitePublicJWK(existing.PublicJwk)
		if parseErr != nil || storedJWK != jwk {
			return generated.CommerceSiteSigningKey{}, ErrSiteAuthority
		}
		if existing.State == string(SigningKeyStateActive) {
			return existing, nil
		}
		if existing.State != string(SigningKeyStatePending) {
			return generated.CommerceSiteSigningKey{}, ErrSiteAuthority
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return generated.CommerceSiteSigningKey{}, fmt.Errorf(
			"read rotating Commerce Site signing key: %w",
			err,
		)
	}
	key, err := registerPendingSiteSigningKeyInTx(ctx, queries, site, jwk)
	if err != nil {
		return generated.CommerceSiteSigningKey{}, err
	}
	if key.State == string(SigningKeyStateRetiring) || key.State == string(SigningKeyStateRevoked) {
		return generated.CommerceSiteSigningKey{}, ErrSiteAuthority
	}
	params := generated.RevokeRetiringCommerceSiteSigningKeysParams{
		CompanyID: site.CompanyID, SiteID: site.ID, Fingerprint: jwk.KeyID,
	}
	if _, err := queries.RevokeRetiringCommerceSiteSigningKeys(ctx, params); err != nil {
		return generated.CommerceSiteSigningKey{}, fmt.Errorf("revoke old retiring Commerce Site key: %w", err)
	}
	if _, err := queries.RetireActiveCommerceSiteSigningKeys(
		ctx,
		generated.RetireActiveCommerceSiteSigningKeysParams{
			CompanyID: site.CompanyID, SiteID: site.ID, Fingerprint: jwk.KeyID,
		},
	); err != nil {
		return generated.CommerceSiteSigningKey{}, fmt.Errorf("retire active Commerce Site key: %w", err)
	}
	activated, err := queries.ActivateCommerceSiteSigningKey(
		ctx,
		generated.ActivateCommerceSiteSigningKeyParams{
			CompanyID: site.CompanyID, SiteID: site.ID, Fingerprint: jwk.KeyID,
		},
	)
	if err != nil {
		return generated.CommerceSiteSigningKey{}, fmt.Errorf("activate Commerce Site signing key: %w", err)
	}
	return activated, nil
}

func signingKeySummaries(rows []generated.CommerceSiteSigningKey) []SigningKeySummary {
	result := make([]SigningKeySummary, 0, len(rows))
	for _, row := range rows {
		result = append(result, signingKeySummary(row))
	}
	return result
}

func signingKeySummary(row generated.CommerceSiteSigningKey) SigningKeySummary {
	return SigningKeySummary{
		KeyID: row.KeyID, Fingerprint: row.Fingerprint,
		State:       SigningKeyState(row.State),
		ActivatedAt: manifestUTCTimePointer(row.ActivatedAt),
		RetireAt:    manifestUTCTimePointer(row.RetireAt),
	}
}

func rateLimitScopeHMAC(key []byte, siteID uuid.UUID, scope string) []byte {
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write([]byte("boardly-commerce-rate-limit-v1\x00"))
	_, _ = digest.Write(siteID[:])
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(scope))
	return digest.Sum(nil)
}

func equalDigestBytes(left, right []byte) bool {
	return hmac.Equal(left, right)
}
