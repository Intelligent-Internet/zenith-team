package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHumanizedCodexPluginName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		slug, developer, description, want string
	}{
		{
			slug: "adobe-for-creativity", developer: "Adobe",
			description: "Harness Adobe's creative AI-powered tools to edit images.",
			want:        "Adobe for Creativity",
		},
		{
			slug: "runway-api", developer: "Runway",
			description: "Generate videos, images, and audio with Runway's API.",
			want:        "Runway API",
		},
		{
			slug: "ui-theme-designer", developer: "SAP SE",
			description: "Plugin for coding agents working with UI theme designer.",
			want:        "UI Theme Designer",
		},
		{
			slug: "hyperframes", developer: "HeyGen",
			description: "HyperFrames by HeyGen. Write HTML, render video.",
			want:        "HyperFrames",
		},
		{slug: "canva", developer: "Canva", want: "Canva"},
		{slug: "monday-com", developer: "monday.com", want: "monday.com"},
		{slug: "hubspot", developer: "HubSpot, Inc.", description: "Work with HubSpot CRM.", want: "HubSpot"},
		// A capital initial in the description is only a sentence start; the
		// acronym wins, and a word written only in lowercase is title-cased.
		{slug: "sql-api", description: "Api access. Run sql against a warehouse.", want: "SQL API"},
		{slug: "ios-macos-e2e_3d", want: "iOS macOS E2E 3D"},
		{slug: "the-state-of-the-art", want: "The State of the Art"},
		{
			slug:        "42crunch-api-security-testing",
			description: "42Crunch API audit.",
			want:        "42Crunch API Security Testing",
		},
		// Capitals the description also writes with only a capital initial
		// are emphasis or an identifier, not the name's spelling.
		{
			slug: "context7", developer: "Upstash",
			description: "Upstash Context7 MCP server. Set CONTEXT7_API_KEY for higher limits.",
			want:        "Context7",
		},
		{slug: "serp-api", description: "SERP API tools.", want: "SERP API"},
		// Capitals inside an identifier or file name are not the name's
		// spelling either.
		{
			slug: "claude-md-management", developer: "Anthropic",
			description: "Tools to maintain and improve CLAUDE.md files - audit quality.",
			want:        "Claude MD Management",
		},
		{slug: "key-vault", description: "Reads KEY_VAULT_URL and VAULT.2 settings.", want: "Key Vault"},
		{slug: "grpc-crud", description: "Speaks GRPC. Does CRUD.", want: "GRPC CRUD"},
		// A slug of several words written as one term in the description.
		{
			slug: "bigdata-com", developer: "RavenPack",
			description: "Official Bigdata.com plugin powered by Bigdata MCP.",
			want:        "Bigdata.com",
		},
		{slug: "code-review", description: "Run a code-review of the diff.", want: "Code Review"},
		// The developer's name spells a word the description does not.
		{
			slug: "stackhawk-hawkscan", developer: "StackHawk",
			description: "Run HawkScan inside Claude Code. Writes stackhawk.yml configs.",
			want:        "StackHawk HawkScan",
		},
		{slug: "codex-app-tools", want: "Codex App Tools"},
		{slug: "gen4.5", want: "Gen4.5"},
		{slug: "a--b__c", want: "A B C"},
		{slug: "---", want: "---"},
	}
	for _, test := range tests {
		got := humanizedCodexPluginName(test.slug, test.developer, test.description)
		if got != test.want {
			t.Errorf("humanizedCodexPluginName(%q, %q, %q) = %q, want %q",
				test.slug, test.developer, test.description, got, test.want)
		}
	}
	long := strings.Repeat("word-", 60) + "end"
	if got := humanizedCodexPluginName(long, "", ""); len(got) > codexConnectorMaximumName {
		t.Errorf("humanized name is %d bytes, want at most %d", len(got), codexConnectorMaximumName)
	}
}

