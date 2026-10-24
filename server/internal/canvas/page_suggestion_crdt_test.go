package canvas

import (
	"encoding/json"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/google/uuid"
)

func TestPageSuggestionAlreadyReflectedText(t *testing.T) {
	base := wiki.BlocksFromMarkdown("Hello world")
	wiki.AssignBlockIDs(&base, uuid.NewString)
	bs, err := wiki.EncodeBlocks(base)
	if err != nil {
		t.Fatal(err)
	}
	next := base.Content[0]
	next.Content = []wiki.BlockNode{{Type: "text", Text: "Hello big world"}}
	op := wiki.BlockOperation{
		Type:         "update",
		BlockID:      next.Attrs["blockId"].(string),
		ExpectedHash: wiki.BlockHash(base.Content[0]),
		Content:      &next,
	}
	updates, err := wiki.BlockOperationUpdates(bs, []wiki.BlockOperation{op})
	if err != nil {
		t.Fatal(err)
	}
	// A person independently makes exactly the proposed edit.
	live, root, err := wiki.ApplyBlockOperations(bs, []wiki.BlockOperation{op})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(base)
	ops, _ := json.Marshal([]wiki.BlockOperation{op})
	deltas, _ := json.Marshal(updates)
	row := generated.ProjectPageChange{
		ID:            uuid.New(),
		BeforeContent: before,
		Operations:    ops,
		CrdtUpdates:   deltas,
		Decisions:     []byte(`{}`),
		Status:        "proposed",
	}
	review, err := pageSuggestionState(row, pageSuggestionSnapshot{root: root, state: live}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, accepted, err := applyPageSuggestions(live, review, []int{0})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf(
		"status=%s; preview=%q; accepted=%q",
		review.suggestions[0].Status,
		wiki.BlocksMarkdown(*review.suggestions[0].After),
		wiki.BlocksMarkdown(accepted),
	)
	if wiki.BlockHash(root) != wiki.BlockHash(accepted) {
		t.Fatal("accepting already-present proposal duplicated its text")
	}
}

func TestPageSuggestionUnrelatedStructuralEdit(t *testing.T) {
	base := wiki.BlocksFromMarkdown("First paragraph")
	wiki.AssignBlockIDs(&base, uuid.NewString)
	bs, err := wiki.EncodeBlocks(base)
	if err != nil {
		t.Fatal(err)
	}
	next := base.Content[0]
	next.Content = []wiki.BlockNode{{Type: "text", Text: "Revised paragraph"}}
	op := wiki.BlockOperation{
		Type:         "update",
		BlockID:      next.Attrs["blockId"].(string),
		ExpectedHash: wiki.BlockHash(base.Content[0]),
		Content:      &next,
	}
	updates, err := wiki.BlockOperationUpdates(bs, []wiki.BlockOperation{op})
	if err != nil {
		t.Fatal(err)
	}
	addition := wiki.BlockNode{
		Type:    "paragraph",
		Attrs:   map[string]any{"blockId": uuid.NewString()},
		Content: []wiki.BlockNode{{Type: "text", Text: "Unrelated new paragraph"}},
	}
	live, root, err := wiki.ApplyBlockOperations(
		bs,
		[]wiki.BlockOperation{{Type: "insert", AfterID: op.BlockID, Content: &addition}},
	)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(base)
	ops, _ := json.Marshal([]wiki.BlockOperation{op})
	deltas, _ := json.Marshal(updates)
	row := generated.ProjectPageChange{
		ID:            uuid.New(),
		BeforeContent: before,
		Operations:    ops,
		CrdtUpdates:   deltas,
		Decisions:     []byte(`{}`),
		Result:        []byte(`{"sequence":1}`),
		Status:        "proposed",
	}
	review, err := pageSuggestionState(
		row,
		pageSuggestionSnapshot{root: root, state: live, structureSequence: 2},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("unchanged target and placement, suggestion status=%s", review.suggestions[0].Status)
	if review.suggestions[0].Status != "pending" {
		t.Fatal("unrelated insertion invalidated a text suggestion whose target is unchanged")
	}
}

func TestPageSuggestionRecreatedTarget(t *testing.T) {
	t.Parallel()
	base := wiki.BlocksFromMarkdown("Hello world")
	wiki.AssignBlockIDs(&base, uuid.NewString)
	saved, err := wiki.EncodeBlocks(base)
	if err != nil {
		t.Fatal(err)
	}
	proposed := base.Content[0]
	proposed.Content = []wiki.BlockNode{{Type: "text", Text: "Hello wonderful world"}}
	op := wiki.BlockOperation{
		Type:         "update",
		BlockID:      proposed.Attrs["blockId"].(string),
		ExpectedHash: wiki.BlockHash(base.Content[0]),
		Content:      &proposed,
	}
	updates, err := wiki.BlockOperationUpdates(saved, []wiki.BlockOperation{op})
	if err != nil {
		t.Fatal(err)
	}
	// Deleting and reinserting the same block ID creates a different XML target.
	live, root, err := wiki.ApplyBlockOperations(
		saved,
		[]wiki.BlockOperation{
			{Type: "move", BlockID: op.BlockID, ExpectedHash: wiki.BlockHash(base.Content[0])},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(base)
	ops, _ := json.Marshal([]wiki.BlockOperation{op})
	deltas, _ := json.Marshal(updates)
	row := generated.ProjectPageChange{
		ID:            uuid.New(),
		BeforeContent: before,
		Operations:    ops,
		CrdtUpdates:   deltas,
		Decisions:     []byte(`{}`),
	}
	review, err := pageSuggestionState(row, pageSuggestionSnapshot{root: root, state: live}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := review.suggestions[0].Status; got != "needs_update" {
		t.Fatalf("status = %s, want needs_update", got)
	}
}
