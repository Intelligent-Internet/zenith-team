package agent

import (
	"encoding/json"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestClaudeExecuteRequiresPluginVerification(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	for _, tc := range []struct {
		name       string
		init       string
		wantStatus ResultStatus
	}{
		{name: "missing init", wantStatus: "failed"},
		{name: "verified init", init: `{"type":"system","subtype":"init","plugins":[]}`, wantStatus: "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fakePath := filepath.Join(t.TempDir(), "claude")
			script := "#!/bin/sh\nIFS= read -r _\n"
			if tc.init != "" {
				script += "echo '" + tc.init + "'\n"
			}
			script += `echo '{"type":"result","subtype":"success","session_id":"session-1","result":"answer"}'` + "\n"
			writeTestExecutable(t, fakePath, []byte(script))
			provider := mustNewProvider(t, "claude", Config{
				ExecutablePath: fakePath,
				Env:            map[string]string{"IS_SANDBOX": "1"},
				Logger:         slog.Default(),
			})
			execution, err := provider.Execute(t.Context(), "prompt", ExecOptions{
				Timeout:         providerFixtureExecTimeout,
				DisabledPlugins: []string{"design@local"},
			})
			if err != nil {
				t.Fatal(err)
			}
			result, _ := collectExecution(t, execution, providerFixtureWaitTimeout)
			if result.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q; error = %q", result.Status, tc.wantStatus, result.Error)
			}
			if tc.wantStatus == "failed" && !strings.Contains(result.Error, "plugin") {
				t.Fatalf("missing plugin verification diagnostic: %q", result.Error)
			}
		})
	}
}

func TestCodexPluginPolicyRejectsAnEnabledProviderOverride(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		fail bool
	}{
		{`{"marketplaces":[{"plugins":[{"id":"design@local","enabled":false}]}]}`, false},
		{`{"marketplaces":[{"plugins":[{"id":"design@local","enabled":true}]}]}`, true},
		{`{"marketplaces":[]}`, false},
		{`{}`, true},
		{`{"marketplaces":[],"marketplaceLoadErrors":[{}]}`, true},
	} {
		if err := checkCodexDisabledPlugins(json.RawMessage(tc.raw), []string{"design@local"}); (err != nil) != tc.fail {
			t.Errorf("check %s = %v", tc.raw, err)
		}
	}
}

func TestClaudePluginPolicyRejectsStillLoadedPlugins(t *testing.T) {
	if err := checkClaudeDisabledPlugins(json.RawMessage(`[{"name":"design"}]`), []string{"design@local"}); err == nil {
		t.Fatal("accepted loaded disabled plugin")
	}
	if err := checkClaudeDisabledPlugins(json.RawMessage(`[]`), []string{"design@local"}); err != nil {
		t.Fatal(err)
	}
}

func TestCodexAgentPluginDenialsOverrideCallerConfig(t *testing.T) {
	args := buildCodexArgs(ExecOptions{DisabledPlugins: []string{"design@local"}, ExtraArgs: []string{"-c", `plugins."design@local".enabled=true`}}, nil)
	if len(args) < 2 || args[len(args)-2] != "-c" || args[len(args)-1] != `plugins."design@local".enabled=false` {
		t.Fatalf("args = %v", args)
	}
	for _, part := range args {
		if strings.Contains(part, "\n") {
			t.Fatalf("unexpected argv newline: %q", part)
		}
	}
	scheduled := buildCodexArgs(ExecOptions{ToolPolicy: ToolPolicyConnectorOnly, DisabledPlugins: []string{"design@local"}}, nil)
	if len(scheduled) != 3 {
		t.Fatalf("scheduled arguments = %v", scheduled)
	}
}
