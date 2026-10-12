package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"boardly.dev/boardly/server/pkg/protocol"
)

const (
	pluginTestCompanyID = "019fa834-a515-79b2-b1bd-19e6bd4f2701"
	pluginTestRuntimeID = "019fa834-a515-79b2-b1bd-19e6bd4f2702"
)

type pluginBackendStub struct {
	items     []nativePluginItem
	detail    pluginDetail
	detailErr error
	discovers []bool
}

func (s *pluginBackendStub) Discover(_ context.Context, force bool) ([]nativePluginItem, error) {
	s.discovers = append(s.discovers, force)
	return slices.Clone(s.items), nil
}

func (*pluginBackendStub) Install(context.Context, string) (nativePluginInstallResult, error) {
	return nativePluginInstallResult{}, nil
}

func (*pluginBackendStub) Uninstall(context.Context, string) error { return nil }

func (s *pluginBackendStub) ReadDetail(context.Context, string) (pluginDetail, error) {
	return s.detail, s.detailErr
}

type pluginPublisherStub struct {
	manifests chan pluginDiscoveryManifest
}

func (s *pluginPublisherStub) publishPluginDiscovery(
	_ context.Context,
	_ string,
	_ string,
	manifest pluginDiscoveryManifest,
) (discoveryPublishResult, error) {
	s.manifests <- manifest
	return discoveryPublishResult{Applied: true}, nil
}

// presentationPluginItem carries every discovery field, presentation included.
func presentationPluginItem() nativePluginItem {
	return nativePluginItem{
		NativeID: "gmail@openai-curated-remote", Name: "Gmail", Description: "Read and send email",
		Category: "Productivity", IconURL: "https://cdn.openai.com/plugins/gmail.png",
		ShareURL: "https://chatgpt.com/plugins/gmail", Status: "installed", Installed: true,
		Enabled: true, Featured: true, Action: "uninstall", CatalogRank: 0,
		ComponentCounts: nativePluginComponentCounts{Apps: 1, Skills: 2},
		ConnectorIdentities: []PluginConnectorIdentity{
			{Source: "codex_app", NativeID: "connector_gmail"},
		},
		LongDescription:   "Triage your inbox.\n\nDraft replies fast.",
		DefaultPrompts:    []string{"Summarize my unread email", "Draft a reply to the latest thread"},
		BrandColor:        "#EA4335",
		DeveloperName:     "Google",
		WebsiteURL:        "https://mail.google.com/",
		PrivacyPolicyURL:  "https://policies.google.com/privacy",
		TermsOfServiceURL: "https://policies.google.com/terms",
	}
}

func TestPluginManifestDigestCoversEveryPresentationField(t *testing.T) {
	t.Parallel()

	items := pluginManifestItems([]nativePluginItem{presentationPluginItem()}, true)
	encoded, err := json.Marshal(items[0])
	if err != nil {
		t.Fatal(err)
	}
	// The server's pluginDigestItem must encode this item byte for byte.
	const wantItem = `{"nativeId":"gmail@openai-curated-remote","name":"Gmail",` +
		`"description":"Read and send email","category":"Productivity",` +
		`"iconUrl":"https://cdn.openai.com/plugins/gmail.png",` +
		`"shareUrl":"https://chatgpt.com/plugins/gmail","status":"installed",` +
		`"installed":true,"enabled":true,"featured":true,"action":"uninstall",` +
		`"catalogRank":0,"componentCounts":{"apps":1,"mcpServers":0,"skills":2},` +
		`"connectorIdentities":[{"source":"codex_app","nativeId":"connector_gmail"}],` +
		`"longDescription":"Triage your inbox.\n\nDraft replies fast.",` +
		`"defaultPrompts":["Summarize my unread email","Draft a reply to the latest thread"],` +
		`"brandColor":"#EA4335","developerName":"Google",` +
		`"websiteUrl":"https://mail.google.com/",` +
		`"privacyPolicyUrl":"https://policies.google.com/privacy",` +
		`"termsOfServiceUrl":"https://policies.google.com/terms"}`
	if string(encoded) != wantItem {
		t.Fatalf("encoded item =\n%s\nwant\n%s", encoded, wantItem)
	}
	sum := sha256.Sum256([]byte("[" + wantItem + "]"))
	const wantDigest = "8180d8962610aea2f4a88c3d6794fc20bc8146b8cec785b840197dcd2b2a96a2"
	if got := pluginManifestDigest(items); got != hex.EncodeToString(sum[:]) || got != wantDigest {
		t.Fatalf("pluginManifestDigest() = %s, want %s", got, wantDigest)
	}
}

