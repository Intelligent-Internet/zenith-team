package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNormalizePluginDiscoveryAcceptsVersionAgnosticUUIDs(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 25, 1, 2, 3, 0, time.UTC)
	item := validPluginObservationForTest()
	digest, err := pluginManifestDigest([]PluginObservationInput{item})
	if err != nil {
		t.Fatal(err)
	}
	input := PluginDiscoveryInput{
		CompanyID: uuid.New(), RuntimeID: uuid.New(), DaemonID: "daemon-a",
		Generation: uuid.New(), ManifestDigest: digest, ObservedAt: now,
		Items: []PluginObservationInput{item},
	}
	if _, err := normalizePluginDiscovery(ConnectorProviderCodex, input, now); err != nil {
		t.Fatalf("normalizePluginDiscovery() error = %v", err)
	}
}

func TestEncodePluginDiscoveryBatchUsesCamelCase(t *testing.T) {
	t.Parallel()

	installCount := int64(42)
	encoded, err := encodePluginDiscoveryBatch([]PluginObservationInput{{
		NativeID: "github-plugin", Name: "GitHub Plugin",
		Description: "Repository tools", Category: "Developer tools",
		IconURL:  "https://cdn.example.test/github.png",
		ShareURL: "https://example.test/plugins/github", Status: PluginStatusInstalled,
		Installed: true, Enabled: true, Featured: true, Action: PluginActionNone,
		CatalogRank: 3, ComponentCounts: PluginComponentCounts{Apps: 1, MCPServers: 2, Skills: 4},
		ConnectorIdentities: []PluginConnectorIdentity{{
			Source: ConnectorSourceCodexApp, NativeID: "github",
		}},
		InstallCount: &installCount,
	}})
	if err != nil {
		t.Fatalf("encodePluginDiscoveryBatch() error = %v", err)
	}
	want := `[{"nativeId":"github-plugin","name":"GitHub Plugin",` +
		`"description":"Repository tools","category":"Developer tools",` +
		`"iconUrl":"https://cdn.example.test/github.png",` +
		`"shareUrl":"https://example.test/plugins/github","status":"installed",` +
		`"installed":true,"enabled":true,"featured":true,"action":"none",` +
		`"catalogRank":3,"appCount":1,"mcpServerCount":2,"skillCount":4,` +
		`"connectorIdentities":[{"source":"codex_app","nativeId":"github"}],` +
		`"installCount":42,"longDescription":null,"defaultPrompts":[],"brandColor":null,` +
		`"developerName":null,"websiteUrl":null,"privacyPolicyUrl":null,"termsOfServiceUrl":null}]`
	if string(encoded) != want {
		t.Fatalf("encoded Plugin discovery batch = %s, want %s", encoded, want)
	}
}

func TestNormalizePluginObservationCanonicalizesConnectorIdentities(t *testing.T) {
	t.Parallel()

	input := validPluginObservationForTest()
	input.ConnectorIdentities = []PluginConnectorIdentity{
		{Source: ConnectorSourceCodexMCP, NativeID: "server-z"},
		{Source: ConnectorSourceCodexApp, NativeID: "connector-z"},
		{Source: ConnectorSourceCodexApp, NativeID: "connector-a"},
	}

	got, err := normalizePluginObservation(ConnectorProviderCodex, input)
	if err != nil {
		t.Fatalf("normalizePluginObservation() error = %v", err)
	}
	want := []PluginConnectorIdentity{
		{Source: ConnectorSourceCodexApp, NativeID: "connector-a"},
		{Source: ConnectorSourceCodexApp, NativeID: "connector-z"},
		{Source: ConnectorSourceCodexMCP, NativeID: "server-z"},
	}
	if !reflect.DeepEqual(got.ConnectorIdentities, want) {
		t.Fatalf("ConnectorIdentities = %+v, want %+v", got.ConnectorIdentities, want)
	}
}

func TestNormalizePluginObservationRejectsInvalidConnectorIdentities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		provider   ConnectorProvider
		identities []PluginConnectorIdentity
	}{
		{
			name:     "provider mismatch",
			provider: ConnectorProviderCodex,
			identities: []PluginConnectorIdentity{
				{Source: ConnectorSourceClaudePlugin, NativeID: "slack@official"},
			},
		},
		{
			name:     "duplicate identity",
			provider: ConnectorProviderClaude,
			identities: []PluginConnectorIdentity{
				{Source: ConnectorSourceClaudePlugin, NativeID: "slack@official"},
				{Source: ConnectorSourceClaudePlugin, NativeID: "slack@official"},
			},
		},
		{
			name:     "standalone mcp is not plugin owned",
			provider: ConnectorProviderClaude,
			identities: []PluginConnectorIdentity{
				{Source: ConnectorSourceClaudeMCP, NativeID: "standalone"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			input := validPluginObservationForTest()
			input.ConnectorIdentities = test.identities
			if _, err := normalizePluginObservation(test.provider, input); err == nil {
				t.Fatal("normalizePluginObservation() accepted invalid connector identities")
			}
		})
	}
}

