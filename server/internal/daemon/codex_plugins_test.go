package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCodexPluginConnectorIdentities(t *testing.T) {
	t.Parallel()

	response := codexPluginReadResponse{}
	response.Plugin.Apps = []json.RawMessage{
		json.RawMessage(`{"id":"connector_z","name":"Z"}`),
		json.RawMessage(`{"id":"connector_a","name":"A"}`),
	}
	response.Plugin.MCPServers = []string{"server-z", "server-a"}

	got, ok := codexPluginConnectorIdentities(response)
	if !ok {
		t.Fatal("codexPluginConnectorIdentities() rejected valid plugin components")
	}
	want := []PluginConnectorIdentity{
		{Source: "codex_app", NativeID: "connector_a"},
		{Source: "codex_app", NativeID: "connector_z"},
		{Source: "codex_mcp", NativeID: "server-a"},
		{Source: "codex_mcp", NativeID: "server-z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("codexPluginConnectorIdentities() = %+v, want %+v", got, want)
	}
}

func TestCodexPluginConnectorIdentitiesRejectsInvalidOrDuplicateIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		apps    []json.RawMessage
		servers []string
	}{
		{
			name: "missing app id",
			apps: []json.RawMessage{json.RawMessage(`{"name":"Missing"}`)},
		},
		{
			name: "duplicate app id",
			apps: []json.RawMessage{
				json.RawMessage(`{"id":"connector_duplicate"}`),
				json.RawMessage(`{"id":"connector_duplicate"}`),
			},
		},
		{
			name:    "invalid mcp id",
			servers: []string{"server with spaces"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			response := codexPluginReadResponse{}
			response.Plugin.Apps = test.apps
			response.Plugin.MCPServers = test.servers
			if _, ok := codexPluginConnectorIdentities(response); ok {
				t.Fatal("codexPluginConnectorIdentities() accepted invalid identities")
			}
		})
	}
}

type codexPluginTransportStub struct {
	catalog     codexPluginCatalog
	detail      CodexPluginDetail
	readLocator codexPluginLocator
	uninstalled atomic.Bool
}

func (s *codexPluginTransportStub) Read(
	context.Context,
	string,
	bool,
) (codexPluginCatalog, error) {
	return s.catalog, nil
}

func (s *codexPluginTransportStub) ReadDetail(
	_ context.Context,
	_ string,
	locator codexPluginLocator,
) (CodexPluginDetail, error) {
	s.readLocator = locator
	return s.detail, nil
}

func (*codexPluginTransportStub) Install(
	context.Context,
	string,
	codexPluginLocator,
) (CodexPluginInstallResult, error) {
	return CodexPluginInstallResult{}, nil
}

func (s *codexPluginTransportStub) Uninstall(
	context.Context,
	string,
	codexPluginLocator,
) error {
	s.uninstalled.Store(true)
	return nil
}

func TestCodexPluginBackendUninstallCancelsPendingOAuth(t *testing.T) {
	t.Parallel()

	const nativeID = "cloudflare@openai-curated-remote"
	transport := &codexPluginTransportStub{catalog: codexPluginCatalog{
		locators: map[string]codexPluginLocator{
			nativeID: {nativeID: nativeID, action: CodexPluginActionUninstall},
		},
	}}
	backend := newCodexPluginBackend("codex", transport)
	backend.cached = &transport.catalog
	flowContext, cancel := context.WithCancel(t.Context())
	flow := newCodexPluginOAuthFlow(
		flowContext,
		cancel,
		&connectorRPCFixture{},
		[]string{"cloudflare-api"},
	)
	backend.observeOAuthFlow(nativeID, flow)

	var changeCount atomic.Int32
	backend.SetChangeHandler(func() { changeCount.Add(1) })
	if err := backend.Uninstall(t.Context(), nativeID); err != nil {
		t.Fatal(err)
	}
	backend.oauthWorkers.Wait()

	if !transport.uninstalled.Load() {
		t.Fatal("provider uninstall was not invoked")
	}
	select {
	case <-flow.done:
	default:
		t.Fatal("pending OAuth flow was not canceled")
	}
	backend.mu.RLock()
	_, flowStillActive := backend.oauthFlows[nativeID]
	backend.mu.RUnlock()
	if flowStillActive {
		t.Fatal("pending OAuth flow remains registered after uninstall")
	}
	if got := changeCount.Load(); got != 1 {
		t.Fatalf("change notifications = %d, want 1", got)
	}
}

