package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestClaudeConnectorBackendDiscoversOnlyProvenConnectors(t *testing.T) {
	t.Parallel()

	runner := newClaudeConnectorRunnerStub(map[string][]claudeConnectorStubResult{
		claudeConnectorCommandKey("plugin", "list", "--json"): {{output: []byte(`[
			{"id":"figma@claude-plugins-official","version":"2.0.1","enabled":true,
			 "installPath":"/must/not/leak","mcpServers":{"figma":{"url":"https://mcp.figma.com/mcp","headers":{"Authorization":"secret-token"}}}},
			{"id":"private-search@team-marketplace","enabled":true,
			 "mcpServers":{"search":{"command":"/secret/binary","env":{"PRIVATE_TOKEN":"secret-token"}}}},
			{"id":"code-review@claude-plugins-official","enabled":true}
		]`)}},
		claudeConnectorCommandKey("plugin", "marketplace", "list", "--json"): {{output: []byte(`[
			{"name":"claude-plugins-official","source":"github","repo":"anthropics/claude-plugins-official",
			 "installLocation":"/must/not/leak"}
		]`)}},
		claudeConnectorCommandKey("plugin", "list", "--available", "--json"): {{output: []byte(`{
			"installed":[],
			"available":[
				{"name":"asana","pluginId":"asana@claude-plugins-official","marketplaceName":"claude-plugins-official"},
				{"name":"code-review","pluginId":"code-review@claude-plugins-official","marketplaceName":"claude-plugins-official"},
				{"name":"asana","pluginId":"asana@untrusted-marketplace","marketplaceName":"untrusted-marketplace","mcpServers":{"evil":{}}},
				{"name":"custom","pluginId":"custom@team-marketplace","marketplaceName":"team-marketplace","mcpServers":{"custom":{}}}
			]
		}`)}},
		claudeConnectorCommandKey("mcp", "list"): {{output: []byte(
			"plugin:figma:figma: https://mcp.figma.com/mcp (HTTP) - ! Needs authentication\n" +
				"plugin:private-search:search: /secret/binary --token secret-token - ✓ Connected\n",
		)}},
	})
	backend := newClaudeConnectorBackend("/usr/local/bin/claude", runner)

	catalog, err := backend.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Connectors) != 9 {
		t.Fatalf("connector count = %d, want 9: %+v", len(catalog.Connectors), catalog.Connectors)
	}
	assertClaudeConnector(t, catalog, "figma@claude-plugins-official", ClaudeConnectorStatusNeedsAuthentication)
	assertClaudeConnector(t, catalog, "private-search@team-marketplace", ClaudeConnectorStatusConnected)
	assertClaudeConnector(t, catalog, "asana@claude-plugins-official", ClaudeConnectorStatusAvailable)
	for _, excluded := range []string{
		"code-review@claude-plugins-official",
		"asana@untrusted-marketplace",
		"custom@team-marketplace",
	} {
		if _, ok := catalogConnector(catalog, excluded); ok {
			t.Fatalf("non-connector %q was included", excluded)
		}
	}
	for _, cloudID := range []string{
		"claude-cloud:google-drive",
		"claude-cloud:gmail",
		"claude-cloud:google-calendar",
		"claude-cloud:github",
		"claude-cloud:slack",
		"claude-cloud:microsoft-365",
	} {
		assertClaudeConnector(t, catalog, cloudID, ClaudeConnectorStatusManagedInProvider)
	}
	body, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-token", "/must/not/leak", "/secret/binary", "Authorization", "PRIVATE_TOKEN"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("catalog leaked %q: %s", secret, body)
		}
	}
}

