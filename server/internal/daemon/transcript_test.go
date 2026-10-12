package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/pkg/agent"
	"boardly.dev/boardly/server/pkg/protocol"
	"boardly.dev/boardly/server/pkg/runfailure"
)

func TestExecuteRunStreamClassifiesProviderStartFailure(t *testing.T) {
	t.Parallel()

	sensitive := errors.New("spawn failed in /Users/founder/private with token_secret")
	d := &Daemon{}
	_, err := d.executeRunStream(
		t.Context(),
		&fakeBackend{execErr: sensitive},
		"prompt",
		agent.ExecOptions{},
		testLogger(),
		Run{ID: uuid.MustParse("00000000-0000-4000-8000-000000004711")},
		"",
	)
	if !errors.Is(err, sensitive) {
		t.Fatalf("executeRunStream() error = %v, want provider start cause", err)
	}
	if got := runPreparationDiagnosticEvent(err); got != "run.preparation_failed.provider_spawn" {
		t.Fatalf("diagnostic event = %q, want provider spawn phase", got)
	}
}

func TestTranscriptAppendRequestNormalizesMissingToolInput(t *testing.T) {
	t.Parallel()

	request, err := transcriptAppendRequest(
		uuid.MustParse("00000000-0000-4000-8000-000000004711"),
		agent.CompletedItem{
			Key:  "item-1",
			Role: agent.RoleAgent,
			Parts: []agent.Part{agent.ToolCallPart{
				CallID: "call-1",
				Name:   "list_files",
			}},
		},
	)
	if err != nil {
		t.Fatalf("transcriptAppendRequest() error = %v", err)
	}
	if got := string(request.Parts[0].Input); got != `{}` {
		t.Fatalf("tool input = %s, want {}", got)
	}
}

func TestTranscriptAppendRequestRejectsNoncanonicalItemKey(t *testing.T) {
	t.Parallel()

	_, err := transcriptAppendRequest(uuid.MustParse("00000000-0000-4000-8000-000000004711"), agent.CompletedItem{
		Key:  " item-1 ",
		Role: agent.RoleAgent,
		Parts: []agent.Part{
			agent.TextPart{Text: "done"},
		},
	})
	if err == nil {
		t.Fatal("transcriptAppendRequest() accepted an item key with surrounding whitespace")
	}
}

func TestTranscriptAppendRequestPreservesEmailAndRedactsCredentials(t *testing.T) {
	t.Parallel()

	request, err := transcriptAppendRequest(uuid.New(), agent.CompletedItem{
		Key:  "mail-draft-reply",
		Role: agent.RoleAgent,
		Parts: []agent.Part{agent.TextPart{
			Text: "Created a draft for alice@example.com. password=hunter2", Final: true,
		}},
	})
	if err != nil {
		t.Fatalf("transcriptAppendRequest() error = %v", err)
	}
	want := "Created a draft for alice@example.com. password=[redacted:kv-secret]"
	if got := request.Parts[0].Text; got != want {
		t.Fatalf("reply = %q, want %q", got, want)
	}
}

func TestTranscriptAppendRequestRedactsStructuredToolResultKeys(t *testing.T) {
	t.Parallel()

	request, err := transcriptAppendRequest(
		uuid.MustParse("00000000-0000-4000-8000-000000004711"),
		agent.CompletedItem{
			Key:  "item-1",
			Role: agent.RoleTool,
			Parts: []agent.Part{agent.ToolResultPart{
				CallID: "call-1",
				Name:   "tool",
				Output: `{"token":["alpha",{"raw":"beta"}],"email":"alice@example.com","safe":1}`,
			}},
		},
	)
	if err != nil {
		t.Fatalf("transcriptAppendRequest() error = %v", err)
	}
	if got := request.Parts[0].Output; got != `{"email":"alice@example.com","safe":1,"token":"[redacted:json-secret]"}` {
		t.Fatalf("tool result = %s", got)
	}
}

