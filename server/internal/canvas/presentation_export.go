package canvas

import (
	"bytes"
	"fmt"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// RenderPresentationHTML renders an already-authorized immutable export. It never
// resolves sources or embeds remote resources; images retain only their alt text.
func RenderPresentationHTML(markdown string) ([]byte, error) {
	source := []byte(markdown)
	md := goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough))
	document := md.Parser().Parse(text.NewReader(source))
	var images []*ast.Image
	if err := ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if image, ok := node.(*ast.Image); entering && ok {
			images = append(images, image)
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	}); err != nil {
		return nil, fmt.Errorf("inspect presentation export: %w", err)
	}
	for _, image := range images {
		label := append([]byte("[Image: "), presentationImageText(image, source)...)
		label = append(label, ']')
		image.Parent().ReplaceChild(image.Parent(), image, ast.NewString(label))
	}
	var output bytes.Buffer
	output.WriteString(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'">
<meta name="referrer" content="no-referrer">
<title>Zenith Team capture</title>
<style>
:root{color-scheme:light dark}body{font:16px/1.6 system-ui,sans-serif;margin:0 auto;padding:2rem;max-width:70rem;overflow-wrap:anywhere}
h1,h2,h3{line-height:1.25}table{border-collapse:collapse;display:block;overflow:auto}th,td{border:1px solid #888;padding:.5rem .75rem;text-align:left}
pre{overflow:auto;padding:1rem;border:1px solid #888}code{font-size:.9em}blockquote{border-left:3px solid #888;margin-left:0;padding-left:1rem}
.capture-note{border-bottom:1px solid #888;padding-bottom:1rem;font-size:.875rem}
@media print{body{max-width:none;padding:0}pre,table{overflow:visible}}
</style></head><body>
<p class="capture-note">Saved capture exported from Zenith Team. Values and assumptions reflect the retained snapshot. Images are represented by their descriptions; links may require access to the original source.</p>
<main>
`)
	if err := md.Renderer().Render(&output, source, document); err != nil {
		return nil, fmt.Errorf("render presentation export: %w", err)
	}
	output.WriteString("</main></body></html>\n")
	return output.Bytes(), nil
}

func presentationImageText(node ast.Node, source []byte) []byte {
	switch node := node.(type) {
	case *ast.Text:
		value := node.Value(source)
		if node.SoftLineBreak() {
			return append(bytes.Clone(value), '\n')
		}
		return value
	case *ast.String:
		return node.Value
	case *ast.AutoLink:
		return node.Label(source)
	case *ast.RawHTML:
		return node.Segments.Value(source)
	}
	var value []byte
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		value = append(value, presentationImageText(child, source)...)
	}
	return value
}