func validPluginObservationForTest() PluginObservationInput {
	return PluginObservationInput{
		NativeID:    "plugin@marketplace",
		Name:        "Plugin",
		Status:      PluginStatusInstalled,
		Installed:   true,
		Enabled:     true,
		Action:      PluginActionNone,
		CatalogRank: 0,
	}
}

func TestEncodePluginDiscoveryBatchCarriesThePresentation(t *testing.T) {
	t.Parallel()

	item := validCodexPresentationForTest()
	encoded, err := encodePluginDiscoveryBatch([]PluginObservationInput{item})
	if err != nil {
		t.Fatalf("encodePluginDiscoveryBatch() error = %v", err)
	}
	want := `[{"nativeId":"plugin@marketplace","name":"Plugin","description":null,"category":"",` +
		`"iconUrl":null,"shareUrl":null,"status":"installed","installed":true,"enabled":true,` +
		`"featured":false,"action":"none","catalogRank":0,"appCount":0,"mcpServerCount":0,` +
		`"skillCount":0,"connectorIdentities":null,"installCount":null,` +
		`"longDescription":"Plan the week.\n\nThen ship it.",` +
		`"defaultPrompts":["Plan my week","Draft a status update"],"brandColor":"#1A2b3C",` +
		`"developerName":"Example Labs","websiteUrl":"https://example.test",` +
		`"privacyPolicyUrl":"https://example.test/privacy?lang=en",` +
		`"termsOfServiceUrl":"https://example.test/terms"}]`
	if string(encoded) != want {
		t.Fatalf("encoded Plugin discovery batch = %s, want %s", encoded, want)
	}
}

// The daemon digests its pluginDiscoveryItem exactly as sent; the server's
// digest item must encode byte for byte the same: the presentation fields
// appended after connectorIdentities in this order, each omitted when empty,
// so an older daemon's manifest keeps its digest.
func TestPluginManifestDigestMirrorsTheDaemonItem(t *testing.T) {
	t.Parallel()

	digestOf := func(encoded string) string {
		sum := sha256.Sum256([]byte(encoded))
		return hex.EncodeToString(sum[:])
	}
	presented := validCodexPresentationForTest()
	presented.ConnectorIdentities = []PluginConnectorIdentity{{Source: ConnectorSourceCodexApp, NativeID: "app-a"}}
	withPresentation := `[{"nativeId":"plugin@marketplace","name":"Plugin","status":"installed",` +
		`"installed":true,"enabled":true,"featured":false,"action":"none","catalogRank":0,` +
		`"componentCounts":{"apps":0,"mcpServers":0,"skills":0},` +
		`"connectorIdentities":[{"source":"codex_app","nativeId":"app-a"}],` +
		`"longDescription":"Plan the week.\n\nThen ship it.",` +
		`"defaultPrompts":["Plan my week","Draft a status update"],"brandColor":"#1A2b3C",` +
		`"developerName":"Example Labs","websiteUrl":"https://example.test",` +
		`"privacyPolicyUrl":"https://example.test/privacy?lang=en",` +
		`"termsOfServiceUrl":"https://example.test/terms"}]`
	withoutPresentation := `[{"nativeId":"plugin@marketplace","name":"Plugin","status":"installed",` +
		`"installed":true,"enabled":true,"featured":false,"action":"none","catalogRank":0,` +
		`"componentCounts":{"apps":0,"mcpServers":0,"skills":0}}]`
	for name, test := range map[string]struct {
		item    PluginObservationInput
		encoded string
	}{
		"presentation": {item: presented, encoded: withPresentation},
		"older daemon": {item: validPluginObservationForTest(), encoded: withoutPresentation},
		"empty prompts []": {item: func() PluginObservationInput {
			item := validPluginObservationForTest()
			item.DefaultPrompts = []string{}
			return item
		}(), encoded: withoutPresentation},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := pluginManifestDigest([]PluginObservationInput{test.item})
			if err != nil {
				t.Fatal(err)
			}
			if want := digestOf(test.encoded); got != want {
				t.Fatalf("pluginManifestDigest() = %s, want the digest of %s", got, test.encoded)
			}
		})
	}
}

