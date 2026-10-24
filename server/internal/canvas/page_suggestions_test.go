package canvas

import (
	"encoding/json"
	"errors"
	"testing"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
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

func TestPassageReviewFingerprintsCombinedPreview(t *testing.T) {
	t.Parallel()
	base := wiki.BlocksFromMarkdown("Old opening. Unchanged middle. Old ending.")
	wiki.AssignBlockIDs(&base, uuid.NewString)
	proposed, live := base.Content[0], base.Content[0]
	blockID, ok := proposed.Attrs["blockId"].(string)
	if !ok {
		t.Fatal("paragraph block ID is not a string")
	}
	proposed.Content = []wiki.BlockNode{{Type: "text", Text: "New opening. Unchanged middle. Old ending."}}
	live.Content = []wiki.BlockNode{{Type: "text", Text: "Old opening. Unchanged middle. Human ending."}}
	op := wiki.BlockOperation{
		Type:         "update",
		BlockID:      blockID,
		ExpectedHash: wiki.BlockHash(base.Content[0]),
		Content:      &proposed,
	}
	before, _ := json.Marshal(base)
	operations, _ := json.Marshal([]wiki.BlockOperation{op})
	row := generated.ProjectPageChange{
		ID:            uuid.New(),
		BeforeContent: before,
		Operations:    operations,
		Decisions:     []byte(`{}`),
		Status:        "proposed",
	}
	baseState, err := wiki.EncodeBlocks(base)
	if err != nil {
		t.Fatal(err)
	}
	updates, err := wiki.BlockOperationUpdates(baseState, []wiki.BlockOperation{op})
	if err != nil {
		t.Fatal(err)
	}
	row.CrdtUpdates, err = json.Marshal(updates)
	if err != nil {
		t.Fatal(err)
	}
	original, err := pageSuggestionState(row, pageSuggestionSnapshot{root: base, state: baseState}, nil)
	if err != nil {
		t.Fatal(err)
	}
	liveOp := op
	liveOp.Content = &live
	liveState, current, err := wiki.ApplyBlockOperations(baseState, []wiki.BlockOperation{liveOp})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := pageSuggestionState(row, pageSuggestionSnapshot{root: current, state: liveState}, nil)
	if err != nil {
		t.Fatal(err)
	}
	suggestion := updated.suggestions[0]
	if suggestion.Status != "pending" ||
		wiki.BlocksMarkdown(*suggestion.After) != "New opening. Unchanged middle. Human ending.\n\n" ||
		original.fingerprint == updated.fingerprint {
		t.Fatalf("review = %+v", suggestion)
	}
	_, approved, err := applyPageSuggestions(liveState, updated, []int{0})
	if err != nil || wiki.BlockHash(approved.Content[0]) != wiki.BlockHash(*suggestion.After) {
		t.Fatalf("approval = %+v, %v; want exact preview", approved, err)
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
			false,
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

func TestThemeSuggestionsApproveIndependently(t *testing.T) {
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
	state, err := pageSuggestionState(
		row,
		pageSuggestionSnapshot{root: root, theme: "default"},
		[]generated.ProjectPageChange{row, peer},
	)
	if err != nil || len(state.suggestions) != 1 || len(state.suggestions[0].CompetingChangeIDs) != 1 {
		t.Fatalf("theme alternatives = %+v, %v", state, err)
	}
	if _, err = suggestionSelection(state, PageChangeReviewInput{Action: "apply"}); err != nil {
		t.Fatalf("theme approval = %v", err)
	}
	if _, err = suggestionSelection(state, PageChangeReviewInput{Action: "apply", Indexes: []int{0}}); err != nil {
		t.Fatal(err)
	}
	reflected, err := pageSuggestionState(row, pageSuggestionSnapshot{root: root, theme: "editorial"}, nil)
	if err != nil || reflected.suggestions[0].Status != "already_reflected" {
		t.Fatalf("reflected theme = %+v, %v", reflected, err)
	}
	changed, err := pageSuggestionState(row, pageSuggestionSnapshot{root: root, theme: "compact"}, nil)
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
			row := generated.ProjectPageChange{
				ID: uuid.New(), BeforeContent: before,
				Operations: operations, Decisions: []byte(`{}`), Status: "proposed",
			}
			original, err := pageSuggestionState(row, pageSuggestionSnapshot{root: base, theme: ""}, nil)
			if err != nil {
				t.Fatal(err)
			}
			unrelated := wiki.BlockNode{
				Type:    "doc",
				Content: append(append([]wiki.BlockNode{}, base.Content...), block("e")),
			}
			stable, err := pageSuggestionState(row, pageSuggestionSnapshot{root: unrelated, theme: ""}, nil)
			if err != nil || stable.fingerprint != original.fingerprint {
				t.Fatalf("unrelated insertion changed review: %v", err)
			}
			for _, children := range [][]wiki.BlockNode{
				{block("a"), block("unrelated"), block("b"), block("c"), block("d")},
				{block("a"), block("b"), block("unrelated"), block("c"), block("d")},
				{block("b"), block("c"), block("d")},
				{block("a"), block("b"), block("d")},
			} {
				stable, err := pageSuggestionState(
					row,
					pageSuggestionSnapshot{
						root:  wiki.BlockNode{Type: "doc", Content: children},
						theme: "",
					},
					nil,
				)
				if err != nil || stable.fingerprint != original.fingerprint {
					t.Fatalf(
						"independent sibling change invalidated review: %+v, %v",
						stable.suggestions,
						err,
					)
				}
				if _, err := suggestionSelection(
					stable,
					PageChangeReviewInput{Action: "apply"},
				); err != nil {
					t.Fatalf("independent sibling change blocked acceptance: %v", err)
				}
			}
			moved := wiki.BlockNode{
				Type:    "doc",
				Content: []wiki.BlockNode{block("a"), block("c"), block("b"), block("d")},
			}
			changed, err := pageSuggestionState(row, pageSuggestionSnapshot{root: moved, theme: ""}, nil)
			if err != nil || changed.suggestions[0].Status != "needs_update" ||
				changed.fingerprint == original.fingerprint {
				t.Fatalf("relocated suggestion = %+v, %v", changed, err)
			}
			if _, err := suggestionSelection(
				changed,
				PageChangeReviewInput{Action: "apply"},
			); !errors.Is(
				err,
				ErrConflict,
			) {
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
		{
			"distinct additions at same position",
			wiki.BlockOperation{Type: "insert", AfterID: "anchor", Content: &first},
			wiki.BlockOperation{Type: "insert", AfterID: "anchor", Content: &second},
			false,
		},
		{
			"same inserted identity",
			wiki.BlockOperation{Type: "insert", AfterID: "anchor", Content: &first},
			wiki.BlockOperation{Type: "insert", AfterID: "anchor", Content: &first},
			true,
		},
		{
			"editing text beside an addition",
			wiki.BlockOperation{Type: "insert", AfterID: "anchor", Content: &first},
			wiki.BlockOperation{Type: "update", BlockID: "anchor", Content: &anchor},
			false,
		},
		{
			"removing insertion anchor",
			wiki.BlockOperation{Type: "insert", AfterID: "anchor", Content: &first},
			wiki.BlockOperation{Type: "remove", BlockID: "anchor"},
			true,
		},
		{
			"replacing insertion parent",
			wiki.BlockOperation{Type: "insert", ParentID: "anchor", Content: &first},
			wiki.BlockOperation{Type: "update", BlockID: "anchor", Content: &anchor},
			true,
		},
		{
			"competing replacements",
			wiki.BlockOperation{Type: "update", BlockID: "anchor", Content: &first},
			wiki.BlockOperation{Type: "update", BlockID: "anchor", Content: &second},
			true,
		},
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
				state, err := pageSuggestionState(
					row,
					pageSuggestionSnapshot{root: root, theme: "default"},
					rows,
				)
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

func TestPageCardSuggestionAfterIndependentAcceptance(t *testing.T) {
	t.Parallel()
	anchorID := uuid.NewString()
	base := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{
		{Type: "paragraph", Attrs: map[string]any{"blockId": anchorID}},
	}}
	card := func() wiki.BlockNode {
		return wiki.BlockNode{Type: "artifactReference", Attrs: map[string]any{
			"blockId": uuid.NewString(), "versionId": uuid.NewString(), "format": "live",
		}}
	}
	first, second := card(), card()
	op := wiki.BlockOperation{Type: "insert", AfterID: anchorID, Content: &second}
	before, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := json.Marshal([]wiki.BlockOperation{op})
	if err != nil {
		t.Fatal(err)
	}
	row := generated.ProjectPageChange{
		ID: uuid.New(), Status: "proposed", BeforeContent: before,
		Operations: operations, Decisions: []byte(`{}`),
	}
	state, err := wiki.EncodeBlocks(base)
	if err != nil {
		t.Fatal(err)
	}
	saved, current, err := wiki.ApplyBlockOperations(state, []wiki.BlockOperation{
		{Type: "insert", AfterID: anchorID, Content: &first},
	})
	if err != nil {
		t.Fatal(err)
	}
	review, err := pageSuggestionState(row, pageSuggestionSnapshot{root: current, theme: ""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := review.suggestions[0].Status; got != "pending" {
		t.Fatalf("status = %s, want pending", got)
	}
	if _, err := suggestionSelection(
		review,
		PageChangeReviewInput{Action: "apply", Indexes: []int{0}},
	); err != nil {
		t.Fatalf("accept independent card: %v", err)
	}
	_, applied, err := wiki.ApplyBlockOperations(saved, []wiki.BlockOperation{op})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Content) != 3 || wiki.FindBlock(applied, first.Attrs["blockId"].(string)) == nil ||
		wiki.FindBlock(applied, second.Attrs["blockId"].(string)) == nil {
		t.Fatalf("accepted page did not preserve both cards: %+v", applied)
	}
}

func TestResolvedSuggestionIsTerminalAndStaleContentChangesFingerprint(t *testing.T) {
	t.Parallel()
	block := wiki.BlockNode{
		Type:    "paragraph",
		Attrs:   map[string]any{"blockId": "a"},
		Content: []wiki.BlockNode{{Type: "text", Text: "original"}},
	}
	base := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{block}}
	proposed := block
	proposed.Content = []wiki.BlockNode{{Type: "text", Text: "proposal"}}
	before, _ := json.Marshal(base)
	operations, _ := json.Marshal(
		[]wiki.BlockOperation{
			{Type: "update", BlockID: "a", ExpectedHash: wiki.BlockHash(block), Content: &proposed},
		},
	)
	row := generated.ProjectPageChange{
		ID:            uuid.New(),
		BeforeContent: before,
		Operations:    operations,
		Decisions:     []byte(`{}`),
		Status:        "proposed",
	}
	first := block
	first.Content = []wiki.BlockNode{{Type: "text", Text: "first human edit"}}
	second := block
	second.Content = []wiki.BlockNode{{Type: "text", Text: "second human edit"}}
	a, err := pageSuggestionState(
		row,
		pageSuggestionSnapshot{root: wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{first}}, theme: ""},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	b, err := pageSuggestionState(
		row,
		pageSuggestionSnapshot{root: wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{second}}, theme: ""},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if a.fingerprint == b.fingerprint {
		t.Fatal("different current text reused a review fingerprint")
	}
	for _, decision := range []string{"already_reflected", "resolved"} {
		row.Decisions, _ = json.Marshal(map[int]string{0: decision})
		state, err := pageSuggestionState(row, pageSuggestionSnapshot{root: base, theme: ""}, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := decision
		if want == "already_reflected" {
			want = "resolved"
		}
		if state.suggestions[0].Status != want {
			t.Fatalf("decision %s status = %s, want %s", decision, state.suggestions[0].Status, want)
		}
		if _, err := suggestionSelection(
			state,
			PageChangeReviewInput{Action: "apply", Indexes: []int{0}},
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("completed review accepted again: %v", err)
		}
	}
}

func TestHistoricalCompletedProposalDoesNotReopen(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"applied", "rejected", "undone", "resolved", "superseded"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			block := wiki.BlockNode{Type: "paragraph", Attrs: map[string]any{"blockId": "a"}}
			root := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{block}}
			before, _ := json.Marshal(root)
			operations, _ := json.Marshal(
				[]wiki.BlockOperation{
					{
						Type:         "update",
						BlockID:      "a",
						ExpectedHash: wiki.BlockHash(block),
						Content:      &block,
					},
				},
			)
			state, err := pageSuggestionState(
				generated.ProjectPageChange{
					ID:            uuid.New(),
					BeforeContent: before,
					Operations:    operations,
					Decisions:     []byte(`{}`),
					Status:        status,
				},
				pageSuggestionSnapshot{root: root, theme: ""},
				nil,
			)
			if err != nil || state.suggestions[0].Status != status {
				t.Fatalf("completed proposal = %+v, %v, want %s", state.suggestions, err, status)
			}
		})
	}
}

func TestSuggestionDecisionStatus(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name           string
		decisions      map[int]string
		fallback, want string
	}{
		{"resolved and rejected", map[int]string{0: "resolved", 1: "rejected"}, "rejected", "resolved"},
		{"reflected and rejected", map[int]string{0: "already_reflected", 1: "rejected"}, "rejected", "resolved"},
		{"applied and pending", map[int]string{0: "applied"}, "resolved", "partially_applied"},
		{"resolved and pending", map[int]string{0: "resolved"}, "resolved", "proposed"},
		{"all applied", map[int]string{0: "applied", 1: "applied"}, "applied", "applied"},
		{"all rejected", map[int]string{0: "rejected", 1: "rejected"}, "rejected", "rejected"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := suggestionDecisionStatus(2, tt.decisions, tt.fallback); got != tt.want {
				t.Fatalf("status = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSuggestionConflictsAndGroupsStayAtTheChangedBlock(t *testing.T) {
	t.Parallel()
	paragraph := func(id string) wiki.BlockNode {
		return wiki.BlockNode{
			Type:    "paragraph",
			Attrs:   map[string]any{"blockId": id},
			Content: []wiki.BlockNode{{Type: "text", Text: id}},
		}
	}
	a, b, c := paragraph("a"), paragraph("b"), paragraph("c")
	root := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{a, b, c}}
	before, _ := json.Marshal(root)
	changed := a
	changed.Content = []wiki.BlockNode{{Type: "text", Text: "other version"}}
	addition, otherAddition := paragraph("new"), paragraph("new-other")
	for _, tt := range []struct {
		name string
		ops  []wiki.BlockOperation
	}{
		{"adjacent updates with placement metadata", []wiki.BlockOperation{
			{Type: "update", BlockID: "a", ExpectedHash: wiki.BlockHash(a), Content: &a},
			{Type: "update", BlockID: "b", AfterID: "a", ExpectedHash: wiki.BlockHash(b), Content: &b},
			{Type: "update", BlockID: "c", AfterID: "b", ExpectedHash: wiki.BlockHash(c), Content: &c},
		}},
		{"update beside independent additions", []wiki.BlockOperation{
			{Type: "update", BlockID: "a", ExpectedHash: wiki.BlockHash(a), Content: &a},
			{Type: "insert", AfterID: "a", Content: &addition},
			{Type: "insert", AfterID: "a", Content: &otherAddition},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			operations, _ := json.Marshal(tt.ops)
			peerOps, _ := json.Marshal(
				[]wiki.BlockOperation{
					{
						Type:         "update",
						BlockID:      "a",
						ExpectedHash: wiki.BlockHash(a),
						Content:      &changed,
					},
				},
			)
			rows := []generated.ProjectPageChange{
				{
					ID:            uuid.New(),
					BeforeContent: before,
					Operations:    operations,
					Decisions:     []byte(`{}`),
					Status:        "proposed",
				},
				{
					ID:            uuid.New(),
					BeforeContent: before,
					Operations:    peerOps,
					Decisions:     []byte(`{}`),
					Status:        "proposed",
				},
			}
			state, err := pageSuggestionState(rows[0], pageSuggestionSnapshot{root: root, theme: ""}, rows)
			if err != nil {
				t.Fatal(err)
			}
			for i, item := range state.suggestions {
				if len(item.Group) != 1 || item.Group[0] != i {
					t.Fatalf("block %d group = %v, want only itself", i, item.Group)
				}
				if got := len(item.CompetingChangeIDs) > 0; got != (i == 0) {
					t.Fatalf("block %d conflicting = %v, want %v", i, got, i == 0)
				}
			}
			for _, action := range []string{"apply", "reject"} {
				indexes, err := suggestionSelection(
					state,
					PageChangeReviewInput{Action: action, Indexes: []int{1}},
				)
				if err != nil || len(indexes) != 1 || indexes[0] != 1 {
					t.Fatalf("%s independent block = %v, %v", action, indexes, err)
				}
			}
		})
	}
}

func TestIndependentInsertAcceptancePreservesProposalOrder(t *testing.T) {
	t.Parallel()
	for _, slot := range []string{"before", "after", "end"} {
		t.Run(slot, func(t *testing.T) {
			t.Parallel()
			anchorID := uuid.NewString()
			base := wiki.BlockNode{
				Type: "doc",
				Content: []wiki.BlockNode{
					{Type: "paragraph", Attrs: map[string]any{"blockId": anchorID}},
				},
			}
			var ops []wiki.BlockOperation
			for _, id := range []string{"one", "two", "three"} {
				block := wiki.BlockNode{
					Type:    "paragraph",
					Attrs:   map[string]any{"blockId": uuid.NewString()},
					Content: []wiki.BlockNode{{Type: "text", Text: id}},
				}
				op := wiki.BlockOperation{Type: "insert", Content: &block}
				if slot == "before" {
					op.BeforeID = anchorID
				}
				if slot == "after" {
					op.AfterID = anchorID
				}
				ops = append(ops, op)
			}
			initial, err := wiki.EncodeBlocks(base)
			if err != nil {
				t.Fatal(err)
			}
			_, want, err := wiki.ApplyBlockOperations(initial, ops)
			if err != nil {
				t.Fatal(err)
			}
			for _, batches := range [][][]int{
				{{0}, {1}, {2}},
				{{0}, {2}, {1}},
				{{1}, {0}, {2}},
				{{1}, {2}, {0}},
				{{2}, {0}, {1}},
				{{2}, {1}, {0}},
				{{0}, {1, 2}},
				{{1}, {0, 2}},
				{{2}, {0, 1}},
				{{0, 1}, {2}},
				{{0, 2}, {1}},
				{{1, 2}, {0}},
				{{0, 1, 2}},
			} {
				saved, current := initial, base
				review := suggestionReviewState{operations: ops, decisions: map[int]string{}}
				for _, batch := range batches {
					var selected []wiki.BlockOperation
					for _, index := range batch {
						selected = append(
							selected,
							insertionBesideAcceptedSibling(review, current, index, batch),
						)
					}
					saved, current, err = wiki.ApplyBlockOperations(saved, selected)
					if err != nil {
						t.Fatalf("batches %v: %v", batches, err)
					}
					for _, index := range batch {
						review.decisions[index] = "applied"
					}
				}
				if wiki.BlockHash(current) != wiki.BlockHash(want) {
					t.Fatalf(
						"batches %v changed proposed order: %+v, want %+v",
						batches,
						current,
						want,
					)
				}
			}
		})
	}
}

func TestUpdateIgnoresUnusedPlacementMetadata(t *testing.T) {
	t.Parallel()
	a := wiki.BlockNode{Type: "paragraph", Attrs: map[string]any{"blockId": "a"}}
	b := wiki.BlockNode{Type: "paragraph", Attrs: map[string]any{"blockId": "b"}}
	base := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{a, b}}
	current := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{b}}
	op := wiki.BlockOperation{
		Type:         "update",
		BlockID:      "b",
		AfterID:      "a",
		ExpectedHash: wiki.BlockHash(b),
		Content:      &b,
	}
	if got := suggestionApplicability(base, current, op); got != "pending" {
		t.Fatalf("status = %s, want pending after unrelated anchor deletion", got)
	}
}

func TestConflictsAreSymmetricAcrossProposalSnapshots(t *testing.T) {
	t.Parallel()
	child := wiki.BlockNode{Type: "paragraph", Attrs: map[string]any{"blockId": "child"}}
	other := wiki.BlockNode{Type: "paragraph", Attrs: map[string]any{"blockId": "other"}}
	parent := wiki.BlockNode{
		Type:    "blockquote",
		Attrs:   map[string]any{"blockId": "parent"},
		Content: []wiki.BlockNode{child, other},
	}
	before := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{parent}}
	movedParent := parent
	movedParent.Content = []wiki.BlockNode{other}
	current := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{movedParent, child}}
	oldBase, _ := json.Marshal(before)
	newBase, _ := json.Marshal(current)
	removal, _ := json.Marshal(
		[]wiki.BlockOperation{{Type: "remove", BlockID: "parent", ExpectedHash: wiki.BlockHash(parent)}},
	)
	updated := child
	updated.Content = []wiki.BlockNode{{Type: "text", Text: "new version"}}
	update, _ := json.Marshal(
		[]wiki.BlockOperation{
			{Type: "update", BlockID: "child", ExpectedHash: wiki.BlockHash(child), Content: &updated},
		},
	)
	rows := []generated.ProjectPageChange{
		{
			ID:            uuid.New(),
			BeforeContent: oldBase,
			Operations:    removal,
			Decisions:     []byte(`{}`),
			Status:        "proposed",
		},
		{
			ID:            uuid.New(),
			BeforeContent: newBase,
			Operations:    update,
			Decisions:     []byte(`{}`),
			Status:        "proposed",
		},
	}
	for i, row := range rows {
		state, err := pageSuggestionState(row, pageSuggestionSnapshot{root: current, theme: ""}, rows)
		if err != nil {
			t.Fatal(err)
		}
		got := state.suggestions[0].CompetingChangeIDs
		if len(got) != 1 || got[0] != rows[1-i].ID {
			t.Fatalf("proposal %d conflicts = %v, want peer %s", i, got, rows[1-i].ID)
		}
	}
}

