// Package siteverify is the outbound, SSRF-safe adapter for Commerce Site
// origin proofs and public signing keys. It is separate from the commerce
// domain because network policy and HTTP are infrastructure concerns.
package siteverify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"boardly.dev/boardly/server/internal/commerce"
)

const (
	maxVerificationBytes       = 16 << 10
	maxVerificationHeaderBytes = 16 << 10
)

var (
	errInvalidSiteAuthority = errors.New("invalid Commerce Site authority")
	aliasPattern            = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
	digestPattern           = regexp.MustCompile(`^[a-f0-9]{64}$`)
	originHostnamePattern   = regexp.MustCompile(`^[a-z0-9.-]+$`)
)

var nonPublicNetworkPrefixes = [...]netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

var globallyRoutedIPv6Prefix = netip.MustParsePrefix("2000::/3")

type Provider struct {
	client *http.Client
}

type verificationDocument struct {
	SchemaVersion  int                      `json:"schemaVersion"`
	SiteAlias      string                   `json:"siteAlias"`
	ManifestDigest string                   `json:"manifestDigest"`
	SigningKeys    []commerce.SitePublicJWK `json:"signingKeys"`
}

func NewProvider(client *http.Client) (*Provider, error) {
	if client == nil || client.Timeout <= 0 {
		return nil, errors.New("Commerce Site verification HTTP client with a timeout is required")
	}
	return &Provider{client: client}, nil
}

// NewSafeHTTPClient returns a client that resolves once, rejects every
// non-public address before dialing, ignores environment proxies, and refuses
// redirects so a verified origin cannot pivot into the private network.
func NewSafeHTTPClient(timeout time.Duration) (*http.Client, error) {
	if timeout < time.Second || timeout > 30*time.Second {
		return nil, errors.New("Commerce Site verification timeout must be between one and thirty seconds")
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DisableKeepAlives:      true,
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		MaxResponseHeaderBytes: maxVerificationHeaderBytes,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext:            publicDialContext(timeout),
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("Commerce Site verification redirects are not allowed")
		},
	}, nil
}

func (provider *Provider) VerifySite(
	ctx context.Context,
	input commerce.SiteVerificationInput,
) (commerce.VerifiedSite, error) {
	if provider == nil || provider.client == nil || !validRemoteHTTPSOrigin(input.Origin) ||
		!aliasPattern.MatchString(input.SiteAlias) || !digestPattern.MatchString(input.ManifestDigest) {
		return commerce.VerifiedSite{}, errInvalidSiteAuthority
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		input.Origin+"/.well-known/boardly-site-verification.json",
		nil,
	)
	if err != nil {
		return commerce.VerifiedSite{}, errInvalidSiteAuthority
	}
	request.Header.Set("Accept", "application/json")
	response, err := provider.client.Do(request)
	if err != nil {
		return commerce.VerifiedSite{}, fmt.Errorf("fetch Commerce Site verification: %w", err)
	}
	defer response.Body.Close()
	mediaType, _, mediaTypeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || mediaTypeErr != nil || mediaType != "application/json" {
		return commerce.VerifiedSite{}, errInvalidSiteAuthority
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxVerificationBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxVerificationBytes {
		return commerce.VerifiedSite{}, errInvalidSiteAuthority
	}
	var document verificationDocument
	if err := decodeClosedJSON(raw, &document); err != nil || document.SchemaVersion != 2 ||
		document.SiteAlias != input.SiteAlias || document.ManifestDigest != input.ManifestDigest ||
		len(document.SigningKeys) != 1 {
		return commerce.VerifiedSite{}, errInvalidSiteAuthority
	}
	key := document.SigningKeys[0]
	if _, err := key.PublicKey(); err != nil {
		return commerce.VerifiedSite{}, errInvalidSiteAuthority
	}
	return commerce.VerifiedSite{SigningKeys: []commerce.SitePublicJWK{key}}, nil
}

func publicDialContext(timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	resolver := net.DefaultResolver
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errInvalidSiteAuthority
		}
		addresses, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, fmt.Errorf("resolve Commerce Site verification endpoint: %w", err)
		}
		for _, resolved := range addresses {
			if !publicAddress(resolved) {
				return nil, errInvalidSiteAuthority
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
	}
}

func publicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() ||
		address.IsLinkLocalUnicast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	if address.Is6() && !globallyRoutedIPv6Prefix.Contains(address) {
		return false
	}
	for _, prefix := range nonPublicNetworkPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func validRemoteHTTPSOrigin(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.Opaque != "" || len(raw) > 2048 {
		return false
	}
	hostname := parsed.Hostname()
	if hostname == "" || hostname != strings.ToLower(hostname) ||
		!originHostnamePattern.MatchString(hostname) || !validCanonicalOriginHostname(hostname) {
		return false
	}
	if address, err := netip.ParseAddr(hostname); err == nil && !publicAddress(address) {
		return false
	}
	canonicalHost := hostname
	if port := parsed.Port(); port != "" {
		number, portErr := strconv.Atoi(port)
		if portErr != nil || number < 1 || number > 65535 || number == 443 {
			return false
		}
		canonicalHost += ":" + strconv.Itoa(number)
	}
	return parsed.Host == canonicalHost && raw == "https://"+canonicalHost
}

func validCanonicalOriginHostname(hostname string) bool {
	if len(hostname) > 253 || strings.Contains(hostname, "..") ||
		strings.HasPrefix(hostname, ".") || strings.HasSuffix(hostname, ".") {
		return false
	}
	if address := net.ParseIP(hostname); address != nil {
		return address.To4() != nil && address.String() == hostname
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
	}
	return true
}

func decodeClosedJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errInvalidSiteAuthority
	}
	return nil
}
