package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon/execenv"
	"github.com/Intelligent-Internet/zenith-team/server/internal/runchannel"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

func TestDoRunClassifiesPreparationFailures(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	validRun := Run{
		ID:             uuid.MustParse("00000000-0000-4000-8000-000000004700"),
		CompanyID:      "company-1",
		ConversationID: "conversation-1",
	}
	invalidConnectorContext, err := json.Marshal(RunContext{
		ConnectorSnapshot: &RunConnectorSnapshot{},
	})
	if err != nil {
		t.Fatal(err)
	}
	mismatchedTaskContext, err := json.Marshal(RunContext{
		ExecutionProfile: RunExecutionProfileContext{Task: &RunTaskContext{
			ConversationID: "another-conversation",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	invalidSnapshotContext, err := json.Marshal(RunContext{
		ConversationSnapshot: json.RawMessage(
			`{"schemaVersion":2,"kind":"direct","conversationId":"conversation-1","throughSeq":0,"truncated":false,"items":[]}`,
		),
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name           string
		run            Run
		provider       string
		agentAvailable bool
		want           string
	}{
		{
			name: "missing run company",
			run: Run{
				ID:             uuid.MustParse("00000000-0000-4000-8000-000000004701"),
				ConversationID: "conversation-1",
			},
			provider: "claude",
			want:     "run.preparation_failed.execution",
		},
		{
			name:     "provider unavailable",
			run:      validRun,
			provider: "claude",
			want:     "run.preparation_failed.provider_availability",
		},
		{
			name: "invalid connector snapshot",
			run: Run{
				ID:             uuid.MustParse("00000000-0000-4000-8000-000000004702"),
				CompanyID:      validRun.CompanyID,
				ConversationID: validRun.ConversationID,
				Context:        invalidConnectorContext,
			},
			provider:       "claude",
			agentAvailable: true,
			want:           "run.preparation_failed.connector",
		},
		{
			name: "task workspace scope mismatch",
			run: Run{
				ID:             uuid.MustParse("00000000-0000-4000-8000-000000004703"),
				CompanyID:      validRun.CompanyID,
				ConversationID: validRun.ConversationID,
				Context:        mismatchedTaskContext,
			},
			provider:       "claude",
			agentAvailable: true,
			want:           "run.preparation_failed.workspace",
		},
		{
			// The frozen history fails validation before any workspace work,
			// and the reason is the server-side one a retry cannot cure.
			name: "invalid conversation snapshot",
			run: Run{
				ID:             uuid.MustParse("00000000-0000-4000-8000-000000004704"),
				CompanyID:      validRun.CompanyID,
				ConversationID: validRun.ConversationID,
				Context:        invalidSnapshotContext,
			},
			provider:       "claude",
			agentAvailable: true,
			want:           "run.preparation_failed.prepare",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d := newTestDaemon(t, "http://127.0.0.1")
			if test.agentAvailable {
				agents := map[string]AgentEntry{test.provider: {Path: executable}}
				d.agentsAvailable.Store(&agents)
			}

			_, err := d.doRun(t.Context(), test.run, test.provider, 0, testLogger())
			if err == nil {
				t.Fatal("doRun() error = nil, want preparation failure")
			}
			if got := runPreparationDiagnosticEvent(err); got != test.want {
				t.Fatalf("diagnostic event = %q, want %q", got, test.want)
			}
			if test.name == "invalid conversation snapshot" {
				if got := runFailureReason(err); got != "run_context_invalid" {
					t.Fatalf("failure reason = %q, want run_context_invalid", got)
				}
				// The daemon's own dot-prefixed housekeeping directories exist
				// from construction; a Run workspace would not.
				entries, _ := os.ReadDir(d.cfg.WorkspacesRoot)
				for _, entry := range entries {
					if !strings.HasPrefix(entry.Name(), ".") {
						t.Fatalf(
							"workspace work happened before the snapshot was refused: %s",
							entry.Name(),
						)
					}
				}
			}
		})
	}
}

func TestHandleRunReportsPhaseClassifiedPreparationFailureWithoutSensitiveDetail(t *testing.T) {
	const diagnosticEvent = "run.preparation_failed.workspace"
	sensitive := errors.New("git failed at /Users/founder/private with token_secret")
	failureBody := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/fail" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode failure body: %v", err)
			} else {
				failureBody <- body
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	d := newTestDaemon(t, server.URL)
	var logs bytes.Buffer
	d.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	trackRuntime(d, Runtime{ID: "rt-1", CompanyID: "company-1", Provider: "claude"})
	d.runner = runnerFunc(func(context.Context, Run, string, int, *slog.Logger) (RunResult, error) {
		return RunResult{}, wrapRunPreparationError(runPreparationPhaseWorkspace, sensitive)
	})

	d.handleRun(
		t.Context(),
		Run{
			ID:             uuid.MustParse("00000000-0000-4000-8000-000000004711"),
			RuntimeID:      "rt-1",
			CompanyID:      "company-1",
			ConversationID: "conv-1",
		},
		0,
	)

	select {
	case body := <-failureBody:
		if got := body["error"]; got != diagnosticEvent {
			t.Fatalf("failure error = %v, want %q", got, diagnosticEvent)
		}
	default:
		t.Fatal("phase-classified failure did not reach the fail endpoint")
	}

	output := logs.String()
	if !strings.Contains(output, `"error_code":"`+diagnosticEvent+`"`) {
		t.Fatalf("preparation failure log missing safe error code: %s", output)
	}
	if !strings.Contains(output, `"phase":"workspace"`) {
		t.Fatalf("preparation failure log missing phase: %s", output)
	}
	if strings.Contains(output, "founder") || strings.Contains(output, "token_secret") {
		t.Fatalf("preparation failure log leaked sensitive detail: %s", output)
	}
}

func TestAcquireManagedRunWorkspaceUsesTheProjectScope(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1")
	run := Run{
		ID:             uuid.MustParse("00000000-0000-4000-8000-000000001501"),
		CompanyID:      "01900000-0000-4000-8000-0000000000e1",
		ConversationID: "01900000-0000-4000-8000-0000000000e2",
	}
	doc := RunContext{
		ExecutionProfile: RunExecutionProfileContext{
			Project: &RunProjectContext{ID: taskMergeTaskID},
			Task: &RunTaskContext{
				ConversationID: run.ConversationID,
			},
		},
	}

	acquired, err := d.acquireManagedRunWorkspace(t.Context(), run, doc)
	if err != nil {
		t.Fatalf("acquireManagedRunWorkspace() error = %v", err)
	}
	wantRoot := filepath.Join(
		d.cfg.WorkspacesRoot,
		run.CompanyID,
		"projects", taskMergeTaskID,
		"worktrees",
		run.ConversationID,
	)
	if acquired.Root != wantRoot || acquired.WorkDir != filepath.Join(wantRoot, "workdir") {
		t.Fatalf("acquireManagedRunWorkspace() = %+v, want root %q", acquired, wantRoot)
	}
	if err := d.workspaces.MarkCompleted(acquired.Root); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireManagedRunWorkspaceRejectsMismatchedTaskConversation(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1")
	run := Run{
		ID:             uuid.MustParse("00000000-0000-4000-8000-000000001506"),
		CompanyID:      "01900000-0000-4000-8000-0000000000e6",
		ConversationID: "01900000-0000-4000-8000-0000000000e7",
	}
	doc := RunContext{ExecutionProfile: RunExecutionProfileContext{Task: &RunTaskContext{
		ConversationID: "01900000-0000-4000-8000-0000000000e8",
	}}}

	_, err := d.acquireManagedRunWorkspace(t.Context(), run, doc)
	if err == nil {
		t.Fatalf("acquireManagedRunWorkspace() error = %v, want task context mismatch", err)
	}
}

// The cancellation poll tells a handoff apart from any other cancel: the
// Run's context is cancelled with errRunHandedOff only when the server says
// the Run was cancelled for a confirmed handoff (ADR 0014).
func TestHandleRunCancelsAHandedOffRunWithItsCause(t *testing.T) {
	for _, test := range []struct {
		name   string
		status string
		want   error
	}{
		{name: "confirmed handoff", status: `{"status":"cancelled","reason":"task_reassigned"}`, want: errRunHandedOff},
		{name: "member cancel", status: `{"status":"cancelled","reason":"user_cancelled"}`, want: context.Canceled},
		{name: "server without reasons", status: `{"status":"cancelled"}`, want: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/status" {
					_, _ = w.Write([]byte(test.status))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
			}))
			defer server.Close()

			d := newTestDaemon(t, server.URL)
			trackRuntime(d, Runtime{ID: "rt-1", CompanyID: "company-1", Provider: "claude"})
			cause := make(chan error, 1)
			d.runner = runnerFunc(
				func(ctx context.Context, _ Run, _ string, _ int, _ *slog.Logger) (RunResult, error) {
					<-ctx.Done()
					cause <- context.Cause(ctx)
					return RunResult{Disposition: RunDispositionCancelled}, nil
				},
			)

			d.handleRun(
				t.Context(),
				Run{
					ID:             uuid.MustParse("00000000-0000-4000-8000-000000004711"),
					RuntimeID:      "rt-1",
					CompanyID:      "company-1",
					ConversationID: "conv-1",
				},
				0,
			)

			if got := <-cause; !errors.Is(got, test.want) {
				t.Fatalf("run context cause = %v, want %v", got, test.want)
			}
		})
	}
}