// The connector backend reads the same available list. Past 1 MiB it must
// still offer the official connectors that are not installed.
func TestClaudeConnectorBackendReadsAnAvailableListPastOneMiB(t *testing.T) {
	t.Parallel()

	available := `{"installed":[],"available":[` +
		`{"name":"asana","pluginId":"asana@claude-plugins-official","marketplaceName":"claude-plugins-official"},` +
		claudeCommunityPlugins(1500) + `]}`
	runner := newClaudeConnectorRunnerStub(map[string][]claudeConnectorStubResult{
		claudeConnectorCommandKey("plugin", "list", "--json"): {{output: []byte(`[]`)}},
		claudeConnectorCommandKey("plugin", "marketplace", "list", "--json"): {{output: []byte(`[
			{"name":"claude-plugins-official","source":"github","repo":"anthropics/claude-plugins-official"}
		]`)}},
		claudeConnectorCommandKey("plugin", "list", "--available", "--json"): {{output: []byte(available)}},
	})

	catalog, err := newClaudeConnectorBackend("/usr/local/bin/claude", runner).Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	assertClaudeConnector(t, catalog, "asana@claude-plugins-official", ClaudeConnectorStatusAvailable)
}

func TestClaudeConnectorBackendFailsClosedOnMalformedPluginOutput(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"invalid JSON":  `not-json`,
		"trailing JSON": `[] {}`,
		"invalid ID":    `[{"id":"figma@claude-plugins-official\n--help","mcpServers":{"figma":{}}}]`,
		"duplicate ID":  `[{"id":"figma@claude-plugins-official","mcpServers":{"figma":{}}},{"id":"figma@claude-plugins-official","mcpServers":{"figma":{}}}]`,
	}
	for name, output := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runner := newClaudeConnectorRunnerStub(map[string][]claudeConnectorStubResult{
				claudeConnectorCommandKey("plugin", "list", "--json"): {{output: []byte(output)}},
			})
			backend := newClaudeConnectorBackend("/usr/local/bin/claude", runner)

			if _, err := backend.Discover(t.Context()); err == nil {
				t.Fatal("malformed plugin output was accepted")
			}
		})
	}
}

func TestClaudeConnectorBackendTreatsUnknownMCPHealthAsInstalled(t *testing.T) {
	t.Parallel()

	runner := newClaudeConnectorRunnerStub(map[string][]claudeConnectorStubResult{
		claudeConnectorCommandKey("plugin", "list", "--json"): {{output: []byte(`[
			{"id":"figma@claude-plugins-official","mcpServers":{"design":{},"assets":{}}},
			{"id":"slack@claude-plugins-official","mcpServers":{"slack":{}}}
		]`)}},
		claudeConnectorCommandKey("plugin", "marketplace", "list", "--json"): {{err: errors.New("old CLI")}},
		claudeConnectorCommandKey("plugin", "list", "--available", "--json"): {{err: errors.New("old CLI")}},
		claudeConnectorCommandKey("mcp", "list"): {{output: []byte(
			"plugin:figma:design: https://example.invalid/mcp (HTTP) - ✓ Connected\n" +
				"plugin:figma:assets: https://example.invalid/mcp (HTTP) - ✗ Failed to connect\n" +
				"plugin:slack:slack: https://example.invalid/mcp (HTTP) - ! Needs authentication\n",
		)}},
	})
	backend := newClaudeConnectorBackend("/usr/local/bin/claude", runner)

	catalog, err := backend.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	assertClaudeConnector(t, catalog, "figma@claude-plugins-official", ClaudeConnectorStatusInstalled)
	assertClaudeConnector(t, catalog, "slack@claude-plugins-official", ClaudeConnectorStatusNeedsAuthentication)
}