type codexPluginCatalogRPCFixture struct {
	catalog json.RawMessage
	// listErr, when set, fails plugin/list the way the session would.
	listErr error
	// reads answers plugin/read by the request's pluginName.
	reads map[string]json.RawMessage
}

func (s *codexPluginCatalogRPCFixture) request(
	_ context.Context,
	method string,
	params any,
) (json.RawMessage, error) {
	switch method {
	case "experimentalFeature/list":
		return json.RawMessage(`{
			"data":[{"name":"plugins","stage":"underDevelopment","enabled":true}],
			"nextCursor":""
		}`), nil
	case "plugin/list":
		if s.listErr != nil {
			return nil, s.listErr
		}
		return s.catalog, nil
	case "plugin/read":
		mapped, _ := params.(map[string]any)
		pluginName, _ := mapped["pluginName"].(string)
		if read, ok := s.reads[pluginName]; ok {
			return read, nil
		}
		return nil, errors.New("unexpected Codex plugin read")
	default:
		return nil, errors.New("unexpected Codex plugin RPC method")
	}
}

func (*codexPluginCatalogRPCFixture) nextNotification(
	context.Context,
) (codexRPCNotification, error) {
	return codexRPCNotification{}, errors.New("unexpected Codex plugin notification read")
}

func (*codexPluginCatalogRPCFixture) drainNotifications() (
	[]codexRPCNotification,
	bool,
) {
	return nil, false
}

func (*codexPluginCatalogRPCFixture) close() {}

func TestReadCodexPluginCatalogQuarantinesAmbiguousIDs(t *testing.T) {
	t.Parallel()

	marketplaces := []codexPluginMarketplace{
		{
			Name: "openai-curated-remote",
			Plugins: []codexPluginSummary{
				remoteCodexPluginSummary("alpha@openai-curated-remote", "plugin_alpha"),
				remoteCodexPluginSummary("conductor@openai-curated-remote", "plugin_old"),
				remoteCodexPluginSummary("bravo@openai-curated-remote", "plugin_bravo"),
			},
		},
		{
			Name: "openai-curated-remote",
			Plugins: []codexPluginSummary{
				remoteCodexPluginSummary("conductor@openai-curated-remote", "plugin_new"),
				remoteCodexPluginSummary("charlie@openai-curated-remote", "plugin_charlie"),
				remoteCodexPluginSummary("conductor@openai-curated-remote", "plugin_newest"),
			},
		},
	}
	featured := []string{"conductor@openai-curated-remote"}
	rawCatalog, err := json.Marshal(codexPluginListResponse{
		Marketplaces:      &marketplaces,
		FeaturedPluginIDs: &featured,
	})
	if err != nil {
		t.Fatal(err)
	}

	catalog, err := readCodexPluginCatalog(
		t.Context(),
		&codexPluginCatalogRPCFixture{catalog: rawCatalog},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}

	want := []struct {
		nativeID string
		rank     int
	}{
		{nativeID: "alpha@openai-curated-remote", rank: 0},
		{nativeID: "bravo@openai-curated-remote", rank: 1},
		{nativeID: "charlie@openai-curated-remote", rank: 2},
	}
	got := make([]struct {
		nativeID string
		rank     int
	}, 0, len(catalog.plugins))
	for _, plugin := range catalog.plugins {
		got = append(got, struct {
			nativeID string
			rank     int
		}{nativeID: plugin.NativeID, rank: plugin.CatalogRank})
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readCodexPluginCatalog() plugins = %+v, want %+v", got, want)
	}
	if _, exists := catalog.locators["conductor@openai-curated-remote"]; exists {
		t.Fatal("ambiguous Codex plugin retained a lifecycle locator")
	}
}

// A plugin/list answer over the session's frame limit fails discovery; the
// cause must survive for the daemon log, or a growing catalog goes stale with
// nothing to show why.
func TestReadCodexPluginCatalogKeepsTheReadCause(t *testing.T) {
	t.Parallel()

	_, err := readCodexPluginCatalog(
		t.Context(),
		&codexPluginCatalogRPCFixture{listErr: bufio.ErrTooLong},
		false,
	)
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("readCodexPluginCatalog() error = %v, want it to wrap %v", err, bufio.ErrTooLong)
	}
}

// plugin/list answers the whole catalog in one frame of about 2.3 KB per
// plugin, so the frame limit must hold the item limit or the item limit is
// never reached.
func TestCodexPluginFrameLimitHoldsTheItemLimit(t *testing.T) {
	t.Parallel()

	const bytesPerPlugin = 3 << 10
	if codexPluginMessageLimit < codexPluginMaximumItems*bytesPerPlugin {
		t.Fatalf("codexPluginMessageLimit = %d, want at least %d for %d plugins",
			codexPluginMessageLimit, codexPluginMaximumItems*bytesPerPlugin, codexPluginMaximumItems)
	}
}

