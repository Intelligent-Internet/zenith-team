package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/mcp/apiclient"
	"boardly.dev/boardly/server/internal/mcp/tools"
	"boardly.dev/boardly/server/internal/runchannel"
)

const (
	testChannelCompany      = "0198aa00-0000-7000-8000-0000000000c0"
	testChannelAgent        = "0198aa00-0000-7000-8000-0000000000a0"
	testChannelConversation = "0198aa00-0000-7000-8000-0000000000d0"
	testChannelTask         = "0198aa00-0000-7000-8000-0000000000e0"
	testChannelProject      = "0198aa00-0000-7000-8000-0000000000f0"
	testChannelRunToken     = "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testChannelArtifact     = "0198aa00-0000-7000-8000-00000000a001"
	testChannelArtifactBody = "report"
)

// testChannelArtifactManifest is the saved Artifact mcpChannelUpstream
// serves on the Run-proven agent-artifacts route.
var testChannelArtifactManifest = tools.ArtifactManifest{
	ID:   testChannelArtifact,
	Type: "file",
	Items: []tools.ArtifactItem{{
		ID:             "0198aa00-0000-7000-8000-00000000a002",
		AssetVersionID: "0198aa00-0000-7000-8000-00000000a003",
		Role:           "primary",
		DisplayName:    "report.txt",
		SizeBytes:      int64(len(testChannelArtifactBody)),
	}},
}

func channelTestRun(t *testing.T, task bool) Run {
	t.Helper()
	run := Run{
		ID:             uuid.New(),
		CompanyID:      testChannelCompany,
		AgentID:        testChannelAgent,
		ConversationID: testChannelConversation,
		AuthToken:      testChannelRunToken,
	}
	if task {
		run.TaskID = testChannelTask
	}
	return run
}

func taskChannelDocument() RunContext {
	return RunContext{
		ExecutionProfile: RunExecutionProfileContext{
			Task:    &RunTaskContext{Key: "ENG-7", Title: "Ship it"},
			Project: &RunProjectContext{ID: testChannelProject, Key: "booking", Title: "Booking"},
		},
	}
}

// channelRefusal returns the daemon's own refusal that err carries.
func channelRefusal(err error) (*runchannel.Refusal, bool) {
	var refusal *runchannel.Refusal
	ok := errors.As(err, &refusal)
	return refusal, ok
}

// allows reports whether any row admits the call, forwarded or completed by
// the daemon itself.
func (p runCLIPolicy) allows(method, path string) bool {
	_, forwarded := p.admit(method, path, false)
	_, local := p.admit(method, path, true)
	return forwarded || local
}

func TestRunCLIPolicyFollowsTheFrozenExecutionProfile(t *testing.T) {
	t.Parallel()
	taskRun := channelTestRun(t, true)
	taskPolicy := runCLIPolicyFor(runCLICommands(taskRun, taskChannelDocument()))
	company := "/companies/" + testChannelCompany
	// A Task Run reads Tasks one at a time by name, its own or another; the
	// server refuses any Task that is not the Run's own
	// (task_outside_run_scope), so a Task Run hears the same refusal as any
	// other Run instead of the daemon's bare run_scope_denied.
	otherTask := uuid.NewString()
	for _, allowed := range [][2]string{
		{http.MethodGet, company + "/tasks/" + testChannelTask},
		{http.MethodGet, company + "/tasks/" + testChannelTask + "/dependencies"},
		{http.MethodGet, company + "/tasks/" + otherTask},
		{http.MethodGet, company + "/tasks/" + otherTask + "/dependencies"},
		{http.MethodGet, "/conversations/" + testChannelConversation + "/messages"},
		{http.MethodGet, company + "/runs/" + taskRun.ID.String()},
		{http.MethodPost, company + "/questions"},
		{http.MethodPost, company + "/task-failures"},
		{http.MethodPost, company + "/conversations/" + testChannelConversation + "/mail-draft-groups"},
		{http.MethodPost, "/runs/" + taskRun.ID.String() + "/output"},
	} {
		if !taskPolicy.allows(allowed[0], allowed[1]) {
			t.Fatalf("task Run policy refused %s %s", allowed[0], allowed[1])
		}
	}
	for _, denied := range [][2]string{
		{http.MethodGet, company + "/tasks"},
		{http.MethodGet, company + "/tasks/" + testChannelTask + "/workspace"},
		{http.MethodGet, company + "/tasks/" + otherTask + "/results"},
		{http.MethodGet, company + "/projects"},
		{http.MethodGet, "/conversations/" + uuid.NewString() + "/messages"},
		{http.MethodGet, company + "/conversations/" + testChannelConversation + "/messages"},
		{http.MethodGet, company + "/usage"},
		{http.MethodPost, company + "/company-plan/apply"},
		{http.MethodPatch, company + "/tasks/" + testChannelTask},
		{http.MethodGet, "/companies"},
		{http.MethodGet, "/companies/" + uuid.NewString()},
		{http.MethodDelete, company + "/tasks/" + testChannelTask},
		{http.MethodPost, "/runs/" + uuid.NewString() + "/output"},
	} {
		if taskPolicy.allows(denied[0], denied[1]) {
			t.Fatalf("task Run policy admitted %s %s", denied[0], denied[1])
		}
	}

	// A CEO direct Conversation plans the Company: it reads every Task and
	// the Agent roster, but has no Task of its own to fail.
	ceoRun := channelTestRun(t, false)
	ceoPolicy := runCLIPolicyFor(runCLICommands(ceoRun, RunContext{Agent: RunAgentContext{IsRoot: true}}))
	for _, allowed := range [][2]string{
		{http.MethodGet, company + "/tasks"},
		{http.MethodGet, company + "/tasks/" + uuid.NewString() + "/dependencies"},
		{http.MethodGet, company + "/agents"},
		{http.MethodGet, company + "/agents/" + uuid.NewString()},
		{http.MethodPost, company + "/questions"},
		{http.MethodPost, "/runs/" + ceoRun.ID.String() + "/output"},
	} {
		if !ceoPolicy.allows(allowed[0], allowed[1]) {
			t.Fatalf("CEO Run policy refused %s %s", allowed[0], allowed[1])
		}
	}
	if ceoPolicy.allows(http.MethodPost, company+"/task-failures") {
		t.Fatal("a direct Conversation Run must not be able to fail a Task")
	}

	// Every other Run outside a Task reads the Projects and its own Tasks
	// over the CLI — the server scopes the Task reads — though only the root
	// Agent is offered the Company Plan export. Switching Company planning
	// off takes the Project reads and the Task list with it, but not a Task
	// read by name: its refusal must still be the server's sentence.
	for name, doc := range map[string]RunContext{
		"member direct":      {},
		"channel invocation": {ChannelInvocation: &RunChannelInvocation{ID: uuid.NewString()}},
	} {
		policy := runCLIPolicyFor(runCLICommands(ceoRun, doc))
		for _, allowed := range [][2]string{
			{http.MethodGet, company + "/projects"},
			{http.MethodGet, company + "/tasks"},
			{http.MethodGet, company + "/tasks/" + otherTask},
			{http.MethodGet, company + "/tasks/" + otherTask + "/dependencies"},
		} {
			if !policy.allows(allowed[0], allowed[1]) {
				t.Fatalf("%s Run policy refused %s %s", name, allowed[0], allowed[1])
			}
		}
		if slices.Contains(boardlyMCPToolsForRun(doc), "company_plan_export") {
			t.Fatalf("%s Run was offered the unscoped Company Plan export", name)
		}
		doc.Agent.Capabilities.DisabledBuiltIns = []string{"planning"}
		planningOff := runCLIPolicyFor(runCLICommands(ceoRun, doc))
		for _, denied := range []string{company + "/tasks", company + "/projects"} {
			if planningOff.allows(http.MethodGet, denied) {
				t.Fatalf("%s Run with Company planning off still reads GET %s", name, denied)
			}
		}
		for _, allowed := range []string{company + "/tasks/" + otherTask, company + "/tasks/BSC-8/dependencies"} {
			if !planningOff.allows(http.MethodGet, allowed) {
				t.Fatalf("%s Run with Company planning off refused GET %s", name, allowed)
			}
		}
	}

	scheduled := runCLICommands(
		ceoRun,
		RunContext{ExecutionProfile: RunExecutionProfileContext{Source: runSourceSchedule}},
	)
	if len(scheduled) != 0 {
		t.Fatalf("scheduled Run commands = %v, want no CLI channel", scheduled)
	}
}

