//go:build !zenith_minimal

package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/idtoken"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
)

func TestGoogleProviderAuthorizationURLUsesExactClientFieldsAndDesktopOnlyPKCE(t *testing.T) {
	t.Parallel()

	provider, err := NewGoogleProvider(GoogleProviderOptions{
		Web: GoogleOAuthClientConfig{
			ClientID: "web-client.apps.googleusercontent.com", ClientSecret: "web-secret",
		},
		Desktop: GoogleOAuthClientConfig{
			ClientID: "desktop-client.apps.googleusercontent.com", ClientSecret: "desktop-secret",
		},
		HTTPClient:      &http.Client{Timeout: 2 * time.Second},
		ExchangeTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewGoogleProvider() error = %v", err)
	}

	tests := []struct {
		name      string
		request   OAuthAuthorizationRequest
		clientID  string
		challenge string
	}{
		{
			name: "Web",
			request: OAuthAuthorizationRequest{
				Client:      ClientWeb,
				RedirectURI: "https://api.zenith-team.test/api/v1/auth/google/web/callback",
				State:       "web-state",
				Nonce:       "web-nonce",
			},
			clientID: "web-client.apps.googleusercontent.com",
		},
		{
			name: "Desktop",
			request: OAuthAuthorizationRequest{
				Client: ClientDesktop, RedirectURI: "http://127.0.0.1:49152/",
				State: "desktop-state", Nonce: "desktop-nonce", CodeChallenge: strings.Repeat("A", 43),
			},
			clientID:  "desktop-client.apps.googleusercontent.com",
			challenge: strings.Repeat("A", 43),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rawURL, err := provider.AuthorizationURL(test.request)
			if err != nil {
				t.Fatalf("AuthorizationURL() error = %v", err)
			}
			parsed, err := url.Parse(rawURL)
			if err != nil {
				t.Fatalf("parse authorization URL: %v", err)
			}
			if parsed.Scheme != "https" || parsed.Host != "accounts.google.com" ||
				parsed.Path != "/o/oauth2/auth" {
				t.Fatalf("authorization endpoint = %s", parsed.Redacted())
			}
			query := parsed.Query()
			want := url.Values{
				"access_type":   {"online"},
				"client_id":     {test.clientID},
				"nonce":         {test.request.Nonce},
				"redirect_uri":  {test.request.RedirectURI},
				"response_type": {"code"},
				"scope":         {"openid email profile"},
				"state":         {test.request.State},
			}
			if test.challenge != "" {
				want["code_challenge"] = []string{test.challenge}
				want["code_challenge_method"] = []string{"S256"}
			}
			if !reflect.DeepEqual(query, want) {
				t.Fatalf("authorization query = %#v, want %#v", query, want)
			}
			if query.Get("access_type") == "offline" || query.Has("prompt") {
				t.Fatalf("authorization URL requested offline/forced access: %s", parsed.Redacted())
			}
		})
	}
}

func TestNewGoogleProviderRejectsIncompleteOrSubstitutableClientConfiguration(t *testing.T) {
	t.Parallel()

	validWeb := GoogleOAuthClientConfig{ClientID: "web-client", ClientSecret: "web-secret"}
	validDesktop := GoogleOAuthClientConfig{ClientID: "desktop-client", ClientSecret: "desktop-secret"}
	tests := []struct {
		name    string
		web     GoogleOAuthClientConfig
		desktop GoogleOAuthClientConfig
	}{
		{name: "missing both clients"},
		{
			name:    "identical client IDs",
			web:     validWeb,
			desktop: GoogleOAuthClientConfig{ClientID: validWeb.ClientID, ClientSecret: "desktop-secret"},
		},
		{
			name:    "Web client ID outer whitespace",
			web:     GoogleOAuthClientConfig{ClientID: " web-client", ClientSecret: "web-secret"},
			desktop: validDesktop,
		},
		{
			name:    "Desktop client ID control",
			web:     validWeb,
			desktop: GoogleOAuthClientConfig{ClientID: "desktop\nclient", ClientSecret: "desktop-secret"},
		},
		{
			name:    "missing Web secret",
			web:     GoogleOAuthClientConfig{ClientID: "web-client"},
			desktop: validDesktop,
		},
		{
			name:    "Desktop secret outer whitespace",
			web:     validWeb,
			desktop: GoogleOAuthClientConfig{ClientID: "desktop-client", ClientSecret: "desktop-secret "},
		},
	}
	for _, test := range tests {
		_, err := NewGoogleProvider(GoogleProviderOptions{
			Web: test.web, Desktop: test.desktop,
			HTTPClient: &http.Client{Timeout: time.Second}, ExchangeTimeout: time.Second,
		})
		if err == nil {
			t.Fatalf("%s: NewGoogleProvider() error = nil", test.name)
		}
		for _, credential := range []string{test.web.ClientID, test.web.ClientSecret, test.desktop.ClientID, test.desktop.ClientSecret} {
			if credential != "" && strings.Contains(err.Error(), credential) {
				t.Fatalf("%s: constructor leaked credential %q in %q", test.name, credential, err)
			}
		}
	}
}