func remoteCodexPluginSummary(nativeID, remotePluginID string) codexPluginSummary {
	interstitial := false
	return codexPluginSummary{
		ID:                               nativeID,
		RemotePluginID:                   &remotePluginID,
		Name:                             nativeID,
		InstallPolicy:                    "AVAILABLE",
		AuthPolicy:                       "ON_INSTALL",
		Availability:                     "AVAILABLE",
		MustShowInstallationInterstitial: &interstitial,
		Source:                           codexPluginSource{Type: "remote"},
	}
}

func TestCodexPluginFromSummaryCarriesSanitizedPresentation(t *testing.T) {
	t.Parallel()

	var summary codexPluginSummary
	if err := json.Unmarshal([]byte(`{
		"id":"gmail@openai-curated-remote","remotePluginId":"plugin_gmail","name":"gmail",
		"installed":true,"enabled":true,"installPolicy":"AVAILABLE","authPolicy":"ON_USE",
		"availability":"AVAILABLE","source":{"type":"remote"},
		"shareContext":{"remotePluginId":"plugin_gmail","shareUrl":"https://chatgpt.com/plugins/gmail"},
		"interface":{
			"displayName":"Gmail","shortDescription":"Read and send email",
			"longDescription":"  Triage your inbox.\r\n\r\n\r\n\tDraft   replies\u0007 fast.\nSend later.  ",
			"defaultPrompt":[
				"  Summarize my unread email  ","","line one\nline two",
				"`+strings.Repeat("a", 129)+`","Fourth prompt","Fifth prompt"
			],
			"brandColor":"#e4A","developerName":"  Google\n LLC ",
			"websiteUrl":"https://mail.google.com/",
			"privacyPolicyUrl":"https://policies.google.com/privacy?hl=\u0085en",
			"termsOfServiceUrl":"https://user:secret@policies.google.com/terms",
			"capabilities":[],"screenshotUrls":[],"screenshots":[]
		}
	}`), &summary); err != nil {
		t.Fatal(err)
	}

	plugin, _, ok := codexPluginFromSummary(summary, "openai-curated-remote", nil, 0, nil, codexMarketplaceListing{})
	if !ok {
		t.Fatal("codexPluginFromSummary() rejected a valid plugin")
	}
	want := CodexPluginPresentation{
		LongDescription: "Triage your inbox.\n\nDraft replies fast.\nSend later.",
		DefaultPrompts:  []string{"Summarize my unread email", "line one line two", "Fourth prompt"},
		BrandColor:      "#ee44AA",
		DeveloperName:   "Google LLC",
		WebsiteURL:      "https://mail.google.com/",
	}
	if !reflect.DeepEqual(plugin.CodexPluginPresentation, want) {
		t.Fatalf("presentation = %#v, want %#v", plugin.CodexPluginPresentation, want)
	}
	if plugin.ShareURL != "https://chatgpt.com/plugins/gmail" {
		t.Fatalf("share URL = %q, want the share context link", plugin.ShareURL)
	}

	summary.Installed, summary.Enabled = false, false
	available, _, _ := codexPluginFromSummary(summary, "openai-curated-remote", nil, 0, nil, codexMarketplaceListing{})
	if available.LongDescription != "" || available.BrandColor != want.BrandColor {
		t.Fatalf("available plugin presentation = %#v, want no long description only",
			available.CodexPluginPresentation)
	}
	featured, _, _ := codexPluginFromSummary(
		summary, "openai-curated-remote", nil, 0,
		map[string]struct{}{"gmail@openai-curated-remote": {}}, codexMarketplaceListing{},
	)
	if featured.LongDescription != want.LongDescription {
		t.Fatalf("featured long description = %q, want %q", featured.LongDescription, want.LongDescription)
	}
}

