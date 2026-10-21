package agent

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"gopkg.in/yaml.v3"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// The app vocabulary is the Workflow contract's WorkflowConnectorType enum,
// in order, so a hint the catalog accepts always has a readiness answer.
func TestConnectorAppTypesMatchTheWorkflowConnectorEnum(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../../docs/api/spec/workflows.yaml")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var spec struct {
		Schemas map[string]struct {
			Enum []string `yaml:"enum"`
		} `yaml:"schemas"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	want := spec.Schemas["WorkflowConnectorType"].Enum
	got := make([]string, len(connectorAppTypes))
	for index, app := range connectorAppTypes {
		got[index] = string(app)
	}
	if len(want) == 0 || !slices.Equal(got, want) {
		t.Fatalf("connectorAppTypes = %v, spec WorkflowConnectorType = %v", got, want)
	}
	for app := range connectorAppPins {
		if !slices.Contains(connectorAppTypes, app) {
			t.Errorf("pinned app %q is not a WorkflowConnectorType", app)
		}
	}
}

// Pins are exact provider identities: every one is a source its provider
// discovers, trimmed, and claimed by exactly one app, so no display-name
// lookalike and no second app can resolve through it.
func TestConnectorAppPinsAreValidExactAndUnique(t *testing.T) {
	t.Parallel()

	claimedIdentities := make(map[string]ConnectorAppType)
	claimedPlugins := make(map[string]ConnectorAppType)
	for app, byProvider := range connectorAppPins {
		for provider, pin := range byProvider {
			if !validConnectorProvider(provider) {
				t.Errorf("%s pins unknown provider %q", app, provider)
			}
			if len(pin.Connectors) == 0 && len(pin.Plugins) == 0 {
				t.Errorf("%s/%s is an empty pin; omit the provider instead", app, provider)
			}
			for _, identity := range pin.Connectors {
				if !validConnectorSource(provider, identity.Source) ||
					!validConnectorText(identity.NativeID, maxConnectorNativeBytes) ||
					identity.NativeID != strings.TrimSpace(identity.NativeID) {
					t.Errorf("%s/%s pins invalid identity %+v", app, provider, identity)
				}
				key := string(provider) + "\x00" + string(identity.Source) + "\x00" + identity.NativeID
				if previous, claimed := claimedIdentities[key]; claimed {
					t.Errorf("%s and %s both pin %+v", previous, app, identity)
				}
				claimedIdentities[key] = app
			}
			for _, nativeID := range pin.Plugins {
				wantSuffix := map[ConnectorProvider]string{
					ConnectorProviderCodex:  "@openai-curated-remote",
					ConnectorProviderClaude: "@claude-plugins-official",
				}[provider]
				if !validConnectorText(nativeID, maxConnectorNativeBytes) ||
					nativeID != strings.TrimSpace(
						nativeID,
					) || !strings.HasSuffix(nativeID, wantSuffix) {
					t.Errorf("%s/%s pins invalid Plugin %q", app, provider, nativeID)
				}
				key := string(provider) + "\x00" + nativeID
				if previous, claimed := claimedPlugins[key]; claimed {
					t.Errorf("%s and %s both pin Plugin %q", previous, app, nativeID)
				}
				claimedPlugins[key] = app
			}
		}
	}

	codex := func(app ConnectorAppType) connectorAppPin {
		return connectorAppPins[app][ConnectorProviderCodex]
	}
	// "LINEAR" (asdk_app_6a8fb168…) is an unrelated third-party app.
	for _, identity := range codex("linear").Connectors {
		if strings.HasPrefix(identity.NativeID, "asdk_app_6a8fb168") {
			t.Errorf("linear pins the unrelated LINEAR app %q", identity.NativeID)
		}
	}
	if got := codex("jira").Connectors; len(got) != 2 ||
		got[0].NativeID != "asdk_app_6a83901dde988191b3f3cefdcc19acfa" ||
		got[1].NativeID != "connector_692de805e3ec8191834719067174a384" {
		t.Errorf("jira must prefer Atlassian Rovo over Rovo Legacy, got %+v", got)
	}
	if got := codex("apollo").Connectors; len(got) != 1 ||
		got[0].NativeID != "asdk_app_69bd664f2a908191a3a0a47eca8559d1" {
		t.Errorf("apollo must pin Apollo.io, got %+v", got)
	}
	// The catalog names only apps with a first-party Codex app, so every
	// type resolves on Codex.
	for _, app := range connectorAppTypes {
		if len(codex(app).Connectors) == 0 && len(codex(app).Plugins) == 0 {
			t.Errorf("%s has no Codex pin", app)
		}
	}
	for _, identity := range connectorAppPins["gmail"][ConnectorProviderClaude].Connectors {
		if identity.Source != ConnectorSourceClaudeCloud {
			t.Errorf("Claude gmail should resolve only to the Claude.ai cloud connector, got %+v", identity)
		}
	}
}

func TestNormalizeConnectorAppReadinessInput(t *testing.T) {
	t.Parallel()

	agentID := uuid.New()
	valid := ConnectorAppReadinessInput{AgentID: agentID, Apps: []ConnectorAppType{"gmail", "slack"}}
	if err := normalizeConnectorAppReadinessInput(valid); err != nil {
		t.Fatalf("normalizeConnectorAppReadinessInput(valid) error = %v", err)
	}
	if err := normalizeConnectorAppReadinessInput(
		ConnectorAppReadinessInput{AgentID: agentID, Apps: []ConnectorAppType{}},
	); err != nil {
		t.Fatalf("normalizeConnectorAppReadinessInput(no apps) error = %v", err)
	}
	for name, input := range map[string]ConnectorAppReadinessInput{
		"nil Agent":     {Apps: []ConnectorAppType{"gmail"}},
		"unknown app":   {AgentID: agentID, Apps: []ConnectorAppType{"myspace"}},
		"duplicate app": {AgentID: agentID, Apps: []ConnectorAppType{"gmail", "gmail"}},
		"seven apps": {AgentID: agentID, Apps: []ConnectorAppType{
			"gmail", "slack", "notion", "linear", "jira", "asana", "github",
		}},
	} {
		if err := normalizeConnectorAppReadinessInput(input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: error = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestConnectorAppDiscoveryState(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, time.September, 24, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name                               string
		bound, eligible, generation, fresh bool
		want                               ConnectorDiscoveryState
	}{
		{"no binding wins over everything", false, false, false, false, ConnectorDiscoveryStateUnbound},
		{"never reported", true, true, false, false, ConnectorDiscoveryStateMissing},
		{"removed Runtime", true, false, true, true, ConnectorDiscoveryStateMissing},
		{"fresh", true, true, true, true, ConnectorDiscoveryStateFresh},
		{"stale", true, true, true, false, ConnectorDiscoveryStateStale},
	}
	for _, testCase := range cases {
		got := connectorAppDiscovery(
			testCase.bound, testCase.eligible, testCase.generation, testCase.fresh, observedAt,
		)
		if got.State != testCase.want {
			t.Errorf("%s: state = %s, want %s", testCase.name, got.State, testCase.want)
		}
		reported := testCase.want == ConnectorDiscoveryStateFresh ||
			testCase.want == ConnectorDiscoveryStateStale
		if reported != (got.ObservedAt != nil) {
			t.Errorf("%s: observedAt = %v, want reported %v", testCase.name, got.ObservedAt, reported)
		}
	}
}

func TestPickConnectorAppState(t *testing.T) {
	t.Parallel()

	codexApp := func(status ConnectorStatus) connectorAppCandidate {
		return connectorAppCandidate{Source: ConnectorSourceCodexApp, Observed: true, Status: status}
	}
	eligible := connectorAppCandidate{
		Source: ConnectorSourceCodexApp, Observed: true, Status: ConnectorStatusConnected,
		CanExecute: true, Eligible: true,
	}
	executable := connectorAppCandidate{
		Source: ConnectorSourceCodexApp, Observed: true, Status: ConnectorStatusConnected, CanExecute: true,
	}
	with := func(candidate connectorAppCandidate, change func(*connectorAppCandidate)) connectorAppCandidate {
		change(&candidate)
		return candidate
	}
	notObserved := connectorAppCandidate{Source: ConnectorSourceCodexApp}
	cloud := connectorAppCandidate{
		Source: ConnectorSourceClaudeCloud, Observed: true, Status: ConnectorStatusManagedInProvider,
	}
	claudeNotObserved := connectorAppCandidate{Source: ConnectorSourceClaudePlugin}

	cases := []struct {
		name        string
		provider    ConnectorProvider
		discovery   ConnectorDiscoveryState
		candidates  []connectorAppCandidate
		installable bool
		want        ConnectorAppState
		wantIndex   int
	}{
		{
			"eligible and fresh is ready", ConnectorProviderCodex, ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{eligible},
			false, ConnectorAppStateReady, 0,
		},
		{
			"eligible but stale is pending", ConnectorProviderCodex, ConnectorDiscoveryStateStale,
			[]connectorAppCandidate{eligible},
			false, ConnectorAppStatePending, 0,
		},
		{
			"eligible but denied", ConnectorProviderCodex, ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{with(eligible, func(c *connectorAppCandidate) { c.Denied = true })},
			false, ConnectorAppStateDisabledForAgent, 0,
		},
		{
			"connected and executable but not yet enabled",
			ConnectorProviderCodex,
			ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{executable},
			false,
			ConnectorAppStatePending,
			0,
		},
		{
			"connected and executable, denied before enablement",
			ConnectorProviderCodex,
			ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{with(executable, func(c *connectorAppCandidate) { c.Denied = true })},
			false,
			ConnectorAppStateDisabledForAgent,
			0,
		},
		{
			"changed contract needs review",
			ConnectorProviderCodex,
			ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{
				with(executable, func(c *connectorAppCandidate) { c.ReviewRequired = true }),
			},
			false,
			ConnectorAppStateReviewRequired,
			0,
		},
		{
			"a disconnected enabled app asks to connect, not review",
			ConnectorProviderCodex,
			ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{
				with(
					codexApp(ConnectorStatusNeedsAuth),
					func(c *connectorAppCandidate) { c.ReviewRequired = true },
				),
			},
			false,
			ConnectorAppStateNeedsConnection,
			0,
		},
		{
			"available", ConnectorProviderCodex, ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{codexApp(ConnectorStatusAvailable)},
			false, ConnectorAppStateNeedsConnection, 0,
		},
		{
			"installed not ready", ConnectorProviderCodex, ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{codexApp(ConnectorStatusInstalledNotReady)},
			false, ConnectorAppStateNeedsConnection, 0,
		},
		{
			"needs auth", ConnectorProviderCodex, ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{codexApp(ConnectorStatusNeedsAuth)},
			false, ConnectorAppStateNeedsConnection, 0,
		},
		{
			"error", ConnectorProviderCodex, ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{codexApp(ConnectorStatusError)},
			false, ConnectorAppStateNeedsConnection, 0,
		},
		{
			"connected without a verified contract is pending",
			ConnectorProviderCodex,
			ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{codexApp(ConnectorStatusManagedInProvider)},
			false,
			ConnectorAppStatePending,
			0,
		},
		{
			"unavailable is not offered", ConnectorProviderCodex, ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{codexApp(ConnectorStatusUnavailable)},
			false, ConnectorAppStateNotOffered, -1,
		},
		{
			"no pinned identity observed", ConnectorProviderCodex, ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{notObserved},
			false, ConnectorAppStateNotOffered, -1,
		},
		{
			"no pin at all", ConnectorProviderCodex, ConnectorDiscoveryStateFresh,
			nil, false, ConnectorAppStateNotOffered, -1,
		},
		{
			"codex mcp is provider managed",
			ConnectorProviderCodex,
			ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{
				{Source: ConnectorSourceCodexMCP, Observed: true, Status: ConnectorStatusConnected},
			},
			false,
			ConnectorAppStateProviderManaged,
			0,
		},
		{
			"Claude.ai cloud connector is provider managed",
			ConnectorProviderClaude,
			ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{claudeNotObserved, cloud},
			false,
			ConnectorAppStateProviderManaged,
			1,
		},
		{
			"an installable pinned Plugin beats the cloud connector",
			ConnectorProviderClaude,
			ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{claudeNotObserved, cloud},
			true,
			ConnectorAppStateNeedsConnection,
			-1,
		},
		{
			"an installable pinned Plugin alone", ConnectorProviderCodex, ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{notObserved},
			true, ConnectorAppStateNeedsConnection, -1,
		},
		{
			"a connectable connector outranks the Plugin on a tie",
			ConnectorProviderCodex,
			ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{codexApp(ConnectorStatusAvailable)},
			true,
			ConnectorAppStateNeedsConnection,
			0,
		},
		{
			"a ready connector ignores the Plugin", ConnectorProviderCodex, ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{eligible},
			true, ConnectorAppStateReady, 0,
		},
		{
			"Rovo Legacy ready beats Rovo needing connection",
			ConnectorProviderCodex,
			ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{codexApp(ConnectorStatusAvailable), eligible},
			false,
			ConnectorAppStateReady,
			1,
		},
		{
			"review outranks needs connection across candidates",
			ConnectorProviderCodex,
			ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{
				with(executable, func(c *connectorAppCandidate) { c.ReviewRequired = true }),
				codexApp(ConnectorStatusAvailable),
			},
			false,
			ConnectorAppStateReviewRequired,
			0,
		},
		{
			"a tie keeps pin preference", ConnectorProviderCodex, ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{codexApp(ConnectorStatusNeedsAuth), codexApp(ConnectorStatusAvailable)},
			false, ConnectorAppStateNeedsConnection, 0,
		},
		{
			"an installed Plugin's identity resolves a re-issued app id",
			ConnectorProviderCodex,
			ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{notObserved, eligible},
			false,
			ConnectorAppStateReady,
			1,
		},
		{
			"disabled outranks review", ConnectorProviderCodex, ConnectorDiscoveryStateFresh,
			[]connectorAppCandidate{
				with(executable, func(c *connectorAppCandidate) { c.ReviewRequired = true }),
				with(eligible, func(c *connectorAppCandidate) { c.Denied = true }),
			},
			false, ConnectorAppStateDisabledForAgent, 1,
		},
		{
			"an Agent the viewer has not set up is unknown",
			ConnectorProviderCodex,
			ConnectorDiscoveryStateUnbound,
			[]connectorAppCandidate{eligible},
			true,
			ConnectorAppStateUnknown,
			-1,
		},
		{
			"missing discovery is unknown", ConnectorProviderCodex, ConnectorDiscoveryStateMissing,
			[]connectorAppCandidate{eligible},
			true, ConnectorAppStateUnknown, -1,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, index := pickConnectorAppState(
				testCase.provider, testCase.discovery, testCase.candidates, testCase.installable,
			)
			if got != testCase.want || index != testCase.wantIndex {
				t.Fatalf("pickConnectorAppState() = %s, %d; want %s, %d",
					got, index, testCase.want, testCase.wantIndex)
			}
		})
	}
}

// A reaffirmation carries no items, but its generation identity and time obey
// exactly the publication rules, including the clock-skew bound.
func TestNormalizeConnectorDiscoveryReaffirmation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 24, 9, 0, 0, 0, time.UTC)
	valid := ConnectorDiscoveryReaffirmInput{
		CompanyID: uuid.New(), RuntimeID: uuid.New(), DaemonID: " daemon-a ",
		Generation:     uuid.New(),
		ManifestDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ObservedAt:     now.Add(4 * time.Minute).In(time.FixedZone("ahead", 7*3600)),
	}
	normalized, err := normalizeConnectorDiscoveryReaffirmation(ConnectorProviderCodex, valid, now)
	if err != nil {
		t.Fatalf("normalizeConnectorDiscoveryReaffirmation() error = %v", err)
	}
	if normalized.DaemonID != "daemon-a" || normalized.ObservedAt.Location() != time.UTC ||
		!normalized.ObservedAt.Equal(valid.ObservedAt) {
		t.Fatalf("normalized = %+v", normalized)
	}
	for name, mutate := range map[string]func(*ConnectorDiscoveryReaffirmInput){
		"future beyond skew": func(input *ConnectorDiscoveryReaffirmInput) {
			input.ObservedAt = now.Add(6 * time.Minute)
		},
		"zero observation": func(input *ConnectorDiscoveryReaffirmInput) { input.ObservedAt = time.Time{} },
		"nil generation":   func(input *ConnectorDiscoveryReaffirmInput) { input.Generation = uuid.Nil },
		"uppercase digest": func(input *ConnectorDiscoveryReaffirmInput) {
			input.ManifestDigest = strings.ToUpper(input.ManifestDigest)
		},
		"short digest":     func(input *ConnectorDiscoveryReaffirmInput) { input.ManifestDigest = "abc" },
		"blank daemon":     func(input *ConnectorDiscoveryReaffirmInput) { input.DaemonID = " " },
		"nil Runtime":      func(input *ConnectorDiscoveryReaffirmInput) { input.RuntimeID = uuid.Nil },
		"unknown provider": nil,
	} {
		input, provider := valid, ConnectorProviderCodex
		if mutate == nil {
			provider = "gemini"
		} else {
			mutate(&input)
		}
		if _, err := normalizeConnectorDiscoveryReaffirmation(
			provider,
			input,
			now,
		); !errors.Is(
			err,
			ErrInvalidInput,
		) {
			t.Errorf("%s: error = %v, want ErrInvalidInput", name, err)
		}
	}
}

// An app's logo comes from the winning Connector, then any other observed
// candidate, then the pinned Plugin, then a Codex logo borrowed by one of
// their names; an unobserved candidate contributes nothing.
func TestConnectorAppIconPrefersTheWinnerThenCandidatesThenThePlugin(t *testing.T) {
	t.Parallel()

	icon := func(value string) pgtype.Text { return pgtype.Text{String: value, Valid: true} }
	rows := []generated.ListConnectorAppReadinessRow{
		{Name: "Unobserved", IconUrl: icon("https://cdn.test/unobserved.png")},
		{Name: "Candidate", IconUrl: icon("https://cdn.test/candidate.png")},
		{Name: "Winner", IconUrl: icon("https://cdn.test/winner.png")},
		{Name: "slack"},
	}
	candidates := []connectorAppCandidate{{}, {Observed: true}, {Observed: true}, {Observed: true}}
	plugin := &generated.ListPluginsForRuntimeByNativeIDRow{
		Name:    "Plugin",
		IconUrl: icon("https://cdn.test/plugin.png"),
	}
	borrowed := appLogoFallbacks{byName: namedLogos(map[string]string{"slack": "https://cdn.test/codex-slack.png"})}

	tests := []struct {
		name       string
		rows       []generated.ListConnectorAppReadinessRow
		candidates []connectorAppCandidate
		winner     int
		plugin     *generated.ListPluginsForRuntimeByNativeIDRow
		want       string
	}{
		{
			name:       "winner",
			rows:       rows,
			candidates: candidates,
			winner:     2,
			plugin:     plugin,
			want:       "https://cdn.test/winner.png",
		},
		{
			name:       "observed candidate",
			rows:       rows,
			candidates: candidates,
			winner:     3,
			plugin:     plugin,
			want:       "https://cdn.test/candidate.png",
		},
		{
			name:       "pinned plugin",
			rows:       rows[3:],
			candidates: candidates[3:],
			winner:     0,
			plugin:     plugin,
			want:       "https://cdn.test/plugin.png",
		},
		{
			name:       "borrowed by name",
			rows:       rows[3:],
			candidates: candidates[3:],
			winner:     0,
			want:       "https://cdn.test/codex-slack.png",
		},
		{
			name: "unobserved candidate only", rows: rows[:1], candidates: candidates[:1], winner: -1,
			plugin: &generated.ListPluginsForRuntimeByNativeIDRow{Name: "Nothing"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := connectorAppIcon(test.rows, test.candidates, test.winner, test.plugin, borrowed)
			if test.want == "" {
				if got != nil {
					t.Fatalf("connectorAppIcon() = %q, want none", *got)
				}
				return
			}
			if got == nil || *got != test.want {
				t.Fatalf("connectorAppIcon() = %v, want %s", got, test.want)
			}
		})
	}
}

// A logo that loads only in a ChatGPT session yields to any loadable one, in
// the same order with a bundling installed Plugin after the candidates; with
// nothing loadable the winner keeps its own.
func TestConnectorAppIconSkipsSessionOnlyLogos(t *testing.T) {
	t.Parallel()

	const (
		sessionOnly = "https://chatgpt.com/images/ecosystem/apps/gmail/icon.png"
		gmailID     = "connector_gmail"
		bundled     = "https://cdn.test/gmail-plugin.png"
		pinned      = "https://cdn.test/pinned.png"
		byAppName   = "https://cdn.test/gmail-by-name.png"
		byPlugin    = "https://cdn.test/plugin-by-name.png"
	)
	icon := func(value string) pgtype.Text { return pgtype.Text{String: value, Valid: true} }
	winner := generated.ListConnectorAppReadinessRow{
		Source: string(ConnectorSourceCodexApp), NativeID: gmailID, Name: "Gmail", IconUrl: icon(sessionOnly),
	}
	candidate := generated.ListConnectorAppReadinessRow{
		Name: "Candidate", IconUrl: icon("https://cdn.test/candidate.png"),
	}
	names := appLogoFallbacks{byName: namedLogos(map[string]string{"Gmail": byAppName, "Plugin": byPlugin})}
	fallbacks := appLogoFallbacks{
		bundles: []pluginLogoBundle{{name: "Gmail", logo: bundled, apps: []string{gmailID}}},
		byName:  names.byName,
	}
	sessionPlugin := &generated.ListPluginsForRuntimeByNativeIDRow{
		Name: "Plugin", IconUrl: icon("https://chatgpt.com/backend-api/estuary/content?id=plugin&sig=x"),
	}
	loadablePlugin := &generated.ListPluginsForRuntimeByNativeIDRow{Name: "Plugin", IconUrl: icon(pinned)}

	tests := []struct {
		name      string
		rows      []generated.ListConnectorAppReadinessRow
		plugin    *generated.ListPluginsForRuntimeByNativeIDRow
		fallbacks appLogoFallbacks
		want      string
	}{
		{
			name: "loadable candidate", rows: []generated.ListConnectorAppReadinessRow{winner, candidate},
			plugin: loadablePlugin, fallbacks: fallbacks, want: "https://cdn.test/candidate.png",
		},
		{
			name: "bundling plugin", rows: []generated.ListConnectorAppReadinessRow{winner},
			plugin: loadablePlugin, fallbacks: fallbacks, want: bundled,
		},
		{
			name: "pinned plugin", rows: []generated.ListConnectorAppReadinessRow{winner},
			plugin: loadablePlugin, fallbacks: names, want: pinned,
		},
		{
			name: "borrowed by the app's name", rows: []generated.ListConnectorAppReadinessRow{winner},
			plugin: sessionPlugin, fallbacks: names, want: byAppName,
		},
		{
			name:   "borrowed by the plugin's name",
			rows:   []generated.ListConnectorAppReadinessRow{winner},
			plugin: sessionPlugin,
			fallbacks: appLogoFallbacks{
				byName: namedLogos(map[string]string{"Plugin": byPlugin}),
			},
			want: byPlugin,
		},
		{
			name: "nothing loadable keeps the winner's own",
			rows: []generated.ListConnectorAppReadinessRow{
				winner,
			},
			plugin: sessionPlugin,
			want:   sessionOnly,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidates := make([]connectorAppCandidate, len(test.rows))
			for index := range candidates {
				candidates[index].Observed = true
			}
			got := connectorAppIcon(test.rows, candidates, 0, test.plugin, test.fallbacks)
			if got == nil || *got != test.want {
				t.Fatalf("connectorAppIcon() = %v, want %s", got, test.want)
			}
		})
	}
}