func TestGoogleProviderAllowsDesktopOnlyConfiguration(t *testing.T) {
	t.Parallel()

	provider, err := NewGoogleProvider(GoogleProviderOptions{
		Desktop: GoogleOAuthClientConfig{
			ClientID: "desktop-client.apps.googleusercontent.com", ClientSecret: "desktop-secret",
		},
		HTTPClient:      &http.Client{Timeout: time.Second},
		ExchangeTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewGoogleProvider() error = %v", err)
	}
	if provider.ClientEnabled(ClientWeb) || !provider.ClientEnabled(ClientDesktop) {
		t.Fatalf(
			"desktop-only client capabilities = Web:%t Desktop:%t",
			provider.ClientEnabled(ClientWeb),
			provider.ClientEnabled(ClientDesktop),
		)
	}
	if _, err := provider.AuthorizationURL(OAuthAuthorizationRequest{
		Client: ClientWeb, RedirectURI: "https://api.zenith-team.test/api/v1/auth/google/web/callback",
		State: "web-state", Nonce: "web-nonce",
	}); !errors.Is(err, ErrOAuthNotConfigured) {
		t.Fatalf("Web AuthorizationURL() error = %v, want ErrOAuthNotConfigured", err)
	}
	rawURL, err := provider.AuthorizationURL(OAuthAuthorizationRequest{
		Client: ClientDesktop, RedirectURI: "http://127.0.0.1:49152/",
		State: "desktop-state", Nonce: "desktop-nonce", CodeChallenge: strings.Repeat("A", 43),
	})
	if err != nil {
		t.Fatalf("Desktop AuthorizationURL() error = %v", err)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Query().Get("client_id") != "desktop-client.apps.googleusercontent.com" {
		t.Fatalf("Desktop authorization URL = %q, parse error = %v", rawURL, err)
	}
}

func TestGoogleProviderExchangeUsesSelectedClientAndReturnsOnlyVerifiedAllowlistedIdentity(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 5, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		client       ClientKind
		clientID     string
		clientSecret string
		redirectURI  string
		verifier     string
	}{
		{
			name: "Web", client: ClientWeb,
			clientID: "web-client.apps.googleusercontent.com", clientSecret: "web-secret",
			redirectURI: "https://api.zenith-team.test/api/v1/auth/google/web/callback",
		},
		{
			name: "Desktop", client: ClientDesktop,
			clientID: "desktop-client.apps.googleusercontent.com", clientSecret: "desktop-secret",
			redirectURI: "http://127.0.0.1:49152/", verifier: strings.Repeat("v", 43),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var tokenRequest url.Values
			httpClient := &http.Client{
				Timeout: 2 * time.Second,
				Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					if request.URL.String() != "https://oauth2.googleapis.com/token" ||
						request.Method != http.MethodPost {
						t.Fatalf(
							"token request = %s %s",
							request.Method,
							request.URL.Redacted(),
						)
					}
					if err := request.ParseForm(); err != nil {
						t.Fatalf("parse token request: %v", err)
					}
					tokenRequest = request.PostForm
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body: io.NopCloser(strings.NewReader(`{
							"access_token":"sentinel-google-access",
							"refresh_token":"sentinel-google-refresh",
							"id_token":"sentinel-signed-google-id-token",
							"token_type":"Bearer",
							"expires_in":3600
						}`)),
						Request: request,
					}, nil
				}),
			}
			validator := &fakeGoogleIDTokenValidator{
				validate: func(_ context.Context, rawIDToken, audience string) (*idtoken.Payload, error) {
					if rawIDToken != "sentinel-signed-google-id-token" ||
						audience != test.clientID {
						t.Fatalf("ID token validation input = %q/%q", rawIDToken, audience)
					}
					return &idtoken.Payload{
						Issuer:   "https://accounts.google.com",
						Audience: test.clientID,
						Expires: now.Add(time.Minute).
							Unix(),
						Subject: "CaseSensitiveSubject123",
						Claims: map[string]any{
							"email":          "  USER@Example.com  ",
							"email_verified": true,
							"nonce":          "signed-nonce",
							"name":           "Provider Name",
							"picture":        "https://images.example.test/avatar.png",
							"locale":         "en-US",
							"hd":             "example.com",
							"access_token":   "sentinel-unknown-claim",
						},
					}, nil
				},
			}
			provider, err := NewGoogleProvider(GoogleProviderOptions{
				Web: GoogleOAuthClientConfig{
					ClientID: "web-client.apps.googleusercontent.com", ClientSecret: "web-secret",
				},
				Desktop: GoogleOAuthClientConfig{
					ClientID:     "desktop-client.apps.googleusercontent.com",
					ClientSecret: "desktop-secret",
				},
				HTTPClient: httpClient, ExchangeTimeout: 3 * time.Second,
				validator: validator, now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatalf("NewGoogleProvider() error = %v", err)
			}

			nonceHash := sha256.Sum256([]byte("signed-nonce"))
			result, err := provider.Exchange(context.Background(), OAuthExchangeRequest{
				Client: test.client, RedirectURI: test.redirectURI,
				Code: "sentinel-google-code", Verifier: test.verifier, NonceHash: nonceHash[:],
			})
			if err != nil {
				t.Fatalf("Exchange() error = %v", err)
			}

			wantTokenRequest := url.Values{
				"client_id":     {test.clientID},
				"client_secret": {test.clientSecret},
				"code":          {"sentinel-google-code"},
				"grant_type":    {"authorization_code"},
				"redirect_uri":  {test.redirectURI},
			}
			if test.verifier != "" {
				wantTokenRequest["code_verifier"] = []string{test.verifier}
			}
			if !reflect.DeepEqual(tokenRequest, wantTokenRequest) {
				t.Fatalf("token request = %#v, want %#v", tokenRequest, wantTokenRequest)
			}
			if result.Identity.Provider != googleOAuthProvider ||
				result.Identity.Subject != "CaseSensitiveSubject123" ||
				result.Identity.Email != "user@example.com" ||
				result.Nonce != "signed-nonce" {
				t.Fatalf("Exchange() identity = %#v", result)
			}
			profile := result.Identity.Profile
			if profile.Name != "Provider Name" || profile.PictureURL == nil ||
				*profile.PictureURL != "https://images.example.test/avatar.png" ||
				profile.Locale == nil ||
				*profile.Locale != "en-US" ||
				profile.HostedDomain == nil ||
				*profile.HostedDomain != "example.com" {
				t.Fatalf("Exchange() profile = %#v", profile)
			}
			serialized := fmt.Sprintf("%#v", result)
			for _, forbidden := range []string{"sentinel-google-access", "sentinel-google-refresh", "sentinel-signed-google-id-token", "sentinel-unknown-claim"} {
				if strings.Contains(serialized, forbidden) {
					t.Fatalf("Exchange() returned %q in %s", forbidden, serialized)
				}
			}
		})
	}
}

