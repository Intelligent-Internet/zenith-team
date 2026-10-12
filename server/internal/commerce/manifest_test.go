package commerce

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func TestParseManifestCanonicalizesClosedV2Contract(t *testing.T) {
	t.Parallel()
	document, err := ParseManifest([]byte(`{
		"offers":[
			{"currency":"gbp","amount":1200,"alias":"pro_monthly","displayName":"Pro","type":"subscription","planGroup":"pro","entitlement":"plan.pro","interval":"month","automaticTax":true,"successPath":"/billing/processing","cancelPath":"/pricing"},
			{"alias":"report","displayName":"Premium report","type":"one_time","entitlement":"report.premium","amount":2900,"currency":"usd","automaticTax":false,"successPath":"/reports/processing","cancelPath":"/reports"}
		],
		"site":{"portalReturnPath":"/settings/billing","origin":"https://store.example.com","alias":"store"},
		"schemaVersion":2
	}`), EnvironmentLive)
	if err != nil {
		t.Fatalf("ParseManifest() error = %v", err)
	}
	if document.Manifest.SchemaVersion != 2 || document.Manifest.Site.Alias != "store" ||
		len(document.Manifest.Offers) != 2 || bytes.Contains(document.CanonicalJSON, []byte("\n")) ||
		len(hex.EncodeToString(document.Digest[:])) != 64 {
		t.Fatalf("ParseManifest() = %+v / %q / %x", document.Manifest, document.CanonicalJSON, document.Digest)
	}
	replay, err := ParseManifest(document.CanonicalJSON, EnvironmentLive)
	if err != nil || !bytes.Equal(replay.CanonicalJSON, document.CanonicalJSON) ||
		replay.Digest != document.Digest {
		t.Fatalf("canonical replay = %q / %x / %v", replay.CanonicalJSON, replay.Digest, err)
	}
}

func TestParseManifestRejectsRetiredAuthorityAndUnsupportedFeatures(t *testing.T) {
	t.Parallel()
	valid := `{"schemaVersion":2,"site":{"alias":"store","origin":"https://store.example.com","portalReturnPath":"/settings/billing"},"offers":[{"alias":"pro","displayName":"Pro","type":"subscription","planGroup":"pro","entitlement":"plan.pro","amount":1200,"currency":"gbp","interval":"month","automaticTax":true,"successPath":"/billing/processing","cancelPath":"/pricing"}]}`
	tests := []struct {
		name     string
		document string
	}{
		{name: "schema v1", document: strings.Replace(valid, `"schemaVersion":2`, `"schemaVersion":1`, 1)},
		{
			name: "retired auth mode",
			document: strings.Replace(
				valid,
				`"portalReturnPath"`,
				`"authMode":"boardly_sandbox","portalReturnPath"`,
				1,
			),
		},
		{
			name: "retired OIDC",
			document: strings.Replace(
				valid,
				`"portalReturnPath"`,
				`"oidc":{"issuer":"https://id.example","audience":"api"},"portalReturnPath"`,
				1,
			),
		},
		{
			name: "caller price",
			document: strings.Replace(
				valid,
				`"amount":1200`,
				`"priceId":"price_attacker","amount":1200`,
				1,
			),
		},
		{
			name: "absolute success",
			document: strings.Replace(
				valid,
				`"successPath":"/billing/processing"`,
				`"successPath":"https://evil.example/paid"`,
				1,
			),
		},
		{
			name:     "trial",
			document: strings.Replace(valid, `"interval":"month"`, `"interval":"month","trialDays":30`, 1),
		},
		{name: "zero amount", document: strings.Replace(valid, `"amount":1200`, `"amount":0`, 1)},
		{
			name:     "uppercase origin",
			document: strings.Replace(valid, `https://store.example.com`, `https://Store.Example.com`, 1),
		},
		{
			name: "default port",
			document: strings.Replace(
				valid,
				`https://store.example.com`,
				`https://store.example.com:443`,
				1,
			),
		},
		{
			name: "origin path",
			document: strings.Replace(
				valid,
				`https://store.example.com`,
				`https://store.example.com/shop`,
				1,
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseManifest([]byte(test.document), EnvironmentLive); err == nil {
				t.Fatal("ParseManifest() error = nil")
			}
		})
	}
}

