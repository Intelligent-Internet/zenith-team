package wiki

import (
	"errors"
	"strings"
	"testing"
)

func TestSourceInputRuleSpans(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ tag, value, code, repair string }{
		{`<NumberInput label="Count" bind="count" min="invalid" />`, `"invalid"`, "input.range", "finite JSON number"},
		{`<Slider label="Count" bind="count" step=0 />`, "0", "input.range", "greater than zero"},
		{`<Select label="Choice" bind="choice" options=["a","a"] />`, `"a"`, "select.options", "distinct strings"},
		{`<DataTable label="Rows" columns=[{"key":"name","label":"Name","type":"invalid"}] />`, `"invalid"`, "table.column", "closed objects"},
		{`<DataTable label="Rows" columns=[] />`, `[]`, "table.columns", "1–20"},
		{`<Scope name="scenario" initial={"count":[]} />`, `[]`, "state.definition", "finite number"},
		{`<Scope name="scenario" initial={} sources={"tasks":{"type":"tasks","projectId":"invalid"}} />`, `"invalid"`, "source.project", "nonzero UUID"},
		{`<NumberInput label="Count" bind="count" min=10 max=5 />`, `5`, "input.range", "less than or equal"},
		{`<Slider label="Count" bind="count" min=101 />`, `101`, "input.range", "omitted bounds"},
		{`<Scope name="scenario" initial={"count":1} computed={"count":{"op":"literal","value":2}} />`, `{"count":{"op":"literal","value":2}}`, "state.duplicate", "distinct names"},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			source := "Résumé\n\n" + tc.tag
			_, err := compileCatalogFixture(source, 1)
			var diagnostic *SourceDiagnostic
			if !errors.As(err, &diagnostic) {
				t.Fatalf("expected source diagnostic, got %v", err)
			}
			start := strings.LastIndex(source, tc.value)
			if diagnostic.Code != tc.code || diagnostic.Start != start ||
				diagnostic.End != start+len(tc.value) ||
				!strings.Contains(diagnostic.Repair, tc.repair) {
				t.Fatalf(
					"diagnostic = %+v, want %s at %d:%d with repair %q",
					diagnostic,
					tc.code,
					start,
					start+len(tc.value),
					tc.repair,
				)
			}
		})
	}
}

func TestSourceDeclarationsEvaluate(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, computed, action string }{
		{
			"shorthand",
			`{{"total": state.price * state.quantity, "offset": 1}}`,
			`{{"type":"setState","name":"quantity","value":state.quantity + 1}}`,
		},
		{
			"mixed AST",
			`{{"total": state.price * state.quantity, "offset":{"op":"literal","value":1}}}`,
			`{{"type":"setState","name":"quantity","value":{"op":"add","args":[{"op":"state","name":"quantity"},{"op":"literal","value":1}]}}}`,
		},
		{
			"direct AST",
			`{"total":{"op":"mul","args":[{"op":"state","name":"price"},{"op":"state","name":"quantity"}]},"offset":{"op":"literal","value":1}}`,
			`{"type":"setState","name":"quantity","value":{"op":"add","args":[{"op":"state","name":"quantity"},{"op":"literal","value":1}]}}`,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			source := `<Scope name="pricing" initial={"price":10,"quantity":2} computed=` + tt.computed + `>
<Metric label="Total" value={state.total + state.offset} />
<Button label="Add one" action=` + tt.action + ` />
</Scope>`
			root, err := compileCatalogFixture(source, 1)
			if err != nil {
				t.Fatal(err)
			}
			scope := root.Content[0]
			env, err := UIInitialEnvironment(
				scope.Attrs["initial"].(map[string]any),
				scope.Attrs["computed"].(map[string]any),
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			metric, err := EvaluateUIExpression(scope.Content[0].Attrs["value"], env)
			if err != nil || metric != float64(21) {
				t.Fatalf("metric = %v, %v; want 21", metric, err)
			}
			action := scope.Content[1].Attrs["action"].(map[string]any)
			next, err := EvaluateUIExpression(action["value"], env)
			if err != nil || next != float64(3) {
				t.Fatalf("next quantity = %v, %v; want 3", next, err)
			}
		})
	}
}

func TestSourceDeclarationsRejectUnsafeInput(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, attribute string }{
		{"duplicate computed", `computed={{"total":1,"total":2}}`},
		{"unknown name", `computed={{"total":state.missing}}`},
		{"host call", `computed={{"total":fetch("https://example.com")}}`},
		{"trailing comma", `computed={{"total":1,}}`},
		{"reserved key", `computed={{"__proto__":1}}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := compileCatalogFixture(
				"<Scope name=\"pricing\" initial={} "+tt.attribute+">\nText\n</Scope>",
				1,
			)
			var diagnostic *SourceDiagnostic
			if !errors.As(err, &diagnostic) || diagnostic.Repair == "" {
				t.Fatalf("error = %v, want repair diagnostic", err)
			}
		})
	}
}

func TestSourcePropertyDiagnosticSpan(t *testing.T) {
	t.Parallel()
	source := "Résumé\n\n  <Grid columns=9>\nText\n</Grid>"
	_, err := compileCatalogFixture(source, 1)
	var diagnostic *SourceDiagnostic
	if !errors.As(err, &diagnostic) {
		t.Fatalf("error = %v, want diagnostic", err)
	}
	start := strings.Index(source, "9")
	if diagnostic.Code != "layout.columns" || diagnostic.Start != start || diagnostic.End != start+1 {
		t.Fatalf("diagnostic = %+v, want layout.columns at %d:%d", diagnostic, start, start+1)
	}
}

func TestSourceActionRejectsDynamicAuthority(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, action string }{
		{"dynamic target", `{{"type":"setState","name":state.target,"value":1}}`},
		{"dynamic URL", `{{"type":"navigate","href":state.target}}`},
		{"duplicate action field", `{{"type":"setState","name":"quantity","name":"quantity","value":1}}`},
		{"unregistered action", `{{"type":"fetch","value":1}}`},
		{"undeclared target", `{{"type":"setState","name":"missing","value":1}}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			source := `<Scope name="example" initial={"quantity":1,"target":"quantity"}>
<Button label="Change" action=` + tt.action + ` />
</Scope>`
			_, err := compileCatalogFixture(source, 1)
			var diagnostic *SourceDiagnostic
			if !errors.As(err, &diagnostic) || diagnostic.Repair == "" {
				t.Fatalf("error = %v, want located repair diagnostic", err)
			}
		})
	}
}
