package canvas

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/reearth/ygo/crdt"

	"boardly.dev/boardly/server/internal/wiki"
)

func TestDocumentStructuralConflict(t *testing.T) {
	t.Parallel()
	root := wiki.BlocksFromMarkdown("Hello world")
	wiki.AssignBlockIDs(&root, uuid.NewString)
	base, err := wiki.EncodeBlocks(root)
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	textDoc, splitDoc, replacementDoc := crdt.New(), crdt.New(), crdt.New()
	defer textDoc.Destroy()
	defer splitDoc.Destroy()
	defer replacementDoc.Destroy()
	for _, doc := range []*crdt.Doc{textDoc, splitDoc, replacementDoc} {
		if err := crdt.ApplyUpdateV1(doc, base, nil); err != nil {
			t.Fatal(err)
		}
	}
	text := textDoc.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement).Children()[0].(*crdt.YXmlText)
	textDoc.Transact(func(tx *crdt.Transaction) { text.Insert(tx, 8, "X", nil) })
	fragment := splitDoc.GetXmlFragment("content")
	splitDoc.Transact(func(tx *crdt.Transaction) {
		old := fragment.Children()[0].(*crdt.YXmlElement).Children()[0].(*crdt.YXmlText)
		old.Delete(tx, 6, 5)
		paragraph := crdt.NewYXmlElement("paragraph")
		fragment.InsertElement(tx, 1, paragraph)
		paragraph.SetAttribute(tx, "blockId", uuid.NewString())
		tail := crdt.NewYXmlText()
		paragraph.InsertText(tx, 0, tail)
		tail.Insert(tx, 0, "world", nil)
	})
	replacement := replacementDoc.GetXmlFragment("content")
	replacementDoc.Transact(func(tx *crdt.Transaction) {
		replacement.Delete(tx, 0, 1)
		paragraph := crdt.NewYXmlElement("paragraph")
		replacement.InsertElement(tx, 0, paragraph)
		paragraph.SetAttribute(tx, "blockId", root.Content[0].Attrs["blockId"].(string))
		body := crdt.NewYXmlText()
		paragraph.InsertText(tx, 0, body)
		body.Insert(tx, 0, "Hello world", nil)
	})
	for _, tc := range []struct {
		name          string
		first, second []byte
		structural    bool
	}{
		{"text before split", textDoc.EncodeStateAsUpdate(), splitDoc.EncodeStateAsUpdate(), false},
		{"split before text", splitDoc.EncodeStateAsUpdate(), textDoc.EncodeStateAsUpdate(), true},
		{"replacement with unchanged block ID", replacementDoc.EncodeStateAsUpdate(), textDoc.EncodeStateAsUpdate(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, merged, markdown, structural, err := mergeBlockEdit(
				Document{State: base, Content: content, Sequence: 1, StructureSequence: 1}, tc.first, 1,
			)
			if err != nil {
				t.Fatal(err)
			}
			if structural != tc.structural {
				t.Fatalf("structural = %v, want %v", structural, tc.structural)
			}
			content, err := json.Marshal(merged)
			if err != nil {
				t.Fatal(err)
			}
			saved := Document{
				State:             state,
				Content:           content,
				Markdown:          markdown,
				Sequence:          2,
				StructureSequence: 1,
			}
			if tc.structural {
				saved.StructureSequence = 2
			}
			if _, _, _, _, err := mergeBlockEdit(saved, tc.second, 1); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale structural merge error = %v, want conflict", err)
			}
			if _, _, _, _, err := mergeBlockEdit(saved, tc.first, 1); err != nil {
				t.Fatalf("acknowledgement replay = %v", err)
			}
		})
	}
}

