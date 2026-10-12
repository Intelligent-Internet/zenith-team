package agent

import (
	"testing"

	"github.com/google/uuid"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// Every chatgpt.com image answers 403 outside a signed-in ChatGPT session;
// files.openai.com and other hosts are loadable (measured 2026-09-25).
func TestSessionOnlyLogo(t *testing.T) {
	t.Parallel()

	for logo, want := range map[string]bool{
		"https://chatgpt.com/images/ecosystem/apps/gmail/icon.png":          true,
		"https://chatgpt.com/backend-api/estuary/content?id=file_1&sig=abc": true,
		"https://ChatGPT.com/images/ecosystem/apps/github/icon.png":         true,
		"https://files.openai.com/content?id=file_1&sig=abc":                false,
		"https://cdn.test/gmail.png":                                        false,
		"https://chatgpt.com.cdn.test/icon.png":                             false,
		"https://cdn.test/chatgpt.com/icon.png":                             false,
		"https://lh3.googleusercontent.com/gmail.png":                       false,
		"":                               false,
		"https://chatgpt.com\x7f/broken": false,
		"https://www.chatgpt.com/images/ecosystem/apps/slack/icon.png":        false,
		"https://files.openai.com/content?redirect=https://chatgpt.com/x.png": false,
	} {
		if got := sessionOnlyLogo(logo); got != want {
			t.Errorf("sessionOnlyLogo(%q) = %v, want %v", logo, got, want)
		}
	}
}

// The first loadable candidate wins; with none, the first present one stays.
func TestPreferLoadableLogo(t *testing.T) {
	t.Parallel()

	text := func(value string) *string { return &value }
	const sessionOnly = "https://chatgpt.com/images/ecosystem/apps/gmail/icon.png"
	tests := []struct {
		name       string
		candidates []*string
		want       *string
	}{
		{name: "none"},
		{name: "empty and missing", candidates: []*string{nil, text("")}},
		{
			name:       "own loadable",
			candidates: []*string{text("https://cdn.test/a.png"), text("https://cdn.test/b.png")},
			want:       text("https://cdn.test/a.png"),
		},
		{
			name:       "skips session-only",
			candidates: []*string{text(sessionOnly), nil, text("https://cdn.test/b.png")},
			want:       text("https://cdn.test/b.png"),
		},
		{
			name:       "keeps the first session-only",
			candidates: []*string{nil, text(sessionOnly), text("https://chatgpt.com/other.png")},
			want:       text(sessionOnly),
		},
	}
	for _, test := range tests {
		got := preferLoadableLogo(test.candidates...)
		if (got == nil) != (test.want == nil) || got != nil && *got != *test.want {
			t.Errorf("%s: preferLoadableLogo() = %v, want %v", test.name, got, test.want)
		}
	}
}

// A Codex app without a loadable logo shows an installed Plugin's that
// bundles it (one named like the app first, then the one bundling the fewest
// apps), then one borrowed by its name, else keeps its own. Other sources
// never take a bundling Plugin's logo.
func TestAppLogoFallbackOrder(t *testing.T) {
	t.Parallel()

	text := func(value string) *string { return &value }
	const sessionOnly = "https://chatgpt.com/images/ecosystem/apps/gmail/icon.png"
	fallbacks := appLogoFallbacks{
		bundles: []pluginLogoBundle{
			{
				name: "Google Workspace",
				logo: "https://cdn.test/workspace.png",
				apps: []string{"gmail", "calendar", "drive"},
			},
			{
				name: "Mail Tools",
				logo: "https://cdn.test/mail-tools.png",
				apps: []string{"gmail", "outlook"},
			},
			{name: "gmail", logo: "https://cdn.test/gmail-plugin.png", apps: []string{"gmail", "contacts"}},
			{name: "Drive Only", logo: "https://cdn.test/drive.png", apps: []string{"drive"}},
			{
				name: "Calendar Tools",
				logo: "https://cdn.test/calendar-tools.png",
				apps: []string{"calendar", "tasks", "drive"},
			},
		},
		byName: namedLogos(map[string]string{
			"Gmail":   "https://cdn.test/gmail-by-name.png",
			"Outlook": "https://cdn.test/outlook-by-name.png",
		}),
	}
	tests := []struct {
		name     string
		source   ConnectorSource
		nativeID string
		appName  string
		own      *string
		want     *string
	}{
		{
			name:     "own loadable",
			source:   ConnectorSourceCodexApp,
			nativeID: "gmail",
			appName:  "Gmail",
			own:      text("https://cdn.test/own.png"),
			want:     text("https://cdn.test/own.png"),
		},
		{
			name:     "plugin named like the app",
			source:   ConnectorSourceCodexApp,
			nativeID: "gmail",
			appName:  "Gmail",
			own:      text(sessionOnly),
			want:     text("https://cdn.test/gmail-plugin.png"),
		},
		{
			name:     "plugin bundling the fewest apps",
			source:   ConnectorSourceCodexApp,
			nativeID: "drive",
			appName:  "Google Drive",
			own:      text(sessionOnly),
			want:     text("https://cdn.test/drive.png"),
		},
		{
			name:     "first of equal bundles",
			source:   ConnectorSourceCodexApp,
			nativeID: "calendar",
			appName:  "Google Calendar",
			want:     text("https://cdn.test/workspace.png"),
		},
		{
			name:     "bundle before name",
			source:   ConnectorSourceCodexApp,
			nativeID: "outlook",
			appName:  "Outlook",
			own:      text(sessionOnly),
			want:     text("https://cdn.test/mail-tools.png"),
		},
		{
			name:     "borrowed by name",
			source:   ConnectorSourceCodexApp,
			nativeID: "unbundled",
			appName:  "Gmail",
			own:      text(sessionOnly),
			want:     text("https://cdn.test/gmail-by-name.png"),
		},
		{
			name:     "keeps its own",
			source:   ConnectorSourceCodexApp,
			nativeID: "unbundled",
			appName:  "Teams",
			own:      text(sessionOnly),
			want:     text(sessionOnly),
		},
		{name: "none", source: ConnectorSourceCodexApp, nativeID: "unbundled", appName: "Teams"},
		{
			name:     "a Claude app borrows only by name",
			source:   ConnectorSourceClaudePlugin,
			nativeID: "gmail",
			appName:  "Gmail",
			want:     text("https://cdn.test/gmail-by-name.png"),
		},
	}
	for _, test := range tests {
		got := fallbacks.appLogo(test.source, test.nativeID, test.appName, test.own)
		if (got == nil) != (test.want == nil) || got != nil && *got != *test.want {
			t.Errorf("%s: appLogo() = %v, want %v", test.name, got, test.want)
		}
	}
}

// Only Codex apps without a loadable logo, and Claude sources without any,
// look for a stand-in.
func TestLogoNeeds(t *testing.T) {
	t.Parallel()

	text := func(value string) *string { return &value }
	const sessionOnly = "https://chatgpt.com/images/ecosystem/apps/gmail/icon.png"
	for _, test := range []struct {
		source ConnectorSource
		own    *string
		want   bool
	}{
		{source: ConnectorSourceCodexApp, want: true},
		{source: ConnectorSourceCodexApp, own: text(sessionOnly), want: true},
		{source: ConnectorSourceCodexApp, own: text("https://cdn.test/own.png")},
		{source: ConnectorSourceClaudePlugin, want: true},
		{source: ConnectorSourceClaudePlugin, own: text(sessionOnly)},
		{source: ConnectorSourceCodexMCP},
		{source: ConnectorSourceCodexMCP, own: text(sessionOnly)},
	} {
		if got := pluginAppNeedsLogo(
			PluginDetailApp{Source: test.source, IconURL: test.own},
		); got != test.want {
			t.Errorf("pluginAppNeedsLogo(%s, %v) = %v, want %v", test.source, test.own, got, test.want)
		}
		connectorWant := test.want && test.source == ConnectorSourceCodexApp
		if got := connectorNeedsLogo(
			ConnectorItemV1{Source: test.source, IconURL: test.own},
		); got != connectorWant {
			t.Errorf("connectorNeedsLogo(%s, %v) = %v, want %v", test.source, test.own, got, connectorWant)
		}
	}
}

// A website host or developer lends a logo only when exactly one Codex Plugin
// with a loadable logo shares it, and then that Plugin's newest loadable one,
// to a Plugin whose name agrees with the lender's; a session-only logo neither
// lends nor makes a match ambiguous. A Plugin takes its website's logo before
// its developer's, and its developer's when the website's lender is another
// product.
func TestLendPublisherLogos(t *testing.T) {
	t.Parallel()

	const (
		canvaSite   = "canva.com"
		openAISite  = "openai.com"
		googleCloud = "cloud.google.com"
		sessionOnly = "https://chatgpt.com/backend-api/estuary/content?id=file_x&sig=a"
	)
	canva, sales, data := uuid.New(), uuid.New(), uuid.New()
	figmaApp, figmaSlides, stripe := uuid.New(), uuid.New(), uuid.New()
	bigQuery, alloyDB := uuid.New(), uuid.New()
	row := func(
		byWebsite bool, requested string, plugin uuid.UUID, name, logo string,
	) generated.ListCompanyCodexPluginLogosByPublisherRow {
		return generated.ListCompanyCodexPluginLogosByPublisherRow{
			ByWebsite: byWebsite, Requested: requested, PluginID: plugin, Name: name, IconUrl: logo,
		}
	}
	logos := lendPublisherLogos([]generated.ListCompanyCodexPluginLogosByPublisherRow{
		// One Plugin on two computers: the newest loadable logo comes first.
		row(true, canvaSite, canva, "Canva", "https://cdn.test/canva-new.png"),
		row(true, canvaSite, canva, "Canva", "https://cdn.test/canva-old.png"),
		// Two Plugins share a host: ambiguous.
		row(true, openAISite, sales, "Sales", "https://cdn.test/sales.png"),
		row(true, openAISite, data, "Data", "https://cdn.test/data.png"),
		// A session-only logo is skipped, leaving one lender.
		row(false, "Figma", figmaApp, "Figma", sessionOnly),
		row(false, "Figma", figmaSlides, "Figma Slides", "https://cdn.test/figma-by-developer.png"),
		// Only a session-only logo: nothing to lend.
		row(false, "Stripe", stripe, "Stripe", sessionOnly),
		// The same developer name reported for two Plugins.
		row(false, "OpenAI", sales, "Sales", "https://cdn.test/sales.png"),
		row(false, "OpenAI", data, "Data", "https://cdn.test/data.png"),
		row(false, "Canva", canva, "Canva", "https://cdn.test/canva-dev.png"),
		// One publisher's host with one lender, and a developer of another product.
		row(true, googleCloud, bigQuery, "BigQuery", "https://cdn.test/bigquery.png"),
		row(false, "Google LLC", bigQuery, "BigQuery", "https://cdn.test/bigquery.png"),
		row(false, "AlloyDB Team", alloyDB, "AlloyDB", "https://cdn.test/alloydb.png"),
	})
	text := func(value string) *string { return &value }
	for _, test := range []struct {
		name      string
		borrower  string
		publisher pluginPublisher
		want      *string
	}{
		{name: "nothing reported", borrower: "Canva"},
		{
			name: "website first", borrower: "canva-design",
			publisher: pluginPublisher{websiteHost: text(canvaSite), developerName: text("Canva")},
			want:      text("https://cdn.test/canva-new.png"),
		},
		{
			name: "developer after an ambiguous website", borrower: "Canva Docs",
			publisher: pluginPublisher{websiteHost: text(openAISite), developerName: text("Canva")},
			want:      text("https://cdn.test/canva-dev.png"),
		},
		{
			name: "ambiguous website and developer", borrower: "Sales",
			publisher: pluginPublisher{websiteHost: text(openAISite), developerName: text("OpenAI")},
		},
		{
			name: "session-only lender skipped", borrower: "Figma Slides",
			publisher: pluginPublisher{developerName: text("Figma")},
			want:      text("https://cdn.test/figma-by-developer.png"),
		},
		{name: "only session-only", borrower: "Stripe", publisher: pluginPublisher{developerName: text("Stripe")}},
		{
			name: "a host is not a developer", borrower: "Canva",
			publisher: pluginPublisher{developerName: text(canvaSite)},
		},
		{
			name: "another product of the publisher", borrower: "AlloyDB",
			publisher: pluginPublisher{websiteHost: text(googleCloud), developerName: text("Google LLC")},
		},
		{
			name: "developer after another product's website", borrower: "AlloyDB Omni",
			publisher: pluginPublisher{websiteHost: text(googleCloud), developerName: text("AlloyDB Team")},
			want:      text("https://cdn.test/alloydb.png"),
		},
		{
			name: "same product on the publisher's website", borrower: "BigQuery Toolbox",
			publisher: pluginPublisher{websiteHost: text(googleCloud)},
			want:      text("https://cdn.test/bigquery.png"),
		},
	} {
		got := logos.lend(test.borrower, test.publisher)
		if (got == nil) != (test.want == nil) || got != nil && *got != *test.want {
			t.Errorf("%s: lend() = %v, want %v", test.name, got, test.want)
		}
	}
	if got := (publisherLogos{}).lend("Canva", pluginPublisher{websiteHost: text(canvaSite)}); got != nil {
		t.Errorf("empty lend() = %v, want nil", *got)
	}
}

// Two names agree when one contains the other, ignoring case, spaces and
// punctuation, or the first word of one is a word of the other.
func TestPluginNamesAgree(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		left, right string
		want        bool
	}{
		{"Sentry CLI", "Sentry", true},
		{"Vibe Prospecting", "AI Vibe Prospecting", true},
		{"monday CRM", "monday.com", true},
		{"canva-design", "Canva", true},
		{"Learn with Coursera", "Coursera Learning", true},
		{"Migration to AWS", "AWS Data Analytics", true},
		{"AlloyDB", "BigQuery", false},
		{"Cloud SQL PostgreSQL", "BigQuery", false},
		{"TeamCity CLI", "YouTrack", false},
		{"Azure SQL Developer", "Microsoft Dataverse", false},
		{"AI Plugins", "Endor Labs Agent Kit", false},
		{"+++", "+++", false},
		{"", "Canva", false},
	} {
		if got := pluginNamesAgree(test.left, test.right); got != test.want {
			t.Errorf("pluginNamesAgree(%q, %q) = %v, want %v", test.left, test.right, got, test.want)
		}
		if got := pluginNamesAgree(test.right, test.left); got != test.want {
			t.Errorf("pluginNamesAgree(%q, %q) = %v, want %v", test.right, test.left, got, test.want)
		}
	}
}

