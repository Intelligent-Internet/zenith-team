//go:build !zenith_minimal

package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/idtoken"
)

var googleOAuthScopes = []string{"openid", "email", "profile"}

type GoogleOAuthClientConfig struct {
	ClientID     string
	ClientSecret string
}

type googleIDTokenValidator interface {
	Validate(context.Context, string, string) (*idtoken.Payload, error)
}

type GoogleProviderOptions struct {
	Web             GoogleOAuthClientConfig
	Desktop         GoogleOAuthClientConfig
	HTTPClient      *http.Client
	ExchangeTimeout time.Duration
	validator       googleIDTokenValidator
	now             func() time.Time
}

type GoogleProvider struct {
	web             oauth2.Config
	desktop         oauth2.Config
	httpClient      *http.Client
	exchangeTimeout time.Duration
	validator       googleIDTokenValidator
	now             func() time.Time
}

func NewGoogleProvider(options GoogleProviderOptions) (*GoogleProvider, error) {
	webEnabled, err := validateOptionalGoogleClientConfig("Web", options.Web)
	if err != nil {
		return nil, err
	}
	desktopEnabled, err := validateOptionalGoogleClientConfig("Desktop", options.Desktop)
	if err != nil {
		return nil, err
	}
	if !webEnabled && !desktopEnabled {
		return nil, errors.New("at least one Google OAuth client configuration is required")
	}
	if webEnabled && desktopEnabled && options.Web.ClientID == options.Desktop.ClientID {
		return nil, errors.New("Web and Desktop Google OAuth client IDs must differ")
	}
	if options.HTTPClient == nil || options.HTTPClient.Timeout <= 0 {
		return nil, errors.New("Google OAuth HTTP client must have a timeout")
	}
	if options.ExchangeTimeout <= 0 {
		return nil, errors.New("Google OAuth exchange timeout must be positive")
	}
	if options.now == nil {
		options.now = time.Now
	}
	if options.validator == nil {
		validator, err := idtoken.NewValidator(context.Background(), idtoken.WithHTTPClient(options.HTTPClient))
		if err != nil {
			return nil, fmt.Errorf("construct Google ID token validator: %w", err)
		}
		options.validator = validator
	}
	return &GoogleProvider{
		web:             newGoogleOAuthConfig(options.Web),
		desktop:         newGoogleOAuthConfig(options.Desktop),
		httpClient:      options.HTTPClient,
		exchangeTimeout: options.ExchangeTimeout,
		validator:       options.validator,
		now:             options.now,
	}, nil
}

func (p *GoogleProvider) AuthorizationURL(request OAuthAuthorizationRequest) (string, error) {
	if p == nil {
		return "", ErrOAuthNotConfigured
	}
	config, err := p.configForClient(request.Client)
	if err != nil {
		return "", err
	}
	if request.State == "" || request.Nonce == "" || request.RedirectURI == "" {
		return "", ErrOAuthFlowInvalid
	}
	if request.Client == ClientWeb && request.CodeChallenge != "" {
		return "", ErrOAuthFlowInvalid
	}
	if request.Client == ClientDesktop && validateS256Challenge(request.CodeChallenge) != nil {
		return "", ErrOAuthFlowInvalid
	}
	config.RedirectURL = request.RedirectURI
	options := []oauth2.AuthCodeOption{
		oauth2.AccessTypeOnline,
		oauth2.SetAuthURLParam("nonce", request.Nonce),
	}
	if request.Client == ClientDesktop {
		options = append(options,
			oauth2.SetAuthURLParam("code_challenge", request.CodeChallenge),
			oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		)
	}
	return config.AuthCodeURL(request.State, options...), nil
}

func (p *GoogleProvider) Exchange(ctx context.Context, request OAuthExchangeRequest) (OAuthProviderIdentity, error) {
	if p == nil {
		return OAuthProviderIdentity{}, ErrOAuthNotConfigured
	}
	config, err := p.configForClient(request.Client)
	if err != nil {
		return OAuthProviderIdentity{}, err
	}
	if request.Code == "" || request.RedirectURI == "" || len(request.NonceHash) != sha256.Size {
		return OAuthProviderIdentity{}, ErrOAuthFlowInvalid
	}
	if request.Client == ClientWeb && request.Verifier != "" {
		return OAuthProviderIdentity{}, ErrOAuthFlowInvalid
	}
	if request.Client == ClientDesktop && !validPKCEVerifier(request.Verifier) {
		return OAuthProviderIdentity{}, ErrOAuthFlowInvalid
	}
	config.RedirectURL = request.RedirectURI
	exchangeCtx, cancel := context.WithTimeout(ctx, p.exchangeTimeout)
	defer cancel()
	exchangeCtx = context.WithValue(exchangeCtx, oauth2.HTTPClient, p.httpClient)
	var options []oauth2.AuthCodeOption
	if request.Client == ClientDesktop {
		options = append(options, oauth2.VerifierOption(request.Verifier))
	}
	token, err := config.Exchange(exchangeCtx, request.Code, options...)
	if err != nil {
		return OAuthProviderIdentity{}, mapGoogleExchangeError(exchangeCtx, err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return OAuthProviderIdentity{}, ErrOAuthIdentityInvalid
	}
	payload, err := p.validator.Validate(exchangeCtx, rawIDToken, config.ClientID)
	if err != nil {
		if exchangeCtx.Err() != nil || errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			return OAuthProviderIdentity{}, ErrOAuthProviderUnavailable
		}
		return OAuthProviderIdentity{}, ErrOAuthIdentityInvalid
	}
	return p.identityFromPayload(payload, config.ClientID, request.NonceHash)
}

