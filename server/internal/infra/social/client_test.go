package social

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestXExchangeUsesPKCEAndDiscoversIdentity(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("code_verifier") != "verifier" ||
				r.Form.Get("redirect_uri") != "https://app.test/callback" {
				t.Error("missing bound verifier or redirect")
			}
			_, _ = w.Write(
				[]byte(
					`{"access_token":"access","refresh_token":"refresh","expires_in":7200,"scope":"users.read tweet.read offline.access"}`,
				),
			)
		case "/me":
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Error("missing access token")
			}
			_, _ = w.Write([]byte(`{"data":{"id":"42","name":"Mai","username":"mai"}}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := NewClient("x", Credentials{ClientID: "client", ClientSecret: "secret"}, server.Client())
	client.tokenURL, client.profileURL = server.URL+"/token", server.URL+"/me"
	result, err := client.Exchange(context.Background(), "code", "verifier", "https://app.test/callback")
	if err != nil || result.ExternalID != "42" || len(result.Targets) != 1 {
		t.Fatalf("exchange = %+v, %v", result, err)
	}
}

func TestAuthorizationSeparatesPlatformContracts(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"x", "facebook", "instagram", "linkedin"} {
		t.Run(platform, func(t *testing.T) {
			t.Parallel()
			client := NewClient(platform, Credentials{ClientID: "client", ClientSecret: "secret"}, nil)
			address := client.AuthorizationURL("state", "challenge", "https://app.test/callback")
			u, err := url.Parse(address)
			if err != nil {
				t.Fatal(err)
			}
			if u.Query().Get("state") != "state" ||
				u.Query().Get("redirect_uri") != "https://app.test/callback" {
				t.Fatal("missing OAuth binding")
			}
			if (u.Query().Get("code_challenge_method") == "S256") != (platform == "x") {
				t.Fatal("incorrect PKCE contract")
			}
			if strings.Contains(address, "secret") {
				t.Fatal("secret in authorization URL")
			}
		})
	}
}

func TestPlatformErrorDoesNotExposeResponseSecrets(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"access_token":"sensitive"}`))
	}))
	defer server.Close()
	client := NewClient("x", Credentials{ClientID: "client"}, server.Client())
	client.tokenURL = server.URL
	_, err := client.Exchange(context.Background(), "code", "verifier", "https://app.test/callback")
	if err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unsafe error = %v", err)
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestXRevocationDeclaresEachTokenType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		token Token
		want  []string
	}{
		{"access and refresh", Token{AccessToken: "access", RefreshToken: "refresh"}, []string{"refresh_token", "access_token"}},
		{"access only", Token{AccessToken: "access"}, []string{"access_token"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost ||
					r.URL.String() != "https://api.x.com/2/oauth2/revoke" {
					t.Errorf("revoke destination = %s %s", r.Method, r.URL)
				}
				user, password, ok := r.BasicAuth()
				if !ok || user != "client" || password != "secret" {
					t.Error("missing confidential client authentication")
				}
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				hint := r.Form.Get("token_type_hint")
				got = append(got, hint)
				if (r.Form.Get("token") == "refresh" && hint != "refresh_token") ||
					(r.Form.Get("token") == "access" && hint != "access_token") {
					return &http.Response{
						StatusCode: http.StatusBadRequest,
						Body: io.NopCloser(
							strings.NewReader(
								`{"error":"invalid_request","error_description":"Missing required parameter [token_type_hint]."}`,
							),
						),
						Header: make(http.Header),
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"revoked":true}`)),
					Header:     make(http.Header),
				}, nil
			})}
			if err := NewClient(
				"x",
				Credentials{ClientID: "client", ClientSecret: "secret"},
				client,
			).Revoke(t.Context(), tc.token); err != nil {
				t.Fatalf("revoke = %v, want nil", err)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("token types = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMetaRevocationAcceptsBooleanSuccess(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"facebook", "instagram"} {
		t.Run(platform, func(t *testing.T) {
			t.Parallel()
			client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodDelete || !strings.HasSuffix(r.URL.Path, "/me/permissions") {
					t.Fatal("unexpected revocation request")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("true")),
					Header:     make(http.Header),
				}, nil
			})}
			if err := NewClient(
				platform,
				Credentials{},
				client,
			).Revoke(t.Context(), Token{AccessToken: "token"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestXRevocationContinuesAfterAlreadyInvalidToken(t *testing.T) {
	t.Parallel()
	var hints []string
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		hint := r.Form.Get("token_type_hint")
		hints = append(hints, hint)
		if hint == "refresh_token" {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_token"}`)),
				Header:     make(http.Header),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"revoked":true}`)),
			Header:     make(http.Header),
		}, nil
	})}
	if err := NewClient(
		"x",
		Credentials{ClientID: "client"},
		client,
	).Revoke(t.Context(), Token{AccessToken: "access", RefreshToken: "refresh"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(hints, ",") != "refresh_token,access_token" {
		t.Fatalf("revoked token types = %v, want refresh and access", hints)
	}
}

func TestMetaAndLinkedInExchangeDiscoverAccounts(t *testing.T) {
	for _, platform := range []string{"facebook", "instagram", "linkedin"} {
		t.Run(platform, func(t *testing.T) {
			t.Parallel()
			pages := 0
			httpClient := &http.Client{
				Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					body := ""
					switch {
					case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "access"):
						_ = r.ParseForm()
						if r.Form.Get("code") != "code" ||
							r.Form.Get("client_secret") != "secret" ||
							r.Form.Get("redirect_uri") != "https://app.test/callback" {
							t.Error("missing confidential authorization binding")
						}
						body = `{"access_token":"short","expires_in":3600}`
					case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "access_token"):
						if r.URL.Query().Get("client_secret") != "secret" {
							t.Error("missing long-lived exchange proof")
						}
						body = `{"access_token":"long","expires_in":5000000}`
					case strings.HasSuffix(r.URL.Path, "/me/permissions"):
						body = `{"data":[{"permission":"pages_manage_posts","status":"granted"},{"permission":"instagram_business_content_publish","status":"granted"}]}`
					case strings.HasSuffix(r.URL.Path, "/me/accounts"):
						pages++
						if pages == 1 {
							body = `{"data":[{"id":"page-one","name":"First page","access_token":"page-secret"}],"paging":{"cursors":{"after":"next-page"},"next":"https://untrusted.test/ignored"}}`
						} else {
							if r.URL.Query().Get("after") != "next-page" {
								t.Error("missing page cursor")
							}
							body = `{"data":[{"id":"page-two","name":"Second page","access_token":"other-secret"}]}`
						}
					case strings.HasSuffix(r.URL.Path, "/userinfo"):
						if r.Header.Get("Authorization") != "Bearer short" {
							t.Error("missing LinkedIn token")
						}
						body = `{"sub":"member-one","name":"Mai","picture":"https://img.test/avatar.png"}`
					case strings.HasSuffix(r.URL.Path, "/me"):
						if r.Header.Get("Authorization") != "Bearer long" {
							t.Error("expected long-lived Meta token")
						}
						body = `{"id":"42","user_id":123456,"name":"Mai","username":"mai","picture":{"data":{"url":"https://img.test/avatar.png"}}}`
					default:
						t.Errorf("unexpected path %s", r.URL.Path)
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(body)),
						Header:     make(http.Header),
					}, nil
				}),
			}
			account, err := NewClient(
				platform,
				Credentials{ClientID: "client", ClientSecret: "secret"},
				httpClient,
			).Exchange(context.Background(), "code", "verifier", "https://app.test/callback")
			if err != nil {
				t.Fatal(err)
			}
			switch platform {
			case "facebook":
				if len(account.Targets) != 2 || account.Targets[0].Token.AccessToken != "page-secret" {
					t.Fatalf("page discovery = %+v", account.Targets)
				}
			case "instagram":
				if account.ExternalID != "123456" {
					t.Fatalf("Instagram identity = %q", account.ExternalID)
				}
			case "linkedin":
				if account.ExternalID != "member-one" {
					t.Fatalf("LinkedIn identity = %q", account.ExternalID)
				}
			}
		})
	}
}