func TestClaudeConnectorBackendDiscoversStandaloneMCPWithoutLeakingConfiguration(t *testing.T) {
	t.Parallel()

	runner := newClaudeConnectorRunnerStub(map[string][]claudeConnectorStubResult{
		claudeConnectorCommandKey("plugin", "list", "--json"): {{output: []byte(`[]`)}},
		claudeConnectorCommandKey("plugin", "marketplace", "list", "--json"): {
			{err: errors.New("old CLI")},
		},
		claudeConnectorCommandKey("plugin", "list", "--available", "--json"): {
			{err: errors.New("old CLI")},
		},
		claudeConnectorCommandKey("mcp", "list"): {{output: []byte(
			"github: https://api.github.example/mcp?token=must-not-leak (HTTP) - ✓ Connected\n" +
				"internal-search: /secret/bin --api-key must-not-leak - ! Needs authentication\n" +
				"plugin:slack:slack: https://mcp.slack.example/mcp (HTTP) - ✓ Connected\n" +
				"bad target;rm: /tmp/x - ✓ Connected\n",
		)}},
	})
	backend := newClaudeConnectorBackend("/usr/local/bin/claude", runner)

	catalog, err := backend.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	github, ok := catalogConnector(catalog, "github")
	if !ok || github.Kind != ClaudeConnectorKindMCP ||
		github.Status != ClaudeConnectorStatusConnected {
		t.Fatalf("standalone GitHub MCP = %+v, %t", github, ok)
	}
	search, ok := catalogConnector(catalog, "internal-search")
	if !ok || search.Kind != ClaudeConnectorKindMCP ||
		search.Status != ClaudeConnectorStatusNeedsAuthentication {
		t.Fatalf("standalone search MCP = %+v, %t", search, ok)
	}
	if _, ok := catalogConnector(catalog, "bad target;rm"); ok {
		t.Fatal("unsafe standalone MCP target was discovered")
	}
	encoded, _ := json.Marshal(catalog)
	for _, secret := range []string{"must-not-leak", "/secret/bin", "api.github.example"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("standalone MCP catalog leaked %q: %s", secret, encoded)
		}
	}
}

func TestClaudeConnectorBackendResolvesOnlySelectedLocalContracts(t *testing.T) {
	t.Parallel()

	selectedDirectory := t.TempDir()
	unselectedDirectory := t.TempDir()
	pluginOutput := `[
		{"id":"slack@claude-plugins-official","enabled":true,"installPath":"` + selectedDirectory + `",
		 "mcpServers":{"messages":{"url":"https://mcp.slack.example"},"search":{"command":"/private/slack"}}},
		{"id":"figma@claude-plugins-official","enabled":true,"installPath":"` + unselectedDirectory + `",
		 "mcpServers":{"figma":{"url":"https://mcp.figma.example"}}}
	]`
	healthOutput := "plugin:slack:messages: https://mcp.slack.example - ✓ Connected\n" +
		"plugin:slack:search: /private/slack - ✓ Connected\n" +
		"plugin:figma:figma: https://mcp.figma.example - ✓ Connected\n" +
		"linear: /private/linear --token must-not-leak - ✓ Connected\n"
	driftedPluginOutput := strings.Replace(pluginOutput, `"search"`, `"files"`, 1)
	driftedHealthOutput := strings.Replace(healthOutput, ":search:", ":files:", 1)
	runner := newClaudeConnectorRunnerStub(map[string][]claudeConnectorStubResult{
		claudeConnectorCommandKey("plugin", "list", "--json"): append(
			repeatClaudeConnectorStub(claudeConnectorStubResult{output: []byte(pluginOutput)}, 2),
			claudeConnectorStubResult{output: []byte(driftedPluginOutput)},
		),
		claudeConnectorCommandKey("plugin", "marketplace", "list", "--json"): {
			{err: errors.New("old CLI")}, {err: errors.New("old CLI")}, {err: errors.New("old CLI")},
		},
		claudeConnectorCommandKey("plugin", "list", "--available", "--json"): {
			{err: errors.New("old CLI")}, {err: errors.New("old CLI")}, {err: errors.New("old CLI")},
		},
		claudeConnectorCommandKey("mcp", "list"): append(
			repeatClaudeConnectorStub(claudeConnectorStubResult{output: []byte(healthOutput)}, 2),
			claudeConnectorStubResult{output: []byte(driftedHealthOutput)},
		),
	})
	backend := newClaudeConnectorBackend("/usr/local/bin/claude", runner)
	backend.loadUserMCPServers = func() (map[string]json.RawMessage, error) {
		return map[string]json.RawMessage{
			"linear": json.RawMessage(`{"command":"/private/linear","env":{"TOKEN":"must-not-leak"}}`),
		}, nil
	}

	catalog, err := backend.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	selected, ok := catalogConnector(catalog, "slack@claude-plugins-official")
	if !ok || !selected.CanExecute || len(selected.Tools) != 2 || selected.ContractDigest == "" {
		t.Fatalf("selected plugin contract = %+v, %t", selected, ok)
	}
	standalone, ok := catalogConnector(catalog, "linear")
	if !ok || !standalone.CanExecute || len(standalone.Tools) != 1 || standalone.ContractDigest == "" {
		t.Fatalf("standalone MCP contract = %+v, %t", standalone, ok)
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		selectedDirectory,
		unselectedDirectory,
		"/private/linear",
		"must-not-leak",
	} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("sanitized catalog leaked %q: %s", private, encoded)
		}
	}

	execution, err := backend.resolveExecution(t.Context(), []claudeConnectorSelection{
		{
			Kind: ClaudeConnectorKindPlugin, NativeID: selected.ID,
			ContractDigest: selected.ContractDigest, Tools: selected.Tools,
		},
		{
			Kind: ClaudeConnectorKindMCP, NativeID: standalone.ID,
			ContractDigest: standalone.ContractDigest, Tools: standalone.Tools,
		},
	})
	if err != nil {
		t.Fatalf("resolveExecution() error = %v", err)
	}
	if !slices.Equal(execution.PluginDirectories, []string{selectedDirectory}) ||
		!slices.Equal(execution.AllowedMCPServers, []string{
			"linear", "plugin_slack_messages", "plugin_slack_search",
		}) || len(execution.MCPServers) != 1 ||
		!strings.Contains(string(execution.MCPServers["linear"]), "must-not-leak") {
		t.Fatalf("resolveExecution() = %+v", execution)
	}
	if slices.Contains(execution.PluginDirectories, unselectedDirectory) {
		t.Fatal("resolveExecution() loaded an unselected plugin")
	}

	drifted := []claudeConnectorSelection{{
		Kind: ClaudeConnectorKindPlugin, NativeID: selected.ID,
		ContractDigest: selected.ContractDigest, Tools: selected.Tools,
	}}
	if _, err := backend.resolveExecution(t.Context(), drifted); !errors.Is(
		err,
		errClaudeConnectorContractDrift,
	) {
		t.Fatalf("resolveExecution(drift) error = %v", err)
	}
}