// Agent-mode `zen task create` files work for the member the Run works for
// (ADR 0014 D6). The channel admits the create and the one Project lookup it
// needs: a Task Run names its own Project, a non-root direct or channel Run
// the Projects its planning reads show. The root Agent outside a Task is not
// offered it. /me stays closed — the server makes the daemon's member the
// assignee — and so does every other Task or Project write.
func TestRunCLIPolicyAdmitsTaskCreationForTheRunsMember(t *testing.T) {
	t.Parallel()
	company := "/companies/" + testChannelCompany
	create := [2]string{http.MethodPost, company + "/tasks"}
	ownProject := [2]string{http.MethodGet, company + "/projects/" + testChannelProject}
	me := [2]string{http.MethodGet, "/me"}

	taskRun := channelTestRun(t, true)
	taskCommands := runCLICommands(taskRun, taskChannelDocument())
	taskPolicy := runCLIPolicyFor(taskCommands)
	for _, allowed := range [][2]string{create, ownProject} {
		if !taskPolicy.allows(allowed[0], allowed[1]) {
			t.Fatalf("task Run policy refused %s %s", allowed[0], allowed[1])
		}
	}
	for _, denied := range [][2]string{
		me,
		{http.MethodGet, company + "/projects"},
		{http.MethodGet, company + "/projects/" + uuid.NewString()},
		{http.MethodPost, company + "/projects"},
		{http.MethodPatch, company + "/projects/" + testChannelProject},
		{http.MethodPatch, company + "/tasks/" + testChannelTask},
		{http.MethodPost, company + "/tasks/" + testChannelTask + "/runs"},
	} {
		if taskPolicy.allows(denied[0], denied[1]) {
			t.Fatalf("task Run policy admitted %s %s", denied[0], denied[1])
		}
	}
	creates := 0
	for _, use := range runCLICommandUses(taskCommands) {
		if use == "zen task create" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("task Run advertises zen task create %d times, want once", creates)
	}

	// Switching work coordination off takes task creation with it.
	coordinationOff := taskChannelDocument()
	coordinationOff.Agent.Capabilities.DisabledBuiltIns = []string{"coordination"}
	offPolicy := runCLIPolicyFor(runCLICommands(taskRun, coordinationOff))
	for _, denied := range [][2]string{create, ownProject} {
		if offPolicy.allows(denied[0], denied[1]) {
			t.Fatalf("coordination-off Task Run admitted %s %s", denied[0], denied[1])
		}
	}

	// A frozen Project id that is not a UUID is no Project at all: an empty
	// or "*" id substituted into the lookup would admit every Project.
	for _, id := range []string{"", "*"} {
		widened := taskChannelDocument()
		widened.ExecutionProfile.Project.ID = id
		commands := runCLICommands(taskRun, widened)
		policy := runCLIPolicyFor(commands)
		for _, denied := range [][2]string{
			create,
			{http.MethodGet, company + "/projects/" + uuid.NewString()},
			{http.MethodGet, company + "/projects/" + id},
		} {
			if policy.allows(denied[0], denied[1]) {
				t.Fatalf("Task Run with Project id %q admitted %s %s", id, denied[0], denied[1])
			}
		}
		if slices.Contains(runCLICommandUses(commands), "zen task create") {
			t.Fatalf("Task Run with Project id %q advertises zen task create", id)
		}
	}

	// The root Agent doing a Task of its own files follow-up work like any
	// other Task Run.
	rootTask := taskChannelDocument()
	rootTask.Agent.IsRoot = true
	rootTaskPolicy := runCLIPolicyFor(runCLICommands(taskRun, rootTask))
	for _, allowed := range [][2]string{create, ownProject} {
		if !rootTaskPolicy.allows(allowed[0], allowed[1]) {
			t.Fatalf("root Agent Task Run refused %s %s", allowed[0], allowed[1])
		}
	}

	directRun := channelTestRun(t, false)
	for name, doc := range map[string]RunContext{
		"member direct":      {},
		"channel invocation": {ChannelInvocation: &RunChannelInvocation{ID: uuid.NewString()}},
	} {
		policy := runCLIPolicyFor(runCLICommands(directRun, doc))
		if !policy.allows(create[0], create[1]) || !policy.allows(http.MethodGet, company+"/projects") {
			t.Fatalf("%s Run cannot create a Task in a Project it can read", name)
		}
		if policy.allows(me[0], me[1]) {
			t.Fatalf("%s Run admitted GET /me", name)
		}
	}
	// CEO chats can schedule explicitly requested Tasks, but their create
	// operation cannot start immediate work outside Company Plan review.
	for name, doc := range map[string]RunContext{
		"CEO direct": {Agent: RunAgentContext{IsRoot: true}},
		"CEO channel invocation": {
			Agent:             RunAgentContext{IsRoot: true},
			ChannelInvocation: &RunChannelInvocation{ID: uuid.NewString()},
		},
	} {
		commands := runCLICommands(directRun, doc)
		policy := runCLIPolicyFor(commands)
		operation, admitted := policy.admit(create[0], create[1], false)
		if !admitted || !operation.ScheduledTaskOnly || policy.allows(me[0], me[1]) {
			t.Fatalf("%s Run lacks schedule-only Task creation or admits GET /me", name)
		}
		if !slices.Contains(runCLICommandUses(commands), "zen task create") {
			t.Fatalf("%s Run omits zen task create", name)
		}
		if !policy.allows(http.MethodGet, company+"/projects") {
			t.Fatalf("%s Run lost its planning reads", name)
		}
	}
	// The platform-authored onboarding research Run never creates user work.
	research := RunContext{Agent: RunAgentContext{IsRoot: true}}
	research.ExecutionProfile.Source = runSourceCompanyResearch
	if runCLIPolicyFor(runCLICommands(directRun, research)).allows(create[0], create[1]) {
		t.Fatal("company research was offered task creation")
	}
	// Without the planning reads a direct Run has no Project to name.
	planningOff := RunContext{
		Agent: RunAgentContext{Capabilities: RunCapabilitySettings{DisabledBuiltIns: []string{"planning"}}},
	}
	if runCLIPolicyFor(runCLICommands(directRun, planningOff)).allows(create[0], create[1]) {
		t.Fatal("a direct Run that reads no Project was offered task creation")
	}
}

func TestRunCLIPatternMatchesOneSegmentPerWildcard(t *testing.T) {
	t.Parallel()
	if !runCLIPatternMatches("/companies/c1/tasks/*", "/companies/c1/tasks/t1") {
		t.Fatal("wildcard did not match one segment")
	}
	for _, path := range []string{
		"/companies/c1/tasks",
		"/companies/c1/tasks/t1/dependencies",
		"/companies/c1/tasks/",
		"/companies/c2/tasks/t1",
	} {
		if runCLIPatternMatches("/companies/c1/tasks/*", path) {
			t.Fatalf("pattern matched %q", path)
		}
	}
}

