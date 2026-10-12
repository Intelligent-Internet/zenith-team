package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	taskMergeDaemonID       = "01900000-0000-4000-8000-0000000000f1"
	taskMergeRuntimeID      = "01900000-0000-4000-8000-0000000000f2"
	taskMergeCompanyID      = "01900000-0000-4000-8000-0000000000f3"
	taskMergeTaskID         = "01900000-0000-4000-8000-0000000000f4"
	taskMergeConversationID = "01900000-0000-4000-8000-0000000000f5"
	taskMergeJobID          = "01900000-0000-4000-8000-0000000000f6"
)

func TestClientTaskMergeUsesRuntimeScopedDaemonContract(t *testing.T) {
	t.Parallel()

	var claimCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/daemon/runtimes/" + taskMergeRuntimeID + "/task-merges/claim":
			if r.Method != http.MethodPost {
				t.Errorf("claim method = %s, want POST", r.Method)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode claim body: %v", err)
			}
			if len(body) != 1 || body["daemonId"] != taskMergeDaemonID {
				t.Errorf("claim body = %#v", body)
			}
			if claimCalls.Add(1) > 1 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(w).Encode(TaskMergeClaim{
				JobID:                   taskMergeJobID,
				CompanyID:               taskMergeCompanyID,
				TaskID:                  taskMergeTaskID,
				RunID:                   uuid.MustParse("00000000-0000-4000-8000-000000001601"),
				TaskExecutionGeneration: 7,
				Attempt:                 3,
				ConversationID:          taskMergeConversationID,
				Context: json.RawMessage(
					`{"executionProfile":{"task":{"key":"ENG-1"}}}`,
				),
			})
		case "/api/v1/daemon/runtimes/" + taskMergeRuntimeID + "/task-merges/manual":
			var body manualTaskMergeClaimRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode manual claim body: %v", err)
			}
			if body.DaemonID != taskMergeDaemonID || body.TaskID != taskMergeTaskID ||
				body.RunID != uuid.MustParse("00000000-0000-4000-8000-000000001601") || body.ExpectedRevision != 9 ||
				body.ExpectedExecutionGeneration != 7 {
				t.Errorf("manual claim body = %+v", body)
			}
			_ = json.NewEncoder(w).Encode(TaskMergeClaim{
				JobID:                   taskMergeJobID,
				CompanyID:               taskMergeCompanyID,
				TaskID:                  taskMergeTaskID,
				RunID:                   uuid.MustParse("00000000-0000-4000-8000-000000001601"),
				TaskExecutionGeneration: 7,
				Attempt:                 4,
				ConversationID:          taskMergeConversationID,
				Context: json.RawMessage(
					`{"executionProfile":{"task":{"key":"ENG-1"}}}`,
				),
			})
		case "/api/v1/daemon/runtimes/" + taskMergeRuntimeID + "/task-merges/" + taskMergeJobID + "/lease":
			if r.Method != http.MethodPost {
				t.Errorf("lease method = %s, want POST", r.Method)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode lease body: %v", err)
			}
			if len(body) != 2 {
				t.Errorf("lease body fields = %d, want 2", len(body))
			}
			if body["daemonId"] != taskMergeDaemonID {
				t.Errorf("lease daemonId = %#v, want %q", body["daemonId"], taskMergeDaemonID)
			}
			if body["attempt"] != float64(3) {
				t.Errorf("lease attempt = %#v, want 3", body["attempt"])
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/daemon/runtimes/" + taskMergeRuntimeID + "/task-merges/" + taskMergeJobID + "/result":
			if r.Method != http.MethodPost {
				t.Errorf("result method = %s, want POST", r.Method)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode result body: %v", err)
			}
			if len(body) != 5 {
				t.Errorf("result body fields = %d, want 5", len(body))
			}
			if body["daemonId"] != taskMergeDaemonID {
				t.Errorf("result daemonId = %#v, want %q", body["daemonId"], taskMergeDaemonID)
			}
			if body["attempt"] != float64(3) {
				t.Errorf("result attempt = %#v, want 3", body["attempt"])
			}
			if body["status"] != "merged" {
				t.Errorf("result status = %#v, want merged", body["status"])
			}
			if body["resultCode"] != "" {
				t.Errorf("result resultCode = %#v, want empty", body["resultCode"])
			}
			if body["mergedCommit"] != strings.Repeat("a", 40) {
				t.Errorf("result mergedCommit = %#v, want 40 a characters", body["mergedCommit"])
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	client.SetToken("session-token")
	claim, err := client.ClaimTaskMerge(t.Context(), taskMergeDaemonID, taskMergeRuntimeID)
	if err != nil {
		t.Fatalf("ClaimTaskMerge() error = %v", err)
	}
	if claim == nil {
		t.Fatal("ClaimTaskMerge() = nil, want a claim")
	}
	if claim.JobID != taskMergeJobID || claim.RunID != uuid.MustParse("00000000-0000-4000-8000-000000001601") || claim.Attempt != 3 ||
		claim.TaskExecutionGeneration != 7 || claim.ConversationID != taskMergeConversationID {
		t.Fatalf("ClaimTaskMerge() = %+v", claim)
	}
	manual, err := client.ClaimManualTaskMerge(
		t.Context(), taskMergeDaemonID, taskMergeRuntimeID,
		manualTaskMergeClaimRequest{
			CompanyID: taskMergeCompanyID, TaskID: taskMergeTaskID,
			RunID: uuid.MustParse("00000000-0000-4000-8000-000000001601"), ExpectedRevision: 9,
			ExpectedExecutionGeneration: 7,
		},
	)
	if err != nil || manual.Attempt != 4 || manual.TaskExecutionGeneration != 7 {
		t.Fatalf("ClaimManualTaskMerge() = %+v, %v", manual, err)
	}
	empty, err := client.ClaimTaskMerge(t.Context(), taskMergeDaemonID, taskMergeRuntimeID)
	if err != nil || empty != nil {
		t.Fatalf("ClaimTaskMerge() no content = %+v, %v", empty, err)
	}
	if err := client.RenewTaskMergeLease(
		t.Context(),
		taskMergeDaemonID,
		taskMergeRuntimeID,
		taskMergeJobID,
		3,
	); err != nil {
		t.Fatalf("RenewTaskMergeLease() error = %v", err)
	}
	if err := client.RecordTaskMergeResult(
		t.Context(),
		taskMergeDaemonID,
		taskMergeRuntimeID,
		taskMergeJobID,
		TaskMergeResult{Attempt: 3, Status: TaskMergeStatusMerged, MergedCommit: strings.Repeat("a", 40)},
	); err != nil {
		t.Fatalf("RecordTaskMergeResult() error = %v", err)
	}
}

func TestPollLoopProcessesTaskMergeWithNoProviderSlots(t *testing.T) {
	localDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runTaskMergeDaemonTestGit(t, localDir, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTaskMergeDaemonTestGit(t, localDir, "add", "-A")
	runTaskMergeDaemonTestGit(
		t,
		localDir,
		"-c",
		"user.name=Test",
		"-c",
		"user.email=test@boardly.local",
		"commit",
		"-m",
		"Initial commit",
	)
	head := strings.TrimSpace(runTaskMergeDaemonTestGit(t, localDir, "rev-parse", "HEAD"))
	contextDocument, err := json.Marshal(RunContext{
		ExecutionProfile: RunExecutionProfileContext{Project: &RunProjectContext{ID: taskMergeTaskID}, Task: &RunTaskContext{
			ConversationID: taskMergeConversationID,
		}},
		Resources: []RunResourceContext{{
			ResourceType:  "local_directory",
			LocalDaemonID: taskMergeDaemonID,
			LocalPath:     localDir,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	resultReceived := make(chan struct {
		DaemonID     string `json:"daemonId"`
		Attempt      int32  `json:"attempt"`
		Status       string `json:"status"`
		ResultCode   string `json:"resultCode"`
		MergedCommit string `json:"mergedCommit"`
	}, 1)
	var claims atomic.Int32
	var renewals atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/daemon/runtimes/" + taskMergeRuntimeID + "/task-merges/claim":
			if claims.Add(1) > 1 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(w).Encode(TaskMergeClaim{
				JobID:                   taskMergeJobID,
				CompanyID:               taskMergeCompanyID,
				TaskID:                  taskMergeTaskID,
				RunID:                   uuid.MustParse("00000000-0000-4000-8000-000000001701"),
				TaskExecutionGeneration: 7,
				Attempt:                 5,
				ConversationID:          taskMergeConversationID,
				Context:                 contextDocument,
			})
		case "/api/v1/daemon/runtimes/" + taskMergeRuntimeID + "/task-merges/" + taskMergeJobID + "/lease":
			var body struct {
				DaemonID string `json:"daemonId"`
				Attempt  int32  `json:"attempt"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body); err != nil {
				t.Errorf("decode merge lease: %v", err)
			}
			if body.DaemonID != taskMergeDaemonID {
				t.Errorf("merge lease daemonId = %q, want %q", body.DaemonID, taskMergeDaemonID)
			}
			if body.Attempt != 5 {
				t.Errorf("merge lease attempt = %d, want 5", body.Attempt)
			}
			renewals.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/daemon/runtimes/" + taskMergeRuntimeID + "/task-merges/" + taskMergeJobID + "/result":
			var body struct {
				DaemonID     string `json:"daemonId"`
				Attempt      int32  `json:"attempt"`
				Status       string `json:"status"`
				ResultCode   string `json:"resultCode"`
				MergedCommit string `json:"mergedCommit"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body); err != nil {
				t.Errorf("decode merge result: %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
			resultReceived <- body
		case "/api/v1/daemon/runs/claim":
			t.Error("provider claim was attempted without a provider slot")
			http.Error(w, "unexpected", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	d.cfg.DaemonID = taskMergeDaemonID
	head = seedManagedMergeClaim(t, d, TaskMergeClaim{CompanyID: taskMergeCompanyID, ConversationID: taskMergeConversationID, RunID: uuid.MustParse("00000000-0000-4000-8000-000000001701")})
	d.cfg.MaxConcurrentRuns = 0
	d.cfg.PollInterval = 10 * time.Millisecond
	trackRuntime(d, Runtime{
		ID: taskMergeRuntimeID, CompanyID: taskMergeCompanyID, Provider: "codex", Status: "online",
	})
	ctx, cancel := context.WithCancel(t.Context())
	pollDone := make(chan error, 1)
	go func() {
		pollDone <- d.pollLoop(ctx, nil)
	}()

	select {
	case result := <-resultReceived:
		if result.DaemonID != taskMergeDaemonID || result.Attempt != 5 || result.Status != "merged" ||
			result.ResultCode != "" || result.MergedCommit != head {
			t.Fatalf("reported merge result = %+v, want clean local HEAD %q", result, head)
		}
		if renewals.Load() == 0 {
			t.Fatal("Task merge result arrived without an exact-attempt lease renewal")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("task merge was not processed without provider capacity")
	}
	cancel()
	select {
	case err := <-pollDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pollLoop() error = %v, want context canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pollLoop() did not stop")
	}
}

func TestProcessTaskMergeClaimRenewsLeaseUntilResultIsAccepted(t *testing.T) {
	previousInterval := taskMergeLeaseRenewInterval
	taskMergeLeaseRenewInterval = 10 * time.Millisecond
	t.Cleanup(func() { taskMergeLeaseRenewInterval = previousInterval })

	claim, head, _ := newTaskMergeLocalClaim(t, uuid.MustParse("00000000-0000-4000-8000-000000001751"), 7)
	resultInFlight := atomic.Bool{}
	renewedDuringResult := make(chan struct{}, 1)
	var renewalCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/daemon/runtimes/" + taskMergeRuntimeID + "/task-merges/" + taskMergeJobID + "/lease":
			var body struct {
				DaemonID string `json:"daemonId"`
				Attempt  int32  `json:"attempt"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body); err != nil {
				t.Errorf("decode merge lease: %v", err)
			}
			if body.DaemonID != taskMergeDaemonID {
				t.Errorf("merge lease daemonId = %q, want %q", body.DaemonID, taskMergeDaemonID)
			}
			if body.Attempt != claim.Attempt {
				t.Errorf("merge lease attempt = %d, want %d", body.Attempt, claim.Attempt)
			}
			renewalCount.Add(1)
			if resultInFlight.Load() {
				select {
				case renewedDuringResult <- struct{}{}:
				default:
				}
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/daemon/runtimes/" + taskMergeRuntimeID + "/task-merges/" + taskMergeJobID + "/result":
			var body struct {
				Attempt      int32  `json:"attempt"`
				Status       string `json:"status"`
				MergedCommit string `json:"mergedCommit"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body); err != nil {
				t.Errorf("decode merge result: %v", err)
			}
			if body.Attempt != claim.Attempt {
				t.Errorf("merge result attempt = %d, want %d", body.Attempt, claim.Attempt)
			}
			if body.Status != "merged" {
				t.Errorf("merge result status = %q, want merged", body.Status)
			}
			if body.MergedCommit != head {
				t.Errorf("merge result commit = %q, want %q", body.MergedCommit, head)
			}
			resultInFlight.Store(true)
			select {
			case <-renewedDuringResult:
				w.WriteHeader(http.StatusNoContent)
			case <-time.After(2 * time.Second):
				http.Error(w, "lease was not renewed during result delivery", http.StatusGatewayTimeout)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	head = seedManagedMergeClaim(t, d, claim)
	d.cfg.DaemonID = taskMergeDaemonID
	d.cfg.PollInterval = 10 * time.Millisecond
	if err := d.processTaskMergeClaim(t.Context(), taskMergeRuntimeID, claim); err != nil {
		t.Fatalf("processTaskMergeClaim() error = %v", err)
	}
	if renewalCount.Load() < 2 {
		t.Fatalf("Task merge lease renewals = %d, want at least 2", renewalCount.Load())
	}
}

func TestProjectMergeIgnoresCompanyLocalDirectoryAndStopsOnLostAuthority(t *testing.T) {
	previousInterval := taskMergeLeaseRenewInterval
	taskMergeLeaseRenewInterval = 10 * time.Millisecond
	t.Cleanup(func() { taskMergeLeaseRenewInterval = previousInterval })

	claim, _, localDir := newTaskMergeLocalClaim(t, uuid.MustParse("00000000-0000-4000-8000-000000001752"), 8)
	var renewalCount atomic.Int32
	var resultCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/daemon/runtimes/" + taskMergeRuntimeID + "/task-merges/" + taskMergeJobID + "/lease":
			renewalCount.Add(1)
			http.Error(w, "claim attempt expired", http.StatusConflict)
		case "/api/v1/daemon/runtimes/" + taskMergeRuntimeID + "/task-merges/" + taskMergeJobID + "/result":
			resultCount.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	seedManagedMergeClaim(t, d, claim)
	d.cfg.DaemonID = taskMergeDaemonID
	d.cfg.PollInterval = 10 * time.Millisecond
	holder, err := d.localDirs.Acquire(localDir)
	if err != nil {
		t.Fatalf("hold local directory: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- d.processTaskMergeClaim(t.Context(), taskMergeRuntimeID, claim)
	}()
	defer holder.Release()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("processTaskMergeClaim() error = nil, want lost lease authority")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("processTaskMergeClaim() did not stop after lease authority was lost")
	}
	if renewalCount.Load() != 1 {
		t.Fatalf("lease renewals after local directory release = %d, want 1", renewalCount.Load())
	}
	if resultCount.Load() != 0 {
		t.Fatalf("result calls after lease authority loss = %d, want 0", resultCount.Load())
	}
}

func TestTaskMergeLeaseRenewerCancelsOperationWhenPeriodicRenewalLosesAuthority(t *testing.T) {
	var renewalCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/daemon/runtimes/"+taskMergeRuntimeID+
			"/task-merges/"+taskMergeJobID+"/lease" {
			http.NotFound(w, r)
			return
		}
		if renewalCount.Add(1) == 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "claim attempt expired", http.StatusConflict)
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	client.SetToken("session-token")
	renewer := newTaskMergeLeaseRenewer(
		t.Context(),
		client,
		taskMergeDaemonID,
		taskMergeRuntimeID,
		taskMergeJobID,
		9,
		10*time.Millisecond,
	)
	defer renewer.Stop()
	if err := renewer.Fence(renewer.Context()); err != nil {
		t.Fatalf("Fence() error = %v", err)
	}

	select {
	case <-renewer.Context().Done():
		cause := context.Cause(renewer.Context())
		if cause == nil {
			t.Fatal("lease renewer context has no authority-loss cause")
		}
		if !strings.Contains(cause.Error(), "returned 409") {
			t.Fatalf("lease renewer cause = %v, want stale-attempt response", cause)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lease renewer did not cancel operation after periodic authority loss")
	}
	if renewalCount.Load() != 2 {
		t.Fatalf("Task merge lease renewals = %d, want 2", renewalCount.Load())
	}
}

func TestRecordTaskMergeResultUntilFinalRetriesTransientAndStopsOnPermanentRejection(t *testing.T) {
	previousSchedule := defaultTerminalRetrySchedule
	defaultTerminalRetrySchedule = nil
	t.Cleanup(func() { defaultTerminalRetrySchedule = previousSchedule })

	var resultCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/daemon/runtimes/"+taskMergeRuntimeID+
			"/task-merges/"+taskMergeJobID+"/result" {
			http.NotFound(w, r)
			return
		}
		if resultCalls.Add(1) == 1 {
			http.Error(w, "temporary outage", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "attempt is stale", http.StatusConflict)
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	d.cfg.DaemonID = taskMergeDaemonID
	d.cfg.PollInterval = time.Millisecond
	err := d.recordTaskMergeResultUntilFinal(
		t.Context(),
		taskMergeRuntimeID,
		taskMergeJobID,
		TaskMergeResult{
			Attempt:      10,
			Status:       TaskMergeStatusMerged,
			MergedCommit: strings.Repeat("a", 40),
		},
	)
	var requestErr *requestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("recordTaskMergeResultUntilFinal() error = %v, want request error", err)
	}
	if requestErr.StatusCode != http.StatusConflict {
		t.Fatalf("result rejection status = %d, want 409", requestErr.StatusCode)
	}
	if resultCalls.Load() != 2 {
		t.Fatalf("Task merge result calls = %d, want transient retry then permanent stop", resultCalls.Load())
	}
}

func TestAcceptTaskMergeFailsClosedOnMalformedContext(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1")
	d.cfg.DaemonID = taskMergeDaemonID
	result, err := d.acceptTaskMerge(t.Context(), TaskMergeClaim{
		JobID:                   taskMergeJobID,
		CompanyID:               taskMergeCompanyID,
		TaskID:                  taskMergeTaskID,
		RunID:                   uuid.MustParse("00000000-0000-4000-8000-000000001801"),
		TaskExecutionGeneration: 7,
		Attempt:                 6,
		ConversationID:          taskMergeConversationID,
		Context:                 json.RawMessage(`{"executionProfile":{}}`),
	})
	if err == nil || result.Status != TaskMergeStatusBlocked || result.ResultCode != "invalid_context" ||
		result.MergedCommit != "" {
		t.Fatalf("acceptTaskMerge() = %+v, %v, want blocked invalid context", result, err)
	}
}

// A Task handed back to a computer that already ran it works in a
// "<conversationId>-g<N>" root. Accepting that Run merges from the root its
// frozen workspace generation names, never the first generation's (ADR 0014).
func TestAcceptTaskMergeUsesTheRunsWorkspaceGeneration(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1")
	d.cfg.DaemonID = taskMergeDaemonID
	scope := "projects/" + taskMergeTaskID
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001901")
	acquired, err := d.workspaces.AcquireScopedScratch(
		t.Context(), taskMergeCompanyID, scope, taskMergeConversationID, runID, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(acquired.WorkDir, "handed-off.txt"),
		[]byte("second generation\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := d.workspaces.MarkCompleted(acquired.Root); err != nil {
		t.Fatal(err)
	}
	contextDocument, err := json.Marshal(RunContext{
		ExecutionProfile: RunExecutionProfileContext{
			Project: &RunProjectContext{ID: taskMergeTaskID},
			Task:    &RunTaskContext{ConversationID: taskMergeConversationID, WorkspaceGeneration: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.acceptTaskMergeWithLease(t.Context(), TaskMergeClaim{
		JobID: taskMergeJobID, CompanyID: taskMergeCompanyID, TaskID: taskMergeTaskID,
		RunID: runID, TaskExecutionGeneration: 3, Attempt: 1,
		ConversationID: taskMergeConversationID, Context: contextDocument,
	}, func(context.Context) error { return nil })
	if err != nil || result.Status != TaskMergeStatusMerged {
		t.Fatalf("acceptTaskMergeWithLease() = %+v, %v, want merged", result, err)
	}
	main, err := d.workspaces.ResolveMain(t.Context(), taskMergeCompanyID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(main, "handed-off.txt")); err != nil ||
		string(content) != "second generation\n" {
		t.Fatalf("merged file = %q, %v", content, err)
	}
}

func newTaskMergeLocalClaim(t *testing.T, runID uuid.UUID, attempt int32) (TaskMergeClaim, string, string) {
	t.Helper()
	localDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runTaskMergeDaemonTestGit(t, localDir, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTaskMergeDaemonTestGit(t, localDir, "add", "-A")
	runTaskMergeDaemonTestGit(
		t,
		localDir,
		"-c",
		"user.name=Test",
		"-c",
		"user.email=test@boardly.local",
		"commit",
		"-m",
		"Initial commit",
	)
	head := strings.TrimSpace(runTaskMergeDaemonTestGit(t, localDir, "rev-parse", "HEAD"))
	contextDocument, err := json.Marshal(RunContext{
		ExecutionProfile: RunExecutionProfileContext{Project: &RunProjectContext{ID: taskMergeTaskID}, Task: &RunTaskContext{
			ConversationID: taskMergeConversationID,
		}},
		Resources: []RunResourceContext{{
			ResourceType:  "local_directory",
			LocalDaemonID: taskMergeDaemonID,
			LocalPath:     localDir,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return TaskMergeClaim{
		JobID:                   taskMergeJobID,
		CompanyID:               taskMergeCompanyID,
		TaskID:                  taskMergeTaskID,
		RunID:                   runID,
		TaskExecutionGeneration: 7,
		Attempt:                 attempt,
		ConversationID:          taskMergeConversationID,
		Context:                 contextDocument,
	}, head, localDir
}

func runTaskMergeDaemonTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(context.Background(), "git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func seedManagedMergeClaim(t *testing.T, d *Daemon, claim TaskMergeClaim) string {
	t.Helper()
	acquired, err := d.workspaces.AcquireScopedScratch(t.Context(), claim.CompanyID, "projects/"+taskMergeTaskID, claim.ConversationID, claim.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.workspaces.MarkCompleted(acquired.Root); err != nil {
		t.Fatal(err)
	}
	main, err := d.workspaces.ResolveMain(t.Context(), claim.CompanyID, "projects/"+taskMergeTaskID)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(runTaskMergeDaemonTestGit(t, main, "rev-parse", "HEAD"))
}