func TestPluginManifestItemsCarryPresentationOnlyWhenAdvertised(t *testing.T) {
	t.Parallel()

	item := presentationPluginItem()
	legacy := pluginManifestItems([]nativePluginItem{item}, false)
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"longDescription", "defaultPrompts", "brandColor", "developerName",
		"websiteUrl", "privacyPolicyUrl", "termsOfServiceUrl",
	} {
		if strings.Contains(string(encoded), `"`+field+`"`) {
			t.Fatalf("manifest without the capability carries %s: %s", field, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"shareUrl":"https://chatgpt.com/plugins/gmail"`) {
		t.Fatalf("manifest without the capability lost the share URL: %s", encoded)
	}
	if pluginManifestDigest(legacy) == pluginManifestDigest(pluginManifestItems(
		[]nativePluginItem{item}, true,
	)) {
		t.Fatal("presentation did not change the manifest digest")
	}
}

func TestPluginManifestItemsShedPresentationToFit(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("x", 4000)
	items := make([]nativePluginItem, 1600)
	for index := range items {
		items[index] = presentationPluginItem()
		items[index].NativeID = fmt.Sprintf("plugin-%04d@openai-curated-remote", index)
		items[index].CatalogRank = index
		items[index].Installed = index < 100
		items[index].LongDescription = text
	}
	summary := func(fitted []pluginDiscoveryItem) (installedText, availableText, presentation int) {
		for _, item := range fitted {
			switch {
			case item.LongDescription != "" && item.Installed:
				installedText++
			case item.LongDescription != "":
				availableText++
			}
			if item.BrandColor != "" {
				presentation++
			}
		}
		return installedText, availableText, presentation
	}

	// Stage one: only installed plugins keep their long description.
	fitted := pluginManifestItems(items, true)
	installedText, availableText, presentation := summary(fitted)
	if !pluginManifestItemsFit(fitted) || installedText != 100 || availableText != 0 ||
		presentation != len(items) {
		t.Fatalf("stage one kept %d installed and %d available long descriptions, %d presentations",
			installedText, availableText, presentation)
	}

	// Stage two: no long description fits, every other field still does.
	for index := range items {
		items[index].Installed = true
	}
	fitted = pluginManifestItems(items, true)
	installedText, availableText, presentation = summary(fitted)
	if !pluginManifestItemsFit(fitted) || installedText+availableText != 0 ||
		presentation != len(items) {
		t.Fatalf("stage two kept %d long descriptions and %d presentations",
			installedText+availableText, presentation)
	}

	// Stage three: presentation that cannot fit leaves the legacy shape.
	link := "https://example.com/" + strings.Repeat("a", 2000)
	for index := range items {
		items[index].WebsiteURL, items[index].PrivacyPolicyURL = link, link
		items[index].TermsOfServiceURL = link
	}
	fitted = pluginManifestItems(items, true)
	legacy, err := json.Marshal(pluginManifestItems(items, false))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(fitted)
	if err != nil {
		t.Fatal(err)
	}
	if !pluginManifestItemsFit(fitted) || string(encoded) != string(legacy) {
		t.Fatal("stage three did not fall back to the legacy manifest")
	}
}

func TestHeartbeatAckAdvertisingPresentationRepublishesPlugins(t *testing.T) {
	t.Parallel()

	publisher := &pluginPublisherStub{manifests: make(chan pluginDiscoveryManifest, 8)}
	service := newLocalPluginService(publisher)
	service.setBackend("codex", &pluginBackendStub{items: []nativePluginItem{presentationPluginItem()}})
	stop := service.startBackground(t.Context(), func(err error) { t.Errorf("refresh: %v", err) })
	t.Cleanup(stop)
	d := &Daemon{plugins: service}

	nextManifest := func() pluginDiscoveryManifest {
		t.Helper()
		select {
		case manifest := <-publisher.manifests:
			return manifest
		case <-time.After(5 * time.Second):
			t.Fatal("plugin discovery was not published")
			return pluginDiscoveryManifest{}
		}
	}
	expectNoManifest := func() {
		t.Helper()
		select {
		case manifest := <-publisher.manifests:
			t.Fatalf("unexpected plugin discovery publish: %+v", manifest)
		case <-time.After(200 * time.Millisecond):
		}
	}
	ack := func(capabilities ...string) {
		d.handleHeartbeatActions(pluginTestRuntimeID, &HeartbeatResponse{
			RuntimeID: pluginTestRuntimeID, Status: "ok", ServerCapabilities: capabilities,
		})
	}

	service.replaceRuntimes([]Runtime{{
		ID: pluginTestRuntimeID, CompanyID: pluginTestCompanyID, Provider: "codex",
	}})
	if manifest := nextManifest(); manifest.Items[0].BrandColor != "" {
		t.Fatalf("manifest before the capability carries presentation: %+v", manifest.Items[0])
	}

	ack(protocol.DaemonCapabilityModelDiscoveryV1, protocol.DaemonCapabilityPluginPresentationV1)
	manifest := nextManifest()
	if manifest.Items[0].BrandColor != "#EA4335" ||
		manifest.ManifestDigest != pluginManifestDigest(manifest.Items) {
		t.Fatalf("manifest after the capability = %+v", manifest)
	}
	ack(protocol.DaemonCapabilityPluginPresentationV1)
	expectNoManifest()

	ack(protocol.DaemonCapabilityModelDiscoveryV1)
	if manifest := nextManifest(); manifest.Items[0].BrandColor != "" {
		t.Fatalf("manifest after the capability disappeared carries presentation: %+v", manifest.Items[0])
	}
}

func TestLocalPluginServiceRefreshRuntimeRefetchesOnlyWhenForced(t *testing.T) {
	t.Parallel()

	publisher := &pluginPublisherStub{manifests: make(chan pluginDiscoveryManifest, 8)}
	service := newLocalPluginService(publisher)
	backend := &pluginBackendStub{items: []nativePluginItem{presentationPluginItem()}}
	service.setBackend("codex", backend)
	service.replaceRuntimes([]Runtime{{
		ID: pluginTestRuntimeID, CompanyID: pluginTestCompanyID, Provider: "codex",
	}})
	refresh := func(force bool) {
		t.Helper()
		if _, err := service.refreshRuntime(
			t.Context(), pluginTestCompanyID, pluginTestRuntimeID, "codex", force,
		); err != nil {
			t.Fatal(err)
		}
		select {
		case <-publisher.manifests:
		default:
			t.Fatalf("refreshRuntime(force=%v) published nothing", force)
		}
	}

	// Every refresh is a new observation, published even when unchanged.
	refresh(false)
	refresh(false)
	refresh(true)
	if want := []bool{false, false, true}; !slices.Equal(backend.discovers, want) {
		t.Fatalf("Discover(force) calls = %v, want %v", backend.discovers, want)
	}
}

func TestLocalPluginServiceReadDetail(t *testing.T) {
	t.Parallel()

	service := newLocalPluginService(nil)
	backend := &pluginBackendStub{
		items:  []nativePluginItem{presentationPluginItem()},
		detail: pluginDetail{NativeID: "gmail@openai-curated-remote", Name: "Gmail"},
	}
	service.setBackend("codex", backend)
	service.setBackend("claude", &pluginBackendStub{})
	service.replaceRuntimes([]Runtime{{
		ID: pluginTestRuntimeID, CompanyID: pluginTestCompanyID, Provider: "codex",
	}})
	if err := service.refreshProvider(t.Context(), "codex", false); err != nil {
		t.Fatal(err)
	}

	detail, err := service.readDetail(
		t.Context(), pluginTestCompanyID, pluginTestRuntimeID, "codex", "gmail@openai-curated-remote",
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"schemaVersion":1,"nativeId":"gmail@openai-curated-remote","name":"Gmail",` +
		`"defaultPrompts":[],"apps":[],"skills":[],"mcpServers":[]}`
	if string(encoded) != want {
		t.Fatalf("detail = %s, want %s", encoded, want)
	}

	tests := []struct {
		name      string
		companyID string
		provider  string
		nativeID  string
		want      error
	}{
		{
			"unknown plugin",
			pluginTestCompanyID,
			"codex",
			"unknown@openai-curated-remote",
			errPluginNotDiscovered,
		},
		{
			"other provider",
			pluginTestCompanyID,
			"claude",
			"gmail@openai-curated-remote",
			errPluginProviderMismatch,
		},
		{
			"other company",
			"019fa834-a515-79b2-b1bd-19e6bd4f2799",
			"claude",
			"gmail@openai-curated-remote",
			errPluginNotDiscovered,
		},
	}
	for _, test := range tests {
		if _, err := service.readDetail(
			t.Context(), test.companyID, pluginTestRuntimeID, test.provider, test.nativeID,
		); !errors.Is(err, test.want) {
			t.Errorf("%s: readDetail() error = %v, want %v", test.name, err, test.want)
		}
	}

	backend.detailErr = errPluginsUnavailable
	if _, err := service.readDetail(
		t.Context(), pluginTestCompanyID, pluginTestRuntimeID, "codex", "gmail@openai-curated-remote",
	); !errors.Is(err, errPluginsUnavailable) {
		t.Fatalf("readDetail() provider failure = %v, want errPluginsUnavailable", err)
	}
}

func TestCodexNativePluginBackendReadDetailProjectsTheLiveRead(t *testing.T) {
	t.Parallel()

	const nativeID = "gmail@openai-curated-remote"
	transport := &codexPluginTransportStub{
		catalog: codexPluginCatalog{locators: map[string]codexPluginLocator{
			nativeID: {nativeID: nativeID, remotePluginID: "plugin_gmail"},
		}},
		detail: CodexPluginDetail{
			NativeID:   nativeID,
			Name:       "Gmail",
			ShareURL:   "https://chatgpt.com/plugins/gmail",
			AuthPolicy: "on_install", InstallationInterstitial: true,
			CodexPluginPresentation: CodexPluginPresentation{
				LongDescription: "Triage your inbox.", DefaultPrompts: []string{"Summarize my inbox"},
				BrandColor: "#EA4335", DeveloperName: "Google",
			},
			Apps: []CodexPluginApp{{
				ID: "connector_gmail", Name: "Gmail", Description: "Email", Category: "Productivity",
			}},
			Skills: []CodexPluginSkill{
				{Name: "triage", DisplayName: "Triage inbox", Description: "Sort mail"},
			},
			MCPServers: []string{"gmail-mcp"},
		},
	}
	codex := newCodexPluginBackend("codex", transport)
	codex.cached = &transport.catalog

	detail, err := codexNativePluginBackend{backend: codex}.ReadDetail(t.Context(), nativeID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"schemaVersion":0,"nativeId":"gmail@openai-curated-remote","name":"Gmail",` +
		`"longDescription":"Triage your inbox.","defaultPrompts":["Summarize my inbox"],` +
		`"brandColor":"#EA4335","developerName":"Google",` +
		`"shareUrl":"https://chatgpt.com/plugins/gmail",` +
		`"authPolicy":"on_install","installationInterstitial":true,` +
		`"apps":[{"id":"connector_gmail","name":"Gmail","description":"Email","category":"Productivity"}],` +
		`"skills":[{"name":"triage","displayName":"Triage inbox","description":"Sort mail"}],` +
		`"mcpServers":["gmail-mcp"]}`
	if string(encoded) != want {
		t.Fatalf("detail = %s, want %s", encoded, want)
	}
	if _, err := (codexNativePluginBackend{backend: codex}).ReadDetail(
		t.Context(), "unknown@openai-curated-remote",
	); !errors.Is(err, errPluginNotDiscovered) {
		t.Fatalf("ReadDetail(unknown) error = %v, want errPluginNotDiscovered", err)
	}
}

type installResultPluginBackend struct {
	pluginBackendStub
	result nativePluginInstallResult
}

func (b *installResultPluginBackend) Install(context.Context, string) (nativePluginInstallResult, error) {
	return b.result, nil
}

// An install answers the provider's auth policy and the apps it still needs
// connected, so the desktop asks for them instead of opening the browser.
func TestLocalPluginServiceInstallReportsTheAppsNeedingAuth(t *testing.T) {
	t.Parallel()

	item := presentationPluginItem()
	item.Status, item.Installed, item.Enabled, item.Action = "available", false, false, "install"
	service := newLocalPluginService(nil)
	service.setBackend("codex", &installResultPluginBackend{
		pluginBackendStub: pluginBackendStub{items: []nativePluginItem{item}},
		result: nativePluginInstallResult{
			AwaitingUser: true,
			ContinueURL:  "https://chatgpt.com/apps/gmail/connector_gmail",
			AuthPolicy:   "on_install",
			AppsNeedingAuth: []pluginDetailApp{
				{ID: "connector_gmail", Name: "Gmail"},
				{ID: "connector_calendar", Name: "Google Calendar"},
			},
		},
	})
	service.replaceRuntimes([]Runtime{{
		ID: pluginTestRuntimeID, CompanyID: pluginTestCompanyID, Provider: "codex",
	}})
	if err := service.refreshProvider(t.Context(), "codex", false); err != nil {
		t.Fatal(err)
	}

	operation, err := service.install(
		t.Context(), pluginTestCompanyID, pluginTestRuntimeID, "codex", item.NativeID,
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"schemaVersion":1,"state":"awaiting_user",` +
		`"continueUrl":"https://chatgpt.com/apps/gmail/connector_gmail","authPolicy":"on_install",` +
		`"appsNeedingAuth":[{"id":"connector_gmail","name":"Gmail"},` +
		`{"id":"connector_calendar","name":"Google Calendar"}]}`
	if string(encoded) != want {
		t.Fatalf("operation = %s, want %s", encoded, want)
	}
}
