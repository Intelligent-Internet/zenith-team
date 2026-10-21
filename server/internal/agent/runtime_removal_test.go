package agent

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// The receipt is the wire shape the remove route answers: four camelCase
// counts a client shows verbatim ("2 agents moved, 1 run cancelled").
func TestRuntimeRemovalSerializesTheReceiptShape(t *testing.T) {
	t.Parallel()
	removed := uuid.MustParse("0198bb00-0000-7000-8000-00000000000a")

	raw, err := json.Marshal(RuntimeRemoval{
		RemovedRuntimeID:   removed,
		MovedBindingCount:  2,
		RetargetedRunCount: 3,
		CancelledRunCount:  0,
	})
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	want := `{"removedRuntimeId":"` + removed.String() +
		`","movedBindingCount":2,"retargetedRunCount":3,"cancelledRunCount":0}`
	if string(raw) != want {
		t.Fatalf("receipt = %s, want %s", raw, want)
	}

	// Nothing bound, nothing queued: every count is present and zero, so a
	// client never branches on a missing key.
	raw, err = json.Marshal(RuntimeRemoval{RemovedRuntimeID: removed})
	if err != nil {
		t.Fatalf("marshal empty receipt: %v", err)
	}
	if string(raw) != `{"removedRuntimeId":"`+removed.String()+
		`","movedBindingCount":0,"retargetedRunCount":0,"cancelledRunCount":0}` {
		t.Fatalf("empty receipt = %s, want every count present", raw)
	}
}

func TestRemovalRuntimesFindsTheSourceAndTargetAmongLockedRows(t *testing.T) {
	t.Parallel()
	source := uuid.MustParse("0198bb00-0000-7000-8000-00000000000a")
	target := uuid.MustParse("0198bb00-0000-7000-8000-00000000000b")
	locked := []generated.Runtime{{ID: source, Provider: "codex"}, {ID: target, Provider: "claude"}}

	gotSource, gotTarget, found := removalRuntimes(locked, source, &target)
	if !found || gotSource.ID != source || gotTarget.ID != target {
		t.Fatalf("removalRuntimes(both) = %s / %s / %v", gotSource.ID, gotTarget.ID, found)
	}
	// No target planned: the source alone is enough.
	gotSource, _, found = removalRuntimes(locked[:1], source, nil)
	if !found || gotSource.ID != source {
		t.Fatalf("removalRuntimes(no target) = %s / %v", gotSource.ID, found)
	}
	// A planned target the lock did not return — foreign, unknown, or already
	// removed — is not found, and so is a missing source.
	if _, _, found := removalRuntimes(locked[:1], source, &target); found {
		t.Fatal("removalRuntimes(missing target) found = true")
	}
	if _, _, found := removalRuntimes(locked[1:], source, &target); found {
		t.Fatal("removalRuntimes(missing source) found = true")
	}
	if _, _, found := removalRuntimes(nil, source, nil); found {
		t.Fatal("removalRuntimes(nothing locked) found = true")
	}
}
