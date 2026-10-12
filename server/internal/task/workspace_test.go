package task

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/conversation"
)

// The workspace embeds one Run timeline per Run the returned message window
// refers to. Every item names a Run now — the user's words name the Run that
// consumed them, not only an answer names the Run that wrote it — so a turn
// whose Agent has not spoken yet still gets its Run into the set, and the two
// items of one turn share a single entry.
func TestWorkspaceRunIDsAreTheWindowsDistinctRunsInOrder(t *testing.T) {
	t.Parallel()

	view := func(runID uuid.UUID, itemType conversation.ItemType) conversation.MessageView {
		return conversation.MessageView{Message: conversation.MessageResponse{
			ID: uuid.Must(uuid.NewV7()), RunID: runID, Type: itemType,
		}}
	}
	got := workspaceRunIDs([]conversation.MessageView{
		view(uuid.MustParse("00000000-0000-4000-8000-000000000011"), conversation.ItemTypeUserMessage),
		view(uuid.MustParse("00000000-0000-4000-8000-000000000011"), conversation.ItemTypeAgentText),
		view(uuid.MustParse("00000000-0000-4000-8000-000000000012"), conversation.ItemTypeUserMessage),
		view(uuid.MustParse("00000000-0000-4000-8000-000000000012"), conversation.ItemTypeAgentText),
		// The turn in flight: its words are committed, its answer is not.
		view(uuid.MustParse("00000000-0000-4000-8000-000000000013"), conversation.ItemTypeUserMessage),
	})
	if want := []uuid.UUID{uuid.MustParse("00000000-0000-4000-8000-000000000011"), uuid.MustParse("00000000-0000-4000-8000-000000000012"), uuid.MustParse("00000000-0000-4000-8000-000000000013")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("workspaceRunIDs() = %v, want %v", got, want)
	}
	if empty := workspaceRunIDs(nil); len(empty) != 0 {
		t.Fatalf("workspaceRunIDs(nil) = %v, want no Runs", empty)
	}
}
