package canvas

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/reearth/ygo/crdt"

	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

func TestPageCommentAnchorFollowsTextEdits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, text, quote, insert, want string
		start, count                    int
		removeBlock                     bool
	}{
		{name: "delete selection", text: "Hello world", quote: "world", start: 6, count: 5},
		{name: "partial deletion", text: "Hello world", quote: "world", start: 8, count: 3, want: "wo"},
		{name: "insert within selection", text: "Hello world", quote: "world", start: 8, insert: "X", want: "woXrld"},
		{name: "edit before selection", text: "Hello world", quote: "world", start: 0, count: 5, insert: "Hi", want: "world"},
		{name: "delete block", text: "Hello world", quote: "world", removeBlock: true},
		{name: "unicode", text: "Hi 🌍 world", quote: "🌍 world", start: 6, count: 5, want: "🌍"},
		{name: "ambiguous repeated quote", text: "world world", quote: "world", start: 0, count: 5, want: "world"},
		{name: "retain unrelated quote", text: "Hello world", quote: "Hello", start: 6, count: 5, want: "Hello"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := wiki.BlocksFromMarkdown(tc.text)
			wiki.AssignBlockIDs(&root, uuid.NewString)
			before, err := wiki.EncodeBlocks(root)
			if err != nil {
				t.Fatal(err)
			}
			doc := crdt.New()
			defer doc.Destroy()
			if err := crdt.ApplyUpdateV1(doc, before, nil); err != nil {
				t.Fatal(err)
			}
			fragment := doc.GetXmlFragment("content")
			element, ok := fragment.Children()[0].(*crdt.YXmlElement)
			if !ok {
				t.Fatal("missing paragraph")
			}
			text, ok := element.Children()[0].(*crdt.YXmlText)
			if !ok {
				t.Fatal("missing text")
			}
			doc.Transact(func(tx *crdt.Transaction) {
				if tc.removeBlock {
					fragment.Delete(tx, 0, 1)
					return
				}
				if tc.count > 0 {
					text.Delete(tx, tc.start, tc.count)
				}
				if tc.insert != "" {
					text.Insert(tx, tc.start, tc.insert, nil)
				}
			})
			anchor, err := json.Marshal(
				map[string]any{
					"type":    "textQuote",
					"exact":   tc.quote,
					"blockId": root.Content[0].Attrs["blockId"],
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			got, err := reconcileCommentQuotes(before, doc.EncodeStateAsUpdate(), []json.RawMessage{anchor})
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if string(got[0]) != `{"type":"version"}` {
					t.Fatalf("anchor = %s, want page-level discussion", got[0])
				}
				return
			}
			var result struct {
				Exact string `json:"exact"`
			}
			if err := json.Unmarshal(got[0], &result); err != nil {
				t.Fatal(err)
			}
			if result.Exact != tc.want {
				t.Fatalf("quote = %q, want %q", result.Exact, tc.want)
			}
		})
	}
}

func TestPageCommentAnchorDoesNotFollowDuplicateInAnotherBlock(t *testing.T) {
	t.Parallel()
	root := wiki.BlocksFromMarkdown("Same words\n\nSame words")
	wiki.AssignBlockIDs(&root, uuid.NewString)
	before, err := wiki.EncodeBlocks(root)
	if err != nil {
		t.Fatal(err)
	}
	doc := crdt.New()
	defer doc.Destroy()
	if err := crdt.ApplyUpdateV1(doc, before, nil); err != nil {
		t.Fatal(err)
	}
	fragment := doc.GetXmlFragment("content")
	anchor, err := json.Marshal(
		map[string]any{"type": "textQuote", "exact": "Same words", "blockId": root.Content[1].Attrs["blockId"]},
	)
	if err != nil {
		t.Fatal(err)
	}
	doc.Transact(func(tx *crdt.Transaction) { fragment.Delete(tx, 0, 1) })
	got, err := reconcileCommentQuotes(before, doc.EncodeStateAsUpdate(), []json.RawMessage{anchor})
	if err != nil {
		t.Fatal(err)
	}
	if string(got[0]) != string(anchor) {
		t.Fatalf("anchor = %s, want %s", got[0], anchor)
	}
	doc.Transact(func(tx *crdt.Transaction) { fragment.Delete(tx, 0, 1) })
	got, err = reconcileCommentQuotes(before, doc.EncodeStateAsUpdate(), []json.RawMessage{anchor})
	if err != nil {
		t.Fatal(err)
	}
	if string(got[0]) != `{"type":"version"}` {
		t.Fatalf("anchor = %s, want page-level discussion", got[0])
	}
}

func TestPageCommentSurvivesApprovedTextUpdate(t *testing.T) {
	t.Parallel()
	for _, blockType := range []string{"paragraph", "uiDocument"} {
		t.Run(blockType, func(t *testing.T) {
			t.Parallel()
			root := wiki.BlocksFromMarkdown("Old opening, quoted passage, old ending.")
			root.Content[0].Type = blockType
			if blockType == "uiDocument" {
				root.Content[0].Attrs = map[string]any{"catalogVersion": 1}
			}
			wiki.AssignBlockIDs(&root, uuid.NewString)
			before, err := wiki.EncodeBlocks(root)
			if err != nil {
				t.Fatal(err)
			}
			block := root.Content[0]
			blockID, ok := block.Attrs["blockId"].(string)
			if !ok {
				t.Fatal("paragraph block ID is not a string")
			}
			block.Content = []wiki.BlockNode{
				{Type: "text", Text: "New opening, quoted passage, new ending."},
			}
			after, _, err := wiki.ApplyBlockOperations(
				before,
				[]wiki.BlockOperation{
					{
						Type:         "update",
						BlockID:      blockID,
						ExpectedHash: wiki.BlockHash(root.Content[0]),
						Content:      &block,
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			anchor, _ := json.Marshal(
				map[string]any{
					"type":    "textQuote",
					"exact":   "quoted passage",
					"blockId": block.Attrs["blockId"],
				},
			)
			anchors, err := reconcileCommentQuotes(before, after, []json.RawMessage{anchor})
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Type  string `json:"type"`
				Exact string `json:"exact"`
			}
			if err = json.Unmarshal(anchors[0], &got); err != nil {
				t.Fatal(err)
			}
			if got.Type != "textQuote" || got.Exact != "quoted passage" {
				t.Fatalf("anchor = %s", anchors[0])
			}
		})
	}
}
