//go:build zenith_minimal

package auth

import (
	"context"
	"net/http"
	"time"
)

type GoogleOAuthClientConfig struct {
	ClientID     string
	ClientSecret string
}

type GoogleProviderOptions struct {
	Web             GoogleOAuthClientConfig
	Desktop         GoogleOAuthClientConfig
	HTTPClient      *http.Client
	ExchangeTimeout time.Duration
}

// GoogleProvider refuses OAuth in the minimal build, which excludes Google's
// SDK. Existing sessions and the ordinary password/daemon login remain usable.
type GoogleProvider struct{}

func NewGoogleProvider(GoogleProviderOptions) (*GoogleProvider, error) {
	return nil, ErrOAuthNotConfigured
}

func (*GoogleProvider) ClientEnabled(ClientKind) bool { return false }

func (*GoogleProvider) AuthorizationURL(OAuthAuthorizationRequest) (string, error) {
	return "", ErrOAuthNotConfigured
}

func (*GoogleProvider) Exchange(context.Context, OAuthExchangeRequest) (OAuthProviderIdentity, error) {
	return OAuthProviderIdentity{}, ErrOAuthNotConfigured
}