func TestRunControlConfigRoundTripsThroughTheIsolatedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BOARDLY_HOME", home)
	if _, err := runchannel.LoadRunControlConfig(home); !runchannel.IsRunControlUnavailable(err) {
		t.Fatalf("operator home reported %v, want run control unavailable", err)
	}
	capability, err := newLocalCapability()
	if err != nil {
		t.Fatal(err)
	}
	written := runchannel.RunControlConfig{
		SchemaVersion: 1, Endpoint: "http://127.0.0.1:19713", Capability: capability,
		RunID: uuid.NewString(), CompanyID: testChannelCompany, AgentID: testChannelAgent,
		AgentName: "Andrew", TaskID: testChannelTask, TaskKey: "ENG-7",
		ProjectID: testChannelProject, ProjectKey: "booking",
		ConversationID: testChannelConversation, ConversationKind: "task",
		Commands: []string{"zen task get"},
	}
	path, err := writeRunControlConfig(home, written)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != runchannel.ControlFileName {
		t.Fatalf("wrote %s, want %s", path, runchannel.ControlFileName)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("run.json mode = %v (err %v), want private", info.Mode(), err)
	}
	loaded, err := runchannel.LoadRunControlConfig(home)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.RunID != written.RunID || loaded.Capability != capability || loaded.TaskKey != "ENG-7" ||
		loaded.ProjectID != testChannelProject || loaded.ProjectKey != "booking" ||
		loaded.ConversationKind != "task" || len(loaded.Commands) != 1 {
		t.Fatalf("loaded %+v, want %+v", loaded, written)
	}
	original := mustReadFile(t, path)
	// A tampered kind or Project id is refused rather than defaulted.
	for _, tampered := range []string{
		strings.Replace(original, `"conversationKind": "task"`, `"conversationKind": "company"`, 1),
		strings.Replace(original, `"projectId": "`+testChannelProject+`"`, `"projectId": "booking"`, 1),
	} {
		if tampered == original {
			t.Fatal("the tampering did not change run.json")
		}
		if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := runchannel.LoadRunControlConfig(home); err == nil {
			t.Fatalf("tampered run.json was accepted:\n%s", tampered)
		}
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// newChannelTestDaemon wires a Daemon whose product server is the supplied
// handler and whose session is a fixed bearer, then opens a channel for run.
func newChannelTestDaemon(
	t *testing.T,
	server http.Handler,
	run Run,
	doc RunContext,
) (*Daemon, runchannel.RunControlConfig, func() error) {
	t.Helper()
	return newChannelTestDaemonWithPresenter(t, server, run, doc, nil)
}

func newChannelTestDaemonWithPresenter(
	t *testing.T,
	server http.Handler,
	run Run,
	doc RunContext,
	present RunOutputPresenter,
) (*Daemon, runchannel.RunControlConfig, func() error) {
	t.Helper()
	return newChannelTestDaemonWithWorkspace(t, server, run, doc, runChannelWorkspace{present: present})
}

// newChannelTestDaemonWithWorkspace opens the channel with the local
// completions a real Run has: present_output's presenter, and the workspace
// artifact_fetch writes into.
func newChannelTestDaemonWithWorkspace(
	t *testing.T,
	server http.Handler,
	run Run,
	doc RunContext,
	workspace runChannelWorkspace,
) (*Daemon, runchannel.RunControlConfig, func() error) {
	t.Helper()
	upstream := httptest.NewServer(server)
	t.Cleanup(upstream.Close)
	d := &Daemon{
		cfg:                Config{ServerBaseURL: upstream.URL, CLIVersion: "test"},
		client:             NewClient(upstream.URL),
		logger:             testLogger(),
		localPlaneEndpoint: "http://127.0.0.1:19713",
	}
	d.client.SetToken("session-token")
	control, revoke, err := d.registerRunChannel(run, doc, run.AuthToken, workspace)
	if err != nil {
		t.Fatalf("register channel: %v", err)
	}
	t.Cleanup(func() { _ = revoke() })
	channelServer := httptest.NewServer(d.runChannelHandler())
	t.Cleanup(channelServer.Close)
	control.Endpoint = channelServer.URL
	return d, control, revoke
}

func TestRunChannelForwardsWithDaemonSessionAndRunProof(t *testing.T) {
	run := channelTestRun(t, true)
	var seen atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		if r.URL.Path != "/api/v1/companies/"+testChannelCompany+"/questions" {
			t.Errorf("forwarded path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer session-token" {
			t.Errorf("bearer = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Boardly-Run-Token") != testChannelRunToken {
			t.Errorf("run proof = %q", r.Header.Get("X-Boardly-Run-Token"))
		}
		if r.Header.Get("Idempotency-Key") == "" || r.Header.Get("X-Client-Platform") != "cli" {
			t.Errorf("headers = %v", r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["title"] != "Pick a name" {
			t.Errorf("body = %v (err %v)", body, err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"question":{"id":"q1","status":"pending"}}`))
	})
	_, control, _ := newChannelTestDaemon(t, upstream, run, taskChannelDocument())

	response, err := runchannel.NewClient(control).Do(context.Background(), runchannel.Request{
		Method:     http.MethodPost,
		Path:       "/companies/" + testChannelCompany + "/questions",
		RequestKey: uuid.NewString(),
		Body:       json.RawMessage(`{"title":"Pick a name","prompts":[]}`),
	})
	if err != nil {
		t.Fatalf("channel request: %v", err)
	}
	if response.Status != http.StatusCreated || !strings.Contains(string(response.Body), `"q1"`) {
		t.Fatalf("relayed %+v", response)
	}
	if seen.Load() != 1 {
		t.Fatalf("upstream saw %d requests, want 1", seen.Load())
	}
}

func TestRunChannelCEOCreatesOnlyScheduledTasks(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		schedule string
		want     bool
	}{
		{name: "once", schedule: `,"schedule":{"type":"once","runAt":"2030-10-12T20:32:00+07:00"}`, want: true},
		{name: "cron", schedule: `,"schedule":{"type":"cron","expression":"*/5 * * * *","timeZone":"Asia/Ho_Chi_Minh"}`, want: true},
		{name: "immediate"},
		{name: "null", schedule: `,"schedule":null`},
		{name: "empty", schedule: `,"schedule":{}`},
		{name: "unknown", schedule: `,"schedule":{"type":"immediate"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var forwarded atomic.Int32
			upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				forwarded.Add(1)
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"` + uuid.NewString() + `","runId":null}`))
			})
			_, control, _ := newChannelTestDaemon(t, upstream, channelTestRun(t, false), RunContext{
				Agent: RunAgentContext{IsRoot: true},
			})
			response, err := runchannel.NewClient(control).Do(context.Background(), runchannel.Request{
				Method: http.MethodPost, Path: "/companies/" + testChannelCompany + "/tasks",
				RequestKey: uuid.NewString(),
				Body:       json.RawMessage(`{"projectId":"` + testChannelProject + `","title":"Scheduled work","milestoneType":"launch"` + test.schedule + `}`),
			})
			if test.want {
				if err != nil || response.Status != http.StatusCreated || forwarded.Load() != 1 {
					t.Fatalf("create = %+v, %v; forwarded = %d, want created once", response, err, forwarded.Load())
				}
			} else if refusal, ok := channelRefusal(err); !ok || refusal.Code != "run_scope_denied" || forwarded.Load() != 0 {
				t.Fatalf("create = %+v, %v; forwarded = %d, want local scope refusal", response, err, forwarded.Load())
			}
		})
	}
}

// A Task Run's run.json names its Project, and `zen task create` reaches the
// server as the daemon's member with the Run proof: the Project lookup and
// the create are forwarded, GET /me never is.
func TestRunChannelForwardsTaskCreationForTheRunsMember(t *testing.T) {
	run := channelTestRun(t, true)
	company := "/companies/" + testChannelCompany
	forwarded := make(chan string, 4)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded <- r.Method + " " + r.URL.Path
		if r.Header.Get("Authorization") != "Bearer session-token" ||
			r.Header.Get("X-Boardly-Run-Token") != testChannelRunToken {
			t.Errorf("forwarded without the daemon session and Run proof: %v", r.Header)
		}
		if r.Method == http.MethodPost {
			if r.Header.Get("Idempotency-Key") == "" {
				t.Error("the create was forwarded without its request key")
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"0198aa00-0000-7000-8000-0000000000e1","runId":null}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"` + testChannelProject + `","key":"booking"}`))
	})
	_, control, _ := newChannelTestDaemon(t, upstream, run, taskChannelDocument())
	if control.ProjectID != testChannelProject || control.ProjectKey != "booking" ||
		!slices.Contains(control.Commands, "zen task create") {
		t.Fatalf("run.json = %+v, want the Run's Project and zen task create", control)
	}
	client := runchannel.NewClient(control)

	if response, err := client.Do(context.Background(), runchannel.Request{
		Method: http.MethodGet, Path: company + "/projects/" + testChannelProject,
	}); err != nil || response.Status != http.StatusOK {
		t.Fatalf("own Project lookup = %+v, %v", response, err)
	}
	response, err := client.Do(context.Background(), runchannel.Request{
		Method: http.MethodPost, Path: company + "/tasks", RequestKey: uuid.NewString(),
		Body: json.RawMessage(`{"projectId":"` + testChannelProject +
			`","title":"Follow up","milestoneType":"launch"}`),
	})
	if err != nil || response.Status != http.StatusCreated {
		t.Fatalf("task create = %+v, %v", response, err)
	}
	_, err = client.Do(context.Background(), runchannel.Request{Method: http.MethodGet, Path: "/me"})
	var refusal *runchannel.Refusal
	if !errors.As(err, &refusal) || refusal.Code != "run_scope_denied" {
		t.Fatalf("GET /me = %v, want run_scope_denied", err)
	}
	close(forwarded)
	var paths []string
	for path := range forwarded {
		paths = append(paths, path)
	}
	want := []string{
		"GET /api/v1" + company + "/projects/" + testChannelProject,
		"POST /api/v1" + company + "/tasks",
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("upstream saw %v, want %v", paths, want)
	}
}

type channelPresenterStub struct {
	inputs []tools.PresentOutputInput
	err    error
}

func (stub *channelPresenterStub) PresentOutput(
	_ context.Context,
	input tools.PresentOutputInput,
) (tools.PresentOutputResult, error) {
	stub.inputs = append(stub.inputs, input)
	if stub.err != nil {
		return tools.PresentOutputResult{}, stub.err
	}
	return tools.PresentOutputResult{OutputID: "0198aa00-0000-7000-8000-00000000ff01"}, nil
}

func TestRunCLIAppPreviewRegistrarRegistersCommandsForTheRun(t *testing.T) {
	t.Parallel()
	previews, err := newAppPreviewService(t.TempDir(), testLogger(), launchAppPreviewUnsandboxed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := previews.Close(); err != nil {
			t.Errorf("close app previews: %v", err)
		}
	})
	run := channelTestRun(t, true)
	run.RuntimeID = uuid.NewString()
	workDir := t.TempDir()
	registrar := &runCLIAppPreviewRegistrar{previews: previews, run: run, workDir: workDir}
	outputID := uuid.Must(uuid.NewV7())

	err = registrar.RegisterAppPreview(t.Context(), outputID.String(), []tools.PresentOutputCommand{
		{Command: "go", Args: []string{"run", "./cmd/api"}, WorkingDirectory: "api", Port: 8080},
		{Command: "npm", Args: []string{"run", "dev"}, WorkingDirectory: "web", Port: 5173},
	})
	if err != nil {
		t.Fatalf("RegisterAppPreview() error = %v", err)
	}
	manifest, legacy, err := previews.loadStoredManifest(outputID)
	if err != nil {
		t.Fatalf("load registered manifest: %v", err)
	}
	if legacy != nil {
		t.Fatalf("legacy manifest = %+v", legacy)
	}
	companyID := uuid.MustParse(run.CompanyID)
	runtimeID := uuid.MustParse(run.RuntimeID)
	wantWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.CompanyID != companyID || manifest.OutputID != outputID || manifest.RunID != run.ID ||
		manifest.RuntimeID != runtimeID ||
		manifest.WorkDir != wantWorkDir || len(manifest.Commands) != 2 {
		t.Fatalf("manifest = %+v", manifest)
	}
	if manifest.Commands[0].Command != "go" || manifest.Commands[0].Port != 8080 ||
		manifest.Commands[1].WorkingDirectory != "web" ||
		len(manifest.Commands[1].Args) != 2 || manifest.Commands[1].Args[1] != "dev" {
		t.Fatalf("commands = %+v", manifest.Commands)
	}
}

// The present_output MCP tool sends the presentation input; the daemon packages the
// local files instead of forwarding it as an ordinary API request.
func TestRunChannelPresentsOutputThroughTheDaemon(t *testing.T) {
	run := channelTestRun(t, true)
	var forwarded atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forwarded.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	stub := &channelPresenterStub{}
	_, control, _ := newChannelTestDaemonWithPresenter(t, upstream, run, taskChannelDocument(), stub)
	proxy, err := tools.NewOutputChannelService(control.Endpoint, control.Capability, run.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	saved, err := proxy.PresentOutput(
		t.Context(),
		tools.PresentOutputInput{
			Type:    "app",
			Title:   "Preview",
			Summary: "Local app",
			App: &tools.PresentOutputApp{
				Commands: []tools.PresentOutputCommand{{Command: "npm", Port: 5173}},
			},
		},
	)
	if err != nil || saved.OutputID == "" || stub.inputs[0].App.Commands[0].Port != 5173 {
		t.Fatalf("MCP proxy result = %+v, %v", saved, err)
	}
	stub.inputs = nil
	for _, command := range control.Commands {
		if command == "zen output present" {
			t.Fatal("MCP operation advertised as CLI command")
		}
	}

	client := runchannel.NewClient(control)
	path := "/runs/" + run.ID.String() + "/output"

	response, err := client.Do(context.Background(), runchannel.Request{
		Method: http.MethodPost, Path: path, RequestKey: uuid.NewString(),
		Body: json.RawMessage(`{"title":"Scope","summary":"The MVP scope","items":[` +
			`{"role":"primary","label":"Scope","target":{"kind":"workspace_file","workspaceRelativePath":"docs/scope.md"}}]}`),
	})
	if err != nil {
		t.Fatalf("present output: %v", err)
	}
	if response.Status != http.StatusCreated ||
		!strings.Contains(string(response.Body), `"outputId":"0198aa00-0000-7000-8000-00000000ff01"`) {
		t.Fatalf("relayed %+v", response)
	}
	if len(stub.inputs) != 1 || stub.inputs[0].Title != "Scope" || len(stub.inputs[0].Items) != 1 ||
		stub.inputs[0].Items[0].Target.WorkspaceRelativePath != "docs/scope.md" {
		t.Fatalf("presenter received %+v", stub.inputs)
	}
	if forwarded.Load() != 0 {
		t.Fatalf("upstream saw %d requests; the daemon must present the output itself", forwarded.Load())
	}

	// A server verdict on the way (an expired Run, a refused upload) is
	// relayed as itself; anything else is the presentation failing.
	stub.err = &apiclient.APIError{StatusCode: http.StatusConflict, Code: "run_not_active", Detail: "Run ended"}
	response, err = client.Do(context.Background(), runchannel.Request{
		Method: http.MethodPost, Path: path, RequestKey: uuid.NewString(),
		Body: json.RawMessage(`{"title":"Scope","summary":"s","items":[]}`),
	})
	if err != nil || response.Status != http.StatusConflict ||
		!strings.Contains(string(response.Body), "run_not_active") {
		t.Fatalf("server verdict relayed as %+v, %v", response, err)
	}
	stub.err = errors.New("output must have exactly one primary item")
	response, err = client.Do(context.Background(), runchannel.Request{
		Method: http.MethodPost, Path: path, RequestKey: uuid.NewString(),
		Body: json.RawMessage(`{"title":"Scope","summary":"s","items":[]}`),
	})
	if err != nil || response.Status != http.StatusUnprocessableEntity ||
		!strings.Contains(string(response.Body), "exactly one primary item") {
		t.Fatalf("presentation failure relayed as %+v, %v", response, err)
	}

	// Without a presenter the local route is not admitted, so the tool's
	// call is refused before it reaches anything.
	_, bare, _ := newChannelTestDaemon(t, upstream, run, taskChannelDocument())
	_, err = runchannel.NewClient(bare).Do(context.Background(), runchannel.Request{
		Method: http.MethodPost, Path: path, RequestKey: uuid.NewString(), Body: json.RawMessage(`{}`),
	})
	if refusal, ok := channelRefusal(err); !ok || refusal.Code != "run_scope_denied" {
		t.Fatalf("presenterless channel answered %v, want run_scope_denied", err)
	}
}

func TestRunChannelRefusesOutOfScopeAndUnauthenticatedCalls(t *testing.T) {
	run := channelTestRun(t, true)
	var seen atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		seen.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	d, control, revoke := newChannelTestDaemon(t, upstream, run, taskChannelDocument())
	logs := &lockedLogBuffer{}
	// The daemon's default level: a refusal must show up without --log-level debug.
	d.logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client := runchannel.NewClient(control)

	// Another Conversation's history is outside this Run's scope.
	foreign := "/conversations/" + uuid.NewString() + "/messages"
	_, err := client.Do(context.Background(), runchannel.Request{
		Method: http.MethodGet,
		Path:   foreign + "?limit=5",
	})
	refusal, ok := channelRefusal(err)
	if !ok || refusal.Code != "run_scope_denied" || refusal.Status != http.StatusForbidden {
		t.Fatalf("out-of-scope read returned %v, want run_scope_denied", err)
	}
	// The refusal never reaches the server, so the daemon log is the only
	// place outside the agent's transcript that records it.
	logged := logs.String()
	for _, want := range []string{
		`"level":"WARN"`, `"code":"run_scope_denied"`, `"run":"` + run.ID.String() + `"`,
		`"method":"GET"`, `"path":"` + foreign + `"`,
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("refusal log is missing %s:\n%s", want, logged)
		}
	}
	// A mutation without a request key cannot converge on retry.
	_, err = client.Do(context.Background(), runchannel.Request{
		Method: http.MethodPost,
		Path:   "/companies/" + testChannelCompany + "/questions",
		Body:   json.RawMessage(`{}`),
	})
	if refusal, ok := channelRefusal(err); !ok || refusal.Code != "request_key_required" {
		t.Fatalf("keyless mutation returned %v, want request_key_required", err)
	}
	// A path that tries to leave the API prefix is invalid before policy.
	_, err = client.Do(context.Background(), runchannel.Request{
		Method: http.MethodGet,
		Path:   "/companies/" + testChannelCompany + "/../../healthz",
	})
	if refusal, ok := channelRefusal(err); !ok || refusal.Code != "invalid_request" {
		t.Fatalf("escaping path returned %v, want invalid_request", err)
	}
	// A forged capability is not a channel.
	forged := control
	forged.Capability, _ = newLocalCapability()
	_, err = runchannel.NewClient(forged).Do(context.Background(), runchannel.Request{
		Method: http.MethodGet,
		Path:   "/companies/" + testChannelCompany + "/tasks/" + testChannelTask,
	})
	if refusal, ok := channelRefusal(err); !ok || refusal.Status != http.StatusUnauthorized {
		t.Fatalf("forged capability returned %v, want 401", err)
	}
	if seen.Load() != 0 {
		t.Fatalf("upstream saw %d requests before any authorized call", seen.Load())
	}

	// The channel dies with the Run.
	if err := revoke(); err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(context.Background(), runchannel.Request{
		Method: http.MethodGet,
		Path:   "/companies/" + testChannelCompany + "/tasks/" + testChannelTask,
	})
	if refusal, ok := channelRefusal(err); !ok || refusal.Status != http.StatusUnauthorized {
		t.Fatalf("revoked channel returned %v, want 401", err)
	}
}

// The policy matches a path segment by segment, but the server routes on the
// decoded path. An encoded slash or dot inside one segment would therefore
// pass a "*" row and land on another route: tasks/<id>%2Fworkspace is one
// segment here and GET /tasks/:taskId/workspace — a whole Task, description
// and messages — on the server. Any percent sign in the path is refused
// before the policy runs and never reaches the server; the query string,
// which takes no part in routing, keeps its encoding.
func TestRunChannelRefusesAPercentEncodedPathBeforePolicy(t *testing.T) {
	company := "/companies/" + testChannelCompany
	other := uuid.NewString()
	for name, test := range map[string]struct {
		task bool
		doc  RunContext
	}{
		"Task Run":    {task: true, doc: taskChannelDocument()},
		"channel Run": {doc: RunContext{ChannelInvocation: &RunChannelInvocation{ID: uuid.NewString()}}},
	} {
		t.Run(name, func(t *testing.T) {
			var forwarded []string
			var mu sync.Mutex
			upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				forwarded = append(forwarded, r.URL.RequestURI())
				mu.Unlock()
				_, _ = w.Write([]byte(`{"items":[]}`))
			})
			_, control, _ := newChannelTestDaemon(t, upstream, channelTestRun(t, test.task), test.doc)
			client := runchannel.NewClient(control)
			for _, path := range []string{
				company + "/tasks/" + other + "%2Fworkspace",
				company + "/tasks/" + other + "%2fresults",
				company + "/tasks/" + other + "%2Fdependencies",
				company + "/tasks/%2E%2E",
				company + "/agents/" + other + "%2F..%2F..%2Fusage",
				company + "/tasks/BSC%2D8",
			} {
				_, err := client.Do(context.Background(), runchannel.Request{
					Method: http.MethodGet, Path: path,
				})
				if refusal, ok := channelRefusal(err); !ok || refusal.Code != "invalid_request" {
					t.Fatalf("GET %s returned %v, want invalid_request", path, err)
				}
			}
			mu.Lock()
			if len(forwarded) != 0 {
				t.Fatalf("encoded paths reached the server: %v", forwarded)
			}
			mu.Unlock()

			// An encoded query is not a path: it is forwarded as sent.
			query := company + "/tasks/" + other + "?x=a%2Fb"
			if response, err := client.Do(context.Background(), runchannel.Request{
				Method: http.MethodGet, Path: query,
			}); err != nil || response.Status != http.StatusOK {
				t.Fatalf("GET %s = %+v, %v", query, response, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(forwarded) != 1 || forwarded[0] != "/api/v1"+query {
				t.Fatalf("forwarded %v, want [%s]", forwarded, "/api/v1"+query)
			}
		})
	}
}

func TestRunChannelRenewsTheDaemonSessionOnceOnRejection(t *testing.T) {
	run := channelTestRun(t, true)
	var calls atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer renewed-token" {
			t.Errorf("retry bearer = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"id":"` + testChannelTask + `"}`))
	})
	d, control, _ := newChannelTestDaemon(t, upstream, run, taskChannelDocument())
	d.client.SetSession(renewingSession{token: "session-token", renewed: "renewed-token"})

	response, err := runchannel.NewClient(control).Do(context.Background(), runchannel.Request{
		Method: http.MethodGet,
		Path:   "/companies/" + testChannelCompany + "/tasks/" + testChannelTask + "?x=1",
	})
	if err != nil || response.Status != http.StatusOK {
		t.Fatalf("response %+v err %v", response, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want the rejected call plus one retry", calls.Load())
	}
}

// lockedLogBuffer is a log sink the channel handler writes from its own
// goroutine while the test reads it.
type lockedLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

type renewingSession struct{ token, renewed string }

func (s renewingSession) AccessToken(context.Context) (string, error)   { return s.token, nil }
func (s renewingSession) Renew(context.Context, string) (string, error) { return s.renewed, nil }

func TestRunSystemPromptAdvertisesExactlyTheChannelCommands(t *testing.T) {
	t.Parallel()
	run := channelTestRun(t, true)
	commands := runCLICommandUses(runCLICommands(run, taskChannelDocument()))
	prompt := buildRunSystemPromptWithCLI(
		taskChannelDocument(),
		"/work",
		runMemoryContext{},
		runPlatformPromptContext{Commands: commands},
	)
	for _, required := range []string{
		"Zenith Team CLI (zen):",
		"bound to this Run",
		"zen run context",
		"- zen task get\n",
		"- zen question ask\n",
		"- zen task fail\n",
		"- zen task create\n",
		"never call the Zenith Team API with curl",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q:\n%s", required, prompt)
		}
	}
	if strings.Contains(prompt, "zen task list") {
		t.Fatal("a Task Run must not be told it can list every Task")
	}
	if silent := buildRunSystemPrompt(
		taskChannelDocument(),
		"/work",
		runMemoryContext{},
	); strings.Contains(
		silent,
		"Zenith Team CLI",
	) {
		t.Fatal("a Run without a channel must not be told about the CLI")
	}
}

// mcpToolDrivers calls every boardly-mcp tool the way boardly-mcp does: the
// tool's own service over apiclient.RunChannel, so the route each tool
// really calls is the one the channel must admit. The two skill tools read
// the Run's local snapshot and call nothing, so they have no driver.
func mcpToolDrivers(
	t *testing.T,
	api *apiclient.RunChannel,
	run tools.RunContext,
	planRoot string,
) map[string]func(context.Context) error {
	t.Helper()
	plans, err := tools.NewCompanyPlanServiceForWorkspace(api, planRoot)
	if err != nil {
		t.Fatalf("Company Plan service: %v", err)
	}
	actionsPath := filepath.Join(planRoot, "company-plan-actions.yaml")
	if err := os.WriteFile(
		actionsPath,
		[]byte("kind: company_plan_transaction\nformat_version: 5\ncompany: acme\nactions: []\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	calendar, calendarErr := tools.NewCalendarToolService(api, run)
	social, socialErr := tools.NewSocialToolService(api, run)
	wiki, wikiErr := tools.NewWikiToolService(api, run)
	pages, pagesErr := tools.NewPageToolService(api, run)
	mail, mailErr := tools.NewMailService(api, run)
	milestones, milestonesErr := tools.NewMilestoneToolService(api, run)
	whiteboards, whiteboardsErr := tools.NewWhiteboardToolService(api, run)
	if err := errors.Join(calendarErr, socialErr, wikiErr, pagesErr, mailErr, milestonesErr, whiteboardsErr); err != nil {
		t.Fatalf("build the tool services: %v", err)
	}
	// Only a Task Run has the Task the failure tool needs.
	var failures *tools.DefaultTaskFailureService
	if run.TaskID != "" {
		var failuresErr error
		failures, failuresErr = tools.NewTaskFailureService(api, run)
		if failuresErr != nil {
			t.Fatalf("build the Task tool services: %v", failuresErr)
		}
	}
	pageID := uuid.NewString()
	board := pageID
	return map[string]func(context.Context) error{
		"ask_human": func(ctx context.Context) error {
			_, err := tools.NewQuestionService(api, run.CompanyID).AskHuman(ctx, tools.AskHumanInput{
				Title:   "Pick a name",
				Prompts: []tools.QuestionPrompt{{ID: "name", Prompt: "Name?", SelectionMode: "single"}},
			})
			return err
		},
		"attest_milestones": func(ctx context.Context) error {
			_, err := milestones.AttestMilestones(ctx, tools.AttestMilestonesInput{
				Milestones: []string{"launch"},
			})
			return err
		},
		"calendar_read": func(ctx context.Context) error {
			_, err := calendar.ReadCalendar(ctx, tools.ReadCalendarInput{})
			return err
		},
		"calendar_write": func(ctx context.Context) error {
			_, err := calendar.WriteCalendar(ctx, tools.WriteCalendarInput{Action: "create"})
			return err
		},
		"company_plan_export": func(ctx context.Context) error {
			_, err := plans.Export(ctx, tools.ExportInput{Overwrite: true})
			return err
		},
		"company_plan_transaction_preview": func(ctx context.Context) error {
			_, err := plans.Preview(ctx, tools.PreviewInput{ActionsPath: actionsPath})
			return err
		},
		// Apply submits only what this process previewed.
		"company_plan_transaction_apply": func(ctx context.Context) error {
			if _, err := plans.Preview(ctx, tools.PreviewInput{ActionsPath: actionsPath}); err != nil {
				return err
			}
			_, err := plans.Apply(ctx, tools.ApplyInput{ActionsPath: actionsPath})
			return err
		},
		"create_mail_drafts": func(ctx context.Context) error {
			_, err := mail.CreateMailDrafts(ctx, tools.CreateMailDraftsInput{
				Drafts: []tools.CreateMailDraftInput{{Subject: "Hello", Text: "Hi"}},
			})
			return err
		},
		"list_agents": func(ctx context.Context) error {
			_, err := plans.ListAgents(ctx, tools.ListAgentsInput{})
			return err
		},
		"mark_task_failed": func(ctx context.Context) error {
			_, err := failures.MarkTaskFailed(ctx, tools.MarkTaskFailedInput{Reason: "The market is gone."})
			return err
		},
		"project_read": func(ctx context.Context) error {
			_, err := pages.ReadProject(ctx)
			return err
		},
		"source_read": func(ctx context.Context) error {
			_, err := pages.ReadSource(ctx, tools.SourceReadInput{VersionID: uuid.NewString()})
			return err
		},
		"page_preview": func(ctx context.Context) error {
			_, err := pages.PreviewPage(ctx, tools.PageReadInput{PageID: pageID})
			return err
		},
		"finalize_task_results": func(ctx context.Context) error {
			service, err := tools.NewTaskResultToolService(api, run)
			if err != nil {
				return err
			}
			_, err = service.FinalizeTaskResults(
				ctx,
				tools.FinalizeTaskResultsInput{
					ExpectedRevision: 1,
					OutputIDs:        []string{uuid.NewString()},
				},
			)
			return err
		},
		"page_create": func(ctx context.Context) error {
			markdown := "# Plan"
			_, err := pages.CreatePage(ctx, tools.PageCreateInput{Title: "Plan", Markdown: &markdown})
			return err
		},
		"page_read": func(ctx context.Context) error {
			_, err := pages.ReadPage(ctx, tools.PageReadInput{PageID: pageID})
			return err
		},
		"page_edit": func(ctx context.Context) error {
			_, err := pages.EditPage(ctx, tools.PageEditInput{
				PageID: pageID, Sequence: 3, Summary: "Findings.",
			})
			return err
		},
		"social_read": func(ctx context.Context) error {
			_, err := social.ReadSocial(ctx, tools.ReadSocialInput{})
			return err
		},
		"social_publish": func(ctx context.Context) error {
			_, err := social.PublishSocial(ctx, tools.PublishSocialInput{QuestionID: uuid.NewString()})
			return err
		},
		"artifact_edit": func(ctx context.Context) error {
			_, err := whiteboards.Edit(ctx, tools.ArtifactEditInput{
				ArtifactID: board,
				Ops: []tools.WhiteboardDrawOp{
					{Type: "notes", Items: []tools.WhiteboardDrawNote{{Text: "Idea"}}},
				},
			})
			if err != nil {
				return err
			}
			_, err = whiteboards.Edit(
				ctx,
				tools.ArtifactEditInput{
					ArtifactID: board,
					Commands: []map[string]any{
						{"type": "create_item", "draftId": "note", "itemType": "sticky"},
					},
				},
			)
			return err
		},
		"wiki_search": func(ctx context.Context) error {
			_, err := wiki.SearchWiki(ctx, tools.WikiSearchInput{Query: "refunds"})
			return err
		},
		"wiki_read": func(ctx context.Context) error {
			_, err := wiki.ReadWiki(ctx, tools.WikiReadInput{PageID: pageID})
			return err
		},
		"wiki_write": func(ctx context.Context) error {
			_, err := wiki.WriteWiki(ctx, tools.WikiWriteInput{Pages: []tools.WikiWritePage{
				{Key: "company-overview", Title: "Company overview", Markdown: "# Overview"},
			}})
			return err
		},
	}
}

// mcpChannelUpstream is the server behind the daemon: it records every
// forwarded call and answers each tool's route with a body the tool accepts.
func mcpChannelUpstream(t *testing.T, run Run, forwarded *[]string, mu *sync.Mutex) http.Handler {
	t.Helper()
	company := "/api/v1/companies/" + run.CompanyID
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer session-token" ||
			r.Header.Get("X-Boardly-Run-Token") != testChannelRunToken {
			t.Errorf("%s %s forwarded without the daemon session and the Run proof", r.Method, r.URL.Path)
		}
		if r.Method != http.MethodGet && r.Header.Get("Idempotency-Key") == "" {
			t.Errorf("%s %s forwarded without its request key", r.Method, r.URL.Path)
		}
		mu.Lock()
		*forwarded = append(*forwarded, r.Method+" "+r.URL.Path)
		mu.Unlock()
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case path == "/api/v1/companies":
			_, _ = w.Write([]byte(`{"items":[{"id":"` + run.CompanyID + `","slug":"acme","revision":1}]}`))
		case path == company+"/projects" || path == company+"/tasks" || path == company+"/agents":
			_, _ = w.Write([]byte(`{"items":[]}`))
		case path == company+"/company-plan/apply":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"` + uuid.NewString() + `","status":"pending"}`))
		case path == company+"/conversations/"+run.ConversationID+"/mail-draft-groups":
			_, _ = w.Write(
				[]byte(`{"id":"` + uuid.NewString() + `","recipientCount":1,"drafts":[{"id":"d1"}]}`),
			)
		case strings.HasSuffix(path, "/results") && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status":"in_progress","revision":2}`))
		case path == company+"/task-failures":
			_, _ = w.Write([]byte(`{"status":"failed"}`))
		case strings.HasSuffix(path, "/presentations"):
			_, _ = w.Write([]byte(`{"presentationId":"` + uuid.NewString() + `","attachmentId":"` +
				uuid.NewString() + `","title":"Plan"}`))
		case path == "/api/v1/runs/"+run.ID.String()+"/output":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"outputId":"` + uuid.NewString() + `"}`))
		case path == company+"/runs/"+run.ID.String()+"/agent-artifacts/"+testChannelArtifact:
			_ = json.NewEncoder(w).Encode(testChannelArtifactManifest)
		case path == company+"/runs/"+run.ID.String()+"/agent-artifacts/"+testChannelArtifact+
			"/items/0/download":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(testChannelArtifactBody))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
}

// Every tool boardly-mcp is offered reaches the server through the Run
// channel, and the channel admits exactly the routes the tools call: each
// call is forwarded with the daemon's session and the Run proof, none is
// refused as outside the Run's scope, and boardly-mcp itself sends no
// bearer and no Run token (Access TECH B3b). A tool whose route changes
// without its row fails here. present_output reaches the daemon's
// presenter, which handles both files and native Pages.
func TestEveryMCPToolCallIsAdmittedByTheRunChannel(t *testing.T) {
	exercised := map[string]bool{}
	for name, shape := range map[string]struct {
		task bool
		doc  RunContext
	}{
		"Task Run":           {task: true, doc: taskChannelDocument()},
		"CEO direct chat":    {doc: RunContext{Agent: RunAgentContext{IsRoot: true}}},
		"member direct chat": {doc: RunContext{}},
		"channel invocation": {doc: RunContext{ChannelInvocation: &RunChannelInvocation{ID: uuid.NewString()}}},
		"company research":   {doc: RunContext{ExecutionProfile: RunExecutionProfileContext{Source: runSourceCompanyResearch}}},
	} {
		t.Run(name, func(t *testing.T) {
			run := channelTestRun(t, shape.task)
			var forwarded []string
			var mu sync.Mutex
			presenter := &channelPresenterStub{}
			d, control, _ := newChannelTestDaemonWithWorkspace(
				t, mcpChannelUpstream(t, run, &forwarded, &mu), run, shape.doc,
				runChannelWorkspace{present: presenter, path: t.TempDir()},
			)
			// boardly-mcp's own requests carry the Run capability and nothing
			// that could act as the person.
			mcpSide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				auth := r.Header.Get("Authorization")
				if !strings.HasPrefix(auth, runchannel.AuthorizationScheme+" ") {
					t.Errorf("boardly-mcp sent Authorization %q, want the Run capability", auth)
				}
				if r.Header.Get("X-Boardly-Run-Token") != "" {
					t.Error("boardly-mcp sent the Run token")
				}
				d.runChannelHandler()(w, r)
			}))
			t.Cleanup(mcpSide.Close)
			control.Endpoint = mcpSide.URL
			planRoot, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			drivers := mcpToolDrivers(t, apiclient.NewRunChannel(control), tools.RunContext{
				CompanyID: run.CompanyID, ConversationID: run.ConversationID, TaskID: run.TaskID,
				RunID: run.ID.String(), AgentID: run.AgentID,
			}, planRoot)
			// present_output is the local row: boardly-mcp sends it over the
			// same channel and the daemon completes it with its presenter.
			output, err := tools.NewOutputChannelService(
				control.Endpoint,
				control.Capability,
				run.ID.String(),
			)
			if err != nil {
				t.Fatalf("output channel service: %v", err)
			}
			drivers["present_output"] = func(ctx context.Context) error {
				_, err := output.PresentOutput(ctx, tools.PresentOutputInput{Title: "Result"})
				return err
			}
			// artifact_inspect and artifact_fetch are local rows too: the
			// daemon reads the Artifact on the server itself, with its
			// session and the Run proof.
			artifacts, err := tools.NewArtifactChannelService(
				control.Endpoint,
				control.Capability,
				run.ID.String(),
			)
			if err != nil {
				t.Fatalf("artifact channel service: %v", err)
			}
			// A Whiteboard reads through artifact_inspect on its Run routes.
			whiteboards, err := tools.NewWhiteboardToolService(
				apiclient.NewRunChannel(control),
				tools.RunContext{
					CompanyID:      run.CompanyID,
					ConversationID: run.ConversationID,
					TaskID:         run.TaskID,
					RunID:          run.ID.String(),
					AgentID:        run.AgentID,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			drivers["artifact_inspect"] = func(ctx context.Context) error {
				if _, err := artifacts.InspectArtifact(
					ctx,
					tools.ArtifactInspectInput{ArtifactID: testChannelArtifact},
				); err != nil {
					return err
				}
				_, err := whiteboards.Read(
					ctx,
					tools.ArtifactInspectInput{
						ArtifactID: testChannelArtifact,
						Area:       "all",
						Image:      true,
					},
				)
				return err
			}
			drivers["artifact_fetch"] = func(ctx context.Context) error {
				fetched, err := artifacts.FetchArtifact(
					ctx,
					tools.ArtifactFetchInput{ArtifactID: testChannelArtifact},
				)
				if err == nil && mustReadFile(t, fetched.LocalPath) != testChannelArtifactBody {
					return errors.New("artifact_fetch wrote other bytes")
				}
				return err
			}
			artifactRoute := "/api/v1/companies/" + run.CompanyID + "/runs/" + run.ID.String() +
				"/agent-artifacts/" + testChannelArtifact
			presented := 0
			offered := boardlyMCPToolsForRun(shape.doc)
			if len(offered) == 0 {
				t.Fatal("the shape offers no boardly-mcp tool")
			}
			for _, tool := range offered {
				drive, ok := drivers[tool]
				if !ok {
					if tool != "search_skills" && tool != "load_skill_reference" {
						t.Fatalf("no driver for boardly-mcp tool %s; add one", tool)
					}
					continue
				}
				mu.Lock()
				before := len(forwarded)
				mu.Unlock()
				err := drive(t.Context())
				var refusal *runchannel.Refusal
				if errors.As(err, &refusal) {
					t.Fatalf("%s was refused by the Run channel: %v", tool, err)
				}
				if err != nil {
					t.Fatalf("%s failed: %v", tool, err)
				}
				mu.Lock()
				reached := len(forwarded) > before
				calls := slices.Clone(forwarded[before:])
				mu.Unlock()
				if tool == "artifact_inspect" || tool == "artifact_fetch" {
					// The daemon completes the call: the server sees only
					// its own Run-proven reads, never the channel path, and
					// a Whiteboard's reading.
					for _, call := range calls {
						if call != http.MethodGet+" "+artifactRoute &&
							call != http.MethodGet+" "+artifactRoute+"/items/0/download" &&
							call != http.MethodPost+" "+artifactRoute+"/whiteboard/view" &&
							call != http.MethodPost+" "+artifactRoute+"/whiteboard/context" {
							t.Fatalf("%s reached the server as %s", tool, call)
						}
					}
				}
				if tool == "present_output" {
					presented++
					if reached || len(presenter.inputs) != presented {
						t.Fatalf(
							"present_output reached the server or skipped the daemon's presenter",
						)
					}
					exercised[tool] = true
					continue
				}
				if !reached {
					t.Fatalf("%s never reached the server", tool)
				}
				exercised[tool] = true
			}
			if len(presenter.inputs) != presented {
				t.Fatalf(
					"the daemon presented %d outputs; only present_output invokes its presenter",
					len(presenter.inputs),
				)
			}
		})
	}
	for _, tool := range boardlyMCPToolCatalog {
		if tool == "search_skills" || tool == "load_skill_reference" {
			continue
		}
		if !exercised[tool] {
			t.Errorf("boardly-mcp tool %s was never driven through the Run channel", tool)
		}
	}
}

// A tool's rows exist only when the Run is offered the tool, and never reach
// run.json or the prompt, which advertise zen commands alone.
func TestMCPToolRowsFollowTheOfferedTools(t *testing.T) {
	t.Parallel()
	run := channelTestRun(t, false)
	company := "/companies/" + testChannelCompany
	member := runCLICommands(run, RunContext{})
	ceo := runCLICommands(run, RunContext{Agent: RunAgentContext{IsRoot: true}})
	for _, route := range [][2]string{
		{http.MethodGet, "/companies"},
		{http.MethodPost, company + "/company-plan/apply"},
		{http.MethodPost, company + "/milestone-attestations"},
	} {
		if runCLIPolicyFor(member).allows(route[0], route[1]) {
			t.Fatalf(
				"a member's direct Run admits %s %s, which only the root Agent's tools call",
				route[0],
				route[1],
			)
		}
		if !runCLIPolicyFor(ceo).allows(route[0], route[1]) {
			t.Fatalf("the root Agent's direct Run refuses %s %s", route[0], route[1])
		}
	}
	planningOff := RunContext{Agent: RunAgentContext{
		IsRoot: true, Capabilities: RunCapabilitySettings{DisabledBuiltIns: []string{"planning"}},
	}}
	if runCLIPolicyFor(runCLICommands(run, planningOff)).allows(http.MethodGet, "/companies") {
		t.Fatal("with planning off the root Agent still lists the member's Companies")
	}
	for _, uses := range [][]string{runCLICommandUses(member), runCLICommandUses(ceo)} {
		for _, use := range uses {
			if !strings.HasPrefix(use, "zen ") {
				t.Fatalf("run.json advertises %q, which is not a zen command", use)
			}
		}
	}
	for _, command := range ceo {
		if command.Tool != "" && command.Use != "" {
			t.Fatalf("row %+v is both a zen command and a tool", command)
		}
	}
}

// artifact_inspect and artifact_fetch post to channel paths no server route
// has. Their rows are local, with the 60 s default, and the daemon keeps
// them only when the Run has a workspace to fetch into: without one, or
// with saved work off, the call is refused and nothing reaches the server
// (Access TECH B3b).
func TestArtifactToolRowsAreLocalAndNeedAWorkspace(t *testing.T) {
	t.Parallel()
	run := channelTestRun(t, false)
	policy := runCLIPolicyFor(runCLICommands(run, RunContext{}))
	for _, path := range []string{runArtifactInspectPath(run), runArtifactFetchPath(run)} {
		if _, ok := policy.admit(http.MethodPost, path, false); ok {
			t.Fatalf("POST %s is forwarded to the server", path)
		}
		operation, ok := policy.admit(http.MethodPost, path, true)
		if !ok || operation.forwardTimeout() != runChannelForwardTimeout {
			t.Fatalf(
				"POST %s admitted %t for %v, want a local row with the %v default",
				path, ok, operation.forwardTimeout(), runChannelForwardTimeout,
			)
		}
	}
	savedWorkOff := RunContext{Agent: RunAgentContext{
		Capabilities: RunCapabilitySettings{DisabledBuiltIns: []string{"saved-work"}},
	}}
	if _, ok := runCLIPolicyFor(runCLICommands(run, savedWorkOff)).
		admit(http.MethodPost, runArtifactFetchPath(run), true); ok {
		t.Fatal("with saved work off the Run still fetches Artifacts")
	}

	var forwarded atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forwarded.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	_, control, _ := newChannelTestDaemon(t, upstream, run, RunContext{})
	service, err := tools.NewArtifactChannelService(control.Endpoint, control.Capability, run.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	_, inspectErr := service.InspectArtifact(
		t.Context(),
		tools.ArtifactInspectInput{ArtifactID: testChannelArtifact},
	)
	_, fetchErr := service.FetchArtifact(
		t.Context(),
		tools.ArtifactFetchInput{ArtifactID: testChannelArtifact},
	)
	for _, err := range []error{inspectErr, fetchErr} {
		if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
			t.Fatalf("a Run without a workspace read an Artifact: %v", err)
		}
	}
	if forwarded.Load() != 0 {
		t.Fatalf("a refused artifact call reached the server %d times", forwarded.Load())
	}
}

// Social calls wait on the provider, so their rows forward for 130 s, the
// longest a row may take; every other row keeps the 60 s default. The
// client waits for the longest row plus a margin, so the daemon's own
// timeout answers first.
func TestRunChannelForwardTimeoutsFollowTheRow(t *testing.T) {
	t.Parallel()
	run := channelTestRun(t, false)
	conversation := "/companies/" + testChannelCompany + "/conversations/" + testChannelConversation
	policy := runCLIPolicyFor(runCLICommands(run, RunContext{}))
	for _, path := range []string{conversation + "/social", conversation + "/social/posts"} {
		method := http.MethodGet
		if strings.HasSuffix(path, "/posts") {
			method = http.MethodPost
		}
		operation, ok := policy.admit(method, path, false)
		if !ok || operation.forwardTimeout() != 130*time.Second {
			t.Fatalf(
				"%s %s forwards for %v (admitted %t), want 130s",
				method,
				path,
				operation.forwardTimeout(),
				ok,
			)
		}
	}
	if operation, ok := policy.admit(http.MethodPost, "/companies/"+testChannelCompany+"/questions", false); !ok ||
		operation.forwardTimeout() != runChannelForwardTimeout {
		t.Fatalf(
			"a question forwards for %v, want the %v default",
			operation.forwardTimeout(),
			runChannelForwardTimeout,
		)
	}
	for _, doc := range []RunContext{{}, {Agent: RunAgentContext{IsRoot: true}}, taskChannelDocument()} {
		for _, command := range runCLICommands(channelTestRun(t, doc.ExecutionProfile.Task != nil), doc) {
			if command.Operation.forwardTimeout() > runchannel.MaxForwardTimeout {
				t.Fatalf("%+v forwards longer than the channel client waits", command)
			}
		}
	}
}

// Agents send structured present_output input. A prebuilt presentation body
// is refused for both Task and direct Runs; only the daemon submits captures.
func TestRunChannelRejectsPrebuiltPresentationAndPresentsOutput(t *testing.T) {
	pageBody := json.RawMessage(`{"type":"text","title":"Plan","summary":"s","presentationId":"` +
		uuid.NewString() + `","items":[{"role":"primary","label":"Plan","attachmentId":"` + uuid.NewString() + `"}]}`)
	zenBody := json.RawMessage(`{"title":"Scope","summary":"s","items":[` +
		`{"role":"primary","label":"Scope","target":{"kind":"workspace_file","workspaceRelativePath":"docs/scope.md"}}]}`)
	for name, test := range map[string]struct {
		task        bool
		doc         RunContext
		wantForward bool
	}{
		"Task Run, native delivery is daemon-owned": {task: true, doc: taskChannelDocument()},
		"direct chat, no page tools":                {doc: RunContext{}},
	} {
		t.Run(name, func(t *testing.T) {
			run := channelTestRun(t, test.task)
			var forwarded atomic.Int32
			upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded.Add(1)
				if r.URL.Path != "/api/v1"+runOutputPath(run) {
					t.Errorf("forwarded %s", r.URL.Path)
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"outputId":"0198aa00-0000-7000-8000-00000000ff02"}`))
			})
			stub := &channelPresenterStub{}
			_, control, _ := newChannelTestDaemonWithPresenter(t, upstream, run, test.doc, stub)
			client := runchannel.NewClient(control)

			response, err := client.Do(context.Background(), runchannel.Request{
				Method: http.MethodPost, Path: runOutputPath(run),
				RequestKey: uuid.NewString(), Body: pageBody,
			})
			var refusal *runchannel.Refusal
			if test.wantForward {
				if err != nil || response.Status != http.StatusCreated || forwarded.Load() != 1 {
					t.Fatalf(
						"page presentation = %+v, %v (forwarded %d), want it forwarded",
						response,
						err,
						forwarded.Load(),
					)
				}
			} else if !errors.As(err, &refusal) || refusal.Code != "run_scope_denied" || forwarded.Load() != 0 {
				t.Fatalf(
					"prebuilt page presentation = %+v, %v, want run_scope_denied",
					response,
					err,
				)
			}
			if len(stub.inputs) != 0 {
				t.Fatal("the daemon tried to present a captured Page itself")
			}

			response, err = client.Do(context.Background(), runchannel.Request{
				Method: http.MethodPost, Path: runOutputPath(run),
				RequestKey: uuid.NewString(), Body: zenBody,
			})
			if err != nil || response.Status != http.StatusCreated || len(stub.inputs) != 1 {
				t.Fatalf("present_output = %+v, %v (presented %d)", response, err, len(stub.inputs))
			}
		})
	}
}

// The channel carries what boardly-mcp sent and read before it moved onto
// it: a Company Plan apply up to the server's own 16 MiB, and an answer up to
// runchannel.MaxResponseBytes, such as the export's one read of every Task. A
// larger answer is refused whole. Cut short, it is not JSON, and the daemon
// would relay it as a bare detail with the server's 200, which a reader takes
// for an empty list.
func TestRunChannelCarriesFullSizeBodiesAndRefusesAnOversizeAnswer(t *testing.T) {
	t.Parallel()
	run := channelTestRun(t, false)
	company := "/companies/" + testChannelCompany
	var oversize atomic.Bool
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"0198aa00-0000-7000-8000-00000000ff03","status":"pending"}`))
			return
		}
		size := runchannel.MaxResponseBytes - len(`{"items":[""]}`)
		if oversize.Load() {
			size++
		}
		_, _ = w.Write([]byte(`{"items":["` + strings.Repeat("a", size) + `"]}`))
	})
	_, control, _ := newChannelTestDaemon(t, upstream, run, RunContext{Agent: RunAgentContext{IsRoot: true}})
	client := runchannel.NewClient(control)

	plan := json.RawMessage(`{"desired":"` + strings.Repeat("a", 12<<20) + `"}`)
	response, err := client.Do(t.Context(), runchannel.Request{
		Method: http.MethodPost, Path: company + "/company-plan/apply",
		RequestKey: uuid.NewString(), Body: plan,
	})
	if err != nil || response.Status != http.StatusAccepted {
		t.Fatalf("a 12 MiB Company Plan apply = %d, %v; want it forwarded", response.Status, err)
	}

	response, err = client.Do(t.Context(), runchannel.Request{Method: http.MethodGet, Path: company + "/tasks"})
	if err != nil || response.Status != http.StatusOK || len(response.Body) != runchannel.MaxResponseBytes {
		t.Fatalf(
			"an answer of exactly the limit = %d bytes, status %d, %v",
			len(response.Body),
			response.Status,
			err,
		)
	}

	oversize.Store(true)
	_, err = client.Do(t.Context(), runchannel.Request{Method: http.MethodGet, Path: company + "/tasks"})
	var refusal *runchannel.Refusal
	if !errors.As(err, &refusal) || refusal.Status != http.StatusBadGateway ||
		refusal.Code != "response_too_large" {
		t.Fatalf("an answer one byte over the limit = %v, want response_too_large", err)
	}
}

func TestOutputMCPChannelEnforcesSavedWorkAndRevocation(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		run := channelTestRun(t, true)
		doc := taskChannelDocument()
		if disabled {
			doc.Agent.Capabilities.DisabledBuiltIns = []string{"saved-work"}
		}
		stub := &channelPresenterStub{}
		_, control, revoke := newChannelTestDaemonWithPresenter(t, http.NotFoundHandler(), run, doc, stub)
		proxy, err := tools.NewOutputChannelService(control.Endpoint, control.Capability, run.ID.String())
		if err != nil {
			t.Fatal(err)
		}
		_, err = proxy.PresentOutput(t.Context(), tools.PresentOutputInput{Title: "Result"})
		if disabled {
			if err == nil || len(stub.inputs) != 0 {
				t.Fatal("saved-work denial bypassed")
			}
		} else if err != nil {
			t.Fatal(err)
		}
		if err := revoke(); err != nil {
			t.Fatal(err)
		}
		calls := len(stub.inputs)
		if _, err := proxy.PresentOutput(
			t.Context(),
			tools.PresentOutputInput{},
		); err == nil ||
			len(stub.inputs) != calls {
			t.Fatal("revoked Run reached presenter")
		}
	}
}