func TestGoogleProviderValidatorFailuresAreClassifiedAndRedacted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "signature key expiry or audience",
			err:  errors.New("sentinel-validator-signature-key-expiry-audience-body"),
			want: ErrOAuthIdentityInvalid,
		},
		{
			name: "validator cancellation",
			err:  fmt.Errorf("validator canceled with sentinel body: %w", context.Canceled),
			want: ErrOAuthProviderUnavailable,
		},
		{
			name: "validator deadline",
			err:  fmt.Errorf("validator timed out with sentinel body: %w", context.DeadlineExceeded),
			want: ErrOAuthProviderUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			httpClient := &http.Client{
				Timeout: time.Second,
				Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body: io.NopCloser(
							strings.NewReader(
								`{"access_token":"sentinel-access","token_type":"Bearer","id_token":"sentinel-id-token"}`,
							),
						),
						Request: request,
					}, nil
				}),
			}
			provider, err := NewGoogleProvider(GoogleProviderOptions{
				Web: GoogleOAuthClientConfig{ClientID: "web-client", ClientSecret: "web-secret"},
				Desktop: GoogleOAuthClientConfig{
					ClientID:     "desktop-client",
					ClientSecret: "desktop-secret",
				},
				HTTPClient:      httpClient,
				ExchangeTimeout: time.Second,
				validator: &fakeGoogleIDTokenValidator{
					validate: func(context.Context, string, string) (*idtoken.Payload, error) {
						return nil, test.err
					},
				},
			})
			if err != nil {
				t.Fatalf("NewGoogleProvider() error = %v", err)
			}

			_, err = provider.Exchange(context.Background(), OAuthExchangeRequest{
				Client:      ClientWeb,
				RedirectURI: "https://api.zenith-team.test/callback",
				Code:        "sentinel-code",
				NonceHash:   make([]byte, sha256.Size),
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("Exchange() error = %v, want %v", err, test.want)
			}
			for _, forbidden := range []string{"sentinel-validator", "sentinel body", "sentinel-id-token", "sentinel-access", "sentinel-code"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("Exchange() leaked %q in %q", forbidden, err)
				}
			}
		})
	}
}