// The agent can finish, and its Run complete its root, in the instant before
// the poll's handoff cause reaches the Run. handleRun still supersedes that
// root when it discards the result; any other cancel leaves it completed.
func TestHandleRunSupersedesARootCompletedBeforeThePollsCause(t *testing.T) {
	for _, test := range []struct {
		name   string
		status string
		want   string
	}{
		{name: "confirmed handoff", status: `{"status":"cancelled","reason":"task_reassigned"}`, want: "superseded"},
		{name: "member cancel", status: `{"status":"cancelled","reason":"user_cancelled"}`, want: "completed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := Run{
				ID:             uuid.MustParse("00000000-0000-4000-8000-000000001541"),
				RuntimeID:      "rt-1",
				CompanyID:      "01900000-0000-4000-8000-0000000000e1",
				ConversationID: "01900000-0000-4000-8000-0000000000e4",
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/daemon/runs/"+run.ID.String()+"/status" {
					_, _ = w.Write([]byte(test.status))
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			d := newTestDaemon(t, server.URL)
			trackRuntime(d, Runtime{ID: run.RuntimeID, CompanyID: run.CompanyID, Provider: "claude"})
			doc := RunContext{ExecutionProfile: RunExecutionProfileContext{
				Project: &RunProjectContext{ID: taskMergeTaskID},
				Task:    &RunTaskContext{ConversationID: run.ConversationID},
			}}
			d.runner = runnerFunc(
				func(ctx context.Context, run Run, _ string, _ int, _ *slog.Logger) (RunResult, error) {
					acquired, err := d.acquireManagedRunWorkspace(t.Context(), run, doc)
					if err != nil {
						return RunResult{}, err
					}
					if err := d.workspaces.MarkCompleted(acquired.Root); err != nil {
						return RunResult{}, err
					}
					<-ctx.Done()
					return RunResult{
						Disposition: RunDispositionCompleted,
						TaskRoot:    acquired.Root,
					}, nil
				},
			)

			d.handleRun(t.Context(), run, 0)

			if got := workspaceSidecarState(t, d, taskWorktreeRoot(d, run)); got != test.want {
				t.Fatalf("workspace state = %q, want %q", got, test.want)
			}
		})
	}
}

