//go:build unix

package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCodexThreadStartTimeoutReapsDetachedStdioDescendant(t *testing.T) {
	tempDir := t.TempDir()
	pidFile := filepath.Join(tempDir, "descendant.pid")
	fakePath := writeFakeCodexAppServer(t, ""+
		`read line`+"\n"+
		`echo '{"jsonrpc":"2.0","id":1,"result":{}}'`+"\n"+
		`read line`+"\n"+
		`read line`+"\n"+
		`sleep 30 >/dev/null 2>&1 & echo $! > "`+pidFile+`"`+"\n"+
		`sleep 3.2`+"\n"+
		`echo '{"jsonrpc":"2.0","id":2,"result":{"thread":{"id":"thr-late"}}}'`+"\n"+
		`read line`+"\n")

	var logs strings.Builder
	provider := mustNewProvider(
		t,
		"codex",
		Config{ExecutablePath: fakePath, Logger: slog.New(slog.NewJSONHandler(&logs, nil))},
	)
	session, err := provider.Execute(
		context.Background(),
		"prompt",
		ExecOptions{Timeout: providerFixtureExecTimeout, HandshakeTimeout: 3 * time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := <-executionResults(session)
	if result.Status != "failed" {
		t.Fatalf("expected thread/start failure, got %+v", result)
	}
	rawPID, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil {
		t.Fatal(err)
	}
	waitProcessGone(t, pid)
	failure := findCodexLifecyclePhase(t, parseJSONLogEntries(t, logs.String()), "thread_start_failure")
	if failure["reaped"] != true || failure["cleanup_confirmed"] != true {
		t.Fatalf("process-tree cleanup not confirmed: %v", failure)
	}
}

func TestCodexInitializeTimeoutReapsDetachedStdioDescendant(t *testing.T) {
	tempDir := t.TempDir()
	pidFile := filepath.Join(tempDir, "descendant.pid")
	fakePath := writeFakeCodexAppServer(t, ""+
		`read line`+"\n"+
		`sleep 30 >/dev/null 2>&1 & echo $! > "`+pidFile+`"`+"\n"+
		`sleep 3.2`+"\n"+
		`echo '{"jsonrpc":"2.0","id":1,"result":{}}'`+"\n"+
		`read line`+"\n")

	var logs strings.Builder
	providerRaw := mustNewProvider(
		t,
		"codex",
		Config{ExecutablePath: fakePath, Logger: slog.New(slog.NewJSONHandler(&logs, nil))},
	)
	provider := providerRaw.(*codexProvider)
	session, err := provider.executeOnce(
		context.Background(),
		"prompt",
		ExecOptions{Timeout: providerFixtureExecTimeout, HandshakeTimeout: 3 * time.Second},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	result := <-session.result
	if result.Status != "failed" || !strings.Contains(result.Error, "initialize") {
		t.Fatalf("expected initialize timeout failure, got %+v", result)
	}
	rawPID, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil {
		t.Fatal(err)
	}
	waitProcessGone(t, pid)
	failure := findCodexLifecyclePhase(t, parseJSONLogEntries(t, logs.String()), "initialize_failure")
	if failure["cleanup_confirmed"] != true || failure["retry_safe"] != true {
		t.Fatalf("process-tree cleanup/retry gate not confirmed: %v", failure)
	}
}

func TestCodexInitializeTimeoutDoesNotPersistOpaqueEnv(t *testing.T) {
	const secret = "opaque-init-auth-sentinel-7319"
	fakePath := writeFakeCodexAppServer(t, ""+
		`read line`+"\n"+
		`echo "$OPAQUE_AUTH_VALUE" >&2`+"\n"+
		`sleep 3.2`+"\n"+
		`echo '{"jsonrpc":"2.0","id":1,"result":{}}'`+"\n")

	var logs strings.Builder
	providerRaw := mustNewProvider(t, "codex", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"OPAQUE_AUTH_VALUE": secret},
		Logger:         slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	provider := providerRaw.(*codexProvider)
	session, err := provider.executeOnce(
		context.Background(),
		"prompt",
		ExecOptions{Timeout: providerFixtureExecTimeout, HandshakeTimeout: 3 * time.Second},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	result := <-session.result
	if result.Status != "failed" || !strings.Contains(result.Error, CodexHandshakeTimeoutMarker) {
		t.Fatalf("expected initialize timeout failure, got %+v", result)
	}
	if strings.Contains(result.Error, secret) {
		t.Fatalf("opaque env persisted in Result.Error: %q", result.Error)
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("opaque env persisted in lifecycle logs: %s", logs.String())
	}
	failure := findCodexLifecyclePhase(t, parseJSONLogEntries(t, logs.String()), "initialize_failure")
	if failure["cleanup_confirmed"] != true || failure["retry_safe"] != true {
		t.Fatalf("cleanup/retry gate changed: %v", failure)
	}
}

func TestCodexInitializeParentContextDoesNotPersistOpaqueEnv(t *testing.T) {
	for _, tc := range []struct {
		name      string
		newCtx    func() (context.Context, context.CancelFunc)
		cancelNow bool
	}{
		{
			name: "deadline before handshake timeout",
			newCtx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 2*time.Second)
			},
		},
		{
			name: "parent cancellation",
			newCtx: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			cancelNow: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const secret = "opaque-init-parent-context-sentinel-8841"
			readyFile := filepath.Join(t.TempDir(), "stderr-written")
			fakePath := writeFakeCodexAppServer(t, ""+
				`read line`+"\n"+
				`echo "$OPAQUE_AUTH_VALUE" >&2`+"\n"+
				`touch "`+readyFile+`"`+"\n"+
				`sleep 5`+"\n")

			var logs strings.Builder
			provider := mustNewProvider(t, "codex", Config{
				ExecutablePath: fakePath,
				Env:            map[string]string{"OPAQUE_AUTH_VALUE": secret},
				Logger:         slog.New(slog.NewJSONHandler(&logs, nil)),
			})
			ctx, cancel := tc.newCtx()
			defer cancel()
			session, err := provider.Execute(
				ctx,
				"prompt",
				ExecOptions{Timeout: providerFixtureExecTimeout, HandshakeTimeout: 4 * time.Second},
			)
			if err != nil {
				t.Fatal(err)
			}
			results := executionResults(session)
			if tc.cancelNow {
				deadline := time.Now().Add(3 * time.Second)
				for {
					if _, err := os.Stat(readyFile); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("fake app-server did not write stderr before cancellation")
					}
					time.Sleep(20 * time.Millisecond)
				}
				cancel()
			}
			result := <-results
			persisted := fmt.Sprintf("%+v\n%s", result, logs.String())
			if strings.Contains(persisted, secret) {
				t.Fatalf("opaque env persisted after parent context ended: %s", persisted)
			}
			if result.Status != "failed" || result.codexInitializeRetrySafe {
				t.Fatalf("parent context must fail without initialize retry: %+v", result)
			}
		})
	}
}
