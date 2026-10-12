package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/db"
)

// newStorelessOAuthService builds an OAuth service over a store that owns no
// pool: every query handle is nil, so any storage call panics. The tests below
// use it wherever the assertion is "this input was rejected before the flow
// row was written or consumed".
func newStorelessOAuthService(t *testing.T, provider OAuthProvider) *DefaultOAuthService {
	t.Helper()
	service, err := NewOAuthService(OAuthServiceOptions{
		Store:          &db.Store{},
		Provider:       provider,
		Tokens:         &fakeOAuthTokenIssuer{},
		WebRedirectURI: "https://api.boardly.test/api/v1/auth/google/web/callback",
		Now:            func() time.Time { return time.Date(2026, time.July, 11, 2, 30, 0, 0, time.UTC) },
		Random:         bytes.NewReader(make([]byte, 96)),
	})
	if err != nil {
		t.Fatalf("NewOAuthService() error = %v", err)
	}
	return service
}

func TestOAuthServiceRequiresExplicitLoopbackPolicyForHTTPWebCallback(t *testing.T) {
	t.Parallel()

	base := OAuthServiceOptions{
		Store:    &db.Store{},
		Provider: &fakeOAuthProvider{},
		Tokens:   &fakeOAuthTokenIssuer{},
	}

	for _, test := range []struct {
		name      string
		redirect  string
		allowHTTP bool
		wantError bool
	}{
		{name: "HTTPS callback remains valid", redirect: "https://api.boardly.test/api/v1/auth/google/web/callback"},
		{name: "loopback HTTP requires explicit policy", redirect: "http://127.0.0.1:8788/api/v1/auth/google/web/callback", wantError: true},
		{name: "explicit loopback HTTP policy", redirect: "http://127.0.0.1:8788/api/v1/auth/google/web/callback", allowHTTP: true},
		{name: "policy never permits remote HTTP", redirect: "http://api.boardly.test/api/v1/auth/google/web/callback", allowHTTP: true, wantError: true},
		{name: "policy never permits callback query", redirect: "http://127.0.0.1:8788/api/v1/auth/google/web/callback?code=sentinel", allowHTTP: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := base
			options.WebRedirectURI = test.redirect
			options.AllowInsecureLoopbackWebRedirect = test.allowHTTP

			_, err := NewOAuthService(options)
			if test.wantError && err == nil {
				t.Fatal("NewOAuthService() error = nil")
			}
			if !test.wantError && err != nil {
				t.Fatalf("NewOAuthService() error = %v", err)
			}
		})
	}
}

func TestNewOAuthServiceRequiresStoreAndTokenIssuer(t *testing.T) {
	t.Parallel()

	if _, err := NewOAuthService(OAuthServiceOptions{
		Tokens: &fakeOAuthTokenIssuer{}, WebRedirectURI: "https://api.boardly.test/callback",
	}); err == nil {
		t.Fatal("NewOAuthService() without store error = nil")
	}
	if _, err := NewOAuthService(OAuthServiceOptions{
		Store: &db.Store{}, WebRedirectURI: "https://api.boardly.test/callback",
	}); err == nil {
		t.Fatal("NewOAuthService() without token issuer error = nil")
	}
	if _, err := NewOAuthService(OAuthServiceOptions{
		Store: &db.Store{}, Tokens: &fakeOAuthTokenIssuer{},
		WebRedirectURI: "https://api.boardly.test/callback", FlowTTL: -time.Second,
	}); err == nil {
		t.Fatal("NewOAuthService() with negative flow TTL error = nil")
	}
}

