package wiki

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCompileSourceReplacement(t *testing.T) {
	t.Parallel()
	rootID, firstID, secondID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	paragraph := func(id, text string) BlockNode {
		attrs := map[string]any{}
		if id != "" {
			attrs["blockId"] = id
		}
		return BlockNode{Type: "paragraph", Attrs: attrs, Content: []BlockNode{{Type: "text", Text: text}}}
	}
	target := BlockNode{Type: "blockquote", Attrs: map[string]any{"blockId": rootID}, Content: []BlockNode{
		paragraph(firstID, "first"), paragraph(secondID, "second"),
	}}
	for _, tc := range []struct {
		name     string
		children []BlockNode
		valid    bool
	}{
		{"reorder", []BlockNode{paragraph(secondID, "second"), paragraph(firstID, "first")}, true},
		{"remove", []BlockNode{paragraph(secondID, "second")}, true},
		{"insert", []BlockNode{paragraph(firstID, "first"), paragraph("", "new")}, true},
		{"foreign identity", []BlockNode{paragraph(uuid.NewString(), "foreign")}, false},
		{"duplicate identity", []BlockNode{paragraph(firstID, "first"), paragraph(firstID, "copy")}, false},
		{"changed type", []BlockNode{{Type: "heading", Attrs: map[string]any{"blockId": firstID, "level": 2}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replacement := BlockNode{Type: target.Type, Attrs: target.Attrs, Content: tc.children}
			got, err := CompileSourceReplacement(BlocksSource(replacement), 1, target)
			if (err == nil) != tc.valid {
				t.Fatalf("replacement error = %v; valid = %v", err, tc.valid)
			}
			if !tc.valid {
				return
			}
			newID := uuid.NewString()
			assigned := 0
			AssignMissingBlockIDs(got, func() string { assigned++; return newID })
			if got.Attrs["blockId"] != rootID {
				t.Fatal("root identity changed")
			}
			if tc.name == "insert" && (assigned != 1 || got.Content[1].Attrs["blockId"] != newID) {
				t.Fatal("new child did not receive exactly one reserved identity")
			}
		})
	}
}

func TestCompileSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, source string
		want         string
	}{
		{"mixed", "# Review\n\n<text>Active tasks</text>", "uiDocument"},
		{"container", "<box>A <bold>finding</bold>.</box>", "uiDocument"},
		{"fenced", "```text\n<Select />\n```", "codeBlock"},
		{"tilde fence", "~~~~text\n<Scope>\n~~~\n</Scope>\n~~~~", "codeBlock"},
		{
			"native",
			"<Native node={\"type\":\"paragraph\",\"content\":[{\"type\":\"text\",\"text\":\"a > b / >\"}]} />",
			"paragraph",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := CompileSource(tt.source, 1)
			if err != nil {
				t.Fatal(err)
			}
			if got.Content[len(got.Content)-1].Type != tt.want {
				t.Fatalf("got = %+v, want last type %s", got, tt.want)
			}
		})
	}
}

func TestCompileSourceRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, source string
		version      int
	}{
		{"version", "text", 2},
		{"expression", "<text size=state.size />", 1},
		{"duplicate", "<text size=\"sm\" size=\"lg\" />", 1},
		{"missing close", "<box>text", 1},
		{"wrong close", "<box>text</row>", 1},
		{"native field", "<Native node={\"type\":\"paragraph\",\"surprise\":1} />", 1},
		{"native document", "<Native node={\"type\":\"doc\"} />", 1},
		{"trailing", "<text size={1 2} />", 1},
		{"too large", strings.Repeat("a", 262145), 1},
		{"too deep", strings.Repeat("<box>", 33), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := CompileSource(tt.source, tt.version)
			var diagnostic *SourceDiagnostic
			if !errors.Is(err, ErrPageInvalidInput) || !errors.As(err, &diagnostic) ||
				diagnostic.Repair == "" ||
				diagnostic.End < diagnostic.Start {
				t.Fatalf("got = %v, want located repair diagnostic", err)
			}
		})
	}
}

