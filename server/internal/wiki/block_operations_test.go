package wiki

import (
	"bytes"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/reearth/ygo/crdt"
)

func TestBlockOperationsCanRestoreFirstPosition(t *testing.T) {
	t.Parallel()
	root := BlocksFromMarkdown("First\n\nSecond")
	AssignBlockIDs(&root, uuid.NewString)
	state, err := EncodeBlocks(root)
	if err != nil {
		t.Fatal(err)
	}
	first, second := root.Content[0], root.Content[1]
	removed, _, err := ApplyBlockOperations(
		state,
		[]BlockOperation{
			{Type: "remove", BlockID: first.Attrs["blockId"].(string), ExpectedHash: BlockHash(first)},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := ApplyBlockOperations(
		removed,
		[]BlockOperation{{Type: "insert", BeforeID: second.Attrs["blockId"].(string), Content: &first}},
	)
	if err != nil || len(got.Content) != 2 || BlockHash(got.Content[0]) != BlockHash(first) {
		t.Fatalf("restored first position = %+v, %v", got, err)
	}
}

func TestApplyBlockOperationsPreservesCollaboratorText(t *testing.T) {
	t.Parallel()
	root := BlocksFromMarkdown("First\n\nSecond")
	ids := []string{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"}
	i := 0
	AssignBlockIDs(&root, func() string { id := ids[i]; i++; return id })
	base, err := EncodeBlocks(root)
	if err != nil {
		t.Fatal(err)
	}
	peer := crdt.New()
	defer peer.Destroy()
	if err = crdt.ApplyUpdateV1(peer, base, nil); err != nil {
		t.Fatal(err)
	}
	text := peer.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement).Children()[0].(*crdt.YXmlText)
	peer.Transact(func(tx *crdt.Transaction) { text.Insert(tx, text.Len(), " from a person", nil) })
	insert := BlocksFromMarkdown("Result").Content[0]
	insert.Attrs = map[string]any{"blockId": "00000000-0000-4000-8000-000000000003"}
	state, _, err := ApplyBlockOperations(
		base,
		[]BlockOperation{{Type: "insert", AfterID: ids[1], Content: &insert}},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, got, _, err := MergeBlocks(state, peer.EncodeStateAsUpdate())
	if err != nil || len(got.Content) != 3 || BlocksMarkdown(got.Content[0]) != "First from a person\n\n" {
		t.Fatalf("merged content = %#v, %v; want preserved human text and inserted result", got, err)
	}
}

func TestApplyBlockOperationsChecksTargets(t *testing.T) {
	t.Parallel()
	root := BlocksFromMarkdown("Human recommendation")
	AssignBlockIDs(&root, func() string { return "00000000-0000-4000-8000-000000000001" })
	base, err := EncodeBlocks(root)
	if err != nil {
		t.Fatal(err)
	}
	for name, operation := range map[string]BlockOperation{
		"missing": {Type: "remove", BlockID: "00000000-0000-4000-8000-000000000002"},
		"stale":   {Type: "remove", BlockID: "00000000-0000-4000-8000-000000000001", ExpectedHash: "stale"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			state, _, err := ApplyBlockOperations(base, []BlockOperation{operation})
			if !errors.Is(err, ErrPageConflict) || len(state) > 0 {
				t.Fatalf("apply = %x, %v; want conflict without mutation", state, err)
			}
		})
	}
	state, _, err := ApplyBlockOperations(base, nil)
	if err != nil || !bytes.Equal(state, base) {
		t.Fatalf("empty apply = %v; want unchanged state", err)
	}
}

func TestTextUpdatePreservesOfflineEdits(t *testing.T) {
	t.Parallel()
	root := BlocksFromMarkdown("Old 🌍 opening, unchanged middle, old ending.")
	AssignBlockIDs(&root, uuid.NewString)
	base, err := EncodeBlocks(root)
	if err != nil {
		t.Fatal(err)
	}
	peer := crdt.New()
	defer peer.Destroy()
	if err = crdt.ApplyUpdateV1(peer, base, nil); err != nil {
		t.Fatal(err)
	}
	element, ok := peer.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement)
	if !ok {
		t.Fatal("paragraph is not an XML element")
	}
	text, ok := element.Children()[0].(*crdt.YXmlText)
	if !ok {
		t.Fatal("paragraph content is not XML text")
	}
	peer.Transact(func(tx *crdt.Transaction) { text.Insert(tx, 26, "shared ", nil) })
	updated := root.Content[0]
	blockID, ok := updated.Attrs["blockId"].(string)
	if !ok {
		t.Fatal("paragraph block ID is not a string")
	}
	updated.Content = []BlockNode{{Type: "text", Text: "New 🌍 opening, unchanged middle, new ending."}}
	state, _, err := ApplyBlockOperations(
		base,
		[]BlockOperation{
			{
				Type:         "update",
				BlockID:      blockID,
				ExpectedHash: BlockHash(root.Content[0]),
				Content:      &updated,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, got, _, structural, err := MergeBlockChanges(state, peer.EncodeStateAsUpdate())
	if err != nil || structural ||
		BlocksMarkdown(got) != "New 🌍 opening, unchanged shared middle, new ending.\n\n" {
		t.Fatalf("offline merge = %q, structural=%v, error=%v", BlocksMarkdown(got), structural, err)
	}
}

func TestTargetEditsIgnoreUnusedDestination(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"update", "remove"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := BlocksFromMarkdown("Keep this target\n\nUnrelated paragraph")
			AssignBlockIDs(&root, uuid.NewString)
			state, err := EncodeBlocks(root)
			if err != nil {
				t.Fatal(err)
			}
			block := root.Content[0]
			op := BlockOperation{
				Type:         kind,
				BlockID:      block.Attrs["blockId"].(string),
				ExpectedHash: BlockHash(block),
				AfterID:      uuid.NewString(),
			}
			if kind == "update" {
				op.Content = &block
			}
			if _, _, err := ApplyBlockOperations(state, []BlockOperation{op}); err != nil {
				t.Fatalf("unused deleted anchor blocked %s: %v", kind, err)
			}
		})
	}
}
