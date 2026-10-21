package auth

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type OAuthAuthorizationRequest struct {
	Client        ClientKind
	RedirectURI   string
	State         string
	Nonce         string
	CodeChallenge string
}

type OAuthExchangeRequest struct {
	Client      ClientKind
	RedirectURI string
	Code        string
	Verifier    string
	NonceHash   []byte
}

type OAuthProviderIdentity struct {
	Identity VerifiedOAuthIdentity
	Nonce    string
}

type OAuthProvider interface {
	AuthorizationURL(OAuthAuthorizationRequest) (string, error)
	Exchange(context.Context, OAuthExchangeRequest) (OAuthProviderIdentity, error)
}

type OAuthLoginFlow struct {
	ID                 uuid.UUID
	Provider           string
	Client             ClientKind
	StateHash          []byte
	NonceHash          []byte
	BrowserBindingHash []byte
	RedirectURI        string
	ReturnPath         *string
	CodeChallenge      *string
	ClientMetadata     OAuthClientMetadata
	ExpiresAt          time.Time
	CreatedAt          time.Time
}

// OAuthClientMetadata is the complete field allowlist for Zenith Team client
// details that OAuth flows and their resulting sessions may persist. Callers
// cannot supply arbitrary JSON keys through this boundary.
type OAuthClientMetadata struct {
	AppVersion string `json:"appVersion,omitempty"`
	Platform   string `json:"platform,omitempty"`
}

type NewOAuthLoginFlow struct {
	Provider           string
	Client             ClientKind
	StateHash          []byte
	NonceHash          []byte
	BrowserBindingHash []byte
	RedirectURI        string
	ReturnPath         *string
	CodeChallenge      *string
	ClientMetadata     OAuthClientMetadata
	ExpiresAt          time.Time
}

type WebOAuthFlowConsumption struct {
	Provider           string
	StateHash          []byte
	BrowserBindingHash []byte
	RedirectURI        string
}

type DesktopOAuthFlowConsumption struct {
	ID            uuid.UUID
	Provider      string
	StateHash     []byte
	RedirectURI   string
	CodeChallenge string
}

type OAuthProfileSnapshot struct {
	Name         string  `json:"name,omitempty"`
	PictureURL   *string `json:"picture,omitempty"`
	Locale       *string `json:"locale,omitempty"`
	HostedDomain *string `json:"hostedDomain,omitempty"`
}

type VerifiedOAuthIdentity struct {
	Provider string
	Subject  string
	Email    string
	Profile  OAuthProfileSnapshot
}

type OAuthLoginInput struct {
	Identity           VerifiedOAuthIdentity
	InitialDisplayName string
	Session            NewOAuthSession
}

type NewOAuthSession struct {
	TokenPrefix    string
	TokenHash      []byte
	ExpiresAt      time.Time
	Client         ClientKind
	ClientMetadata OAuthClientMetadata
}