func TestNormalizePluginDiscoveryAcceptsTheCodexPresentation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 25, 8, 0, 0, 0, time.UTC)
	item := validCodexPresentationForTest()
	// Characters, not bytes: 4000 two-byte characters and a 128-character
	// prompt of them are within bounds.
	item.LongDescription = strings.Repeat("é", 4000)
	item.DefaultPrompts = []string{strings.Repeat("é", 128)}
	digest, err := pluginManifestDigest([]PluginObservationInput{item})
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := normalizePluginDiscovery(ConnectorProviderCodex, PluginDiscoveryInput{
		CompanyID: uuid.New(), RuntimeID: uuid.New(), DaemonID: "daemon-a",
		Generation: uuid.New(), ManifestDigest: digest, ObservedAt: now,
		Items: []PluginObservationInput{item},
	}, now)
	if err != nil {
		t.Fatalf("normalizePluginDiscovery() error = %v", err)
	}
	got := normalized.Items[0]
	if got.LongDescription != item.LongDescription || !reflect.DeepEqual(got.DefaultPrompts, item.DefaultPrompts) ||
		got.BrandColor != item.BrandColor || got.TermsOfServiceURL != item.TermsOfServiceURL {
		t.Fatalf("normalized presentation = %+v", got)
	}
}

func TestNormalizePluginObservationRejectsInvalidPresentation(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		provider ConnectorProvider
		change   func(*PluginObservationInput)
	}{
		"claude long description": {ConnectorProviderClaude, func(item *PluginObservationInput) {
			item.LongDescription = "About"
		}},
		"claude prompt": {ConnectorProviderClaude, func(item *PluginObservationInput) {
			item.DefaultPrompts = []string{"Try it"}
		}},
		"claude brand colour": {ConnectorProviderClaude, func(item *PluginObservationInput) {
			item.BrandColor = "#FFFFFF"
		}},
		"claude developer": {ConnectorProviderClaude, func(item *PluginObservationInput) {
			item.DeveloperName = "Anthropic"
		}},
		"claude website": {ConnectorProviderClaude, func(item *PluginObservationInput) {
			item.WebsiteURL = "https://example.test"
		}},
		"claude privacy policy": {ConnectorProviderClaude, func(item *PluginObservationInput) {
			item.PrivacyPolicyURL = "https://example.test/privacy"
		}},
		"claude terms": {ConnectorProviderClaude, func(item *PluginObservationInput) {
			item.TermsOfServiceURL = "https://example.test/terms"
		}},
		"four prompts": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.DefaultPrompts = []string{"a", "b", "c", "d"}
		}},
		"blank prompt": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.DefaultPrompts = []string{"  "}
		}},
		"multi-line prompt": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.DefaultPrompts = []string{"Plan\nmy week"}
		}},
		"long prompt": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.DefaultPrompts = []string{strings.Repeat("p", 129)}
		}},
		"three newlines": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.LongDescription = "One.\n\n\nTwo."
		}},
		"tab in description": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.LongDescription = "One.\tTwo."
		}},
		"carriage return in description": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.LongDescription = "One.\r\nTwo."
		}},
		"long description": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.LongDescription = strings.Repeat("d", 4001)
		}},
		"invalid UTF-8 description": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.LongDescription = "bad \xff byte"
		}},
		"short brand colour": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.BrandColor = "#FFF"
		}},
		"brand colour without hash": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.BrandColor = "EA4335A"
		}},
		"non-hex brand colour": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.BrandColor = "#GG4335"
		}},
		"long developer": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.DeveloperName = strings.Repeat("n", 121)
		}},
		"multi-line developer": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.DeveloperName = "Example\nLabs"
		}},
		"http website": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.WebsiteURL = "http://example.test"
		}},
		"website with credentials": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.WebsiteURL = "https://user:secret@example.test"
		}},
		"website with fragment": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.WebsiteURL = "https://example.test/#about"
		}},
		"relative privacy policy": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.PrivacyPolicyURL = "/privacy"
		}},
		"terms without host": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.TermsOfServiceURL = "https://:443/terms"
		}},
		"javascript terms": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.TermsOfServiceURL = "javascript:alert(1)"
		}},
		// The daemon drops such a link; url.Parse alone would keep it.
		"C1 control in a website query": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.WebsiteURL = "https://example.test/?ref=\u0085x"
		}},
		"C1 control in a privacy policy query": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.PrivacyPolicyURL = "https://example.test/privacy?x=\u009f"
		}},
		"long website": {ConnectorProviderCodex, func(item *PluginObservationInput) {
			item.WebsiteURL = "https://example.test/" + strings.Repeat("w", 2030)
		}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			item := validPluginObservationForTest()
			test.change(&item)
			if _, err := normalizePluginObservation(test.provider, item); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("normalizePluginObservation() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func validCodexPresentationForTest() PluginObservationInput {
	item := validPluginObservationForTest()
	item.LongDescription = "Plan the week.\n\nThen ship it."
	item.DefaultPrompts = []string{"Plan my week", "Draft a status update"}
	item.BrandColor = "#1A2b3C"
	item.DeveloperName = "Example Labs"
	item.WebsiteURL = "https://example.test"
	item.PrivacyPolicyURL = "https://example.test/privacy?lang=en"
	item.TermsOfServiceURL = "https://example.test/terms"
	return item
}
