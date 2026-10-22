package wiki

import (
	"testing"

	"github.com/google/uuid"
)

func TestOfflineTextUpdatesConverge(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, base, left, right, want string }{
		{"separate passages", "Old start. Middle. Old end.", "New start. Middle. Old end.", "Old start. Middle. Human end.", "New start. Middle. Human end."},
		{"overlapping replacement", "Price: $10", "Price: $20", "Price: $30", ""},
		{"same position insertion", "Hello world", "Hello big world", "Hello new world", ""},
		{"unicode", "Hi 🌍. Goodbye.", "Hi 🌏. Goodbye.", "Hi 🌍. Bye.", "Hi 🌏. Bye."},
		{"independent marks", "Start middle end", "**Start** middle end", "Start middle **end**", "**Start** middle **end**"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := BlocksFromMarkdown(tc.base)
			AssignBlockIDs(&root, uuid.NewString)
			base, err := EncodeBlocks(root)
			if err != nil {
				t.Fatal(err)
			}
			id, ok := root.Content[0].Attrs["blockId"].(string)
			if !ok {
				t.Fatal("missing block ID")
			}
			delta := func(markdown string) []byte {
				block := root.Content[0]
				block.Content = BlocksFromMarkdown(markdown).Content[0].Content
				updates, err := BlockOperationUpdates(
					base,
					[]BlockOperation{
						{
							Type:         "update",
							BlockID:      id,
							ExpectedHash: BlockHash(root.Content[0]),
							Content:      &block,
						},
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				if len(updates[0]) == 0 {
					t.Fatal("missing offline update")
				}
				return updates[0]
			}
			left, right := delta(tc.left), delta(tc.right)
			merge := func(first, second []byte) ([]byte, BlockNode) {
				state, _, _, err := MergeBlocks(base, first)
				if err != nil {
					t.Fatal(err)
				}
				state, root, _, err := MergeBlocks(state, second)
				if err != nil {
					t.Fatal(err)
				}
				return state, root
			}
			state, forward := merge(left, right)
			_, reverse := merge(right, left)
			if BlockHash(forward) != BlockHash(reverse) {
				t.Fatalf(
					"different order diverged: %s vs %s",
					BlocksMarkdown(forward),
					BlocksMarkdown(reverse),
				)
			}
			_, replayed, _, err := MergeBlocks(state, left)
			if err != nil || BlockHash(replayed) != BlockHash(forward) {
				t.Fatalf("replay changed content: %+v, %v", replayed, err)
			}
			if tc.want != "" && BlocksMarkdown(forward) != tc.want+"\n\n" {
				t.Fatalf("merge = %q, want %q", BlocksMarkdown(forward), tc.want)
			}
			if tc.want == "" &&
				(BlockHash(forward) == BlockHash(root) || BlocksMarkdown(forward) == tc.left+"\n\n" || BlocksMarkdown(forward) == tc.right+"\n\n") {
				t.Fatalf("concurrent insertion was lost: %q", BlocksMarkdown(forward))
			}
		})
	}
}

func TestTextUpdateRetainsFormatting(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ before, after string }{
		{"First **bold** last", "New **bold** ending"},
		{"First **bold** last", "First bold last"},
		{"First plain last", "First **plain** last"},
		{"🌍 **bold** last", "🌏 **bold and more** last"},
		{"All text", ""},
		{"", "New text"},
	} {
		t.Run(tc.before+" to "+tc.after, func(t *testing.T) {
			t.Parallel()
			root := BlocksFromMarkdown(tc.before)
			if len(root.Content) == 0 {
				root.Content = []BlockNode{{Type: "paragraph"}}
			}
			AssignBlockIDs(&root, uuid.NewString)
			base, err := EncodeBlocks(root)
			if err != nil {
				t.Fatal(err)
			}
			block := root.Content[0]
			blockID, ok := block.Attrs["blockId"].(string)
			if !ok {
				t.Fatal("paragraph block ID is not a string")
			}
			after := BlocksFromMarkdown(tc.after)
			block.Content = nil
			if len(after.Content) > 0 {
				block.Content = after.Content[0].Content
			}
			_, got, err := ApplyBlockOperations(
				base,
				[]BlockOperation{
					{
						Type:         "update",
						BlockID:      blockID,
						ExpectedHash: BlockHash(root.Content[0]),
						Content:      &block,
					},
				},
			)
			if err != nil || BlockHash(got.Content[0]) != BlockHash(block) {
				t.Fatalf("update = %#v, %v; want %#v", got, err, block)
			}
		})
	}
}

