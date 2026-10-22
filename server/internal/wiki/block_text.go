package wiki

import (
	"strings"
	"unicode/utf16"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/reearth/ygo/crdt"
)

// textEdits returns disjoint edits in original coordinates. Trimming first keeps
// ordinary passage edits cheap; the work bound avoids quadratic diffing of a
// large wholesale rewrite; unchanged outer passages retain their identities.
func textEdits(before, after []string) []difflib.OpCode {
	start := 0
	for start < len(before) && start < len(after) && before[start] == after[start] {
		start++
	}
	a, b := len(before), len(after)
	for a > start && b > start && before[a-1] == after[b-1] {
		a--
		b--
	}
	if a == start && b == start {
		return nil
	}
	if int64(a-start)*int64(b-start) > 4_000_000 {
		return []difflib.OpCode{{Tag: 'r', I1: start, I2: a, J1: start, J2: b}}
	}
	var edits []difflib.OpCode
	for _, op := range difflib.NewMatcherWithJunk(before[start:a], after[start:b], false, nil).GetOpCodes() {
		if op.Tag == 'e' {
			continue
		}
		op.I1 += start
		op.I2 += start
		op.J1 += start
		op.J2 += start
		edits = append(edits, op)
	}
	return edits
}

func textMarks(marks []BlockMark) map[string]any {
	attrs := map[string]any{}
	for _, mark := range marks {
		value := mark.Attrs
		if value == nil {
			value = map[string]any{}
		}
		attrs[mark.Type] = value
	}
	return attrs
}

// prepareBlockTextUpdate retains the existing XML text objects and unchanged character
// identities, including when an older writer created several adjacent objects.
// Non-text children use the ordinary structural update path.
func prepareBlockTextUpdate(element *crdt.YXmlElement, nodes []BlockNode) func(*crdt.Transaction) {
	switch element.NodeName {
	case "paragraph", "heading", "codeBlock", "uiDocument":
	default:
		return nil
	}
	// Keep hard-break elements and the text on either side in their original
	// XML objects. Inserting/removing breaks remains a structural operation.
	textRuns := [][]*crdt.YXmlText{{}}
	for _, child := range element.Children() {
		switch node := child.(type) {
		case *crdt.YXmlText:
			textRuns[len(textRuns)-1] = append(textRuns[len(textRuns)-1], node)
		case *crdt.YXmlElement:
			if node.NodeName != "hardBreak" {
				return nil
			}
			textRuns = append(textRuns, nil)
		default:
			return nil
		}
	}
	nodeRuns := [][]BlockNode{{}}
	for _, node := range nodes {
		switch node.Type {
		case "hardBreak":
			nodeRuns = append(nodeRuns, nil)
		case "text":
			nodeRuns[len(nodeRuns)-1] = append(nodeRuns[len(nodeRuns)-1], node)
		default:
			return nil
		}
	}
	if len(textRuns) != len(nodeRuns) {
		return nil
	}
	// Empty runs need a new XML text node; use the structural path rather than
	// guessing insertion positions around existing breaks.
	if len(textRuns) > 1 {
		for i, run := range textRuns {
			if len(run) == 0 && len(nodeRuns[i]) > 0 {
				return nil
			}
		}
	}
	var edits []func(*crdt.Transaction)
	for i, texts := range textRuns {
		edits = append(edits, prepareTextRunUpdate(element, texts, nodeRuns[i]))
	}
	return func(tx *crdt.Transaction) {
		for _, edit := range edits {
			edit(tx)
		}
	}
}