func TestClaudeConnectorBackendKeepsStandaloneMCPManagedWhenLoginIsUnsupported(t *testing.T) {
	t.Parallel()

	runner := newClaudeConnectorRunnerStub(map[string][]claudeConnectorStubResult{
		claudeConnectorCommandKey("plugin", "list", "--json"): {{output: []byte(`[]`)}},
		claudeConnectorCommandKey("plugin", "marketplace", "list", "--json"): {
			{err: errors.New("old CLI")},
		},
		claudeConnectorCommandKey("plugin", "list", "--available", "--json"): {
			{err: errors.New("old CLI")},
		},
		claudeConnectorCommandKey("mcp", "list"): {
			{output: []byte("internal-search: https://mcp.example/search - ! Needs authentication\n")},
			{output: []byte("internal-search: https://mcp.example/search - ! Needs authentication\n")},
		},
	})
	backend := newClaudeConnectorBackend("/usr/local/bin/claude", runner)
	if _, err := backend.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}

	result, err := backend.Connect(t.Context(), "internal-search")
	if err != nil || result.Status != ClaudeConnectorStatusManagedInProvider ||
		result.AuthorizationURL != "" || !strings.Contains(result.Instruction, "Claude") {
		t.Fatalf("standalone MCP Connect() = %+v, %v", result, err)
	}
	baseline := len(runner.callsSnapshot())
	if _, err := backend.Connect(
		t.Context(),
		"internal-search;rm",
	); !errors.Is(
		err,
		errClaudeConnectorNotDiscovered,
	) {
		t.Fatalf("unsafe standalone Connect() error = %v", err)
	}
	if len(runner.callsSnapshot()) != baseline {
		t.Fatal("unsafe standalone MCP target executed a command")
	}
}

