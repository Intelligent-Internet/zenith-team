//go:build integration

package execution

import "testing"

func TestStudioAuthWaitKeepsTheLiveRunAndResumesWithoutRequeue(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := t.Context()
	id := h.seedQueuedRun(t, h.seedDirectConversation(t))
	dbtestUpdateRunStatus(t, h, id, "running")
	before := mustRun(t, h, id)
	waiting, err := h.svc.WaitForStudioAuth(ctx, id, true)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != "running" || waiting.WaitReason.String != "studio_auth_required" {
		t.Fatalf("wait = %+v", waiting)
	}
	if _, err := h.svc.WaitForStudioAuth(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if runs, err := h.svc.PromoteDueDeferredRuns(ctx); err != nil || len(runs) != 0 {
		t.Fatalf("waiting Run was requeued: %v, %v", runs, err)
	}
	resumed, err := h.svc.WaitForStudioAuth(ctx, id, false)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != "running" || resumed.WaitReason.Valid || resumed.Attempt != before.Attempt || string(resumed.Context) != string(before.Context) {
		t.Fatalf("resume changed live execution: %+v", resumed)
	}
	if _, err := h.svc.CancelRun(ctx, id, "user stopped execution"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.WaitForStudioAuth(ctx, id, false); err == nil {
		t.Fatal("resumed cancelled Run")
	}
}

func TestStudioAuthWaitRequiresRunningAgent(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"queued", "dispatched", "waiting_local_directory", "deferred", "cancelled", "completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			h := newRunHarness(t)
			id := h.seedQueuedRun(t, h.seedDirectConversation(t))
			dbtestUpdateRunStatus(t, h, id, status)
			if _, err := h.svc.WaitForStudioAuth(t.Context(), id, true); err == nil {
				t.Fatal("auth prompt accepted without a running Agent")
			}
			assertTerminalTestRunStatus(t, h, id, status)
		})
	}
}
