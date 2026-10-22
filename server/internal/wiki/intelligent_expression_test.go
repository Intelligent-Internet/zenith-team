package wiki

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

func expressionJSON(t *testing.T, source string) any {
	t.Helper()
	var expression any
	if err := json.Unmarshal([]byte(source), &expression); err != nil {
		t.Fatal(err)
	}
	return expression
}

func TestUIExpressionSemantics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, source string
		want         any
	}{
		{
			"arithmetic",
			`{"op":"add","args":[{"op":"state","name":"price"},{"op":"literal","value":2}]}`,
			float64(12),
		},
		{"missing field is null", `{"op":"field","input":{"op":"literal","value":{}},"field":"missing"}`, nil},
		{"null field is null", `{"op":"field","input":{"op":"literal","value":null},"field":"missing"}`, nil},
		{
			"coalesce",
			`{"op":"coalesce","args":[{"op":"literal","value":null},{"op":"literal","value":"fallback"}]}`,
			"fallback",
		},
		{
			"strict equality",
			`{"op":"eq","args":[{"op":"literal","value":2},{"op":"literal","value":"2"}]}`,
			false,
		},
		{
			"short circuit",
			`{"op":"and","args":[{"op":"literal","value":false},{"op":"data","name":"unavailable"}]}`,
			false,
		},
		{
			"conditional",
			`{"op":"if","args":[{"op":"literal","value":true},{"op":"literal","value":3},{"op":"div","args":[{"op":"literal","value":1},{"op":"literal","value":0}]}]}`,
			float64(3),
		},
		{"sum", `{"op":"sum","input":{"op":"literal","value":[1,2,3]}}`, float64(6)},
		{"empty aggregate", `{"op":"avg","input":{"op":"literal","value":[]}}`, nil},
		{"empty sum", `{"op":"sum","input":{"op":"literal","value":[]}}`, float64(0)},
		{"count", `{"op":"count","input":{"op":"data","name":"rows"}}`, float64(2)},
		{
			"map",
			`{"op":"map","input":{"op":"data","name":"rows"},"body":{"op":"field","input":{"op":"item"},"field":"value"}}`,
			[]any{float64(3), float64(1)},
		},
		{
			"filter",
			`{"op":"filter","input":{"op":"literal","value":[1,2,3]},"body":{"op":"gt","args":[{"op":"item"},{"op":"literal","value":1}]}}`,
			[]any{float64(2), float64(3)},
		},
		{
			"sort",
			`{"op":"sort","input":{"op":"literal","value":[3,null,1,2]},"body":{"op":"item"}}`,
			[]any{float64(1), float64(2), float64(3), nil},
		},
		{
			"descending null last",
			`{"op":"sort","input":{"op":"literal","value":[3,null,1,2]},"body":{"op":"item"},"descending":true}`,
			[]any{float64(3), float64(2), float64(1), nil},
		},
		{
			"take",
			`{"op":"take","args":[{"op":"literal","value":[1,2,3]},{"op":"literal","value":2}]}`,
			[]any{float64(1), float64(2)},
		},
		{
			"percent",
			`{"op":"format","input":{"op":"literal","value":0.125},"style":"percent","digits":1}`,
			"12.5%",
		},
		{
			"date offsets",
			`{"op":"dateDiff","args":[{"op":"literal","value":"2026-10-18T10:00:00+07:00"},{"op":"literal","value":"2026-10-18T02:00:00Z"}]}`,
			float64(3600),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := EvaluateUIExpression(
				expressionJSON(t, tt.source),
				UIEnvironment{
					State: map[string]any{"price": 10},
					Data: map[string]any{
						"rows": []any{map[string]any{"value": 3}, map[string]any{"value": 1}},
					},
				},
			)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got = %#v, %v; want %#v", got, err, tt.want)
			}
		})
	}
}

