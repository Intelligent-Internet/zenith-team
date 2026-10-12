package canvas

import (
	"encoding/json"
	"testing"
	"unicode/utf16"

	"boardly.dev/boardly/server/internal/wiki"
	db "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
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
			target := DocumentWritingTarget{BlockID: block.Attrs["blockId"].(string), Text: text, From: test.from, To: test.to}
			replacement := writingReplacement(base, target, writingResponse(test.response))
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
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			base := wiki.BlocksFromMarkdown(test.markdown)
			wiki.AssignBlockIDs(&base, uuid.NewString)
			block := base.Content[0]
			text, _ := writingBlockText(block)
			target := DocumentWritingTarget{BlockID: block.Attrs["blockId"].(string), Text: text, To: len(utf16.Encode([]rune(text)))}
			replacement := writingReplacement(base, target, writingResponse(test.response))
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
			row := db.ProjectPageChange{BeforeContent: beforeJSON, Operations: operationsJSON, Decisions: []byte("{}"), Status: "proposed"}
			review, err := pageSuggestionState(row, base, "default", nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, suggestion := range review.suggestions {
				if suggestion.Status != "pending" || len(suggestion.Group) != len(operations) {
					t.Fatalf("review = %+v, want all operations pending in one group", review.suggestions)
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
