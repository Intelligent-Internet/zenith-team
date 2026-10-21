package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

const (
	maxOAuthReturnPathBytes = 2048
	googleOAuthProvider     = "google"
)

var desktopOAuthRedirectPattern = regexp.MustCompile(`^http://127\.0\.0\.1:([0-9]{4,5})/$`)

type OAuthTokenIssuer interface {
	IssueRefresh() (IssuedRefresh, error)
	IssueAccess(uuid.UUID, uuid.UUID) (string, time.Time, error)
}

type OAuthServiceOptions struct {
	Store          *db.Store
	Provider       OAuthProvider
	Tokens         OAuthTokenIssuer
	WebRedirectURI string
	Now            func() time.Time
	Random         io.Reader
	FlowTTL        time.Duration
	// AllowInsecureLoopbackWebRedirect permits an HTTP Web callback only when
	// its host is loopback. Deployment configuration must opt in explicitly.
	AllowInsecureLoopbackWebRedirect bool
}

// OAuthService owns the Web and desktop provider login flows, from the
// authorization redirect through the callback that mints a session.
type OAuthService interface {
	ProviderEnabled() bool
	WebEnabled() bool
	DesktopEnabled() bool
	StartWeb(context.Context, WebOAuthStartInput) (WebOAuthStartResult, error)
	StartDesktop(context.Context, DesktopOAuthStartInput) (DesktopOAuthStartResult, error)
	CompleteWeb(context.Context, WebOAuthCompleteInput) (WebOAuthCompleteResult, error)
	CompleteDesktop(context.Context, DesktopOAuthCompleteInput) (Result, error)
}

var _ OAuthService = (*DefaultOAuthService)(nil)

type DefaultOAuthService struct {
	store          *db.Store
	provider       OAuthProvider
	tokens         OAuthTokenIssuer
	webRedirectURI string
	now            func() time.Time
	random         io.Reader
	flowTTL        time.Duration
}

type oauthProviderClientCapability interface {
	ClientEnabled(ClientKind) bool
}

func oauthProviderClientEnabled(provider OAuthProvider, client ClientKind) bool {
	if provider == nil {
		return false
	}
	capability, ok := provider.(oauthProviderClientCapability)
	if !ok {
		// OAuth providers predating per-surface configuration are complete for
		// both clients by contract.
		return true
	}
	return capability.ClientEnabled(client)
}

type WebOAuthStartInput struct {
	ReturnPath     string
	ClientMetadata OAuthClientMetadata
}

type WebOAuthStartResult struct {
	AuthorizationURL string
	BrowserBinding   string
	ExpiresAt        time.Time
}

type DesktopOAuthStartInput struct {
	RedirectURI    string
	CodeChallenge  string
	ClientMetadata OAuthClientMetadata
}

type DesktopOAuthStartResult struct {
	FlowID           uuid.UUID
	State            string
	AuthorizationURL string
	ExpiresAt        time.Time
}

type WebOAuthCompleteInput struct {
	Code           string
	ProviderError  string
	State          string
	BrowserBinding string
}

type WebOAuthCompleteResult struct {
	Result     Result
	ReturnPath string
}

type DesktopOAuthCompleteInput struct {
	FlowID        uuid.UUID
	Code          string
	ProviderError string
	State         string
	RedirectURI   string
	Verifier      string
}

