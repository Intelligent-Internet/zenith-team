package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestConnectorToolContractDigestIsOrderIndependent(t *testing.T) {
	t.Parallel()

	left := []ConnectorTool{
		{Name: "files.read", Enabled: true, ReadOnly: true},
		{Name: "files.write", Enabled: true},
	}
	right := []ConnectorTool{left[1], left[0]}

	leftDigest, err := ConnectorToolContractDigest(left)
	if err != nil {
		t.Fatalf("ConnectorToolContractDigest(left) error = %v", err)
	}
	rightDigest, err := ConnectorToolContractDigest(right)
	if err != nil {
		t.Fatalf("ConnectorToolContractDigest(right) error = %v", err)
	}
	if leftDigest != rightDigest {
		t.Fatalf("digests differ: left = %q, right = %q", leftDigest, rightDigest)
	}
}

func TestApplyCodexConnectorScopeIsExact(t *testing.T) {
	t.Parallel()

	params := map[string]any{"config": map[string]any{"model_reasoning_effort": "high"}}
	applyCodexConnectorScope(params, []ConnectorApp{{
		ID: "google_drive", Name: "Google Drive",
		Tools: []ConnectorTool{{Name: "files.read", Enabled: true, ReadOnly: true}},
	}})
	config := params["config"].(map[string]any)
	apps := config["apps"].(map[string]any)
	defaults := apps["_default"].(map[string]any)
	drive := apps["google_drive"].(map[string]any)
	tools := drive["tools"].(map[string]any)
	if defaults["enabled"] != false || drive["enabled"] != true ||
		tools["files.read"].(map[string]any)["enabled"] != true {
		t.Fatalf("connector scope = %#v", apps)
	}
	if _, found := apps["unselected"]; found {
		t.Fatalf("unselected connector was enabled: %#v", apps)
	}
	if config["model_reasoning_effort"] != "high" {
		t.Fatalf("existing config was replaced: %#v", config)
	}
}

func TestApplyCodexConnectorScopeEmptySliceDisablesAllApps(t *testing.T) {
	t.Parallel()

	params := map[string]any{"config": nil}
	applyCodexConnectorScope(params, []ConnectorApp{})
	apps := params["config"].(map[string]any)["apps"].(map[string]any)
	if len(apps) != 1 || apps["_default"].(map[string]any)["enabled"] != false {
		t.Fatalf("apps = %#v", apps)
	}
}

func TestCodexTurnInputWithConnectorsAddsMentions(t *testing.T) {
	t.Parallel()

	input := codexTurnInputWithConnectors("continue", true, false, "context lost: ", []ConnectorApp{
		{ID: "drive", Name: "Drive"},
		{ID: "slack", Name: "Slack"},
	})
	want := []map[string]any{
		{"type": "text", "text": "$drive $slack context lost: continue"},
		{"type": "mention", "name": "Drive", "path": "app://drive"},
		{"type": "mention", "name": "Slack", "path": "app://slack"},
	}
	if !reflect.DeepEqual(input, want) {
		t.Fatalf("input = %#v, want %#v", input, want)
	}
}

func TestBuildClaudeArgsUsesOnlyManagedPluginDirectories(t *testing.T) {
	t.Parallel()

	managed := filepath.Join(t.TempDir(), "managed-plugin")
	args := buildClaudeArgs(ExecOptions{
		PluginDirectories: []string{managed},
		CustomArgs:        []string{"--plugin-dir", "/tmp/unreviewed", "--plugin-dir=/tmp/also-unreviewed"},
	}, "", slog.Default())
	if !containsArg(args, "--plugin-dir="+managed) {
		t.Fatalf("managed plugin directory missing from args: %v", args)
	}
	if containsArg(args, "/tmp/unreviewed") || containsArg(args, "--plugin-dir=/tmp/also-unreviewed") {
		t.Fatalf("unreviewed plugin directory survived filtering: %v", args)
	}
}

func TestBuildClaudeArgsConnectorOnlyIsFailClosed(t *testing.T) {
	t.Parallel()

	managed := filepath.Join(t.TempDir(), "managed-plugin")
	args := buildClaudeArgs(ExecOptions{
		ToolPolicy:        ToolPolicyConnectorOnly,
		PluginDirectories: []string{managed},
		ExtraArgs: []string{
			"--tools=default",
			"--permission-mode", "bypassPermissions",
		},
		CustomArgs: []string{"--plugin-dir=/tmp/unreviewed", "--chrome"},
	}, "", slog.Default())

	for _, want := range []string{
		"--disable-slash-commands",
		"--no-chrome",
		"--no-session-persistence",
		"--strict-mcp-config",
		"--setting-sources=",
		"--permission-mode",
		"manual",
		"--permission-prompt-tool=stdio",
		"--tools=",
		"--plugin-dir=" + managed,
	} {
		if !slices.Contains(args, want) {
			t.Fatalf("connector-only args missing %q: %v", want, args)
		}
	}
	joined := strings.Join(args, "\x00")
	for _, forbidden := range []string{"bypassPermissions", "/tmp/unreviewed", "--chrome", "--tools=default"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("connector-only args retained %q: %v", forbidden, args)
		}
	}
}