func TestGoogleProviderRejectsInvalidRequiredIdentityClaims(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 5, 30, 0, 0, time.UTC)
	expectedNonce := "expected-signed-nonce"
	expectedNonceHash := sha256.Sum256([]byte(expectedNonce))
	tests := []struct {
		name   string
		mutate func(*idtoken.Payload)
		want   error
	}{
		{
			name:   "bad issuer",
			mutate: func(payload *idtoken.Payload) { payload.Issuer = "https://issuer.attacker.test" },
			want:   ErrOAuthIdentityInvalid,
		},
		{
			name:   "wrong audience",
			mutate: func(payload *idtoken.Payload) { payload.Audience = "desktop-client" },
			want:   ErrOAuthIdentityInvalid,
		},
		{
			name:   "expired",
			mutate: func(payload *idtoken.Payload) { payload.Expires = now.Unix() },
			want:   ErrOAuthIdentityInvalid,
		},
		{
			name:   "missing nonce",
			mutate: func(payload *idtoken.Payload) { delete(payload.Claims, "nonce") },
			want:   ErrOAuthIdentityInvalid,
		},
		{
			name:   "wrong nonce",
			mutate: func(payload *idtoken.Payload) { payload.Claims["nonce"] = "wrong-signed-nonce" },
			want:   ErrOAuthIdentityInvalid,
		},
		{
			name:   "missing subject",
			mutate: func(payload *idtoken.Payload) { payload.Subject = "" },
			want:   ErrOAuthIdentityInvalid,
		},
		{
			name:   "oversize subject",
			mutate: func(payload *idtoken.Payload) { payload.Subject = strings.Repeat("a", 256) },
			want:   ErrOAuthIdentityInvalid,
		},
		{
			name:   "non-ASCII subject",
			mutate: func(payload *idtoken.Payload) { payload.Subject = "subject-é" },
			want:   ErrOAuthIdentityInvalid,
		},
		{
			name:   "missing email",
			mutate: func(payload *idtoken.Payload) { delete(payload.Claims, "email") },
			want:   ErrOAuthIdentityInvalid,
		},
		{
			name:   "invalid email",
			mutate: func(payload *idtoken.Payload) { payload.Claims["email"] = "not-an-email" },
			want:   ErrOAuthIdentityInvalid,
		},
		{
			name:   "missing verified email",
			mutate: func(payload *idtoken.Payload) { delete(payload.Claims, "email_verified") },
			want:   ErrOAuthEmailUnverified,
		},
		{
			name:   "false verified email",
			mutate: func(payload *idtoken.Payload) { payload.Claims["email_verified"] = false },
			want:   ErrOAuthEmailUnverified,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := &idtoken.Payload{
				Issuer: "https://accounts.google.com", Audience: "web-client",
				Expires: now.Add(time.Minute).Unix(), Subject: "subject-123",
				Claims: map[string]any{
					"email": "user@example.com", "email_verified": true, "nonce": expectedNonce,
				},
			}
			test.mutate(payload)
			provider := newGoogleProviderForPayloadTest(t, now, payload)

			_, err := provider.Exchange(context.Background(), OAuthExchangeRequest{
				Client:      ClientWeb,
				RedirectURI: "https://api.zenith-team.test/callback",
				Code:        "sentinel-code",
				NonceHash:   expectedNonceHash[:],
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("Exchange() error = %v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), "sentinel") ||
				strings.Contains(err.Error(), "user@example.com") {
				t.Fatalf("Exchange() leaked identity/provider material in %q", err)
			}
		})
	}
}

func TestGoogleProviderDiscardsInvalidOrOversizedOptionalClaims(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 6, 0, 0, 0, time.UTC)
	nonce := "expected-signed-nonce"
	nonceHash := sha256.Sum256([]byte(nonce))
	for _, locale := range []string{"../../provider-token", "en--US", "-en", "en-", "en__US"} {
		payload := &idtoken.Payload{
			Issuer: "https://accounts.google.com", Audience: "web-client",
			Expires: now.Add(time.Minute).Unix(), Subject: "subject-123",
			Claims: map[string]any{
				"email": "user@example.com", "email_verified": true, "nonce": nonce,
				"name":                   strings.Repeat("n", 120+1),
				"picture":                "http://images.example.test/avatar.png",
				"locale":                 locale,
				"hd":                     "../not-a-hosted-domain",
				"unknown_provider_token": "sentinel-unknown-provider-token",
			},
		}
		provider := newGoogleProviderForPayloadTest(t, now, payload)

		result, err := provider.Exchange(context.Background(), OAuthExchangeRequest{
			Client: ClientWeb, RedirectURI: "https://api.zenith-team.test/callback", Code: "sentinel-code",
			NonceHash: nonceHash[:],
		})
		if err != nil {
			t.Fatalf("Exchange(locale=%q) error = %v", locale, err)
		}
		if result.Identity.Profile != (OAuthProfileSnapshot{}) {
			t.Fatalf(
				"invalid optional profile escaped allowlist for locale %q = %#v",
				locale,
				result.Identity.Profile,
			)
		}
		if strings.Contains(fmt.Sprintf("%#v", result), "sentinel-unknown-provider-token") {
			t.Fatalf("unknown claim escaped allowlist: %#v", result)
		}
	}
}

