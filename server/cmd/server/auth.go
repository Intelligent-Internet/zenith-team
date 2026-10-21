package main

import (
	"fmt"
	"net/http"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/config"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/infra/email"
)

func newGoogleOAuthService(
	authConfig config.AuthConfig,
	store *db.Store,
	tokens auth.OAuthTokenIssuer,
) (*auth.DefaultOAuthService, error) {
	var provider auth.OAuthProvider
	if authConfig.Google.Enabled {
		providerTimeout := authConfig.Google.ProviderTimeout
		googleProvider, err := auth.NewGoogleProvider(auth.GoogleProviderOptions{
			Web: auth.GoogleOAuthClientConfig{
				ClientID:     authConfig.Google.WebClientID,
				ClientSecret: authConfig.Google.WebClientSecret,
			},
			Desktop: auth.GoogleOAuthClientConfig{
				ClientID:     authConfig.Google.DesktopClientID,
				ClientSecret: authConfig.Google.DesktopClientSecret,
			},
			HTTPClient:      &http.Client{Timeout: providerTimeout},
			ExchangeTimeout: providerTimeout,
		})
		if err != nil {
			return nil, fmt.Errorf("construct Google OAuth provider: %w", err)
		}
		provider = googleProvider
	}
	oauthService, err := auth.NewOAuthService(auth.OAuthServiceOptions{
		Store:                            store,
		Provider:                         provider,
		Tokens:                           tokens,
		WebRedirectURI:                   authConfig.Google.WebRedirectURI,
		FlowTTL:                          authConfig.Google.FlowTTL,
		AllowInsecureLoopbackWebRedirect: authConfig.Google.AllowInsecureLoopbackWebCallback,
	})
	if err != nil {
		return nil, fmt.Errorf("construct Google OAuth service: %w", err)
	}
	return oauthService, nil
}

func newEmailAuthOptions(
	configuration config.EmailVerificationConfig,
	webAppOrigin string,
) (*auth.EmailVerificationOptions, *auth.PasswordResetOptions, error) {
	if !configuration.Enabled {
		return nil, nil, nil
	}
	provider, err := email.NewResendProvider(email.ResendOptions{
		APIKey:     configuration.ResendAPIKey.Reveal(),
		HTTPClient: &http.Client{Timeout: configuration.ProviderTimeout},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("construct Resend email provider: %w", err)
	}
	emailService, err := email.NewService(provider)
	if err != nil {
		return nil, nil, fmt.Errorf("construct email service: %w", err)
	}
	verificationSender, err := email.NewVerificationService(emailService, configuration.TemplateID)
	if err != nil {
		return nil, nil, fmt.Errorf("construct verification email service: %w", err)
	}
	passwordResetSender, err := email.NewPasswordResetService(emailService, configuration.PasswordResetTemplateID)
	if err != nil {
		return nil, nil, fmt.Errorf("construct password reset email service: %w", err)
	}
	verification := &auth.EmailVerificationOptions{
		Sender: verificationSender,
		Secret: []byte(configuration.Secret.Reveal()), TTL: configuration.TTL,
		ResendCooldown: configuration.ResendCooldown,
	}
	passwordReset := &auth.PasswordResetOptions{
		Sender: passwordResetSender, WebAppOrigin: webAppOrigin,
		TTL: configuration.PasswordResetTTL, ResendCooldown: configuration.PasswordResetResendCooldown,
	}
	return verification, passwordReset, nil
}
