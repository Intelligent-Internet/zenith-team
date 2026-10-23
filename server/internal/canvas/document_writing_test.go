package canvas

import (
	"encoding/json"
	"testing"
	"unicode/utf16"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestWritingReplacementWhitespace(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, markdown, response, want string
		from, to                       int
	}{
		{"code indentation", "```python\nif ready:\n    old()\n```", "    new()\n", "if ready:\n    new()\n", 10, 20},
		{"code tabs", "```go\nold()\n```", "\tnew()\n", "\tnew()\n", 0, 6},
		{"insert prose", "Hello world", " dear", "Hello dear world", 5, 5},
		{"replace prose", "Hello world", "Hi ", "Hi world", 0, 6},
		{"both boundaries", "Hello world", " kind ", "Hello kind world", 5, 6},
		{"formatted prose", "Hello world", " **kind** ", "Hello kind world", 5, 6},
		{"UTF-16 selection", "Hi 😀 friend", "dear", "Hi dear friend", 3, 5},
		{"outer fence", "```python\nold()\n```", "```markdown\n    new()\n```", "    new()\n", 0, 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			base := wiki.BlocksFromMarkdown(test.markdown)
			wiki.AssignBlockIDs(&base, uuid.NewString)
			block := base.Content[0]
			text, _ := writingBlockText(block)
			target := DocumentWritingTarget{
				BlockID: block.Attrs["blockId"].(string),
				Text:    text,
				From:    test.from,
				To:      test.to,
			}
			replacement, err := writingReplacement(base, target, writingResponse(test.response))
			if err != nil {
				t.Fatal(err)
			}
			got, _ := writingBlockText(replacement.Content[0])
			if got != test.want {
				t.Fatalf("replacement = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWritingOperationsReviewAndUndo(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, markdown, response, want string
	}{
		{"paragraph to heading", "Original", "# Heading", "# Heading\n\n"},
		{"empty paragraph to list", "", "- First\n- Second", "- First\n- Second\n\n"},
		{"heading to paragraphs", "# Original", "First\n\nSecond", "First\n\nSecond\n\n"},
		{"paragraphs", "Original", "First\n\nSecond\n\nThird", "First\n\nSecond\n\nThird\n\n"},
		{"heading and paragraph", "Original", "# Heading\n\nBody", "# Heading\n\nBody\n\n"},
		{"interactive UI", "Original", `<button>Try it</button>`, "<button>Try it</button>\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			base := wiki.BlocksFromMarkdown(test.markdown)
			wiki.AssignBlockIDs(&base, uuid.NewString)
			block := base.Content[0]
			text, _ := writingBlockText(block)
			target := DocumentWritingTarget{
				BlockID: block.Attrs["blockId"].(string),
				Text:    text,
				To:      len(utf16.Encode([]rune(text))),
			}
			replacement, err := writingReplacement(base, target, writingResponse(test.response))
			if err != nil {
				t.Fatal(err)
			}
			wiki.AssignBlockIDs(&replacement, uuid.NewString)
			operations := writingOperations(base, target, replacement)
			state, err := wiki.EncodeBlocks(base)
			if err != nil {
				t.Fatal(err)
			}
			afterState, after, err := wiki.ApplyBlockOperations(state, operations)
			if err != nil {
				t.Fatalf("apply writing proposal: %v", err)
			}
			if got := wiki.BlocksMarkdown(after); got != test.want {
				t.Fatalf("applied Markdown = %q, want %q", got, test.want)
			}
			beforeJSON, err := json.Marshal(base)
			if err != nil {
				t.Fatal(err)
			}
			operationsJSON, err := json.Marshal(operations)
			if err != nil {
				t.Fatal(err)
			}
			row := db.ProjectPageChange{
				BeforeContent: beforeJSON,
				Operations:    operationsJSON,
				Decisions:     []byte("{}"),
				Status:        "proposed",
			}
			review, err := pageSuggestionState(
				row,
				pageSuggestionSnapshot{root: base, theme: "default"},
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			groupSize := len(operations)
			if test.name == "paragraphs" {
				groupSize = 1
			}
			for _, suggestion := range review.suggestions {
				if suggestion.Status != "pending" || len(suggestion.Group) != groupSize {
					t.Fatalf(
						"review = %+v, want pending groups of %d",
						review.suggestions,
						groupSize,
					)
				}
			}
			if _, err = suggestionSelection(review, PageChangeReviewInput{Action: "apply"}); err != nil {
				t.Fatalf("accept writing proposal: %v", err)
			}
			_, restored, err := undoPageOperations(afterState, base, operations)
			if err != nil {
				t.Fatalf("undo writing proposal: %v", err)
			}
			if wiki.BlockHash(restored) != wiki.BlockHash(base) {
				t.Fatalf("undo = %+v, want %+v", restored, base)
			}
		})
	}
}

func TestWritingReplacementIntelligentUI(t *testing.T) {
	t.Parallel()
	source := "{@body const [count, setCount] = DIL.useState(2);}\n" +
		`<button onClick={() => setCount(count + 1)}>{count}</button>`
	for _, test := range []struct {
		name, markdown, response string
		from, to                 int
		wantTypes                []string
		wantError                bool
	}{
		{"insert UI", "Before after", source, 7, 7, []string{"paragraph", "uiDocument", "paragraph"}, false},
		{"replace UI", "Original", source, 0, 8, []string{"uiDocument"}, false},
		{"mixed prose and UI", "Original", "# Estimate\n\n" + source, 0, 8, []string{"uiDocument"}, false},
		{"fenced UI stays code", "Original", "```text\n" + source + "\n```", 0, 8, []string{"codeBlock"}, false},
		{"code target stays code", "```text\nOriginal\n```", source, 0, 8, []string{"codeBlock"}, false},
		{"incomplete UI", "Original", "<box><text>Unfinished", 0, 8, nil, true},
		{"invalid UI", "Original", "<text>{1 + }</text>", 0, 8, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			base := wiki.BlocksFromMarkdown(test.markdown)
			wiki.AssignBlockIDs(&base, uuid.NewString)
			block := base.Content[0]
			text, _ := writingBlockText(block)
			replacement, err := writingReplacement(base, DocumentWritingTarget{
				BlockID: block.Attrs["blockId"].(string), Text: text, From: test.from, To: test.to,
			}, test.response)
			if test.wantError {
				if err == nil {
					t.Fatal("invalid UI was staged as literal Markdown")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(replacement.Content) != len(test.wantTypes) {
				t.Fatalf("replacement = %+v, want types %v", replacement, test.wantTypes)
			}
			for i, node := range replacement.Content {
				if node.Type != test.wantTypes[i] {
					t.Fatalf("block %d = %s, want %s", i, node.Type, test.wantTypes[i])
				}
				if node.Type == "uiDocument" && wiki.InlineSource(node) != test.response {
					t.Fatal("interactive source was not retained losslessly")
				}
			}
			if err := wiki.ValidateBlockImport(replacement); err != nil {
				t.Fatalf("replacement is not a valid native Page document: %v", err)
			}
		})
	}
}