func TestOAuthServiceProviderCapabilityIsNilSafe(t *testing.T) {
	t.Parallel()

	var nilService *DefaultOAuthService
	if nilService.ProviderEnabled() || nilService.WebEnabled() || nilService.DesktopEnabled() {
		t.Fatal("nil OAuth service reported an enabled provider")
	}
	disabled, err := NewOAuthService(OAuthServiceOptions{
		Store:  &db.Store{},
		Tokens: &fakeOAuthTokenIssuer{},
	})
	if err != nil {
		t.Fatalf("NewOAuthService(disabled) error = %v", err)
	}
	if disabled.ProviderEnabled() || disabled.WebEnabled() || disabled.DesktopEnabled() {
		t.Fatal("provider-less OAuth service reported enabled")
	}
	enabled, err := NewOAuthService(OAuthServiceOptions{
		Store:          &db.Store{},
		Provider:       &fakeOAuthProvider{},
		Tokens:         &fakeOAuthTokenIssuer{},
		WebRedirectURI: "https://api.boardly.test/api/v1/auth/google/web/callback",
	})
	if err != nil {
		t.Fatalf("NewOAuthService(enabled) error = %v", err)
	}
	if !enabled.ProviderEnabled() || !enabled.WebEnabled() || !enabled.DesktopEnabled() {
		t.Fatal("configured legacy OAuth provider did not report both client capabilities")
	}
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

// TestOAuthReturnPathBoundary splits the boundary the same way the service
// does: normalization is a pure decision (asserted directly), while rejection
// must happen before the provider is called or a flow row is written — the
// storeless service turns any storage call into a panic.
func TestOAuthReturnPathBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "root", path: "/", want: "/"},
		{name: "same app path", path: "/companies/acme/tasks", want: "/companies/acme/tasks"},
		{name: "query", path: "/tasks?status=open", want: "/tasks?status=open"},
		{name: "literal query space", path: "/search?q=hello world", want: "/search?q=hello world"},
		{name: "encoded UTF-8 path", path: "/%e2%9c%93", want: "/%e2%9c%93"},
		{name: "encoded UTF-8 query", path: "/?next=%e2%9c%93", want: "/?next=%e2%9c%93"},
		{name: "contract whitespace", path: "  /onboarding  ", want: "/onboarding"},
		{name: "empty"},
		{name: "normalized empty", path: " \t\n "},
		{name: "absolute", path: "https://attacker.test/path"},
		{name: "scheme relative", path: "//attacker.test/path"},
		{name: "backslash", path: `/tasks\next`},
		{name: "encoded backslash", path: `/tasks/%5cnext`},
		{name: "encoded scheme relative", path: `/%2f%2fattacker.test`},
		{name: "encoded path control", path: `/tasks/%0anext`},
		{name: "encoded query control", path: `/tasks?next=%0dprovider-token`},
		{name: "invalid decoded path UTF-8 byte", path: `/%ff`},
		{name: "invalid decoded query UTF-8 byte", path: `/?next=%ff`},
		{name: "invalid decoded path UTF-8 sequence", path: `/%c3%28`},
		{name: "invalid decoded query UTF-8 sequence", path: `/?next=%e2%28%a1`},
		{name: "truncated decoded path UTF-8 sequence", path: `/%e2%82`},
		{name: "fragment", path: "/tasks#provider-code"},
		{name: "embedded control", path: "/tasks\x00next"},
		{name: "trailing control", path: "/tasks\n"},
		{name: "invalid UTF-8", path: string([]byte{'/', 0xff})},
		{name: "over byte limit", path: "/" + strings.Repeat("a", 2048)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			normalized, err := normalizeOAuthReturnPath(test.path)
			if test.want != "" {
				if err != nil || normalized != test.want {
					t.Fatalf(
						"normalizeOAuthReturnPath(%q) = %q, %v; want %q",
						test.path,
						normalized,
						err,
						test.want,
					)
				}
				return
			}
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf(
					"normalizeOAuthReturnPath(%q) error = %v, want ErrInvalidInput",
					test.path,
					err,
				)
			}

			providerCalls := 0
			service := newStorelessOAuthService(t, &fakeOAuthProvider{
				authorizationURL: func(OAuthAuthorizationRequest) (string, error) {
					providerCalls++
					return "https://accounts.google.test/authorize", nil
				},
			})
			if _, err := service.StartWeb(
				context.Background(),
				WebOAuthStartInput{ReturnPath: test.path},
			); !errors.Is(
				err,
				ErrInvalidInput,
			) {
				t.Fatalf("StartWeb(%q) error = %v, want ErrInvalidInput", test.path, err)
			}
			if providerCalls != 0 {
				t.Fatalf("invalid path reached the provider: %d calls", providerCalls)
			}
		})
	}
}

