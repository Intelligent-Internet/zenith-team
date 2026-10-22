package wiki

import (
	"errors"
	"testing"
)

func TestUIScopeRejectsCyclesAndMissingBindings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		root BlockNode
		code string
	}{
		{
			"missing state",
			uiTestScope(
				BlockNode{
					Type:  "uiMetric",
					Attrs: map[string]any{"label": "Count", "value": uiState("absent")},
				},
			),
			"state.undeclared",
		},
		{
			"missing source",
			uiTestScope(
				BlockNode{
					Type: "uiMetric",
					Attrs: map[string]any{
						"label": "Count",
						"value": map[string]any{"op": "data", "name": "tasks"},
					},
				},
			),
			"data.undeclared",
		},
		{
			"unbound item",
			uiTestScope(
				BlockNode{
					Type:  "uiValue",
					Attrs: map[string]any{"value": map[string]any{"op": "item"}},
				},
			),
			"item.undeclared",
		},
		{
			"wrong input type",
			uiTestScope(
				BlockNode{
					Type:  "uiCheckbox",
					Attrs: map[string]any{"label": "Enabled", "bind": "status"},
				},
			),
			"binding.type",
		},
		{
			"missing binding",
			uiTestScope(
				BlockNode{
					Type:  "uiTextInput",
					Attrs: map[string]any{"label": "Status", "bind": "absent"},
				},
			),
			"binding.undeclared",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateUIDocument(tt.root)
			var diagnostic *UIEvaluationError
			if !errors.As(err, &diagnostic) || diagnostic.Code != tt.code {
				t.Fatalf("got = %v, want %s", err, tt.code)
			}
		})
	}
	root := uiTestScope(BlockNode{Type: "paragraph"})
	root.Attrs["computed"] = map[string]any{"a": uiState("b"), "b": uiState("a")}
	err := ValidateUIDocument(root)
	var diagnostic *UIEvaluationError
	if !errors.As(err, &diagnostic) || diagnostic.Code != "state.cycle" {
		t.Fatalf("got = %v, want cycle rejection", err)
	}
	root.Attrs["computed"] = map[string]any{"status": uiLiteral("done")}
	if err = ValidateUIDocument(root); err == nil {
		t.Fatal("initial and computed declaration collided")
	}
}

func TestUIScopeLexicalShadowingAndMove(t *testing.T) {
	t.Parallel()
	metric := BlockNode{Type: "uiMetric", Attrs: map[string]any{"label": "Inner", "value": uiState("inner")}}
	inner := BlockNode{
		Type:    "uiScope",
		Attrs:   map[string]any{"name": "inner", "initial": map[string]any{"inner": float64(2)}},
		Content: []BlockNode{metric, {Type: "uiValue", Attrs: map[string]any{"value": uiState("status")}}},
	}
	root := uiTestScope(inner)
	if err := ValidateUIDocument(root); err != nil {
		t.Fatal(err)
	}
	root.Content = append(root.Content, metric)
	if err := ValidateUIDocument(root); err == nil {
		t.Fatal("moving a reference out of its declaration scope remained valid")
	}
	root = uiTestScope(
		BlockNode{
			Type:  "uiRepeat",
			Attrs: map[string]any{"items": uiLiteral([]any{"a"})},
			Content: []BlockNode{
				{Type: "uiValue", Attrs: map[string]any{"value": map[string]any{"op": "item"}}},
			},
		},
	)
	if err := ValidateUIDocument(root); err != nil {
		t.Fatal(err)
	}
}

func TestUIComputedDefaultsFollowDependencies(t *testing.T) {
	t.Parallel()
	env, err := UIInitialEnvironment(map[string]any{"price": float64(10)}, map[string]any{
		"total":    map[string]any{"op": "mul", "args": []any{uiState("price"), uiState("quantity")}},
		"quantity": uiLiteral(float64(3)),
	}, nil)
	if err != nil || env.State["total"] != float64(30) {
		t.Fatalf("environment = %+v, %v", env, err)
	}
}
