package wiki

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"
)

//go:embed inline_parser.generated.js
var inlineParserSource string

// Only the release-owned parser executes. Authored declarations and callbacks
// are parsed to AST nodes, never evaluated by this VM. A fresh VM per parse
// prevents one request from sharing mutable globals with another.
var inlineParserProgram = sync.OnceValues(func() (*goja.Program, error) {
	return goja.Compile("inline-parser", inlineParserSource, true)
})

type inlineSourceInfo struct {
	complete    bool
	interactive bool
	diagnostic  string
	diagnostics []SourceDiagnostic
}

// HasInlineUI identifies source definitions whose hook state lives only in the
// mounted renderer and cannot be frozen by the native capture evaluator.
func HasInlineUI(root BlockNode) bool {
	return root.Type == "uiDocument" || slices.ContainsFunc(root.Content, HasInlineUI)
}

func inspectInlineSource(source string, version int) (inlineSourceInfo, error) {
	return inspectInlineSourceMode(source, version, false)
}

func inspectInlineSourceMode(source string, version int, recover bool) (inlineSourceInfo, error) {
	fail := func(code, repair string) error {
		return &SourceDiagnostic{
			Code:     code,
			Severity: "error",
			End:      len(source),
			Repair:   repair,
		}
	}
	if version != 1 {
		return inlineSourceInfo{}, fail("source.version", "Use languageVersion 1.")
	}
	if len(source) > 262144 || !utf8.ValidString(source) {
		return inlineSourceInfo{}, fail("source.size", "Use valid UTF-8 source of at most 256 KiB.")
	}
	program, err := inlineParserProgram()
	if err != nil {
		return inlineSourceInfo{}, fmt.Errorf("compile inline parser: %w", err)
	}
	vm := goja.New()
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	timer := time.AfterFunc(time.Second, func() { vm.Interrupt("source parse budget exceeded") })
	defer timer.Stop()
	if _, err := vm.RunProgram(program); err != nil {
		return inlineSourceInfo{}, fail("source.budget", "The source exceeded its parsing budget.")
	}
	inspect, ok := goja.AssertFunction(vm.Get("ZenithInline").ToObject(vm).Get("inspectInlineSource"))
	if !ok {
		return inlineSourceInfo{}, fmt.Errorf("inline parser entry point unavailable")
	}
	value, err := inspect(goja.Undefined(), vm.ToValue(source), vm.ToValue(recover))
	if err != nil {
		return inlineSourceInfo{}, fail("source.parse", "The source could not be parsed within its limits.")
	}
	result := value.ToObject(vm)
	info := inlineSourceInfo{
		complete:    result.Get("complete").ToBoolean(),
		interactive: result.Get("interactive").ToBoolean(),
		diagnostic:  result.Get("diagnostic").String(),
	}
	if err := vm.ExportTo(result.Get("diagnostics"), &info.diagnostics); err != nil {
		return inlineSourceInfo{}, fmt.Errorf("decode inline diagnostics: %w", err)
	}
	if info.diagnostic != "" {
		return inlineSourceInfo{}, fail("source.syntax", info.diagnostic)
	}
	return info, nil
}

// InlineSource reads the source text shared by the editor and the renderer.
// It is stored as XML text, so concurrent edits keep their character identities.
func InlineSource(node BlockNode) string {
	var source strings.Builder
	for _, child := range node.Content {
		source.WriteString(child.Text)
	}
	return source.String()
}

func inlineSourceDocument(source string) BlockNode {
	return BlockNode{
		Type: "doc",
		Content: []BlockNode{
			{
				Type:    "uiDocument",
				Attrs:   map[string]any{"catalogVersion": 1},
				Content: []BlockNode{{Type: "text", Text: source}},
			},
		},
	}
}

// ValidateInlinePublication separates recoverable draft syntax from evidence.
// Draft text may be incomplete while collaborators type; published content may not.
func ValidateInlinePublication(root BlockNode) error {
	if root.Type == "uiDocument" {
		info, err := inspectInlineSource(InlineSource(root), 1)
		if err != nil {
			return err
		}
		if !info.complete {
			return uiError("source.incomplete")
		}
	}
	for _, child := range root.Content {
		if err := ValidateInlinePublication(child); err != nil {
			return err
		}
	}
	return nil
}

func compileInlineSource(source string, version int) (BlockNode, error) {
	info, err := inspectInlineSource(source, version)
	if err != nil {
		return BlockNode{}, err
	}
	if !info.complete {
		return BlockNode{}, &SourceDiagnostic{
			Code:     "source.incomplete",
			Severity: "error",
			End:      len(source),
			Repair:   "Complete the open component or expression before saving.",
		}
	}
	if !info.interactive {
		return BlocksFromMarkdown(source), nil
	}
	return inlineSourceDocument(source), nil
}
