package canvas

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/reearth/ygo/crdt"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type commentCharacter struct {
	value rune
	text  *crdt.YXmlText
	index int
}

// Positions bind to CRDT items, so deleting a different copy of the same words
// cannot detach this discussion, and partial edits retain their remaining text.
// A removed selection becomes a page-level anchor: document edits never delete
// the discussion or the Agent conversations and Run evidence it owns.
func reconcileCommentQuotes(before, after []byte, anchors []json.RawMessage) ([]json.RawMessage, error) {
	doc := crdt.New()
	defer doc.Destroy()
	if err := crdt.ApplyUpdateV1(doc, before, nil); err != nil {
		return nil, err
	}
	fragment := doc.GetXmlFragment("content")
	spans := make([][]crdt.ID, len(anchors))
	blocks := make(map[string][]commentCharacter)
	tracked := make(map[*crdt.YXmlText]bool)
	for i, raw := range anchors {
		var anchor struct {
			Type    string `json:"type"`
			Exact   string `json:"exact"`
			BlockID string `json:"blockId"`
		}
		if err := json.Unmarshal(raw, &anchor); err != nil {
			return nil, err
		}
		if anchor.Type != "textQuote" {
			continue
		}
		chars, found := blocks[anchor.BlockID]
		if !found {
			chars = commentCharacters(fragment, anchor.BlockID)
			blocks[anchor.BlockID] = chars
		}
		var corpus strings.Builder
		for _, char := range chars {
			corpus.WriteRune(char.value)
		}
		quote := strings.Join(strings.Fields(anchor.Exact), " ")
		offset := strings.Index(corpus.String(), quote)
		if offset < 0 || quote == "" {
			continue
		}
		// Older quote anchors have no occurrence offset. Preserve ambiguous
		// discussions rather than deleting one because another copy changed.
		if strings.Contains(corpus.String()[offset+1:], quote) {
			continue
		}
		start := utf8.RuneCountInString(corpus.String()[:offset])
		selected := chars[start : start+utf8.RuneCountInString(quote)]

		for _, char := range selected {
			if char.text == nil {
				continue
			}
			tracked[char.text] = true
			position := crdt.CreateRelativePositionFromIndex(char.text, char.index, 0)
			if position.Item != nil {
				spans[i] = append(spans[i], *position.Item)
			}
		}
	}
	if err := crdt.ApplyUpdateV1(doc, after, nil); err != nil {
		return nil, err
	}

	chars := commentCharacters(fragment, "")
	positions := make(map[crdt.ID]int)
	for index, char := range chars {
		if !tracked[char.text] {
			continue
		}
		position := crdt.CreateRelativePositionFromIndex(char.text, char.index, 0)
		if position.Item != nil {
			positions[*position.Item] = index
		}
	}
	result := append([]json.RawMessage(nil), anchors...)
	for i, ids := range spans {
		if len(ids) == 0 {
			continue
		}
		first, last := len(chars), -1
		for _, id := range ids {
			if index, ok := positions[id]; ok {
				first = min(first, index)
				last = max(last, index)
			}
		}
		var text strings.Builder
		if last >= first {
			for _, char := range chars[first : last+1] {
				text.WriteRune(char.value)
			}
		}
		exact := strings.Join(strings.Fields(text.String()), " ")
		if exact == "" {
			result[i] = json.RawMessage(`{"type":"version"}`)
			continue
		}
		var anchor map[string]json.RawMessage
		if err := json.Unmarshal(anchors[i], &anchor); err != nil {
			return nil, err
		}
		var previous string
		if err := json.Unmarshal(anchor["exact"], &previous); err != nil {
			return nil, err
		}
		if previous == exact {
			continue
		}
		encoded, err := json.Marshal(exact)
		if err != nil {
			return nil, err
		}
		anchor["exact"] = encoded
		result[i], err = json.Marshal(anchor)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func commentCharacters(fragment *crdt.YXmlFragment, blockID string) []commentCharacter {
	var chars []commentCharacter
	appendChar := func(char commentCharacter) {
		if unicode.IsSpace(char.value) {
			char.value = ' '
			if len(chars) == 0 || chars[len(chars)-1].value == ' ' {
				return
			}
		}
		chars = append(chars, char)
	}
	var walk func(*crdt.YXmlFragment, bool)
	walk = func(parent *crdt.YXmlFragment, matched bool) {
		for _, child := range parent.Children() {
			switch node := child.(type) {
			case *crdt.YXmlElement:
				id, _ := node.GetAttribute("blockId")
				walk(&node.YXmlFragment, matched || id == blockID)
				if matched || id == blockID {
					appendChar(commentCharacter{value: ' '})
				}
			case *crdt.YXmlText:
				if !matched {
					continue
				}
				index := 0
				for _, value := range node.ToString() {
					appendChar(commentCharacter{value: value, text: node, index: index})
					index += utf16.RuneLen(value)
				}
			}
		}
	}
	walk(fragment, blockID == "")
	return chars
}

type pageCommentEdit struct {
	companyID, pageID uuid.UUID
	before, after     []byte
}

func reconcilePageComments(ctx context.Context, q *generated.Queries, in pageCommentEdit) error {
	threads, err := q.ListPageCommentAnchors(
		ctx,
		generated.ListPageCommentAnchorsParams{CompanyID: in.companyID, PageID: nullable(&in.pageID)},
	)
	if err != nil || len(threads) == 0 {
		return err
	}
	anchors := make([]json.RawMessage, len(threads))
	for i, thread := range threads {
		anchors[i] = thread.Anchor
	}
	next, err := reconcileCommentQuotes(in.before, in.after, anchors)
	if err != nil {
		return err
	}
	for i, anchor := range next {
		if string(anchor) != string(anchors[i]) {
			err = q.UpdatePageCommentAnchor(
				ctx,
				generated.UpdatePageCommentAnchorParams{
					CompanyID: in.companyID,
					PageID:    nullable(&in.pageID),
					ID:        threads[i].ID,
					Anchor:    anchor,
				},
			)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