func TestUIExpressionRejectsUnsafeOrInvalidEvaluation(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, source, code string }{
		{"arbitrary op", `{"op":"eval","value":"alert(1)"}`, "expression.unknown"},
		{"unknown property", `{"op":"literal","value":1,"script":"private"}`, "expression.property"},
		{
			"prototype access",
			`{"op":"field","input":{"op":"literal","value":{}},"field":"__proto__"}`,
			"field.unsafe",
		},
		{"unsafe nested literal", `{"op":"literal","value":{"constructor":1}}`, "field.unsafe"},
		{"missing state", `{"op":"state","name":"absent"}`, "reference.missing"},
		{
			"wrong type",
			`{"op":"add","args":[{"op":"literal","value":"2"},{"op":"literal","value":3}]}`,
			"number.type",
		},
		{
			"division",
			`{"op":"div","args":[{"op":"literal","value":1},{"op":"literal","value":0}]}`,
			"number.zeroDivision",
		},
		{
			"overflow",
			`{"op":"mul","args":[{"op":"literal","value":1e308},{"op":"literal","value":1e308}]}`,
			"number.finite",
		},
		{
			"unselected invalid branch",
			`{"op":"if","args":[{"op":"literal","value":true},{"op":"literal","value":1},{"op":"evil"}]}`,
			"expression.unknown",
		},
		{"truthiness", `{"op":"not","args":[{"op":"literal","value":0}]}`, "boolean.type"},
		{
			"mixed sorting",
			`{"op":"sort","input":{"op":"literal","value":[1,"a"]},"body":{"op":"item"}}`,
			"comparison.type",
		},
		{
			"fractional take",
			`{"op":"take","args":[{"op":"literal","value":[1]},{"op":"literal","value":0.5}]}`,
			"take.type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := EvaluateUIExpression(expressionJSON(t, tt.source), UIEnvironment{})
			var diagnostic *UIEvaluationError
			if !errors.As(err, &diagnostic) || diagnostic.Code != tt.code {
				t.Fatalf("got = %v, want %s", err, tt.code)
			}
		})
	}
}

func TestUIExpressionBudgetsAndDependencies(t *testing.T) {
	t.Parallel()
	values := make([]any, UICollectionMaxItems+1)
	if _, err := EvaluateUIExpression(
		map[string]any{"op": "literal", "value": values},
		UIEnvironment{},
	); err == nil {
		t.Fatal("unbounded collection accepted")
	}
	if _, err := EvaluateUIExpression(
		map[string]any{"op": "literal", "value": math.Inf(1)},
		UIEnvironment{},
	); err == nil {
		t.Fatal("nonfinite literal accepted")
	}
	var nested any = map[string]any{"op": "literal", "value": true}
	for range UIExpressionMaxDepth + 1 {
		nested = map[string]any{"op": "not", "args": []any{nested}}
	}
	if err := ValidateUIExpression(nested); err == nil {
		t.Fatal("overdeep AST accepted")
	}
	expensive := expressionJSON(
		t,
		`{"op":"map","input":{"op":"data","name":"rows"},"body":{"op":"sum","input":{"op":"data","name":"rows"}}}`,
	)
	rows := make([]any, UICollectionMaxItems)
	for i := range rows {
		rows[i] = float64(i)
	}
	_, err := EvaluateUIExpression(expensive, UIEnvironment{Data: map[string]any{"rows": rows}})
	var diagnostic *UIEvaluationError
	if !errors.As(err, &diagnostic) || diagnostic.Code != "evaluation.budget" {
		t.Fatalf("got = %v, want budget rejection", err)
	}
	deps, err := UIExpressionDependencies(
		expressionJSON(
			t,
			`{"op":"if","args":[{"op":"state","name":"enabled"},{"op":"data","name":"tasks"},{"op":"literal","value":{"op":"state","name":"fake"}}]}`,
		),
	)
	if err != nil || !reflect.DeepEqual(deps, []string{"data.tasks", "state.enabled"}) {
		t.Fatalf("dependencies = %v, %v", deps, err)
	}
}

