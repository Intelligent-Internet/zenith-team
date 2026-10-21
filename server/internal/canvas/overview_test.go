package canvas

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

func TestOverviewProtectsHeadingSection(t *testing.T) {
	root := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{
		{Type: "heading", Attrs: map[string]any{"blockId": "protected", "level": 2, "agentProtected": true}},
		{Type: "paragraph", Attrs: map[string]any{"blockId": "inside"}},
		{Type: "heading", Attrs: map[string]any{"blockId": "subheading", "level": 3}},
		{Type: "paragraph", Attrs: map[string]any{"blockId": "nested"}},
		{Type: "heading", Attrs: map[string]any{"blockId": "next", "level": 2}},
		{Type: "paragraph", Attrs: map[string]any{"blockId": "outside"}},
	}}
	for _, id := range []string{"protected", "inside", "subheading", "nested"} {
		if !blockProtected(root, id) {
			t.Errorf("%s escaped section protection", id)
		}
	}
	for _, id := range []string{"", "missing", "next", "outside"} {
		if blockProtected(root, id) {
			t.Errorf("%s was incorrectly protected", id)
		}
	}
}

func TestOverviewNestedProtectionAndMoveDestination(t *testing.T) {
	t.Parallel()
	root := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{
		{Type: "paragraph", Attrs: map[string]any{"blockId": "outside"}},
		{Type: "heading", Attrs: map[string]any{"blockId": "section", "level": 2, "agentProtected": true}},
		{Type: "heading", Attrs: map[string]any{"blockId": "nested", "level": 3, "agentProtected": true}},
		{Type: "heading", Attrs: map[string]any{"blockId": "peer", "level": 3}},
		{Type: "paragraph", Attrs: map[string]any{"blockId": "inside"}},
		{Type: "heading", Attrs: map[string]any{"blockId": "next", "level": 2}},
	}}
	if !blockProtected(root, "inside") {
		t.Fatal("nested heading lost enclosing section protection")
	}
	for _, test := range []struct {
		name string
		op   wiki.BlockOperation
		want bool
	}{
		{"move into section", wiki.BlockOperation{Type: "move", BlockID: "outside", AfterID: "inside"}, true},
		{"move before section end", wiki.BlockOperation{Type: "move", BlockID: "outside", BeforeID: "next"}, true},
		{"append to protected section", wiki.BlockOperation{Type: "insert", AfterID: "section"}, true},
		{"before section starts", wiki.BlockOperation{Type: "insert", BeforeID: "section"}, false},
		{"append after unprotected heading", wiki.BlockOperation{Type: "insert", AfterID: "next"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := operationProtected(root, test.op); got != test.want {
				t.Fatalf("protected = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDeclaredSourcesAreBoundedAndDeduplicated(t *testing.T) {
	t.Parallel()
	first, second := uuid.New(), uuid.New()
	root := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{
		{
			Type:  "paragraph",
			Attrs: map[string]any{"sourceVersionIds": []string{first.String(), second.String()}},
		},
		{Type: "artifactReference", Attrs: map[string]any{"versionId": first.String()}},
	}}
	got := declaredSources(root)
	if len(got) != 2 || !slices.Contains(got, first) || !slices.Contains(got, second) {
		t.Fatalf("sources = %v, want both unique IDs", got)
	}
}

func TestOverviewUndoPreservesLaterUnrelatedContent(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"remove", "move"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			before := wiki.BlocksFromMarkdown("First\n\nSecond")
			wiki.AssignBlockIDs(&before, uuid.NewString)
			state, err := wiki.EncodeBlocks(before)
			if err != nil {
				t.Fatal(err)
			}
			first := before.Content[0]
			op := wiki.BlockOperation{
				Type:         operation,
				BlockID:      first.Attrs["blockId"].(string),
				ExpectedHash: wiki.BlockHash(first),
			}
			state, _, err = wiki.ApplyBlockOperations(state, []wiki.BlockOperation{op})
			if err != nil {
				t.Fatal(err)
			}
			later := wiki.BlocksFromMarkdown("A later human note").Content[0]
			later.Attrs = map[string]any{"blockId": uuid.NewString()}
			state, _, err = wiki.ApplyBlockOperations(
				state,
				[]wiki.BlockOperation{
					{
						Type:     "insert",
						BeforeID: before.Content[1].Attrs["blockId"].(string),
						Content:  &later,
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, got, err := undoPageOperations(state, before, []wiki.BlockOperation{op})
			if err != nil || len(got.Content) != 3 ||
				!strings.Contains(wiki.BlocksMarkdown(got), "A later human note") {
				t.Fatalf("undo = %+v, %v", got, err)
			}
		})
	}
}
