package canvas

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/reearth/ygo/crdt"
)

func TestBoardNoteWireConvergence(t *testing.T) {
	t.Parallel()
	var fixture struct {
		ItemID       uuid.UUID `json:"itemId"`
		Base         []byte    `json:"base"`
		UpdateA      []byte    `json:"updateA"`
		UpdateB      []byte    `json:"updateB"`
		UndoA        []byte    `json:"undoA"`
		Expected     string    `json:"expected"`
		ExpectedUndo string    `json:"expectedUndo"`
	}
	data, err := os.ReadFile("testdata/board-note-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		first, second []byte
	}{
		{"A then B", fixture.UpdateA, fixture.UpdateB},
		{"B then A", fixture.UpdateB, fixture.UpdateA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state, _, err := mergeBoardNote(fixture.ItemID, fixture.Base, tc.first)
			if err != nil {
				t.Fatal(err)
			}
			state, got, err := mergeBoardNote(fixture.ItemID, state, tc.second)
			if err != nil {
				t.Fatal(err)
			}
			if got != fixture.Expected {
				t.Fatalf("text = %q, want %q", got, fixture.Expected)
			}
			repeated, got, err := mergeBoardNote(fixture.ItemID, state, tc.second)
			if err != nil || got != fixture.Expected || !bytes.Equal(repeated, state) {
				t.Fatalf("retry changed accepted content: %q, %v", got, err)
			}
			_, got, err = mergeBoardNote(fixture.ItemID, state, fixture.UndoA)
			if err != nil || got != fixture.ExpectedUndo {
				t.Fatalf("personal undo = %q, %v; want %q", got, err, fixture.ExpectedUndo)
			}
		})
	}
}

func TestBoardNoteEmptyAndStructuralFence(t *testing.T) {
	t.Parallel()
	id := uuid.MustParse("5b78175d-061b-4986-96e3-54a7d640a113")
	state, err := newBoardNote(id, "")
	if err != nil {
		t.Fatal(err)
	}
	doc := crdt.New()
	defer doc.Destroy()
	if err = crdt.ApplyUpdateV1(doc, state, nil); err != nil {
		t.Fatal(err)
	}
	p := doc.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement)
	text := p.Children()[0].(*crdt.YXmlText)
	doc.Transact(func(tx *crdt.Transaction) { text.Insert(tx, 0, "Xin chào\n\nDòng cuối\n", nil) })
	_, got, err := mergeBoardNote(id, state, doc.EncodeStateAsUpdate())
	if err != nil || got != "Xin chào\n\nDòng cuối\n" {
		t.Fatalf("text = %q, %v", got, err)
	}
	fragment := doc.GetXmlFragment("content")
	doc.Transact(func(tx *crdt.Transaction) {
		fragment.InsertElement(tx, 1, crdt.NewYXmlElement("paragraph"))
	})
	if _, _, err = mergeBoardNote(id, state, doc.EncodeStateAsUpdate()); err == nil {
		t.Fatal("structural edit accepted")
	}
	if _, _, err = mergeBoardNote(id, state, []byte{255, 255, 255}); err == nil {
		t.Fatal("malformed update accepted")
	}
}
