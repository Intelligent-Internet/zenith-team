package siteverify

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/commerce"
)

func TestProviderVerifiesClosedSiteProofAndP256Key(t *testing.T) {
	t.Parallel()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	coordinateBytes := 32
	jwk, err := commerce.NewSitePublicJWK(commerce.SitePublicKey{
		KeyType: "EC", Curve: "P-256",
		X: base64.RawURLEncoding.EncodeToString(
			privateKey.X.FillBytes(make([]byte, coordinateBytes)),
		),
		Y: base64.RawURLEncoding.EncodeToString(
			privateKey.Y.FillBytes(make([]byte, coordinateBytes)),
		),
	})
	if err != nil {
		t.Fatalf("NewSitePublicJWK() error = %v", err)
	}
	digest := strings.Repeat("a", 64)
	client := &http.Client{
		Timeout: time.Second,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.String() != "https://site.example/.well-known/boardly-site-verification.json" {
				t.Fatalf("verification URL = %q", request.URL)
			}
			body := fmt.Sprintf(
				`{"schemaVersion":2,"siteAlias":"store","manifestDigest":"%s","signingKeys":[{"kty":"%s","use":"%s","crv":"%s","kid":"%s","alg":"%s","x":"%s","y":"%s"}]}`,
				digest,
				jwk.KeyType,
				jwk.Use,
				jwk.Curve,
				jwk.KeyID,
				jwk.Algorithm,
				jwk.X,
				jwk.Y,
			)
			return jsonResponse(body), nil
		}),
	}
	provider, err := NewProvider(client)
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	verified, err := provider.VerifySite(t.Context(), commerce.SiteVerificationInput{
		Origin: "https://site.example", SiteAlias: "store", ManifestDigest: digest,
	})
	if err != nil {
		t.Fatalf("VerifySite() error = %v", err)
	}
	if len(verified.SigningKeys) != 1 || verified.SigningKeys[0].KeyID != jwk.KeyID {
		t.Fatalf("VerifySite() = %#v", verified)
	}
}

func TestProviderRejectsOpenOrOversizedSiteDocuments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{
			name: "unknown field",
			body: `{"schemaVersion":2,"siteAlias":"store","manifestDigest":"` + strings.Repeat(
				"a",
				64,
			) + `","signingKeys":[],"issuer":"attacker"}`,
		},
		{name: "oversized", body: strings.Repeat(" ", maxVerificationBytes+1)},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			provider, err := NewProvider(&http.Client{
				Timeout: time.Second,
				Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return jsonResponse(test.body), nil
				}),
			})
			if err != nil {
				t.Fatalf("NewProvider() error = %v", err)
			}
			if _, err := provider.VerifySite(t.Context(), commerce.SiteVerificationInput{
				Origin: "https://site.example", SiteAlias: "store",
				ManifestDigest: strings.Repeat("a", 64),
			}); err == nil {
				t.Fatal("VerifySite() accepted an invalid closed document")
			}
		})
	}
}

func TestProviderRequiresExactJSONMediaType(t *testing.T) {
	t.Parallel()
	provider, err := NewProvider(&http.Client{
		Timeout: time.Second,
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			response := jsonResponse(`{}`)
			response.Header.Set("Content-Type", "application/json-foo")
			return response, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	_, err = provider.VerifySite(t.Context(), commerce.SiteVerificationInput{
		Origin: "https://site.example", SiteAlias: "store",
		ManifestDigest: strings.Repeat("a", 64),
	})
	if !errors.Is(err, errInvalidSiteAuthority) {
		t.Fatalf("VerifySite() error = %v, want invalid authority", err)
	}
}

func TestValidRemoteHTTPSOriginRequiresCanonicalPublicAuthority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		origin string
		want   bool
	}{
		{origin: "https://site.example", want: true},
		{origin: "https://site.example:8443", want: true},
		{origin: "https://site.example:443"},
		{origin: "https://SITE.example"},
		{origin: "https://site.example."},
		{origin: "https://site..example"},
		{origin: "https://127.0.0.1"},
		{origin: "https://169.254.169.254"},
		{origin: "https://site.example/"},
		{origin: "https://user@site.example"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.origin, func(t *testing.T) {
			t.Parallel()
			if got := validRemoteHTTPSOrigin(test.origin); got != test.want {
				t.Fatalf("validRemoteHTTPSOrigin(%q) = %t, want %t", test.origin, got, test.want)
			}
		})
	}
}

func TestSafeHTTPClientRefusesRedirectsAndPinsTimeouts(t *testing.T) {
	t.Parallel()
	timeout := 2 * time.Second
	client, err := NewSafeHTTPClient(timeout)
	if err != nil {
		t.Fatalf("NewSafeHTTPClient() error = %v", err)
	}
	if client.Timeout != timeout {
		t.Fatalf("client timeout = %v, want %v", client.Timeout, timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil || !transport.DisableKeepAlives ||
		transport.TLSHandshakeTimeout != timeout ||
		transport.ResponseHeaderTimeout != timeout ||
		transport.MaxResponseHeaderBytes != maxVerificationHeaderBytes ||
		transport.TLSClientConfig == nil ||
		transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatalf("unsafe verification transport = %#v", transport)
	}
	request, requestErr := http.NewRequest(http.MethodGet, "https://other.example", nil)
	if requestErr != nil {
		t.Fatalf("NewRequest() error = %v", requestErr)
	}
	if redirectErr := client.CheckRedirect(request, nil); redirectErr == nil {
		t.Fatal("safe client accepted a redirect")
	}
	for _, invalid := range []time.Duration{time.Second - 1, 30*time.Second + 1} {
		if _, err := NewSafeHTTPClient(invalid); err == nil {
			t.Fatalf("NewSafeHTTPClient(%v) accepted an unsafe timeout", invalid)
		}
	}
}

func TestPublicDialContextRejectsPrivateLiteralAndDNSName(t *testing.T) {
	t.Parallel()
	dial := publicDialContext(time.Second)
	for _, address := range []string{"127.0.0.1:443", "169.254.169.254:443", "localhost:443"} {
		address := address
		t.Run(address, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			connection, err := dial(ctx, "tcp", address)
			if connection != nil {
				connection.Close()
				t.Fatalf("publicDialContext() connected to %s", address)
			}
			if err == nil {
				t.Fatalf("publicDialContext() accepted %s", address)
			}
		})
	}
}

func TestPublicAddressRejectsEveryPrivateAndReservedClass(t *testing.T) {
	t.Parallel()
	tests := []struct {
		address string
		want    bool
	}{
		{address: "8.8.8.8", want: true},
		{address: "127.0.0.1"},
		{address: "10.0.0.1"},
		{address: "169.254.169.254"},
		{address: "192.168.1.1"},
		{address: "::1"},
		{address: "fc00::1"},
		{address: "fec0::1"},
		{address: "400::1"},
		{address: "2001:db8::1"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.address, func(t *testing.T) {
			t.Parallel()
			got := publicAddress(netip.MustParseAddr(test.address))
			if got != test.want {
				t.Fatalf("publicAddress(%q) = %t, want %t", test.address, got, test.want)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