func TestParseClaudeMarketplaceListings(t *testing.T) {
	t.Parallel()

	listings := parseClaudeMarketplaceListings([]byte(`{
		"name":"claude-plugins-official","owner":{"name":"Anthropic"},"renames":{"old":"new"},
		"plugins":[
			{"name":"canva","description":"  Create and edit\n Canva designs. ",
			 "author":{"name":"Canva","email":"x@canva.com"},"category":"design",
			 "source":{"source":"git-subdir","url":"https://github.com/canva-sdks/canva-skills.git"},
			 "homepage":"https://www.canva.com"},
			{"name":"newrelic","displayName":"New Relic","description":"Observe apps.",
			 "author":"New Relic, Inc.","homepage":"http://newrelic.com"},
			{"name":"typed","displayName":7,"description":["x"],"author":{"name":3},
			 "homepage":"https://user:secret@example.com/"},
			{"name":"twice","description":"first"},
			{"name":"twice","description":"second"},
			{"name":"twice","description":"third"},
			{"name":"bad name","description":"invalid slug"},
			{"description":"no name"},
			"not an object"
		]
	}`))
	want := map[string]codexMarketplaceListing{
		"canva": {
			Description: "Create and edit Canva designs.", DeveloperName: "Canva",
			WebsiteURL: "https://www.canva.com",
		},
		"newrelic": {
			DisplayName: "New Relic", Description: "Observe apps.",
			DeveloperName: "New Relic, Inc.",
		},
		"typed": {},
	}
	if !reflect.DeepEqual(listings, want) {
		t.Fatalf("parseClaudeMarketplaceListings() = %#v, want %#v", listings, want)
	}
	for _, content := range []string{``, `[]`, `{"plugins":{}}`, `{"plugins":[]}`, `{"plugins":[`} {
		if got := parseClaudeMarketplaceListings([]byte(content)); got != nil {
			t.Errorf("parseClaudeMarketplaceListings(%q) = %#v, want none", content, got)
		}
	}
}