func TestParseManifestAllowsLoopbackOnlyInSandbox(t *testing.T) {
	t.Parallel()
	base := `{"schemaVersion":2,"site":{"alias":"store","origin":"%s","portalReturnPath":"/billing"},"offers":[{"alias":"report","displayName":"Report","type":"one_time","entitlement":"report.access","amount":1200,"currency":"gbp","automaticTax":false,"successPath":"/processing","cancelPath":"/pricing"}]}`
	tests := []struct {
		name        string
		environment Environment
		origin      string
		valid       bool
	}{
		{
			name:        "sandbox HTTPS",
			environment: EnvironmentSandbox,
			origin:      "https://preview.example",
			valid:       true,
		},
		{name: "live HTTPS", environment: EnvironmentLive, origin: "https://store.example", valid: true},
		{
			name:        "sandbox localhost",
			environment: EnvironmentSandbox,
			origin:      "http://localhost:5173",
			valid:       true,
		},
		{
			name:        "sandbox localhost subdomain",
			environment: EnvironmentSandbox,
			origin:      "http://store.localhost:5173",
			valid:       true,
		},
		{name: "sandbox IPv4", environment: EnvironmentSandbox, origin: "http://127.0.0.1:5173", valid: true},
		{name: "sandbox IPv6", environment: EnvironmentSandbox, origin: "http://[::1]:5173", valid: true},
		{name: "live localhost", environment: EnvironmentLive, origin: "http://localhost:5173"},
		{name: "public HTTP", environment: EnvironmentSandbox, origin: "http://store.example"},
		{name: "default HTTP port", environment: EnvironmentSandbox, origin: "http://localhost:80"},
		{name: "loopback query", environment: EnvironmentSandbox, origin: "http://localhost:5173?x=1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseManifest([]byte(fmt.Sprintf(base, test.origin)), test.environment)
			if got := err == nil; got != test.valid {
				t.Fatalf("ParseManifest() valid = %t, want %t; error = %v", got, test.valid, err)
			}
		})
	}
}

func TestParseManifestRejectsDuplicateAliasesAndInconsistentPlanGroups(t *testing.T) {
	t.Parallel()
	base := `{"schemaVersion":2,"site":{"alias":"store","origin":"https://store.example.com","portalReturnPath":"/billing"},"offers":[%s]}`
	monthly := `{"alias":"pro_month","displayName":"Pro","type":"subscription","planGroup":"pro","entitlement":"plan.pro","amount":1200,"currency":"gbp","interval":"month","automaticTax":true,"successPath":"/processing","cancelPath":"/pricing"}`
	tests := []string{
		monthly + `,` + strings.Replace(monthly, `"amount":1200`, `"amount":2400`, 1),
		monthly + `,` + strings.NewReplacer(`"alias":"pro_month"`, `"alias":"pro_year"`, `"displayName":"Pro"`, `"displayName":"Other"`, `"interval":"month"`, `"interval":"year"`).
			Replace(monthly),
		monthly + `,` + strings.NewReplacer(`"alias":"pro_month"`, `"alias":"pro_year"`, `"entitlement":"plan.pro"`, `"entitlement":"plan.other"`, `"interval":"month"`, `"interval":"year"`).
			Replace(monthly),
	}
	for index, offers := range tests {
		if _, err := ParseManifest([]byte(fmt.Sprintf(base, offers)), EnvironmentLive); err == nil {
			t.Fatalf("case %d ParseManifest() error = nil", index)
		}
	}
}

func TestValidateManifestRejectsPortalCatalogShapesProviderCannotRepresent(t *testing.T) {
	t.Parallel()
	manifest := Manifest{
		SchemaVersion: 2,
		Site: ManifestSite{
			Alias:            "store",
			Origin:           "https://store.example.com",
			PortalReturnPath: "/billing",
		},
	}
	for index := range maximumPortalSubscriptionProducts + 1 {
		manifest.Offers = append(manifest.Offers, ManifestOffer{
			Alias: fmt.Sprintf("offer_%02d", index), DisplayName: fmt.Sprintf("Plan %02d", index),
			Type: OfferTypeSubscription, PlanGroup: fmt.Sprintf("group_%02d", index),
			Entitlement: fmt.Sprintf("plan.%02d", index), Amount: int64(1000 + index),
			Currency: "gbp", Interval: "month", AutomaticTax: true,
			SuccessPath: "/processing", CancelPath: "/pricing",
		})
	}
	if err := validateManifest(manifest, EnvironmentLive); err == nil {
		t.Fatal("validateManifest() accepted too many Customer Portal products")
	}
}