func TestToolActivityIsIdempotentAcrossStartAndCompletedItem(t *testing.T) {
	t.Parallel()

	active := make(map[string]struct{})
	seen := make(map[string]struct{})
	var total atomic.Int32
	var inFlight atomic.Int32
	started := agent.ToolActivity{
		ItemKey: "tool-1",
		Phase:   agent.ToolActivityPhaseStarted,
	}
	if err := applyToolActivity(active, seen, &total, &inFlight, started); err != nil {
		t.Fatalf("apply first start: %v", err)
	}
	if err := applyToolActivity(active, seen, &total, &inFlight, started); err != nil {
		t.Fatalf("apply duplicate start: %v", err)
	}
	applyCompletedItemToolActivity(active, seen, &total, &inFlight, agent.CompletedItem{
		Key:  "tool-1",
		Role: agent.RoleTool,
		Parts: []agent.Part{
			agent.ToolCallPart{CallID: "tool-1", Name: "exec_command"},
			agent.ToolResultPart{CallID: "tool-1", Name: "exec_command", Output: "done"},
		},
	})
	if got := total.Load(); got != 1 {
		t.Fatalf("tool count = %d, want 1", got)
	}
	if got := inFlight.Load(); got != 0 {
		t.Fatalf("in-flight tool count = %d, want 0", got)
	}
	if err := applyToolActivity(active, seen, &total, &inFlight, started); err != nil {
		t.Fatalf("apply replayed start: %v", err)
	}
	if got := total.Load(); got != 1 {
		t.Fatalf("tool count after replay = %d, want 1", got)
	}
}

func TestRunIdleWatchdogWaitsForTranscriptAppend(t *testing.T) {
	t.Parallel()

	d := &Daemon{}
	ctx, cancelContext := context.WithCancel(context.Background())
	defer cancelContext()
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().Add(-time.Second).UnixNano())
	var inFlightTools atomic.Int32
	var fired atomic.Bool
	var threshold atomic.Int64
	var appendInFlight atomic.Bool
	appendInFlight.Store(true)
	watchdogCancelled := make(chan struct{})
	cancelExecution := func() { close(watchdogCancelled) }

	go d.runIdleWatchdog(
		ctx,
		20*time.Millisecond,
		time.Second,
		&lastActivity,
		&inFlightTools,
		&fired,
		&threshold,
		&appendInFlight,
		cancelExecution,
		testLogger(),
		uuid.MustParse("00000000-0000-4000-8000-000000004711"),
	)
	select {
	case <-watchdogCancelled:
		t.Fatal("watchdog fired while transcript append was in flight")
	case <-time.After(60 * time.Millisecond):
	}
	lastActivity.Store(time.Now().UnixNano())
	appendInFlight.Store(false)
	select {
	case <-watchdogCancelled:
		t.Fatal("watchdog fired immediately after a durable append acknowledgement")
	case <-time.After(10 * time.Millisecond):
	}
	select {
	case <-watchdogCancelled:
	case <-time.After(time.Second):
		t.Fatal("watchdog did not fire after transcript append finished")
	}
	if !fired.Load() {
		t.Fatal("watchdog cancellation did not record the fired state")
	}
}

func TestRunIdleWatchdogUsesToolWindowWhileToolIsActive(t *testing.T) {
	t.Parallel()

	d := &Daemon{}
	ctx, cancelContext := context.WithCancel(context.Background())
	defer cancelContext()
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())
	var inFlightTools atomic.Int32
	inFlightTools.Store(1)
	var fired atomic.Bool
	var threshold atomic.Int64
	var appendInFlight atomic.Bool
	watchdogCancelled := make(chan struct{})

	go d.runIdleWatchdog(
		ctx,
		20*time.Millisecond,
		100*time.Millisecond,
		&lastActivity,
		&inFlightTools,
		&fired,
		&threshold,
		&appendInFlight,
		func() { close(watchdogCancelled) },
		testLogger(),
		uuid.MustParse("00000000-0000-4000-8000-000000004711"),
	)
	select {
	case <-watchdogCancelled:
		t.Fatal("watchdog used the ordinary idle window for an active tool")
	case <-time.After(60 * time.Millisecond):
	}
	select {
	case <-watchdogCancelled:
	case <-time.After(time.Second):
		t.Fatal("watchdog did not enforce the tool window")
	}
	if got := time.Duration(threshold.Load()); got != 100*time.Millisecond {
		t.Fatalf("watchdog threshold = %s, want 100ms", got)
	}
}

