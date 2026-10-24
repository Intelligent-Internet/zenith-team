package wiki

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCaptureUIRetainsInlineState(t *testing.T) {
	t.Parallel()
	source := `{@body const [count, setCount] = DIL.useState(2);}<button onClick={() => setCount(count + 1)}>{count}</button>`
	root, err := CompileSource(source, 1)
	if err != nil {
		t.Fatal(err)
	}
	original := BlocksSource(root)
	baseline, err := CaptureUI(root, UICaptureInput{})
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for key := range baseline.Values {
		path = strings.TrimSuffix(key, "/inlineState")
	}
	state := inlineCaptureState{
		RendererVersion: 1, Identity: "viewer", Source: source, Theme: "dark", Width: 800,
		State: map[string]any{"count": float64(7)}, Refs: map[string]any{}, Memo: map[string]any{},
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	captured, err := CaptureUI(
		root,
		UICaptureInput{Selected: map[string]map[string]any{path: {"inlineState": string(encoded)}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	retained, ok := captured.Content.Content[0].Attrs["inlineCapture"].(inlineCaptureState)
	if !ok || retained.State["count"] != float64(7) || retained.Theme != "dark" || retained.Width != 800 {
		t.Fatalf("retained state = %+v, want selected viewer state", retained)
	}
	if !strings.Contains(UICaptureMarkdown(captured), `"count": 7`) || BlocksSource(root) != original {
		t.Fatal("capture lost selected state or changed authored source")
	}
	for _, mutation := range []struct {
		name   string
		change func(*inlineCaptureState)
	}{
		{"source mismatch", func(s *inlineCaptureState) { s.Source = "other" }},
		{"unknown renderer", func(s *inlineCaptureState) { s.RendererVersion = 2 }},
		{"invalid refs", func(s *inlineCaptureState) { s.Refs = map[string]any{"ref": true} }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			invalid := state
			mutation.change(&invalid)
			raw, err := json.Marshal(invalid)
			if err != nil {
				t.Fatal(err)
			}
			_, err = CaptureUI(
				root,
				UICaptureInput{Selected: map[string]map[string]any{path: {"inlineState": string(raw)}}},
			)
			if !errors.Is(err, ErrPageInvalidInput) {
				t.Fatalf("capture error = %v, want invalid state", err)
			}
		})
	}
}

func TestCaptureUIRetainsSelectedScenario(t *testing.T) {
	t.Parallel()
	source := `<Scope name="pricing" initial={"price":10,"quantity":2} computed={{"total":state.price * state.quantity}}>
<NumberInput label="Quantity" bind="quantity" />
<Metric label="Total" value={state.total} />
<Button label="Add one" action={{"type":"setState","name":"quantity","value":state.quantity + 1}} />
</Scope>`
	root, err := compileCatalogFixture(source, 1)
	if err != nil {
		t.Fatal(err)
	}
	original := BlocksSource(root)
	baseline, err := CaptureUI(root, UICaptureInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.Scopes) != 1 {
		t.Fatalf("scopes = %d, want 1", len(baseline.Scopes))
	}
	captured, err := CaptureUI(
		root,
		UICaptureInput{Selected: map[string]map[string]any{baseline.Scopes[0].Path: {"quantity": float64(3)}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Scopes[0].Computed["total"] != float64(30) {
		t.Fatalf("computed = %+v, want total 30", captured.Scopes[0].Computed)
	}
	if BlocksSource(root) != original || BlocksSource(captured.Definition) != original {
		t.Fatal("capture changed the authored definition")
	}
	scope := captured.Content.Content[0]
	if scope.Type != "uiStack" || scope.Content[0].Type != "paragraph" || scope.Content[2].Type != "paragraph" {
		t.Fatalf("frozen controls = %+v, want inert paragraphs", scope)
	}
	value, err := EvaluateUIExpression(scope.Content[1].Attrs["value"], UIEnvironment{})
	if err != nil || value != float64(30) {
		t.Fatalf("frozen metric = %v, %v; want 30", value, err)
	}
	if !strings.Contains(UICaptureMarkdown(captured), "30") {
		t.Fatal("export omitted the evaluated result")
	}
	encoded, err := json.Marshal(captured)
	if err != nil {
		t.Fatal(err)
	}
	var replay UICapture
	if err := json.Unmarshal(encoded, &replay); err != nil {
		t.Fatal(err)
	}
	var quantity any
	if err := json.Unmarshal(
		replay.Scopes[0].Selected["quantity"],
		&quantity,
	); err != nil ||
		quantity != float64(3) {
		t.Fatalf("selected quantity = %v, %v; want JSON number 3", quantity, err)
	}
	if UICaptureMarkdown(replay) != UICaptureMarkdown(captured) {
		t.Fatal("capture round trip changed exported evidence")
	}
	if captured.DefinitionHash != baseline.DefinitionHash {
		t.Fatal("viewer selections changed the authored definition hash")
	}
}

func TestCaptureUIChartMarkdownIncludesAccessibleSummary(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		variant string
		rows    []any
		want    string
	}{
		{
			name:    "line chart extrema",
			variant: "line",
			rows: []any{
				map[string]any{"quarter": "Q1", "amount": float64(12)},
				map[string]any{"quarter": "Q2", "amount": float64(19)},
			},
			want: "2 points. Lowest amount: 12 at Q1; highest amount: 19 at Q2.",
		},
		{
			name:    "scatter axis ranges",
			variant: "scatter",
			rows: []any{
				map[string]any{"quarter": float64(1), "amount": float64(12)},
				map[string]any{"quarter": float64(2), "amount": float64(19)},
			},
			want: "2 points. quarter ranges from 1 to 2; amount ranges from 12 to 19.",
		},
		{
			name:    "line chart escapes untrusted category text",
			variant: "line",
			rows: []any{
				map[string]any{
					"quarter": "Q1\n\n[click](https://example.test)|",
					"amount":  float64(12),
				},
				map[string]any{"quarter": "Q2", "amount": float64(19)},
			},
			want: `Lowest amount: 12 at Q1 \[click\]\(https\:\/\/example\.test\)\|; highest amount: 19 at Q2.`,
		},
	}
	for _, item := range testCases {
		t.Run(item.name, func(t *testing.T) {
			t.Parallel()
			root := BlockNode{
				Type: "doc",
				Content: []BlockNode{{
					Type: "uiChart",
					Attrs: map[string]any{
						"label":   "Quarterly amount",
						"variant": item.variant,
						"x":       "quarter",
						"y":       "amount",
						"data":    uiLiteral(item.rows),
					},
				}},
			}
			capture, err := CaptureUI(root, UICaptureInput{})
			if err != nil {
				t.Fatal(err)
			}
			if got := UICaptureMarkdown(capture); !strings.Contains(got, item.want) {
				t.Fatalf("markdown = %q, want summary %q", got, item.want)
			}
		})
	}
}

func TestCaptureUIMarkdownEscapesSelectedText(t *testing.T) {
	t.Parallel()
	root, err := compileCatalogFixture(`<Scope name="review" initial={"note":"safe"}>
<TextInput label="Note" bind="note" />
</Scope>`, 1)
	if err != nil {
		t.Fatal(err)
	}
	root.Content[0].Content[0].Attrs["blockId"] = "input-1"
	baseline, err := CaptureUI(root, UICaptureInput{})
	if err != nil {
		t.Fatal(err)
	}
	path := baseline.Scopes[0].Path
	selected := "Review\n\n[click](https://example.test)|"
	capture, err := CaptureUI(root, UICaptureInput{
		Selected: map[string]map[string]any{path: {"note": selected}},
	})
	if err != nil {
		t.Fatal(err)
	}
	markdown := UICaptureMarkdown(capture)
	frozenText := capture.Content.Content[0].Content[0].Content[0].Text
	if !strings.Contains(frozenText, selected) {
		t.Fatalf("saved capture text = %q, want original selected text %q", frozenText, selected)
	}
	if strings.Contains(markdown, "[click](") || strings.Contains(markdown, "\n\n[click]") {
		t.Fatalf("selected value became active Markdown: %q", markdown)
	}
	if !strings.Contains(markdown, `Review \[click\]\(https\:\/\/example\.test\)\|`) {
		t.Fatalf("escaped selected value missing from markdown: %q", markdown)
	}
}

func TestCaptureUIRejectsUnknownSelections(t *testing.T) {
	t.Parallel()
	root, err := compileCatalogFixture("Text", 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = CaptureUI(root, UICaptureInput{Selected: map[string]map[string]any{"missing": {"value": float64(1)}}})
	if !errors.Is(err, ErrPageInvalidInput) {
		t.Fatalf("error = %v, want invalid selection", err)
	}
}

func TestCaptureUISourceEvidenceIsIndependent(t *testing.T) {
	t.Parallel()
	root, err := compileCatalogFixture(
		`<Scope name="work" initial={} sources={"tasks":{"type":"tasks","projectId":"11111111-1111-4111-8111-111111111111"}}>
<Metric label="Count" value={count(data.tasks)} />
</Scope>`,
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	rows := []any{map[string]any{"id": "one", "title": "Original"}}
	coverage := &UISourceCoverage{RowCount: 1, Limit: 1, Complete: false}
	captured, err := CaptureUI(
		root,
		UICaptureInput{Resolve: func(_, _ string, descriptor map[string]any) (UISourceSnapshot, error) {
			descriptor["filter"] = "done"
			return UISourceSnapshot{
				Value:      rows,
				Coverage:   coverage,
				Version:    "revision-1",
				ObservedAt: "2026-10-19T00:00:00Z",
			}, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	rows[0].(map[string]any)["title"] = "Changed"
	coverage.Complete = true
	evidence := captured.Sources[0]
	if evidence.Snapshot.Coverage.Complete {
		t.Fatal("capture retained caller-owned coverage")
	}
	if evidence.Snapshot.Value.([]any)[0].(map[string]any)["title"] != "Original" {
		t.Fatal("capture retained caller-owned rows")
	}
	if _, mutated := evidence.Descriptor["filter"]; mutated {
		t.Fatal("resolver mutated the retained source descriptor")
	}
}

func TestCaptureUIPropagatesSourceFailure(t *testing.T) {
	t.Parallel()
	root, err := compileCatalogFixture(
		`<Scope name="work" initial={} sources={"tasks":{"type":"tasks","projectId":"11111111-1111-4111-8111-111111111111"}}>
<Metric label="Count" value={count(data.tasks)} />
</Scope>`,
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("source denied")
	capture, err := CaptureUI(
		root,
		UICaptureInput{
			Resolve: func(_, _ string, _ map[string]any) (UISourceSnapshot, error) { return UISourceSnapshot{}, denied },
		},
	)
	if !errors.Is(err, denied) || capture.Content.Type != "" {
		t.Fatalf("capture = %+v, error = %v; want no partial evidence and source error", capture, err)
	}
}