func TestUIStableSortDoesNotMutateSnapshot(t *testing.T) {
	t.Parallel()
	rows := []any{
		map[string]any{"id": "a", "rank": 1},
		map[string]any{"id": "b", "rank": 1},
		map[string]any{"id": "c", "rank": 0},
	}
	before, _ := json.Marshal(rows)
	got, err := EvaluateUIExpression(
		expressionJSON(
			t,
			`{"op":"sort","input":{"op":"data","name":"rows"},"body":{"op":"field","input":{"op":"item"},"field":"rank"}}`,
		),
		UIEnvironment{Data: map[string]any{"rows": rows}},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"id":"c","rank":0},{"id":"a","rank":1},{"id":"b","rank":1}]`
	raw, _ := json.Marshal(got)
	after, _ := json.Marshal(rows)
	if string(raw) != want || string(before) != string(after) {
		t.Fatalf("got = %s, original = %s", raw, after)
	}
}

func TestUIOutputExpansionIsBounded(t *testing.T) {
	t.Parallel()
	rows := make([]any, 1000)
	expression := map[string]any{
		"op":    "map",
		"input": map[string]any{"op": "data", "name": "rows"},
		"body":  map[string]any{"op": "literal", "value": strings.Repeat("x", 65536)},
	}
	_, err := EvaluateUIExpression(expression, UIEnvironment{Data: map[string]any{"rows": rows}})
	var diagnostic *UIEvaluationError
	if !errors.As(err, &diagnostic) || diagnostic.Code != "value.size" {
		t.Fatalf("got = %v, want output size rejection", err)
	}
}

func TestUIItemRequiresBinding(t *testing.T) {
	t.Parallel()
	expression := map[string]any{"op": "item"}
	if _, err := EvaluateUIExpression(expression, UIEnvironment{}); err == nil {
		t.Fatal("unbound item accepted")
	}
	got, err := EvaluateUIExpression(expression, UIEnvironment{HasItem: true, Item: nil})
	if err != nil || got != nil {
		t.Fatalf("bound null item = %v, %v", got, err)
	}
}

func TestUISharedExpressionFixtures(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/intelligent-expressions.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name        string
		Expression  any
		Environment UIEnvironment
		Value       any
		Code        string
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) < 50 {
		t.Fatal("shared expression fixture coverage is incomplete")
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			t.Parallel()
			before, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			got, err := EvaluateUIExpression(fixture.Expression, fixture.Environment)
			if fixture.Code != "" {
				var diagnostic *UIEvaluationError
				if !errors.As(err, &diagnostic) || diagnostic.Code != fixture.Code {
					t.Fatalf("got = %v, want %s", err, fixture.Code)
				}
			} else if err != nil || !reflect.DeepEqual(got, fixture.Value) {
				t.Fatalf("got = %#v, %v; want %#v", got, err, fixture.Value)
			}
			after, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("evaluation mutated its input")
			}
		})
	}
}

func TestUIValueBoundaryFixtures(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/intelligent-value-boundaries.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name       string
		Unit       string
		Repeat     int
		Rows       int
		LastRepeat *int
		Value      float64
		Code       string
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			rows := make([]any, fixture.Rows)
			for i := range rows {
				rows[i] = strings.Repeat(fixture.Unit, fixture.Repeat)
			}
			if fixture.LastRepeat != nil {
				rows[len(rows)-1] = strings.Repeat(fixture.Unit, *fixture.LastRepeat)
			}
			value, err := EvaluateUIExpression(
				map[string]any{"op": "count", "input": map[string]any{"op": "data", "name": "rows"}},
				UIEnvironment{State: map[string]any{}, Data: map[string]any{"rows": rows}},
			)
			if fixture.Code != "" {
				var diagnostic *UIEvaluationError
				if !errors.As(err, &diagnostic) || diagnostic.Code != fixture.Code {
					t.Fatalf("got %v, want %s", err, fixture.Code)
				}
			} else if err != nil || value != fixture.Value {
				t.Fatalf("got %v, %v; want %v", value, err, fixture.Value)
			}
		})
	}
}
