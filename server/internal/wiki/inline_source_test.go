package wiki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestInlineSourceSharedGrammar(t *testing.T) {
	source := `{@body const [count, setCount] = DIL.useState(2);}<box border padding={4}><title>Hello</title>{#if count > 0}<button onClick={() => setCount(count + 1)}>{count}</button>{/if}</box>`
	root, err := CompileSource(source, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Content) != 1 || root.Content[0].Type != "uiDocument" {
		t.Fatalf("unexpected document: %#v", root)
	}
	if err := ValidateUIDocument(root); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(BlocksSource(root)) != source {
		t.Fatal("source round trip changed")
	}
}

func TestInlineSourceStreamingAndLiteralCode(t *testing.T) {
	source := `<box><title>Hello</title><text>Still arriving`
	preview := PreviewSource(source, 1)
	if preview.Complete || preview.Content == nil {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	if _, err := CompileSource(source, 1); err == nil {
		t.Fatal("incomplete source was accepted for saving")
	}
	root, err := CompileSource("```jsx\n<box>literal</box>\n```", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Content) != 1 || root.Content[0].Type != "codeBlock" {
		t.Fatalf("code fence interpreted: %#v", root)
	}
}

func TestInlineSourceDoesNotExecuteDeclarations(t *testing.T) {
	// Parsing a declaration must not execute even an unbounded script.
	root, err := CompileSource(`{@body while (true) {}}<text>Unexecuted</text>`, 1)
	if err != nil {
		t.Fatal(err)
	}
	if root.Content[0].Type != "uiDocument" {
		t.Fatal("expected parsed source")
	}
	if _, err := CompileSource(strings.Repeat("x", 262145), 1); err == nil {
		t.Fatal("accepted oversized source")
	}
}

func TestInlineSourceAppBlockHTML(t *testing.T) {
	// HTML scripts are parsed as source, never executed by the server.
	for _, source := range []string{
		`<AppBlock app_block_id="chess"><div>Chess</div><script>while (true) {}</script></AppBlock>`,
		"<AppBlock app_block_id=\"chess\" html={`<!doctype html><html><body><div>Chess</div><script>while (true) {}</script></body></html>`} />",
	} {
		root, err := CompileSource(source, 1)
		if err != nil {
			t.Fatalf("AppBlock source rejected: %v", err)
		}
		if len(root.Content) != 1 || root.Content[0].Type != "uiDocument" {
			t.Fatalf("unexpected document: %#v", root)
		}
		if strings.TrimSpace(BlocksSource(root)) != source {
			t.Fatal("AppBlock source round trip changed")
		}
	}
	if _, err := CompileSource(`<AppBlock html={42} />`, 1); err == nil || !strings.Contains(err.Error(), "AppBlock.html requires string") {
		t.Fatalf("non-string HTML must be rejected: %v", err)
	}
}

func TestInlineSourceNativePreviewRoundTrip(t *testing.T) {
	root := BlocksFromMarkdown("A native paragraph.")
	source := BlocksSource(root)
	preview := PreviewSource(source, 1)
	if !preview.Complete || preview.Content == nil || BlockHash(*preview.Content) != BlockHash(root) {
		t.Fatalf("native source preview changed: %#v", preview)
	}
}

func TestInlineSourceUsesCollaborativeText(t *testing.T) {
	t.Parallel()
	root, err := CompileSource(`<text>Original start. Original end.</text>`, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := root.Content[0].Attrs["source"]; exists {
		t.Fatal("source must not be an atomic attribute")
	}
	AssignBlockIDs(&root, uuid.NewString)
	base, err := EncodeBlocks(root)
	if err != nil {
		t.Fatal(err)
	}
	original := root.Content[0]
	id, ok := original.Attrs["blockId"].(string)
	if !ok {
		t.Fatal("missing block identity")
	}
	var deltas [][]byte
	for _, source := range []string{`<text>Agent start. Original end.</text>`, `<text>Original start. Human end.</text>`} {
		proposed := original
		proposed.Content = []BlockNode{{Type: "text", Text: source}}
		updates, err := BlockOperationUpdates(
			base,
			[]BlockOperation{
				{Type: "update", BlockID: id, ExpectedHash: BlockHash(original), Content: &proposed},
			},
		)
		if err != nil || len(updates[0]) == 0 {
			t.Fatalf("missing source delta: %v", err)
		}
		deltas = append(deltas, updates[0])
	}
	merged, _, _, err := MergeBlocks(base, deltas[0])
	if err != nil {
		t.Fatal(err)
	}
	_, got, _, err := MergeBlocks(merged, deltas[1])
	if err != nil || strings.TrimSpace(BlocksSource(got)) != `<text>Agent start. Human end.</text>` {
		t.Fatalf("concurrent source merge = %q, %v", BlocksSource(got), err)
	}
}

func TestInlineSourceDraftAllowsIncompleteText(t *testing.T) {
	t.Parallel()
	root := inlineSourceDocument(`<text>Typing`)
	AssignBlockIDs(&root, uuid.NewString)
	if _, err := EncodeBlocks(root); err != nil {
		t.Fatalf("incomplete draft cannot be saved: %v", err)
	}
	if err := ValidateInlinePublication(root); err == nil {
		t.Fatal("incomplete draft can be published")
	}
}

func TestFinalSkillExamplesCompile(t *testing.T) {
	paths, err := filepath.Glob("../mcp/tools/builtin/zenith-intelligent-ui/references/example-*.zenith-ui")
	if err != nil || len(paths) == 0 {
		t.Fatalf("missing final skill fixtures: %v", err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := CompileSource(string(source), 1); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, source := range []string{
		`<grid-item><text>Orphan</text></grid-item>`,
		`<table-cell>Orphan</table-cell>`,
		`<box><flow-item>Wrong parent</flow-item></box>`,
		`<radio value="a">Orphan</radio>`,
		`<box padding=oops/>`,
	} {
		if _, err := CompileSource(source, 1); err == nil {
			t.Fatalf("accepted invalid structure/value: %s", source)
		}
	}
}
