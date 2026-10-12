package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/runchannel"
)

const (
	commerceDevLeaseLifetime = 5 * time.Minute
	commerceDevJWTLifetime   = 55 * time.Second
)

var (
	commerceDevBodyDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commerceDevNoncePattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

var (
	errCommerceDevLeaseInvalid = errors.New("local Commerce signing lease is invalid")
	errCommerceDevSignInvalid  = errors.New("local Commerce signing request is invalid")
)

type commerceSigningKeyRegistrar interface {
	RegisterLocalCommerceSigningKey(
		ctx context.Context,
		runAuthToken string,
		companyID string,
		siteAlias string,
		request RegisterLocalCommerceSigningKeyRequest,
	) (RegisterLocalCommerceSigningKeyResponse, error)
}

// CommerceDevLeaseResponse is injected into a child process as non-secret
// Site configuration plus one short-lived signer capability.
type CommerceDevLeaseResponse struct {
	SchemaVersion    int               `json:"schemaVersion"`
	LeaseID          string            `json:"leaseId"`
	SignerURL        string            `json:"signerUrl"`
	SignerCapability string            `json:"signerCapability"`
	ExpiresAt        time.Time         `json:"expiresAt"`
	APIBaseURL       string            `json:"apiBaseUrl"`
	SiteOrigin       string            `json:"siteOrigin"`
	SiteID           string            `json:"siteId"`
	Environment      string            `json:"environment"`
	KeyID            string            `json:"keyId"`
	PublicJWK        CommercePublicJWK `json:"publicJwk"`
}

// CommerceDevSignRequest is the only input a local website may ask the
// daemon to sign. Site authority is pinned by the lease, never caller input.
type CommerceDevSignRequest struct {
	SchemaVersion         int      `json:"schemaVersion"`
	Subject               string   `json:"subject"`
	SessionID             string   `json:"sessionId"`
	AuthenticatedAt       string   `json:"authenticatedAt"`
	AuthenticationMethods []string `json:"authenticationMethods"`
	Method                string   `json:"method"`
	Path                  string   `json:"path"`
	OperationID           string   `json:"operationId"`
	BodySHA256            string   `json:"bodySha256"`
	Nonce                 string   `json:"nonce,omitempty"`
}

// CommerceDevSignResponse contains one strict ES256 compact JWT.
type CommerceDevSignResponse struct {
	SchemaVersion int       `json:"schemaVersion"`
	Token         string    `json:"token"`
	ExpiresAt     time.Time `json:"expiresAt"`
	KeyID         string    `json:"keyId"`
}

// CommerceDevSignerMetadata lets the server-only kit render the localhost
// well-known authority document without putting a JWK into environment text.
type CommerceDevSignerMetadata struct {
	SchemaVersion int               `json:"schemaVersion"`
	Environment   string            `json:"environment"`
	KeyID         string            `json:"keyId"`
	PublicJWK     CommercePublicJWK `json:"publicJwk"`
}

type commerceDevLeaseState struct {
	leaseID           string
	capabilityKey     [sha256.Size]byte
	ownerPID          int
	expiresAt         time.Time
	siteID            string
	environment       string
	origin            string
	keyID             string
	publicJWK         CommercePublicJWK
	apiBaseURL        string
	key               *ecdsa.PrivateKey
	scopedBootstrap   bool
	bootstrapSourceID commerceDevBootstrapKey
}

type commerceDevKeyBinding struct {
	companyID string
	siteAlias string
	origin    string
}

type commerceDevKeyState struct {
	key         *ecdsa.PrivateKey
	publicJWK   CommercePublicJWK
	fingerprint string
}

type commerceDevService struct {
	mu              sync.Mutex
	registrar       commerceSigningKeyRegistrar
	serverAuthority string
	keys            map[commerceDevKeyBinding]*commerceDevKeyState
	leasesByID      map[string]*commerceDevLeaseState
	leasesByKey     map[[sha256.Size]byte]*commerceDevLeaseState
	now             func() time.Time
	processAlive    func(int) bool
}

func newCommerceDevService(
	registrar commerceSigningKeyRegistrar,
	serverAuthority string,
) *commerceDevService {
	return &commerceDevService{
		registrar:       registrar,
		serverAuthority: serverAuthority,
		keys:            make(map[commerceDevKeyBinding]*commerceDevKeyState),
		leasesByID:      make(map[string]*commerceDevLeaseState),
		leasesByKey:     make(map[[sha256.Size]byte]*commerceDevLeaseState),
		now:             time.Now,
		processAlive:    commerceDevProcessAlive,
	}
}

func (s *commerceDevService) createLeaseForAuthority(
	ctx context.Context,
	companyID string,
	site CommerceDevManifestSite,
	ownerPID int,
	localBaseURL string,
	authority commerceDevBootstrapAuthority,
) (CommerceDevLeaseResponse, error) {
	if s == nil || s.registrar == nil || !validCommerceCompanyID(companyID) ||
		!validCommerceDevManifestSite(site) ||
		ownerPID <= 0 || !s.processAlive(ownerPID) || !runchannel.ValidEndpoint(localBaseURL) ||
		!authority.scoped || authority.companyID != companyID || authority.runID == uuid.Nil ||
		!validCommerceCompanyID(authority.taskID) ||
		!validRunScopedAuthTokenValue(authority.runAuthToken) {
		return CommerceDevLeaseResponse{}, errCommerceDevLeaseInvalid
	}
	binding := commerceDevKeyBinding{
		companyID: companyID,
		siteAlias: site.Alias,
		origin:    site.Origin,
	}
	keyState, err := s.keyForBinding(binding)
	if err != nil {
		return CommerceDevLeaseResponse{}, err
	}
	registrationRequest := RegisterLocalCommerceSigningKeyRequest{
		SchemaVersion: 1,
		Origin:        site.Origin,
		PublicJWK:     keyState.publicJWK,
		Fingerprint:   keyState.fingerprint,
	}
	registered, err := s.registrar.RegisterLocalCommerceSigningKey(
		ctx,
		authority.runAuthToken,
		companyID,
		site.Alias,
		registrationRequest,
	)
	if commerceDevKeyExplicitlyRejected(err) {
		keyState, err = s.replaceRejectedKey(binding, keyState.fingerprint)
		if err != nil {
			return CommerceDevLeaseResponse{}, err
		}
		registrationRequest.PublicJWK = keyState.publicJWK
		registrationRequest.Fingerprint = keyState.fingerprint
		registered, err = s.registrar.RegisterLocalCommerceSigningKey(
			ctx,
			authority.runAuthToken,
			companyID,
			site.Alias,
			registrationRequest,
		)
	}
	if err != nil {
		return CommerceDevLeaseResponse{}, fmt.Errorf("register local Commerce signing key: %w", err)
	}
	apiBaseURL := strings.TrimRight(s.serverAuthority, "/") + "/api/v1"
	if registered.SchemaVersion != 1 || registered.Environment != "sandbox" ||
		registered.KeyID != keyState.fingerprint || !validCommerceAPIBaseURL(apiBaseURL) {
		return CommerceDevLeaseResponse{}, errors.New("server returned an invalid local Commerce Site binding")
	}
	registeredSiteID, err := uuid.Parse(registered.SiteID)
	if err != nil || registeredSiteID == uuid.Nil || registeredSiteID.String() != registered.SiteID {
		return CommerceDevLeaseResponse{}, errors.New("server returned an invalid local Commerce Site ID")
	}
	leaseID := uuid.NewString()
	capability, err := newCommerceDevCapability()
	if err != nil {
		return CommerceDevLeaseResponse{}, err
	}
	expiresAt := s.now().UTC().Add(commerceDevLeaseLifetime)
	state := &commerceDevLeaseState{
		leaseID:           leaseID,
		capabilityKey:     sha256.Sum256([]byte(capability)),
		ownerPID:          ownerPID,
		expiresAt:         expiresAt,
		siteID:            registered.SiteID,
		environment:       registered.Environment,
		origin:            site.Origin,
		keyID:             keyState.fingerprint,
		publicJWK:         keyState.publicJWK,
		apiBaseURL:        apiBaseURL,
		key:               keyState.key,
		scopedBootstrap:   authority.scoped,
		bootstrapSourceID: authority.bootstrapKey,
	}
	s.mu.Lock()
	s.pruneLocked(s.now())
	s.leasesByID[leaseID] = state
	s.leasesByKey[state.capabilityKey] = state
	s.mu.Unlock()
	return CommerceDevLeaseResponse{
		SchemaVersion:    1,
		LeaseID:          leaseID,
		SignerURL:        strings.TrimRight(localBaseURL, "/") + commerceDevSignPath,
		SignerCapability: capability,
		ExpiresAt:        expiresAt,
		APIBaseURL:       apiBaseURL,
		SiteOrigin:       site.Origin,
		SiteID:           registered.SiteID,
		Environment:      registered.Environment,
		KeyID:            keyState.fingerprint,
		PublicJWK:        keyState.publicJWK,
	}, nil
}

func (s *commerceDevService) keyForBinding(
	binding commerceDevKeyBinding,
) (*commerceDevKeyState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.keys[binding]; current != nil {
		return current, nil
	}
	generated, err := generateCommerceDevKeyState()
	if err != nil {
		return nil, err
	}
	s.keys[binding] = generated
	return generated, nil
}

func (s *commerceDevService) replaceRejectedKey(
	binding commerceDevKeyBinding,
	rejectedFingerprint string,
) (*commerceDevKeyState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.keys[binding]; current != nil && current.fingerprint != rejectedFingerprint {
		return current, nil
	}
	generated, err := generateCommerceDevKeyState()
	if err != nil {
		return nil, err
	}
	s.keys[binding] = generated
	return generated, nil
}

func generateCommerceDevKeyState() (*commerceDevKeyState, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate in-memory local Commerce signing key: %w", err)
	}
	publicJWK, fingerprint, err := commercePublicJWK(key)
	if err != nil {
		return nil, err
	}
	return &commerceDevKeyState{
		key:         key,
		publicJWK:   publicJWK,
		fingerprint: fingerprint,
	}, nil
}