// A handed-off Run's root is superseded, not completed: GC keeps it, and no
// later Run continues it. The poll's cause says so; when a Task Run ended any
// other way than a clean completion, the server's run status does. Every
// other ending completes the root as before, and an unanswered status read
// never supersedes: that is final for the generation.
func TestFinishRunWorkspaceSupersedesAHandedOffRun(t *testing.T) {
	const (
		handedOff     = `{"status":"cancelled","reason":"task_reassigned"}`
		memberCancel  = `{"status":"cancelled","reason":"user_cancelled"}`
		stillRunning  = `{"status":"running"}`
		serverFailing = ""
	)
	for _, test := range []struct {
		name         string
		cause        error
		checkHandoff bool
		status       string
		want         string
		wantReads    int32
	}{
		{name: "poll saw the handoff", cause: errRunHandedOff, status: stillRunning, want: "superseded"},
		{name: "run ended before the poll saw the handoff", checkHandoff: true, status: handedOff, want: "superseded", wantReads: 1},
		{name: "member cancel", checkHandoff: true, status: memberCancel, want: "completed", wantReads: 1},
		{name: "run failed on its own", checkHandoff: true, status: stillRunning, want: "completed", wantReads: 1},
		{name: "server unreachable", checkHandoff: true, status: serverFailing, want: "completed", wantReads: 1},
		// handleRun's final check owns a clean completion.
		{name: "clean completion", status: handedOff, want: "completed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := Run{
				ID:             uuid.MustParse("00000000-0000-4000-8000-000000001511"),
				CompanyID:      "01900000-0000-4000-8000-0000000000e1",
				ConversationID: "01900000-0000-4000-8000-0000000000e2",
			}
			var reads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/daemon/runs/"+run.ID.String()+"/status" {
					http.NotFound(w, r)
					return
				}
				reads.Add(1)
				if test.status == serverFailing {
					http.Error(w, "unavailable", http.StatusBadGateway)
					return
				}
				_, _ = w.Write([]byte(test.status))
			}))
			defer server.Close()
			d := newTestDaemon(t, server.URL)
			doc := RunContext{ExecutionProfile: RunExecutionProfileContext{
				Project: &RunProjectContext{ID: taskMergeTaskID},
				Task:    &RunTaskContext{ConversationID: run.ConversationID},
			}}
			acquired, err := d.acquireManagedRunWorkspace(t.Context(), run, doc)
			if err != nil {
				t.Fatalf("acquireManagedRunWorkspace() error = %v", err)
			}
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			if test.cause != nil {
				cancel(test.cause)
			}

			if err := d.finishRunWorkspace(ctx, run.ID, acquired.Root, test.checkHandoff); err != nil {
				t.Fatalf("finishRunWorkspace() error = %v", err)
			}
			if got := workspaceSidecarState(t, d, acquired.Root); got != test.want {
				t.Fatalf("workspace state = %q, want %q", got, test.want)
			}
			if got := reads.Load(); got != test.wantReads {
				t.Fatalf("run status reads = %d, want %d", got, test.wantReads)
			}
		})
	}
}

// The two ways a handed-off Run ends that the cancellation poll does not see
// first, driven through handleRun and the real doRun with the poll held off.
// The cancel commits, so the server refuses the Run's next transcript append
// and that refusal ends the Run; or the agent finishes just as the handoff
// lands, and only the final check before reporting sees it. Either way the
// root is superseded. A member's cancel on the same path still completes it.
func TestHandleRunSupersedesTheRootOfAHandoffThePollMissed(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name         string
		appendStatus int
		runStatus    string
		want         string
	}{
		{
			name:         "append refused after the cancel committed",
			appendStatus: http.StatusNotFound,
			runStatus:    `{"status":"cancelled","reason":"task_reassigned"}`,
			want:         "superseded",
		},
		{
			name:         "agent finished as the handoff landed",
			appendStatus: http.StatusOK,
			runStatus:    `{"status":"cancelled","reason":"task_reassigned"}`,
			want:         "superseded",
		},
		{
			name:         "append refused after a member cancel",
			appendStatus: http.StatusNotFound,
			runStatus:    `{"status":"cancelled","reason":"user_cancelled"}`,
			want:         "completed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := Run{
				ID:             uuid.MustParse("00000000-0000-4000-8000-000000001531"),
				RuntimeID:      "rt-1",
				CompanyID:      "01900000-0000-4000-8000-0000000000e1",
				ConversationID: "01900000-0000-4000-8000-0000000000e3",
				AuthToken:      "brt_" + strings.Repeat("ab", 32),
			}
			runContext, err := json.Marshal(RunContext{ExecutionProfile: RunExecutionProfileContext{
				Project: &RunProjectContext{ID: taskMergeTaskID},
				Task:    &RunTaskContext{ConversationID: run.ConversationID},
			}})
			if err != nil {
				t.Fatal(err)
			}
			run.Context = runContext
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/daemon/runs/"+run.ID.String()+"/status" {
					_, _ = w.Write([]byte(test.runStatus))
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()

			d := newTestDaemon(t, server.URL)
			d.cancelPollInterval = time.Hour
			trackRuntime(d, Runtime{ID: run.RuntimeID, CompanyID: run.CompanyID, Provider: "claude"})
			agents := map[string]AgentEntry{"claude": {Path: executable}}
			d.agentsAvailable.Store(&agents)
			appends := attachTranscriptAppendStatusRPC(t, d, test.appendStatus)
			stubAgentProvider(t, &fakeBackend{
				events: []agent.Event{agent.CompletedItem{
					Key:   "item-1",
					Role:  agent.RoleAgent,
					Parts: []agent.Part{agent.TextPart{Text: "half of the work"}},
				}},
				result: agent.Result{Status: agent.ResultCompleted},
			})

			d.handleRun(t.Context(), run, 0)

			// The agent really ran and reached the server with its item.
			if got := appends(); got != 1 {
				t.Fatalf("transcript appends = %d, want 1", got)
			}
			if got := workspaceSidecarState(t, d, taskWorktreeRoot(d, run)); got != test.want {
				t.Fatalf("workspace state = %q, want %q", got, test.want)
			}
		})
	}
}

// taskWorktreeRoot is the generation 0 root of a Task Run in the test Project.
func taskWorktreeRoot(d *Daemon, run Run) string {
	return filepath.Join(
		d.cfg.WorkspacesRoot, run.CompanyID, "projects", taskMergeTaskID, "worktrees", run.ConversationID,
	)
}

// workspaceSidecarState reads the lifecycle state of a managed root from its
// sidecar on disk.
func workspaceSidecarState(t *testing.T, d *Daemon, root string) string {
	t.Helper()
	relative, err := filepath.Rel(d.cfg.WorkspacesRoot, root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(d.cfg.WorkspacesRoot, ".metadata", relative+".json"))
	if err != nil {
		t.Fatalf("read workspace sidecar: %v", err)
	}
	var sidecar struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &sidecar); err != nil {
		t.Fatalf("decode workspace sidecar: %v", err)
	}
	return sidecar.State
}

// stubAgentProvider hands every Run the test's provider instead of spawning a
// real agent. Callers must not run in parallel with other doRun tests.
func stubAgentProvider(t *testing.T, provider agent.Provider) {
	t.Helper()
	previous := newAgentProvider
	newAgentProvider = func(string, agent.Config) (agent.Provider, error) { return provider, nil }
	t.Cleanup(func() { newAgentProvider = previous })
}