func NewOAuthService(options OAuthServiceOptions) (*DefaultOAuthService, error) {
	if options.Store == nil || options.Tokens == nil {
		return nil, errors.New("OAuth store and token issuer are required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.FlowTTL == 0 {
		options.FlowTTL = 10 * time.Minute
	}
	if options.FlowTTL < 0 {
		return nil, errors.New("OAuth flow TTL must be positive")
	}
	if oauthProviderClientEnabled(options.Provider, ClientWeb) {
		if err := validateWebOAuthRedirect(
			options.WebRedirectURI,
			options.AllowInsecureLoopbackWebRedirect,
		); err != nil {
			return nil, err
		}
	}
	return &DefaultOAuthService{
		store:          options.Store,
		provider:       options.Provider,
		tokens:         options.Tokens,
		webRedirectURI: options.WebRedirectURI,
		now:            options.Now,
		random:         options.Random,
		flowTTL:        options.FlowTTL,
	}, nil
}

// ProviderEnabled reports whether either OAuth client is configured. It
// exposes no provider identity or credential details.
func (s *DefaultOAuthService) ProviderEnabled() bool {
	return s.WebEnabled() || s.DesktopEnabled()
}

// WebEnabled reports whether the provider has a browser OAuth client.
func (s *DefaultOAuthService) WebEnabled() bool {
	return s != nil && oauthProviderClientEnabled(s.provider, ClientWeb)
}

// DesktopEnabled reports whether the provider has a desktop OAuth client.
func (s *DefaultOAuthService) DesktopEnabled() bool {
	return s != nil && oauthProviderClientEnabled(s.provider, ClientDesktop)
}

func (s *DefaultOAuthService) StartWeb(ctx context.Context, input WebOAuthStartInput) (WebOAuthStartResult, error) {
	if s == nil || !oauthProviderClientEnabled(s.provider, ClientWeb) {
		return WebOAuthStartResult{}, ErrOAuthNotConfigured
	}
	returnPath, err := normalizeOAuthReturnPath(input.ReturnPath)
	if err != nil {
		return WebOAuthStartResult{}, err
	}
	if err := validateOAuthClientMetadata(ClientWeb, input.ClientMetadata); err != nil {
		return WebOAuthStartResult{}, err
	}
	state, err := s.randomValue()
	if err != nil {
		return WebOAuthStartResult{}, fmt.Errorf("generate OAuth state: %w", err)
	}
	nonce, err := s.randomValue()
	if err != nil {
		return WebOAuthStartResult{}, fmt.Errorf("generate OAuth nonce: %w", err)
	}
	browserBinding, err := s.randomValue()
	if err != nil {
		return WebOAuthStartResult{}, fmt.Errorf("generate OAuth browser binding: %w", err)
	}
	expiresAt := s.now().UTC().Add(s.flowTTL)
	authorizationURL, err := s.provider.AuthorizationURL(OAuthAuthorizationRequest{
		Client:      ClientWeb,
		RedirectURI: s.webRedirectURI,
		State:       state,
		Nonce:       nonce,
	})
	if err != nil {
		return WebOAuthStartResult{}, stableOAuthProviderError(err)
	}
	stateHash := sha256.Sum256([]byte(state))
	nonceHash := sha256.Sum256([]byte(nonce))
	browserBindingHash := sha256.Sum256([]byte(browserBinding))
	if _, err := createOAuthLoginFlow(ctx, s.store.Queries(), NewOAuthLoginFlow{
		Provider:           googleOAuthProvider,
		Client:             ClientWeb,
		StateHash:          stateHash[:],
		NonceHash:          nonceHash[:],
		BrowserBindingHash: browserBindingHash[:],
		RedirectURI:        s.webRedirectURI,
		ReturnPath:         &returnPath,
		ClientMetadata:     input.ClientMetadata,
		ExpiresAt:          expiresAt,
	}); err != nil {
		return WebOAuthStartResult{}, fmt.Errorf("persist Web OAuth flow: %w", err)
	}
	return WebOAuthStartResult{
		AuthorizationURL: authorizationURL,
		BrowserBinding:   browserBinding,
		ExpiresAt:        expiresAt,
	}, nil
}

func (s *DefaultOAuthService) StartDesktop(
	ctx context.Context,
	input DesktopOAuthStartInput,
) (DesktopOAuthStartResult, error) {
	if s == nil || !oauthProviderClientEnabled(s.provider, ClientDesktop) {
		return DesktopOAuthStartResult{}, ErrOAuthNotConfigured
	}
	if err := validateDesktopOAuthRedirect(input.RedirectURI); err != nil {
		return DesktopOAuthStartResult{}, err
	}
	if err := validateS256Challenge(input.CodeChallenge); err != nil {
		return DesktopOAuthStartResult{}, err
	}
	if err := validateOAuthClientMetadata(ClientDesktop, input.ClientMetadata); err != nil {
		return DesktopOAuthStartResult{}, err
	}
	state, err := s.randomValue()
	if err != nil {
		return DesktopOAuthStartResult{}, fmt.Errorf("generate OAuth state: %w", err)
	}
	nonce, err := s.randomValue()
	if err != nil {
		return DesktopOAuthStartResult{}, fmt.Errorf("generate OAuth nonce: %w", err)
	}
	expiresAt := s.now().UTC().Add(s.flowTTL)
	authorizationURL, err := s.provider.AuthorizationURL(OAuthAuthorizationRequest{
		Client:        ClientDesktop,
		RedirectURI:   input.RedirectURI,
		State:         state,
		Nonce:         nonce,
		CodeChallenge: input.CodeChallenge,
	})
	if err != nil {
		return DesktopOAuthStartResult{}, stableOAuthProviderError(err)
	}
	stateHash := sha256.Sum256([]byte(state))
	nonceHash := sha256.Sum256([]byte(nonce))
	challenge := input.CodeChallenge
	flowID, err := createOAuthLoginFlow(ctx, s.store.Queries(), NewOAuthLoginFlow{
		Provider:       googleOAuthProvider,
		Client:         ClientDesktop,
		StateHash:      stateHash[:],
		NonceHash:      nonceHash[:],
		RedirectURI:    input.RedirectURI,
		CodeChallenge:  &challenge,
		ClientMetadata: input.ClientMetadata,
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		return DesktopOAuthStartResult{}, fmt.Errorf("persist Desktop OAuth flow: %w", err)
	}
	return DesktopOAuthStartResult{
		FlowID:           flowID,
		State:            state,
		AuthorizationURL: authorizationURL,
		ExpiresAt:        expiresAt,
	}, nil
}

func (s *DefaultOAuthService) CompleteWeb(
	ctx context.Context,
	input WebOAuthCompleteInput,
) (WebOAuthCompleteResult, error) {
	if s == nil || !oauthProviderClientEnabled(s.provider, ClientWeb) {
		return WebOAuthCompleteResult{}, ErrOAuthNotConfigured
	}
	validCompletion, completionError := classifyOAuthCompletion(input.Code, input.ProviderError)
	if !validCompletion || !webCallbackCredentialsWellFormed(input) {
		return WebOAuthCompleteResult{}, ErrOAuthFlowInvalid
	}
	stateHash := sha256.Sum256([]byte(input.State))
	bindingHash := sha256.Sum256([]byte(input.BrowserBinding))
	flow, err := consumeWebOAuthLoginFlow(ctx, s.store.Queries(), WebOAuthFlowConsumption{
		Provider:           googleOAuthProvider,
		StateHash:          stateHash[:],
		BrowserBindingHash: bindingHash[:],
		RedirectURI:        s.webRedirectURI,
	})
	if err != nil {
		if errors.Is(err, ErrOAuthFlowInvalid) {
			return WebOAuthCompleteResult{}, ErrOAuthFlowInvalid
		}
		return WebOAuthCompleteResult{}, fmt.Errorf("consume Web OAuth flow: %w", err)
	}
	if !s.consumedWebFlowMatchesCallback(flow) {
		return WebOAuthCompleteResult{}, ErrOAuthFlowInvalid
	}
	if completionError != nil {
		return WebOAuthCompleteResult{}, completionError
	}
	provided, err := s.provider.Exchange(ctx, OAuthExchangeRequest{
		Client:      ClientWeb,
		RedirectURI: flow.RedirectURI,
		Code:        input.Code,
		NonceHash:   flow.NonceHash,
	})
	if err != nil {
		return WebOAuthCompleteResult{}, stableOAuthProviderError(err)
	}
	nonceHash := sha256.Sum256([]byte(provided.Nonce))
	if subtle.ConstantTimeCompare(nonceHash[:], flow.NonceHash) != 1 {
		return WebOAuthCompleteResult{}, ErrOAuthFlowInvalid
	}
	result, err := s.createOAuthSession(ctx, flow, provided.Identity)
	if err != nil {
		return WebOAuthCompleteResult{}, err
	}
	return WebOAuthCompleteResult{Result: result, ReturnPath: *flow.ReturnPath}, nil
}

func (s *DefaultOAuthService) CompleteDesktop(ctx context.Context, input DesktopOAuthCompleteInput) (Result, error) {
	if s == nil || !oauthProviderClientEnabled(s.provider, ClientDesktop) {
		return Result{}, ErrOAuthNotConfigured
	}
	validCompletion, completionError := classifyOAuthCompletion(input.Code, input.ProviderError)
	if !validCompletion || !desktopCallbackCredentialsWellFormed(input) {
		return Result{}, ErrOAuthFlowInvalid
	}
	stateHash := sha256.Sum256([]byte(input.State))
	challenge := oauth2.S256ChallengeFromVerifier(input.Verifier)
	flow, err := consumeDesktopOAuthLoginFlow(ctx, s.store.Queries(), DesktopOAuthFlowConsumption{
		ID:            input.FlowID,
		Provider:      googleOAuthProvider,
		StateHash:     stateHash[:],
		RedirectURI:   input.RedirectURI,
		CodeChallenge: challenge,
	})
	if err != nil {
		if errors.Is(err, ErrOAuthFlowInvalid) {
			return Result{}, ErrOAuthFlowInvalid
		}
		return Result{}, fmt.Errorf("consume Desktop OAuth flow: %w", err)
	}
	if !consumedDesktopFlowMatchesCallback(flow, input, challenge) {
		return Result{}, ErrOAuthFlowInvalid
	}
	if completionError != nil {
		return Result{}, completionError
	}
	provided, err := s.provider.Exchange(ctx, OAuthExchangeRequest{
		Client:      ClientDesktop,
		RedirectURI: flow.RedirectURI,
		Code:        input.Code,
		Verifier:    input.Verifier,
		NonceHash:   flow.NonceHash,
	})
	if err != nil {
		return Result{}, stableOAuthProviderError(err)
	}
	nonceHash := sha256.Sum256([]byte(provided.Nonce))
	if subtle.ConstantTimeCompare(nonceHash[:], flow.NonceHash) != 1 {
		return Result{}, ErrOAuthFlowInvalid
	}
	return s.createOAuthSession(ctx, flow, provided.Identity)
}

func (s *DefaultOAuthService) createOAuthSession(
	ctx context.Context,
	flow OAuthLoginFlow,
	identity VerifiedOAuthIdentity,
) (Result, error) {
	identity, initialDisplayName, err := normalizeOAuthIdentity(identity)
	if err != nil {
		return Result{}, err
	}
	refresh, err := s.tokens.IssueRefresh()
	if err != nil {
		return Result{}, fmt.Errorf("issue OAuth refresh credential: %w", err)
	}
	user, sessionID, err := loginWithOAuthAndCreateSession(ctx, s.store, OAuthLoginInput{
		Identity:           identity,
		InitialDisplayName: initialDisplayName,
		Session: NewOAuthSession{
			TokenPrefix:    refresh.Prefix,
			TokenHash:      refresh.Hash,
			ExpiresAt:      refresh.ExpiresAt,
			Client:         flow.Client,
			ClientMetadata: flow.ClientMetadata,
		},
	})
	if err != nil {
		return Result{}, err
	}
	issuedAt := s.now().UTC()
	accessToken, accessExpiresAt, err := s.tokens.IssueAccess(user.ID, sessionID)
	if err != nil {
		return Result{}, fmt.Errorf("issue OAuth access credential: %w", err)
	}
	return Result{
		User:   user,
		Client: flow.Client,
		Tokens: IssuedTokens{
			SessionID:          sessionID,
			IssuedAt:           issuedAt,
			AccessToken:        accessToken,
			RefreshToken:       refresh.Token,
			RefreshTokenHash:   refresh.Hash,
			RefreshTokenPrefix: refresh.Prefix,
			AccessExpiresAt:    accessExpiresAt,
			RefreshExpiresAt:   refresh.ExpiresAt,
		},
	}, nil
}

func normalizeOAuthIdentity(identity VerifiedOAuthIdentity) (VerifiedOAuthIdentity, string, error) {
	if identity.Provider != googleOAuthProvider || !validOAuthSubject(identity.Subject) {
		return VerifiedOAuthIdentity{}, "", ErrOAuthIdentityInvalid
	}
	email, err := normalizeEmail(identity.Email)
	if err != nil {
		return VerifiedOAuthIdentity{}, "", ErrOAuthIdentityInvalid
	}
	identity.Email = email
	name := protocol.TrimContractWhitespace(identity.Profile.Name)
	if !validInitialOAuthDisplayName(name) {
		name = strings.SplitN(email, "@", 2)[0]
	}
	if !validInitialOAuthDisplayName(name) {
		return VerifiedOAuthIdentity{}, "", ErrOAuthIdentityInvalid
	}
	return identity, name, nil
}

func validInitialOAuthDisplayName(name string) bool {
	if !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > 120 {
		return false
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validOAuthSubject(subject string) bool {
	if len(subject) == 0 || len(subject) > 255 {
		return false
	}
	for index := range len(subject) {
		if subject[index] < 0x21 || subject[index] > 0x7e {
			return false
		}
	}
	return true
}

// webCallbackCredentialsWellFormed and desktopCallbackCredentialsWellFormed
// report whether the opaque values a callback echoed back still have the shape
// this service minted them with, before either is hashed and used to look a
// flow up.
//
// These fields make a round trip through the provider and, for desktop,
// through a loopback listener the OS lets any local process bind, so they are
// external input no matter which of our clients started the flow. The binder
// cannot stand in for this: the Web callback is a GET whose values are read
// off the query string and a cookie rather than from a bound body at all, and
// desktopOAuthCompleteRequest's `required`/`uuid` tags prove only that a field
// was sent and parses — not that state is a 43-character opaque value, that
// the redirect URI is still a loopback callback on a non-privileged port, or
// that verifier is a legal PKCE verifier.
func webCallbackCredentialsWellFormed(input WebOAuthCompleteInput) bool {
	return validOAuthRandomValue(input.State) && validOAuthRandomValue(input.BrowserBinding)
}

func desktopCallbackCredentialsWellFormed(input DesktopOAuthCompleteInput) bool {
	return util.ValidUUID(input.FlowID) && validOAuthRandomValue(input.State) &&
		validateDesktopOAuthRedirect(input.RedirectURI) == nil &&
		validPKCEVerifier(input.Verifier)
}

// consumedWebFlowMatchesCallback and consumedDesktopFlowMatchesCallback report
// whether the row the consuming DELETE returned is the flow this callback
// claims to be completing.
//
// The queries already filter on most of these columns, so this is deliberate
// defense in depth on the one step that turns a redirect from Google into a
// session: it re-reads the returned row against the request instead of
// trusting a WHERE clause in another file, and covers the columns that clause
// does not — client kind, and the nullable ReturnPath and CodeChallenge whose
// presence the code below dereferences.
func (s *DefaultOAuthService) consumedWebFlowMatchesCallback(flow OAuthLoginFlow) bool {
	return flow.Provider == googleOAuthProvider && flow.Client == ClientWeb &&
		flow.RedirectURI == s.webRedirectURI && flow.ReturnPath != nil
}

func consumedDesktopFlowMatchesCallback(
	flow OAuthLoginFlow,
	input DesktopOAuthCompleteInput,
	challenge string,
) bool {
	return flow.ID == input.FlowID && flow.Provider == googleOAuthProvider &&
		flow.Client == ClientDesktop && flow.RedirectURI == input.RedirectURI &&
		flow.CodeChallenge != nil && *flow.CodeChallenge == challenge
}

func validOAuthRandomValue(value string) bool {
	if len(value) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == 32
}

func validPKCEVerifier(verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for index := range len(verifier) {
		character := verifier[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '.' ||
			character == '_' || character == '~' {
			continue
		}
		return false
	}
	return true
}

func stableOAuthProviderError(err error) error {
	switch {
	case errors.Is(err, ErrOAuthNotConfigured):
		return ErrOAuthNotConfigured
	case errors.Is(err, ErrOAuthProviderDenied):
		return ErrOAuthProviderDenied
	case errors.Is(err, ErrOAuthIdentityInvalid):
		return ErrOAuthIdentityInvalid
	case errors.Is(err, ErrOAuthEmailUnverified):
		return ErrOAuthEmailUnverified
	default:
		return ErrOAuthProviderUnavailable
	}
}

func classifyOAuthCompletion(code, providerError string) (bool, error) {
	if (code == "") == (providerError == "") {
		return false, nil
	}
	if providerError == "" {
		return true, nil
	}
	if providerError == "access_denied" {
		return true, ErrOAuthProviderDenied
	}
	return true, ErrOAuthProviderUnavailable
}

func (s *DefaultOAuthService) randomValue() (string, error) {
	return randomOpaqueValue(s.random)
}

// randomOpaqueValue reads 32 random bytes from random and returns them
// base64url-encoded without padding: the canonical 43-character opaque value
// shape shared by OAuth state/nonce/browser-binding values and desktop
// handoff codes. validOAuthRandomValue checks a value has this shape.
func randomOpaqueValue(random io.Reader) (string, error) {
	randomBytes := make([]byte, 32)
	if _, err := io.ReadFull(random, randomBytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(randomBytes), nil
}

func normalizeOAuthReturnPath(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("%w: OAuth return path is invalid", ErrInvalidInput)
	}
	for _, character := range raw {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("%w: OAuth return path is invalid", ErrInvalidInput)
		}
	}
	normalized := protocol.TrimContractWhitespace(raw)
	if normalized == "" || len(normalized) > maxOAuthReturnPathBytes ||
		!strings.HasPrefix(normalized, "/") || strings.HasPrefix(normalized, "//") ||
		strings.Contains(normalized, "\\") || strings.Contains(normalized, "#") {
		return "", fmt.Errorf("%w: OAuth return path is invalid", ErrInvalidInput)
	}
	for _, character := range normalized {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("%w: OAuth return path is invalid", ErrInvalidInput)
		}
	}
	parsed, err := url.ParseRequestURI(normalized)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.Fragment != "" ||
		strings.HasPrefix(parsed.Path, "//") || invalidOAuthURLComponent(parsed.Path) {
		return "", fmt.Errorf("%w: OAuth return path is invalid", ErrInvalidInput)
	}
	decodedQuery, err := url.QueryUnescape(parsed.RawQuery)
	if err != nil || invalidOAuthURLComponent(decodedQuery) {
		return "", fmt.Errorf("%w: OAuth return path is invalid", ErrInvalidInput)
	}
	return normalized, nil
}

func invalidOAuthURLComponent(value string) bool {
	if !utf8.ValidString(value) {
		return true
	}
	for _, character := range value {
		if character == '\\' || unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func validateWebOAuthRedirect(raw string, allowInsecureLoopback bool) error {
	parsed, err := url.Parse(raw)
	secureScheme := err == nil && parsed.Scheme == "https"
	loopbackHTTP := err == nil && allowInsecureLoopback && parsed.Scheme == "http" &&
		isOAuthLoopbackHost(parsed.Hostname())
	if err != nil || (!secureScheme && !loopbackHTTP) || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || len(raw) > maxOAuthReturnPathBytes {
		return errors.New("Web OAuth redirect URI is invalid")
	}
	return nil
}

func isOAuthLoopbackHost(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") || strings.HasSuffix(strings.ToLower(hostname), ".localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func validateOAuthClientMetadata(client ClientKind, metadata OAuthClientMetadata) error {
	for _, value := range []string{metadata.AppVersion, metadata.Platform} {
		if !utf8.ValidString(value) || len(value) > 255 {
			return fmt.Errorf("%w: OAuth client metadata is invalid", ErrInvalidInput)
		}
		for _, character := range value {
			if unicode.IsControl(character) {
				return fmt.Errorf("%w: OAuth client metadata is invalid", ErrInvalidInput)
			}
		}
	}
	if _, err := encodeOAuthClientMetadata(client, metadata); err != nil {
		return err
	}
	return nil
}

func validateDesktopOAuthRedirect(raw string) error {
	matches := desktopOAuthRedirectPattern.FindStringSubmatch(raw)
	if len(matches) != 2 {
		return fmt.Errorf("%w: Desktop OAuth redirect URI is invalid", ErrInvalidInput)
	}
	port, err := strconv.Atoi(matches[1])
	if err != nil || port < 1024 || port > 65535 || strconv.Itoa(port) != matches[1] {
		return fmt.Errorf("%w: Desktop OAuth redirect URI is invalid", ErrInvalidInput)
	}
	return nil
}

func validateS256Challenge(challenge string) error {
	if len(challenge) != 43 {
		return fmt.Errorf("%w: OAuth code challenge is invalid", ErrInvalidInput)
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(challenge)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("%w: OAuth code challenge is invalid", ErrInvalidInput)
	}
	return nil
}