// TestOAuthDesktopLoopbackAndChallengeBoundary mirrors TestOAuthReturnPathBoundary
// for the desktop surface: the loopback redirect and PKCE challenge rules are
// pure decisions, and a rejection must precede the provider and the flow row.
func TestOAuthDesktopLoopbackAndChallengeBoundary(t *testing.T) {
	t.Parallel()

	validChallenge := strings.Repeat("A", 43)
	tests := []struct {
		name      string
		redirect  string
		challenge string
		valid     bool
	}{
		{name: "lowest port", redirect: "http://127.0.0.1:1024/", challenge: validChallenge, valid: true},
		{name: "ephemeral port", redirect: "http://127.0.0.1:49152/", challenge: validChallenge, valid: true},
		{name: "highest port", redirect: "http://127.0.0.1:65535/", challenge: validChallenge, valid: true},
		{name: "localhost", redirect: "http://localhost:49152/", challenge: validChallenge},
		{name: "IPv6", redirect: "http://[::1]:49152/", challenge: validChallenge},
		{name: "HTTPS", redirect: "https://127.0.0.1:49152/", challenge: validChallenge},
		{name: "userinfo", redirect: "http://user@127.0.0.1:49152/", challenge: validChallenge},
		{name: "query", redirect: "http://127.0.0.1:49152/?code=sentinel", challenge: validChallenge},
		{name: "fragment", redirect: "http://127.0.0.1:49152/#fragment", challenge: validChallenge},
		{name: "alternate path", redirect: "http://127.0.0.1:49152/callback", challenge: validChallenge},
		{name: "missing slash", redirect: "http://127.0.0.1:49152", challenge: validChallenge},
		{name: "low port", redirect: "http://127.0.0.1:1023/", challenge: validChallenge},
		{name: "high port", redirect: "http://127.0.0.1:65536/", challenge: validChallenge},
		{name: "nonnumeric port", redirect: "http://127.0.0.1:port/", challenge: validChallenge},
		{name: "leading-zero port", redirect: "http://127.0.0.1:01024/", challenge: validChallenge},
		{name: "missing challenge", redirect: "http://127.0.0.1:49152/"},
		{name: "short challenge", redirect: "http://127.0.0.1:49152/", challenge: strings.Repeat("A", 42)},
		{name: "long challenge", redirect: "http://127.0.0.1:49152/", challenge: strings.Repeat("A", 44)},
		{
			name:      "padded challenge",
			redirect:  "http://127.0.0.1:49152/",
			challenge: strings.Repeat("A", 42) + "=",
		},
		{
			name:      "noncanonical challenge",
			redirect:  "http://127.0.0.1:49152/",
			challenge: strings.Repeat("_", 43),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			redirectErr := validateDesktopOAuthRedirect(test.redirect)
			challengeErr := validateS256Challenge(test.challenge)
			if test.valid {
				if redirectErr != nil || challengeErr != nil {
					t.Fatalf(
						"validate(%q, %q) = %v / %v, want accepted",
						test.redirect,
						test.challenge,
						redirectErr,
						challengeErr,
					)
				}
				return
			}
			if redirectErr == nil && challengeErr == nil {
				t.Fatalf(
					"validate(%q, %q) accepted an invalid desktop start",
					test.redirect,
					test.challenge,
				)
			}

			providerCalls := 0
			service := newStorelessOAuthService(t, &fakeOAuthProvider{
				authorizationURL: func(OAuthAuthorizationRequest) (string, error) {
					providerCalls++
					return "https://accounts.google.test/authorize", nil
				},
			})
			if _, err := service.StartDesktop(context.Background(), DesktopOAuthStartInput{
				RedirectURI: test.redirect, CodeChallenge: test.challenge,
			}); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf(
					"StartDesktop(%q, %q) error = %v, want ErrInvalidInput",
					test.redirect,
					test.challenge,
					err,
				)
			}
			if providerCalls != 0 {
				t.Fatalf("invalid input reached the provider: %d calls", providerCalls)
			}
		})
	}
}