func TestReadCodexPluginCatalogPrefersThePluginReadShareURL(t *testing.T) {
	t.Parallel()

	withShare := func(nativeID, remotePluginID, shareURL string) codexPluginSummary {
		summary := remoteCodexPluginSummary(nativeID, remotePluginID)
		summary.Installed, summary.Enabled = true, true
		summary.ShareContext = &codexPluginShareContext{ShareURL: &shareURL}
		return summary
	}
	marketplaces := []codexPluginMarketplace{{
		Name: "openai-curated-remote",
		Plugins: []codexPluginSummary{
			withShare(
				"alpha@openai-curated-remote",
				"plugin_alpha",
				"https://chatgpt.com/plugins/alpha-context",
			),
			withShare(
				"bravo@openai-curated-remote",
				"plugin_bravo",
				"https://chatgpt.com/plugins/bravo-context",
			),
		},
	}}
	featured := []string{}
	rawCatalog, err := json.Marshal(codexPluginListResponse{
		Marketplaces: &marketplaces, FeaturedPluginIDs: &featured,
	})
	if err != nil {
		t.Fatal(err)
	}
	read := func(nativeID, shareURL string) json.RawMessage {
		return json.RawMessage(`{"plugin":{"summary":{"id":"` + nativeID + `"},` +
			`"shareUrl":"` + shareURL + `","apps":[],"mcpServers":[],"skills":[]}}`)
	}

	catalog, err := readCodexPluginCatalog(t.Context(), &codexPluginCatalogRPCFixture{
		catalog: rawCatalog,
		reads: map[string]json.RawMessage{
			"plugin_alpha": read("alpha@openai-curated-remote", "https://chatgpt.com/plugins/alpha"),
			"plugin_bravo": read("bravo@openai-curated-remote", "https://example.com/plugins/bravo"),
		},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{catalog.plugins[0].ShareURL, catalog.plugins[1].ShareURL}
	want := []string{
		"https://chatgpt.com/plugins/alpha",
		"https://chatgpt.com/plugins/bravo-context",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("share URLs = %q, want %q", got, want)
	}
}

func TestSanitizeCodexPluginParagraphs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{name: "carriage returns", value: "one\r\ntwo\rthree", limit: 4000, want: "one\ntwo\nthree"},
		{name: "blank runs", value: "\n\n  one  \n \n\n\n\t\n two \n", limit: 4000, want: "one\n\ntwo"},
		{name: "control characters", value: "a\u0000b\u007fc\u0085d", limit: 4000, want: "a b c d"},
		{name: "rune bound", value: strings.Repeat("é", 4001), limit: 4000, want: strings.Repeat("é", 4000)},
		{name: "cut before whitespace", value: "abc\n\ndef", limit: 4, want: "abc"},
		{name: "blank", value: " \r\n\t ", limit: 4000, want: ""},
	}
	for _, test := range tests {
		if got := sanitizeCodexPluginParagraphs(test.value, test.limit); got != test.want {
			t.Errorf("%s: sanitizeCodexPluginParagraphs() = %q, want %q", test.name, got, test.want)
		}
	}
}