func TestClaudeConnectorBackendConnectsDiscoveredPluginAndReturnsOnlyHTTPSAuthorizationURL(t *testing.T) {
	t.Parallel()

	runner := newClaudeConnectorRunnerStub(map[string][]claudeConnectorStubResult{
		claudeConnectorCommandKey("plugin", "list", "--json"): {
			{output: []byte(`[]`)},
			{output: []byte(`[{"id":"asana@claude-plugins-official","mcpServers":{"asana":{}}}]`)},
		},
		claudeConnectorCommandKey("plugin", "marketplace", "list", "--json"): {{output: []byte(`[
			{"name":"claude-plugins-official","source":"github","repo":"anthropics/claude-plugins-official"}
		]`)}},
		claudeConnectorCommandKey("plugin", "list", "--available", "--json"): {{output: []byte(`{
			"installed":[],"available":[
				{"name":"asana","pluginId":"asana@claude-plugins-official","marketplaceName":"claude-plugins-official"}
			]
		}`)}},
		claudeConnectorCommandKey("plugin", "install", "asana@claude-plugins-official"): {
			{output: []byte("installed")},
		},
		claudeConnectorCommandKey("mcp", "list"): {
			{output: []byte("No MCP servers configured.\n")},
			{
				output: []byte(
					"plugin:asana:asana: https://mcp.asana.com/mcp (HTTP) - ! Needs authentication\n",
				),
			},
		},
	})
	backend := newClaudeConnectorBackend("/usr/local/bin/claude", runner)
	if _, err := backend.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}

	result, err := backend.Connect(t.Context(), "asana@claude-plugins-official")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ClaudeConnectorStatusManagedInProvider ||
		result.AuthorizationURL != "" || !strings.Contains(result.Instruction, "Claude") {
		t.Fatalf("connect result = %+v", result)
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "must-not-leak") {
		t.Fatalf("connect result leaked CLI output: %s", body)
	}
	wantCalls := [][]string{
		{"plugin", "install", "asana@claude-plugins-official"},
	}
	for _, want := range wantCalls {
		if !runner.called(want...) {
			t.Fatalf("command was not called: %v; calls = %v", want, runner.callsSnapshot())
		}
	}
}

func TestClaudeConnectorBackendRejectsConnectInjectionAndUnknownIDs(t *testing.T) {
	t.Parallel()

	runner := newClaudeConnectorRunnerStub(map[string][]claudeConnectorStubResult{
		claudeConnectorCommandKey("plugin", "list", "--json"): {{output: []byte(`[]`)}},
		claudeConnectorCommandKey("plugin", "marketplace", "list", "--json"): {{output: []byte(`[
			{"name":"claude-plugins-official","source":"github","repo":"anthropics/claude-plugins-official"}
		]`)}},
		claudeConnectorCommandKey("plugin", "list", "--available", "--json"): {{output: []byte(`{
			"installed":[],"available":[
				{"name":"figma","pluginId":"figma@claude-plugins-official","marketplaceName":"claude-plugins-official"}
			]
		}`)}},
		claudeConnectorCommandKey("mcp", "list"): {{output: []byte("No MCP servers configured.\n")}},
	})
	backend := newClaudeConnectorBackend("/usr/local/bin/claude", runner)
	if _, err := backend.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	baseline := len(runner.callsSnapshot())

	for _, id := range []string{
		"figma@claude-plugins-official; rm -rf /",
		"--help",
		"figma@claude-plugins-official\nplugin install slack@claude-plugins-official",
		"slack@claude-plugins-official",
	} {
		if _, err := backend.Connect(t.Context(), id); !errors.Is(err, errClaudeConnectorNotDiscovered) {
			t.Fatalf("Connect(%q) error = %v", id, err)
		}
	}
	if got := len(runner.callsSnapshot()); got != baseline {
		t.Fatalf("rejected IDs executed commands: calls before = %d, after = %d", baseline, got)
	}
}