func TestAppendTranscriptItemUsesDaemonWebSocket(t *testing.T) {
	d := newTestDaemon(t, "http://unused.invalid")
	generation := d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
		var message protocol.Message
		if err := json.Unmarshal(frame, &message); err != nil {
			t.Fatalf("unmarshal message: %v", err)
		}
		var request protocol.RPCRequestPayload
		if err := json.Unmarshal(message.Payload, &request); err != nil {
			t.Fatalf("unmarshal rpc request: %v", err)
		}
		if request.Method != protocol.DaemonRPCMethodRunTranscriptAppend {
			t.Fatalf("method = %q, want %q", request.Method, protocol.DaemonRPCMethodRunTranscriptAppend)
		}
		var appendRequest protocol.RunTranscriptAppendRequest
		if err := json.Unmarshal(request.Body, &appendRequest); err != nil {
			t.Fatalf("unmarshal append request: %v", err)
		}
		if appendRequest.RunID != uuid.MustParse("00000000-0000-4000-8000-000000004711") ||
			appendRequest.ItemKey != "item-1" {
			t.Fatalf("request = %+v, want run 4711 item-1", appendRequest)
		}

		outbound := &wsOutbound{data: frame}
		outbound.beginWrite()
		go d.wsRPC.deliver(protocol.RPCResponsePayload{
			RequestID: request.RequestID,
			Status:    http.StatusOK,
			Body: json.RawMessage(
				`{"id":"row-1","runId":"00000000-0000-4000-8000-000000004711","itemKey":"item-1","seq":2,"throughSeq":2,"itemDigest":"item-digest","committedAt":"2026-08-21T00:00:00Z"}`,
			),
		})
		return outbound, nil
	})
	d.wsRPC.markRPCV1Supported(generation)

	receipt, err := d.appendTranscriptItem(context.Background(), protocol.RunTranscriptAppendRequest{
		RunID:   uuid.MustParse("00000000-0000-4000-8000-000000004711"),
		ItemKey: "item-1",
		Role:    protocol.RunItemRoleAgent,
		Parts: []protocol.RunItemPart{
			{Type: protocol.RunItemPartTypeText, Text: "Done."},
		},
	})
	if err != nil {
		t.Fatalf("appendTranscriptItem: %v", err)
	}
	if receipt.ID != "row-1" || receipt.Seq != 2 || receipt.ThroughSeq != 2 {
		t.Fatalf("receipt = %+v, want row-1 at sequence 2", receipt)
	}
}

func TestTranscriptAppendRequestRejectsItemBeforeItCanExceedWebSocketFrame(t *testing.T) {
	t.Parallel()

	_, err := transcriptAppendRequest(uuid.MustParse("00000000-0000-4000-8000-000000004711"), agent.CompletedItem{
		Key:  "oversized-item",
		Role: agent.RoleAgent,
		Parts: []agent.Part{
			agent.TextPart{Text: strings.Repeat("x", protocol.RunTranscriptMaxItemBytes+1)},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "transcript limit") {
		t.Fatalf("error = %v, want deterministic transcript-limit failure", err)
	}
}

func TestAppendTranscriptItemWaitsForWebSocket(t *testing.T) {
	d := newTestDaemon(t, "http://unused.invalid")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := d.appendTranscriptItem(ctx, protocol.RunTranscriptAppendRequest{
		RunID:   uuid.MustParse("00000000-0000-4000-8000-000000004711"),
		ItemKey: "item-1",
		Role:    protocol.RunItemRoleAgent,
		Parts:   []protocol.RunItemPart{{Type: protocol.RunItemPartTypeText, Text: "Done."}},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
}

func TestAppendTranscriptItemRejectsPermanentConflict(t *testing.T) {
	d := newTestDaemon(t, "http://unused.invalid")
	generation := d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
		var message protocol.Message
		if err := json.Unmarshal(frame, &message); err != nil {
			t.Fatalf("unmarshal message: %v", err)
		}
		var request protocol.RPCRequestPayload
		if err := json.Unmarshal(message.Payload, &request); err != nil {
			t.Fatalf("unmarshal rpc request: %v", err)
		}
		outbound := &wsOutbound{data: frame}
		outbound.beginWrite()
		go d.wsRPC.deliver(protocol.RPCResponsePayload{
			RequestID: request.RequestID,
			Status:    http.StatusConflict,
			Error:     "run transcript item conflicts with committed content",
		})
		return outbound, nil
	})
	d.wsRPC.markRPCV1Supported(generation)

	_, err := d.appendTranscriptItem(context.Background(), protocol.RunTranscriptAppendRequest{
		RunID:   uuid.MustParse("00000000-0000-4000-8000-000000004711"),
		ItemKey: "item-1",
		Role:    protocol.RunItemRoleAgent,
		Parts:   []protocol.RunItemPart{{Type: protocol.RunItemPartTypeText, Text: "different"}},
	})
	if err == nil {
		t.Fatal("appendTranscriptItem accepted a permanent conflict")
	}
}

func TestAppendTranscriptItemRetriesRPCSaturation(t *testing.T) {
	d := newTestDaemon(t, "http://unused.invalid")
	var attempts atomic.Int32
	generation := d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
		var message protocol.Message
		if err := json.Unmarshal(frame, &message); err != nil {
			t.Fatalf("unmarshal message: %v", err)
		}
		var request protocol.RPCRequestPayload
		if err := json.Unmarshal(message.Payload, &request); err != nil {
			t.Fatalf("unmarshal rpc request: %v", err)
		}
		outbound := &wsOutbound{data: frame}
		outbound.beginWrite()
		response := protocol.RPCResponsePayload{RequestID: request.RequestID}
		if attempts.Add(1) == 1 {
			response.Status = http.StatusTooManyRequests
			response.Error = "too many in-flight rpc requests"
		} else {
			response.Status = http.StatusOK
			response.Body = json.RawMessage(
				`{"id":"row-1","runId":"00000000-0000-4000-8000-000000004711","itemKey":"item-1","seq":1,"throughSeq":1,"itemDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","committedAt":"2026-08-21T00:00:00Z"}`,
			)
		}
		go d.wsRPC.deliver(response)
		return outbound, nil
	})
	d.wsRPC.markRPCV1Supported(generation)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	receipt, err := d.appendTranscriptItem(ctx, protocol.RunTranscriptAppendRequest{
		RunID:   uuid.MustParse("00000000-0000-4000-8000-000000004711"),
		ItemKey: "item-1",
		Role:    protocol.RunItemRoleAgent,
		Parts:   []protocol.RunItemPart{{Type: protocol.RunItemPartTypeText, Text: "Done."}},
	})
	if err != nil {
		t.Fatalf("appendTranscriptItem after saturation: %v", err)
	}
	if receipt.Seq != 1 || attempts.Load() != 2 {
		t.Fatalf("receipt = %+v, attempts = %d; want seq 1 after two attempts", receipt, attempts.Load())
	}
}