func TestThreeWayBlockComparison(t *testing.T) {
	t.Parallel()
	block := func(id, text string) wiki.BlockNode {
		return wiki.BlockNode{
			Type:    "paragraph",
			Attrs:   map[string]any{"blockId": id},
			Content: []wiki.BlockNode{{Type: "text", Text: text}},
		}
	}
	a, b := block("a", "base"), block("b", "other")
	proposed, divergent, addition := block("a", "proposed"), block("a", "human"), block("new", "new stanza")
	doc := func(blocks ...wiki.BlockNode) wiki.BlockNode { return wiki.BlockNode{Type: "doc", Content: blocks} }
	base := doc(a, b)
	update := wiki.BlockOperation{Type: "update", BlockID: "a", ExpectedHash: wiki.BlockHash(a), Content: &proposed}
	remove := wiki.BlockOperation{Type: "remove", BlockID: "a", ExpectedHash: wiki.BlockHash(a)}
	move := wiki.BlockOperation{Type: "move", BlockID: "a", ExpectedHash: wiki.BlockHash(a), AfterID: "b"}
	insert := wiki.BlockOperation{Type: "insert", Content: &addition, AfterID: "a"}
	for _, tt := range []struct {
		name    string
		current wiki.BlockNode
		op      wiki.BlockOperation
		want    string
	}{
		{"unchanged target", base, update, "pending"},
		{"independent edit", doc(a, block("b", "human other")), update, "pending"},
		{"same result", doc(proposed, b), update, "already_reflected"},
		{"different result", doc(divergent, b), update, "needs_update"},
		{"deleted target update", doc(b), update, "needs_update"},
		{"deletion already present", doc(b), remove, "already_reflected"},
		{"deletion against edited text", doc(divergent, b), remove, "needs_update"},
		{"move unchanged source", base, move, "pending"},
		{"move already present", doc(b, a), move, "already_reflected"},
		{"move against edited text", doc(b, divergent), move, "needs_update"},
		{"insert beside edited anchor", doc(divergent, b), insert, "pending"},
		{"insert missing anchor", doc(b), insert, "needs_update"},
		{"insert already present", doc(a, addition, b), insert, "already_reflected"},
		{"insert identity differs", doc(a, block("new", "different"), b), insert, "needs_update"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := suggestionApplicability(base, tt.current, tt.op); got != tt.want {
				t.Fatalf("status = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestCRDTReviewOverlappingTextAndStructuralFence(t *testing.T) {
	t.Parallel()
	base := wiki.BlocksFromMarkdown("Price: $10")
	wiki.AssignBlockIDs(&base, uuid.NewString)
	baseState, err := wiki.EncodeBlocks(base)
	if err != nil {
		t.Fatal(err)
	}
	original := base.Content[0]
	id, ok := original.Attrs["blockId"].(string)
	if !ok {
		t.Fatal("missing block ID")
	}
	op := wiki.BlockOperation{Type: "update", BlockID: id, ExpectedHash: wiki.BlockHash(original)}
	proposed, human := original, original
	proposed.Content = []wiki.BlockNode{{Type: "text", Text: "Price: $20"}}
	human.Content = []wiki.BlockNode{{Type: "text", Text: "Price: $30"}}
	op.Content = &proposed
	updates, err := wiki.BlockOperationUpdates(baseState, []wiki.BlockOperation{op})
	if err != nil {
		t.Fatal(err)
	}
	row := generated.ProjectPageChange{
		ID:        uuid.New(),
		Status:    "proposed",
		Decisions: []byte(`{}`),
		Result:    []byte(`{"sequence":1}`),
	}
	row.BeforeContent, _ = json.Marshal(base)
	row.Operations, _ = json.Marshal([]wiki.BlockOperation{op})
	row.CrdtUpdates, _ = json.Marshal(updates)
	humanOp := op
	humanOp.Content = &human
	liveState, current, err := wiki.ApplyBlockOperations(baseState, []wiki.BlockOperation{humanOp})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := pageSuggestionSnapshot{root: current, state: liveState, structureSequence: 1}
	review, err := pageSuggestionState(row, snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if review.suggestions[0].Status != "pending" {
		t.Fatalf("overlap blocked: %+v", review.suggestions)
	}
	repeat, err := pageSuggestionState(row, snapshot, nil)
	if err != nil || repeat.fingerprint != review.fingerprint {
		t.Fatalf("preview was regenerated: %+v, %v", repeat, err)
	}
	_, approved, err := applyPageSuggestions(liveState, review, []int{0})
	if err != nil || wiki.BlockHash(approved.Content[0]) != wiki.BlockHash(*review.suggestions[0].After) {
		t.Fatalf("approval differs from merged preview: %+v, %v", approved, err)
	}
	if wiki.BlockHash(approved.Content[0]) == wiki.BlockHash(human) ||
		wiki.BlockHash(approved.Content[0]) == wiki.BlockHash(proposed) {
		t.Fatal("overlap discarded one writer")
	}
	snapshot.structureSequence = 2
	blocked, err := pageSuggestionState(row, snapshot, nil)
	if err != nil || blocked.suggestions[0].Status != "pending" {
		t.Fatalf("unrelated structure blocked text: %+v, %v", blocked, err)
	}
}