func TestClaudeConnectorBackendFallsBackToClaudeManagedAuthOnOldCLI(t *testing.T) {
	t.Parallel()

	runner := newClaudeConnectorRunnerStub(map[string][]claudeConnectorStubResult{
		claudeConnectorCommandKey("plugin", "list", "--json"): {{output: []byte(`[
			{"id":"slack@claude-plugins-official","mcpServers":{"slack":{}}}
		]`)}},
		claudeConnectorCommandKey("plugin", "marketplace", "list", "--json"): {
			{err: errors.New("unsupported")},
		},
		claudeConnectorCommandKey("plugin", "list", "--available", "--json"): {
			{err: errors.New("unsupported")},
		},
		claudeConnectorCommandKey("mcp", "list"): {
			{
				output: []byte(
					"plugin:slack:slack: https://mcp.slack.com/mcp (HTTP) - ! Needs authentication\n",
				),
			},
			{
				output: []byte(
					"plugin:slack:slack: https://mcp.slack.com/mcp (HTTP) - ! Needs authentication\n",
				),
			},
		},
	})
	backend := newClaudeConnectorBackend("/usr/local/bin/claude", runner)
	catalog, err := backend.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	assertClaudeConnector(t, catalog, "slack@claude-plugins-official", ClaudeConnectorStatusNeedsAuthentication)

	result, err := backend.Connect(t.Context(), "slack@claude-plugins-official")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ClaudeConnectorStatusManagedInProvider || result.AuthorizationURL != "" ||
		!strings.Contains(result.Instruction, "Claude") {
		t.Fatalf("connect result = %+v", result)
	}
	if runner.called("mcp", "login", "plugin:slack:slack") {
		t.Fatal("unsupported login command was executed")
	}
}

func TestClaudeConnectorBackendReturnsManagementInstructionForAlreadyConnectedPlugin(
	t *testing.T,
) {
	t.Parallel()

	runner := newClaudeConnectorRunnerStub(map[string][]claudeConnectorStubResult{
		claudeConnectorCommandKey("plugin", "list", "--json"): {{output: []byte(`[
			{"id":"slack@claude-plugins-official","mcpServers":{"slack":{}}}
		]`)}},
		claudeConnectorCommandKey("plugin", "marketplace", "list", "--json"): {
			{err: errors.New("unsupported")},
		},
		claudeConnectorCommandKey("plugin", "list", "--available", "--json"): {
			{err: errors.New("unsupported")},
		},
		claudeConnectorCommandKey("mcp", "list"): {
			{output: []byte("plugin:slack:slack: https://mcp.slack.com/mcp - ✓ Connected\n")},
			{output: []byte("plugin:slack:slack: https://mcp.slack.com/mcp - ✓ Connected\n")},
		},
	})
	backend := newClaudeConnectorBackend("/usr/local/bin/claude", runner)
	if _, err := backend.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := backend.Connect(t.Context(), "slack@claude-plugins-official")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ClaudeConnectorStatusManagedInProvider || result.Instruction == "" ||
		result.AuthorizationURL != "" {
		t.Fatalf("connect result = %+v", result)
	}
}

func TestClaudeConnectorBackendCloudConnectorsStayManagedInClaude(t *testing.T) {
	t.Parallel()

	runner := newClaudeConnectorRunnerStub(map[string][]claudeConnectorStubResult{
		claudeConnectorCommandKey("plugin", "list", "--json"): {{err: errors.New("plugins unsupported")}},
	})
	backend := newClaudeConnectorBackend("/usr/local/bin/claude", runner)
	catalog, err := backend.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Connectors) != 6 {
		t.Fatalf("connector count = %d, want 6", len(catalog.Connectors))
	}

	result, err := backend.Connect(t.Context(), "claude-cloud:gmail")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ClaudeConnectorStatusManagedInProvider || result.AuthorizationURL != "" ||
		!strings.Contains(result.Instruction, "Claude") {
		t.Fatalf("connect result = %+v", result)
	}
	if len(runner.callsSnapshot()) != 2 ||
		!runner.called("auth", "status", "--json") ||
		!runner.called("plugin", "list", "--json") {
		t.Fatalf("cloud connector executed a command: %v", runner.callsSnapshot())
	}
}