// attachTranscriptAppendStatusRPC answers every transcript append with
// status: 200 with a receipt, or a refusal like the one a Run whose cancel
// has committed gets. It returns how many appends arrived.
func attachTranscriptAppendStatusRPC(t *testing.T, d *Daemon, status int) func() int {
	t.Helper()
	if status == http.StatusOK {
		requests := attachTranscriptAppendRPC(t, d)
		return func() int { return len(requests()) }
	}
	var appends atomic.Int32
	generation := d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
		var message protocol.Message
		if err := json.Unmarshal(frame, &message); err != nil {
			t.Errorf("unmarshal ws message: %v", err)
			return nil, err
		}
		var rpc protocol.RPCRequestPayload
		if err := json.Unmarshal(message.Payload, &rpc); err != nil {
			t.Errorf("unmarshal rpc request: %v", err)
			return nil, err
		}
		appends.Add(1)
		outbound := &wsOutbound{data: frame}
		outbound.beginWrite()
		go d.wsRPC.deliver(protocol.RPCResponsePayload{
			RequestID: rpc.RequestID, Status: status, Error: "run not found",
		})
		return outbound, nil
	})
	d.wsRPC.markRPCV1Supported(generation)
	return func() int { return int(appends.Load()) }
}

func TestPrepareRunCLIHomeWithholdsAuthorityFromScheduledRuns(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", filepath.Join(t.TempDir(), "missing"))
	envRoot := t.TempDir()
	d := &Daemon{}
	tests := []struct {
		name string
		doc  RunContext
	}{
		{
			name: "schedule even with malformed task context",
			doc: RunContext{ExecutionProfile: RunExecutionProfileContext{
				Source: runSourceSchedule,
				Task:   &RunTaskContext{},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cliEnvironment, err := d.prepareRunCLIEnvironment(
				Run{ID: uuid.MustParse("00000000-0000-4000-8000-000000001508")},
				test.doc, envRoot, "", execenv.SeatbeltOptions{},
			)
			if err != nil {
				t.Fatalf("prepareRunCLIEnvironment() error = %v", err)
			}
			if cliEnvironment.zenithTeamHome == "" || cliEnvironment.cleanup == nil {
				t.Fatalf(
					"prepareRunCLIEnvironment() = %+v, want an empty isolated home",
					cliEnvironment,
				)
			}
			if cliEnvironment.executableDirectory != "" {
				t.Fatalf(
					"non-Task executable directory = %q, want none",
					cliEnvironment.executableDirectory,
				)
			}
			entries, err := os.ReadDir(cliEnvironment.zenithTeamHome)
			if err != nil {
				t.Fatalf("read isolated home: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("isolated non-Task home entries = %v, want none", entries)
			}
			if err := cliEnvironment.cleanup(); err != nil {
				t.Fatalf("cleanup isolated home: %v", err)
			}
		})
	}
}

func TestPinnedZenithTeamExecutableSurvivesOriginalReplacement(t *testing.T) {
	directory := t.TempDir()
	original := filepath.Join(directory, zenithTeamExecutableName())
	if err := os.WriteFile(original, []byte("startup-image"), 0o700); err != nil {
		t.Fatalf("write startup executable: %v", err)
	}
	pinned, cleanup, err := pinZenithTeamExecutable(original)
	if err != nil {
		t.Fatalf("pinZenithTeamExecutable() error = %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup pinned executable: %v", err)
		}
	})
	if err := os.Remove(original); err != nil {
		t.Fatalf("remove original executable: %v", err)
	}
	if err := os.WriteFile(original, []byte("replacement-image"), 0o700); err != nil {
		t.Fatalf("write replacement executable: %v", err)
	}

	runDirectory := t.TempDir()
	if err := copyZenithTeamExecutable(pinned, runDirectory); err != nil {
		t.Fatalf("copyZenithTeamExecutable() error = %v", err)
	}
	staged, err := os.ReadFile(filepath.Join(runDirectory, zenithTeamExecutableName()))
	if err != nil {
		t.Fatalf("read run-local executable: %v", err)
	}
	if string(staged) != "startup-image" {
		t.Fatalf("run-local executable = %q, want pinned startup image", staged)
	}
}

func TestDaemonRunCleansPinnedZenithTeamExecutableOnStartupFailure(t *testing.T) {
	source := filepath.Join(t.TempDir(), zenithTeamExecutableName())
	if err := os.WriteFile(source, []byte("startup-image"), 0o700); err != nil {
		t.Fatalf("write startup executable: %v", err)
	}
	pinned, cleanup, err := pinZenithTeamExecutable(source)
	if err != nil {
		t.Fatalf("pinZenithTeamExecutable() error = %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup pinned executable: %v", err)
		}
	})
	pinnedDirectory := filepath.Dir(pinned.Name())
	d := &Daemon{
		cfg:                         Config{HealthPort: -1},
		logger:                      testLogger(),
		zenithTeamExecutable:        pinned,
		cleanupZenithTeamExecutable: cleanup,
	}
	if err := d.Run(t.Context()); err == nil {
		t.Fatal("Run() error = nil, want invalid health port failure")
	}
	if _, err := os.Stat(pinnedDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pinned executable directory remains after Run exit: %v", err)
	}
}

func TestPrepareRunCLIEnvironmentAllowsTaskWithoutLocalEndpoint(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", filepath.Join(t.TempDir(), "missing"))
	zenithTeamExecutable := filepath.Join(t.TempDir(), "source-zenith-team")
	if err := os.WriteFile(zenithTeamExecutable, []byte("exact-zenith-team-cli"), 0o700); err != nil {
		t.Fatalf("seed Zenith Team executable: %v", err)
	}
	pinnedExecutable, cleanupPinnedExecutable, err := pinZenithTeamExecutable(zenithTeamExecutable)
	if err != nil {
		t.Fatalf("pin Zenith Team executable: %v", err)
	}
	t.Cleanup(func() {
		if err := cleanupPinnedExecutable(); err != nil {
			t.Errorf("cleanup pinned Zenith Team executable: %v", err)
		}
	})
	d := &Daemon{zenithTeamExecutable: pinnedExecutable}
	run := Run{
		ID:        uuid.MustParse("00000000-0000-4000-8000-000000001510"),
		CompanyID: "01900000-0000-4000-8000-0000000000ee",
		TaskID:    "01900000-0000-4000-8000-0000000000ef",
	}
	doc := RunContext{ExecutionProfile: RunExecutionProfileContext{Task: &RunTaskContext{}}}

	cliEnvironment, err := d.prepareRunCLIEnvironment(run, doc, t.TempDir(), "", execenv.SeatbeltOptions{})
	if err != nil {
		t.Fatalf("prepareRunCLIEnvironment() error = %v", err)
	}
	defer func() {
		if err := cliEnvironment.cleanup(); err != nil {
			t.Errorf("cleanup isolated CLI environment: %v", err)
		}
	}()
	entries, err := os.ReadDir(cliEnvironment.zenithTeamHome)
	if err != nil {
		t.Fatalf("read isolated home: %v", err)
	}
	if len(entries) != 0 || cliEnvironment.executableDirectory == "" {
		t.Fatalf(
			"Task CLI environment = %+v, entries %v; want CLI with no local authority",
			cliEnvironment,
			entries,
		)
	}
}

