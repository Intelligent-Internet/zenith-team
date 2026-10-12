package mailbox

import (
	"bytes"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var allowedHTMLTags = map[string]struct{}{
	"a": {}, "b": {}, "blockquote": {}, "br": {}, "code": {}, "del": {},
	"div": {}, "em": {}, "h1": {}, "h2": {}, "h3": {}, "h4": {}, "h5": {},
	"h6": {}, "hr": {}, "i": {}, "ins": {}, "li": {}, "ol": {}, "p": {},
	"pre": {}, "s": {}, "span": {}, "strong": {}, "table": {}, "tbody": {},
	"td": {}, "tfoot": {}, "th": {}, "thead": {}, "tr": {}, "u": {}, "ul": {},
}

var blockedHTMLTags = map[string]struct{}{
	"applet": {}, "audio": {}, "base": {}, "button": {}, "canvas": {},
	"embed": {}, "form": {}, "frame": {}, "frameset": {}, "iframe": {},
	"img": {}, "input": {}, "link": {}, "math": {}, "meta": {}, "object": {},
	"script": {}, "select": {}, "source": {}, "style": {}, "svg": {},
	"textarea": {}, "video": {},
}

func sanitizeHTML(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	contextNode := &html.Node{Type: html.ElementNode, DataAtom: atom.Div, Data: "div"}
	nodes, err := html.ParseFragment(strings.NewReader(value), contextNode)
	if err != nil {
		return ""
	}
	root := &html.Node{Type: html.ElementNode, DataAtom: atom.Div, Data: "div"}
	for _, node := range nodes {
		root.AppendChild(node)
	}
	sanitizeHTMLChildren(root)
	var output bytes.Buffer
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if err := html.Render(&output, child); err != nil {
			return ""
		}
	}
	return output.String()
}

func sanitizeHTMLChildren(parent *html.Node) {
	for child := parent.FirstChild; child != nil; {
		next := child.NextSibling
		switch child.Type {
		case html.CommentNode, html.DoctypeNode:
			parent.RemoveChild(child)
		case html.ElementNode:
			child.Data = strings.ToLower(child.Data)
			if _, blocked := blockedHTMLTags[child.Data]; blocked {
				parent.RemoveChild(child)
				break
			}
			sanitizeHTMLChildren(child)
			if _, allowed := allowedHTMLTags[child.Data]; !allowed {
				for grandchild := child.FirstChild; grandchild != nil; {
					nextGrandchild := grandchild.NextSibling
					child.RemoveChild(grandchild)
					parent.InsertBefore(grandchild, child)
					grandchild = nextGrandchild
				}
				parent.RemoveChild(child)
				break
			}
			child.Attr = sanitizeHTMLAttributes(child.Data, child.Attr)
		}
		child = next
	}
}

func sanitizeHTMLAttributes(tag string, attributes []html.Attribute) []html.Attribute {
	clean := make([]html.Attribute, 0, len(attributes))
	for _, attribute := range attributes {
		name := strings.ToLower(attribute.Key)
		switch {
		case name == "title" || name == "dir" || name == "lang":
			clean = append(clean, html.Attribute{Key: name, Val: attribute.Val})
		case tag == "a" && name == "href" && safeLink(attribute.Val):
			clean = append(clean, html.Attribute{Key: "href", Val: attribute.Val})
		case (tag == "td" || tag == "th") && (name == "colspan" || name == "rowspan"):
			clean = append(clean, html.Attribute{Key: name, Val: attribute.Val})
		}
	}
	return clean
}

func safeLink(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "mailto", "tel":
		return true
	default:
		return false
	}
}

func sanitizeText(value string) string {
	return strings.ToValidUTF8(value, "�")
}
