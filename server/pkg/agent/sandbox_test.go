package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestSandboxCommandWrapsTheProviderInSandboxExec(t *testing.T) {
	t.Parallel()

	var none *Sandbox
	name, args := none.Command("/opt/bin/codex", []string{"app-server"})
	if name != "/opt/bin/codex" || !reflect.DeepEqual(args, []string{"app-server"}) {
		t.Fatalf("no sandbox = %q %q, want the provider launched directly", name, args)
	}

	sandbox := &Sandbox{
		Profile: "(version 1)\n(allow default)",
		Params: map[string]string{
			"PROTECTED_1": "/Users/olivia/Library/Application Support/Zenith Team/boardly-home",
			"PROTECTED_0": "/Users/olivia/.boardly",
		},
	}
	name, args = sandbox.Command("/opt/bin/codex", []string{"app-server", "--listen", "stdio://"})
	want := []string{
		"-D", "PROTECTED_0=/Users/olivia/.boardly",
		"-D", "PROTECTED_1=/Users/olivia/Library/Application Support/Zenith Team/boardly-home",
		"-p", "(version 1)\n(allow default)",
		"--", "/opt/bin/codex", "app-server", "--listen", "stdio://",
	}
	if name != "/usr/bin/sandbox-exec" || !reflect.DeepEqual(args, want) {
		t.Fatalf("sandboxed command = %q %q, want /usr/bin/sandbox-exec %q", name, args, want)
	}
}

// A sandboxed Claude launch goes through sandbox-exec with the profile and
// still hands the provider its own argv.
func TestClaudeLaunchRunsUnderTheSandbox(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS only")
	}

	dir := t.TempDir()
	secret := filepath.Join(dir, "home", "daemon-auth.json")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("refresh-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakePath := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		"if cat \"$SECRET\" >/dev/null 2>&1; then text=leaked; else text=denied; fi\n" +
		`echo '{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"'$text'"}]}}'` + "\n" +
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"'$text'","session_id":"s1"}'` + "\n"
	writeTestExecutable(t, fakePath, []byte(script))

	resolvedHome, err := filepath.EvalSymlinks(filepath.Dir(secret))
	if err != nil {
		t.Fatal(err)
	}
	provider := mustNewProvider(t, "claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"SECRET": secret},
	})
	session, err := provider.Execute(t.Context(), "prompt", ExecOptions{
		Timeout: providerFixtureExecTimeout,
		Sandbox: &Sandbox{
			Profile: "(version 1)\n(allow default)\n(deny file-read* file-write* (subpath (param \"HOME_0\")))",
			Params:  map[string]string{"HOME_0": resolvedHome},
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	result := <-executionResults(session)
	if result.Status != ResultCompleted || strings.TrimSpace(result.Output) != "denied" {
		t.Fatalf(
			"result = %q %q (error %q), want the sandbox to deny the read",
			result.Status,
			result.Output,
			result.Error,
		)
	}
}