func TestAcquireManagedRunWorkspaceKeepsScheduleIsolation(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1")
	scheduled := Run{
		ID:             uuid.MustParse("00000000-0000-4000-8000-000000001503"),
		CompanyID:      "01900000-0000-4000-8000-0000000000e3",
		ConversationID: "01900000-0000-4000-8000-0000000000e5",
	}
	scheduleDoc := RunContext{ExecutionProfile: RunExecutionProfileContext{Source: runSourceSchedule}}
	isolated, err := d.acquireManagedRunWorkspace(t.Context(), scheduled, scheduleDoc)
	if err != nil {
		t.Fatalf("schedule acquireManagedRunWorkspace() error = %v", err)
	}
	if isolated.Root == filepath.Join(d.cfg.WorkspacesRoot, scheduled.ConversationID) || isolated.Branch != "" {
		t.Fatalf("schedule workspace = %+v, want isolated scratch", isolated)
	}
	if err := d.workspaces.MarkCompleted(isolated.Root); err != nil {
		t.Fatal(err)
	}
}

// A tagged Run gets a single-use scratch folder, as a Schedule occurrence
// does: two Runs of one channel chat never share a folder, and neither is the
// Company main.
func TestAcquireManagedRunWorkspaceGivesATaggedRunFreshScratch(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1")
	doc := RunContext{
		Agent:             RunAgentContext{IsRoot: true},
		ChannelInvocation: &RunChannelInvocation{ID: "0198aa00-0000-7000-8000-0000000000c3"},
	}
	const companyID = "01900000-0000-4000-8000-0000000000e3"
	companyMain := filepath.Join(d.cfg.WorkspacesRoot, companyID, companyWorkspaceScope, "main")
	roots := map[string]bool{}
	for _, id := range []string{"00000000-0000-4000-8000-000000001541", "00000000-0000-4000-8000-000000001542"} {
		run := Run{
			ID:             uuid.MustParse(id),
			CompanyID:      companyID,
			ConversationID: "01900000-0000-4000-8000-0000000000e6",
		}
		acquired, err := d.acquireManagedRunWorkspace(t.Context(), run, doc)
		if err != nil {
			t.Fatalf("tagged acquireManagedRunWorkspace() error = %v", err)
		}
		if acquired.Reused || acquired.Branch != "" || acquired.WorkDir == companyMain ||
			filepath.Dir(acquired.Root) != d.cfg.WorkspacesRoot {
			t.Fatalf("tagged workspace = %+v, want fresh isolated scratch", acquired)
		}
		if roots[acquired.Root] {
			t.Fatalf("tagged Run %s reused root %q", id, acquired.Root)
		}
		roots[acquired.Root] = true
		if _, err := os.Lstat(filepath.Join(acquired.WorkDir, ".memory")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("tagged scratch .memory = %v, want none", err)
		}
		if err := d.workspaces.MarkCompleted(acquired.Root); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Lstat(companyMain); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tagged Runs created the Company main: %v", err)
	}
}