func (p *GoogleProvider) identityFromPayload(
	payload *idtoken.Payload,
	audience string,
	expectedNonceHash []byte,
) (OAuthProviderIdentity, error) {
	if payload == nil ||
		(payload.Issuer != "accounts.google.com" && payload.Issuer != "https://accounts.google.com") ||
		payload.Audience != audience ||
		payload.Expires <= p.now().UTC().Unix() ||
		!validOAuthSubject(payload.Subject) {
		return OAuthProviderIdentity{}, ErrOAuthIdentityInvalid
	}
	email, ok := stringClaim(payload.Claims, "email")
	if !ok {
		return OAuthProviderIdentity{}, ErrOAuthIdentityInvalid
	}
	normalizedEmail, err := normalizeEmail(email)
	if err != nil {
		return OAuthProviderIdentity{}, ErrOAuthIdentityInvalid
	}
	verified, ok := payload.Claims["email_verified"].(bool)
	if !ok || !verified {
		return OAuthProviderIdentity{}, ErrOAuthEmailUnverified
	}
	nonce, ok := stringClaim(payload.Claims, "nonce")
	if !ok {
		return OAuthProviderIdentity{}, ErrOAuthIdentityInvalid
	}
	nonceHash := sha256.Sum256([]byte(nonce))
	if subtle.ConstantTimeCompare(nonceHash[:], expectedNonceHash) != 1 {
		return OAuthProviderIdentity{}, ErrOAuthIdentityInvalid
	}
	profile := OAuthProfileSnapshot{}
	if name, ok := stringClaim(payload.Claims, "name"); ok && validGoogleProfileName(name) {
		profile.Name = name
	}
	if picture, ok := stringClaim(payload.Claims, "picture"); ok && validGooglePictureURL(picture) {
		profile.PictureURL = &picture
	}
	if locale, ok := stringClaim(payload.Claims, "locale"); ok && validGoogleLocale(locale) {
		profile.Locale = &locale
	}
	if hostedDomain, ok := stringClaim(payload.Claims, "hd"); ok && validGoogleHostedDomain(hostedDomain) {
		profile.HostedDomain = &hostedDomain
	}
	return OAuthProviderIdentity{
		Identity: VerifiedOAuthIdentity{
			Provider: googleOAuthProvider,
			Subject:  payload.Subject,
			Email:    normalizedEmail,
			Profile:  profile,
		},
		Nonce: nonce,
	}, nil
}

func mapGoogleExchangeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ErrOAuthProviderUnavailable
	}
	var retrieveError *oauth2.RetrieveError
	if errors.As(err, &retrieveError) && retrieveError.ErrorCode == "access_denied" {
		return ErrOAuthProviderDenied
	}
	return ErrOAuthProviderUnavailable
}

func stringClaim(claims map[string]any, name string) (string, bool) {
	value, ok := claims[name].(string)
	return value, ok && value != "" && utf8.ValidString(value)
}

func validGoogleProfileName(name string) bool {
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 120 {
		return false
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validGooglePictureURL(raw string) bool {
	if len(raw) == 0 || len(raw) > 2048 || !utf8.ValidString(raw) {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

func validGoogleLocale(locale string) bool {
	if len(locale) == 0 || len(locale) > 35 {
		return false
	}
	parts := strings.Split(strings.ReplaceAll(locale, "_", "-"), "-")
	if len(parts) == 0 || len(parts[0]) < 2 || len(parts[0]) > 8 {
		return false
	}
	for partIndex, part := range parts {
		if part == "" || len(part) > 8 {
			return false
		}
		for index := range len(part) {
			character := part[index]
			if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
				(partIndex > 0 && character >= '0' && character <= '9') {
				continue
			}
			return false
		}
	}
	return true
}

func validGoogleHostedDomain(domain string) bool {
	if len(domain) == 0 || len(domain) > 253 || strings.HasPrefix(domain, ".") ||
		strings.HasSuffix(domain, ".") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for index := range len(label) {
			character := label[index]
			if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func (p *GoogleProvider) configForClient(client ClientKind) (oauth2.Config, error) {
	if !p.ClientEnabled(client) {
		return oauth2.Config{}, ErrOAuthNotConfigured
	}
	switch client {
	case ClientWeb:
		return p.web, nil
	case ClientDesktop:
		return p.desktop, nil
	default:
		return oauth2.Config{}, ErrOAuthFlowInvalid
	}
}

// ClientEnabled reports whether the provider has a complete credential pair
// for the requested Zenith Team client surface.
func (p *GoogleProvider) ClientEnabled(client ClientKind) bool {
	if p == nil {
		return false
	}
	switch client {
	case ClientWeb:
		return p.web.ClientID != "" && p.web.ClientSecret != ""
	case ClientDesktop:
		return p.desktop.ClientID != "" && p.desktop.ClientSecret != ""
	default:
		return false
	}
}

func validateOptionalGoogleClientConfig(name string, config GoogleOAuthClientConfig) (bool, error) {
	if config.ClientID == "" && config.ClientSecret == "" {
		return false, nil
	}
	if !validGoogleClientCredential(config.ClientID) || !validGoogleClientCredential(config.ClientSecret) {
		return false, fmt.Errorf("%s Google OAuth client configuration is invalid", name)
	}
	return true, nil
}

func validGoogleClientCredential(value string) bool {
	if value == "" || len(value) > 2048 || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func newGoogleOAuthConfig(config GoogleOAuthClientConfig) oauth2.Config {
	return oauth2.Config{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       append([]string(nil), googleOAuthScopes...),
	}
}