func TestCodexConnectorOnlyThreadPolicyDisablesNativeTools(t *testing.T) {
	t.Parallel()

	params := map[string]any{
		"approvalPolicy":         nil,
		"sandbox":                nil,
		"persistExtendedHistory": true,
	}
	applyCodexConnectorOnlyThreadPolicy(params, ExecOptions{
		ToolPolicy: ToolPolicyConnectorOnly,
		McpConfig: json.RawMessage(
			`{"mcpServers":{"calendar":{"command":"calendar-connector"}}}`,
		),
		AllowedMCPServers: []string{"calendar"},
		ConnectorApps:     []ConnectorApp{},
	})

	if params["approvalPolicy"] != "never" || params["sandbox"] != "read-only" ||
		params["ephemeral"] != true || params["persistExtendedHistory"] != false {
		t.Fatalf("connector-only thread policy = %#v", params)
	}
	config := params["config"].(map[string]any)
	if config["web_search"] != "disabled" || config["allow_login_shell"] != false {
		t.Fatalf("connector-only config = %#v", config)
	}
	features := config["features"].(map[string]any)
	for _, feature := range []string{
		"browser_use", "computer_use", "default_mode_request_user_input", "hooks",
		"memories", "multi_agent", "plugins", "remote_plugin", "shell_tool",
		"skill_search", "unified_exec", "view_image",
	} {
		if features[feature] != false {
			t.Fatalf("feature %q = %#v, want false in %#v", feature, features[feature], features)
		}
	}
	servers := config["mcp_servers"].(map[string]any)
	if len(servers) != 1 || servers["calendar"] == nil {
		t.Fatalf("MCP scope = %#v", servers)
	}
	apps := config["apps"].(map[string]any)
	if len(apps) != 1 || apps["_default"].(map[string]any)["enabled"] != false {
		t.Fatalf("app scope = %#v", apps)
	}
}

func TestCodexConnectorOnlyToolsAreDirectlyCallable(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		servers []string
		apps    []ConnectorApp
		want    []string
	}{
		{name: "page comments", servers: []string{"zenith-team"}, want: []string{"mcp__zenith-team"}},
		{name: "scheduled connectors", servers: []string{"calendar"}, apps: []ConnectorApp{{ID: "mail"}}, want: []string{"mcp__calendar", "mcp__mail"}},
		{name: "writing without tools", want: []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			params := map[string]any{}
			applyCodexConnectorOnlyThreadPolicy(params, ExecOptions{
				ToolPolicy:        ToolPolicyConnectorOnly,
				AllowedMCPServers: test.servers,
				ConnectorApps:     test.apps,
			})
			features := params["config"].(map[string]any)["features"].(map[string]any)
			mode := features["code_mode"].(map[string]any)
			if got := mode["direct_only_tool_namespaces"]; !reflect.DeepEqual(got, test.want) {
				t.Fatalf("direct namespaces = %v, want %v", got, test.want)
			}
			if mode["enabled"] != false || features["code_mode_host"] != false ||
				features["code_mode_only_strict_3p_tools"] != false {
				t.Fatalf("restricted tools must not require code-mode execution: %v", features)
			}
		})
	}
}

func TestApplyCodexConnectorOnlyTurnPolicyIsReadOnlyAndOffline(t *testing.T) {
	t.Parallel()

	params := map[string]any{"threadId": "thread-1"}
	applyCodexConnectorOnlyTurnPolicy(params, ExecOptions{ToolPolicy: ToolPolicyConnectorOnly})

	if !reflect.DeepEqual(params["sandboxPolicy"], map[string]any{
		"type": "readOnly", "networkAccess": false,
	}) {
		t.Fatalf("sandboxPolicy = %#v", params["sandboxPolicy"])
	}
	if roots, ok := params["runtimeWorkspaceRoots"].([]string); !ok || len(roots) != 0 {
		t.Fatalf("runtimeWorkspaceRoots = %#v", params["runtimeWorkspaceRoots"])
	}
}

// Inside the daemon's macOS sandbox Codex cannot start its own Seatbelt, so
// a connector-only launch asks for danger-full-access at both the thread and
// the turn; the daemon's profile fences writes instead.
func TestCodexConnectorOnlyPolicyInsideTheDaemonSandboxAsksForNoSeatbelt(t *testing.T) {
	t.Parallel()

	opts := ExecOptions{ToolPolicy: ToolPolicyConnectorOnly, Sandbox: &Sandbox{Profile: "(version 1)"}}
	thread := map[string]any{"sandbox": nil}
	applyCodexConnectorOnlyThreadPolicy(thread, opts)
	if thread["sandbox"] != "danger-full-access" {
		t.Fatalf("thread sandbox = %#v, want danger-full-access", thread["sandbox"])
	}
	features := thread["config"].(map[string]any)["features"].(map[string]any)
	if features["shell_tool"] != false || features["unified_exec"] != false {
		t.Fatalf("native tools came back with the sandbox: %#v", features)
	}
	turn := map[string]any{"threadId": "thread-1"}
	applyCodexConnectorOnlyTurnPolicy(turn, opts)
	if !reflect.DeepEqual(turn["sandboxPolicy"], map[string]any{"type": "dangerFullAccess"}) {
		t.Fatalf("turn sandboxPolicy = %#v, want dangerFullAccess", turn["sandboxPolicy"])
	}
}

