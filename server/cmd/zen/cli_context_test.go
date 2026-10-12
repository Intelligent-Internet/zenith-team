package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/runchannel"
)

const (
	testRunID          = "0198aa00-0000-7000-8000-00000000f001"
	testRunAgentID     = "0198aa00-0000-7000-8000-00000000f002"
	testRunTaskID      = "0198aa00-0000-7000-8000-00000000f003"
	testRunConvID      = "0198aa00-0000-7000-8000-00000000f004"
	testRunProjectID   = "0198aa00-0000-7000-8000-00000000f0a1"
	testRunCapability  = "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXowMTIzNDU"
	testRunControlKind = "task"
)

// fakeRunChannel is the daemon's loopback plane as an Agent shell sees it:
// it checks the capability, records what the CLI asked for, and answers
// with the envelope the real daemon relays.
type fakeRunChannel struct {
	t        *testing.T
	server   *httptest.Server
	requests []runchannel.Request
	respond  func(runchannel.Request) (int, string)
}

func newFakeRunChannel(t *testing.T, respond func(runchannel.Request) (int, string)) *fakeRunChannel {
	t.Helper()
	channel := &fakeRunChannel{t: t, respond: respond}
	channel.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != runchannel.Path || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != runchannel.AuthorizationScheme+" "+testRunCapability {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"schemaVersion":1,"code":"unauthorized","reason":"Run capability is invalid or the Run has ended."}`))
			return
		}
		var request runchannel.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode channel request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		channel.requests = append(channel.requests, request)
		status, body := channel.respond(request)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(runchannel.Response{
			SchemaVersion: 1, Status: status, Body: json.RawMessage(body),
		})
	}))
	t.Cleanup(channel.server.Close)
	return channel
}

// seedAgentShell makes the test process look like a provider shell the
// daemon launched: an isolated BOARDLY_HOME holding run.json and nothing
// else, plus the Run environment variables.
func seedAgentShell(t *testing.T, channel *fakeRunChannel, taskID string, commands []string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BOARDLY_HOME", home)
	t.Setenv("BOARDLY_RUN_ID", testRunID)
	t.Setenv("BOARDLY_RUN_TOKEN", "brt_"+strings.Repeat("ab", 32))
	control := runchannel.RunControlConfig{
		SchemaVersion: 1, Endpoint: channel.server.URL, Capability: testRunCapability,
		RunID: testRunID, CompanyID: testCompanyID, AgentID: testRunAgentID, AgentName: "Scout",
		TaskID: taskID, ConversationID: testRunConvID, ConversationKind: "direct",
		Commands: commands,
	}
	// A Task Run names its Task and the Project the Task lives in.
	if taskID != "" {
		control.ConversationKind = testRunControlKind
		control.TaskKey = "ENG-7"
		control.ProjectID, control.ProjectKey = testRunProjectID, "work"
	}
	encoded, err := json.Marshal(control)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "run.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOperatorShellIsOperatorMode(t *testing.T) {
	t.Setenv("BOARDLY_HOME", t.TempDir())
	t.Setenv("BOARDLY_RUN_ID", "")
	t.Setenv("BOARDLY_RUN_TOKEN", "")
	if context := resolveCommandContext(); context.mode != modeOperator {
		t.Fatalf("mode = %v, want operator", context.mode)
	}
	if _, err := executeCLI(t, newRootCmd, "run", "context"); err == nil ||
		!strings.Contains(err.Error(), "not inside a Zenith Team Run") {
		t.Fatalf("run context outside a Run = %v", err)
	}
}

func TestAgentShellNeverFallsBackToOperatorCredentials(t *testing.T) {
	// A Run environment without run.json is a broken channel, not an
	// invitation to read the operator's session.
	seedSession(t, "http://127.0.0.1:1")
	t.Setenv("BOARDLY_RUN_ID", testRunID)
	t.Setenv("BOARDLY_RUN_TOKEN", "brt_"+strings.Repeat("ab", 32))
	context := resolveCommandContext()
	if context.mode != modeAgent || context.channelErr == nil {
		t.Fatalf("context = %+v, want agent mode with a channel error", context)
	}
	_, err := executeCLI(t, newRootCmd, "task", "list")
	if err == nil || !strings.Contains(err.Error(), "Run channel is unavailable") {
		t.Fatalf("task list in a broken agent shell = %v", err)
	}
	if code := exitCodeFor(err); code != exitAuth {
		t.Fatalf("exit code = %d, want %d", code, exitAuth)
	}
}

func TestAgentModeRefusesOperatorCommandsAndForeignCompanies(t *testing.T) {
	channel := newFakeRunChannel(t, func(runchannel.Request) (int, string) { return 200, `{}` })
	seedAgentShell(t, channel, testRunTaskID, []string{"zen task get"})
	for _, args := range [][]string{
		{"auth", "login"},
		{"auth", "logout"},
		{"daemon", "start"},
		{"daemon", "stop"},
		{"config", "set", "company_id", testCompanyID},
		{"company", "use", testCompanyID},
	} {
		_, err := executeCLI(t, newRootCmd, args...)
		if err == nil || !strings.Contains(err.Error(), "not available inside a Zenith Team Run") {
			t.Fatalf("%v inside a Run = %v, want an operator-only refusal", args, err)
		}
		if code := exitCodeFor(err); code != exitAuth {
			t.Fatalf("%v exit code = %d, want %d", args, code, exitAuth)
		}
	}
	other := uuid.NewString()
	if _, err := executeCLI(t, newRootCmd, "task", "get", "--company", other); err == nil ||
		!strings.Contains(err.Error(), "bound to company "+testCompanyID) {
		t.Fatalf("--company in agent mode = %v, want a binding refusal", err)
	}
	if len(channel.requests) != 0 {
		t.Fatalf("channel saw %d requests from refused commands", len(channel.requests))
	}
}

func TestRunContextPrintsTheDaemonWrittenIdentityWithoutNetwork(t *testing.T) {
	channel := newFakeRunChannel(t, func(runchannel.Request) (int, string) {
		t.Fatal("run context must not use the channel")
		return 0, ""
	})
	seedAgentShell(t, channel, testRunTaskID, []string{"zen task get", "zen question ask"})
	out, err := executeCLI(t, newRootCmd, "run", "context")
	if err != nil {
		t.Fatalf("run context: %v", err)
	}
	var printed struct {
		Mode             string   `json:"mode"`
		RunID            string   `json:"runId"`
		TaskKey          string   `json:"taskKey"`
		ProjectID        string   `json:"projectId"`
		ProjectKey       string   `json:"projectKey"`
		ConversationKind string   `json:"conversationKind"`
		Commands         []string `json:"commands"`
	}
	if err := json.Unmarshal([]byte(out), &printed); err != nil {
		t.Fatalf("run context printed %q: %v", out, err)
	}
	if printed.Mode != "agent" || printed.RunID != testRunID || printed.TaskKey != "ENG-7" ||
		printed.ProjectID != testRunProjectID || printed.ProjectKey != "work" ||
		printed.ConversationKind != "task" || len(printed.Commands) != 2 {
		t.Fatalf("run context = %+v", printed)
	}
	table, err := executeCLI(t, newRootCmd, "run", "context", "--output", "table")
	if err != nil || !strings.Contains(table, "zen question ask") || !strings.Contains(table, "Scout") ||
		!strings.Contains(table, "work "+testRunProjectID) {
		t.Fatalf("table form = %q (err %v)", table, err)
	}
}

func TestExitCodesFollowTheServerVerdict(t *testing.T) {
	for _, test := range []struct {
		err  error
		want int
	}{
		{&apiError{Status: 401}, exitAuth},
		{&apiError{Status: 403, Code: "run_scope_denied"}, exitAuth},
		{&apiError{Status: 404}, exitNotFound},
		{&apiError{Status: 409}, exitConflict},
		{&apiError{Status: 422}, exitValidation},
		{&apiError{Status: 0, Code: "server_unreachable"}, exitNetwork},
		{&apiError{Status: 500}, exitGeneric},
		{&networkError{Method: "GET", Path: "/x"}, exitNetwork},
		{usagef("bad flag"), exitValidation},
		{&modeError{msg: "no"}, exitAuth},
	} {
		if got := exitCodeFor(test.err); got != test.want {
			t.Fatalf("exitCodeFor(%v) = %d, want %d", test.err, got, test.want)
		}
	}
}