// A description cut at its byte bound never keeps a trailing space: the
// server trims text before recomputing the manifest digest, so a trailing
// space made it refuse the whole catalog.
func TestSanitizeCodexConnectorText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{name: "whitespace runs", value: "  one\n\ttwo  ", limit: 100, want: "one two"},
		{name: "cut after a space", value: "abc def", limit: 4, want: "abc"},
		{name: "cut inside a word", value: "abc def", limit: 6, want: "abc de"},
		{name: "byte bound", value: strings.Repeat("é", 3), limit: 5, want: "éé"},
		{name: "invalid UTF-8", value: "a\xffb", limit: 100, want: ""},
	}
	for _, test := range tests {
		if got := sanitizeCodexConnectorText(test.value, test.limit); got != test.want {
			t.Errorf("%s: sanitizeCodexConnectorText() = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestNormalizedCodexPluginBrandColor(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ value, want string }{
		{"#0B84F3", "#0B84F3"},
		{" #0b84f3 ", "#0b84f3"},
		{"#aBc", "#aaBBcc"},
		{"0B84F3", ""},
		{"#0B84F", ""},
		{"#GGGGGG", ""},
		{"#0B84F3FF", ""},
		{"#éab", ""},
		{"", ""},
	} {
		if got := normalizedCodexPluginBrandColor(test.value); got != test.want {
			t.Errorf("normalizedCodexPluginBrandColor(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}

// Every chatgpt.com image answers 403 outside a signed-in ChatGPT session;
// files.openai.com and other hosts are loadable (measured 2026-09-25).
func TestSessionOnlyCodexIconURL(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]bool{
		"https://chatgpt.com/images/ecosystem/apps/gmail/icon.png":          true,
		"https://chatgpt.com/backend-api/estuary/content?id=file_1&sig=abc": true,
		"https://ChatGPT.com/images/ecosystem/apps/github/icon.png":         true,
		"https://files.openai.com/content?id=file_1&sig=abc":                false,
		"https://www.chatgpt.com/icon.png":                                  false,
		"https://chatgpt.com.cdn.test/icon.png":                             false,
		"https://cdn.test/chatgpt.com/icon.png":                             false,
		"https://chatgpt.com\x7f/broken":                                    false,
		"":                                                                  false,
	} {
		if got := sessionOnlyCodexIconURL(value); got != want {
			t.Errorf("sessionOnlyCodexIconURL(%q) = %v, want %v", value, got, want)
		}
	}
}

// A plugin's logo is the first valid of logoUrl, logoUrlDark and
// composerIconUrl that loads outside a ChatGPT session; a session-only one is
// kept only when no other is valid.
func TestCodexPluginIconURLPrefersALoadableLogo(t *testing.T) {
	t.Parallel()

	const (
		estuary  = "https://chatgpt.com/backend-api/estuary/content?id=file_1&sig=abc"
		appPath  = "/images/ecosystem/apps/gmail/icon.png"
		appURL   = "https://chatgpt.com" + appPath
		files    = "https://files.openai.com/content?id=file_2&sig=def"
		composer = "https://cdn.test/composer.png"
	)
	text := func(value string) *string { return &value }
	for _, test := range []struct {
		name            string
		logo, dark, cmp *string
		want            string
	}{
		{name: "loadable logo first", logo: text(files), dark: text(composer), want: files},
		{
			name: "skips a session-only logo",
			logo: text(estuary), dark: text(files), cmp: text(composer), want: files,
		},
		{
			name: "composer icon last",
			logo: text(appPath), dark: text(estuary), cmp: text(composer), want: composer,
		},
		{
			name: "invalid then loadable",
			logo: text("http://cdn.test/logo.png"), dark: text(estuary), cmp: text(files), want: files,
		},
		{name: "first session-only kept", logo: text(appPath), dark: text(estuary), want: appURL},
		{
			name: "first valid session-only kept",
			logo: text("not a url\x7f"), dark: text(estuary), cmp: text(appPath), want: estuary,
		},
		{name: "none"},
	} {
		got := codexPluginIconURL(
			&codexPluginInterface{LogoURL: test.logo, LogoURLDark: test.dark, ComposerIconURL: test.cmp},
		)
		if got != test.want {
			t.Errorf("%s: codexPluginIconURL() = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestValidatedCodexPluginPresentationURL(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ value, want string }{
		{"https://example.com/privacy", "https://example.com/privacy"},
		{" https://example.com/terms ", "https://example.com/terms"},
		{"https://example.com:8443/a?b=c", "https://example.com:8443/a?b=c"},
		{"https://example.com/a b", "https://example.com/a%20b"},
		// C1 controls: escaped in the path, kept by String in the query.
		{"https://example.com/a\u0085b", "https://example.com/a%C2%85b"},
		{"https://example.com/?ref=\u0085x", ""},
		{"https://example.com/privacy?x=\u009f", ""},
		{"http://example.com/", ""},
		{"https://user:secret@example.com/", ""},
		{"https://example.com/#section", ""},
		{"https://example.com/#", ""},
		{"https:///path", ""},
		{"//example.com/", ""},
		{"mailto:privacy@example.com", ""},
		{"https://example.com/" + strings.Repeat("a", 2048), ""},
	} {
		if got := validatedCodexPluginPresentationURL(test.value); got != test.want {
			t.Errorf("validatedCodexPluginPresentationURL(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestCodexPluginDetailFromReadKeepsOnlyValidComponents(t *testing.T) {
	t.Parallel()

	var response codexPluginReadResponse
	if err := json.Unmarshal([]byte(`{"plugin":{
		"summary":{
			"id":"gmail@openai-curated-remote","name":"gmail","installed":false,"enabled":false,
			"installPolicy":"AVAILABLE","authPolicy":"ON_USE","source":{"type":"remote"},
			"shareContext":{"remotePluginId":"plugin_gmail","shareUrl":"https://chatgpt.com/plugins/gmail"},
			"interface":{
				"displayName":"Gmail","longDescription":"Line one.\n\n\nLine two.",
				"defaultPrompt":["Summarize my inbox"],"brandColor":"#EA4335",
				"developerName":"Google","websiteUrl":"https://mail.google.com/",
				"privacyPolicyUrl":"https://policies.google.com/privacy",
				"termsOfServiceUrl":"https://policies.google.com/terms",
				"capabilities":[],"screenshotUrls":[],"screenshots":[]
			}
		},
		"marketplaceName":"openai-curated-remote","shareUrl":null,
		"apps":[
			{"id":"connector_gmail","name":"Gmail","description":"Email\nclient","category":"Productivity"},
			{"id":"connector_gmail","name":"Duplicate"},
			{"id":"bad id","name":"Bad"},
			{"id":"connector_drive","name":"  "}
		],
		"skills":[
			{"name":"triage","description":"Long","enabled":true,"shortDescription":"Sort mail",
			 "interface":{"displayName":"Triage inbox"}},
			{"name":"digest","description":"Daily digest","enabled":true},
			{"name":"digest","description":"Duplicate","enabled":true},
			{"name":" ","description":"Nameless","enabled":true}
		],
		"mcpServers":["gmail-mcp","gmail-mcp","bad server"],
		"appTemplates":[],"hooks":[]
	}}`), &response); err != nil {
		t.Fatal(err)
	}

	detail, ok := codexPluginDetailFromRead(response, "gmail@openai-curated-remote")
	if !ok {
		t.Fatal("codexPluginDetailFromRead() rejected a valid read")
	}
	want := CodexPluginDetail{
		NativeID:   "gmail@openai-curated-remote",
		Name:       "Gmail",
		ShareURL:   "https://chatgpt.com/plugins/gmail",
		AuthPolicy: "on_use",
		// Codex shows the connect dialog unless the plugin says false.
		InstallationInterstitial: true,
		CodexPluginPresentation: CodexPluginPresentation{
			LongDescription:   "Line one.\n\nLine two.",
			DefaultPrompts:    []string{"Summarize my inbox"},
			BrandColor:        "#EA4335",
			DeveloperName:     "Google",
			WebsiteURL:        "https://mail.google.com/",
			PrivacyPolicyURL:  "https://policies.google.com/privacy",
			TermsOfServiceURL: "https://policies.google.com/terms",
		},
		Apps: []CodexPluginApp{{
			ID: "connector_gmail", Name: "Gmail", Description: "Email client", Category: "Productivity",
		}},
		Skills: []CodexPluginSkill{
			{Name: "triage", DisplayName: "Triage inbox", Description: "Sort mail"},
			{Name: "digest", Description: "Daily digest"},
		},
		MCPServers: []string{"gmail-mcp"},
	}
	if !reflect.DeepEqual(detail, want) {
		t.Fatalf("detail = %#v, want %#v", detail, want)
	}
	if _, ok := codexPluginDetailFromRead(response, "other@openai-curated-remote"); ok {
		t.Fatal("codexPluginDetailFromRead() accepted a read of another plugin")
	}
}

// The install policy decides how the desktop installs: an on_install plugin
// connects its app first, in the browser at once when Codex waives the
// connect dialog.
func TestCodexPluginDetailFromReadCarriesTheInstallPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		summary          string
		wantPolicy       string
		wantInterstitial bool
	}{
		{
			name:       "on install without the dialog",
			summary:    `"authPolicy":"ON_INSTALL","mustShowInstallationInterstitial":false`,
			wantPolicy: "on_install",
		},
		{
			name:       "on install with the dialog",
			summary:    `"authPolicy":"ON_INSTALL","mustShowInstallationInterstitial":true`,
			wantPolicy: "on_install", wantInterstitial: true,
		},
		{
			name:       "unknown policy",
			summary:    `"authPolicy":"LATER","mustShowInstallationInterstitial":null`,
			wantPolicy: "", wantInterstitial: true,
		},
	}
	for _, test := range tests {
		var response codexPluginReadResponse
		if err := json.Unmarshal([]byte(`{"plugin":{"summary":{
			"id":"slack@openai-curated-remote","name":"slack",`+test.summary+`,
			"source":{"type":"remote"}
		},"apps":[],"skills":[],"mcpServers":[]}}`), &response); err != nil {
			t.Fatal(err)
		}
		detail, ok := codexPluginDetailFromRead(response, "slack@openai-curated-remote")
		if !ok {
			t.Fatalf("%s: codexPluginDetailFromRead() rejected a valid read", test.name)
		}
		if detail.AuthPolicy != test.wantPolicy ||
			detail.InstallationInterstitial != test.wantInterstitial {
			t.Errorf("%s: policy = %q, interstitial = %v, want %q, %v", test.name,
				detail.AuthPolicy, detail.InstallationInterstitial,
				test.wantPolicy, test.wantInterstitial)
		}
	}
}

func TestCodexPluginBackendReadDetailUsesTheDiscoveredLocator(t *testing.T) {
	t.Parallel()

	const nativeID = "gmail@openai-curated-remote"
	locator := codexPluginLocator{
		nativeID: nativeID, pluginName: "gmail", marketplaceName: "openai-curated-remote",
		remotePluginID: "plugin_gmail", action: CodexPluginActionInstall,
	}
	transport := &codexPluginTransportStub{
		catalog: codexPluginCatalog{locators: map[string]codexPluginLocator{nativeID: locator}},
		detail:  CodexPluginDetail{NativeID: nativeID, Name: "Gmail"},
	}
	backend := newCodexPluginBackend("codex", transport)
	backend.cached = &transport.catalog

	detail, err := backend.ReadDetail(t.Context(), nativeID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Name != "Gmail" || transport.readLocator != locator {
		t.Fatalf("ReadDetail() = %+v via %+v, want the discovered locator", detail, transport.readLocator)
	}
	_, err = backend.ReadDetail(t.Context(), "unknown@openai-curated-remote")
	if !errors.Is(err, ErrCodexPluginNotFound) {
		t.Fatalf("ReadDetail(unknown) error = %v, want ErrCodexPluginNotFound", err)
	}
}

// heldCodexPluginTransport holds its first unforced read until release
// closes and answers it with stale; every other read answers the stub's
// catalog.
type heldCodexPluginTransport struct {
	codexPluginTransportStub
	stale   codexPluginCatalog
	held    atomic.Bool
	started chan struct{}
	release chan struct{}
}

func (s *heldCodexPluginTransport) Read(
	ctx context.Context,
	path string,
	force bool,
) (codexPluginCatalog, error) {
	if !force && s.held.CompareAndSwap(false, true) {
		close(s.started)
		<-s.release
		return s.stale, nil
	}
	return s.codexPluginTransportStub.Read(ctx, path, force)
}

// ReadDetail reads the catalog without operationMu when none is cached. A
// read that an install's invalidation or the Discover after it overtook must
// answer from its own catalog without restoring it, or uninstalling the
// plugin just installed is refused.
func TestCodexPluginBackendReadDetailKeepsANewerCatalog(t *testing.T) {
	t.Parallel()

	const nativeID = "gmail@openai-curated-remote"
	installable := codexPluginLocator{
		nativeID: nativeID, pluginName: "gmail", marketplaceName: "openai-curated-remote",
		remotePluginID: "plugin_gmail", action: CodexPluginActionInstall,
	}
	installed := installable
	installed.action = CodexPluginActionUninstall
	for name, overtake := range map[string]func(*CodexPluginBackend) error{
		"a newer discovery": func(backend *CodexPluginBackend) error {
			_, err := backend.Discover(t.Context(), true)
			return err
		},
		"an invalidation": func(backend *CodexPluginBackend) error {
			backend.invalidate()
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			transport := &heldCodexPluginTransport{
				codexPluginTransportStub: codexPluginTransportStub{
					catalog: codexPluginCatalog{
						locators: map[string]codexPluginLocator{nativeID: installed},
					},
					detail: CodexPluginDetail{NativeID: nativeID, Name: "Gmail"},
				},
				stale: codexPluginCatalog{
					locators: map[string]codexPluginLocator{nativeID: installable},
				},
				started: make(chan struct{}),
				release: make(chan struct{}),
			}
			backend := newCodexPluginBackend("codex", transport)
			detailErr := make(chan error, 1)
			go func() {
				_, err := backend.ReadDetail(t.Context(), nativeID)
				detailErr <- err
			}()
			<-transport.started
			if err := overtake(backend); err != nil {
				t.Fatal(err)
			}
			close(transport.release)
			if err := <-detailErr; err != nil {
				t.Fatalf("ReadDetail() error = %v", err)
			}
			if transport.readLocator != installable {
				t.Fatalf(
					"ReadDetail() read via %+v, want its own catalog's locator",
					transport.readLocator,
				)
			}
			if err := backend.Uninstall(t.Context(), nativeID); err != nil {
				t.Fatalf("Uninstall() after an overtaken detail read error = %v", err)
			}
		})
	}
}

func TestReadCodexPluginIconServesSVG(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	svg := "\ufeff<?xml version=\"1.0\"?>\n<!-- logo -->\n" +
		`<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">` +
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><rect width="1" height="1"/></svg>`
	if err := os.WriteFile(filepath.Join(root, "logo.svg"), []byte(svg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "page.svg"), []byte("<html><body/></html>"), 0o600); err != nil {
		t.Fatal(err)
	}

	icon, err := readCodexPluginIcon(t.Context(), codexPluginIconAsset{
		rootDirectory: root, relativePath: "logo.svg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if icon.ContentType != "image/svg+xml" || string(icon.Bytes) != svg {
		t.Fatalf("icon = %q (%d bytes), want the SVG document", icon.ContentType, len(icon.Bytes))
	}
	if _, err := readCodexPluginIcon(t.Context(), codexPluginIconAsset{
		rootDirectory: root, relativePath: "page.svg",
	}); !errors.Is(err, ErrCodexPluginIconUnavailable) {
		t.Fatalf("HTML icon error = %v, want ErrCodexPluginIconUnavailable", err)
	}
}

// A local marketplace ships full-size app icons: Codex Record & Replay's
// assets/app-icon.png is a 1024x1024 PNG of 1,167,902 bytes. Up to 4 MiB is
// served; anything larger is refused.
func TestReadCodexPluginIconAcceptsFullSizeAppIcons(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	png := func(size int) []byte {
		content := make([]byte, size)
		copy(content, "\x89PNG\r\n\x1a\n")
		return content
	}
	for name, content := range map[string][]byte{
		"app-icon.png": png(1_167_902),
		"at-cap.png":   png(codexPluginMaximumIconBytes),
		"oversize.png": png(codexPluginMaximumIconBytes + 1),
	} {
		if err := os.WriteFile(filepath.Join(root, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for name, size := range map[string]int{"app-icon.png": 1_167_902, "at-cap.png": 4 << 20} {
		icon, err := readCodexPluginIcon(t.Context(), codexPluginIconAsset{
			rootDirectory: root, relativePath: name,
		})
		if err != nil || icon.ContentType != "image/png" || len(icon.Bytes) != size {
			t.Fatalf("%s = %q (%d bytes), %v; want the %d-byte PNG",
				name, icon.ContentType, len(icon.Bytes), err, size)
		}
	}
	if _, err := readCodexPluginIcon(t.Context(), codexPluginIconAsset{
		rootDirectory: root, relativePath: "oversize.png",
	}); !errors.Is(err, ErrCodexPluginIconUnavailable) {
		t.Fatalf("oversize icon error = %v, want ErrCodexPluginIconUnavailable", err)
	}
}

func TestValidPluginSVGIcon(t *testing.T) {
	t.Parallel()

	for content, want := range map[string]bool{
		`<svg xmlns="http://www.w3.org/2000/svg"/>`: true,
		"  \n<svg></svg>": true,
		`<?xml version="1.0"?><svg:svg xmlns:svg="http://www.w3.org/2000/svg"/>`: true,
		`<html><svg/></html>`:   false,
		`text<svg/>`:            false,
		`<svgx/>`:               false,
		`<?xml version="1.0"?>`: false,
		"\x89PNG\r\n\x1a\n":     false,
		"<svg>\xff</svg>":       false,
	} {
		if got := validPluginSVGIcon([]byte(content)); got != want {
			t.Errorf("validPluginSVGIcon(%q) = %t, want %t", content, got, want)
		}
	}
}

func TestCodexRPCSessionRequestAllMatchesOutOfOrderResponses(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	session := &codexRPCSession{stdin: writer, messages: make(chan []byte)}
	go func() {
		decoder := json.NewDecoder(reader)
		ids := make([]int, 0, 3)
		for range 3 {
			var request struct {
				ID int `json:"id"`
			}
			if err := decoder.Decode(&request); err != nil {
				t.Error(err)
				return
			}
			ids = append(ids, request.ID)
		}
		// Answer in reverse, interleaving a notification and an error.
		for _, message := range []string{
			fmt.Sprintf(`{"id":%d,"result":{"n":3}}`, ids[2]),
			`{"method":"mcpServer/startupStatus/updated","params":{}}`,
			fmt.Sprintf(`{"id":%d,"error":{"code":-32600,"message":"bad"}}`, ids[1]),
			fmt.Sprintf(`{"id":%d,"result":{"n":1}}`, ids[0]),
		} {
			session.messages <- []byte(message)
		}
	}()

	results, errs := session.requestAll(t.Context(), "plugin/read", []any{1, 2, 3})
	if errs[0] != nil || string(results[0]) != `{"n":1}` {
		t.Fatalf("first = %s, %v", results[0], errs[0])
	}
	if errs[1] == nil {
		t.Fatal("second error was not reported")
	}
	if errs[2] != nil || string(results[2]) != `{"n":3}` {
		t.Fatalf("third = %s, %v", results[2], errs[2])
	}
}