func writeClaudeMarketplace(t *testing.T, root, content string) string {
	t.Helper()
	path := filepath.Join(root, ".claude-plugin", "marketplace.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadCodexMarketplaceListingsReadsOnlyAClaudeManifestInItsRoot(t *testing.T) {
	t.Parallel()

	const manifest = `{"plugins":[{"name":"canva","description":"Design."}]}`
	want := map[string]codexMarketplaceListing{"canva": {Description: "Design."}}

	root := t.TempDir()
	path := writeClaudeMarketplace(t, root, manifest)
	if got := readCodexMarketplaceListings(path); !reflect.DeepEqual(got, want) {
		t.Fatalf("readCodexMarketplaceListings(claude) = %#v, want %#v", got, want)
	}

	// The same content in a Codex-format manifest is an unknown format.
	codexPath := filepath.Join(root, ".agents", "plugins", "marketplace.json")
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	// A symbolic link may point inside the marketplace root, never out of it.
	outside := t.TempDir()
	outsidePath := filepath.Join(outside, "marketplace.json")
	if err := os.WriteFile(outsidePath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	escaping := filepath.Join(t.TempDir(), "escaping")
	if err := os.MkdirAll(filepath.Join(escaping, ".claude-plugin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(escaping, ".claude-plugin", "marketplace.json")); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(t.TempDir(), "inside")
	if err := os.MkdirAll(filepath.Join(inside, ".claude-plugin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inside, "catalog.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("..", "catalog.json"),
		filepath.Join(inside, ".claude-plugin", "marketplace.json"),
	); err != nil {
		t.Fatal(err)
	}
	if got := readCodexMarketplaceListings(
		filepath.Join(inside, ".claude-plugin", "marketplace.json"),
	); !reflect.DeepEqual(got, want) {
		t.Fatalf("readCodexMarketplaceListings(link inside root) = %#v, want %#v", got, want)
	}

	directory := filepath.Join(t.TempDir(), "directory")
	if err := os.MkdirAll(filepath.Join(directory, ".claude-plugin", "marketplace.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	oversized := writeClaudeMarketplace(t, filepath.Join(t.TempDir(), "oversized"),
		`{"plugins":[{"name":"canva","description":"`+
			strings.Repeat("a", codexPluginMaximumMarketplaceManifestBytes)+`"}]}`)

	for name, path := range map[string]string{
		"codex format":       codexPath,
		"link out of root":   filepath.Join(escaping, ".claude-plugin", "marketplace.json"),
		"directory":          filepath.Join(directory, ".claude-plugin", "marketplace.json"),
		"over 4 MiB":         oversized,
		"missing":            filepath.Join(t.TempDir(), ".claude-plugin", "marketplace.json"),
		"relative":           filepath.Join(".claude-plugin", "marketplace.json"),
		"other file name":    filepath.Join(root, ".claude-plugin", "plugin.json"),
		"empty":              "",
		"parent traversal":   root + "/.claude-plugin/../.claude-plugin/../x/marketplace.json",
		"not a manifest dir": outsidePath,
	} {
		if got := readCodexMarketplaceListings(path); got != nil {
			t.Errorf("readCodexMarketplaceListings(%s) = %#v, want none", name, got)
		}
	}
}

// claudeMarketplaceSummary is a claude-plugins-official entry as Codex's
// plugin/list reports it: no display name, description or logo.
func claudeMarketplaceSummary(t *testing.T, name, pluginInterface string) codexPluginSummary {
	t.Helper()
	var summary codexPluginSummary
	if err := json.Unmarshal([]byte(`{
		"id":"`+name+`@claude-plugins-official","name":"`+name+`",
		"installPolicy":"AVAILABLE","authPolicy":"ON_INSTALL","availability":"AVAILABLE",
		"source":{"type":"git","url":"https://github.com/example/skills.git"},
		"interface":`+pluginInterface+`
	}`), &summary); err != nil {
		t.Fatal(err)
	}
	return summary
}

func TestReadCodexPluginCatalogCompletesFromTheMarketplaceListing(t *testing.T) {
	t.Parallel()

	manifestPath := writeClaudeMarketplace(t, t.TempDir(), `{"plugins":[
		{"name":"canva","description":"Create, edit and brand-check Canva designs.",
		 "author":{"name":"Canva"},"homepage":"https://www.canva.com"},
		{"name":"hyperframes","description":"HyperFrames by HeyGen. Write HTML, render video.",
		 "author":{"name":"HeyGen"},"homepage":"https://hyperframes.heygen.com"},
		{"name":"terraform","description":"Write Terraform.","author":{"name":"HashiCorp"},
		 "homepage":"https://www.terraform.io"},
		{"name":"newrelic","displayName":"New Relic (listing)","description":"Listing text.",
		 "author":{"name":"Listing developer"},"homepage":"https://listing.example.com"}
	]}`)
	curated := remoteCodexPluginSummary("canva@openai-curated-remote", "plugin_canva")
	curatedName, curatedDescription := "Canva", "Search, create, edit designs"
	curated.Interface = &codexPluginInterface{
		DisplayName: &curatedName, ShortDescription: &curatedDescription,
	}
	marketplaces := []codexPluginMarketplace{
		{
			Name: "claude-plugins-official",
			Path: &manifestPath,
			Plugins: []codexPluginSummary{
				claudeMarketplaceSummary(
					t,
					"canva",
					`{"displayName":null,"shortDescription":null,"developerName":null,"websiteUrl":null,"category":"Design"}`,
				),
				claudeMarketplaceSummary(t, "hyperframes", `null`),
				claudeMarketplaceSummary(
					t,
					"terraform",
					`{"developerName":"HashiCorp (Codex)","websiteUrl":"https://developer.hashicorp.com"}`,
				),
				claudeMarketplaceSummary(t, "newrelic",
					`{"displayName":"New Relic","shortDescription":"Codex text."}`),
				claudeMarketplaceSummary(t, "unlisted-mcp-server", `{}`),
			},
		},
		{Name: "openai-curated-remote", Plugins: []codexPluginSummary{curated}},
	}
	featured := []string{}
	rawCatalog, err := json.Marshal(codexPluginListResponse{
		Marketplaces: &marketplaces, FeaturedPluginIDs: &featured,
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
	type row struct {
		NativeID, Name, Description, DeveloperName, WebsiteURL string
		Rank                                                   int
	}
	got := make([]row, 0, len(catalog.plugins))
	for _, plugin := range catalog.plugins {
		got = append(got, row{
			plugin.NativeID, plugin.Name, plugin.Description, plugin.DeveloperName,
			plugin.WebsiteURL, plugin.CatalogRank,
		})
	}
	want := []row{
		{"canva@openai-curated-remote", "Canva", "Search, create, edit designs", "", "", 0},
		{
			"canva@claude-plugins-official", "Canva",
			"Create, edit and brand-check Canva designs.", "Canva", "https://www.canva.com", 1,
		},
		{
			"hyperframes@claude-plugins-official", "HyperFrames",
			"HyperFrames by HeyGen. Write HTML, render video.", "HeyGen",
			"https://hyperframes.heygen.com", 2,
		},
		// Codex's own values win over the listing's.
		{
			"terraform@claude-plugins-official", "Terraform", "Write Terraform.",
			"HashiCorp (Codex)", "https://developer.hashicorp.com", 3,
		},
		{"newrelic@claude-plugins-official", "New Relic", "Codex text.", "", "", 4},
		{"unlisted-mcp-server@claude-plugins-official", "Unlisted MCP Server", "", "", "", 5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog =\n%+v\nwant\n%+v", got, want)
	}
}

// The builder reads a marketplace's manifest at most once per catalog read,
// and only for a plugin whose interface lacks a name or short description.
func TestCodexPluginCatalogBuilderReadsEachManifestOnce(t *testing.T) {
	t.Parallel()

	manifestPath := writeClaudeMarketplace(t, t.TempDir(),
		`{"plugins":[{"name":"canva","description":"Design."},{"name":"figma","description":"Frames."}]}`)
	builder := newCodexPluginCatalogBuilder(map[string]struct{}{})
	if err := builder.appendMarketplace(codexPluginMarketplace{
		Name: "claude-plugins-official", Path: &manifestPath,
		Plugins: []codexPluginSummary{claudeMarketplaceSummary(t, "canva", `null`)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	if err := builder.appendMarketplace(codexPluginMarketplace{
		Name: "claude-plugins-official-mirror", Path: &manifestPath,
		Plugins: []codexPluginSummary{claudeMarketplaceSummary(t, "figma", `null`)},
	}); err != nil {
		t.Fatal(err)
	}
	catalog := builder.build()
	if len(catalog.plugins) != 2 || catalog.plugins[1].Description != "Frames." {
		t.Fatalf("plugins = %+v, want the second read from the cached manifest", catalog.plugins)
	}

	listedPath := writeClaudeMarketplace(t, t.TempDir(),
		`{"plugins":[{"name":"canva","description":"Design.","author":{"name":"Canva"}}]}`)
	named := claudeMarketplaceSummary(t, "canva",
		`{"displayName":"Canva","shortDescription":"Codex text."}`)
	fresh := newCodexPluginCatalogBuilder(nil)
	if listing := fresh.listing(&listedPath, named); listing != (codexMarketplaceListing{}) ||
		len(fresh.listings) != 0 {
		t.Fatalf("listing() = %+v after %d manifest reads for a complete interface, want none",
			listing, len(fresh.listings))
	}
}

func TestCodexPluginCatalogRanksOpenAIMarketplacesFirst(t *testing.T) {
	t.Parallel()

	local := func(nativeID string) codexPluginSummary {
		name, _, _ := strings.Cut(nativeID, "@")
		return codexPluginSummary{
			ID: nativeID, Name: name, InstallPolicy: "AVAILABLE", AuthPolicy: "ON_USE",
			Availability: "AVAILABLE", Source: codexPluginSource{Type: "local"},
		}
	}
	path := "/nonexistent/marketplace.json"
	builder := newCodexPluginCatalogBuilder(map[string]struct{}{})
	for _, marketplace := range []codexPluginMarketplace{
		{Name: "openai-primary-runtime", Path: &path, Plugins: []codexPluginSummary{
			local("documents@openai-primary-runtime"), local("pdf@openai-primary-runtime"),
		}},
		{Name: "claude-plugins-official", Path: &path, Plugins: []codexPluginSummary{
			local("canva@claude-plugins-official"), local("asana@claude-plugins-official"),
		}},
		{Name: "openai-bundled", Path: &path, Plugins: []codexPluginSummary{
			local("browser@openai-bundled"),
		}},
		{Name: "team-marketplace", Path: &path, Plugins: []codexPluginSummary{
			local("deploy@team-marketplace"),
		}},
		{Name: "openai-curated-remote", Plugins: []codexPluginSummary{
			remoteCodexPluginSummary("gmail@openai-curated-remote", "plugin_gmail"),
		}},
	} {
		if err := builder.appendMarketplace(marketplace); err != nil {
			t.Fatal(err)
		}
	}
	catalog := builder.build()
	got := make([]string, len(catalog.plugins))
	for index, plugin := range catalog.plugins {
		if plugin.CatalogRank != index {
			t.Fatalf("%s rank = %d, want %d", plugin.NativeID, plugin.CatalogRank, index)
		}
		got[index] = plugin.NativeID
	}
	want := []string{
		"documents@openai-primary-runtime", "pdf@openai-primary-runtime",
		"browser@openai-bundled", "gmail@openai-curated-remote",
		"canva@claude-plugins-official", "asana@claude-plugins-official",
		"deploy@team-marketplace",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog order = %q, want %q", got, want)
	}
	if _, err := canonicalNativePluginItems(func() []nativePluginItem {
		items := make([]nativePluginItem, len(catalog.plugins))
		for index, plugin := range catalog.plugins {
			items[index] = nativePluginItem{NativeID: plugin.NativeID, CatalogRank: plugin.CatalogRank}
		}
		return items
	}()); err != nil {
		t.Fatalf("canonicalNativePluginItems() rejected the ranked catalog: %v", err)
	}
}

func TestCodexPluginBackendReadDetailTakesTheCatalogNameForASlug(t *testing.T) {
	t.Parallel()

	const nativeID = "canva@claude-plugins-official"
	locator := codexPluginLocator{
		nativeID: nativeID, pluginName: "canva", marketplaceName: "claude-plugins-official",
		marketplacePath: "/marketplace/.claude-plugin/marketplace.json",
		action:          CodexPluginActionInstall,
	}
	transport := &codexPluginTransportStub{
		catalog: codexPluginCatalog{
			plugins: []CodexPlugin{{
				NativeID: nativeID, Name: "Canva",
				CodexPluginPresentation: CodexPluginPresentation{
					DeveloperName: "Canva", WebsiteURL: "https://www.canva.com",
				},
			}},
			locators: map[string]codexPluginLocator{nativeID: locator},
		},
		detail: CodexPluginDetail{NativeID: nativeID, Name: "canva"},
	}
	backend := newCodexPluginBackend("codex", transport)
	backend.cached = &transport.catalog

	detail, err := backend.ReadDetail(t.Context(), nativeID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Name != "Canva" || detail.DeveloperName != "Canva" ||
		detail.WebsiteURL != "https://www.canva.com" {
		t.Fatalf("ReadDetail() = %+v, want the catalog's name, developer and website", detail)
	}

	transport.detail = CodexPluginDetail{
		NativeID: nativeID, Name: "Canva Live",
		CodexPluginPresentation: CodexPluginPresentation{DeveloperName: "Canva Pty Ltd"},
	}
	detail, err = backend.ReadDetail(t.Context(), nativeID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Name != "Canva Live" || detail.DeveloperName != "Canva Pty Ltd" {
		t.Fatalf("ReadDetail() = %+v, want Codex's live values to win", detail)
	}
}

// An unnamed plugin of OpenAI's own marketplaces is internal and keeps its
// slug, which the client reads to keep it out of the Installed strip; the same
// unnamed plugin from a third-party marketplace gets a readable name.
func TestCodexPluginFromSummaryNamesOnlyThirdPartySlugs(t *testing.T) {
	t.Parallel()
	var summary codexPluginSummary
	if err := json.Unmarshal([]byte(`{
		"id":"codex-app-tools@openai-bundled","name":"codex-app-tools",
		"installed":true,"enabled":true,"installPolicy":"AVAILABLE","authPolicy":"ON_USE",
		"availability":"AVAILABLE","source":{"type":"local","path":"/tmp/codex-app-tools"}
	}`), &summary); err != nil {
		t.Fatal(err)
	}
	internal, _, ok := codexPluginFromSummary(summary, "openai-bundled", nil, 0, nil, codexMarketplaceListing{})
	if !ok || internal.Name != "codex-app-tools" {
		t.Fatalf("OpenAI marketplace plugin name = %q (ok %v), want the slug", internal.Name, ok)
	}
	summary.ID = "codex-app-tools@claude-plugins-official"
	thirdParty, _, ok := codexPluginFromSummary(
		summary,
		"claude-plugins-official",
		nil,
		0,
		nil,
		codexMarketplaceListing{},
	)
	if !ok || thirdParty.Name != "Codex App Tools" {
		t.Fatalf("third-party plugin name = %q (ok %v), want %q", thirdParty.Name, ok, "Codex App Tools")
	}
}