// A daemon WS that never comes up (a proxy blocking upgrades) must not wedge
// a run forever in "running" with the idle watchdog suppressed: after the
// delivery window the append fails with a RETRYABLE reason so auto-retry
// resumes the work when the lane returns.
func TestAppendTranscriptItemFailsRetryablyAfterDeliveryWindow(t *testing.T) {
	d := newTestDaemon(t, "http://unused.invalid")
	d.transcriptDeliveryWindowOverride = 250 * time.Millisecond

	_, err := d.appendTranscriptItem(context.Background(), protocol.RunTranscriptAppendRequest{
		RunID:   uuid.MustParse("00000000-0000-4000-8000-000000004711"),
		ItemKey: "item-1",
		Role:    protocol.RunItemRoleAgent,
		Parts:   []protocol.RunItemPart{{Type: protocol.RunItemPartTypeText, Text: "Done."}},
	})
	if !errors.Is(err, errTranscriptDeliveryUnavailable) {
		t.Fatalf("error = %v, want errTranscriptDeliveryUnavailable", err)
	}
	if got := runFailureReason(err); got != string(runfailure.ReasonTimeout) {
		t.Fatalf("failure reason = %q, want the retryable timeout reason", got)
	}
}

// The local-directory park is server-sanctioned indefinite waiting, not
// preparation work: the wait must ride the run's outer context, so a
// directory held longer than the prepare budget cannot fail the queued run,
// and the caller must learn a park happened so it can restart its deadline.
func TestAcquireLocalDirectoryParkOutlivesPrepareBudget(t *testing.T) {
	d := newTestDaemon(t, "http://unused.invalid")
	// macOS temp dirs live behind symlinks the validator rejects.
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}

	holder, err := d.localDirs.Acquire(path)
	if err != nil {
		t.Fatalf("acquire holder lease: %v", err)
	}
	go func() {
		time.Sleep(1200 * time.Millisecond) // longer than a 1s "prepare budget"
		holder.Release()
	}()

	lease, parked, err := d.acquireLocalDirectory(
		context.Background(),
		Run{ID: uuid.MustParse("00000000-0000-4000-8000-000000004711")},
		path,
		testLogger(),
	)
	if err != nil {
		t.Fatalf("acquireLocalDirectory() error = %v, want the lease after the holder releases", err)
	}
	defer lease.Release()
	if !parked {
		t.Fatal("parked = false, want true for a contended acquisition")
	}

	// The uncontended path reports no park.
	lease.Release()
	direct, parked, err := d.acquireLocalDirectory(
		context.Background(),
		Run{ID: uuid.MustParse("00000000-0000-4000-8000-000000004712")},
		path,
		testLogger(),
	)
	if err != nil {
		t.Fatalf("uncontended acquire: %v", err)
	}
	defer direct.Release()
	if parked {
		t.Fatal("parked = true for an uncontended acquisition")
	}
}
