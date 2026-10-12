package canvas

import (
	"encoding/json"
	"errors"
	"testing"

	generated "boardly.dev/boardly/server/pkg/db/generated"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/wiki"
)

func TestSuggestionApplicability(t *testing.T) {
	t.Parallel()
	paragraph := func(id, text string) wiki.BlockNode {
		return wiki.BlockNode{
			Type:    "paragraph",
			Attrs:   map[string]any{"blockId": id},
			Content: []wiki.BlockNode{{Type: "text", Text: text}},
		}
	}
	base := wiki.BlockNode{
		Type:    "doc",
		Content: []wiki.BlockNode{paragraph("a", "Original"), paragraph("b", "Other")},
	}
	proposed := paragraph("a", "Proposed")
	op := wiki.BlockOperation{
		Type:         "update",
		BlockID:      "a",
		ExpectedHash: wiki.BlockHash(base.Content[0]),
		Content:      &proposed,
	}
	cases := []struct {
		name    string
		current wiki.BlockNode
		want    string
	}{
		{"unchanged", base, "pending"},
		{
			"unrelated edit",
			wiki.BlockNode{
				Type:    "doc",
				Content: []wiki.BlockNode{base.Content[0], paragraph("b", "Human edit")},
			},
			"pending",
		},
		{
			"same block edited",
			wiki.BlockNode{
				Type:    "doc",
				Content: []wiki.BlockNode{paragraph("a", "Human edit"), base.Content[1]},
			},
			"needs_update",
		},
		{
			"already reflected",
			wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{proposed, base.Content[1]}},
			"already_reflected",
		},
		{
			"removed target",
			wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{base.Content[1]}},
			"needs_update",
		},
		{
			"moved target",
			wiki.BlockNode{
				Type: "doc",
				Content: []wiki.BlockNode{
					{
						Type:    "blockquote",
						Attrs:   map[string]any{"blockId": "c"},
						Content: []wiki.BlockNode{base.Content[0]},
					},
					base.Content[1],
				},
			},
			"needs_update",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := suggestionApplicability(base, tt.current, op); got != tt.want {
				t.Fatalf("status = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSuggestionScopesOverlap(t *testing.T) {
	t.Parallel()
	root := wiki.BlockNode{
		Type: "doc",
		Content: []wiki.BlockNode{
			{
				Type:  "blockquote",
				Attrs: map[string]any{"blockId": "parent"},
				Content: []wiki.BlockNode{
					{Type: "paragraph", Attrs: map[string]any{"blockId": "child"}},
				},
			},
			{Type: "paragraph", Attrs: map[string]any{"blockId": "other"}},
		},
	}
	cases := []struct {
		name string
		a, b wiki.BlockOperation
		want bool
	}{
		{
			"same target",
			wiki.BlockOperation{Type: "update", BlockID: "child"},
			wiki.BlockOperation{Type: "update", BlockID: "child"},
			true,
		},
		{
			"parent deletion",
			wiki.BlockOperation{Type: "remove", BlockID: "parent"},
			wiki.BlockOperation{Type: "update", BlockID: "child"},
			true,
		},
		{
			"independent blocks",
			wiki.BlockOperation{Type: "update", BlockID: "child"},
			wiki.BlockOperation{Type: "update", BlockID: "other"},
			false,
		},
		{
			"same insertion slot",
			wiki.BlockOperation{Type: "insert", AfterID: "other"},
			wiki.BlockOperation{Type: "insert", AfterID: "other"},
			true,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := suggestionScopesOverlap(root, tt.a, tt.b); got != tt.want {
				t.Fatalf("overlap = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestThemeSuggestionsRequireExplicitAlternative(t *testing.T) {
	t.Parallel()
	root := wiki.BlocksFromMarkdown("Shared text")
	before, _ := json.Marshal(root)
	row := generated.ProjectPageChange{
		ID:            uuid.New(),
		BeforeContent: before,
		Operations:    []byte(`[]`),
		Decisions:     []byte(`{}`),
		Status:        "proposed",
		BeforeTheme:   pgtype.Text{String: "default", Valid: true},
		AfterTheme:    pgtype.Text{String: "editorial", Valid: true},
	}
	peer := row
	peer.ID = uuid.New()
	peer.AfterTheme.String = "compact"
	state, err := pageSuggestionState(row, root, "default", []generated.ProjectPageChange{row, peer})
	if err != nil || len(state.suggestions) != 1 || len(state.suggestions[0].CompetingChangeIDs) != 1 {
		t.Fatalf("theme alternatives = %+v, %v", state, err)
	}
	if _, err = suggestionSelection(state, PageChangeReviewInput{Action: "apply"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("bulk theme acceptance = %v, want conflict", err)
	}
	if _, err = suggestionSelection(state, PageChangeReviewInput{Action: "apply", Indexes: []int{0}}); err != nil {
		t.Fatal(err)
	}
	reflected, err := pageSuggestionState(row, root, "editorial", nil)
	if err != nil || reflected.suggestions[0].Status != "already_reflected" {
		t.Fatalf("reflected theme = %+v, %v", reflected, err)
	}
	changed, err := pageSuggestionState(row, root, "compact", nil)
	if err != nil || changed.suggestions[0].Status != "needs_update" || changed.fingerprint == state.fingerprint {
		t.Fatalf("changed theme = %+v, %v", changed, err)
	}
}

func TestUndoThemePreservesDocumentContent(t *testing.T) {
	t.Parallel()
	root := wiki.BlocksFromMarkdown("Keep the shared document")
	root.Content[0].Attrs = map[string]any{"blockId": uuid.NewString()}
	state, err := wiki.EncodeBlocks(root)
	if err != nil {
		t.Fatal(err)
	}
	_, restored, err := undoPageOperations(state, root, nil)
	if err != nil || wiki.BlocksMarkdown(restored) != wiki.BlocksMarkdown(root) {
		t.Fatalf("theme undo document = %+v, %v, want %+v", restored, err, root)
	}
}

func TestSuggestionPlacementInvalidatesReview(t *testing.T) {
	t.Parallel()
	block := func(id string) wiki.BlockNode {
		return wiki.BlockNode{Type: "paragraph", Attrs: map[string]any{"blockId": id}}
	}
	base := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{block("a"), block("b"), block("c"), block("d")}}
	before, _ := json.Marshal(base)
	addition := block("new")
	for _, op := range []wiki.BlockOperation{
		{Type: "remove", BlockID: "b", ExpectedHash: wiki.BlockHash(base.Content[1])},
		{Type: "update", BlockID: "b", ExpectedHash: wiki.BlockHash(base.Content[1]), Content: &addition},
		{Type: "insert", AfterID: "b", Content: &addition},
		{Type: "insert", BeforeID: "b", Content: &addition},
	} {
		t.Run(op.Type+op.AfterID+op.BeforeID, func(t *testing.T) {
			t.Parallel()
			operations, _ := json.Marshal([]wiki.BlockOperation{op})
			row := generated.ProjectPageChange{ID: uuid.New(), BeforeContent: before,
				Operations: operations, Decisions: []byte(`{}`), Status: "proposed"}
			original, err := pageSuggestionState(row, base, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			unrelated := wiki.BlockNode{Type: "doc", Content: append(append([]wiki.BlockNode{}, base.Content...), block("e"))}
			stable, err := pageSuggestionState(row, unrelated, "", nil)
			if err != nil || stable.fingerprint != original.fingerprint {
				t.Fatalf("unrelated insertion changed review: %v", err)
			}
			moved := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{block("a"), block("c"), block("b"), block("d")}}
			changed, err := pageSuggestionState(row, moved, "", nil)
			if err != nil || changed.suggestions[0].Status != "needs_update" || changed.fingerprint == original.fingerprint {
				t.Fatalf("relocated suggestion = %+v, %v", changed, err)
			}
			if _, err := suggestionSelection(changed, PageChangeReviewInput{Action: "apply"}); !errors.Is(err, ErrConflict) {
				t.Fatalf("relocated application = %v, want conflict", err)
			}
		})
	}
}

func TestIndependentAdditionsAreNotCompetingSuggestions(t *testing.T) {
	t.Parallel()
	block := func(id string) wiki.BlockNode {
		return wiki.BlockNode{Type: "paragraph", Attrs: map[string]any{"blockId": id}}
	}
	anchor, first, second := block("anchor"), block("first"), block("second")
	root := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{anchor}}
	before, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		proposal wiki.BlockOperation
		peer     wiki.BlockOperation
		want     bool
	}{
		{"distinct additions at same position",
			wiki.BlockOperation{Type: "insert", AfterID: "anchor", Content: &first},
			wiki.BlockOperation{Type: "insert", AfterID: "anchor", Content: &second}, false},
		{"same inserted identity",
			wiki.BlockOperation{Type: "insert", AfterID: "anchor", Content: &first},
			wiki.BlockOperation{Type: "insert", AfterID: "anchor", Content: &first}, true},
		{"editing text beside an addition",
			wiki.BlockOperation{Type: "insert", AfterID: "anchor", Content: &first},
			wiki.BlockOperation{Type: "update", BlockID: "anchor", Content: &anchor}, false},
		{"removing insertion anchor",
			wiki.BlockOperation{Type: "insert", AfterID: "anchor", Content: &first},
			wiki.BlockOperation{Type: "remove", BlockID: "anchor"}, true},
		{"replacing insertion parent",
			wiki.BlockOperation{Type: "insert", ParentID: "anchor", Content: &first},
			wiki.BlockOperation{Type: "update", BlockID: "anchor", Content: &anchor}, true},
		{"competing replacements",
			wiki.BlockOperation{Type: "update", BlockID: "anchor", Content: &first},
			wiki.BlockOperation{Type: "update", BlockID: "anchor", Content: &second}, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rows := make([]generated.ProjectPageChange, 2)
			for i, op := range []wiki.BlockOperation{tt.proposal, tt.peer} {
				operations, err := json.Marshal([]wiki.BlockOperation{op})
				if err != nil {
					t.Fatal(err)
				}
				rows[i] = generated.ProjectPageChange{
					ID: uuid.New(), BeforeContent: before, Operations: operations,
					Decisions: []byte(`{}`), Status: "proposed",
				}
			}
			for _, row := range rows {
				state, err := pageSuggestionState(row, root, "default", rows)
				if err != nil {
					t.Fatal(err)
				}
				if got := len(state.suggestions[0].CompetingChangeIDs) > 0; got != tt.want {
					t.Fatalf("competing = %v, want %v", got, tt.want)
				}
			}
		})
	}
}