func TestClaudeConnectorBackendProvesCloudReuseOnlyForClaudeSubscriptionAuth(t *testing.T) {
	t.Parallel()

	for name, authOutput := range map[string]string{
		"subscription": `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","subscriptionType":"max","email":"must-not-leak@example.test"}`,
		"api key":      `{"loggedIn":true,"authMethod":"apiKey","apiProvider":"firstParty"}`,
		"bedrock":      `{"loggedIn":true,"authMethod":"external","apiProvider":"bedrock"}`,
		"signed out":   `{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}`,
	} {
		t.Run(name, func(t *testing.T) {
			runner := newClaudeConnectorRunnerStub(map[string][]claudeConnectorStubResult{
				claudeConnectorCommandKey("auth", "status", "--json"): {{output: []byte(authOutput)}},
				claudeConnectorCommandKey("plugin", "list", "--json"): {
					{err: errors.New("plugins unsupported")},
				},
			})
			catalog, err := newClaudeConnectorBackend("/usr/local/bin/claude", runner).Discover(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := name == "subscription"
			if catalog.CloudReusable != want {
				t.Fatalf("CloudReusable = %t, want %t", catalog.CloudReusable, want)
			}
			encoded, _ := json.Marshal(catalog)
			if strings.Contains(string(encoded), "must-not-leak") {
				t.Fatalf("catalog leaked auth identity: %s", encoded)
			}
		})
	}
}

func assertClaudeConnector(
	t *testing.T,
	catalog ClaudeConnectorCatalog,
	id string,
	status ClaudeConnectorStatus,
) {
	t.Helper()
	connector, ok := catalogConnector(catalog, id)
	if !ok {
		t.Fatalf("connector %q is missing: %+v", id, catalog.Connectors)
	}
	if connector.Status != status {
		t.Fatalf("connector %q status = %q, want %q", id, connector.Status, status)
	}
}

func catalogConnector(catalog ClaudeConnectorCatalog, id string) (ClaudeConnector, bool) {
	for _, connector := range catalog.Connectors {
		if connector.ID == id {
			return connector, true
		}
	}
	return ClaudeConnector{}, false
}

type claudeConnectorStubResult struct {
	output []byte
	err    error
}

type claudeConnectorRunnerStub struct {
	mu        sync.Mutex
	responses map[string][]claudeConnectorStubResult
	calls     [][]string
}

func newClaudeConnectorRunnerStub(
	responses map[string][]claudeConnectorStubResult,
) *claudeConnectorRunnerStub {
	return &claudeConnectorRunnerStub{responses: responses}
}

func (s *claudeConnectorRunnerStub) Output(
	_ context.Context,
	_ string,
	args []string,
) (claudeConnectorCommandOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, slices.Clone(args))
	key := claudeConnectorCommandKey(args...)
	results := s.responses[key]
	if len(results) == 0 {
		return claudeConnectorCommandOutput{}, errors.New("unexpected command")
	}
	result := results[0]
	if len(results) == 1 {
		delete(s.responses, key)
	} else {
		s.responses[key] = results[1:]
	}
	return claudeConnectorCommandOutput{stdout: slices.Clone(result.output)}, result.err
}

func (s *claudeConnectorRunnerStub) called(args ...string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.ContainsFunc(s.calls, func(call []string) bool {
		return slices.Equal(call, args)
	})
}

func (s *claudeConnectorRunnerStub) callsSnapshot() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([][]string, len(s.calls))
	for index, call := range s.calls {
		result[index] = slices.Clone(call)
	}
	return result
}

func claudeConnectorCommandKey(args ...string) string {
	return strings.Join(args, "\x00")
}

func repeatClaudeConnectorStub(
	result claudeConnectorStubResult,
	count int,
) []claudeConnectorStubResult {
	results := make([]claudeConnectorStubResult, count)
	for index := range results {
		results[index] = claudeConnectorStubResult{
			output: slices.Clone(result.output),
			err:    result.err,
		}
	}
	return results
}