func TestOfflineUpdatesAllowPartialApproval(t *testing.T) {
	t.Parallel()
	root := BlocksFromMarkdown("First paragraph\n\nSecond paragraph")
	AssignBlockIDs(&root, uuid.NewString)
	base, err := EncodeBlocks(root)
	if err != nil {
		t.Fatal(err)
	}
	var operations []BlockOperation
	for _, original := range root.Content {
		proposed := original
		proposed.Content = []BlockNode{{Type: "text", Text: "Rewritten paragraph"}}
		id, ok := original.Attrs["blockId"].(string)
		if !ok {
			t.Fatal("missing block ID")
		}
		operations = append(
			operations,
			BlockOperation{
				Type:         "update",
				BlockID:      id,
				ExpectedHash: BlockHash(original),
				Content:      &proposed,
			},
		)
	}
	updates, err := BlockOperationUpdates(base, operations)
	if err != nil {
		t.Fatal(err)
	}
	for i, delta := range updates {
		_, merged, _, err := MergeBlocks(base, delta)
		if err != nil {
			t.Fatal(err)
		}
		if BlockHash(merged.Content[i]) != BlockHash(*operations[i].Content) ||
			BlockHash(merged.Content[1-i]) != BlockHash(root.Content[1-i]) {
			t.Fatalf("approval of %d changed sibling: %+v", i, merged)
		}
	}
	// Moving a block replaces its XML identity. A following update must use the
	// structural path, otherwise its delta would silently edit a deleted object.
	id, ok := root.Content[0].Attrs["blockId"].(string)
	if !ok {
		t.Fatal("missing block ID")
	}
	updates, err = BlockOperationUpdates(
		base,
		[]BlockOperation{{Type: "move", BlockID: id, ExpectedHash: BlockHash(root.Content[0])}, operations[0]},
	)
	if err != nil || len(updates[1]) != 0 {
		t.Fatalf("dependent text update = %v, %v", updates, err)
	}
}

func TestTextUpdatesAcrossExistingHardBreak(t *testing.T) {
	t.Parallel()
	id := uuid.NewString()
	root := BlockNode{
		Type: "doc",
		Content: []BlockNode{
			{
				Type:  "paragraph",
				Attrs: map[string]any{"blockId": id},
				Content: []BlockNode{
					{Type: "text", Text: "Hello world"},
					{Type: "hardBreak"},
					{Type: "text", Text: "Second line"},
				},
			},
		},
	}
	saved, err := EncodeBlocks(root)
	if err != nil {
		t.Fatal(err)
	}
	proposed := root.Content[0]
	proposed.Content = []BlockNode{
		{Type: "text", Text: "Bonjour world"},
		{Type: "hardBreak"},
		{Type: "text", Text: "Second line"},
	}
	update := BlockOperation{
		Type:         "update",
		BlockID:      id,
		ExpectedHash: BlockHash(root.Content[0]),
		Content:      &proposed,
	}
	deltas, err := BlockOperationUpdates(saved, []BlockOperation{update})
	if err != nil || len(deltas[0]) == 0 {
		t.Fatalf("text update missing: %v", err)
	}
	human := root.Content[0]
	human.Content = []BlockNode{
		{Type: "text", Text: "Hello world"},
		{Type: "hardBreak"},
		{Type: "text", Text: "Second edited line"},
	}
	live, _, err := ApplyBlockOperations(
		saved,
		[]BlockOperation{
			{Type: "update", BlockID: id, ExpectedHash: BlockHash(root.Content[0]), Content: &human},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, merged, _, err := MergeBlocks(live, deltas[0])
	if err != nil {
		t.Fatal(err)
	}
	want := BlockNode{
		Type:  "paragraph",
		Attrs: map[string]any{"blockId": id},
		Content: []BlockNode{
			{Type: "text", Text: "Bonjour world"},
			{Type: "hardBreak"},
			{Type: "text", Text: "Second edited line"},
		},
	}
	if BlockHash(merged.Content[0]) != BlockHash(want) {
		t.Fatalf("merged = %+v, want %+v", merged.Content[0], want)
	}
}