func TestSourcePreservesNativeIdentityAndCodec(t *testing.T) {
	t.Parallel()
	root := BlocksFromMarkdown(
		"## Review\n\n> A **finding** with [evidence](https://example.com).\n\n- First\n- Second",
	)
	AssignBlockIDs(&root, uuid.NewString)
	state, err := EncodeBlocks(root)
	if err != nil {
		t.Fatal(err)
	}
	_, canonical, _, err := MergeBlocks(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := CompileSource(BlocksSource(canonical), 1)
	if err != nil {
		t.Fatal(err)
	}
	if BlockHash(got) != BlockHash(canonical) {
		t.Fatal("source round trip changed hashes")
	}
}

func TestCompileOperationSource(t *testing.T) {
	t.Parallel()
	source := "Replacement text"
	op := BlockOperation{Type: "insert", Source: &source, LanguageVersion: 1}
	if err := CompileOperationSource(&op, nil); err != nil {
		t.Fatal(err)
	}
	if op.Content == nil || op.Content.Type != "paragraph" || op.Source != nil {
		t.Fatalf("got = %+v", op)
	}
	op = BlockOperation{Type: "update", Content: op.Content, Source: &source, LanguageVersion: 1}
	if err := CompileOperationSource(&op, nil); err == nil {
		t.Fatal("accepted two authorities")
	}
	source = "First\n\nSecond"
	op = BlockOperation{Type: "insert", Source: &source, LanguageVersion: 1}
	if err := CompileOperationSource(&op, nil); err == nil {
		t.Fatal("accepted multiple subtrees")
	}
}

func TestSourceUpdatePreservesAddressedDescendants(t *testing.T) {
	t.Parallel()
	root := BlocksFromMarkdown("> First\n\n> Second")
	AssignBlockIDs(&root, uuid.NewString)
	target := root.Content[0]
	replacement := BlocksSource(target)
	op := BlockOperation{
		Type:            "update",
		BlockID:         target.Attrs["blockId"].(string),
		Source:          &replacement,
		LanguageVersion: 1,
	}
	if err := CompileOperationSource(&op, &target); err != nil {
		t.Fatal(err)
	}
	modified := target
	modified.Content = nil
	replacement = BlocksSource(modified)
	op.Content = nil
	op.Source = &replacement
	op.LanguageVersion = 1
	if err := CompileOperationSource(&op, &target); err == nil {
		t.Fatal("source update dropped addressed children")
	}
}

func TestIntelligentSkillExamplesCompile(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../mcp/tools/builtin/zenith-intelligent-ui/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	sections := strings.Split(string(raw), "```text\n")
	if len(sections) < 2 {
		t.Fatal("authoring skill has no runnable source examples")
	}
	for _, section := range sections[1:] {
		example, _, ok := strings.Cut(section, "```")
		if !ok {
			t.Fatal("unclosed source example")
		}
		if _, err = CompileSource(example, 1); err != nil {
			t.Fatalf("skill source example: %v", err)
		}
	}
}

func TestReactiveSourceSurvivesDocumentCodec(t *testing.T) {
	t.Parallel()
	source := `{@body const [quantity, setQuantity] = DIL.useState(2);}
<box><input inputType="number" value={quantity} onChange={setQuantity}/><text>{quantity * 1.25}</text></box>`
	root, err := CompileSource(source, 1)
	if err != nil {
		t.Fatal(err)
	}
	AssignBlockIDs(&root, uuid.NewString)
	state, err := EncodeBlocks(root)
	if err != nil {
		t.Fatal(err)
	}
	_, decoded, _, err := MergeBlocks(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if BlockHash(decoded) != BlockHash(root) {
		t.Fatal("CRDT round trip changed reactive definitions")
	}
}

func TestIntelligentSkillGalleryAndOrbitRoundTrip(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"component-demo.zenith-ui", "orbit-demo.zenith-ui"} {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile("../mcp/tools/builtin/zenith-intelligent-ui/" + name)
			if err != nil {
				t.Fatal(err)
			}
			root, err := CompileSource(string(source), 1)
			if err != nil {
				t.Fatal(err)
			}
			AssignBlockIDs(&root, uuid.NewString)
			state, err := EncodeBlocks(root)
			if err != nil {
				t.Fatal(err)
			}
			_, decoded, _, err := MergeBlocks(state, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(BlocksSource(decoded)); got != strings.TrimSpace(string(source)) {
				t.Fatal("Page codec changed the authored example source")
			}
		})
	}
}

func TestPreviewSourceRecoversIncompleteConstructs(t *testing.T) {
	source := `<box><title>Ready</title><input value={"hello"}/></box>`
	for end := 1; end <= len(source); end++ {
		preview := PreviewSource(source[:end], 1)
		if len(preview.Diagnostics) > 0 {
			t.Fatalf("prefix %q: %#v", source[:end], preview.Diagnostics)
		}
		if end == len(source) && !preview.Complete {
			t.Fatal("complete source not recognized")
		}
	}
}

// Catalog fixtures test native document validation independently of the public
// inline authoring grammar. Native envelopes still use this internal decoder.
func compileCatalogFixture(source string, version int) (BlockNode, error) {
	return parseSource(source, version, sourceCompileOptions{document: true})
}