func TestDocumentIncrementalUpdate(t *testing.T) {
	t.Parallel()
	root := wiki.BlocksFromMarkdown("Hello")
	wiki.AssignBlockIDs(&root, uuid.NewString)
	base, err := wiki.EncodeBlocks(root)
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	client := crdt.New()
	defer client.Destroy()
	if err := crdt.ApplyUpdateV1(client, base, nil); err != nil {
		t.Fatal(err)
	}
	vector := client.StateVector()
	text := client.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement).Children()[0].(*crdt.YXmlText)
	client.Transact(func(tx *crdt.Transaction) { text.Insert(tx, 5, " world", nil) })
	update := crdt.EncodeStateAsUpdateV1(client, vector)
	state, _, markdown, structural, err := mergeBlockEdit(Document{State: base, Content: content}, update, 0)
	if err != nil || structural || !strings.Contains(markdown, "Hello world") {
		t.Fatalf("incremental merge = %q, structural %v, error %v", markdown, structural, err)
	}
	peer := crdt.New()
	defer peer.Destroy()
	if err := crdt.ApplyUpdateV1(peer, state, nil); err != nil {
		t.Fatal(err)
	}
	peerVector := peer.StateVector()
	peerText := peer.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement).Children()[0].(*crdt.YXmlText)
	peer.Transact(func(tx *crdt.Transaction) {
		peerText.Delete(tx, 0, 6)
		peerText.Insert(tx, 5, "!", nil)
	})
	state, _, _, structural, err = mergeBlockEdit(
		Document{State: state, Content: content, Sequence: 1},
		crdt.EncodeStateAsUpdateV1(peer, peerVector),
		1,
	)
	if err != nil || structural {
		t.Fatalf("peer edit: structural %v, error %v", structural, err)
	}
	delta, accepted, err := wiki.BlockStateDelta(state, crdt.EncodeStateVectorV1(client))
	if err != nil || !wiki.ValidBlockStateVector(accepted) {
		t.Fatalf("delta: %v", err)
	}
	if err := crdt.ApplyUpdateV1(client, delta, nil); err != nil {
		t.Fatal(err)
	}
	if got := text.ToString(); got != "world!" {
		t.Fatalf("missing inserts/deletes = %q, want world!", got)
	}
	if _, _, _, _, err := mergeBlockEdit(
		Document{State: state, Content: content, Sequence: 1},
		update,
		0,
	); err != nil {
		t.Fatalf("idempotent retry = %v", err)
	}
	if wiki.ValidBlockStateVector([]byte{255, 255, 255}) {
		t.Fatal("malformed vector accepted")
	}
}

func TestDocumentMergeInteroperatesWithYjs(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/yjs-merge.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Base, UpdateA, UpdateB []byte
		Expected               string
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, order := range []struct {
		name          string
		first, second []byte
	}{{"A then B", fixture.UpdateA, fixture.UpdateB}, {"B then A", fixture.UpdateB, fixture.UpdateA}} {
		t.Run(order.name, func(t *testing.T) {
			state, _, err := mergeDocument(fixture.Base, order.first)
			if err != nil {
				t.Fatal(err)
			}
			state, body, err := mergeDocument(state, order.second)
			if err != nil || body != fixture.Expected {
				t.Fatalf("merge = %q, %v; want %q", body, err, fixture.Expected)
			}
			_, body, err = mergeDocument(state, order.first)
			if err != nil || body != fixture.Expected {
				t.Fatalf("replay = %q, %v", body, err)
			}
		})
	}
	if _, _, err = mergeDocument(fixture.Base, []byte{255, 255, 255}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed update: %v", err)
	}
}

// Retained interoperability fixture for the pre-migration backup format.
func mergeDocument(saved, update []byte) (state []byte, markdown string, err error) {
	// The decoder has structural limits; recover protects the API boundary from
	// malformed third-party binary input without recovering application mutations.
	defer func() {
		if recover() != nil {
			state = nil
			markdown = ""
			err = ErrInvalid
		}
	}()
	doc := crdt.New(crdt.WithMaxPendingItems(10000))
	defer doc.Destroy()
	doc.GetText("markdown")
	if err = crdt.ApplyUpdateV1(doc, saved, nil); err != nil {
		return nil, "", err
	}
	if err = crdt.ApplyUpdateV1(doc, update, nil); err != nil {
		return nil, "", ErrInvalid
	}
	if pending := doc.PendingStats(); pending.Items != 0 || pending.DeleteRanges != 0 {
		return nil, "", ErrInvalid
	}
	markdown = doc.GetText("markdown").ToString()
	if len(markdown) > 262144 {
		return nil, "", ErrLimit
	}
	state = doc.EncodeStateAsUpdate()
	if len(state) > 2<<20 || len(doc.GetSubdocs()) > 0 {
		return nil, "", ErrLimit
	}
	return state, markdown, nil
}
