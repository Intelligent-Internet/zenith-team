package canvas

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/reearth/ygo/crdt"

	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

// Board note schema 1 has one immutable paragraph and one shared text object.
// Newlines are characters, so Enter never replaces the shared text structure.
func newBoardNote(itemID uuid.UUID, text string) ([]byte, error) {
	if len(text) > boardMaxNoteText {
		return nil, fmt.Errorf("board note exceeds text limit: %w", ErrBoardLimit)
	}
	doc := crdt.New()
	defer doc.Destroy()
	fragment := doc.GetXmlFragment("content")
	doc.Transact(func(tx *crdt.Transaction) {
		paragraph := crdt.NewYXmlElement("paragraph")
		fragment.InsertElement(tx, 0, paragraph)
		paragraph.SetAttribute(tx, "blockId", itemID.String())
		body := crdt.NewYXmlText()
		paragraph.InsertText(tx, 0, body)
		body.Insert(tx, 0, text, nil)
	})
	return doc.EncodeStateAsUpdate(), nil
}

// A semantic edit changes the existing shared text, so open human sessions keep
// their CRDT identity and can merge buffered edits after the revision fence.
func replaceBoardNote(itemID uuid.UUID, saved []byte, text string) ([]byte, error) {
	doc := crdt.New()
	defer doc.Destroy()
	if err := doc.ApplyUpdate(saved); err != nil {
		return nil, err
	}
	paragraph, ok := doc.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement)
	if !ok {
		return nil, ErrBoardInvalid
	}
	body, ok := paragraph.Children()[0].(*crdt.YXmlText)
	if !ok {
		return nil, ErrBoardInvalid
	}
	doc.Transact(func(tx *crdt.Transaction) {
		body.Delete(tx, 0, body.Len())
		body.Insert(tx, 0, text, nil)
	})
	state, _, err := mergeBoardNote(itemID, saved, doc.EncodeStateAsUpdate())
	return state, err
}

func mergeBoardNote(itemID uuid.UUID, saved, update []byte) ([]byte, string, error) {
	if len(update) > boardMaxTextUpdate || len(saved) > boardMaxNoteState {
		return nil, "", fmt.Errorf("board note exceeds state limit: %w", ErrBoardLimit)
	}
	state, root, _, structural, err := wiki.MergeBlockChanges(saved, update)
	if err != nil {
		return nil, "", err
	}
	if structural || len(root.Content) != 1 {
		return nil, "", fmt.Errorf("board note structure is immutable")
	}
	paragraph := root.Content[0]
	if paragraph.Type != "paragraph" || len(paragraph.Attrs) != 1 || paragraph.Attrs["blockId"] != itemID.String() {
		return nil, "", fmt.Errorf("invalid board note paragraph")
	}
	var text strings.Builder
	for _, node := range paragraph.Content {
		if node.Type != "text" || len(node.Marks) != 0 {
			return nil, "", fmt.Errorf("board note accepts plain text only")
		}
		text.WriteString(node.Text)
	}
	if text.Len() > boardMaxNoteText || len(state) > boardMaxNoteState {
		return nil, "", fmt.Errorf("board note exceeds state limit: %w", ErrBoardLimit)
	}
	return state, text.String(), nil
}
