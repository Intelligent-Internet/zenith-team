package wiki

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/reearth/ygo/crdt"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	goldhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// BlockNode is the versioned native document projection. The Yjs XML tree is
// canonical while editing; this projection is frozen with every checkpoint.
type BlockNode struct {
	Type    string         `json:"type"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Content []BlockNode    `json:"content,omitempty"`
	Text    string         `json:"text,omitempty"`
	Marks   []BlockMark    `json:"marks,omitempty"`
}

type BlockMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// AssignBlockIDs assigns database-reserved identities, including when cloning
// a checkpoint into another independently editable document.
func AssignBlockIDs(node *BlockNode, next func() string) {
	if node.Type != "doc" && !inlineBlockNode(node.Type) {
		if node.Attrs == nil {
			node.Attrs = map[string]any{}
		}
		node.Attrs["blockId"] = next()
	}
	for i := range node.Content {
		AssignBlockIDs(&node.Content[i], next)
	}
}

func inlineBlockNode(nodeType string) bool {
	return slices.Contains([]string{"text", "hardBreak", "wikiPageLink", "wikiArtifactRef"}, nodeType)
}

func BlockIDs(root BlockNode) []uuid.UUID {
	var ids []uuid.UUID
	var walk func(BlockNode)
	walk = func(n BlockNode) {
		if id, ok := n.Attrs["blockId"].(string); ok {
			if parsed, err := uuid.Parse(id); err == nil {
				ids = append(ids, parsed)
			}
		}
		for _, child := range n.Content {
			walk(child)
		}
	}
	walk(root)
	return ids
}

// BlocksRelocated fences stale structural copies. ProseMirror moves XML by
// deleting and reinserting it; unlike text deltas, that cannot safely absorb an
// unseen edit of the old XML subtree. Independent inserts remain mergeable.
func BlocksRelocated(left, right BlockNode) bool {
	type location struct{ id, parent string }
	flatten := func(root BlockNode) []location {
		var rows []location
		var walk func(BlockNode, string)
		walk = func(node BlockNode, parent string) {
			id, _ := node.Attrs["blockId"].(string)
			if id != "" {
				rows = append(rows, location{id: id, parent: parent})
				parent = id
			}
			for _, child := range node.Content {
				walk(child, parent)
			}
		}
		walk(root, "")
		return rows
	}
	a, b := flatten(left), flatten(right)
	parents := map[string]string{}
	for _, row := range a {
		parents[row.id] = row.parent
	}
	common := map[string]bool{}
	var order []string
	for _, row := range b {
		if parent, ok := parents[row.id]; ok {
			if parent != row.parent {
				return true
			}
			common[row.id] = true
			order = append(order, row.id)
		}
	}
	var original []string
	for _, row := range a {
		if common[row.id] {
			original = append(original, row.id)
		}
	}
	return !slices.Equal(original, order)
}

// BlockStructureChanged includes splits, joins, deletion and node conversion.
// Text runs and formatting are deliberately excluded from the structural fence.
func BlockStructureChanged(left, right BlockNode) bool {
	type position struct {
		Type, ID string
		Depth    int
	}
	flatten := func(root BlockNode) []position {
		var result []position
		var walk func(BlockNode, int)
		walk = func(node BlockNode, depth int) {
			if node.Type == "text" {
				return
			}
			id, _ := node.Attrs["blockId"].(string)
			result = append(result, position{node.Type, id, depth})
			for _, child := range node.Content {
				walk(child, depth+1)
			}
		}
		walk(root, 0)
		return result
	}
	return !slices.Equal(flatten(left), flatten(right))
}

// BlockStateDelta returns a wire update and the accepted state vector. A target
// vector changes only the transport; the complete state remains authoritative.
func BlockStateDelta(state, target []byte) ([]byte, []byte, error) {
	vector, err := crdt.EncodeStateVectorFromUpdate(state)
	if err != nil {
		return nil, nil, ErrPageInvalidInput
	}
	if len(target) == 0 {
		return state, vector, nil
	}
	if len(target) > 65536 {
		return nil, nil, ErrPageInvalidInput
	}
	sv, err := crdt.DecodeStateVectorV1(target)
	if err != nil {
		return nil, nil, ErrPageInvalidInput
	}
	update, err := crdt.DiffUpdateV1(state, sv)
	if err != nil {
		return nil, nil, ErrPageInvalidInput
	}
	return update, vector, nil
}

func ValidBlockStateVector(vector []byte) bool {
	if len(vector) == 0 {
		return true
	}
	if len(vector) > 65536 {
		return false
	}
	_, err := crdt.DecodeStateVectorV1(vector)
	return err == nil
}

func BlocksFromMarkdown(markdown string) BlockNode {
	// GFM splits table cells before inline parsing. Protect the Wiki token's
	// separator while parsing; code restores its original literal spelling.
	markdown = wikiInlineReference.ReplaceAllStringFunc(markdown, func(token string) string {
		return strings.Replace(token, "|", "&#124;", 1)
	})
	source := []byte(markdown)
	parser := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser()
	root := parser.Parse(text.NewReader(source))
	var convert func(ast.Node) []BlockNode
	children := func(n ast.Node) []BlockNode {
		var result []BlockNode
		underline := false
		for child := n.FirstChild(); child != nil; child = child.NextSibling() {
			if raw, ok := child.(*ast.RawHTML); ok {
				switch strings.ToLower(string(raw.Segments.Value(source))) {
				case "<u>":
					underline = true
					continue
				case "</u>":
					if underline {
						underline = false
						continue
					}
				}
			}
			converted := convert(child)
			if underline {
				converted = markBlocks(converted, BlockMark{Type: "underline"})
			}
			result = append(result, converted...)
		}
		return result
	}
	convert = func(n ast.Node) []BlockNode {
		b := BlockNode{Type: "paragraph"}
		switch node := n.(type) {
		case *ast.Document:
			b.Type = "doc"
		case *ast.Heading:
			b.Type = "heading"
			b.Attrs = map[string]any{"level": node.Level}
		case *ast.Paragraph, *ast.TextBlock:
		case *ast.Text:
			value := markdownInlineText(node.Segment.Value(source))
			if node.SoftLineBreak() {
				value += "\n"
			}
			result := []BlockNode{}
			if value != "" {
				result = append(result, BlockNode{Type: "text", Text: value})
			}
			if node.HardLineBreak() {
				result = append(result, BlockNode{Type: "hardBreak"})
			}
			return result
		case *ast.String:
			value := string(node.Value)
			if !node.IsCode() && !node.IsRaw() {
				value = markdownInlineText(node.Value)
			}
			return []BlockNode{{Type: "text", Text: value}}
		case *ast.Emphasis:
			kind := "italic"
			if node.Level == 2 {
				kind = "bold"
			}
			return markBlocks(children(n), BlockMark{Type: kind})
		case *extast.Strikethrough:
			return markBlocks(children(n), BlockMark{Type: "strike"})
		case *ast.Link:
			if !safeBlockURL(string(node.Destination), false) {
				return []BlockNode{
					{
						Type: "text",
						Text: "[" + markdownNodeText(
							node,
							source,
						) + "](" + string(
							node.Destination,
						) + ")",
					},
				}
			}
			return markBlocks(
				children(n),
				BlockMark{
					Type: "link",
					Attrs: map[string]any{
						"href":   string(node.Destination),
						"target": "_blank",
						"rel":    "noopener noreferrer nofollow",
					},
				},
			)
		case *ast.AutoLink:
			value := string(node.URL(source))
			label := string(node.Label(source))
			if !safeBlockURL(value, false) {
				return []BlockNode{{Type: "text", Text: label}}
			}
			return []BlockNode{
				{
					Type:  "text",
					Text:  label,
					Marks: []BlockMark{{Type: "link", Attrs: map[string]any{"href": value}}},
				},
			}
		case *ast.CodeSpan:
			return []BlockNode{
				{Type: "text", Text: restoreWikiPipes(markdownNodeText(n, source)), Marks: []BlockMark{{Type: "code"}}},
			}
		case *ast.FencedCodeBlock:
			b.Type = "codeBlock"
			language := string(node.Language(source))
			b.Attrs = map[string]any{"language": language}
			if language == "mermaid" {
				b.Type = "mermaid"
				b.Attrs = nil
			}
			value := restoreWikiPipes(string(node.Lines().Value(source)))
			if value != "" {
				b.Content = []BlockNode{{Type: "text", Text: strings.TrimSuffix(value, "\n")}}
			}
			return []BlockNode{b}
		case *ast.CodeBlock:
			b.Type = "codeBlock"
			value := restoreWikiPipes(string(node.Lines().Value(source)))
			if value != "" {
				b.Content = []BlockNode{{Type: "text", Text: value}}
			}
			return []BlockNode{b}
		case *ast.Blockquote:
			b.Type = "blockquote"
		case *ast.List:
			b.Type = "bulletList"
			if node.IsOrdered() {
				b.Type = "orderedList"
				b.Attrs = map[string]any{"start": node.Start}
			}
			for item := n.FirstChild(); item != nil; item = item.NextSibling() {
				if first := item.FirstChild(); first != nil {
					if _, ok := first.FirstChild().(*extast.TaskCheckBox); ok {
						b.Type = "taskList"
						break
					}
				}
			}
		case *ast.ListItem:
			b.Type = "listItem"
			if first := node.FirstChild(); first != nil {
				if check, ok := first.FirstChild().(*extast.TaskCheckBox); ok {
					b.Type = "taskItem"
					b.Attrs = map[string]any{"checked": check.IsChecked}
				}
			}
		case *extast.TaskCheckBox:
			return nil
		case *ast.ThematicBreak:
			b.Type = "horizontalRule"
		case *ast.Image:
			if !safeBlockURL(string(node.Destination), true) {
				return []BlockNode{
					{
						Type: "text",
						Text: "![" + markdownNodeText(
							node,
							source,
						) + "](" + string(
							node.Destination,
						) + ")",
					},
				}
			}
			return []BlockNode{
				{
					Type: "image",
					Attrs: map[string]any{
						"src":   string(node.Destination),
						"alt":   markdownNodeText(node, source),
						"title": string(node.Title),
					},
				},
			}
		case *extast.Table:
			b.Type = "table"
		case *extast.TableHeader:
			b.Type = "tableRow"
		case *extast.TableRow:
			b.Type = "tableRow"
		case *extast.TableCell:
			b.Type = "tableCell"
			if _, ok := node.Parent().(*extast.TableHeader); ok {
				b.Type = "tableHeader"
			}
			b.Attrs = map[string]any{"colspan": 1, "rowspan": 1}
			b.Content = paragraphBlocks(children(n))
			return []BlockNode{b}
		case *ast.RawHTML:
			return []BlockNode{{Type: "text", Text: string(node.Segments.Value(source))}}
		case *ast.HTMLBlock:
			value := string(node.Lines().Value(source))
			if node.HasClosure() {
				value += string(node.ClosureLine.Value(source))
			}
			b.Content = []BlockNode{{Type: "text", Text: value}}
			return []BlockNode{b}
		default:
			return children(n)
		}
		b.Content = children(n)
		if b.Type == "taskList" {
			b.Attrs = nil
			for i := range b.Content {
				b.Content[i].Type = "taskItem"
				if b.Content[i].Attrs == nil {
					b.Content[i].Attrs = map[string]any{"checked": false}
				}
			}
		}
		if b.Type == "heading" {
			for i, child := range b.Content {
				if child.Type == "image" {
					b.Content[i] = BlockNode{
						Type: "text",
						Text: fmt.Sprint(
							child.Attrs["alt"],
						) + " (" + fmt.Sprint(
							child.Attrs["src"],
						) + ")",
					}
				}
			}
		}
		if (b.Type == "listItem" || b.Type == "taskItem") &&
			(len(b.Content) == 0 || b.Content[0].Type != "paragraph") {
			b.Content = append([]BlockNode{{Type: "paragraph"}}, b.Content...)
		}
		if b.Type == "paragraph" {
			return paragraphBlocks(b.Content)
		}
		return []BlockNode{b}
	}
	result := convert(root)[0]
	if len(result.Content) == 0 {
		result.Content = []BlockNode{{Type: "paragraph"}}
	}
	importWikiReferences(&result)
	return result
}

var wikiInlineReference = regexp.MustCompile(`\[\[(artifact:)?([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})(?:#(\d{1,2})|(?:\\?\||&#124;)([^\]]*))?\]\]`)

// paragraphBlocks lifts block images out of inline paragraph content.
func paragraphBlocks(children []BlockNode) []BlockNode {
	var out []BlockNode
	current := BlockNode{Type: "paragraph"}
	for _, child := range children {
		if child.Type == "image" {
			if len(current.Content) > 0 {
				out = append(out, current)
			}
			out = append(out, child)
			current = BlockNode{Type: "paragraph"}
		} else {
			current.Content = append(current.Content, child)
		}
	}
	if len(current.Content) > 0 || len(out) == 0 {
		out = append(out, current)
	}
	return out
}

// Use Goldmark's single-pass escape handling, then remove only its HTML encoding.
// Decoding entities after unescaping punctuation would incorrectly decode \&amp;.
func markdownInlineText(source []byte) string {
	var rendered strings.Builder
	writer := bufio.NewWriter(&rendered)
	goldhtml.DefaultWriter.Write(writer, source)
	_ = writer.Flush() // strings.Builder writes cannot fail.
	return html.UnescapeString(rendered.String())
}

func restoreWikiPipes(value string) string {
	return wikiInlineReference.ReplaceAllStringFunc(value, func(token string) string {
		return strings.Replace(token, "&#124;", "|", 1)
	})
}

// Goldmark may split a literal token across adjacent text nodes. Join equally
// formatted text before importing references; code and link labels stay literal.
func importWikiReferences(node *BlockNode) {
	if node.Type == "codeBlock" || node.Type == "mermaid" {
		return
	}
	var children []BlockNode
	for i := 0; i < len(node.Content); i++ {
		child := node.Content[i]
		literal := slices.ContainsFunc(child.Marks, func(mark BlockMark) bool { return mark.Type == "code" || mark.Type == "link" })
		if child.Type != "text" || literal {
			importWikiReferences(&child)
			children = append(children, child)
			continue
		}
		for i+1 < len(node.Content) && node.Content[i+1].Type == "text" && reflect.DeepEqual(node.Content[i+1].Marks, child.Marks) {
			i++
			child.Text += node.Content[i].Text
		}
		matches := wikiInlineReference.FindAllStringSubmatchIndex(child.Text, -1)
		start := 0
		for _, match := range matches {
			artifact := match[2] >= 0
			ordinal, label := match[6] >= 0, match[8] >= 0
			if artifact && label || !artifact && ordinal {
				continue
			}
			if match[0] > start {
				children = append(children, BlockNode{Type: "text", Text: child.Text[start:match[0]], Marks: child.Marks})
			}
			ref := BlockNode{Type: "wikiPageLink", Attrs: map[string]any{"id": child.Text[match[4]:match[5]], "label": ""}}
			if label {
				ref.Attrs["label"] = child.Text[match[8]:match[9]]
			}
			if artifact {
				ref.Type = "wikiArtifactRef"
				ref.Attrs = map[string]any{"id": child.Text[match[4]:match[5]], "ordinal": nil}
				if ordinal {
					value := 0
					for _, digit := range child.Text[match[6]:match[7]] {
						value = value*10 + int(digit-'0')
					}
					ref.Attrs["ordinal"] = value
				}
			}
			children = append(children, ref)
			start = match[1]
		}
		if start < len(child.Text) {
			children = append(children, BlockNode{Type: "text", Text: child.Text[start:], Marks: child.Marks})
		}
	}
	node.Content = children
}

func markdownNodeText(node ast.Node, source []byte) string {
	var value strings.Builder
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		switch part := child.(type) {
		case *ast.Text:
			value.Write(part.Value(source))
		case *ast.String:
			value.Write(part.Value)
		default:
			value.WriteString(markdownNodeText(part, source))
		}
	}
	return value.String()
}

func markBlocks(nodes []BlockNode, mark BlockMark) []BlockNode {
	for i := range nodes {
		if nodes[i].Type == "text" {
			nodes[i].Marks = append(nodes[i].Marks, mark)
		}
	}
	return nodes
}

func safeBlockURL(value string, image bool) bool {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.User != nil {
		return false
	}
	if u.Scheme == "https" || u.Scheme == "http" {
		return u.Host != ""
	}
	return !image && (u.Scheme == "mailto" || (u.Scheme == "" && u.Host == "" && !strings.Contains(value, "\\") && (strings.HasPrefix(value, "#") || strings.HasPrefix(value, "/"))))
}

// EncodeBlocks is used for migration and independent branches, never to replace
// a live CRDT with every keystroke.
func EncodeBlocks(root BlockNode) ([]byte, error) {
	if err := validateBlocks(root); err != nil {
		return nil, err
	}
	doc := crdt.New()
	defer doc.Destroy()
	fragment := doc.GetXmlFragment("content")
	var appendNodes func(*crdt.Transaction, *crdt.YXmlFragment, []BlockNode)
	appendNodes = func(tx *crdt.Transaction, parent *crdt.YXmlFragment, nodes []BlockNode) {
		var rich *crdt.YXmlText
		var attrs map[string]any
		for _, node := range nodes {
			if node.Type == "text" {
				if rich == nil {
					rich = crdt.NewYXmlText()
					parent.InsertText(tx, parent.Len(), rich)
					attrs = map[string]any{}
				}
				// An empty attribute map means "inherit" to Yjs. Clear marks
				// explicitly so adjacent native text runs retain their boundaries.
				for key := range attrs {
					attrs[key] = nil
				}
				for _, mark := range node.Marks {
					value := mark.Attrs
					if value == nil {
						value = map[string]any{}
					}
					attrs[mark.Type] = value
				}
				rich.Insert(tx, rich.Len(), node.Text, attrs)
				continue
			}
			rich = nil
			elem := crdt.NewYXmlElement(node.Type)
			parent.InsertElement(tx, parent.Len(), elem)
			keys := make([]string, 0, len(node.Attrs))
			for key := range node.Attrs {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				elem.SetAttributeValue(tx, key, node.Attrs[key])
			}
			appendNodes(tx, &elem.YXmlFragment, node.Content)
		}
	}
	doc.Transact(func(tx *crdt.Transaction) { appendNodes(tx, fragment, root.Content) })
	return doc.EncodeStateAsUpdate(), nil
}

func MergeBlocks(saved, update []byte) ([]byte, BlockNode, string, error) {
	return mergeBlocks(saved, update, nil)
}

// MergeBlockChanges also detects replacement of XML objects whose authored
// block ID stayed the same. A copied subtree cannot inherit unseen text edits.
func MergeBlockChanges(saved, update []byte) ([]byte, BlockNode, string, bool, error) {
	structural := false
	state, root, markdown, err := mergeBlocks(saved, update, &structural)
	return state, root, markdown, structural, err
}

func mergeBlocks(saved, update []byte, structural *bool) (state []byte, root BlockNode, markdown string, err error) {
	defer func() {
		if recover() != nil {
			state = nil
			root = BlockNode{}
			markdown = ""
			err = ErrPageInvalidInput
		}
	}()
	if len(saved) > 8<<20 || len(update) > 8<<20 {
		return nil, root, "", ErrPageBodyTooLarge
	}
	doc := crdt.New(crdt.WithMaxPendingItems(10000))
	defer doc.Destroy()
	fragment := doc.GetXmlFragment("content")
	xmlNodes := func() []*crdt.YXmlElement {
		var nodes []*crdt.YXmlElement
		var walk func(*crdt.YXmlFragment, int)
		walk = func(parent *crdt.YXmlFragment, depth int) {
			if depth > 32 {
				return
			}
			for _, child := range parent.Children() {
				if len(nodes) > 10000 {
					return
				}
				if element, ok := child.(*crdt.YXmlElement); ok {
					nodes = append(nodes, element)
					walk(&element.YXmlFragment, depth+1)
				}
			}
		}
		walk(fragment, 0)
		return nodes
	}
	for i, data := range [][]byte{saved, update} {
		var before []*crdt.YXmlElement
		if i == 1 && structural != nil {
			before = xmlNodes()
		}
		if len(data) > 0 {
			if err = crdt.ApplyUpdateV1(doc, data, nil); err != nil {
				return nil, root, "", ErrPageInvalidInput
			}
		}
		if i == 1 && structural != nil {
			*structural = !slices.Equal(before, xmlNodes())
		}
	}
	if pending := doc.PendingStats(); pending.Items != 0 || pending.DeleteRanges != 0 || len(doc.GetSubdocs()) > 0 {
		return nil, root, "", ErrPageInvalidInput
	}
	var decode func(*crdt.YXmlFragment, int) ([]BlockNode, error)
	count := 0
	decode = func(parent *crdt.YXmlFragment, depth int) ([]BlockNode, error) {
		if depth > 32 {
			return nil, ErrPageBodyTooLarge
		}
		var result []BlockNode
		for _, child := range parent.Children() {
			count++
			if count > 10000 {
				return nil, ErrPageBodyTooLarge
			}
			switch node := child.(type) {
			case *crdt.YXmlElement:
				content, e := decode(&node.YXmlFragment, depth+1)
				if e != nil {
					return nil, e
				}
				result = append(
					result,
					BlockNode{
						Type:    node.NodeName,
						Attrs:   node.GetAttributeValues(),
						Content: content,
					},
				)
			case *crdt.YXmlText:
				for _, delta := range node.ToDelta() {
					value, ok := delta.Insert.(string)
					if !ok {
						return nil, ErrPageInvalidInput
					}
					if value == "" {
						continue
					}
					b := BlockNode{Type: "text", Text: value}
					keys := make([]string, 0, len(delta.Attributes))
					for key := range delta.Attributes {
						keys = append(keys, key)
					}
					sort.Strings(keys)
					for _, key := range keys {
						attrs, ok := delta.Attributes[key].(map[string]any)
						if !ok {
							return nil, ErrPageInvalidInput
						}
						b.Marks = append(b.Marks, BlockMark{Type: key, Attrs: attrs})
					}
					result = append(result, b)
				}
			default:
				return nil, ErrPageInvalidInput
			}
		}
		return result, nil
	}
	root.Type = "doc"
	root.Content, err = decode(fragment, 0)
	if err != nil {
		return nil, root, "", err
	}
	if err = validateBlocks(root); err != nil {
		return nil, root, "", err
	}
	markdown = BlocksMarkdown(root)
	if len(markdown) > 262144 {
		return nil, root, "", ErrPageBodyTooLarge
	}
	state = doc.EncodeStateAsUpdate()
	if len(state) > 8<<20 {
		return nil, root, "", ErrPageBodyTooLarge
	}
	return state, root, markdown, nil
}

// ValidateBlockImport checks untrusted native input before deriving Markdown.
// Imported identities are remapped by PostgreSQL before EncodeBlocks validates them.
func ValidateBlockImport(root BlockNode) error {
	return validateBlockTree(root, false)
}

// ValidateBlockInsert checks an unallocated subtree in its actual destination.
// The complete edited document is validated again after applying the operations.
func ValidateBlockInsert(root BlockNode, parentType string) error {
	if root.Type == "doc" || inlineBlockNode(root.Type) {
		return ErrPageInvalidInput
	}
	return validateBlockSubtree(root, parentType, false)
}

func validateBlocks(root BlockNode) error {
	return validateBlockTree(root, true)
}

func validateBlockTree(root BlockNode, checkIDs bool) error {
	if root.Type != "doc" || len(root.Content) == 0 {
		return ErrPageInvalidInput
	}
	return validateBlockSubtree(root, "", checkIDs)
}

func validateBlockSubtree(root BlockNode, parentType string, checkIDs bool) error {
	seen := map[string]bool{}
	count := 0
	var walk func(BlockNode, string, int) error
	walk = func(n BlockNode, parent string, depth int) error {
		count++
		if count > 10000 || depth > 32 {
			return ErrPageBodyTooLarge
		}
		if (n.Type == "doc" && parent != "") || (n.Type != "text" && n.Text != "") {
			return ErrPageInvalidInput
		}
		if slices.Contains(
			[]string{
				"image",
				"horizontalRule",
				"artifactReference",
				"taskReference",
				"taskCollection",
				"outputCollection",
				"workflowReference",
				"fileBlock",
				"audioBlock",
				"videoBlock",
				"equation",
				"tableOfContents",
				"text",
				"hardBreak",
				"wikiPageLink",
				"wikiArtifactRef",
			},
			n.Type,
		) &&
			len(n.Content) > 0 {
			return ErrPageInvalidInput
		}
		if slices.Contains(
			[]string{
				"blockquote",
				"callout",
				"column",
				"toggleContent",
				"listItem",
				"taskItem",
				"bulletList",
				"orderedList",
				"taskList",
				"table",
				"tableRow",
				"tableCell",
				"tableHeader",
			},
			n.Type,
		) &&
			len(n.Content) == 0 {
			return ErrPageInvalidInput
		}
		if n.Type == "toggle" &&
			(len(n.Content) != 2 || n.Content[0].Type != "toggleSummary" || n.Content[1].Type != "toggleContent") {
			return ErrPageInvalidInput
		}
		if n.Type == "columns" && (len(n.Content) < 2 || len(n.Content) > 4) {
			return ErrPageInvalidInput
		}
		if n.Type == "column" && parent != "columns" || parent == "columns" && n.Type != "column" {
			return ErrPageInvalidInput
		}
		if n.Type == "toggleSummary" && parent != "toggle" || n.Type == "toggleContent" && parent != "toggle" {
			return ErrPageInvalidInput
		}
		if slices.Contains([]string{"tableRow", "tableCell", "tableHeader", "listItem", "taskItem"}, n.Type) {
			valid := n.Type == "tableRow" && parent == "table" ||
				(n.Type == "tableCell" || n.Type == "tableHeader") && parent == "tableRow" ||
				n.Type == "listItem" && (parent == "bulletList" || parent == "orderedList") ||
				n.Type == "taskItem" && parent == "taskList"
			if !valid {
				return ErrPageInvalidInput
			}
		}
		if (n.Type == "listItem" || n.Type == "taskItem") && n.Content[0].Type != "paragraph" {
			return ErrPageInvalidInput
		}
		if align, ok := n.Attrs["textAlign"]; ok && align != nil &&
			!slices.Contains([]string{"left", "right", "center", "justify"}, fmt.Sprint(align)) {
			return ErrPageInvalidInput
		}
		textParent := slices.Contains(
			[]string{"paragraph", "heading", "codeBlock", "mermaid", "toggleSummary"},
			parent,
		)
		if inlineBlockNode(n.Type) {
			if !textParent {
				return ErrPageInvalidInput
			}
			if (n.Type == "wikiPageLink" || n.Type == "wikiArtifactRef") && (parent == "codeBlock" || parent == "mermaid") {
				return ErrPageInvalidInput
			}
		} else if n.Type != "doc" {
			if textParent {
				return ErrPageInvalidInput
			}
			id, ok := n.Attrs["blockId"].(string)
			parsed, err := uuid.Parse(id)
			if checkIDs && (!ok || err != nil || parsed == uuid.Nil || seen[id]) {
				return ErrPageInvalidInput
			}
			seen[id] = true
		}
		switch n.Type {
		case "doc",
			"paragraph",
			"heading",
			"blockquote",
			"bulletList",
			"orderedList",
			"listItem",
			"taskList",
			"taskItem",
			"codeBlock",
			"mermaid",
			"horizontalRule",
			"image",
			"table",
			"tableRow",
			"tableCell",
			"tableHeader",
			"callout",
			"toggle",
			"toggleSummary",
			"toggleContent",
			"columns",
			"column",
			"artifactReference",
			"taskReference",
			"taskCollection",
			"outputCollection",
			"workflowReference",
			"fileBlock",
			"audioBlock",
			"videoBlock",
			"equation",
			"tableOfContents",
			"text",
			"hardBreak",
			"wikiPageLink",
			"wikiArtifactRef":
		default:
			return ErrPageInvalidInput
		}
		if parent == "table" && n.Type != "tableRow" {
			return ErrPageInvalidInput
		}
		if parent == "tableRow" && n.Type != "tableCell" && n.Type != "tableHeader" {
			return ErrPageInvalidInput
		}
		if (parent == "bulletList" || parent == "orderedList") && n.Type != "listItem" {
			return ErrPageInvalidInput
		}
		if parent == "taskList" && n.Type != "taskItem" {
			return ErrPageInvalidInput
		}
		if n.Type == "heading" {
			level := fmt.Sprint(n.Attrs["level"])
			if !slices.Contains([]string{"1", "2", "3", "4", "5", "6"}, level) {
				return ErrPageInvalidInput
			}
		}
		if n.Type == "table" && len(n.Content) > 1000 {
			return ErrPageBodyTooLarge
		}
		if n.Type == "tableRow" {
			width := 0
			for _, cell := range n.Content {
				width += intAttr(cell.Attrs["colspan"], 1)
			}
			if width > 200 {
				return ErrPageBodyTooLarge
			}
		}
		if n.Type == "tableCell" || n.Type == "tableHeader" {
			for key, limit := range map[string]int{"colspan": 200, "rowspan": 1000} {
				if value := n.Attrs[key]; value != nil && !boundedBlockNumber(value, 1, limit) {
					return ErrPageInvalidInput
				}
			}
			if value := n.Attrs["colwidth"]; value != nil {
				raw, err := json.Marshal(value)
				var widths []int
				if err != nil || json.Unmarshal(raw, &widths) != nil || len(widths) > 200 {
					return ErrPageInvalidInput
				}
				for _, width := range widths {
					if width < 0 || width > 10000 {
						return ErrPageInvalidInput
					}
				}
			}
		}
		if n.Type == "image" {
			src, _ := n.Attrs["src"].(string)
			if !safeBlockURL(src, true) {
				return ErrPageInvalidInput
			}
		}
		if slices.Contains([]string{"fileBlock", "audioBlock", "videoBlock"}, n.Type) {
			src, _ := n.Attrs["src"].(string)
			if src != "" && !safeBlockURL(src, true) {
				return ErrPageInvalidInput
			}
		}
		for key := range n.Attrs {
			if !slices.Contains(
				[]string{
					"blockId",
					"agentProtected",
					"sourceVersionIds",
					"taskId",
					"filter",
					"display",
					"caption",
					"workflowKey",
					"capturedTasks",
					"capturedOutputs",
					"capturedWorkflow",
					"capturedAt",
					"capturedNeedsRefresh",
					"level",
					"textAlign",
					"start",
					"checked",
					"language",
					"filename",
					"src",
					"alt",
					"title",
					"width",
					"height",
					"colspan",
					"rowspan",
					"colwidth",
					"background",
					"label",
					"versionId",
					"blockTargetId",
					"artifactId",
					"format",
					"source",
					"open",
					"id",
					"ordinal",
				},
				key,
			) {
				return ErrPageInvalidInput
			}
		}
		if n.Type == "wikiPageLink" || n.Type == "wikiArtifactRef" {
			id, _ := n.Attrs["id"].(string)
			parsed, err := uuid.Parse(id)
			if err != nil || parsed == uuid.Nil {
				return ErrPageInvalidInput
			}
			if n.Type == "wikiArtifactRef" && n.Attrs["ordinal"] != nil && !boundedBlockNumber(n.Attrs["ordinal"], 0, 99) {
				return ErrPageInvalidInput
			}
			if label, ok := n.Attrs["label"]; ok && label != nil {
				if _, ok := label.(string); !ok {
					return ErrPageInvalidInput
				}
			}
		}
		if value := n.Attrs["agentProtected"]; value != nil {
			if _, ok := value.(bool); !ok {
				return ErrPageInvalidInput
			}
		}
		// Captured projections are server materializations, never authored state.
		for _, key := range []string{"capturedTasks", "capturedOutputs", "capturedWorkflow", "capturedAt", "capturedNeedsRefresh"} {
			if n.Attrs[key] != nil {
				return ErrPageInvalidInput
			}
		}
		if value := n.Attrs["workflowKey"]; value != nil && value != "" {
			key, ok := value.(string)
			if !ok || len(key) > 39 || !workflowBlockKey.MatchString(key) {
				return ErrPageInvalidInput
			}
		}
		if value := n.Attrs["taskId"]; value != nil && value != "" {
			id, ok := value.(string)
			if !ok {
				return ErrPageInvalidInput
			}
			if _, err := uuid.Parse(id); err != nil {
				return ErrPageInvalidInput
			}
		}
		if value := n.Attrs["display"]; value != nil &&
			!slices.Contains(
				[]string{"link", "card", "section", "expanded", "list", "table", "board", "gallery"},
				fmt.Sprint(value),
			) {
			return ErrPageInvalidInput
		}
		if n.Type == "artifactReference" && n.Attrs["format"] != nil && !slices.Contains([]string{"pinned", "accepted", "live"}, fmt.Sprint(n.Attrs["format"])) {
			return ErrPageInvalidInput
		}
		if value := n.Attrs["filter"]; value != nil &&
			!slices.Contains([]string{"all", "active", "review", "done", "new"}, fmt.Sprint(value)) {
			return ErrPageInvalidInput
		}
		if value := n.Attrs["caption"]; value != nil {
			text, ok := value.(string)
			if !ok || len(text) > 2000 {
				return ErrPageInvalidInput
			}
		}
		if value := n.Attrs["sourceVersionIds"]; value != nil {
			raw, err := json.Marshal(value)
			var ids []string
			if err != nil || json.Unmarshal(raw, &ids) != nil || len(ids) > 16 {
				return ErrPageInvalidInput
			}
			for _, id := range ids {
				if _, err = uuid.Parse(id); err != nil {
					return ErrPageInvalidInput
				}
			}
		}
		for _, mark := range n.Marks {
			if !slices.Contains(
				[]string{"bold", "italic", "strike", "underline", "code", "link", "highlight"},
				mark.Type,
			) {
				return ErrPageInvalidInput
			}
			if mark.Type == "link" {
				href, _ := mark.Attrs["href"].(string)
				if !safeBlockURL(href, false) {
					return ErrPageInvalidInput
				}
			}
		}
		for _, child := range n.Content {
			if err := walk(child, n.Type, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, parentType, 0); err != nil {
		return err
	}
	raw, err := json.Marshal(root)
	if err != nil || len(raw) > 2<<20 {
		return ErrPageBodyTooLarge
	}
	return nil
}

func boundedBlockNumber(value any, minimum, maximum int) bool {
	number := intAttr(value, -1)
	return number >= minimum && number <= maximum && fmt.Sprint(value) == fmt.Sprint(number)
}

var workflowBlockKey = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func BlocksMarkdown(n BlockNode) string {
	var content strings.Builder
	for _, child := range n.Content {
		content.WriteString(BlocksMarkdown(child))
	}
	value := content.String()
	switch n.Type {
	case "wikiPageLink":
		label, _ := n.Attrs["label"].(string)
		label = strings.Join(strings.Fields(label), " ")
		var escaped strings.Builder
		for _, r := range label {
			if strings.ContainsRune("&<>[]\\`*_~|", r) {
				fmt.Fprintf(&escaped, "&#%d;", r)
			} else {
				escaped.WriteRune(r)
			}
		}
		if label != "" {
			return "[[" + fmt.Sprint(n.Attrs["id"]) + "|" + escaped.String() + "]]"
		}
		return "[[" + fmt.Sprint(n.Attrs["id"]) + "]]"
	case "wikiArtifactRef":
		ordinal := ""
		if n.Attrs["ordinal"] != nil {
			ordinal = "#" + fmt.Sprint(n.Attrs["ordinal"])
		}
		return "[[artifact:" + fmt.Sprint(n.Attrs["id"]) + ordinal + "]]"
	case "text":
		value = n.Text
		for _, mark := range n.Marks {
			switch mark.Type {
			case "bold":
				value = "**" + value + "**"
			case "italic":
				value = "*" + value + "*"
			case "strike":
				value = "~~" + value + "~~"
			case "code":
				value = "`" + value + "`"
			case "link":
				value = "[" + value + "](" + fmt.Sprint(mark.Attrs["href"]) + ")"
			case "underline":
				value = "<u>" + value + "</u>"
			}
		}
		return value
	case "heading":
		return strings.Repeat("#", intAttr(n.Attrs["level"], 1)) + " " + value + "\n\n"
	case "paragraph", "toggleSummary":
		return value + "\n\n"
	case "hardBreak":
		return "  \n"
	case "codeBlock", "mermaid":
		lang, _ := n.Attrs["language"].(string)
		if n.Type == "mermaid" {
			lang = "mermaid"
		}
		fence := "```"
		for strings.Contains(value, fence) {
			fence += "`"
		}
		return fence + lang + "\n" + value + "\n" + fence + "\n\n"
	case "listItem":
		return "- " + strings.ReplaceAll(strings.TrimSpace(value), "\n", "\n  ") + "\n"
	case "taskItem":
		check := " "
		if n.Attrs["checked"] == true {
			check = "x"
		}
		return "- [" + check + "] " + strings.TrimSpace(value) + "\n"
	case "orderedList":
		var list strings.Builder
		start := intAttr(n.Attrs["start"], 1)
		for i, item := range n.Content {
			line := strings.TrimPrefix(BlocksMarkdown(item), "- ")
			fmt.Fprintf(&list, "%d. %s", start+i, line)
		}
		return list.String() + "\n"
	case "bulletList", "taskList":
		return value + "\n"
	case "blockquote", "callout":
		return "> " + strings.ReplaceAll(strings.TrimSpace(value), "\n", "\n> ") + "\n\n"
	case "horizontalRule":
		return "---\n\n"
	case "image":
		alt, _ := n.Attrs["alt"].(string)
		return "![" + alt + "](" + fmt.Sprint(n.Attrs["src"]) + ")\n\n"
	case "table":
		var out strings.Builder
		for i, row := range n.Content {
			out.WriteString(BlocksMarkdown(row))
			if i == 0 {
				out.WriteString("|")
				for range row.Content {
					out.WriteString(" --- |")
				}
				out.WriteString("\n")
			}
		}
		return out.String() + "\n"
	case "tableRow":
		return "| " + value + "\n"
	case "tableCell", "tableHeader":
		var escaped strings.Builder
		start := 0
		for _, match := range wikiInlineReference.FindAllStringIndex(value, -1) {
			escaped.WriteString(strings.ReplaceAll(value[start:match[0]], "|", "\\|"))
			escaped.WriteString(value[match[0]:match[1]])
			start = match[1]
		}
		escaped.WriteString(strings.ReplaceAll(value[start:], "|", "\\|"))
		return strings.TrimSpace(escaped.String()) + " | "
	case "artifactReference":
		label, _ := n.Attrs["label"].(string)
		version, _ := n.Attrs["versionId"].(string)
		return "[" + label + "](#artifact-" + version + ")\n\n"
	case "taskReference", "taskCollection":
		var rows []struct {
			Title  string `json:"title"`
			Status string `json:"status"`
		}
		raw, _ := json.Marshal(n.Attrs["capturedTasks"])
		_ = json.Unmarshal(raw, &rows)
		var out strings.Builder
		for _, row := range rows {
			fmt.Fprintf(&out, "- %s — %s\n", row.Title, strings.ReplaceAll(row.Status, "_", " "))
		}
		return out.String() + "\n"
	case "outputCollection":
		var rows []struct {
			Title     string `json:"title"`
			VersionID string `json:"latestVersionId"`
		}
		raw, _ := json.Marshal(n.Attrs["capturedOutputs"])
		_ = json.Unmarshal(raw, &rows)
		var out strings.Builder
		for _, row := range rows {
			fmt.Fprintf(&out, "- [%s](#artifact-%s)\n", row.Title, row.VersionID)
		}
		return out.String() + "\n"
	case "workflowReference":
		var definition struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Highlights  []string `json:"highlights"`
		}
		raw, _ := json.Marshal(n.Attrs["capturedWorkflow"])
		_ = json.Unmarshal(raw, &definition)
		if definition.Name == "" {
			return "Workflow: " + fmt.Sprint(n.Attrs["workflowKey"]) + "\n\n"
		}
		return "### " + definition.Name + "\n\n" + definition.Description + "\n\n- " + strings.Join(
			definition.Highlights,
			"\n- ",
		) + "\n\n"
	case "fileBlock", "audioBlock", "videoBlock":
		label, _ := n.Attrs["label"].(string)
		src, _ := n.Attrs["src"].(string)
		return "[" + label + "](" + src + ")\n\n"
	case "equation":
		return "$$\n" + fmt.Sprint(n.Attrs["source"]) + "\n$$\n\n"
	default:
		return value
	}
}

func intAttr(value any, fallback int) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	default:
		return fallback
	}
}