// namedLogos lends each name one logo with no developer, as a Codex app does.
func namedLogos(logos map[string]string) map[string][]nameLender {
	lenders := make(map[string][]nameLender, len(logos))
	for name, logo := range logos {
		lenders[name] = []nameLender{{logo: logo}}
	}
	return lenders
}

// A Plugin borrows by name only from a lender whose developer does not
// contradict its own: "data" by Astronomer skips "Data" by OpenAI and takes the
// next lender, "notion" by Notion Labs takes Notion's, and a Plugin or lender
// with no developer is never contradicted.
func TestNamedForSkipsALenderFromAnotherDeveloper(t *testing.T) {
	t.Parallel()
	developer := func(name string) pluginPublisher { return pluginPublisher{developerName: &name} }
	fallbacks := appLogoFallbacks{byName: map[string][]nameLender{
		"data": {
			{logo: "https://cdn.test/openai-data.png", developer: "OpenAI"},
			{logo: "https://cdn.test/astronomer-data.png", developer: "Astronomer, Inc."},
		},
		"notion": {{logo: "https://cdn.test/notion.png", developer: "Notion"}},
		"miro":   {{logo: "https://cdn.test/miro-app.png"}},
	}}
	tests := []struct {
		name      string
		borrower  string
		publisher pluginPublisher
		want      string
	}{
		{name: "another developer is skipped", borrower: "data", publisher: developer("Astronomer"), want: "https://cdn.test/astronomer-data.png"},
		{name: "only contradicting lenders lend nothing", borrower: "data", publisher: developer("Snowflake"), want: ""},
		{name: "a longer spelling of one developer agrees", borrower: "notion", publisher: developer("Notion Labs"), want: "https://cdn.test/notion.png"},
		{name: "a borrower without a developer takes the first lender", borrower: "data", publisher: pluginPublisher{}, want: "https://cdn.test/openai-data.png"},
		{name: "a lender without a developer never contradicts", borrower: "miro", publisher: developer("Miro"), want: "https://cdn.test/miro-app.png"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := ""
			if logo := fallbacks.namedFor(test.borrower, test.publisher); logo != nil {
				got = *logo
			}
			if got != test.want {
				t.Fatalf("namedFor(%q) = %q, want %q", test.borrower, got, test.want)
			}
		})
	}
}