func TestRunUsesMainWorkspaceOnlyForNonTaskConversationRuns(t *testing.T) {
	tests := []struct {
		name string
		doc  RunContext
		want bool
	}{
		{
			name: "direct chat with the root agent",
			doc:  RunContext{Agent: RunAgentContext{IsRoot: true}},
			want: true,
		},
		{
			name: "direct chat with any other agent",
			doc:  RunContext{},
			want: true,
		},
		{
			name: "task run",
			doc: RunContext{ExecutionProfile: RunExecutionProfileContext{
				Task: &RunTaskContext{Key: "ENG-1"},
			}},
			want: false,
		},
		{
			name: "scheduled run",
			doc:  RunContext{ExecutionProfile: RunExecutionProfileContext{Source: runSourceSchedule}},
			want: false,
		},
		{
			name: "root agent tagged in a channel",
			doc: RunContext{
				Agent:             RunAgentContext{IsRoot: true},
				ChannelInvocation: &RunChannelInvocation{ID: "0198aa00-0000-7000-8000-0000000000c2"},
			},
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := runUsesMainWorkspace(test.doc); got != test.want {
				t.Fatalf("runUsesMainWorkspace() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestMainWorkspaceScopeForRunIsAlwaysTheCompanyScope(t *testing.T) {
	for name, doc := range map[string]RunContext{
		"root agent":  {Agent: RunAgentContext{IsRoot: true}},
		"other agent": {Agent: RunAgentContext{Name: "Eli"}},
		"empty":       {},
	} {
		if got := mainWorkspaceScopeFor(doc); got != companyWorkspaceScope {
			t.Fatalf("%s scope = %q, want %q", name, got, companyWorkspaceScope)
		}
	}
}

func TestAcquireMainRunWorkspaceRunsInDurableCompanyMain(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1")
	run := Run{
		ID:             uuid.MustParse("00000000-0000-4000-8000-000000001504"),
		CompanyID:      "01900000-0000-4000-8000-0000000000e9",
		ConversationID: "01900000-0000-4000-8000-0000000000ea",
	}
	doc := RunContext{Agent: RunAgentContext{Name: "Eli", Title: "Engineering Agent"}}

	workDir, lease, parked, err := d.acquireMainRunWorkspace(
		t.Context(), t.Context(), run, doc, d.logger,
	)
	if err != nil {
		t.Fatalf("acquireMainRunWorkspace() error = %v", err)
	}
	wantMain := filepath.Join(d.cfg.WorkspacesRoot, run.CompanyID, companyWorkspaceScope, "main")
	if workDir != wantMain || parked {
		t.Fatalf("acquireMainRunWorkspace() = %q parked=%v, want %q", workDir, parked, wantMain)
	}
	if info, err := os.Lstat(filepath.Join(workDir, ".memory")); err != nil || !info.IsDir() {
		t.Fatalf("main workspace memory directory missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".git")); err != nil {
		t.Fatalf("main workspace is not a Git repository: %v", err)
	}
	lease.Release()

	// The root Agent lands in the same Company main: there is one scope per
	// Company, and every direct Conversation shares it.
	ceo := Run{
		ID:             uuid.MustParse("00000000-0000-4000-8000-000000001505"),
		CompanyID:      run.CompanyID,
		ConversationID: "01900000-0000-4000-8000-0000000000eb",
	}
	ceoWorkDir, ceoLease, _, err := d.acquireMainRunWorkspace(
		t.Context(), t.Context(), ceo, RunContext{Agent: RunAgentContext{IsRoot: true}}, d.logger,
	)
	if err != nil {
		t.Fatalf("acquireMainRunWorkspace(root) error = %v", err)
	}
	defer ceoLease.Release()
	if ceoWorkDir != wantMain {
		t.Fatalf("root main = %q, want %q", ceoWorkDir, wantMain)
	}
}

func TestCompanyRunContextSharesMemoryAndBusinessContextAcrossRunKinds(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1")
	run := Run{
		ID:             uuid.MustParse("00000000-0000-4000-8000-000000001506"),
		CompanyID:      "01900000-0000-4000-8000-0000000000f1",
		ConversationID: "01900000-0000-4000-8000-0000000000f2",
	}
	companyMain := filepath.Join(d.cfg.WorkspacesRoot, run.CompanyID, companyWorkspaceScope, "main")
	wantMemory := filepath.Join(companyMain, ".memory")
	wantBusinessContext := filepath.Join(companyMain, "assets", "general", "business_context.md")
	if err := os.MkdirAll(filepath.Dir(wantBusinessContext), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wantBusinessContext, []byte("# Business context"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Every direct Run works in the Company main, whichever Agent it is for:
	// the business context is a file in its own workspace and nothing is
	// mounted from elsewhere.
	for name, doc := range map[string]RunContext{
		"root agent":  {Agent: RunAgentContext{IsRoot: true}},
		"other agent": {Agent: RunAgentContext{Name: "Eli", Title: "Engineering Agent"}},
	} {
		context, err := d.companyRunContext(t.Context(), run, doc, companyMain)
		if err != nil || context.BusinessContextPath != wantBusinessContext ||
			context.ReadDirs != nil || context.MemoryReadDirs != nil {
			t.Fatalf("%s direct context = %+v, err = %v", name, context, err)
		}
	}

	// A Task worktree branches from the Company main but never carries what
	// the main holds uncommitted (the CEO's saved business context), so every
	// Task Run gets the live Company main Memory and business context mounted
	// read-only — a worktree under the Company scope and one retained under a
	// legacy scoped root alike.
	taskDoc := RunContext{
		Agent: RunAgentContext{Name: "Eli", Title: "Engineering Agent"},
		ExecutionProfile: RunExecutionProfileContext{
			Source: "assign",
			Task:   &RunTaskContext{Key: "ENG-1", ConversationID: run.ConversationID},
		},
	}
	legacyRoot := filepath.Join(d.cfg.WorkspacesRoot, run.CompanyID, "engineering", "worktrees", run.ConversationID)
	if err := os.MkdirAll(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, workDir := range map[string]string{
		"company scope": filepath.Join(d.cfg.WorkspacesRoot, run.CompanyID, companyWorkspaceScope, "worktrees", run.ConversationID, "workdir"),
		"legacy scope":  filepath.Join(legacyRoot, "workdir"),
	} {
		context, err := d.companyRunContext(t.Context(), run, taskDoc, workDir)
		if err != nil {
			t.Fatalf("companyRunContext(%s task) error = %v", name, err)
		}
		if len(context.ReadDirs) != 2 || context.ReadDirs[0] != wantMemory ||
			context.ReadDirs[1] != filepath.Dir(wantBusinessContext) ||
			len(context.MemoryReadDirs) != 1 || context.MemoryReadDirs[0] != wantMemory ||
			context.BusinessContextPath != wantBusinessContext {
			t.Fatalf("%s task context = %+v, want Company memory and business context", name, context)
		}
	}
	if info, err := os.Lstat(wantMemory); err != nil || !info.IsDir() {
		t.Fatalf("Company memory directory not materialized: %v", err)
	}

	scheduleDoc := RunContext{
		Agent:            RunAgentContext{Name: "Eli", Title: "Engineering Agent"},
		ExecutionProfile: RunExecutionProfileContext{Source: runSourceSchedule},
	}
	context, err := d.companyRunContext(t.Context(), run, scheduleDoc, "/scheduled")
	if err != nil || context.BusinessContextPath != "" ||
		context.ReadDirs != nil || context.MemoryReadDirs != nil {
		t.Fatalf("scheduled context = %+v, err = %v, want none", context, err)
	}

	// A tagged Run works in its own scratch: it gets the business context
	// folder read-only and no memory folder, whichever Agent it is for.
	for name, doc := range map[string]RunContext{
		"root agent":  {Agent: RunAgentContext{IsRoot: true}},
		"other agent": {Agent: RunAgentContext{Name: "Eli", Title: "Engineering Agent"}},
	} {
		doc.ChannelInvocation = &RunChannelInvocation{ID: "0198aa00-0000-7000-8000-0000000000c4"}
		context, err := d.companyRunContext(t.Context(), run, doc, "/scratch/workdir")
		if err != nil {
			t.Fatalf("companyRunContext(tagged %s) error = %v", name, err)
		}
		if len(context.ReadDirs) != 1 || context.ReadDirs[0] != filepath.Dir(wantBusinessContext) ||
			context.MemoryReadDirs != nil || context.BusinessContextPath != wantBusinessContext {
			t.Fatalf("tagged %s context = %+v, want the business context folder alone", name, context)
		}
	}
}

// A whole tagged Run, through handleRun, starts clean on the machine its
// person bound a local folder to and whose Company main holds memory: its
// provider works in a fresh scratch folder, with no memory there, starts a
// new provider session with no notice of a lost one, and its prompt names
// neither memory, the folder nor the main, only the business context folder
// read-only. Document writing also starts fresh with no tools. An ordinary
// direct chat is placed as before: in
// the bound folder, or in the Company main with its memory, and resumes its
// session.
func TestConversationRunIsolation(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	d := newTestDaemon(t, server.URL)
	d.cancelPollInterval = time.Hour
	const companyID = "01900000-0000-4000-8000-0000000000f5"
	trackRuntime(d, Runtime{ID: "rt-1", CompanyID: companyID, Provider: "claude"})
	agents := map[string]AgentEntry{"claude": {Path: executable}}
	d.agentsAvailable.Store(&agents)
	appends := attachTranscriptAppendStatusRPC(t, d, http.StatusOK)
	recorder := &execOptionsRecorder{}
	stubAgentProvider(t, recorder)

	companyMain, err := d.workspaces.EnsureMain(t.Context(), companyID, companyWorkspaceScope)
	if err != nil {
		t.Fatal(err)
	}
	businessContext := filepath.Join(companyMain, "assets", "general", "business_context.md")
	if err := os.MkdirAll(filepath.Dir(businessContext), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		businessContext: "# Business context",
		filepath.Join(companyMain, ".memory", "pricing-decisions.md"): "# Pricing",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	localDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(localDir, ".memory"), 0o700); err != nil {
		t.Fatal(err)
	}
	boundFolder := []RunResourceContext{{
		ResourceType: "local_directory", LocalDaemonID: d.cfg.DaemonID, LocalPath: localDir,
	}}
	tagged := &RunChannelInvocation{ID: "0198aa00-0000-7000-8000-0000000000c6"}

	// Every Run follows an earlier one in its chat, so each asks to resume
	// that Run's provider session.
	const earlierSession = "claude-session-earlier"
	launch := func(t *testing.T, id string, doc RunContext) (agent.ExecOptions, string) {
		t.Helper()
		before := len(recorder.launches())
		doc.Agent.Name = "Ada"
		doc.Agent.IsRoot = true
		doc.Company = &RunCompanyContext{Name: "Acme"}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		d.handleRun(t.Context(), Run{
			ID:                     uuid.MustParse(id),
			RuntimeID:              "rt-1",
			CompanyID:              companyID,
			AgentID:                "01900000-0000-4000-8000-0000000000f6",
			ConversationID:         "01900000-0000-4000-8000-0000000000f7",
			AuthToken:              "brt_" + strings.Repeat("cd", 32),
			Context:                raw,
			PriorSessionID:         earlierSession,
			PriorSessionThroughSeq: 3,
		}, 0)
		launches := recorder.launches()
		if len(launches) != before+1 {
			t.Fatalf("launches = %d, want %d", len(launches), before+1)
		}
		return launches[before], recorder.launchPrompts()[before]
	}

	t.Run("tagged", func(t *testing.T) {
		workDirs := map[string]bool{}
		for _, id := range []string{"00000000-0000-4000-8000-000000001544", "00000000-0000-4000-8000-000000001545"} {
			opts, prompt := launch(t, id, RunContext{ChannelInvocation: tagged, Resources: boundFolder})
			if opts.ResumeSessionID != "" || opts.ResumeExpected || opts.ResumeContinuityNotice != "" ||
				strings.Contains(prompt, "Session continuity notice") {
				t.Fatalf(
					"tagged Run resumes %q (expected %v, notice %q), prompt %s, want a new session",
					opts.ResumeSessionID,
					opts.ResumeExpected,
					opts.ResumeContinuityNotice,
					prompt,
				)
			}
			root := filepath.Dir(opts.Cwd)
			if filepath.Base(opts.Cwd) != "workdir" || filepath.Dir(root) != d.cfg.WorkspacesRoot {
				t.Fatalf(
					"tagged Run worked in %q, want a scratch workdir under %q",
					opts.Cwd,
					d.cfg.WorkspacesRoot,
				)
			}
			if workDirs[opts.Cwd] {
				t.Fatalf("a second tagged Run reused %q", opts.Cwd)
			}
			workDirs[opts.Cwd] = true
			if got := workspaceSidecarState(t, d, root); got != "completed" {
				t.Fatalf("tagged scratch state = %q, want completed", got)
			}
			if _, err := os.Lstat(filepath.Join(opts.Cwd, ".memory")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("tagged scratch .memory = %v, want none", err)
			}
			for _, forbidden := range []string{
				"Workspace Memory", "Company Memory", ".memory", localDir, strconv.Quote(companyMain),
				"Company main workspace, not a Task",
			} {
				if strings.Contains(opts.SystemPrompt, forbidden) {
					t.Fatalf("tagged Run prompt names %q: %s", forbidden, opts.SystemPrompt)
				}
			}
			for _, required := range []string{
				"Run workspace: " + strconv.Quote(opts.Cwd),
				"Read-only company context: " + strconv.Quote(filepath.Dir(businessContext)),
				"Shared business context asset: " + businessContext,
				"fresh scratch folder of its own",
			} {
				if !strings.Contains(opts.SystemPrompt, required) {
					t.Fatalf("tagged Run prompt missing %q: %s", required, opts.SystemPrompt)
				}
			}
		}
	})

	t.Run("document writing", func(t *testing.T) {
		opts, prompt := launch(t, "00000000-0000-4000-8000-000000001548", RunContext{
			ExecutionProfile: RunExecutionProfileContext{DocumentThreadID: "thread", DocumentWriting: true},
			Resources:        boundFolder,
			Agent: RunAgentContext{
				McpConfig: json.RawMessage(`{"mcpServers":{"external":{"command":"external-tool"}}}`),
			},
			// An irrelevant external connector must not prevent document generation.
			ConnectorSnapshot: &RunConnectorSnapshot{},
		})
		if opts.ToolPolicy != agent.ToolPolicyConnectorOnly || len(opts.McpConfig) != 0 ||
			len(
				opts.AllowedMCPServers,
			) != 0 || len(opts.ConnectorApps) != 0 || len(opts.PluginDirectories) != 0 {
			t.Fatalf("writing options = %+v, want no tools", opts)
		}
		if opts.Cwd == localDir || opts.Cwd == companyMain || filepath.Base(opts.Cwd) != "workdir" {
			t.Fatalf("writing workspace = %q, want isolated scratch", opts.Cwd)
		}
		if opts.ResumeSessionID != "" || opts.ResumeExpected ||
			strings.Contains(prompt, "Session continuity notice") {
			t.Fatalf("writing resumed prior session: %+v, %s", opts, prompt)
		}
		if opts.ClaudeSettingsPath == "" {
			t.Fatal("writing has no restricted provider settings")
		}
	})

	t.Run("direct chat with a bound folder", func(t *testing.T) {
		opts, _ := launch(t, "00000000-0000-4000-8000-000000001546", RunContext{Resources: boundFolder})
		if opts.Cwd != localDir || !strings.Contains(opts.SystemPrompt, "Workspace Memory") {
			t.Fatalf("direct Run worked in %q with prompt %s, want the bound folder and its memory",
				opts.Cwd, opts.SystemPrompt)
		}
		if opts.ResumeSessionID != earlierSession {
			t.Fatalf("direct Run resumes %q, want %q", opts.ResumeSessionID, earlierSession)
		}
	})

	t.Run("direct chat", func(t *testing.T) {
		opts, _ := launch(t, "00000000-0000-4000-8000-000000001547", RunContext{})
		if opts.Cwd != companyMain || !strings.Contains(opts.SystemPrompt, "Workspace Memory") ||
			!strings.Contains(opts.SystemPrompt, "Company main workspace, not a Task worktree") {
			t.Fatalf("direct Run worked in %q with prompt %s, want the Company main and its memory",
				opts.Cwd, opts.SystemPrompt)
		}
		if opts.ResumeSessionID != earlierSession {
			t.Fatalf("direct Run resumes %q, want %q", opts.ResumeSessionID, earlierSession)
		}
	})
	if got := appends(); got != len(recorder.launches()) {
		t.Fatalf("transcript appends = %d, want one per Run", got)
	}
}

// A tagged Run in a Company whose main this computer never made reads no
// business context and does not make the main to look for one.
func TestCompanyRunContextOfATaggedRunNeverMakesTheCompanyMain(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1")
	run := Run{
		ID:             uuid.MustParse("00000000-0000-4000-8000-000000001543"),
		CompanyID:      "01900000-0000-4000-8000-0000000000f3",
		ConversationID: "01900000-0000-4000-8000-0000000000f4",
	}
	doc := RunContext{ChannelInvocation: &RunChannelInvocation{ID: "0198aa00-0000-7000-8000-0000000000c5"}}
	context, err := d.companyRunContext(t.Context(), run, doc, "/scratch/workdir")
	if err != nil || context.BusinessContextPath != "" ||
		context.ReadDirs != nil || context.MemoryReadDirs != nil {
		t.Fatalf("tagged context = %+v, err = %v, want none", context, err)
	}
	if _, err := os.Lstat(filepath.Join(d.cfg.WorkspacesRoot, run.CompanyID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tagged Run made the Company's workspace: %v", err)
	}
}

// pinnedTestExecutable stages a fake zen binary the way daemon.New does.
func pinnedTestExecutable(t *testing.T) *os.File {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source-zen")
	if err := os.WriteFile(source, []byte("exact-zen-cli"), 0o700); err != nil {
		t.Fatalf("seed zen executable: %v", err)
	}
	pinned, cleanup, err := pinZenithTeamExecutable(source)
	if err != nil {
		t.Fatalf("pin zen executable: %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup pinned zen executable: %v", err)
		}
	})
	return pinned
}

// Every attended Run — a direct Conversation as much as a Task — gets the
// zen binary and a run.json bound to its channel. Revoking the environment
// closes the channel.
func TestPrepareRunCLIHomeBindsAttendedRunsToTheChannel(t *testing.T) {
	t.Setenv("ZENITH_TEAM_HOME", filepath.Join(t.TempDir(), "missing"))
	d := &Daemon{
		zenithTeamExecutable: pinnedTestExecutable(t),
		localPlaneEndpoint:   "http://127.0.0.1:19713",
	}
	tests := []struct {
		name        string
		run         Run
		doc         RunContext
		wantCommand string
	}{
		{
			name: "direct conversation",
			run: Run{
				ID:             uuid.MustParse("00000000-0000-4000-8000-000000001509"),
				CompanyID:      "01900000-0000-4000-8000-0000000000f0",
				AgentID:        "01900000-0000-4000-8000-0000000000f1",
				ConversationID: "01900000-0000-4000-8000-0000000000f2",
				AuthToken:      testChannelRunToken,
			},
			doc:         RunContext{},
			wantCommand: "zen task list",
		},
		{
			name: "task",
			run: Run{
				ID:             uuid.MustParse("00000000-0000-4000-8000-00000000150a"),
				CompanyID:      "01900000-0000-4000-8000-0000000000f0",
				AgentID:        "01900000-0000-4000-8000-0000000000f1",
				ConversationID: "01900000-0000-4000-8000-0000000000f3",
				TaskID:         "01900000-0000-4000-8000-0000000000f4",
				AuthToken:      testChannelRunToken,
			},
			doc: RunContext{ExecutionProfile: RunExecutionProfileContext{
				Task: &RunTaskContext{Key: "ENG-9"},
			}},
			wantCommand: "zen task fail",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cliEnvironment, err := d.prepareRunCLIEnvironment(
				test.run, test.doc, t.TempDir(), "", execenv.SeatbeltOptions{},
			)
			if err != nil {
				t.Fatalf("prepareRunCLIEnvironment() error = %v", err)
			}
			if cliEnvironment.executableDirectory == "" {
				t.Fatal("attended Run got no zen executable")
			}
			entries, err := os.ReadDir(cliEnvironment.zenithTeamHome)
			if err != nil {
				t.Fatalf("read isolated home: %v", err)
			}
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			wantNames := []string{runchannel.ControlFileName}
			if strings.Join(names, ",") != strings.Join(wantNames, ",") {
				t.Fatalf("isolated home entries = %v, want %v", names, wantNames)
			}
			if cliEnvironment.channelHome() != cliEnvironment.zenithTeamHome {
				t.Fatal("the isolated home holds run.json but does not report the channel")
			}
			control, err := runchannel.LoadRunControlConfig(cliEnvironment.zenithTeamHome)
			if err != nil {
				t.Fatalf("load run.json: %v", err)
			}
			if control.RunID != test.run.ID.String() || control.CompanyID != test.run.CompanyID ||
				control.ConversationID != test.run.ConversationID || control.TaskID != test.run.TaskID {
				t.Fatalf("run.json identity = %+v, want the Run's", control)
			}
			if !slices.Contains(control.Commands, test.wantCommand) ||
				!slices.Contains(cliEnvironment.commands, test.wantCommand) {
				t.Fatalf("run.json commands = %v, want %q", control.Commands, test.wantCommand)
			}
			if _, release, ok := d.lookupRunChannel(
				runchannel.AuthorizationScheme + " " + control.Capability,
			); !ok {
				t.Fatal("run.json capability does not open the channel")
			} else {
				release()
			}
			if err := cliEnvironment.cleanup(); err != nil {
				t.Fatalf("cleanup: %v", err)
			}
			if _, _, ok := d.lookupRunChannel(
				runchannel.AuthorizationScheme + " " + control.Capability,
			); ok {
				t.Fatal("channel survived run cleanup")
			}
		})
	}
}
