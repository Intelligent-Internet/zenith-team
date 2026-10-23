//go:build zenith_minimal

package auth

import (
	"context"
	"errors"
	"testing"
)

func TestMinimalGoogleProviderRefusesOAuthAuthority(t *testing.T) {
	p, err := NewGoogleProvider(GoogleProviderOptions{})
	if !errors.Is(err, ErrOAuthNotConfigured) || p != nil {
		t.Fatal("minimal build accepted Google OAuth")
	}
	var disabled GoogleProvider
	if disabled.ClientEnabled(ClientWeb) || disabled.ClientEnabled(ClientDesktop) {
		t.Fatal("minimal provider advertised OAuth")
	}
	if _, err := disabled.AuthorizationURL(OAuthAuthorizationRequest{}); !errors.Is(err, ErrOAuthNotConfigured) {
		t.Fatal("minimal provider authorized OAuth")
	}
	if _, err := disabled.Exchange(
		context.Background(),
		OAuthExchangeRequest{},
	); !errors.Is(
		err,
		ErrOAuthNotConfigured,
	) {
		t.Fatal("minimal provider exchanged credentials")
	}
}