func TestOAuthServiceStartRejectsMalformedClosedClientMetadataBeforeSideEffects(t *testing.T) {
	t.Parallel()

	tests := []OAuthClientMetadata{
		{AppVersion: string([]byte{0xff})},
		{AppVersion: "2.0.0\nprovider-code"},
		{AppVersion: strings.Repeat("a", 256)},
		{Platform: string([]byte{0xff})},
		{Platform: "darwin\x00provider-token"},
		{Platform: strings.Repeat("p", 256)},
	}
	for index, metadata := range tests {
		providerCalls := 0
		service := newStorelessOAuthService(t, &fakeOAuthProvider{
			authorizationURL: func(OAuthAuthorizationRequest) (string, error) {
				providerCalls++
				return "https://accounts.google.test/authorize", nil
			},
		})
		_, err := service.StartWeb(
			context.Background(),
			WebOAuthStartInput{ReturnPath: "/", ClientMetadata: metadata},
		)
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("case %d StartWeb() error = %v", index, err)
		}
		if providerCalls != 0 {
			t.Fatalf("case %d malformed metadata reached the provider: %d", index, providerCalls)
		}
	}
}

func TestOAuthServiceStartRedactsProviderAuthorizationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		start func(*DefaultOAuthService) error
	}{
		{
			name: "Web",
			start: func(service *DefaultOAuthService) error {
				_, err := service.StartWeb(context.Background(), WebOAuthStartInput{ReturnPath: "/"})
				return err
			},
		},
		{
			name: "Desktop",
			start: func(service *DefaultOAuthService) error {
				_, err := service.StartDesktop(context.Background(), DesktopOAuthStartInput{
					RedirectURI: "http://127.0.0.1:49152/", CodeChallenge: strings.Repeat("A", 43),
				})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			providerCalls := 0
			// A provider failure must abort the start before the flow row is
			// written; the storeless service turns a write into a panic.
			service := newStorelessOAuthService(t, &fakeOAuthProvider{
				authorizationURL: func(OAuthAuthorizationRequest) (string, error) {
					providerCalls++
					return "", fmt.Errorf(
						"%w: sentinel-provider-body sentinel-client-secret user@example.test",
						ErrOAuthProviderUnavailable,
					)
				},
			})

			err := test.start(service)
			if !errors.Is(err, ErrOAuthProviderUnavailable) {
				t.Fatalf("Start%s() error = %v, want stable unavailable", test.name, err)
			}
			for _, forbidden := range []string{"sentinel-provider-body", "sentinel-client-secret", "user@example.test"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("Start%s() leaked %q in %q", test.name, forbidden, err)
				}
			}
			if providerCalls != 1 {
				t.Fatalf("Start%s() provider calls = %d", test.name, providerCalls)
			}
		})
	}
}

func TestOAuthServiceCompleteDesktopRejectsMalformedVerifierBeforeConsumption(t *testing.T) {
	t.Parallel()

	flowID := uuid.MustParse("0197f276-3bc0-7000-8000-000000000030")
	state := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{6}, 32))
	tests := []struct {
		name     string
		verifier string
	}{
		{name: "missing"},
		{name: "short", verifier: strings.Repeat("a", 42)},
		{name: "long", verifier: strings.Repeat("a", 129)},
		{name: "plus", verifier: strings.Repeat("a", 42) + "+"},
		{name: "slash", verifier: strings.Repeat("a", 42) + "/"},
		{name: "padding", verifier: strings.Repeat("a", 42) + "="},
		{name: "space", verifier: strings.Repeat("a", 42) + " "},
		{name: "non-ASCII", verifier: strings.Repeat("a", 42) + "é"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			providerCalls := 0
			service := newStorelessOAuthService(t, &fakeOAuthProvider{
				exchange: func(context.Context, OAuthExchangeRequest) (OAuthProviderIdentity, error) {
					providerCalls++
					return OAuthProviderIdentity{}, nil
				},
			})

			_, err := service.CompleteDesktop(context.Background(), DesktopOAuthCompleteInput{
				FlowID: flowID, Code: "sentinel-code", State: state,
				RedirectURI: "http://127.0.0.1:49152/", Verifier: test.verifier,
			})
			if !errors.Is(err, ErrOAuthFlowInvalid) {
				t.Fatalf("CompleteDesktop(%q) error = %v", test.verifier, err)
			}
			if providerCalls != 0 {
				t.Fatalf("malformed verifier reached the provider: %d", providerCalls)
			}
		})
	}
}

