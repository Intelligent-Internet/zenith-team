package conversation

import (
	"testing"

	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
)

func TestHuddleWorkState(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, run, want             string
		online, input, continuation bool
	}{
		{name: "missing", want: "unavailable"},
		{name: "offline", run: "queued", want: "waiting_for_runtime"},
		{name: "queued", run: "queued", online: true, want: "queued"},
		{name: "preparing", run: "dispatched", want: "preparing"},
		{name: "running", run: "running", want: "running"},
		{name: "question before completion", run: "running", input: true, want: "waiting_for_input"},
		{name: "question after completion", run: "completed", input: true, want: "waiting_for_input"},
		{name: "continuation pending", run: "completed", continuation: true, want: "resuming"},
		{name: "completed is not task done", run: "completed", want: "completed"},
		{name: "failure", run: "failed", want: "failed"},
		{name: "cancelled ignores stale question", run: "cancelled", input: true, want: "cancelled"},
		{name: "directory", run: "waiting_local_directory", want: "waiting_local_directory"},
		{name: "deferred", run: "deferred", want: "deferred"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := huddleWorkState(tt.run, tt.online, tt.input, tt.continuation); got != tt.want {
				t.Fatalf("state = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestHuddleWorkNoticeEligibility(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, status, want              string
		result, input, newer, announced bool
	}{
		{name: "result", status: "completed", result: true, want: "completed"},
		{name: "unproven completion", status: "completed"},
		{name: "input", status: "running", input: true, want: "waiting_for_input"},
		{name: "input precedes result", status: "completed", input: true, result: true, want: "waiting_for_input"},
		{name: "failed", status: "failed", input: true},
		{name: "queued", status: "queued"},
		{name: "superseded", status: "completed", result: true, newer: true},
		{name: "already claimed", status: "completed", result: true, announced: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			row := generated.ReadHuddleWorkRow{
				ProposalID: uuid.New(), RunID: uuid.New(), RunStatus: tt.status,
				HasResult: tt.result, WaitingInput: tt.input, NewerTaskExecution: tt.newer,
			}
			if tt.announced {
				row.VoiceNoticeKeys = []string{row.ProposalID.String() + ":" + row.RunID.String() + ":completed"}
			}
			got := huddleWorkNotice(row)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("notice = %+v, want none", got)
				}
				return
			}
			if got == nil || got.State != tt.want {
				t.Fatalf("notice = %+v, want %s", got, tt.want)
			}
		})
	}
}

func TestHuddleWorkNoticeCap(t *testing.T) {
	t.Parallel()
	row := generated.ReadHuddleWorkRow{
		ProposalID: uuid.New(), RunID: uuid.New(), RunStatus: "completed", HasResult: true,
		VoiceNoticeKeys: make([]string, 128),
	}
	if got := huddleWorkNotice(row); got != nil {
		t.Fatalf("capped notice = %+v, want none", got)
	}
}
