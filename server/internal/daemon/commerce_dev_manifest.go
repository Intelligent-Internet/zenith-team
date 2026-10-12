package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	commerceDevAliasPattern       = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
	commerceDevEntitlementPattern = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{1,127}$`)
	commerceDevCurrencyPattern    = regexp.MustCompile(`^[a-z]{3}$`)
)

// CommerceDevManifestSite is the Site authority the daemon needs to bind a
// local signing key. Offers remain owner-reviewed server state.
type CommerceDevManifestSite struct {
	Alias            string `json:"alias"`
	Origin           string `json:"origin"`
	PortalReturnPath string `json:"portalReturnPath"`
}

type commerceDevManifest struct {
	SchemaVersion int                        `json:"schemaVersion"`
	Site          CommerceDevManifestSite    `json:"site"`
	Offers        []commerceDevManifestOffer `json:"offers"`
}

type commerceDevManifestOffer struct {
	Alias        string `json:"alias"`
	DisplayName  string `json:"displayName"`
	Type         string `json:"type"`
	PlanGroup    string `json:"planGroup,omitempty"`
	Entitlement  string `json:"entitlement"`
	Amount       int64  `json:"amount"`
	Currency     string `json:"currency"`
	Interval     string `json:"interval,omitempty"`
	AutomaticTax bool   `json:"automaticTax"`
	SuccessPath  string `json:"successPath"`
	CancelPath   string `json:"cancelPath"`
}

// ParseCommerceDevManifest strictly validates the closed v2 manifest shape
// before a local process is given signing access.
func ParseCommerceDevManifest(raw []byte) (CommerceDevManifestSite, error) {
	if len(raw) == 0 || len(raw) > 64<<10 || !utf8.Valid(raw) {
		return CommerceDevManifestSite{}, errors.New("Commerce manifest is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest commerceDevManifest
	if err := decoder.Decode(&manifest); err != nil {
		return CommerceDevManifestSite{}, fmt.Errorf("decode Commerce manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return CommerceDevManifestSite{}, errors.New("Commerce manifest has trailing data")
	}
	if manifest.SchemaVersion != 2 || !validCommerceDevManifestSite(manifest.Site) ||
		len(manifest.Offers) == 0 || len(manifest.Offers) > 100 {
		return CommerceDevManifestSite{}, errors.New("Commerce manifest v2 is invalid")
	}
	aliases := make(map[string]struct{}, len(manifest.Offers))
	planGroups := make(map[string]commerceDevManifestOffer)
	planGroupIntervals := make(map[string]map[string]struct{})
	for _, offer := range manifest.Offers {
		if !validCommerceDevManifestOffer(offer) {
			return CommerceDevManifestSite{}, errors.New("Commerce manifest v2 has an invalid offer")
		}
		if _, exists := aliases[offer.Alias]; exists {
			return CommerceDevManifestSite{}, errors.New("Commerce manifest v2 has duplicate offer aliases")
		}
		aliases[offer.Alias] = struct{}{}
		if offer.Type != "subscription" {
			continue
		}
		if first, exists := planGroups[offer.PlanGroup]; exists {
			if first.DisplayName != offer.DisplayName || first.Entitlement != offer.Entitlement ||
				first.Currency != offer.Currency {
				return CommerceDevManifestSite{}, errors.New(
					"Commerce manifest v2 has an inconsistent subscription group",
				)
			}
		} else {
			planGroups[offer.PlanGroup] = offer
			planGroupIntervals[offer.PlanGroup] = make(map[string]struct{}, 2)
		}
		if _, exists := planGroupIntervals[offer.PlanGroup][offer.Interval]; exists {
			return CommerceDevManifestSite{}, errors.New(
				"Commerce manifest v2 repeats a subscription interval",
			)
		}
		planGroupIntervals[offer.PlanGroup][offer.Interval] = struct{}{}
	}
	if len(planGroups) > 10 {
		return CommerceDevManifestSite{}, errors.New("Commerce manifest v2 has too many subscription groups")
	}
	return manifest.Site, nil
}

func validCommerceDevManifestSite(site CommerceDevManifestSite) bool {
	return commerceDevAliasPattern.MatchString(site.Alias) &&
		validCommerceDevLoopbackOrigin(site.Origin) &&
		validCommerceDevReturnPath(site.PortalReturnPath)
}

func validCommerceDevManifestOffer(offer commerceDevManifestOffer) bool {
	if !commerceDevAliasPattern.MatchString(offer.Alias) ||
		!validCommerceDevDisplayName(offer.DisplayName) ||
		!commerceDevEntitlementPattern.MatchString(offer.Entitlement) ||
		offer.Amount < 1 || offer.Amount > 999999999999 ||
		!commerceDevCurrencyPattern.MatchString(offer.Currency) ||
		!validCommerceDevReturnPath(offer.SuccessPath) ||
		!validCommerceDevReturnPath(offer.CancelPath) {
		return false
	}
	switch offer.Type {
	case "one_time":
		return offer.PlanGroup == "" && offer.Interval == ""
	case "subscription":
		return commerceDevAliasPattern.MatchString(offer.PlanGroup) &&
			(offer.Interval == "month" || offer.Interval == "year")
	default:
		return false
	}
}

func validCommerceDevDisplayName(value string) bool {
	return len(value) <= 255 && utf8.RuneCountInString(value) >= 1 &&
		strings.TrimSpace(value) == value && !commerceDevContainsControl(value)
}

func validCommerceDevReturnPath(value string) bool {
	if len(value) < 1 || len(value) > 1024 || !strings.HasPrefix(value, "/") ||
		strings.HasPrefix(value, "//") || commerceDevContainsControl(value) {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && !parsed.IsAbs() && parsed.Host == "" &&
		parsed.RawQuery == "" && parsed.Fragment == "" && parsed.Path == value
}

func validCommerceDevLoopbackOrigin(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Host == "" ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Opaque != "" || len(value) > 2048 {
		return false
	}
	hostname := parsed.Hostname()
	if hostname == "" || strings.HasSuffix(hostname, ".") || hostname != strings.ToLower(hostname) ||
		!isLoopbackCommerceHost(hostname) {
		return false
	}
	canonicalHostname := hostname
	if address := net.ParseIP(hostname); address != nil {
		canonicalHostname = address.String()
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

func isLoopbackCommerceHost(hostname string) bool {
	if address := net.ParseIP(hostname); address != nil {
		return address.IsLoopback()
	}
	if hostname != "localhost" && !strings.HasSuffix(hostname, ".localhost") {
		return false
	}
	if len(hostname) > 253 || strings.Contains(hostname, "..") ||
		strings.HasPrefix(hostname, ".") || strings.HasSuffix(hostname, ".") {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') {
				return false
			}
		}
	}
	return true
}

func commerceDevContainsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