func prepareTextRunUpdate(
	element *crdt.YXmlElement,
	texts []*crdt.YXmlText,
	nodes []BlockNode,
) func(*crdt.Transaction) {
	var before, after strings.Builder
	clearMarks := map[string]any{}
	for _, text := range texts {
		before.WriteString(text.ToString())
		for _, delta := range text.ToDelta() {
			for key := range delta.Attributes {
				clearMarks[key] = nil
			}
		}
	}
	for _, node := range nodes {
		after.WriteString(node.Text)
		for _, mark := range node.Marks {
			clearMarks[mark.Type] = nil
		}
	}
	return func(tx *crdt.Transaction) {
		if len(texts) == 0 && len(nodes) == 0 {
			return
		}
		if len(texts) == 0 {
			text := crdt.NewYXmlText()
			element.InsertText(tx, 0, text)
			texts = append(texts, text)
		}
		old, next := strings.Split(before.String(), ""), strings.Split(after.String(), "")
		offsets := make([]int, len(old)+1)
		for i, char := range old {
			offsets[i+1] = offsets[i] + len(utf16.Encode([]rune(char)))
		}
		edits := textEdits(old, next)
		for i := len(edits) - 1; i >= 0; i-- {
			edit := edits[i]
			from, to, pos := offsets[edit.I1], offsets[edit.I2], 0
			var insertion *crdt.YXmlText
			insertAt := 0
			for _, text := range texts {
				length := text.Len()
				if insertion == nil && from <= pos+length {
					insertion, insertAt = text, from-pos
				}
				lo, hi := max(from-pos, 0), min(to-pos, length)
				if hi > lo {
					text.Delete(tx, lo, hi-lo)
				}
				pos += length
			}
			if edit.J2 > edit.J1 {
				insertion.Insert(tx, insertAt, strings.Join(next[edit.J1:edit.J2], ""), clearMarks)
			}
		}
		// Formatting is an attribute edit, never a delete/reinsert of the text.
		pos := 0
		for _, node := range nodes {
			attrs := textMarks(node.Marks)
			for key := range clearMarks {
				if _, exists := attrs[key]; !exists {
					attrs[key] = nil
				}
			}
			end, offset := pos+len(utf16.Encode([]rune(node.Text))), 0
			for _, text := range texts {
				lo, hi := max(pos-offset, 0), min(end-offset, text.Len())
				if hi > lo {
					text.Format(tx, lo, hi-lo, attrs)
				}
				offset += text.Len()
			}
			pos = end
		}
	}
}

// IsTextBlockUpdate identifies updates that preserve a block's XML text objects.
// Other operations retain structural validation and revision fences.
func IsTextBlockUpdate(before, after BlockNode) bool {
	if before.Type != after.Type ||
		BlockHash(
			BlockNode{Type: before.Type, Attrs: before.Attrs},
		) != BlockHash(
			BlockNode{Type: after.Type, Attrs: after.Attrs},
		) {
		return false
	}
	switch before.Type {
	case "paragraph", "heading", "codeBlock", "uiDocument":
	default:
		return false
	}
	breaks := [2]int{}
	for i, block := range []BlockNode{before, after} {
		for _, child := range block.Content {
			if child.Type == "hardBreak" {
				breaks[i]++
			} else if child.Type != "text" {
				return false
			}
		}
	}
	if breaks[0] != breaks[1] {
		return false
	}
	// An empty segment cannot receive a text-only update without introducing
	// a new XML child. Leave those cases to the structural path.
	if breaks[0] > 0 {
		for _, block := range []BlockNode{before, after} {
			hasText := false
			for _, child := range block.Content {
				if child.Type == "text" {
					hasText = true
				} else {
					if !hasText {
						return false
					}
					hasText = false
				}
			}
			if !hasText {
				return false
			}
		}
	}
	return true
}

// BlockOperationUpdates authors independent offline text updates from the real
// shared base. Persist these bytes once: regenerating them assigns new CRDT
// identities and changes concurrent ordering. Structural operations have nil entries.
func BlockOperationUpdates(base []byte, operations []BlockOperation) ([][]byte, error) {
	_, root, _, err := MergeBlocks(base, nil)
	if err != nil {
		return nil, err
	}
	_, vector, err := BlockStateDelta(base, nil)
	if err != nil {
		return nil, err
	}
	updates := make([][]byte, len(operations))
	for i, op := range operations {
		original := FindBlock(root, op.BlockID)
		if op.Type != "update" || original == nil || op.Content == nil ||
			!IsTextBlockUpdate(*original, *op.Content) {
			continue
		}
		// A move or ancestor replacement in this proposal recreates XML nodes.
		// Its dependent text operation must run against that new structure,
		// rather than replaying an update that addresses the old text object.
		dependent := false
		for j, other := range operations {
			if i == j || other.Type == "insert" {
				continue
			}
			ancestor := FindBlock(root, other.BlockID)
			if ancestor != nil && FindBlock(*ancestor, op.BlockID) != nil {
				dependent = true
				break
			}
		}
		if dependent {
			continue
		}
		branch, _, err := ApplyBlockOperations(base, []BlockOperation{op})
		if err != nil {
			return nil, err
		}
		updates[i], _, err = BlockStateDelta(branch, vector)
		if err != nil {
			return nil, err
		}
	}
	return updates, nil
}
