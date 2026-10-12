package wiki

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/reearth/ygo/crdt"
)

// BlockOperation changes an addressed subtree, preserving every unrelated CRDT item.
type BlockOperation struct {
	Type         string     `json:"type"                   binding:"required,oneof=insert update remove move"`
	BlockID      string     `json:"blockId,omitempty"`
	ParentID     string     `json:"parentId,omitempty"`
	AfterID      string     `json:"afterId,omitempty"`
	BeforeID     string     `json:"beforeId,omitempty"`
	ExpectedHash string     `json:"expectedHash,omitempty"`
	Content      *BlockNode `json:"content,omitempty"`
}

func BlockHash(node BlockNode) string {
	raw, _ := json.Marshal(node)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func FindBlock(root BlockNode, id string) *BlockNode {
	if id == "" {
		return nil
	}
	if root.Attrs["blockId"] == id {
		return &root
	}
	for _, child := range root.Content {
		if found := FindBlock(child, id); found != nil {
			return found
		}
	}
	return nil
}

func appendBlockNodes(tx *crdt.Transaction, parent *crdt.YXmlFragment, index int, nodes []BlockNode) {
	for _, node := range nodes {
		if node.Type == "text" {
			text := crdt.NewYXmlText()
			parent.InsertText(tx, index, text)
			attrs := map[string]any{}
			for _, mark := range node.Marks {
				value := mark.Attrs
				if value == nil {
					value = map[string]any{}
				}
				attrs[mark.Type] = value
			}
			text.Insert(tx, 0, node.Text, attrs)
		} else {
			element := crdt.NewYXmlElement(node.Type)
			parent.InsertElement(tx, index, element)
			for key, value := range node.Attrs {
				element.SetAttributeValue(tx, key, value)
			}
			appendBlockNodes(tx, &element.YXmlFragment, 0, node.Content)
		}
		index++
	}
}

// ApplyBlockOperations edits the existing XML document; it never resets the root.
// Callers check scope/protection and allocate inserted identities before calling.
func ApplyBlockOperations(saved []byte, operations []BlockOperation) ([]byte, BlockNode, error) {
	if len(operations) == 0 {
		_, root, _, err := MergeBlocks(saved, nil)
		return saved, root, err
	}
	doc := crdt.New()
	defer doc.Destroy()
	if err := crdt.ApplyUpdateV1(doc, saved, nil); err != nil {
		return nil, BlockNode{}, ErrPageInvalidInput
	}
	fragment := doc.GetXmlFragment("content")
	type location struct {
		parent  *crdt.YXmlFragment
		element *crdt.YXmlElement
		index   int
	}
	var locate func(*crdt.YXmlFragment, string) *location
	locate = func(parent *crdt.YXmlFragment, id string) *location {
		for index, child := range parent.Children() {
			if element, ok := child.(*crdt.YXmlElement); ok {
				if element.GetAttributeValues()["blockId"] == id {
					return &location{parent, element, index}
				}
				if found := locate(&element.YXmlFragment, id); found != nil {
					return found
				}
			}
		}
		return nil
	}
	for _, operation := range operations {
		if operation.BeforeID != "" && operation.AfterID != "" {
			return nil, BlockNode{}, ErrPageInvalidInput
		}
		_, root, _, err := MergeBlocks(doc.EncodeStateAsUpdate(), nil)
		if err != nil {
			return nil, BlockNode{}, err
		}
		target := locate(fragment, operation.BlockID)
		if operation.Type != "insert" {
			block := FindBlock(root, operation.BlockID)
			if target == nil || block == nil || operation.ExpectedHash == "" ||
				BlockHash(*block) != operation.ExpectedHash {
				return nil, BlockNode{}, ErrPageConflict
			}
		}
		parent := fragment
		if operation.ParentID != "" {
			found := locate(fragment, operation.ParentID)
			if found == nil {
				return nil, BlockNode{}, ErrPageConflict
			}
			parent = &found.element.YXmlFragment
		}
		index := parent.Len()
		if operation.AfterID != "" {
			after := locate(fragment, operation.AfterID)
			if after == nil || after.parent != parent {
				return nil, BlockNode{}, ErrPageConflict
			}
			index = after.index + 1
		}
		if operation.BeforeID != "" {
			before := locate(fragment, operation.BeforeID)
			if before == nil || before.parent != parent {
				return nil, BlockNode{}, ErrPageConflict
			}
			index = before.index
		}
		switch operation.Type {
		case "insert":
			if operation.Content == nil {
				return nil, BlockNode{}, ErrPageInvalidInput
			}
			doc.Transact(
				func(tx *crdt.Transaction) { appendBlockNodes(tx, parent, index, []BlockNode{*operation.Content}) },
			)
		case "update":
			if operation.Content == nil || operation.Content.Type != target.element.NodeName ||
				operation.Content.Attrs["blockId"] != operation.BlockID {
				return nil, BlockNode{}, ErrPageInvalidInput
			}
			doc.Transact(func(tx *crdt.Transaction) {
				for key := range target.element.GetAttributeValues() {
					target.element.DeleteAttribute(tx, key)
				}
				for key, value := range operation.Content.Attrs {
					target.element.SetAttributeValue(tx, key, value)
				}
				target.element.Delete(tx, 0, target.element.Len())
				appendBlockNodes(tx, &target.element.YXmlFragment, 0, operation.Content.Content)
			})
		case "remove":
			doc.Transact(func(tx *crdt.Transaction) { target.parent.Delete(tx, target.index, 1) })
		case "move":
			if operation.BlockID == operation.AfterID || operation.BlockID == operation.BeforeID ||
				operation.BlockID == operation.ParentID {
				return nil, BlockNode{}, ErrPageInvalidInput
			}
			block := FindBlock(root, operation.BlockID)
			if FindBlock(*block, operation.ParentID) != nil {
				return nil, BlockNode{}, ErrPageInvalidInput
			}
			doc.Transact(func(tx *crdt.Transaction) {
				target.parent.Delete(tx, target.index, 1)
				if parent == target.parent && target.index < index {
					index--
				}
				appendBlockNodes(tx, parent, index, []BlockNode{*block})
			})
		default:
			return nil, BlockNode{}, ErrPageInvalidInput
		}
	}
	state, root, _, err := MergeBlocks(doc.EncodeStateAsUpdate(), nil)
	return state, root, err
}