func (s *commerceDevService) revokeBootstrapLeases(sourceID commerceDevBootstrapKey) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, lease := range s.leasesByID {
		if lease.scopedBootstrap && lease.bootstrapSourceID == sourceID {
			s.removeLocked(lease)
		}
	}
}

func (s *commerceDevService) clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.keys)
	clear(s.leasesByID)
	clear(s.leasesByKey)
}

func commerceDevKeyExplicitlyRejected(err error) bool {
	var responseError *requestError
	return errors.As(err, &responseError) &&
		responseError.StatusCode == http.StatusConflict &&
		responseError.Code == "commerce_site_verification_failed"
}

func validCommerceCompanyID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}

func (s *commerceDevService) renewWithCapability(
	capability string,
	leaseID string,
	ownerPID int,
) (time.Time, error) {
	if !validCommerceDevCapability(capability) {
		return time.Time{}, errCommerceDevLeaseInvalid
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	lease := s.leasesByID[leaseID]
	capabilityMatches := lease != nil &&
		lease.capabilityKey == sha256.Sum256([]byte(capability))
	ownerAlive := lease != nil && s.processAlive(lease.ownerPID)
	if lease == nil || lease.ownerPID != ownerPID || !ownerAlive || !capabilityMatches {
		if lease != nil && !ownerAlive {
			s.removeLocked(lease)
		}
		return time.Time{}, errCommerceDevLeaseInvalid
	}
	lease.expiresAt = now.UTC().Add(commerceDevLeaseLifetime)
	return lease.expiresAt, nil
}

func (s *commerceDevService) revokeWithCapability(
	capability string,
	leaseID string,
	ownerPID int,
) error {
	if !validCommerceDevCapability(capability) {
		return errCommerceDevLeaseInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lease := s.leasesByID[leaseID]
	if lease == nil {
		return nil
	}
	capabilityMismatch := lease.capabilityKey != sha256.Sum256([]byte(capability))
	if lease.ownerPID != ownerPID || capabilityMismatch {
		return errCommerceDevLeaseInvalid
	}
	s.removeLocked(lease)
	return nil
}

func (s *commerceDevService) sign(
	capability string,
	request CommerceDevSignRequest,
) (CommerceDevSignResponse, error) {
	now := s.now().UTC()
	capabilityKey := sha256.Sum256([]byte(capability))
	s.mu.Lock()
	s.pruneLocked(now)
	lease := s.leasesByKey[capabilityKey]
	if lease == nil || !s.processAlive(lease.ownerPID) {
		if lease != nil {
			s.removeLocked(lease)
		}
		s.mu.Unlock()
		return CommerceDevSignResponse{}, errCommerceDevLeaseInvalid
	}
	leaseCopy := *lease
	s.mu.Unlock()
	if err := validateCommerceDevSignRequest(request, now); err != nil {
		return CommerceDevSignResponse{}, err
	}
	issuedAt := now.Truncate(time.Second)
	expiresAt := issuedAt.Add(commerceDevJWTLifetime)
	claims := commerceDevSiteRequestClaims{
		SchemaVersion:         1,
		Environment:           leaseCopy.environment,
		Origin:                leaseCopy.origin,
		SessionID:             request.SessionID,
		AuthenticatedAt:       request.AuthenticatedAt,
		AuthenticationMethods: request.AuthenticationMethods,
		Method:                request.Method,
		Path:                  request.Path,
		Action:                commerceDevAction(request.Path),
		OperationID:           request.OperationID,
		BodySHA256:            request.BodySHA256,
		Nonce:                 request.Nonce,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    leaseCopy.siteID,
			Subject:   request.Subject,
			Audience:  jwt.ClaimStrings{"boardly-site-commerce"},
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(issuedAt),
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			ID:        uuid.NewString(),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["typ"] = "boardly-site-request+jwt"
	token.Header["kid"] = leaseCopy.keyID
	signed, err := token.SignedString(leaseCopy.key)
	if err != nil {
		return CommerceDevSignResponse{}, fmt.Errorf("sign local Commerce request: %w", err)
	}
	return CommerceDevSignResponse{
		SchemaVersion: 1,
		Token:         signed,
		ExpiresAt:     expiresAt,
		KeyID:         leaseCopy.keyID,
	}, nil
}

func (s *commerceDevService) metadata(capability string) (CommerceDevSignerMetadata, error) {
	now := s.now()
	capabilityKey := sha256.Sum256([]byte(capability))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	lease := s.leasesByKey[capabilityKey]
	if lease == nil || !s.processAlive(lease.ownerPID) {
		if lease != nil {
			s.removeLocked(lease)
		}
		return CommerceDevSignerMetadata{}, errCommerceDevLeaseInvalid
	}
	return CommerceDevSignerMetadata{
		SchemaVersion: 1,
		Environment:   lease.environment,
		KeyID:         lease.keyID,
		PublicJWK:     lease.publicJWK,
	}, nil
}

type commerceDevSiteRequestClaims struct {
	SchemaVersion         int      `json:"schemaVersion"`
	Environment           string   `json:"environment"`
	Origin                string   `json:"origin"`
	SessionID             string   `json:"sessionId"`
	AuthenticatedAt       string   `json:"authenticatedAt"`
	AuthenticationMethods []string `json:"authenticationMethods"`
	Method                string   `json:"method"`
	Path                  string   `json:"path"`
	Action                string   `json:"action"`
	OperationID           string   `json:"operationId"`
	BodySHA256            string   `json:"bodySha256"`
	Nonce                 string   `json:"nonce,omitempty"`
	jwt.RegisteredClaims
}

func validateCommerceDevSignRequest(request CommerceDevSignRequest, now time.Time) error {
	if request.SchemaVersion != 1 || request.Method != http.MethodPost ||
		!validCommerceDevClaimValue(request.Subject, 255) ||
		!validCommerceDevClaimValue(request.SessionID, 255) ||
		!validCommerceOperationID(request.OperationID) ||
		!commerceDevBodyDigestPattern.MatchString(request.BodySHA256) ||
		len(request.AuthenticationMethods) == 0 || len(request.AuthenticationMethods) > 16 {
		return errCommerceDevSignInvalid
	}
	authenticatedAt, err := time.Parse(time.RFC3339, request.AuthenticatedAt)
	if err != nil || authenticatedAt.After(now.Add(5*time.Second)) {
		return errCommerceDevSignInvalid
	}
	seenMethods := make(map[string]struct{}, len(request.AuthenticationMethods))
	for _, method := range request.AuthenticationMethods {
		if !validCommerceDevClaimValue(method, 64) {
			return errCommerceDevSignInvalid
		}
		if _, exists := seenMethods[method]; exists {
			return errCommerceDevSignInvalid
		}
		seenMethods[method] = struct{}{}
	}
	switch request.Path {
	case "/api/v1/site-commerce/checkout-sessions":
		if !commerceDevNoncePattern.MatchString(request.Nonce) {
			return errCommerceDevSignInvalid
		}
	case "/api/v1/site-commerce/portal-sessions":
		if !commerceDevNoncePattern.MatchString(request.Nonce) || now.Sub(authenticatedAt) > 5*time.Minute {
			return errCommerceDevSignInvalid
		}
	case "/api/v1/site-commerce/entitlements/verify":
		if request.Nonce != "" {
			return errCommerceDevSignInvalid
		}
	default:
		return errCommerceDevSignInvalid
	}
	return nil
}

func validCommerceOperationID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func commerceDevAction(path string) string {
	switch path {
	case "/api/v1/site-commerce/checkout-sessions":
		return "checkout"
	case "/api/v1/site-commerce/portal-sessions":
		return "portal"
	case "/api/v1/site-commerce/entitlements/verify":
		return "entitlements"
	default:
		return ""
	}
}

func validCommerceDevClaimValue(value string, maximum int) bool {
	if len(value) == 0 || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func (s *commerceDevService) pruneLocked(now time.Time) {
	for _, lease := range s.leasesByID {
		if !now.Before(lease.expiresAt) || !s.processAlive(lease.ownerPID) {
			s.removeLocked(lease)
		}
	}
}

func (s *commerceDevService) removeLocked(lease *commerceDevLeaseState) {
	delete(s.leasesByID, lease.leaseID)
	delete(s.leasesByKey, lease.capabilityKey)
}

func newCommerceDevCapability() (string, error) {
	return newLocalCapability()
}

func validCommerceDevCapability(value string) bool {
	return runchannel.ValidCapability(value)
}