// TestOAuthServiceCompletionRejectsMalformedCallbackArgumentsBeforeConsumption
// pins the shared shape gate on both completion surfaces: a callback that is
// neither a clean code nor a clean provider error, or that carries a
// non-canonical state or browser binding, is rejected before the flow row is
// consumed.
func TestOAuthServiceCompletionRejectsMalformedCallbackArgumentsBeforeConsumption(t *testing.T) {
	t.Parallel()

	canonical := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	for _, test := range []struct {
		name  string
		input WebOAuthCompleteInput
	}{
		{name: "neither code nor error", input: WebOAuthCompleteInput{State: canonical, BrowserBinding: canonical}},
		{
			name:  "both code and error",
			input: WebOAuthCompleteInput{Code: "code", ProviderError: "access_denied", State: canonical, BrowserBinding: canonical},
		},
		{name: "short state", input: WebOAuthCompleteInput{Code: "code", State: strings.Repeat("a", 42), BrowserBinding: canonical}},
		{name: "padded binding", input: WebOAuthCompleteInput{Code: "code", State: canonical, BrowserBinding: strings.Repeat("a", 42) + "="}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			providerCalls := 0
			service := newStorelessOAuthService(t, &fakeOAuthProvider{
				exchange: func(context.Context, OAuthExchangeRequest) (OAuthProviderIdentity, error) {
					providerCalls++
					return OAuthProviderIdentity{}, nil
				},
			})
			if _, err := service.CompleteWeb(
				context.Background(),
				test.input,
			); !errors.Is(
				err,
				ErrOAuthFlowInvalid,
			) {
				t.Fatalf("CompleteWeb(%#v) error = %v, want ErrOAuthFlowInvalid", test.input, err)
			}
			if providerCalls != 0 {
				t.Fatalf("malformed callback reached the provider: %d", providerCalls)
			}
		})
	}
}

func TestNormalizeOAuthIdentityFallsBackForInvalidProviderNameWithoutMutatingSnapshot(t *testing.T) {
	t.Parallel()

	for _, providerName := range []string{
		"",
		"   ",
		"Provider\nName",
		strings.Repeat("n", 121),
	} {
		identity := VerifiedOAuthIdentity{
			Provider: googleOAuthProvider, Subject: "subject", Email: "fallback.user@boardly.test",
			Profile: OAuthProfileSnapshot{Name: providerName},
		}
		normalized, displayName, err := normalizeOAuthIdentity(identity)
		if err != nil {
			t.Fatalf("normalizeOAuthIdentity(%q) error = %v", providerName, err)
		}
		if displayName != "fallback.user" {
			t.Fatalf("normalizeOAuthIdentity(%q) display name = %q", providerName, displayName)
		}
		if normalized.Profile.Name != providerName {
			t.Fatalf(
				"normalizeOAuthIdentity(%q) mutated provider snapshot to %q",
				providerName,
				normalized.Profile.Name,
			)
		}
	}
	_, _, err := normalizeOAuthIdentity(VerifiedOAuthIdentity{
		Provider: googleOAuthProvider,
		Subject:  "subject",
		Email:    strings.Repeat("a", 121) + "@boardly.test",
	})
	if !errors.Is(err, ErrOAuthIdentityInvalid) {
		t.Fatalf("overlong email-local fallback error = %v", err)
	}
}

func oauthS256Challenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

type fakeOAuthProvider struct {
	authorizationURL func(OAuthAuthorizationRequest) (string, error)
	exchange         func(context.Context, OAuthExchangeRequest) (OAuthProviderIdentity, error)
}

func (f *fakeOAuthProvider) AuthorizationURL(request OAuthAuthorizationRequest) (string, error) {
	return f.authorizationURL(request)
}

func (f *fakeOAuthProvider) Exchange(ctx context.Context, request OAuthExchangeRequest) (OAuthProviderIdentity, error) {
	return f.exchange(ctx, request)
}

type fakeOAuthTokenIssuer struct {
	issueRefresh func() (IssuedRefresh, error)
	issueAccess  func(uuid.UUID, uuid.UUID) (string, time.Time, error)
}

func (f *fakeOAuthTokenIssuer) IssueRefresh() (IssuedRefresh, error) {
	return f.issueRefresh()
}

func (f *fakeOAuthTokenIssuer) IssueAccess(userID, sessionID uuid.UUID) (string, time.Time, error) {
	return f.issueAccess(userID, sessionID)
}