func TestGoogleProviderTokenEndpointFailuresAreClassifiedAndRedacted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		transport roundTripFunc
		cancel    bool
		want      error
	}{
		{
			name: "provider denial",
			transport: func(request *http.Request) (*http.Response, error) {
				return googleTokenErrorResponse(
					request,
					http.StatusBadRequest,
					`{"error":"access_denied","error_description":"sentinel-provider-denial-body sentinel-provider-code"}`,
				), nil
			},
			want: ErrOAuthProviderDenied,
		},
		{
			name: "provider unavailable",
			transport: func(request *http.Request) (*http.Response, error) {
				return googleTokenErrorResponse(
					request,
					http.StatusServiceUnavailable,
					`{"error":"server_error","error_description":"sentinel-provider-body sentinel-access-token sentinel-id-token"}`,
				), nil
			},
			want: ErrOAuthProviderUnavailable,
		},
		{
			name: "exchange timeout",
			transport: func(request *http.Request) (*http.Response, error) {
				<-request.Context().Done()
				return nil, request.Context().Err()
			},
			want: ErrOAuthProviderUnavailable,
		},
		{
			name: "caller cancellation",
			transport: func(request *http.Request) (*http.Response, error) {
				<-request.Context().Done()
				return nil, request.Context().Err()
			},
			cancel: true,
			want:   ErrOAuthProviderUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			httpClient := &http.Client{Timeout: time.Second, Transport: test.transport}
			provider, err := NewGoogleProvider(GoogleProviderOptions{
				Web: GoogleOAuthClientConfig{ClientID: "web-client", ClientSecret: "web-secret"},
				Desktop: GoogleOAuthClientConfig{
					ClientID:     "desktop-client",
					ClientSecret: "desktop-secret",
				},
				HTTPClient:      httpClient,
				ExchangeTimeout: 10 * time.Millisecond,
				validator: &fakeGoogleIDTokenValidator{
					validate: func(context.Context, string, string) (*idtoken.Payload, error) {
						t.Fatal("validator called after token endpoint failure")
						return nil, nil
					},
				},
			})
			if err != nil {
				t.Fatalf("NewGoogleProvider() error = %v", err)
			}
			ctx := context.Background()
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			_, err = provider.Exchange(ctx, OAuthExchangeRequest{
				Client:      ClientWeb,
				RedirectURI: "https://api.zenith-team.test/callback",
				Code:        "sentinel-provider-code",
				NonceHash:   make([]byte, sha256.Size),
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("Exchange() error = %v, want %v", err, test.want)
			}
			for _, forbidden := range []string{"sentinel-provider", "sentinel-access", "sentinel-id-token", "access_denied", "server_error"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("Exchange() leaked %q in %q", forbidden, err)
				}
			}
		})
	}
}

func googleTokenErrorResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func newGoogleProviderForPayloadTest(t *testing.T, now time.Time, payload *idtoken.Payload) *GoogleProvider {
	t.Helper()
	httpClient := &http.Client{
		Timeout: time.Second,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(
					strings.NewReader(
						`{"access_token":"sentinel-access","token_type":"Bearer","id_token":"sentinel-id-token"}`,
					),
				),
				Request: request,
			}, nil
		}),
	}
	provider, err := NewGoogleProvider(GoogleProviderOptions{
		Web:        GoogleOAuthClientConfig{ClientID: "web-client", ClientSecret: "web-secret"},
		Desktop:    GoogleOAuthClientConfig{ClientID: "desktop-client", ClientSecret: "desktop-secret"},
		HTTPClient: httpClient, ExchangeTimeout: time.Second,
		validator: &fakeGoogleIDTokenValidator{
			validate: func(context.Context, string, string) (*idtoken.Payload, error) { return payload, nil },
		},
		now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewGoogleProvider() error = %v", err)
	}
	return provider
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type fakeGoogleIDTokenValidator struct {
	validate func(context.Context, string, string) (*idtoken.Payload, error)
}

func (f *fakeGoogleIDTokenValidator) Validate(
	ctx context.Context,
	rawIDToken, audience string,
) (*idtoken.Payload, error) {
	return f.validate(ctx, rawIDToken, audience)
}

// TestOAuthServiceRejectsWebStartForDesktopOnlyProvider pins that a provider
// configured for desktop only refuses Web starts outright — before the flow
// row, and without requiring a Web redirect URI at construction.
func TestOAuthServiceRejectsWebStartForDesktopOnlyProvider(t *testing.T) {
	t.Parallel()

	provider, err := NewGoogleProvider(GoogleProviderOptions{
		Desktop: GoogleOAuthClientConfig{
			ClientID: "desktop-client.apps.googleusercontent.com", ClientSecret: "desktop-secret",
		},
		HTTPClient:      &http.Client{Timeout: time.Second},
		ExchangeTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewGoogleProvider() error = %v", err)
	}
	service, err := NewOAuthService(OAuthServiceOptions{
		Store:    &db.Store{},
		Provider: provider,
		Tokens:   &fakeOAuthTokenIssuer{},
		Random:   bytes.NewReader(make([]byte, 64)),
	})
	if err != nil {
		t.Fatalf("NewOAuthService() error = %v", err)
	}
	if !service.ProviderEnabled() {
		t.Fatal("desktop-only OAuth service reports disabled")
	}
	if service.WebEnabled() || !service.DesktopEnabled() {
		t.Fatalf(
			"desktop-only OAuth capabilities = web %t, desktop %t",
			service.WebEnabled(),
			service.DesktopEnabled(),
		)
	}
	if _, err := service.StartWeb(
		context.Background(),
		WebOAuthStartInput{ReturnPath: "/"},
	); !errors.Is(
		err,
		ErrOAuthNotConfigured,
	) {
		t.Fatalf("StartWeb() error = %v, want ErrOAuthNotConfigured", err)
	}
}
