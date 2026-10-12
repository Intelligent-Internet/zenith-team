package commerce

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	manifestSchemaVersion             = 2
	maxManifestBytes                  = 64 << 10
	maxManifestOffers                 = 100
	maximumPortalSubscriptionProducts = 10
)

var (
	manifestAliasPattern    = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
	entitlementKeyPattern   = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{1,127}$`)
	manifestCurrencyPattern = regexp.MustCompile(`^[a-z]{3}$`)
	originHostnamePattern   = regexp.MustCompile(`^[a-z0-9.-]+$`)
	errInvalidManifest      = errors.New("invalid Commerce manifest")
)

// Manifest is the closed, versioned Task-authored Commerce contract. It never
// accepts provider IDs, absolute return URLs, customer identity, or mutable
// quantities from a Task.
type Manifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	Site          ManifestSite    `json:"site"`
	Offers        []ManifestOffer `json:"offers"`
}

type ManifestSite struct {
	Alias            string `json:"alias"`
	Origin           string `json:"origin"`
	PortalReturnPath string `json:"portalReturnPath"`
}

type OfferType string

const (
	OfferTypeOneTime      OfferType = "one_time"
	OfferTypeSubscription OfferType = "subscription"
)

type ManifestOffer struct {
	Alias        string    `json:"alias"`
	DisplayName  string    `json:"displayName"`
	Type         OfferType `json:"type"`
	PlanGroup    string    `json:"planGroup,omitempty"`
	Entitlement  string    `json:"entitlement"`
	Amount       int64     `json:"amount"`
	Currency     string    `json:"currency"`
	Interval     string    `json:"interval,omitempty"`
	AutomaticTax bool      `json:"automaticTax"`
	SuccessPath  string    `json:"successPath"`
	CancelPath   string    `json:"cancelPath"`
}

type ManifestDocument struct {
	Manifest      Manifest
	CanonicalJSON []byte
	Digest        [sha256.Size]byte
}

// ParseManifest strictly decodes, validates, orders, and canonicalizes one V2
// manifest for its selected environment. The returned digest is the exact
// owner-approval boundary.
func ParseManifest(raw []byte, environment Environment) (ManifestDocument, error) {
	if len(raw) == 0 || len(raw) > maxManifestBytes || !utf8.Valid(raw) {
		return ManifestDocument{}, errInvalidManifest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return ManifestDocument{}, fmt.Errorf("%w: decode document", errInvalidManifest)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ManifestDocument{}, fmt.Errorf("%w: trailing document", errInvalidManifest)
	}
	if err := validateManifest(manifest, environment); err != nil {
		return ManifestDocument{}, err
	}
	manifest.Offers = slices.Clone(manifest.Offers)
	slices.SortFunc(manifest.Offers, func(left, right ManifestOffer) int {
		return strings.Compare(left.Alias, right.Alias)
	})
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return ManifestDocument{}, fmt.Errorf("encode canonical Commerce manifest: %w", err)
	}
	return ManifestDocument{
		Manifest: manifest, CanonicalJSON: canonical, Digest: sha256.Sum256(canonical),
	}, nil
}

func validateManifest(manifest Manifest, environment Environment) error {
	if !environment.valid() || manifest.SchemaVersion != manifestSchemaVersion ||
		!manifestAliasPattern.MatchString(manifest.Site.Alias) ||
		!validManifestSiteAuthority(manifest.Site, environment) ||
		!validReturnPath(manifest.Site.PortalReturnPath) ||
		len(manifest.Offers) == 0 || len(manifest.Offers) > maxManifestOffers {
		return errInvalidManifest
	}
	aliases := make(map[string]struct{}, len(manifest.Offers))
	planGroups := make(map[string]ManifestOffer)
	planGroupIntervals := make(map[string]map[string]struct{})
	for _, offer := range manifest.Offers {
		if err := validateManifestOffer(offer); err != nil {
			return err
		}
		if _, exists := aliases[offer.Alias]; exists {
			return errInvalidManifest
		}
		aliases[offer.Alias] = struct{}{}
		if offer.Type != OfferTypeSubscription {
			continue
		}
		if first, exists := planGroups[offer.PlanGroup]; exists {
			if first.DisplayName != offer.DisplayName ||
				first.Entitlement != offer.Entitlement || first.Currency != offer.Currency {
				return errInvalidManifest
			}
		} else {
			planGroups[offer.PlanGroup] = offer
			planGroupIntervals[offer.PlanGroup] = make(map[string]struct{}, 2)
		}
		if _, exists := planGroupIntervals[offer.PlanGroup][offer.Interval]; exists {
			return errInvalidManifest
		}
		planGroupIntervals[offer.PlanGroup][offer.Interval] = struct{}{}
	}
	if len(planGroups) > maximumPortalSubscriptionProducts {
		return errInvalidManifest
	}
	return nil
}

func validManifestSiteAuthority(site ManifestSite, environment Environment) bool {
	return validSiteOrigin(site.Origin, environment)
}

func validateManifestOffer(offer ManifestOffer) error {
	if !manifestAliasPattern.MatchString(offer.Alias) ||
		!validDisplayName(offer.DisplayName) ||
		!entitlementKeyPattern.MatchString(offer.Entitlement) ||
		offer.Amount < 1 || offer.Amount > 999999999999 ||
		!manifestCurrencyPattern.MatchString(offer.Currency) ||
		!validReturnPath(offer.SuccessPath) || !validReturnPath(offer.CancelPath) {
		return errInvalidManifest
	}
	switch offer.Type {
	case OfferTypeOneTime:
		if offer.PlanGroup != "" || offer.Interval != "" {
			return errInvalidManifest
		}
	case OfferTypeSubscription:
		if !manifestAliasPattern.MatchString(offer.PlanGroup) ||
			offer.Interval != "month" && offer.Interval != "year" {
			return errInvalidManifest
		}
	default:
		return errInvalidManifest
	}
	return nil
}

func validDisplayName(value string) bool {
	return len(value) <= 255 && utf8.RuneCountInString(value) >= 1 &&
		strings.TrimSpace(value) == value && !containsControl(value)
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func validHTTPSOrigin(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || len(value) > 2048 {
		return false
	}
	hostname := parsed.Hostname()
	if hostname == "" || strings.HasSuffix(hostname, ".") ||
		hostname != strings.ToLower(hostname) || !originHostnamePattern.MatchString(hostname) ||
		!validCanonicalOriginHostname(hostname) {
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
	return parsed.Host == canonicalHost && value == "https://"+canonicalHost
}

func validSiteOrigin(value string, environment Environment) bool {
	return validHTTPSOrigin(value) ||
		environment == EnvironmentSandbox && validLoopbackHTTPOrigin(value)
}

func validLoopbackHTTPOrigin(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Host == "" ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Opaque != "" || len(value) > 2048 {
		return false
	}
	hostname := parsed.Hostname()
	if hostname == "" || strings.HasSuffix(hostname, ".") || hostname != strings.ToLower(hostname) {
		return false
	}
	canonicalHostname := hostname
	address := net.ParseIP(hostname)
	switch {
	case address != nil:
		if !address.IsLoopback() {
			return false
		}
		canonicalHostname = address.String()
	case hostname != "localhost" && !strings.HasSuffix(hostname, ".localhost"):
		return false
	case !originHostnamePattern.MatchString(hostname) ||
		!validCanonicalOriginHostname(hostname):
		return false
	}
	canonicalHost := canonicalHostname
	if strings.Contains(canonicalHostname, ":") {
		canonicalHost = "[" + canonicalHostname + "]"
	}
	if port := parsed.Port(); port != "" {
		number, portErr := strconv.Atoi(port)
		if portErr != nil || number < 1 || number > 65535 || number == 80 {
			return false
		}
		canonicalHost += ":" + strconv.Itoa(number)
	}
	return parsed.Host == canonicalHost && value == "http://"+canonicalHost
}

func validCanonicalOriginHostname(hostname string) bool {
	if len(hostname) > 253 || strings.Contains(hostname, "..") ||
		strings.HasPrefix(hostname, ".") || strings.HasSuffix(hostname, ".") {
		return false
	}
	if ip := net.ParseIP(hostname); ip != nil {
		return ip.To4() != nil && ip.String() == hostname
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
	}
	return true
}

func validReturnPath(value string) bool {
	if len(value) < 1 || len(value) > 1024 || !strings.HasPrefix(value, "/") ||
		strings.HasPrefix(value, "//") || containsControl(value) {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && !parsed.IsAbs() && parsed.Host == "" &&
		parsed.RawQuery == "" && parsed.Fragment == "" && parsed.Path == value
}