func TestNormalizeConnectorExecutionOptionsClonesMCPConfig(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"mcpServers":{"calendar":{}}}`)
	want := string(raw)
	normalized := normalizeConnectorExecutionOptions(ExecOptions{McpConfig: raw})

	raw[0] = '['
	if got := string(normalized.McpConfig); got != want {
		t.Fatalf("normalized MCP config = %q, want immutable snapshot %q", got, want)
	}
}

func containsArg(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}

func TestValidateConnectorExecutionOptions(t *testing.T) {
	t.Parallel()

	pluginDir := filepath.Join(t.TempDir(), "selected-plugin")
	tools := []ConnectorTool{{Name: "search", Enabled: true, ReadOnly: true}}
	digest, err := ConnectorToolContractDigest(tools)
	if err != nil {
		t.Fatalf("ConnectorToolContractDigest() error = %v", err)
	}
	maximumMCPServers := make([]string, maxAllowedMCPServers)
	for index := range maximumMCPServers {
		maximumMCPServers[index] = fmt.Sprintf("connector-%d", index)
	}

	tests := []struct {
		name string
		opts ExecOptions
		want string
	}{
		{
			name: "valid exact connector scope",
			opts: ExecOptions{
				ConnectorApps: []ConnectorApp{{
					ID: "google_drive", Name: "Google Drive", ContractDigest: digest, Tools: tools,
				}},
				PluginDirectories: []string{pluginDir},
			},
		},
		{
			name: "invalid digest",
			opts: ExecOptions{ConnectorApps: []ConnectorApp{
				{
					ID:             "google_drive",
					Name:           "Google Drive",
					ContractDigest: strings.Repeat("0", 64),
					Tools:          tools,
				},
			}},
			want: "tool contract",
		},
		{
			name: "relative plugin directory",
			opts: ExecOptions{PluginDirectories: []string{"relative/plugin"}},
			want: "absolute",
		},
		{
			name: "duplicate plugin directory",
			opts: ExecOptions{PluginDirectories: []string{pluginDir, pluginDir}},
			want: "duplicate",
		},
		{
			name: "valid connector-only MCP scope",
			opts: ExecOptions{
				ToolPolicy:        ToolPolicyConnectorOnly,
				AllowedMCPServers: []string{"calendar"},
				McpConfig: json.RawMessage(
					`{"mcpServers":{"calendar":{"command":"calendar-connector"}}}`,
				),
			},
		},
		{
			name: "connector-only scope reserves one namespace for a product MCP server",
			opts: ExecOptions{
				ToolPolicy:        ToolPolicyConnectorOnly,
				AllowedMCPServers: maximumMCPServers,
			},
		},
		{
			name: "connector-only scope rejects too many MCP namespaces",
			opts: ExecOptions{
				ToolPolicy: ToolPolicyConnectorOnly,
				AllowedMCPServers: append(
					append([]string(nil), maximumMCPServers...),
					"one-too-many",
				),
			},
			want: "exceeds",
		},
		{
			name: "unsupported tool policy",
			opts: ExecOptions{ToolPolicy: ToolPolicy("unmanaged")},
			want: "tool policy",
		},
		{
			name: "connector-only cannot resume",
			opts: ExecOptions{
				ToolPolicy:      ToolPolicyConnectorOnly,
				ResumeSessionID: "prior",
			},
			want: "cannot resume",
		},
		{
			name: "MCP allowlist requires connector-only",
			opts: ExecOptions{AllowedMCPServers: []string{"calendar"}},
			want: "requires connector-only",
		},
		{
			name: "duplicate MCP namespace",
			opts: ExecOptions{
				ToolPolicy:        ToolPolicyConnectorOnly,
				AllowedMCPServers: []string{"calendar", "calendar"},
			},
			want: "duplicate",
		},
		{
			name: "unreviewed MCP config namespace",
			opts: ExecOptions{
				ToolPolicy:        ToolPolicyConnectorOnly,
				AllowedMCPServers: []string{"calendar"},
				McpConfig: json.RawMessage(
					`{"mcpServers":{"unreviewed":{"command":"unsafe"}}}`,
				),
			},
			want: "outside the connector-only allowlist",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateConnectorExecutionOptions(test.opts)
			if test.want == "" && err != nil {
				t.Fatalf("validateConnectorExecutionOptions() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf(
					"validateConnectorExecutionOptions() error = %v, want substring %q",
					err,
					test.want,
				)
			}
		})
	}
}
